package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	client "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	squirrelv1alpha1 "github.com/RemkoMolier/squirrel/api/v1alpha1"
	"github.com/RemkoMolier/squirrel/internal/metrics"
)

// ClusterImagePolicyReconciler reconciles ClusterImagePolicy resources
// by validating spec and persisting the outcome through the Accepted
// status condition. The reconciler is read-only on every resource
// except the policy's own status subresource - it never patches the
// spec, pods, or other policy kinds.
//
// The RBAC permissions this reconciler needs are emitted from
// package-level +kubebuilder:rbac markers in doc.go; controller-gen's
// rbac generator only picks markers up at package scope, not from
// type comments.
type ClusterImagePolicyReconciler struct {
	// Client is the controller-runtime client used to fetch the
	// policy and write its status subresource.
	Client client.Client

	// Recorder emits Kubernetes Events on every Accepted=False
	// outcome so operators see the rejection via `kubectl describe
	// clusterimagepolicy` even if they never read the operator's
	// logs. SetupManager wires this from the manager's
	// EventRecorderFor; tests pass record.NewFakeRecorder.
	Recorder record.EventRecorder
}

// Reconcile implements reconcile.Reconciler. It is idempotent: when
// neither the spec generation nor the Accepted condition would change,
// it returns without issuing a status update so the reconciler does not
// trigger itself in a loop.
func (r *ClusterImagePolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var policy squirrelv1alpha1.ClusterImagePolicy
	if err := r.Client.Get(ctx, req.NamespacedName, &policy); err != nil {
		if apierrors.IsNotFound(err) {
			// Policy deleted between the watch event firing and this
			// reconcile. Drop the per-policy gauge series so a removed
			// policy stops appearing in /metrics until the next process
			// restart: the gauge is documented as one observation per
			// installed policy.
			deletePolicyMetrics(metrics.PolicyKindCluster, req.Name, "")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get ClusterImagePolicy %q: %w", req.NamespacedName, err)
	}

	result := ValidateSpec(policy.Spec.Rules, policy.Spec.DefaultTarget, policy.Spec.DefaultAction)
	// Namespace-selector validation is cluster-only (ImagePolicy has
	// no NamespaceSelector field). Prepending the findings rather
	// than appending puts the selector error first, so the Accepted
	// condition's Reason surfaces it in `kubectl describe` when the
	// selector is also the most visible thing operators wrote.
	if selErrs := ValidateNamespaceSelector(policy.Spec.NamespaceSelector); len(selErrs) > 0 {
		result.Errors = append(selErrs, result.Errors...)
	}
	desired := AcceptedCondition(result, policy.Generation)

	// Gauges always reflect the latest known truth, so squirrel_policies
	// is set unconditionally.
	recordPolicyMetricsGauge(metrics.PolicyKindCluster, &policy, result)
	if !applyAcceptedStatus(&policy.Status.ObservedGeneration, &policy.Status.Conditions, policy.Generation, desired) {
		// No state change since the last reconcile: a manager restart
		// or an unrelated requeue should not re-emit Warning Events
		// for the same broken rules nor double-count the per-error
		// counter. Both are side-effects that operators alert on; a
		// reconcile loop's natural requeue cadence would otherwise
		// pollute the signal.
		return ctrl.Result{}, nil
	}
	// State actually changed: bump the per-reason counter once per
	// transition and surface Events.
	recordPolicyMetricsErrors(metrics.PolicyKindCluster, &policy, result)
	r.recordValidationEvents(&policy, result)
	if err := r.Client.Status().Update(ctx, &policy); err != nil {
		return ctrl.Result{}, fmt.Errorf("update ClusterImagePolicy %q status: %w", req.NamespacedName, err)
	}
	return ctrl.Result{}, nil
}

// recordValidationEvents emits one Kubernetes Event per validation
// error (Warning) and a single Accepted Event (Normal) when the
// policy is clean. The Recorder is wired by SetupManager; nil
// is tolerated so unit tests that exercise Reconcile directly
// without an injected Recorder do not panic.
func (r *ClusterImagePolicyReconciler) recordValidationEvents(policy *squirrelv1alpha1.ClusterImagePolicy, res ValidationResult) {
	if r.Recorder == nil {
		return
	}
	if !res.HasErrors() {
		r.Recorder.Event(policy, corev1.EventTypeNormal, squirrelv1alpha1.ReasonAccepted, "policy accepted; rules are live at admission time")
		return
	}
	for _, e := range res.Errors {
		r.Recorder.Eventf(policy, corev1.EventTypeWarning, e.Reason, "%s", e.Message)
	}
}

// SetupWithManager registers the reconciler with the controller-runtime
// manager. The wiring lives in cmd/manager (Phase 7); separating it
// here keeps the reconciler self-contained and unit-testable without a
// manager.
//
// GenerationChangedPredicate elides the self-trigger event that
// follows every status write: status writes do not bump
// metadata.generation, so the predicate filters them out and the
// reconciler only re-runs when spec or metadata-modifying operations
// actually change the resource. applyAcceptedStatus remains as the
// belt-and-braces guard for any code path that bypasses the predicate.
//
// Concurrency: the reconciler relies on controller-runtime's default
// MaxConcurrentReconciles=1 because policy counts are bounded by the
// CRD's spec.rules MaxItems=128 cap and ValidateSpec is sub-
// millisecond for any plausible policy. A burst reconciliation
// triggered by a cluster boot processes the whole serialized queue
// in milliseconds, so introducing parallelism would just complicate
// the metric story (per-policy state changes are not commutative)
// without measurably reducing time-to-Accepted. If a future cluster
// shape grows policy counts significantly, add WithOptions(
// controller.Options{MaxConcurrentReconciles: N}) here and validate
// that the per-policy metric helpers stay correct under interleaving.
func (r *ClusterImagePolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := ctrl.NewControllerManagedBy(mgr).
		For(&squirrelv1alpha1.ClusterImagePolicy{}).
		WithEventFilter(predicate.GenerationChangedPredicate{}).
		Complete(r); err != nil {
		return fmt.Errorf("setup ClusterImagePolicy reconciler: %w", err)
	}
	return nil
}
