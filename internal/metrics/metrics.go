package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

// All metrics named per docs/design/v1alpha1.md. Constants for
// label names so a typo at a call site breaks compilation rather
// than silently producing a metric with an unintended label set.
const (
	LabelPolicyKind = "policy_kind"
	LabelPolicyName = "policy_name"
	LabelNamespace  = "namespace"
	LabelStatus     = "status"
	LabelKind       = "kind"
	LabelReason     = "reason"

	PolicyKindCluster    = "ClusterImagePolicy"
	PolicyKindNamespaced = "ImagePolicy"
)

// Counter / gauge / histogram instances. Exported so the call sites
// in webhook, controller, and certs can increment them directly.
var (
	// RewritesTotal counts every container image actually mutated
	// by the admission handler. The namespace label is the
	// admission namespace (where the Pod was admitted), not the
	// policy's own namespace: a ClusterImagePolicy match would
	// otherwise emit namespace="" and lose the location signal
	// operators alert on. The policy is already identified by
	// policy_kind + policy_name.
	RewritesTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "squirrel_rewrites_total",
			Help: "Total number of container image rewrites the webhook has performed. Labelled by policy + admission namespace.",
		},
		[]string{LabelPolicyKind, LabelPolicyName, LabelNamespace},
	)

	// SkipsTotal counts every container matched by a skip rule.
	// Same label set as RewritesTotal; namespace is the admission
	// namespace.
	SkipsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "squirrel_skips_total",
			Help: "Total number of container images the webhook left unchanged because a skip rule matched. Labelled by policy + admission namespace.",
		},
		[]string{LabelPolicyKind, LabelPolicyName, LabelNamespace},
	)

	// Policies is a per-policy gauge reflecting the Accepted status
	// condition. status="True" or "False"; one observation per
	// installed policy. ClusterImagePolicy entries use the empty
	// namespace label.
	Policies = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "squirrel_policies",
			Help: "1 for every installed policy with status.Accepted=<status>; 0 otherwise. Useful for alerting on Accepted=False.",
		},
		[]string{LabelKind, LabelPolicyName, LabelNamespace, LabelStatus},
	)

	// InvalidRulesTotal counts rules that fail reconcile-time
	// validation. Reason matches the api/v1alpha1.Reason* constants
	// (InvalidMatch, InvalidPlaceholder, MissingRegistry, ...).
	//
	// The counter is incremented once per reconcile per failing
	// rule, so a persistently-broken policy will keep adding to its
	// own series across reconciles (every Reconcile call sees the
	// same broken rule and bumps the counter again). This is by
	// design: Prometheus rate() over the counter then surfaces an
	// alertable signal "policy X is reconciling and still broken,"
	// rather than a flat gauge that would silently mask continued
	// breakage. Operators alerting on the absolute value should pin
	// on the per-policy Accepted gauge (squirrel_policies) instead.
	InvalidRulesTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "squirrel_invalid_rules_total",
			Help: "Total number of policy rules that failed reconcile-time validation. Increments once per reconcile per failing rule; alert on rate(), not value.",
		},
		[]string{LabelKind, LabelPolicyName, LabelNamespace, LabelReason},
	)

	// InvalidAdmissionRulesTotal counts rules whose match expression
	// failed to compile at admission time. This is the
	// defence-in-depth metric for the case where a rule slipped
	// past reconcile-time validation; the webhook skips the rule
	// and increments this counter.
	//
	// Unlabelled by design despite the per-rule symmetric counter
	// (InvalidRulesTotal) carrying full kind/name/namespace/reason
	// labels: ApplicableRules returns the per-rule failures as
	// pre-formatted error strings (policy kind + name embedded),
	// not as structured RuleSource values, so the call site has no
	// scalar handles to bind to labels. The associated log line
	// (see Handle's perRuleErrs loop) carries the policy detail;
	// alerts on this metric should rely on rate() to detect the
	// defence-in-depth path firing at all and route to the log for
	// triage. Adding labels here without a parallel refactor of
	// ApplicableRules' return shape would surface as cardinality
	// explosion or label-mismatch panics.
	InvalidAdmissionRulesTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "squirrel_invalid_admission_rules_total",
			Help: "Total number of policy rules the webhook resolver could not compile at admission time. See operator logs for the offending policy; alert on rate(), then triage via logs.",
		},
	)

	// UnparseableImagesTotal counts container images the webhook
	// could not parse as OCI references. failurePolicy=Ignore means
	// these admit unchanged; the metric is the only operational
	// signal.
	UnparseableImagesTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "squirrel_unparseable_images_total",
			Help: "Total number of container images the webhook could not parse as OCI references.",
		},
		[]string{LabelNamespace},
	)

	// InvalidTargetRendersTotal counts rewrite rules whose target
	// produced an invalid render at admission time (e.g. a template
	// placeholder rendered to an empty value that broke OCI validity).
	// Same namespace semantics as RewritesTotal: the admission
	// namespace, not the policy's own namespace.
	InvalidTargetRendersTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "squirrel_invalid_target_renders_total",
			Help: "Total number of rewrite rules whose target produced an invalid render at admission time. Labelled by policy + admission namespace.",
		},
		[]string{LabelPolicyKind, LabelPolicyName, LabelNamespace},
	)

	// UnoptedNamespaceSkipsTotal counts admissions short-circuited
	// because the target namespace lacks the
	// `squirrel.molier.dev/enabled=true` opt-in label. For "advisory"
	// deployments (default failurePolicy=Ignore, redirect-style
	// policies) this is a normal-mode outcome and the count is high.
	// For "enforcement" deployments (failurePolicy=Fail or a strict
	// opt-in posture) operators alert on rate>0 against namespaces
	// they expect to be enforced. The namespace label makes the
	// alert target a specific namespace rather than a cluster-wide
	// blanket count.
	UnoptedNamespaceSkipsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "squirrel_unopted_namespace_skips_total",
			Help: "Total number of admissions short-circuited because the target namespace lacks the squirrel.molier.dev/enabled=true opt-in label. Labelled by namespace.",
		},
		[]string{LabelNamespace},
	)

	// ReservedNamespaceSkipsTotal counts admissions short-circuited
	// by the reserved-namespace guard. The check is a defensive
	// complement to the MWC namespaceSelector; an operator who
	// expects the selector to keep admissions out of these
	// namespaces but sees this counter climbing has a
	// misconfiguration to chase (selector missing from the rendered
	// MWC, wrong OperatorNamespace, kustomize overlay drift). The
	// `reason` label distinguishes the two policy categories:
	// `system` (kube-system, kube-public, kube-node-lease) vs
	// `operator` (the operator's own namespace) so a runaway count
	// on one but not the other narrows the misconfiguration.
	ReservedNamespaceSkipsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "squirrel_reserved_namespace_skips_total",
			Help: "Total number of admissions short-circuited by the reserved-namespace guard (defence in depth for the MWC namespaceSelector). Labelled by reason (system vs operator).",
		},
		[]string{LabelReason},
	)

	// NamespaceLookupFailuresTotal counts transient errors reading
	// the admission namespace from the cache or apiserver. The
	// webhook admits unchanged on these failures (failurePolicy=Ignore
	// would do the same on an unreachable webhook); the counter is
	// the operational signal that something is amiss.
	NamespaceLookupFailuresTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "squirrel_namespace_lookup_failures_total",
			Help: "Total number of transient namespace lookup failures the webhook absorbed into an admit-unchanged outcome.",
		},
	)

	// FatalResolverFailuresTotal counts fatal errors from the
	// applicable-rules resolver (list failures). The webhook admits
	// unchanged on these too; this counter lets operators alert
	// when the operator is repeatedly unable to determine the full
	// policy set.
	FatalResolverFailuresTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "squirrel_fatal_resolver_failures_total",
			Help: "Total number of fatal errors from the webhook resolver that caused the handler to admit unchanged.",
		},
	)

	// WebhookAdmissionDurationSeconds tracks per-request handler
	// duration. No per-policy / per-namespace label to keep
	// cardinality bounded by the bucket count alone.
	WebhookAdmissionDurationSeconds = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "squirrel_webhook_admission_duration_seconds",
			Help:    "Per-request webhook handler duration in seconds.",
			Buckets: prometheus.DefBuckets,
		},
	)
)

func init() {
	ctrlmetrics.Registry.MustRegister(
		RewritesTotal,
		SkipsTotal,
		Policies,
		InvalidRulesTotal,
		InvalidAdmissionRulesTotal,
		UnparseableImagesTotal,
		InvalidTargetRendersTotal,
		ReservedNamespaceSkipsTotal,
		UnoptedNamespaceSkipsTotal,
		NamespaceLookupFailuresTotal,
		FatalResolverFailuresTotal,
		WebhookAdmissionDurationSeconds,
	)
}
