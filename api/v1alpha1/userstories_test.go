package v1alpha1_test

import (
	"reflect"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	squirrelv1alpha1 "github.com/RemkoMolier/squirrel/api/v1alpha1"
)

// These tests load each of the user-story YAML samples from
// docs/design/v1alpha1.md and assert that the unmarshalled Go object
// (and its parsed match expression) matches the design. They are the
// design's executable contract for the v1alpha1 API surface; any
// spec-level change that breaks one of these is an API change and must
// be reflected in the design doc.
//
// Rule.Match is stored as raw JSON on the wire, so the tests assert on
// the parsed MatchExpr returned by Rule.MatchExpression() rather than
// on the raw bytes - the user-facing contract is the parsed form, not
// the on-wire encoding.

func TestUserStoryDockerHubMirror(t *testing.T) {
	t.Parallel()

	src := `apiVersion: squirrel.molier.dev/v1alpha1
kind: ClusterImagePolicy
metadata:
  name: dockerhub-mirror
spec:
  namespaceSelector:
    matchLabels:
      tier: production
  defaultTarget:
    registry: mirror.internal
    repository: dockerhub/{repository}
    tags: ["{tag}", "{digest:short8}"]
  rules:
    - match: "docker.io/**:*"
`

	var got squirrelv1alpha1.ClusterImagePolicy
	if err := yaml.Unmarshal([]byte(src), &got); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}

	if got.APIVersion != "squirrel.molier.dev/v1alpha1" || got.Kind != "ClusterImagePolicy" {
		t.Errorf("TypeMeta: got APIVersion=%q Kind=%q, want squirrel.molier.dev/v1alpha1 / ClusterImagePolicy",
			got.APIVersion, got.Kind)
	}
	if got.Name != "dockerhub-mirror" {
		t.Errorf("Name: got %q, want dockerhub-mirror", got.Name)
	}

	wantNS := &metav1.LabelSelector{MatchLabels: map[string]string{"tier": "production"}}
	if !reflect.DeepEqual(got.Spec.NamespaceSelector, wantNS) {
		t.Errorf("NamespaceSelector: got %+v, want %+v", got.Spec.NamespaceSelector, wantNS)
	}

	wantTarget := &squirrelv1alpha1.Target{
		Registry:   "mirror.internal",
		Repository: "dockerhub/{repository}",
		Tags:       []string{"{tag}", "{digest:short8}"},
	}
	if !reflect.DeepEqual(got.Spec.DefaultTarget, wantTarget) {
		t.Errorf("DefaultTarget: got %+v, want %+v", got.Spec.DefaultTarget, wantTarget)
	}

	if len(got.Spec.Rules) != 1 {
		t.Fatalf("Rules: got %d entries, want 1", len(got.Spec.Rules))
	}
	mx, err := got.Spec.Rules[0].MatchExpression()
	if err != nil {
		t.Fatalf("Rules[0].MatchExpression: %v", err)
	}
	if want := (squirrelv1alpha1.MatchExpr{String: "docker.io/**:*"}); !reflect.DeepEqual(mx, want) {
		t.Errorf("Rules[0] match: got %+v, want %+v", mx, want)
	}
}

func TestUserStorySkipSpecificImage(t *testing.T) {
	t.Parallel()

	src := `apiVersion: squirrel.molier.dev/v1alpha1
kind: ClusterImagePolicy
metadata:
  name: allow-direct-pulls
spec:
  rules:
    - match: "docker.io/library/distroless-base:*"
      action: skip
`

	var got squirrelv1alpha1.ClusterImagePolicy
	if err := yaml.Unmarshal([]byte(src), &got); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	if len(got.Spec.Rules) != 1 {
		t.Fatalf("Rules: got %d entries, want 1", len(got.Spec.Rules))
	}
	rule := got.Spec.Rules[0]
	if rule.Action != squirrelv1alpha1.ActionSkip {
		t.Errorf("Action: got %q, want %q", rule.Action, squirrelv1alpha1.ActionSkip)
	}
	mx, err := rule.MatchExpression()
	if err != nil {
		t.Fatalf("MatchExpression: %v", err)
	}
	if want := (squirrelv1alpha1.MatchExpr{String: "docker.io/library/distroless-base:*"}); !reflect.DeepEqual(mx, want) {
		t.Errorf("match: got %+v, want %+v", mx, want)
	}
}

func TestUserStoryPerNamespaceOverride(t *testing.T) {
	t.Parallel()

	src := `apiVersion: squirrel.molier.dev/v1alpha1
kind: ImagePolicy
metadata:
  name: payments-nginx-override
  namespace: payments
spec:
  rules:
    - match: "docker.io/library/nginx:*"
      target:
        registry: registry.internal
        repository: payments/nginx-patched
        tags: ["{tag}"]
`

	var got squirrelv1alpha1.ImagePolicy
	if err := yaml.Unmarshal([]byte(src), &got); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	if got.Namespace != "payments" {
		t.Errorf("Namespace: got %q, want payments", got.Namespace)
	}
	if len(got.Spec.Rules) != 1 {
		t.Fatalf("Rules: got %d entries, want 1", len(got.Spec.Rules))
	}
	rule := got.Spec.Rules[0]

	mx, err := rule.MatchExpression()
	if err != nil {
		t.Fatalf("MatchExpression: %v", err)
	}
	if want := (squirrelv1alpha1.MatchExpr{String: "docker.io/library/nginx:*"}); !reflect.DeepEqual(mx, want) {
		t.Errorf("match: got %+v, want %+v", mx, want)
	}
	wantTarget := &squirrelv1alpha1.Target{
		Registry:   "registry.internal",
		Repository: "payments/nginx-patched",
		Tags:       []string{"{tag}"},
	}
	if !reflect.DeepEqual(rule.Target, wantTarget) {
		t.Errorf("Target: got %+v, want %+v", rule.Target, wantTarget)
	}
}

func TestSpecRulesIsRequiredViaJSONTag(t *testing.T) {
	t.Parallel()

	// Marshal a zero-value Spec; the Rules field has the JSON tag
	// `rules` (no omitempty), so a nil slice marshals as `rules: null`
	// rather than being omitted. The CRD-level MinItems=1 marker
	// rejects null/empty rule lists at admission; the reconciler then
	// catches anything that slipped through.
	cip := squirrelv1alpha1.ClusterImagePolicy{}
	out, err := yaml.Marshal(cip.Spec)
	if err != nil {
		t.Fatalf("yaml.Marshal: %v", err)
	}
	if string(out) != "rules: null\n" {
		t.Errorf("expected `rules: null` in marshalled output, got:\n%s", out)
	}
}
