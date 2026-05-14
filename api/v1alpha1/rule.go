package v1alpha1

import (
	"encoding/json"
	"errors"
	"fmt"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

// Action selects what a Rule does when its match expression matches a
// container image. The set of allowed values is closed to keep the
// validation surface small; future actions (e.g. cache pre-pull) extend
// this enum in a backwards-compatible release.
//
// +kubebuilder:validation:Enum=rewrite;skip
type Action string

const (
	// ActionRewrite (the default) applies the rule's target rendering to
	// produce a new image reference for the container.
	ActionRewrite Action = "rewrite"

	// ActionSkip matches the input and terminates resolution for this
	// container; the image is unchanged. Skip rules are always evaluated
	// before any rewrite rule, regardless of priority.
	ActionSkip Action = "skip"
)

// EffectiveAction resolves a rule's effective action per the design:
// the rule's own Action overrides the spec-level DefaultAction; an
// unset rule.Action combined with an unset DefaultAction falls back
// to ActionRewrite (the documented default). The reconciler's
// validation layer and the webhook's compile layer must agree on
// this rule; centralising it here keeps the two call sites in lockstep.
func EffectiveAction(ruleAction, defaultAction Action) Action {
	switch {
	case ruleAction != "":
		return ruleAction
	case defaultAction != "":
		return defaultAction
	default:
		return ActionRewrite
	}
}

// Rule is one entry in a policy's rules list.
//
// Resolution semantics are documented in docs/design/v1alpha1.md and
// implemented in internal/engine. Briefly:
//
//   - A skip rule whose Match matches an image terminates resolution
//     for that container.
//
//   - Otherwise, rewrite rules are sorted by effective priority (the
//     Priority override if set, else the match-glob's specificity score
//     from internal/imageref) and the first one whose Match matches
//     wins for that container.
type Rule struct {
	// Match is the match expression for this rule, stored as raw JSON.
	// Either a glob string (`"docker.io/**:*"`) or a structured object
	// with the per-field globs from the Match type. Callers parse the
	// payload via Rule.MatchExpression().
	//
	// Why raw JSON: the CRD schema for this field is schemaless to
	// accommodate the string-or-object union (the API server has no way
	// to validate a structural shape across both types). Storing the
	// field as apiextensionsv1.JSON means the Go decoder never fails on
	// a malformed payload, which is essential - if the decoder errored,
	// a bad policy would poison every typed informer's list/watch and
	// the reconciler could not even read it to set Accepted=False with
	// reason InvalidMatch. With raw JSON the policy lands in etcd, the
	// informer hands it to the reconciler, and the reconciler surfaces
	// the parse error through status conditions as designed.
	//
	// We deliberately do *not* attach a +kubebuilder:validation:XValidation
	// CEL rule to this field. Kubernetes static CRD validation refuses to
	// construct CEL type information for rules under a field that has no
	// OpenAPI type (the Schemaless + XPreserveUnknownFields pairing) and
	// the apiextensions validator rejects the CRD itself before resource
	// admission. All match-shape validation happens in
	// MatchExpression() and is surfaced via the Accepted=False /
	// InvalidMatch condition instead.
	//
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:validation:XPreserveUnknownFields
	Match apiextensionsv1.JSON `json:"match"`

	// Action is the action to take when this rule matches. Optional;
	// inherits Spec.DefaultAction (which defaults to "rewrite") when
	// unset.
	// +optional
	Action Action `json:"action,omitempty"`

	// Target is the rewrite target for this rule. Meaningful only when
	// the effective action is "rewrite"; merged field-by-field over
	// Spec.DefaultTarget. The reconciler rejects rules that set Target
	// on an effective-skip action.
	// +optional
	Target *Target `json:"target,omitempty"`

	// Priority overrides the default specificity-based priority of this
	// rule. Higher values win. The default (when Priority is nil) is the
	// byte length of the match-glob's normalised form, so longer, more
	// literal globs naturally apply first. Skip rules ignore this field
	// because they run unconditionally before any rewrite rule.
	// +optional
	Priority *int32 `json:"priority,omitempty"`
}

// MatchExpression parses the raw Match payload into its typed MatchExpr
// form, surfacing decode errors (unknown structured fields, scalar
// values, malformed JSON) to the caller. The reconciler uses this to
// set the Accepted=False / InvalidMatch condition; the engine uses it
// to skip rules whose match cannot be parsed.
//
// An empty payload (Match.Raw nil or zero-length) is treated as a
// missing required field and returns an error.
func (r *Rule) MatchExpression() (MatchExpr, error) {
	if len(r.Match.Raw) == 0 {
		return MatchExpr{}, errors.New("match: payload is empty")
	}
	var m MatchExpr
	if err := json.Unmarshal(r.Match.Raw, &m); err != nil {
		return MatchExpr{}, fmt.Errorf("match: %w", err)
	}
	return m, nil
}
