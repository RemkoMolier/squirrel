package controller

import (
	"testing"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	squirrelv1alpha1 "github.com/RemkoMolier/squirrel/api/v1alpha1"
)

// applyAcceptedStatus is package-private, so this test lives in the
// `controller` package rather than the `_test` variant. The reconciler
// integration tests exercise the helper end-to-end via fake clients;
// this file pins each branch of the helper directly so a future
// refactor that adds a third condition (Ready, Available, ...) or
// changes the invariant called out in the doc comment can't silently
// break the existing semantics.

func acceptedCondition(observedGeneration int64, reason string) metav1.Condition {
	return metav1.Condition{
		Type:               squirrelv1alpha1.ConditionAccepted,
		Status:             metav1.ConditionTrue,
		ObservedGeneration: observedGeneration,
		Reason:             reason,
		Message:            "test",
	}
}

func TestApplyAcceptedStatusFirstObservation(t *testing.T) {
	t.Parallel()

	var observed int64
	var conditions []metav1.Condition
	changed := applyAcceptedStatus(&observed, &conditions, 1, acceptedCondition(1, squirrelv1alpha1.ReasonAccepted))

	if !changed {
		t.Errorf("first observation: got changed=false, want true")
	}
	if observed != 1 {
		t.Errorf("observed: got %d, want 1", observed)
	}
	if cond := apimeta.FindStatusCondition(conditions, squirrelv1alpha1.ConditionAccepted); cond == nil {
		t.Errorf("conditions: Accepted condition was not appended")
	}
}

func TestApplyAcceptedStatusNoChangeReturnsFalse(t *testing.T) {
	t.Parallel()

	observed := int64(1)
	conditions := []metav1.Condition{acceptedCondition(1, squirrelv1alpha1.ReasonAccepted)}

	changed := applyAcceptedStatus(&observed, &conditions, 1, acceptedCondition(1, squirrelv1alpha1.ReasonAccepted))

	if changed {
		t.Errorf("same generation, same condition: got changed=true, want false")
	}
}

func TestApplyAcceptedStatusDifferentReasonTriggersUpdate(t *testing.T) {
	t.Parallel()

	observed := int64(1)
	conditions := []metav1.Condition{acceptedCondition(1, squirrelv1alpha1.ReasonAccepted)}

	changed := applyAcceptedStatus(&observed, &conditions, 1, acceptedCondition(1, squirrelv1alpha1.ReasonPriorityIgnoredOnSkip))

	if !changed {
		t.Errorf("reason changed: got changed=false, want true")
	}
	cond := apimeta.FindStatusCondition(conditions, squirrelv1alpha1.ConditionAccepted)
	if cond == nil || cond.Reason != squirrelv1alpha1.ReasonPriorityIgnoredOnSkip {
		t.Errorf("updated reason: got %+v, want PriorityIgnoredOnSkip", cond)
	}
}

// TestApplyAcceptedStatusGenChangeImpliesCondChange is the regression
// guard for the invariant documented on applyAcceptedStatus: a bump in
// policy generation always pairs with a condition change because
// AcceptedCondition stamps ObservedGeneration onto the condition
// itself. If a future refactor produces a desired condition whose
// ObservedGeneration is stale, this test catches the leak.
func TestApplyAcceptedStatusGenChangeImpliesCondChange(t *testing.T) {
	t.Parallel()

	observed := int64(1)
	conditions := []metav1.Condition{acceptedCondition(1, squirrelv1alpha1.ReasonAccepted)}

	// Generation 2 with a desired condition that correctly stamps
	// ObservedGeneration=2.
	changed := applyAcceptedStatus(&observed, &conditions, 2, acceptedCondition(2, squirrelv1alpha1.ReasonAccepted))

	if !changed {
		t.Fatalf("generation bump: got changed=false; invariant says genChanged implies condChanged")
	}
	if observed != 2 {
		t.Errorf("observed: got %d, want 2", observed)
	}
	cond := apimeta.FindStatusCondition(conditions, squirrelv1alpha1.ConditionAccepted)
	if cond == nil || cond.ObservedGeneration != 2 {
		t.Errorf("condition.ObservedGeneration: got %+v, want 2", cond)
	}
}
