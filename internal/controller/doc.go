// Package controller validates squirrel policies and surfaces the
// outcome through their status conditions.
//
// The package is split into two layers:
//
//   - Pure validation (validation.go and siblings): take a parsed
//     ClusterImagePolicySpec or ImagePolicySpec and return a
//     ValidationResult listing the hard errors and informational
//     warnings the design enumerates (EmptyRules, InvalidMatch,
//     InvalidPlaceholder, MissingRegistry, ActionTargetConflict,
//     PriorityIgnoredOnSkip, InvalidNamespaceSelector). This layer
//     has no Kubernetes-API dependencies and is exhaustively covered
//     by unit tests in the same package.
//
//   - Reconcilers (cluster_image_policy_controller.go and
//     image_policy_controller.go): controller-runtime reconcilers
//     that wrap the validation layer and persist the outcome to the
//     policy's status subresource. The webhook reads the resulting
//     Accepted condition (gated by observedGeneration) to decide
//     whether a policy's rules apply at admission time.
//
// Behaviour is exhaustively defined by the table-driven tests in this
// package; the design contract lives in docs/design/v1alpha1.md.
//
// controller-gen's rbac generator only honours markers at package
// scope, so the RBAC permissions every reconciler in this package
// needs are declared here rather than next to each reconciler type.
//
// +kubebuilder:rbac:groups=squirrel.molier.dev,resources=clusterimagepolicies,verbs=get;list;watch
// +kubebuilder:rbac:groups=squirrel.molier.dev,resources=clusterimagepolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=squirrel.molier.dev,resources=imagepolicies,verbs=get;list;watch
// +kubebuilder:rbac:groups=squirrel.molier.dev,resources=imagepolicies/status,verbs=get;update;patch
//
// The reconcilers emit Kubernetes Events on every Accepted=False
// outcome so operators see the rejection via `kubectl describe
// (cluster)imagepolicy`. The events-recorder RBAC is granted
// cluster-wide rather than scoped to the operator's namespace
// because the implementation surface is in flux: the legacy
// k8s.io/client-go/tools/record recorder writes core/v1 Events
// into whatever namespace the recorder is configured with - the
// caller chooses, not the cluster, so a cluster-scoped
// InvolvedObject does not pin the destination - while a future
// migration to the newer events.k8s.io/v1 API
// (k8s.io/client-go/tools/events) keeps the same EventTime-keyed
// data flow but is free to broaden where the records land. A
// cluster-wide create+patch on a low-trust resource is the path
// that survives either choice without forcing an RBAC churn at
// the migration point.
//
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
package controller
