package controller

import (
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	squirrelv1alpha1 "github.com/RemkoMolier/squirrel/api/v1alpha1"
)

// AcceptedCondition builds the Accepted status condition from a
// validation result. The mapping is:
//
//   - Hard errors: Status=False, Reason from the first error, Message
//     lists every error in the order ValidateSpec collected them. The
//     webhook reads the False status and skips every rule in the
//     policy until the policy is fixed.
//   - No errors, one or more warnings: Status=True, Reason from the
//     first warning, Message lists every warning so operators see the
//     informational finding via `kubectl describe`.
//   - No errors, no warnings: Status=True, Reason=Accepted, Message
//     "all rules pass validation".
//
// observedGeneration is the spec generation the reconciler is acting
// on; the caller passes metadata.generation so consumers can compare
// it against status.observedGeneration to decide whether the condition
// reflects the latest spec.
//
// CONTRACT: this function must stamp ObservedGeneration onto every
// returned condition. applyAcceptedStatus depends on the invariant
// "generation change implies condition change" - it sets the
// spec.observedGeneration field unconditionally and relies on
// SetStatusCondition's per-field diff (including ObservedGeneration)
// to flip its changed flag. A regression that dropped the
// ObservedGeneration assignment here would leave applyAcceptedStatus
// issuing one-sided spec writes that observe no condition change,
// producing API calls that look like updates but are functionally
// no-ops. The pinning test is
// TestApplyAcceptedStatusGenChangeImpliesCondChange.
func AcceptedCondition(res ValidationResult, observedGeneration int64) metav1.Condition {
	cond := metav1.Condition{
		Type:               squirrelv1alpha1.ConditionAccepted,
		ObservedGeneration: observedGeneration,
	}
	switch {
	case res.HasErrors():
		cond.Status = metav1.ConditionFalse
		cond.Reason = res.Errors[0].Reason
		cond.Message = joinFindings(res.Errors)
	case res.HasWarnings():
		cond.Status = metav1.ConditionTrue
		cond.Reason = res.Warnings[0].Reason
		cond.Message = joinFindings(res.Warnings)
	default:
		cond.Status = metav1.ConditionTrue
		cond.Reason = squirrelv1alpha1.ReasonAccepted
		cond.Message = "all rules pass validation"
	}
	return cond
}

// joinFindings produces the Message text for an Accepted condition by
// concatenating each finding's Error() form with `; ` separators. The
// per-finding format is `rule[N]: Reason: ...` (or `spec: Reason: ...`)
// so operators reading the condition can immediately identify the
// offending rule index.
func joinFindings(findings []ValidationError) string {
	parts := make([]string, len(findings))
	for i, e := range findings {
		parts[i] = e.Error()
	}
	return strings.Join(parts, "; ")
}
