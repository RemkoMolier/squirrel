// Package manager wires the squirrel operator's component packages
// (api, internal/controller, internal/webhook, internal/certs) into
// a runnable controller-runtime manager.
//
// The package is split into layers, each independently unit-testable:
//
//   - Options (options.go): the value type the rest of the package
//     consumes. ParseOptions reads command-line flags and the
//     POD_NAMESPACE downward-API environment variable; Validate
//     enforces cross-field invariants and applies the design's
//     defaults. The wiring layer never inspects flags or environment
//     directly so the helpers can be exercised against any value of
//     Options in tests.
//
//   - Setup helpers (setup.go): per-concern wiring functions
//     (setupReconcilers, NewCertSource, setupWebhook,
//     setupCertRunnables) the orchestrating SetupManager composes.
//     Each helper takes the controller-runtime Manager plus the
//     relevant Options fields and is independently testable.
//
//   - Run entry point (run.go): the function the cmd/manager shim
//     calls. Builds the Manager from the Options, bootstraps the
//     cert material synchronously (before mgr.Start, so the webhook
//     server finds tls.crt/tls.key on disk when it boots), hands the
//     Manager to SetupManager, and starts everything.
//
// The cmd/manager package is a thin shim that parses flags and
// invokes Run; all the testable logic lives here.
//
// controller-gen's rbac generator only honours markers at package
// scope. The operator runs under controller-runtime leader election
// when --leader-elect=true, which uses a coordination.k8s.io Lease
// in the manager's namespace; the verbs needed for that lease live
// here rather than in a reconciler-specific package because the
// dependency is on the manager's machinery, not on any one
// reconciler.
//
// The marker is scoped to squirrel-system via namespace= so the
// generated Role lives alongside the operator's own ServiceAccount
// (Roles in K8s are themselves namespaced; controller-gen emits a
// Role + RoleBinding pair for the namespaced marker). A
// cluster-wide ClusterRole grant on Leases would let the operator
// elect leadership in any namespace - operationally meaningless
// and a least-privilege violation.
//
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,namespace=squirrel-system,verbs=get;list;watch;create;update;patch;delete
package manager
