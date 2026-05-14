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
//   - Setup helpers (forthcoming setup.go): per-concern wiring
//     functions (setupReconcilers, setupCertSource, setupWebhook,
//     setupRotation) the orchestrating SetupManager composes. Each
//     helper takes the controller-runtime Manager plus the relevant
//     Options fields and is independently testable.
//
//   - Run entry point (forthcoming run.go): the function the
//     cmd/manager shim calls. Builds the Manager from the Options,
//     hands it to SetupManager, and starts everything.
//
// The cmd/manager package is a thin shim that parses flags and
// invokes Run; all the testable logic lives here.
package manager
