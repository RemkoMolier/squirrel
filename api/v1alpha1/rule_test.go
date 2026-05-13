package v1alpha1_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	squirrelv1alpha1 "github.com/RemkoMolier/squirrel/api/v1alpha1"
)

// mustJSON returns an apiextensionsv1.JSON wrapping the given value's
// JSON encoding. Test-only helper.
func mustJSON(t *testing.T, v any) apiextensionsv1.JSON {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal helper: %v", err)
	}
	return apiextensionsv1.JSON{Raw: raw}
}

func TestActionConstants(t *testing.T) {
	t.Parallel()

	if got, want := string(squirrelv1alpha1.ActionRewrite), "rewrite"; got != want {
		t.Errorf("ActionRewrite: got %q, want %q", got, want)
	}
	if got, want := string(squirrelv1alpha1.ActionSkip), "skip"; got != want {
		t.Errorf("ActionSkip: got %q, want %q", got, want)
	}
}

func TestRuleRoundTripJSON(t *testing.T) {
	t.Parallel()

	priority := int32(99999)
	tests := []struct {
		name         string
		json         string
		wantMatch    squirrelv1alpha1.MatchExpr
		wantAction   squirrelv1alpha1.Action
		wantTarget   *squirrelv1alpha1.Target
		wantPriority *int32
	}{
		{
			name:      "string-form match only",
			json:      `{"match":"docker.io/library/**:*"}`,
			wantMatch: squirrelv1alpha1.MatchExpr{String: "docker.io/library/**:*"},
		},
		{
			name: "structured match, action rewrite, target, priority",
			json: `{"match":{"registry":"gcr.io","repository":"google_containers/**"},"action":"rewrite","target":{"registry":"mirror.internal","repository":"gcr-mirror/{repository:flat}"},"priority":99999}`,
			wantMatch: squirrelv1alpha1.MatchExpr{
				Structured: &squirrelv1alpha1.Match{Registry: "gcr.io", Repository: "google_containers/**"},
			},
			wantAction: squirrelv1alpha1.ActionRewrite,
			wantTarget: &squirrelv1alpha1.Target{
				Registry:   "mirror.internal",
				Repository: "gcr-mirror/{repository:flat}",
			},
			wantPriority: &priority,
		},
		{
			name:       "skip rule with string-form match",
			json:       `{"match":"docker.io/library/distroless-base:*","action":"skip"}`,
			wantMatch:  squirrelv1alpha1.MatchExpr{String: "docker.io/library/distroless-base:*"},
			wantAction: squirrelv1alpha1.ActionSkip,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var got squirrelv1alpha1.Rule
			if err := json.Unmarshal([]byte(tt.json), &got); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}

			// Parsed Match must match expectations.
			mx, err := got.MatchExpression()
			if err != nil {
				t.Fatalf("MatchExpression: %v", err)
			}
			if !reflect.DeepEqual(mx, tt.wantMatch) {
				t.Errorf("MatchExpression:\n got: %+v\nwant: %+v", mx, tt.wantMatch)
			}
			if got.Action != tt.wantAction {
				t.Errorf("Action: got %q, want %q", got.Action, tt.wantAction)
			}
			if !reflect.DeepEqual(got.Target, tt.wantTarget) {
				t.Errorf("Target: got %+v, want %+v", got.Target, tt.wantTarget)
			}
			if !reflect.DeepEqual(got.Priority, tt.wantPriority) {
				t.Errorf("Priority: got %v, want %v", got.Priority, tt.wantPriority)
			}

			// Re-marshalling produces JSON equivalent to the input.
			back, err := json.Marshal(got)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if string(back) != tt.json {
				t.Errorf("re-Marshal:\n got: %s\nwant: %s", back, tt.json)
			}
		})
	}
}

func TestRuleMatchExpressionAlwaysDecodesFromAnyJSON(t *testing.T) {
	t.Parallel()

	// These payloads would all fail the strict MatchExpr decoder. The
	// raw-JSON storage on Rule.Match guarantees Rule itself still
	// decodes - which is the whole point of switching the field type
	// away from MatchExpr: a malformed payload must land in etcd
	// cleanly so the reconciler can surface InvalidMatch, not poison
	// every typed informer's list/watch.
	tests := []struct {
		name string
		body string
	}{
		{name: "numeric scalar", body: `{"match":42}`},
		{name: "boolean scalar", body: `{"match":true}`},
		{name: "object with unknown field", body: `{"match":{"registrry":"docker.io"}}`},
		{name: "empty object", body: `{"match":{}}`},
		{name: "JSON null", body: `{"match":null}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var r squirrelv1alpha1.Rule
			if err := json.Unmarshal([]byte(tt.body), &r); err != nil {
				t.Fatalf("Rule must decode regardless of match shape; got %v", err)
			}
		})
	}
}

func TestRuleMatchExpressionRejectsMalformed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
	}{
		{name: "scalar number", body: `{"match":42}`},
		{name: "scalar boolean", body: `{"match":true}`},
		{name: "empty string", body: `{"match":""}`},
		{name: "object with unknown field", body: `{"match":{"registrry":"docker.io"}}`},
		{name: "object with known and unknown fields", body: `{"match":{"registry":"docker.io","extras":"foo"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var r squirrelv1alpha1.Rule
			if err := json.Unmarshal([]byte(tt.body), &r); err != nil {
				t.Fatalf("Rule.Unmarshal: %v", err)
			}
			if _, err := r.MatchExpression(); err == nil {
				t.Errorf("MatchExpression on %s: expected error, got nil", tt.body)
			}
		})
	}
}

func TestRuleMatchExpressionRejectsEmptyPayload(t *testing.T) {
	t.Parallel()

	var r squirrelv1alpha1.Rule
	_, err := r.MatchExpression()
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Errorf("MatchExpression on zero Rule: expected empty-payload error, got %v", err)
	}
}

// TestRuleMatchExpressionRejectsNullRawBytes guards the fake-client /
// fixture / pre-CEL-persisted path. apiextensionsv1.JSON.UnmarshalJSON
// strips a JSON-decoded `null` to an empty Raw slice (caught by the
// empty-payload check above), but a manually-constructed Rule that
// sets Match.Raw = []byte("null") goes through MatchExpr.UnmarshalJSON.
// Without an explicit null rejection there, MatchExpression would
// silently return a zero MatchExpr - a match-everything catch-all.
func TestRuleMatchExpressionRejectsNullRawBytes(t *testing.T) {
	t.Parallel()

	r := squirrelv1alpha1.Rule{
		Match: apiextensionsv1.JSON{Raw: []byte("null")},
	}
	if _, err := r.MatchExpression(); err == nil {
		t.Errorf("MatchExpression on Match.Raw=`null`: expected error, got nil")
	}
}

func TestRuleDeepCopyIsIndependent(t *testing.T) {
	t.Parallel()

	priority := int32(42)
	orig := squirrelv1alpha1.Rule{
		Match:  mustJSON(t, squirrelv1alpha1.Match{Registry: "docker.io", Repository: "library/**"}),
		Action: squirrelv1alpha1.ActionRewrite,
		Target: &squirrelv1alpha1.Target{
			Registry: "mirror.internal",
			Tags:     []string{"{tag}"},
		},
		Priority: &priority,
	}
	clone := orig.DeepCopy()

	// Each pointer / slice field must be independently allocated.
	if &clone.Match.Raw[0] == &orig.Match.Raw[0] {
		t.Error("DeepCopy shared Match.Raw backing array")
	}
	if clone.Target == orig.Target {
		t.Error("DeepCopy shared Target pointer")
	}
	if clone.Priority == orig.Priority {
		t.Error("DeepCopy shared Priority pointer")
	}

	// Mutations on the clone must not bleed into the original.
	clone.Match.Raw[0] = 'X'
	clone.Target.Tags[0] = "mutated"
	*clone.Priority = 0

	if orig.Match.Raw[0] == 'X' {
		t.Errorf("DeepCopy: mutating clone.Match.Raw affected the original")
	}
	if orig.Target.Tags[0] != "{tag}" {
		t.Errorf("DeepCopy: mutating clone.Target.Tags[0] affected the original")
	}
	if *orig.Priority != 42 {
		t.Errorf("DeepCopy: mutating *clone.Priority affected the original; got %d, want %d", *orig.Priority, 42)
	}
}
