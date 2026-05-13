package engine

import (
	"fmt"
	"sort"

	"github.com/RemkoMolier/squirrel/internal/imageref"
)

// Action selects what a rule does when its match expression matches a
// container image. The engine's vocabulary is intentionally narrower
// than the API's: the empty string indicates "no rule applied" in the
// returned Decision, while ActionRewrite and ActionSkip mirror the API
// enum and are the only values valid on a CompiledRule.
type Action string

const (
	// ActionRewrite produces a new image reference by rendering the
	// rule's Target against the input image.
	ActionRewrite Action = "rewrite"

	// ActionSkip matches the input image and terminates resolution for
	// the container; the image is unchanged.
	ActionSkip Action = "skip"
)

// CompiledRule is the engine's input shape. The webhook (Phase 5)
// builds one of these per applicable rule after the reconciler has
// accepted the parent policy:
//
//   - Match comes from MatchExpr.UnmarshalJSON / ParseMatchString.
//   - Action is the effective action (rule.action or spec.defaultAction).
//   - Target is the *merged* target (rule.target over spec.defaultTarget,
//     field-by-field, with the design's passthrough defaults applied).
//     It is empty for ActionSkip rules.
//   - Priority is the effective priority (rule.priority override or the
//     match's specificity).
//   - Source identifies the originating policy + rule index; the engine
//     uses it for tie-breaking and the webhook uses it for the rewrite
//     annotation and metrics labels.
type CompiledRule struct {
	Match    imageref.Match
	Action   Action
	Target   Target
	Priority int32
	Source   RuleSource
}

// Target is the engine's view of a rewrite target. Each field is a
// template string consumed by internal/imageref. Tags is the ordered
// list of tag candidates; the rewrite action uses the first that
// renders to a non-empty OCI-valid tag (see imageref.SelectTag).
//
// All three fields are subject to the design's passthrough defaults
// when empty: Registry empty makes the rule non-applicable (the
// reconciler should have rejected this upstream); Repository empty
// expands to {repository}; Tags empty expands to ["{tag}"].
type Target struct {
	Registry   string
	Repository string
	Tags       []string
}

// RuleSource identifies the rule that produced a Decision. The engine
// uses Scope, Name, and RuleIndex for tie-breaking within the rewrite
// phase; the webhook uses every field for the per-pod rewrite
// annotation and Prometheus labels.
type RuleSource struct {
	// Scope is ScopeNamespaced for ImagePolicy rules and ScopeCluster
	// for ClusterImagePolicy rules. The two-phase resolver prefers
	// namespaced rules over cluster rules when priorities tie.
	Scope Scope

	// Name is the metadata.name of the parent policy resource.
	Name string

	// Namespace is the metadata.namespace of the parent policy resource.
	// Empty for cluster-scoped policies.
	Namespace string

	// RuleIndex is the rule's position within Spec.Rules (zero-based).
	RuleIndex int
}

// Scope distinguishes namespace-scoped rules from cluster-scoped ones
// for tie-breaking. The engine's vocabulary is closed; the webhook
// passes the right constant when compiling a rule from each kind.
type Scope string

const (
	// ScopeNamespaced flags a rule that originated from an ImagePolicy.
	// These rules beat ScopeCluster rules at equal priority.
	ScopeNamespaced Scope = "Namespaced"

	// ScopeCluster flags a rule that originated from a ClusterImagePolicy.
	ScopeCluster Scope = "Cluster"
)

// Decision is the outcome of resolving one container image. The
// webhook turns each Decision into either a JSON patch (when Action is
// Rewrite and RewrittenImage differs from the input) or a no-op
// (otherwise), plus a per-container
// original-image.squirrel.molier.dev/<containerName> annotation.
type Decision struct {
	// OriginalImage is the parsed input. Always set on a successful
	// Resolve call; zero-valued when the input failed to parse and
	// Resolve returned an error.
	OriginalImage imageref.Image

	// RewrittenImage is the assembled reference string the webhook
	// should write back into the container. When Action is empty (no
	// rule applied) or ActionSkip, RewrittenImage is the canonical
	// form of OriginalImage and the webhook leaves the spec untouched.
	RewrittenImage string

	// Action records which arm of resolution produced this Decision:
	//   - ActionRewrite: a rewrite rule matched and rendered a valid target.
	//   - ActionSkip:    a skip rule matched.
	//   - "":            no rule applied; image left unchanged.
	Action Action

	// Source identifies the rule that produced the decision. Zero
	// value when Action is "".
	Source RuleSource

	// InvalidRenders lists rewrite rules that matched the input image
	// but whose target rendered invalidly (empty registry, empty
	// repository after subform, no SelectTag candidate produced a
	// valid tag, or the assembled reference failed reparse). The
	// resolver treats such rules as non-applicable for the container
	// and continues to lower-priority rules; the webhook reads this
	// slice to emit one squirrel_invalid_target_renders_total
	// observation per matching-but-broken rule. The reconciler's
	// reconcile-time validation should have rejected most of these
	// upstream; entries here are the input-dependent residue (e.g.
	// `{repository:owner}` on a single-segment repo) that only
	// surfaces at admission time.
	InvalidRenders []RuleSource
}

// Resolve runs the two-phase resolution over rules for the given input
// image. The image is parsed and normalised through internal/imageref
// before matching; a parse failure returns the zero-valued Decision
// and the wrapped imageref error so the caller (the webhook) can
// admit the pod unchanged and emit the squirrel_unparseable_images_total
// metric per the design.
//
// The function is pure: same inputs always produce the same Decision.
// The webhook is free to invoke it concurrently across containers in a
// single Pod admission. For pods with multiple containers, callers can
// avoid resorting rules per container by calling Prepare once and
// passing the result to ResolvePrepared - Resolve itself does the
// sorting on every call.
func Resolve(image string, rules []CompiledRule) (Decision, error) {
	return ResolvePrepared(image, Prepare(rules))
}

// PreparedRules carries the two pre-sorted rule slices Resolve needs.
// Callers that resolve a single image use Resolve directly; callers
// that resolve a batch (e.g. a Pod's containers under a single
// admission) build a PreparedRules once and pass it to ResolvePrepared
// per image, amortising the sort cost over the batch.
type PreparedRules struct {
	skips    []CompiledRule
	rewrites []CompiledRule
}

// Prepare sorts rules into the two ordered slices Resolve consults.
// Sorting is the only state Resolve carries between containers in a
// Pod admission; running Prepare once per admission and reusing the
// result keeps admission latency O(n log n) per admission rather than
// O(n log n) per container as policy count grows.
func Prepare(rules []CompiledRule) PreparedRules {
	return PreparedRules{
		skips:    skipRulesSorted(rules),
		rewrites: rewriteRulesSorted(rules),
	}
}

// ResolvePrepared runs the two-phase resolution against a
// pre-sorted PreparedRules. Identical semantics to Resolve; only the
// per-call sort is elided.
func ResolvePrepared(image string, prepared PreparedRules) (Decision, error) {
	parsed, err := imageref.Parse(image)
	if err != nil {
		return Decision{}, fmt.Errorf("engine: %w", err)
	}

	// Phase 1: skip rules, sorted by the same Scope > Name > RuleIndex
	// tie-breaker as the rewrite phase (skip rules ignore priority).
	// The ordering is what makes the Decision.Source field deterministic
	// when multiple skip rules match the same image: without it the
	// webhook (Phase 5) would record arbitrary sources across
	// admissions, producing inconsistent
	// `original-image.squirrel.molier.dev/<containerName>` annotation
	// content and noisy diffs.
	for _, r := range prepared.skips {
		if r.Match.Matches(parsed) {
			return Decision{
				OriginalImage:  parsed,
				RewrittenImage: canonicalForm(parsed),
				Action:         ActionSkip,
				Source:         r.Source,
			}, nil
		}
	}

	// Phase 2: rewrite rules sorted by effective priority descending
	// with the design's tie-breaker chain (namespaced beats cluster,
	// then policy name ascending, then rule index ascending). The
	// first rule whose match expression matches and whose target
	// renders to a valid OCI image reference wins; rules that match
	// but render invalidly fall through per "rule is non-applicable
	// for this container".
	rewrites := prepared.rewrites
	var invalidRenders []RuleSource
	for _, r := range rewrites {
		if !r.Match.Matches(parsed) {
			continue
		}
		rendered, ok := renderTarget(r.Target, parsed)
		if !ok {
			// Rule matched but its target rendered to an invalid
			// OCI reference. Record the source so the webhook can
			// surface this as squirrel_invalid_target_renders_total
			// and fall through to the next-priority rule (the
			// design's "rule is non-applicable for this container"
			// path).
			invalidRenders = append(invalidRenders, r.Source)
			continue
		}
		return Decision{
			OriginalImage:  parsed,
			RewrittenImage: rendered,
			Action:         ActionRewrite,
			Source:         r.Source,
			InvalidRenders: invalidRenders,
		}, nil
	}

	return Decision{
		OriginalImage:  parsed,
		RewrittenImage: canonicalForm(parsed),
		InvalidRenders: invalidRenders,
	}, nil
}

// renderTarget renders a Target against the input image and assembles
// the result into a canonical image-reference string. The second
// return is false when the rule is non-applicable for this container:
//
//   - the target's Registry is empty (the reconciler should reject this
//     upstream; defence-in-depth here so the engine never produces a
//     malformed reference);
//   - a template references an unknown placeholder
//     (RenderField surfaces this; the reconciler should also have
//     rejected the rule);
//   - no Tags candidate renders to a non-empty OCI-valid tag
//     (SelectTag returns ok=false);
//   - the assembled reference fails imageref.Parse validation, e.g.
//     because a sub-form rendered to the empty string and produced a
//     trailing slash.
//
// On a non-applicable result the caller continues to the next rewrite
// rule per the design; only a successful render returns a Decision.
func renderTarget(target Target, original imageref.Image) (string, bool) {
	if target.Registry == "" {
		return "", false
	}

	registry, err := imageref.RenderField(target.Registry, original)
	if err != nil || registry == "" {
		return "", false
	}

	repositoryTemplate := target.Repository
	if repositoryTemplate == "" {
		repositoryTemplate = "{repository}"
	}
	repository, err := imageref.RenderField(repositoryTemplate, original)
	if err != nil || repository == "" {
		return "", false
	}

	tags := target.Tags
	if len(tags) == 0 {
		tags = []string{"{tag}"}
	}
	tag, ok := imageref.SelectTag(tags, original)
	if !ok {
		return "", false
	}

	assembled := registry + "/" + repository + ":" + tag
	if original.Digest != "" {
		assembled += "@" + original.Digest
	}

	parsed, err := imageref.Parse(assembled)
	if err != nil {
		return "", false
	}
	return canonicalForm(parsed), true
}

// skipRulesSorted returns the skip-action subset of rules sorted by
// the design's tie-breaker chain (minus priority, which is ignored on
// skip rules):
//
//  1. Source.Scope ascending so namespaced beats cluster.
//  2. Source.Name ascending lexicographically.
//  3. Source.RuleIndex ascending.
//
// The function does not mutate the input slice. Returning a sorted
// view rather than walking in slice order makes the Decision.Source
// recorded on a skip outcome deterministic across admissions even when
// the webhook builds the rule slice from map iteration.
func skipRulesSorted(rules []CompiledRule) []CompiledRule {
	out := make([]CompiledRule, 0, len(rules))
	for _, r := range rules {
		if r.Action == ActionSkip {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(a, b int) bool {
		ra, rb := out[a], out[b]
		if ra.Source.Scope != rb.Source.Scope {
			return ra.Source.Scope == ScopeNamespaced
		}
		if ra.Source.Name != rb.Source.Name {
			return ra.Source.Name < rb.Source.Name
		}
		return ra.Source.RuleIndex < rb.Source.RuleIndex
	})
	return out
}

// rewriteRulesSorted returns the rewrite-action subset of rules sorted
// by the design's order:
//
//  1. Effective priority, descending.
//  2. Source.Scope ascending so namespaced beats cluster.
//  3. Source.Name ascending lexicographically.
//  4. Source.RuleIndex ascending.
//
// The function does not mutate the input slice; the returned slice
// references the same CompiledRule values (no deep copy) and is safe
// to walk without further allocations per rule.
func rewriteRulesSorted(rules []CompiledRule) []CompiledRule {
	out := make([]CompiledRule, 0, len(rules))
	for _, r := range rules {
		if r.Action == ActionRewrite {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(a, b int) bool {
		ra, rb := out[a], out[b]
		if ra.Priority != rb.Priority {
			return ra.Priority > rb.Priority
		}
		// Namespaced beats cluster at equal priority.
		if ra.Source.Scope != rb.Source.Scope {
			return ra.Source.Scope == ScopeNamespaced
		}
		if ra.Source.Name != rb.Source.Name {
			return ra.Source.Name < rb.Source.Name
		}
		return ra.Source.RuleIndex < rb.Source.RuleIndex
	})
	return out
}

// canonicalForm returns the canonical string representation of an
// imageref.Image: <registry>/<repository>:<tag>@<digest>, omitting the
// tag or digest fields when they are empty.
func canonicalForm(img imageref.Image) string {
	out := img.Registry + "/" + img.Repository
	if img.Tag != "" {
		out += ":" + img.Tag
	}
	if img.Digest != "" {
		out += "@" + img.Digest
	}
	return out
}
