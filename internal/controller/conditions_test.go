package controller_test

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	squirrelv1alpha1 "github.com/RemkoMolier/squirrel/api/v1alpha1"
	"github.com/RemkoMolier/squirrel/internal/controller"
)

func TestAcceptedConditionHappyPath(t *testing.T) {
	t.Parallel()

	got := controller.AcceptedCondition(controller.ValidationResult{}, 7)
	if got.Type != squirrelv1alpha1.ConditionAccepted {
		t.Errorf("Type: got %q, want %q", got.Type, squirrelv1alpha1.ConditionAccepted)
	}
	if got.Status != metav1.ConditionTrue {
		t.Errorf("Status: got %q, want %q", got.Status, metav1.ConditionTrue)
	}
	if got.Reason != squirrelv1alpha1.ReasonAccepted {
		t.Errorf("Reason: got %q, want %q", got.Reason, squirrelv1alpha1.ReasonAccepted)
	}
	if got.ObservedGeneration != 7 {
		t.Errorf("ObservedGeneration: got %d, want 7", got.ObservedGeneration)
	}
	if !strings.Contains(got.Message, "all rules pass") {
		t.Errorf("Message: got %q, want a pass message", got.Message)
	}
}

func TestAcceptedConditionErrorsTakePrecedence(t *testing.T) {
	t.Parallel()

	res := controller.ValidationResult{
		Errors: []controller.ValidationError{
			{RuleIndex: 0, Reason: squirrelv1alpha1.ReasonMissingRegistry, Message: "no registry"},
			{RuleIndex: 1, Reason: squirrelv1alpha1.ReasonInvalidPlaceholder, Message: "rule.target.registry: unknown placeholder {repos}"},
		},
		Warnings: []controller.ValidationError{
			{RuleIndex: 2, Reason: squirrelv1alpha1.ReasonPriorityIgnoredOnSkip, Message: "unused"},
		},
	}
	got := controller.AcceptedCondition(res, 3)

	if got.Status != metav1.ConditionFalse {
		t.Errorf("Status: got %q, want False (errors must take precedence over warnings)", got.Status)
	}
	if got.Reason != squirrelv1alpha1.ReasonMissingRegistry {
		t.Errorf("Reason: got %q, want first error reason %q", got.Reason, squirrelv1alpha1.ReasonMissingRegistry)
	}
	if got.ObservedGeneration != 3 {
		t.Errorf("ObservedGeneration: got %d, want 3", got.ObservedGeneration)
	}
	for _, want := range []string{"rule[0]:", "rule[1]:", "no registry", "unknown placeholder"} {
		if !strings.Contains(got.Message, want) {
			t.Errorf("Message: missing %q in %q", want, got.Message)
		}
	}
	if strings.Contains(got.Message, squirrelv1alpha1.ReasonPriorityIgnoredOnSkip) {
		t.Errorf("Message should not mention the warning when errors are present; got %q", got.Message)
	}
}

func TestAcceptedConditionWarningsKeepStatusTrue(t *testing.T) {
	t.Parallel()

	res := controller.ValidationResult{
		Warnings: []controller.ValidationError{
			{RuleIndex: 0, Reason: squirrelv1alpha1.ReasonPriorityIgnoredOnSkip, Message: "skip rule has explicit priority"},
		},
	}
	got := controller.AcceptedCondition(res, 5)

	if got.Status != metav1.ConditionTrue {
		t.Errorf("Status: got %q, want True (warnings keep status True)", got.Status)
	}
	if got.Reason != squirrelv1alpha1.ReasonPriorityIgnoredOnSkip {
		t.Errorf("Reason: got %q, want %q", got.Reason, squirrelv1alpha1.ReasonPriorityIgnoredOnSkip)
	}
	if !strings.Contains(got.Message, "rule[0]:") {
		t.Errorf("Message: expected rule index, got %q", got.Message)
	}
}

func TestAcceptedConditionSpecLevelErrorFormatting(t *testing.T) {
	t.Parallel()

	res := controller.ValidationResult{
		Errors: []controller.ValidationError{
			{RuleIndex: controller.SpecRuleIndex, Reason: squirrelv1alpha1.ReasonEmptyRules, Message: "spec.rules must contain at least one entry"},
		},
	}
	got := controller.AcceptedCondition(res, 1)

	if got.Status != metav1.ConditionFalse {
		t.Errorf("Status: got %q, want False", got.Status)
	}
	if got.Reason != squirrelv1alpha1.ReasonEmptyRules {
		t.Errorf("Reason: got %q, want %q", got.Reason, squirrelv1alpha1.ReasonEmptyRules)
	}
	if !strings.Contains(got.Message, "spec:") {
		t.Errorf("Message: expected `spec:` prefix for spec-level errors, got %q", got.Message)
	}
}
