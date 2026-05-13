package controller

import (
	client "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/RemkoMolier/squirrel/internal/metrics"
)

// recordPolicyMetricsGauge sets the per-policy squirrel_policies
// gauge from the validation result. The gauge is a 1/0 per
// (kind, name, namespace, status) pair, with both status="True"
// and status="False" series emitted so PromQL can sum over them
// without by() tricks. Safe to call on every reconcile - a gauge
// Set is idempotent and reflects the latest known truth.
//
// The (name, namespace) labels are derived from obj rather than
// taken as separate arguments: a caller cannot silently mismatch
// the policy and its label values. ClusterImagePolicy returns
// the empty namespace string from GetNamespace() by definition.
func recordPolicyMetricsGauge(kind string, obj client.Object, res ValidationResult) {
	name := obj.GetName()
	namespace := obj.GetNamespace()
	switch {
	case res.HasErrors():
		metrics.Policies.WithLabelValues(kind, name, namespace, "True").Set(0)
		metrics.Policies.WithLabelValues(kind, name, namespace, "False").Set(1)
	default:
		metrics.Policies.WithLabelValues(kind, name, namespace, "True").Set(1)
		metrics.Policies.WithLabelValues(kind, name, namespace, "False").Set(0)
	}
}

// recordPolicyMetricsErrors increments squirrel_invalid_rules_total
// once per validation error. The reconciler must only call this
// when it has detected an actual state transition (the
// applyAcceptedStatus short-circuit returned true): a manager
// restart or periodic resync would otherwise pollute the rate()
// signal operators alert on with phantom per-restart spikes.
func recordPolicyMetricsErrors(kind string, obj client.Object, res ValidationResult) {
	name := obj.GetName()
	namespace := obj.GetNamespace()
	for _, e := range res.Errors {
		metrics.InvalidRulesTotal.WithLabelValues(kind, name, namespace, e.Reason).Inc()
	}
}

// deletePolicyMetrics removes both Accepted=True and Accepted=False
// gauge series for a (kind, name, namespace) tuple. The reconciler
// calls this on the NotFound path so a deleted policy stops appearing
// in /metrics. We leave the InvalidRulesTotal counter alone:
// counters are monotonic by contract and zeroing one mid-flight would
// produce a misleading rate spike for any operator alerting on it -
// the per-policy view returns once a same-name policy is recreated.
func deletePolicyMetrics(kind, name, namespace string) {
	metrics.Policies.DeleteLabelValues(kind, name, namespace, "True")
	metrics.Policies.DeleteLabelValues(kind, name, namespace, "False")
}
