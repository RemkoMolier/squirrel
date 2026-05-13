package engine

import (
	"fmt"

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
// (otherwise), plus an entry in the squirrel.molier.dev/rewrites
// annotation.
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
// single Pod admission.
func Resolve(image string, rules []CompiledRule) (Decision, error) {
	parsed, err := imageref.Parse(image)
	if err != nil {
		return Decision{}, fmt.Errorf("engine: %w", err)
	}

	// Phase 1: skip rules. Walk in any order; the first match
	// terminates resolution with the image unchanged.
	for _, r := range rules {
		if r.Action != ActionSkip {
			continue
		}
		if r.Match.Matches(parsed) {
			return Decision{
				OriginalImage:  parsed,
				RewrittenImage: canonicalForm(parsed),
				Action:         ActionSkip,
				Source:         r.Source,
			}, nil
		}
	}

	// Phase 2: rewrite rules. Not yet implemented; subsequent commits
	// in this phase wire up priority-ordered iteration, target
	// rendering, and the non-applicable fallthrough.

	return Decision{
		OriginalImage:  parsed,
		RewrittenImage: canonicalForm(parsed),
	}, nil
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
