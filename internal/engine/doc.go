// Package engine resolves a single container-image string against a set
// of compiled rules drawn from the cluster's image policies.
//
// The engine is intentionally stateless and side-effect-free: the
// webhook (Phase 5) is responsible for assembling the applicable rules
// from the controller-runtime informer cache - filtering on namespace
// selectors, dropping policies whose Accepted condition is false,
// computing effective priorities, and resolving merged targets. The
// engine consumes the flat compiled-rule list, runs the two-phase
// resolution documented in docs/design/v1alpha1.md (skip first, then
// rewrite-by-priority), and returns a Decision describing what to do.
//
// Behaviour is exhaustively defined by the table-driven tests in this
// package.
package engine
