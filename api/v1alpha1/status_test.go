package v1alpha1_test

import (
	"reflect"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	squirrelv1alpha1 "github.com/RemkoMolier/squirrel/api/v1alpha1"
)

func TestConditionConstants(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, got, want string
	}{
		{"ConditionAccepted", squirrelv1alpha1.ConditionAccepted, "Accepted"},
		{"ReasonAccepted", squirrelv1alpha1.ReasonAccepted, "Accepted"},
		{"ReasonEmptyRules", squirrelv1alpha1.ReasonEmptyRules, "EmptyRules"},
		{"ReasonInvalidMatch", squirrelv1alpha1.ReasonInvalidMatch, "InvalidMatch"},
		{"ReasonInvalidPlaceholder", squirrelv1alpha1.ReasonInvalidPlaceholder, "InvalidPlaceholder"},
		{"ReasonMissingRegistry", squirrelv1alpha1.ReasonMissingRegistry, "MissingRegistry"},
		{"ReasonActionTargetConflict", squirrelv1alpha1.ReasonActionTargetConflict, "ActionTargetConflict"},
		{"ReasonInvalidAction", squirrelv1alpha1.ReasonInvalidAction, "InvalidAction"},
		{"ReasonPriorityIgnoredOnSkip", squirrelv1alpha1.ReasonPriorityIgnoredOnSkip, "PriorityIgnoredOnSkip"},
		{"ReasonInvalidNamespaceSelector", squirrelv1alpha1.ReasonInvalidNamespaceSelector, "InvalidNamespaceSelector"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}

// localTimestamp returns a metav1.Time built from a fixed local-zone
// instant. metav1.Time.UnmarshalJSON normalises any parsed time to the
// process's local timezone, so constructing the original timestamp in
// the local zone is what makes round-tripped objects compare equal under
// reflect.DeepEqual.
func localTimestamp() metav1.Time {
	return metav1.NewTime(time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC).Local())
}

func TestClusterImagePolicyStatusYAMLRoundTrip(t *testing.T) {
	t.Parallel()

	orig := squirrelv1alpha1.ClusterImagePolicyStatus{
		ObservedGeneration: 7,
		Conditions: []metav1.Condition{{
			Type:               squirrelv1alpha1.ConditionAccepted,
			Status:             metav1.ConditionTrue,
			ObservedGeneration: 7,
			Reason:             squirrelv1alpha1.ReasonAccepted,
			Message:            "all rules pass validation",
			LastTransitionTime: localTimestamp(),
		}},
	}

	data, err := yaml.Marshal(orig)
	if err != nil {
		t.Fatalf("yaml.Marshal: %v", err)
	}

	var back squirrelv1alpha1.ClusterImagePolicyStatus
	if err := yaml.Unmarshal(data, &back); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(orig, back) {
		t.Errorf("round-trip mismatch:\norig: %+v\nback: %+v\nYAML:\n%s",
			orig, back, data)
	}
}

func TestImagePolicyStatusYAMLRoundTrip(t *testing.T) {
	t.Parallel()

	orig := squirrelv1alpha1.ImagePolicyStatus{
		ObservedGeneration: 3,
		Conditions: []metav1.Condition{{
			Type:               squirrelv1alpha1.ConditionAccepted,
			Status:             metav1.ConditionFalse,
			ObservedGeneration: 3,
			Reason:             squirrelv1alpha1.ReasonInvalidPlaceholder,
			Message:            "rule[0].target.repository: unknown placeholder {repos}",
			LastTransitionTime: localTimestamp(),
		}},
	}

	data, err := yaml.Marshal(orig)
	if err != nil {
		t.Fatalf("yaml.Marshal: %v", err)
	}

	var back squirrelv1alpha1.ImagePolicyStatus
	if err := yaml.Unmarshal(data, &back); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(orig, back) {
		t.Errorf("round-trip mismatch:\norig: %+v\nback: %+v", orig, back)
	}
}

func TestStatusDeepCopyIsIndependent(t *testing.T) {
	t.Parallel()

	orig := squirrelv1alpha1.ClusterImagePolicyStatus{
		ObservedGeneration: 1,
		Conditions: []metav1.Condition{
			{Type: squirrelv1alpha1.ConditionAccepted, Reason: squirrelv1alpha1.ReasonAccepted},
		},
	}
	clone := orig.DeepCopy()
	clone.Conditions[0].Reason = "Mutated"
	if orig.Conditions[0].Reason != squirrelv1alpha1.ReasonAccepted {
		t.Errorf("DeepCopy: mutating clone.Conditions[0] affected the original; got %q, want %q",
			orig.Conditions[0].Reason, squirrelv1alpha1.ReasonAccepted)
	}
}
