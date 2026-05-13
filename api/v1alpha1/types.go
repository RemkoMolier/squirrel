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
// Subsequent commits in Phase 2 populate this with namespaceSelector,
// defaultAction, defaultTarget, and rules. The struct is intentionally
// empty in this initial commit so the scheme registration and the
// surrounding generated DeepCopy can land first.
type ClusterImagePolicySpec struct{}

// ClusterImagePolicyStatus reports the reconciler's observation of a
// ClusterImagePolicy.
//
// Subsequent commits in Phase 2 populate this with conditions and the
// observedGeneration.
type ClusterImagePolicyStatus struct{}

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
// Subsequent commits in Phase 2 populate this with defaultAction,
// defaultTarget, and rules; ImagePolicy has no namespaceSelector since
// it is namespaced.
type ImagePolicySpec struct{}

// ImagePolicyStatus reports the reconciler's observation of an
// ImagePolicy. Same shape as ClusterImagePolicyStatus.
type ImagePolicyStatus struct{}
