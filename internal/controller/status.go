package controller

import (
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// applyAcceptedStatus stages the desired Accepted condition and the
// observed generation onto a policy's status subresource and reports
// whether anything actually changed. The reconciler returns early when
// the answer is false, to avoid triggering its own informer with a
// write that would be a no-op on the apiserver side.
//
// The function wraps meta.SetStatusCondition - which already diffs
// Status / Reason / Message / ObservedGeneration on the existing
// condition and handles the LastTransitionTime semantics for genuine
// status flips - and adds the observedGeneration comparison the
// condition itself does not cover. Two callers (the cluster-scoped
// and namespaced reconcilers) share this helper rather than each
// duplicating a field-by-field comparison alongside SetStatusCondition.
//
// The observedGeneration assignment runs unconditionally. That is
// safe because of an invariant the helper depends on but does not
// itself enforce: AcceptedCondition (in condition.go) sets
// desired.ObservedGeneration = policyGeneration on every call. With
// that contract, whenever policyGeneration differs from
// *observedGeneration, the existing stored condition's
// ObservedGeneration is guaranteed stale and SetStatusCondition
// reports condChanged=true. The helper therefore never returns the
// (genChanged && !condChanged) combination that would otherwise
// leak a one-sided ObservedGeneration mutation back to the caller
// without a matching status write.
//
// CONTRACT: if AcceptedCondition ever stops stamping
// ObservedGeneration on the returned condition, this helper must be
// updated to either stage the spec.ObservedGeneration write only
// when condChanged is true, or to return (genChanged || condChanged)
// against a different invariant. Tests covering the genChanged path
// pin the current behaviour - a regression there would surface there
// before it shipped.
func applyAcceptedStatus(
	observedGeneration *int64,
	conditions *[]metav1.Condition,
	policyGeneration int64,
	desired metav1.Condition,
) bool {
	genChanged := *observedGeneration != policyGeneration
	*observedGeneration = policyGeneration
	condChanged := meta.SetStatusCondition(conditions, desired)
	return genChanged || condChanged
}
