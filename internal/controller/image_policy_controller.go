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

// ImagePolicyReconciler reconciles namespaced ImagePolicy resources by
// validating spec and persisting the outcome through the Accepted
// status condition. The reconciler is read-only on every resource
// except the policy's own status subresource - it never patches the
// spec, pods, or other policy kinds.
//
// The RBAC permissions this reconciler needs are emitted from
// package-level +kubebuilder:rbac markers in doc.go; controller-gen's
// rbac generator only picks markers up at package scope, not from
// type comments.
type ImagePolicyReconciler struct {
	// Client is the controller-runtime client used to fetch the
	// policy and write its status subresource.
	Client client.Client

	// Recorder emits Kubernetes Events on every Accepted=False
	// outcome; see ClusterImagePolicyReconciler.Recorder for the
	// same rationale. nil is tolerated for unit tests that bypass
	// SetupManager.
	Recorder record.EventRecorder
}

// Reconcile implements reconcile.Reconciler. It is idempotent: when
// neither the spec generation nor the Accepted condition would change,
// it returns without issuing a status update so the reconciler does not
// trigger itself in a loop.
func (r *ImagePolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var policy squirrelv1alpha1.ImagePolicy
	if err := r.Client.Get(ctx, req.NamespacedName, &policy); err != nil {
		if apierrors.IsNotFound(err) {
			// Policy deleted: drop the per-policy gauge series so a
			// removed policy stops appearing in /metrics. See
			// deletePolicyMetrics for the matching cluster-scoped path.
			deletePolicyMetrics(metrics.PolicyKindNamespaced, req.Name, req.Namespace)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get ImagePolicy %q: %w", req.NamespacedName, err)
	}

	result := ValidateSpec(policy.Spec.Rules, policy.Spec.DefaultTarget, policy.Spec.DefaultAction)

	// Inert-policy warning: an ImagePolicy whose namespace lacks the
	// `squirrel.molier.dev/enabled=true` opt-in label is well-formed
	// (validation may pass) but the MWC namespaceSelector gates the
	// webhook on the label, so the policy never fires at admission
	// time. Surface this through the Accepted reason so operators
	// reading `kubectl describe imagepolicy` can immediately tell the
	// policy is inert and why. Lookup failure here is non-fatal: a
	// transient cache miss should not regress the reconciler from
	// Accepted=True to Accepted=False; the next reconcile will sample
	// the namespace again.
	if !result.HasErrors() {
		if optedIn, ok := r.namespaceOptedIn(ctx, policy.Namespace); ok && !optedIn {
			result.Warnings = append(result.Warnings, ValidationError{
				Reason:  squirrelv1alpha1.ReasonNamespaceNotOptedIn,
				Message: fmt.Sprintf("namespace %q lacks the %q=%q opt-in label; policy is well-formed but inert at admission time", policy.Namespace, squirrelv1alpha1.OptInLabel, "true"),
			})
		}
	}

	desired := AcceptedCondition(result, policy.Generation)

	recordPolicyMetricsGauge(metrics.PolicyKindNamespaced, &policy, result)
	if !applyAcceptedStatus(&policy.Status.ObservedGeneration, &policy.Status.Conditions, policy.Generation, desired) {
		// Mirror the cluster-scoped reconciler: emit Events and bump
		// the per-reason counter only when the state actually
		// transitions.
		return ctrl.Result{}, nil
	}
	recordPolicyMetricsErrors(metrics.PolicyKindNamespaced, &policy, result)
	r.recordValidationEvents(&policy, result)
	if err := r.Client.Status().Update(ctx, &policy); err != nil {
		return ctrl.Result{}, fmt.Errorf("update ImagePolicy %q status: %w", req.NamespacedName, err)
	}
	return ctrl.Result{}, nil
}

// namespaceOptedIn reports whether the named namespace carries the
// design's `squirrel.molier.dev/enabled=true` opt-in label. The
// second return is false when the lookup fails (cache miss, RBAC
// denial) - callers treat that as "don't change the policy state"
// rather than risk flipping Accepted to a stale value over a
// transient API blip.
func (r *ImagePolicyReconciler) namespaceOptedIn(ctx context.Context, namespace string) (bool, bool) {
	var ns corev1.Namespace
	if err := r.Client.Get(ctx, client.ObjectKey{Name: namespace}, &ns); err != nil {
		return false, false
	}
	return ns.Labels[squirrelv1alpha1.OptInLabel] == "true", true
}

// recordValidationEvents emits one Warning Event per validation
// error (and a single Normal "Accepted" Event when the policy is
// clean). See ClusterImagePolicyReconciler.recordValidationEvents
// for the rationale; this is the namespaced sibling.
func (r *ImagePolicyReconciler) recordValidationEvents(policy *squirrelv1alpha1.ImagePolicy, res ValidationResult) {
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
func (r *ImagePolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := ctrl.NewControllerManagedBy(mgr).
		For(&squirrelv1alpha1.ImagePolicy{}).
		WithEventFilter(predicate.GenerationChangedPredicate{}).
		Complete(r); err != nil {
		return fmt.Errorf("setup ImagePolicy reconciler: %w", err)
	}
	return nil
}
