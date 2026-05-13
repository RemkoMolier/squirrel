package controller

import (
	"errors"
	"fmt"
	"regexp"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	squirrelv1alpha1 "github.com/RemkoMolier/squirrel/api/v1alpha1"
	"github.com/RemkoMolier/squirrel/internal/imageref"
)

// errMatchNotPopulated is the sentinel returned by matchFields when
// MatchExpression succeeded but neither the string nor the structured
// form was populated. The caller surfaces it as an InvalidMatch
// finding with the rule index in the message.
var errMatchNotPopulated = errors.New("match: neither string nor structured form was populated")

// SpecRuleIndex is the sentinel ValidationError.RuleIndex used for
// errors that apply to the spec as a whole (e.g. EmptyRules,
// defaultTarget HasBothTagForms) rather than to any single rule.
const SpecRuleIndex = -1

// ValidationError describes one validation failure or informational
// finding. RuleIndex is the zero-based index into spec.rules; the
// SpecRuleIndex sentinel indicates a spec-level finding.
type ValidationError struct {
	// RuleIndex is the offending rule's position in spec.rules, or
	// SpecRuleIndex for spec-level findings.
	RuleIndex int

	// Reason matches one of the apiv1alpha1.Reason* constants. The
	// reconciler uses this verbatim on the Accepted status condition.
	Reason string

	// Message is a human-readable explanation suitable for the
	// status-condition Message field and `kubectl describe` output.
	Message string
}

// Error implements the error interface so a ValidationError can be
// returned and wrapped as an ordinary Go error.
func (e ValidationError) Error() string {
	if e.RuleIndex == SpecRuleIndex {
		return fmt.Sprintf("spec: %s: %s", e.Reason, e.Message)
	}
	return fmt.Sprintf("rule[%d]: %s: %s", e.RuleIndex, e.Reason, e.Message)
}

// ValidationResult is the outcome of validating a policy spec.
//
//   - Errors are hard failures: the reconciler sets Accepted=False with
//     the reason of the first error and lists every error in the
//     message. The webhook then skips every rule in the policy.
//   - Warnings are informational findings (today, only
//     PriorityIgnoredOnSkip): the policy remains Accepted=True; the
//     reconciler may surface warnings via events or by setting the
//     condition's reason when Errors is empty.
//
// The Errors slice is in reconciler-defined order. ValidateSpec
// collects rule errors in spec.rules order; the cluster reconciler
// prepends ValidateNamespaceSelector findings so the Accepted
// condition's Reason surfaces the selector failure first (operators
// reading `kubectl describe` see the most-visible piece of spec
// they wrote first).
type ValidationResult struct {
	Errors   []ValidationError
	Warnings []ValidationError
}

// HasErrors reports whether the result contains any hard validation
// failures.
func (r ValidationResult) HasErrors() bool {
	return len(r.Errors) > 0
}

// HasWarnings reports whether the result contains any informational
// findings.
func (r ValidationResult) HasWarnings() bool {
	return len(r.Warnings) > 0
}

// ValidateNamespaceSelector reports any validation failures from the
// given LabelSelector. Only the ClusterImagePolicyReconciler calls
// this: ImagePolicy is namespaced and has no namespaceSelector. The
// CRD schema permits any structural LabelSelector value, including
// MatchExpressions with operators apimachinery does not support; the
// webhook then fails LabelSelectorAsSelector and silently skips the
// policy. Surfacing the failure as an Accepted=False condition lets
// operators see the problem in `kubectl describe` rather than chasing
// a policy that the CRD says is fine yet never applies.
//
// A nil selector is valid (matches everything); the function returns
// an empty slice in that case. A non-nil selector is round-tripped
// through metav1.LabelSelectorAsSelector to surface every category of
// failure apimachinery checks, including unsupported operators,
// invalid label keys, and malformed values.
func ValidateNamespaceSelector(sel *metav1.LabelSelector) []ValidationError {
	if sel == nil {
		return nil
	}
	if _, err := metav1.LabelSelectorAsSelector(sel); err != nil {
		return []ValidationError{{
			RuleIndex: SpecRuleIndex,
			Reason:    squirrelv1alpha1.ReasonInvalidNamespaceSelector,
			Message:   fmt.Sprintf("spec.namespaceSelector: %v", err),
		}}
	}
	return nil
}

// ValidateSpec validates a policy spec's rules and target merge.
//
// The function is pure: same inputs always produce the same result.
// Both ClusterImagePolicy and ImagePolicy reconcilers call this with
// the spec's Rules, DefaultTarget, and DefaultAction. The cluster
// reconciler also calls ValidateNamespaceSelector and merges its
// result into the same ValidationResult; ImagePolicy has no
// namespaceSelector field and so does not.
func ValidateSpec(
	rules []squirrelv1alpha1.Rule,
	defaultTarget *squirrelv1alpha1.Target,
	defaultAction squirrelv1alpha1.Action,
) ValidationResult {
	var result ValidationResult

	if len(rules) == 0 {
		result.Errors = append(result.Errors, ValidationError{
			RuleIndex: SpecRuleIndex,
			Reason:    squirrelv1alpha1.ReasonEmptyRules,
			Message:   "spec.rules must contain at least one entry",
		})
		return result
	}

	if defaultTarget != nil && defaultTarget.HasBothTagForms() {
		result.Errors = append(result.Errors, ValidationError{
			RuleIndex: SpecRuleIndex,
			Reason:    squirrelv1alpha1.ReasonActionTargetConflict,
			Message:   "spec.defaultTarget: both `tag` and `tags` are set; they are mutually exclusive",
		})
	}

	for i, r := range rules {
		validateRule(i, r, defaultTarget, defaultAction, &result)
	}

	return result
}

func validateRule(
	i int,
	r squirrelv1alpha1.Rule,
	defaultTarget *squirrelv1alpha1.Target,
	defaultAction squirrelv1alpha1.Action,
	result *ValidationResult,
) {
	mx, err := r.MatchExpression()
	if err != nil {
		result.Errors = append(result.Errors, ValidationError{
			RuleIndex: i,
			Reason:    squirrelv1alpha1.ReasonInvalidMatch,
			Message:   err.Error(),
		})
		return
	}

	// Both forms produce four glob strings (registry, repository, tag,
	// digest). We validate each one by compiling it through
	// imageref.ValidateGlob - the engine's matchGlob silently returns
	// false on a gobwas compile error, so without this check a
	// malformed structured match such as
	// `match: {repository: "["}` would land Accepted=True and
	// silently match nothing at admission time.
	fields, err := matchFields(mx)
	if err != nil {
		// Two cases collapsed into one error path:
		//   - ParseMatchString rejected a string-form match (missing
		//     registry/repository separator, empty field with a
		//     trailing delimiter, etc.); the parse error message
		//     identifies the issue.
		//   - MatchExpression succeeded but neither string nor
		//     structured form was populated; errMatchNotPopulated
		//     carries that explanation.
		result.Errors = append(result.Errors, ValidationError{
			RuleIndex: i,
			Reason:    squirrelv1alpha1.ReasonInvalidMatch,
			Message:   err.Error(),
		})
		return
	}
	globOK := true
	for _, f := range fields {
		if f.value == "" {
			continue
		}
		if err := imageref.ValidateGlob(f.value); err != nil {
			globOK = false
			result.Errors = append(result.Errors, ValidationError{
				RuleIndex: i,
				Reason:    squirrelv1alpha1.ReasonInvalidMatch,
				Message:   fmt.Sprintf("rule.match.%s: %v", f.name, err),
			})
		}
		// Digest is content-addressed; partial-prefix matches like
		// "sha256:abc*" are almost certainly an operator error
		// (treating cryptographic hashes as if they had meaningful
		// structure beyond bit-for-bit equality). Reject every
		// digest value except empty, "*", or a literal
		// "<algorithm>:<hex>". CRD-level validation can't catch
		// this because the Match payload is stored as raw JSON; this
		// is the reconciler-side counterpart.
		if f.name == "digest" && !isValidDigestMatch(f.value) {
			globOK = false
			result.Errors = append(result.Errors, ValidationError{
				RuleIndex: i,
				Reason:    squirrelv1alpha1.ReasonInvalidMatch,
				Message:   fmt.Sprintf("rule.match.digest: %q is not a valid digest match; expected empty, \"*\", or a literal \"<algorithm>:<hex>\"", f.value),
			})
		}
	}
	if !globOK {
		// Don't carry on into target validation when the match itself
		// is unusable - subsequent errors would name fields the user
		// can't reach until the match is fixed.
		return
	}

	effective := effectiveAction(r.Action, defaultAction)
	switch effective {
	case squirrelv1alpha1.ActionRewrite:
		validateRewriteRule(i, r, defaultTarget, result)
	case squirrelv1alpha1.ActionSkip:
		validateSkipRule(i, r, result)
	default:
		// The CRD's +kubebuilder:validation:Enum marker should reject
		// any other value at admission time; defence in depth here.
		result.Errors = append(result.Errors, ValidationError{
			RuleIndex: i,
			Reason:    squirrelv1alpha1.ReasonInvalidAction,
			Message:   fmt.Sprintf("unknown effective action %q; expected %q or %q", effective, squirrelv1alpha1.ActionRewrite, squirrelv1alpha1.ActionSkip),
		})
	}
}

// effectiveAction is kept as a package-local alias for the shared
// api/v1alpha1.EffectiveAction so the call site reads the same
// either way; centralising the resolution rule on the API type
// keeps the reconciler and webhook in lockstep.
func effectiveAction(ruleAction, defaultAction squirrelv1alpha1.Action) squirrelv1alpha1.Action {
	return squirrelv1alpha1.EffectiveAction(ruleAction, defaultAction)
}

func validateRewriteRule(
	i int,
	r squirrelv1alpha1.Rule,
	defaultTarget *squirrelv1alpha1.Target,
	result *ValidationResult,
) {
	if r.Target != nil && r.Target.HasBothTagForms() {
		// Classified as an Error rather than a Warning because the
		// operator's spec is internally inconsistent; the CRD's CEL
		// gate (api/v1alpha1/target.go) rejects this at apiserver
		// write, so the controller-level check is defensive only -
		// it fires when a legacy or out-of-band-applied object slips
		// past the CEL gate. We still continue rather than return
		// so co-occurring errors (MissingRegistry, InvalidPlaceholder)
		// surface in the same reconcile pass and operators see every
		// problem in one shot.
		result.Errors = append(result.Errors, ValidationError{
			RuleIndex: i,
			Reason:    squirrelv1alpha1.ReasonActionTargetConflict,
			Message:   "rule.target: both `tag` and `tags` are set; they are mutually exclusive",
		})
	}

	registry, repository, tags := mergeTarget(r.Target, defaultTarget)

	if registry == "" {
		result.Errors = append(result.Errors, ValidationError{
			RuleIndex: i,
			Reason:    squirrelv1alpha1.ReasonMissingRegistry,
			Message:   "effective target has no registry; either set rule.target.registry or spec.defaultTarget.registry",
		})
	} else if err := imageref.ValidateFieldTemplate(registry); err != nil {
		result.Errors = append(result.Errors, ValidationError{
			RuleIndex: i,
			Reason:    squirrelv1alpha1.ReasonInvalidPlaceholder,
			Message:   fmt.Sprintf("rule.target.registry: %v", err),
		})
	}
	if repository != "" {
		if err := imageref.ValidateFieldTemplate(repository); err != nil {
			result.Errors = append(result.Errors, ValidationError{
				RuleIndex: i,
				Reason:    squirrelv1alpha1.ReasonInvalidPlaceholder,
				Message:   fmt.Sprintf("rule.target.repository: %v", err),
			})
		}
	}
	for j, tmpl := range tags {
		if err := imageref.ValidateTagTemplate(tmpl); err != nil {
			result.Errors = append(result.Errors, ValidationError{
				RuleIndex: i,
				Reason:    squirrelv1alpha1.ReasonInvalidPlaceholder,
				Message:   fmt.Sprintf("rule.target.tags[%d]: %v", j, err),
			})
		}
	}
}

func validateSkipRule(i int, r squirrelv1alpha1.Rule, result *ValidationResult) {
	if r.Target != nil && !isEmptyTarget(*r.Target) {
		result.Errors = append(result.Errors, ValidationError{
			RuleIndex: i,
			Reason:    squirrelv1alpha1.ReasonActionTargetConflict,
			Message:   "skip rule has a non-empty target; skip rules must not specify a rewrite target",
		})
	}
	if r.Priority != nil {
		result.Warnings = append(result.Warnings, ValidationError{
			RuleIndex: i,
			Reason:    squirrelv1alpha1.ReasonPriorityIgnoredOnSkip,
			Message:   "skip rule has an explicit priority; the value is unused because skip rules run unconditionally before any rewrite rule",
		})
	}
}

// mergeTarget returns the effective Target fields after merging
// rule.target over defaultTarget field-by-field. The third return is
// the effective tag template list, with the Tag/Tags sugar already
// folded in via EffectiveTags().
func mergeTarget(rule, def *squirrelv1alpha1.Target) (registry, repository string, tags []string) {
	switch {
	case rule != nil && rule.Registry != "":
		registry = rule.Registry
	case def != nil:
		registry = def.Registry
	}

	switch {
	case rule != nil && rule.Repository != "":
		repository = rule.Repository
	case def != nil:
		repository = def.Repository
	}

	switch {
	case rule != nil && len(rule.EffectiveTags()) > 0:
		tags = rule.EffectiveTags()
	case def != nil:
		tags = def.EffectiveTags()
	}

	return registry, repository, tags
}

func isEmptyTarget(t squirrelv1alpha1.Target) bool {
	return t.Registry == "" && t.Repository == "" && t.Tag == "" && len(t.Tags) == 0
}

// validDigestMatchRe accepts the only digest forms the design
// recognises beyond empty and "*": a literal "<algorithm>:<hex>"
// where the algorithm is one or more lowercase letters/digits
// starting with a letter and the hex segment is at least one
// hexadecimal character. Partial-prefix patterns (trailing `*`,
// `?`, character classes, doublestars) are rejected. The check is
// case-insensitive on the hex segment because OCI canonical digests
// are lowercase but some toolchains emit uppercase.
var validDigestMatchRe = regexp.MustCompile(`^[a-z][a-z0-9]*:[A-Fa-f0-9]+$`)

// isValidDigestMatch is the reconciler-side counterpart to the
// missing CRD-level CEL gate on Match.Digest. See the matchFieldGlob
// loop in validateRule for the call site and rationale.
func isValidDigestMatch(v string) bool {
	if v == "" || v == "*" {
		return true
	}
	return validDigestMatchRe.MatchString(v)
}

// matchFieldGlob is the name/value pair the glob validator iterates
// over. The name appears in the resulting ValidationError message so
// operators can identify which Match field is malformed.
type matchFieldGlob struct {
	name  string
	value string
}

// matchFields returns the four glob strings inside a parsed MatchExpr,
// regardless of which form populated it. The string form is split via
// imageref.ParseMatchString; any parse error is returned so the caller
// surfaces it once instead of re-running ParseMatchString in its own
// validation path. The structured form is read directly and never
// produces a parse error.
//
// Possible outcomes:
//
//   - (fields, nil)                       - the rule's match is
//     well-formed; the caller
//     walks fields to validate
//     each glob.
//   - (nil,    ParseMatchString err)      - string form set but
//     failed structural parse;
//     caller surfaces it as
//     InvalidMatch.
//   - (nil,    errMatchNotPopulated)      - MatchExpression
//     succeeded but neither
//     form is populated;
//     caller surfaces it as
//     InvalidMatch.
func matchFields(mx squirrelv1alpha1.MatchExpr) ([]matchFieldGlob, error) {
	switch {
	case mx.String != "":
		parsed, err := imageref.ParseMatchString(mx.String)
		if err != nil {
			return nil, err
		}
		return []matchFieldGlob{
			{"registry", parsed.Registry},
			{"repository", parsed.Repository},
			{"tag", parsed.Tag},
			{"digest", parsed.Digest},
		}, nil
	case mx.Structured != nil:
		return []matchFieldGlob{
			{"registry", mx.Structured.Registry},
			{"repository", mx.Structured.Repository},
			{"tag", mx.Structured.Tag},
			{"digest", mx.Structured.Digest},
		}, nil
	}
	return nil, errMatchNotPopulated
}
