package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Condition types.
const (
	// ConditionAccepted is the single condition every policy carries.
	// It is True when the policy is well-formed and being applied at
	// admission time, False when the reconciler has rejected at least
	// one rule.
	ConditionAccepted = "Accepted"
)

// OptInLabel is the namespace label that gates whether squirrel
// applies its policies in a given namespace. The design's
// MutatingWebhookConfiguration namespaceSelector matches namespaces
// carrying this label set to "true". Defined on the API package so
// both the webhook handler (defence-in-depth re-check) and the
// reconcilers (inert-policy warning) reference a single source of
// truth instead of duplicating the literal.
const OptInLabel = "squirrel.molier.dev/enabled"

// Condition reasons. The full list mirrors the Validation section of
// docs/design/v1alpha1.md. Each is CamelCase per the apimachinery
// convention.
const (
	// ReasonAccepted is set when every rule passed validation.
	ReasonAccepted = "Accepted"

	// ReasonEmptyRules is set when spec.rules is empty or absent.
	ReasonEmptyRules = "EmptyRules"

	// ReasonInvalidMatch is set when a rule's match field is malformed.
	// The condition message identifies the offending rule index.
	ReasonInvalidMatch = "InvalidMatch"

	// ReasonInvalidPlaceholder is set when a rule's target uses an
	// unknown placeholder or `{digest}` inside a tag template.
	ReasonInvalidPlaceholder = "InvalidPlaceholder"

	// ReasonMissingRegistry is set when a rule's effective target has
	// no registry (neither rule nor defaultTarget supplies one).
	ReasonMissingRegistry = "MissingRegistry"

	// ReasonActionTargetConflict is set when a rule has `action: skip`
	// with a non-empty target, or sets both Target.Tag and Target.Tags.
	ReasonActionTargetConflict = "ActionTargetConflict"

	// ReasonInvalidAction is set when a rule's effective action is
	// neither `rewrite` nor `skip`. The credible trigger is
	// forward-compatibility during a rolling upgrade: a newer
	// operator version may add a third action value to the Go enum,
	// and policies that adopt it can reach the reconciler before
	// every replica has rolled to the new build. Surfacing the
	// mismatch as an InvalidAction condition is far easier to debug
	// than a silently-skipped rule. The CRD's
	// +kubebuilder:validation:Enum marker keeps this an exceptional
	// path - the API server rejects unknown values at admission time
	// on every normal write.
	ReasonInvalidAction = "InvalidAction"

	// ReasonPriorityIgnoredOnSkip is an informational reason set
	// alongside Accepted=True when a skip rule has an explicit priority
	// override (the value is unused because skip rules run
	// unconditionally before any rewrite rule).
	ReasonPriorityIgnoredOnSkip = "PriorityIgnoredOnSkip"

	// ReasonInvalidNamespaceSelector is set on a ClusterImagePolicy
	// whose spec.namespaceSelector fails to compile via
	// LabelSelectorAsSelector (e.g. an unsupported MatchExpressions
	// operator). The CRD schema does not catch these; without this
	// condition the webhook would silently skip the policy at
	// admission time, leaving operators with an Accepted policy whose
	// rules never run. Only ClusterImagePolicy uses this reason -
	// ImagePolicy has no namespaceSelector field.
	ReasonInvalidNamespaceSelector = "InvalidNamespaceSelector"

	// ReasonNamespaceNotOptedIn is an informational reason set
	// alongside Accepted=True on an ImagePolicy whose containing
	// namespace lacks the `squirrel.molier.dev/enabled=true` opt-in
	// label. The policy itself is well-formed (hence Accepted=True),
	// but it is inert at admission time because the
	// MutatingWebhookConfiguration's namespaceSelector gates on the
	// label. Surfacing this through the Accepted reason gives
	// operators reading `kubectl describe imagepolicy` an immediate
	// answer to "why isn't my policy firing?".
	ReasonNamespaceNotOptedIn = "NamespaceNotOptedIn"
)

// ClusterImagePolicyStatus reports the reconciler's observation of a
// ClusterImagePolicy.
type ClusterImagePolicyStatus struct {
	// ObservedGeneration is the spec generation the reconciler has
	// finished evaluating. Consumers compare this to metadata.generation
	// to determine whether the conditions reflect the latest spec.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions report the reconciler's evaluation of the policy. Today
	// the only condition type is "Accepted"; reasons follow the
	// Reason* constants defined in this package.
	// +listType=map
	// +listMapKey=type
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// ImagePolicyStatus reports the reconciler's observation of an
// ImagePolicy. Same shape as ClusterImagePolicyStatus.
type ImagePolicyStatus struct {
	// ObservedGeneration is the spec generation the reconciler has
	// finished evaluating.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions report the reconciler's evaluation of the policy.
	// +listType=map
	// +listMapKey=type
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}
