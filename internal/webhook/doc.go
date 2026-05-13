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
//     Patch plus the squirrel.molier.dev/rewrites annotation per the
//     design. Defensively re-checks the namespace's opt-in label
//     because controller-tools cannot put the corresponding
//     namespaceSelector on the generated MutatingWebhookConfiguration
//     (Phase 8 manifest overlays add the full design selector).
//
// Behaviour is exhaustively defined by the tests in this package;
// the design contract lives in docs/design/v1alpha1.md.
package webhook
