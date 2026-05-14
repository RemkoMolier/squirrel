// Package metrics declares and registers the Prometheus counters,
// gauges, and histograms the operator exposes on /metrics.
//
// All names follow the design's `squirrel_*` namespace convention
// and the cardinality is deliberately bounded:
//
//   - Per-policy labels (policy_kind, policy_name, namespace) are
//     bounded by the number of policies installed in the cluster
//     plus the small constant set of ClusterImagePolicy vs
//     ImagePolicy scopes. Operators with very large policy counts
//     can drop these labels in their Prometheus scrape config.
//   - The admission duration histogram has no per-policy or
//     per-namespace label, so cardinality is just the bucket count.
//   - The status / reason labels (Accepted vs InvalidMatch /
//     InvalidPlaceholder / ...) are bounded by the Reason*
//     constants in api/v1alpha1.
//
// Metrics live in their own package so the call sites in
// internal/webhook, internal/controller, and internal/certs can
// import them without dragging in the entire metrics-registration
// init function chain.
package metrics
