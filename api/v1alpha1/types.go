package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ClusterImagePolicy is a cluster-scoped policy that rewrites or skips
// container images at Pod admission time according to its rules.
//
// See docs/design/v1alpha1.md for the full semantics, including the
// `squirrel.molier.dev/enabled` namespace opt-in gate and the two-phase
// (skip-then-rewrite) resolution model.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=cip
// +kubebuilder:subresource:status
// +kubebuilder:storageversion
// +kubebuilder:printcolumn:name="Accepted",type=string,JSONPath=`.status.conditions[?(@.type=="Accepted")].status`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.conditions[?(@.type=="Accepted")].reason`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type ClusterImagePolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec is the desired state of this ClusterImagePolicy.
	Spec ClusterImagePolicySpec `json:"spec,omitempty"`
	// Status carries the most recent observation from the reconciler.
	Status ClusterImagePolicyStatus `json:"status,omitempty"`
}

// ClusterImagePolicyList contains a list of ClusterImagePolicy objects.
//
// +kubebuilder:object:root=true
type ClusterImagePolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	// Items is the list of ClusterImagePolicy objects in this response.
	Items []ClusterImagePolicy `json:"items"`
}

// ClusterImagePolicySpec is the desired state of a ClusterImagePolicy.
//
// Resolution semantics (skip-then-rewrite, priority-ordered) are
// documented in docs/design/v1alpha1.md and implemented in
// internal/engine.
type ClusterImagePolicySpec struct {
	// NamespaceSelector narrows the set of opted-in namespaces this
	// policy applies to. Nil or empty applies to every namespace that
	// carries the operator-wide `squirrel.molier.dev/enabled=true`
	// label; the MutatingWebhookConfiguration is the safety gate.
	// +optional
	NamespaceSelector *metav1.LabelSelector `json:"namespaceSelector,omitempty"`

	// DefaultAction is the action applied to rules that omit their own
	// Action. Defaults to "rewrite" when unset.
	// +optional
	DefaultAction Action `json:"defaultAction,omitempty"`

	// DefaultTarget is the rewrite target merged under any rule-level
	// target. The effective merged target (rule over default,
	// field-by-field) must end up with a Registry.
	// +optional
	DefaultTarget *Target `json:"defaultTarget,omitempty"`

	// Rules is the ordered list of rules. The list must have at least
	// one entry; the reconciler marks empty rule lists as
	// `Accepted=False` with reason `EmptyRules`. The MaxItems=128 cap
	// is set to keep the CRD's CEL cost estimator within K8s 1.33+'s
	// budget for per-rule XValidation (see api/v1alpha1/target.go).
	// In practice a policy with more than a few rules indicates a
	// structural design problem; 128 is generous.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=128
	Rules []Rule `json:"rules"`
}

// ImagePolicy is the namespaced sibling of ClusterImagePolicy. It applies
// only to Pods in its own namespace.
//
// ImagePolicy scopes (or overrides) rules WITHIN a namespace that has
// already opted in to squirrel via the squirrel.molier.dev/enabled=true
// label. Creating an ImagePolicy by itself does not opt the namespace
// in; without the label the webhook never runs against Pods in that
// namespace and the policy is inert. Treat the label as the gate and
// ImagePolicy as the per-namespace customisation that takes effect
// once the gate is open.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced,shortName=ip
// +kubebuilder:subresource:status
// +kubebuilder:storageversion
// +kubebuilder:printcolumn:name="Accepted",type=string,JSONPath=`.status.conditions[?(@.type=="Accepted")].status`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.conditions[?(@.type=="Accepted")].reason`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type ImagePolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec is the desired state of this ImagePolicy.
	Spec ImagePolicySpec `json:"spec,omitempty"`
	// Status carries the most recent observation from the reconciler.
	Status ImagePolicyStatus `json:"status,omitempty"`
}

// ImagePolicyList contains a list of ImagePolicy objects.
//
// +kubebuilder:object:root=true
type ImagePolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	// Items is the list of ImagePolicy objects in this response.
	Items []ImagePolicy `json:"items"`
}

// ImagePolicySpec is the desired state of an ImagePolicy.
//
// ImagePolicy is namespace-scoped, so it has no NamespaceSelector;
// rules in an ImagePolicy scope or override behaviour WITHIN a
// namespace that has already opted in via the
// squirrel.molier.dev/enabled=true label (see the ImagePolicy type
// doc). Creating an ImagePolicy by itself does not opt the namespace
// in - the label is the gate, the ImagePolicy is the per-namespace
// customisation that takes effect once the gate is open.
type ImagePolicySpec struct {
	// DefaultAction is the action applied to rules that omit their own
	// Action. Defaults to "rewrite" when unset.
	// +optional
	DefaultAction Action `json:"defaultAction,omitempty"`

	// DefaultTarget is the rewrite target merged under any rule-level
	// target. The effective merged target must end up with a Registry.
	// +optional
	DefaultTarget *Target `json:"defaultTarget,omitempty"`

	// Rules is the ordered list of rules. The list must have at least
	// one entry; the reconciler marks empty rule lists as
	// `Accepted=False` with reason `EmptyRules`. The MaxItems=128 cap
	// is set to keep the CRD's CEL cost estimator within K8s 1.33+'s
	// budget for per-rule XValidation (see api/v1alpha1/target.go).
	// In practice a policy with more than a few rules indicates a
	// structural design problem; 128 is generous.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=128
	Rules []Rule `json:"rules"`
}
