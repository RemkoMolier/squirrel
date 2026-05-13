// Package webhook is the mutating admission webhook that applies
// squirrel policies to Pod resources at admission time.
//
// The package is structured as three layers:
//
//   - Pure compile helpers (compile.go and siblings): convert an
//     api/v1alpha1.Rule plus its parent policy's spec into the
//     engine.CompiledRule shape the resolver consumes. The helpers
//     have no Kubernetes dependencies and are exhaustively
//     unit-tested.
//
//   - The applicable-rule resolver (resolve.go): reads
//     ClusterImagePolicy and ImagePolicy resources from the
//     controller-runtime cache, filters by Accepted condition (with
//     the observedGeneration gate) and namespaceSelector, and
//     produces the flat []CompiledRule the engine consumes. A list
//     failure surfaces as a fatal error so the caller can admit
//     unchanged rather than apply a partial policy set.
//
//   - The PodMutator (mutator.go): implements the controller-runtime
//     admission.Handler interface, runs the engine for every
//     container in CREATE and UPDATE admissions, and returns a JSON
//     Patch plus per-container
//     `original-image.squirrel.molier.dev/<containerName>` annotations
//     per the design. Defensively re-checks the namespace's opt-in label
//     because controller-tools cannot put the corresponding
//     namespaceSelector on the generated MutatingWebhookConfiguration
//     (Phase 8 manifest overlays add the full design selector).
//
// Behaviour is exhaustively defined by the tests in this package;
// the design contract lives in docs/design/v1alpha1.md.
//
// controller-gen's webhook and rbac generators only honour markers
// at package scope, so the MutatingWebhookConfiguration the handler
// is invoked from and the namespace RBAC the handler needs are
// declared here rather than next to the PodMutator type. The
// `namespaceSelector` field that the design specifies on the MWC is
// not expressible via this marker; Phase 8 manifest overlays add it
// to the generated configuration, and PodMutator.Handle defensively
// re-checks the per-namespace opt-in label.
//
// timeoutSeconds=5 caps the apiserver's wait per webhook call.
// failurePolicy=Ignore takes over after the timeout, so a wedged
// webhook only adds 5s of latency to each Pod admission before the
// apiserver continues without us - half of the kube-apiserver
// default of 10s, matching the design.
//
// failurePolicy choice. The marker defaults to Ignore because
// squirrel's headline use case is "advisory" rewriting (e.g. mirror
// docker.io through an internal proxy), where a temporary webhook
// unavailability translating to "admit unchanged" is operationally
// preferable to blocking Pod creation cluster-wide. Operators
// running squirrel as an *enforcement* layer ("only mirror images
// allowed") should flip the field to Fail via a kustomize overlay
// patch on the generated MutatingWebhookConfiguration: in that
// posture a DoS against the webhook must NOT silently bypass
// policy. The per-reason admission-short-circuit counters
// (squirrel_unopted_namespace_skips_total,
// squirrel_reserved_namespace_skips_total,
// squirrel_namespace_lookup_failures_total,
// squirrel_fatal_resolver_failures_total) let enforcement-mode
// operators alert when any silent-admit path fires.
//
// +kubebuilder:webhook:path=/mutate-pod-v1,mutating=true,failurePolicy=ignore,sideEffects=None,groups="",resources=pods;pods/ephemeralcontainers,verbs=create;update,versions=v1,name=mutate-pods.squirrel.molier.dev,admissionReviewVersions=v1,reinvocationPolicy=IfNeeded,timeoutSeconds=5
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
package webhook
