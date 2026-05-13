package webhook_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	runtime "k8s.io/apimachinery/pkg/runtime"
	client "sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"
	interceptor "sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	squirrelv1alpha1 "github.com/RemkoMolier/squirrel/api/v1alpha1"
	"github.com/RemkoMolier/squirrel/internal/engine"
	"github.com/RemkoMolier/squirrel/internal/webhook"
)

// match returns an apiextensionsv1.JSON wrapping a glob-string match.
// json.Marshal on a string cannot fail in practice; the helper panics
// on the impossible error path so it is usable both from tests and
// from package-level fixtures (e.g. mirrorPolicy() in mutator_test.go)
// without threading *testing.T through every caller.
func match(s string) apiextensionsv1.JSON {
	raw, err := json.Marshal(s)
	if err != nil {
		panic(fmt.Sprintf("match helper marshal: %v", err))
	}
	return apiextensionsv1.JSON{Raw: raw}
}

// acceptedCondition is a True Accepted condition with reason Accepted
// pinned to the given spec generation. The generation parameter is
// required (rather than defaulted to 1) so the observedGeneration gate
// in isAccepted is exercised by every test against an explicit value -
// a future "Accepted at stale generation" case must not silently
// inherit a magic constant from the helper.
//
//nolint:unparam // generation is intentionally explicit at every callsite even when it is 1.
func acceptedCondition(generation int64) metav1.Condition {
	return metav1.Condition{
		Type:               squirrelv1alpha1.ConditionAccepted,
		Status:             metav1.ConditionTrue,
		Reason:             squirrelv1alpha1.ReasonAccepted,
		ObservedGeneration: generation,
	}
}

// rejectedCondition mirrors acceptedCondition for the False case; see
// that helper's comment for why the generation is parameterised.
func rejectedCondition(generation int64) metav1.Condition {
	return metav1.Condition{
		Type:               squirrelv1alpha1.ConditionAccepted,
		Status:             metav1.ConditionFalse,
		Reason:             squirrelv1alpha1.ReasonInvalidMatch,
		ObservedGeneration: generation,
	}
}

// newTestClient builds a fake client with the squirrel scheme and the
// supplied objects pre-installed.
func newTestClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := squirrelv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	return fakeclient.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&squirrelv1alpha1.ClusterImagePolicy{}, &squirrelv1alpha1.ImagePolicy{}).
		Build()
}

func TestApplicableRulesNoPolicies(t *testing.T) {
	t.Parallel()

	cli := newTestClient(t)
	compiled, errs, fatalErr := webhook.ApplicableRules(context.Background(), cli, "default", nil)
	if fatalErr != nil {
		t.Fatalf("unexpected fatal error: %v", fatalErr)
	}
	if len(compiled) != 0 {
		t.Errorf("compiled rules: got %d, want 0", len(compiled))
	}
	if len(errs) != 0 {
		t.Errorf("errs: got %v, want none", errs)
	}
}

func TestApplicableRulesIncludesAcceptedClusterPolicy(t *testing.T) {
	t.Parallel()

	policy := &squirrelv1alpha1.ClusterImagePolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "mirror", Generation: 1},
		Spec: squirrelv1alpha1.ClusterImagePolicySpec{
			DefaultTarget: &squirrelv1alpha1.Target{Registry: "mirror.internal"},
			Rules: []squirrelv1alpha1.Rule{
				{Match: match("docker.io/**:*")},
			},
		},
		Status: squirrelv1alpha1.ClusterImagePolicyStatus{
			Conditions: []metav1.Condition{acceptedCondition(1)},
		},
	}
	cli := newTestClient(t, policy)

	compiled, errs, fatalErr := webhook.ApplicableRules(context.Background(), cli, "default", nil)
	if fatalErr != nil {
		t.Fatalf("unexpected fatal error: %v", fatalErr)
	}
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(compiled) != 1 {
		t.Fatalf("compiled rules: got %d, want 1", len(compiled))
	}
	if got, want := compiled[0].Source.Name, "mirror"; got != want {
		t.Errorf("Source.Name: got %q, want %q", got, want)
	}
	if compiled[0].Source.Scope != engine.ScopeCluster {
		t.Errorf("Source.Scope: got %q, want %q", compiled[0].Source.Scope, engine.ScopeCluster)
	}
}

func TestApplicableRulesExcludesNonAcceptedClusterPolicy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		conditions []metav1.Condition
	}{
		{name: "no conditions at all", conditions: nil},
		{name: "Accepted=False", conditions: []metav1.Condition{rejectedCondition(1)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			policy := &squirrelv1alpha1.ClusterImagePolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "mirror", Generation: 1},
				Spec: squirrelv1alpha1.ClusterImagePolicySpec{
					DefaultTarget: &squirrelv1alpha1.Target{Registry: "mirror.internal"},
					Rules:         []squirrelv1alpha1.Rule{{Match: match("docker.io/**:*")}},
				},
				Status: squirrelv1alpha1.ClusterImagePolicyStatus{Conditions: tt.conditions},
			}
			cli := newTestClient(t, policy)
			compiled, errs, fatalErr := webhook.ApplicableRules(context.Background(), cli, "default", nil)
			if fatalErr != nil {
				t.Fatalf("unexpected fatal error: %v", fatalErr)
			}
			if len(compiled) != 0 {
				t.Errorf("compiled rules: got %d, want 0 (policy was not Accepted)", len(compiled))
			}
			if len(errs) != 0 {
				t.Errorf("errs: got %v, want none", errs)
			}
		})
	}
}

func TestApplicableRulesNamespaceSelectorFiltering(t *testing.T) {
	t.Parallel()

	policy := &squirrelv1alpha1.ClusterImagePolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "prod-only", Generation: 1},
		Spec: squirrelv1alpha1.ClusterImagePolicySpec{
			NamespaceSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"tier": "production"},
			},
			DefaultTarget: &squirrelv1alpha1.Target{Registry: "mirror.internal"},
			Rules:         []squirrelv1alpha1.Rule{{Match: match("docker.io/**:*")}},
		},
		Status: squirrelv1alpha1.ClusterImagePolicyStatus{
			Conditions: []metav1.Condition{acceptedCondition(1)},
		},
	}
	cli := newTestClient(t, policy)

	tests := []struct {
		name            string
		namespaceLabels map[string]string
		wantCount       int
	}{
		{name: "label matches", namespaceLabels: map[string]string{"tier": "production"}, wantCount: 1},
		{name: "label mismatched", namespaceLabels: map[string]string{"tier": "staging"}, wantCount: 0},
		{name: "label absent", namespaceLabels: nil, wantCount: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			compiled, errs, fatalErr := webhook.ApplicableRules(context.Background(), cli, "anyns", tt.namespaceLabels)
			if fatalErr != nil {
				t.Fatalf("unexpected fatal error: %v", fatalErr)
			}
			if len(errs) != 0 {
				t.Errorf("unexpected errors: %v", errs)
			}
			if len(compiled) != tt.wantCount {
				t.Errorf("compiled rules: got %d, want %d", len(compiled), tt.wantCount)
			}
		})
	}
}

func TestApplicableRulesEmptyNamespaceSelectorMatchesEverything(t *testing.T) {
	t.Parallel()

	// Per the design, a nil/empty namespaceSelector on a
	// ClusterImagePolicy applies to every namespace that has
	// the webhook-level opt-in label. The webhook itself enforces
	// the opt-in via the MWC namespaceSelector; the resolver treats
	// nil as "match everything". apimachinery's
	// LabelSelectorAsSelector(nil) returns labels.Nothing() (a
	// long-standing gotcha), so the resolver substitutes an empty
	// LabelSelector for a nil one before converting, which yields
	// labels.Everything() as required.
	policy := &squirrelv1alpha1.ClusterImagePolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "mirror", Generation: 1},
		Spec: squirrelv1alpha1.ClusterImagePolicySpec{
			// No NamespaceSelector.
			DefaultTarget: &squirrelv1alpha1.Target{Registry: "mirror.internal"},
			Rules:         []squirrelv1alpha1.Rule{{Match: match("docker.io/**:*")}},
		},
		Status: squirrelv1alpha1.ClusterImagePolicyStatus{
			Conditions: []metav1.Condition{acceptedCondition(1)},
		},
	}
	cli := newTestClient(t, policy)
	compiled, errs, fatalErr := webhook.ApplicableRules(context.Background(), cli, "anyns", nil)
	if fatalErr != nil {
		t.Fatalf("unexpected fatal error: %v", fatalErr)
	}
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(compiled) != 1 {
		t.Errorf("compiled rules: got %d, want 1 (nil selector should match)", len(compiled))
	}
}

func TestApplicableRulesIncludesAcceptedImagePolicyInTargetNamespace(t *testing.T) {
	t.Parallel()

	policy := &squirrelv1alpha1.ImagePolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "override", Namespace: "payments", Generation: 1},
		Spec: squirrelv1alpha1.ImagePolicySpec{
			Rules: []squirrelv1alpha1.Rule{{
				Match: match("docker.io/library/nginx:*"),
				Target: &squirrelv1alpha1.Target{
					Registry:   "registry.internal",
					Repository: "payments/nginx-patched",
				},
			}},
		},
		Status: squirrelv1alpha1.ImagePolicyStatus{
			Conditions: []metav1.Condition{acceptedCondition(1)},
		},
	}
	cli := newTestClient(t, policy)

	compiled, errs, fatalErr := webhook.ApplicableRules(context.Background(), cli, "payments", nil)
	if fatalErr != nil {
		t.Fatalf("unexpected fatal error: %v", fatalErr)
	}
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(compiled) != 1 {
		t.Fatalf("compiled rules: got %d, want 1", len(compiled))
	}
	if got := compiled[0].Source; got.Scope != engine.ScopeNamespaced || got.Name != "override" || got.Namespace != "payments" {
		t.Errorf("Source: got %+v, want namespaced override/payments", got)
	}
}

func TestApplicableRulesIgnoresImagePolicyInOtherNamespace(t *testing.T) {
	t.Parallel()

	policy := &squirrelv1alpha1.ImagePolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "override", Namespace: "other", Generation: 1},
		Spec: squirrelv1alpha1.ImagePolicySpec{
			Rules: []squirrelv1alpha1.Rule{{Match: match("docker.io/**:*")}},
		},
		Status: squirrelv1alpha1.ImagePolicyStatus{
			Conditions: []metav1.Condition{acceptedCondition(1)},
		},
	}
	cli := newTestClient(t, policy)

	compiled, _, _ := webhook.ApplicableRules(context.Background(), cli, "payments", nil)
	if len(compiled) != 0 {
		t.Errorf("compiled rules: got %d, want 0 (ImagePolicy in other namespace must not apply)", len(compiled))
	}
}

func TestApplicableRulesCombinesClusterAndNamespacedSources(t *testing.T) {
	t.Parallel()

	cip := &squirrelv1alpha1.ClusterImagePolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster-mirror", Generation: 1},
		Spec: squirrelv1alpha1.ClusterImagePolicySpec{
			DefaultTarget: &squirrelv1alpha1.Target{Registry: "mirror.internal"},
			Rules:         []squirrelv1alpha1.Rule{{Match: match("docker.io/**:*")}},
		},
		Status: squirrelv1alpha1.ClusterImagePolicyStatus{
			Conditions: []metav1.Condition{acceptedCondition(1)},
		},
	}
	ip := &squirrelv1alpha1.ImagePolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "ns-override", Namespace: "payments", Generation: 1},
		Spec: squirrelv1alpha1.ImagePolicySpec{
			Rules: []squirrelv1alpha1.Rule{{
				Match:  match("docker.io/library/nginx:*"),
				Target: &squirrelv1alpha1.Target{Registry: "registry.internal"},
			}},
		},
		Status: squirrelv1alpha1.ImagePolicyStatus{
			Conditions: []metav1.Condition{acceptedCondition(1)},
		},
	}
	cli := newTestClient(t, cip, ip)

	compiled, errs, fatalErr := webhook.ApplicableRules(context.Background(), cli, "payments", nil)
	if fatalErr != nil {
		t.Fatalf("unexpected fatal error: %v", fatalErr)
	}
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(compiled) != 2 {
		t.Fatalf("compiled rules: got %d, want 2 (cluster + namespaced)", len(compiled))
	}
	scopes := []engine.Scope{compiled[0].Source.Scope, compiled[1].Source.Scope}
	gotCluster, gotNamespaced := false, false
	for _, s := range scopes {
		switch s {
		case engine.ScopeCluster:
			gotCluster = true
		case engine.ScopeNamespaced:
			gotNamespaced = true
		}
	}
	if !gotCluster || !gotNamespaced {
		t.Errorf("scopes: got cluster=%t namespaced=%t, want both true", gotCluster, gotNamespaced)
	}
}

func TestApplicableRulesSurfacesCompileErrorsAsNonFatal(t *testing.T) {
	t.Parallel()

	// Accepted policy with a rule whose match payload is a JSON
	// scalar that fails MatchExpression. The reconciler should have
	// rejected this, but defence-in-depth: the resolver skips that
	// rule and returns an error so the webhook can log + emit a
	// metric without crashing admission.
	policy := &squirrelv1alpha1.ClusterImagePolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "mirror", Generation: 1},
		Spec: squirrelv1alpha1.ClusterImagePolicySpec{
			DefaultTarget: &squirrelv1alpha1.Target{Registry: "mirror.internal"},
			Rules: []squirrelv1alpha1.Rule{
				{Match: match("docker.io/**:*")},
				{Match: apiextensionsv1.JSON{Raw: []byte("42")}}, // scalar, will fail
			},
		},
		Status: squirrelv1alpha1.ClusterImagePolicyStatus{
			Conditions: []metav1.Condition{acceptedCondition(1)},
		},
	}
	cli := newTestClient(t, policy)

	compiled, errs, fatalErr := webhook.ApplicableRules(context.Background(), cli, "default", nil)
	if fatalErr != nil {
		t.Fatalf("unexpected fatal error: %v", fatalErr)
	}
	if len(compiled) != 1 {
		t.Errorf("compiled rules: got %d, want 1 (one good rule, one skipped)", len(compiled))
	}
	if len(errs) != 1 {
		t.Errorf("errs: got %d, want 1 (the skipped rule must surface an error)", len(errs))
	}
}

// TestApplicableRulesExcludesPolicyWithStaleObservedGeneration covers
// the observedGeneration gate in isAccepted: a user edits a policy
// spec, the cache reflects the new generation immediately, but the
// reconciler has not run yet, so the Accepted condition still points
// at the previous generation. The webhook must NOT apply the unblessed
// spec just because a previous generation of the policy was accepted.
func TestApplicableRulesExcludesPolicyWithStaleObservedGeneration(t *testing.T) {
	t.Parallel()

	policy := &squirrelv1alpha1.ClusterImagePolicy{
		// Generation 2 - the latest spec edit.
		ObjectMeta: metav1.ObjectMeta{Name: "mirror", Generation: 2},
		Spec: squirrelv1alpha1.ClusterImagePolicySpec{
			DefaultTarget: &squirrelv1alpha1.Target{Registry: "mirror.internal"},
			Rules:         []squirrelv1alpha1.Rule{{Match: match("docker.io/**:*")}},
		},
		Status: squirrelv1alpha1.ClusterImagePolicyStatus{
			// Stale - the reconciler has not seen generation 2.
			Conditions: []metav1.Condition{acceptedCondition(1)},
		},
	}
	cli := newTestClient(t, policy)

	compiled, _, fatalErr := webhook.ApplicableRules(context.Background(), cli, "default", nil)
	if fatalErr != nil {
		t.Fatalf("unexpected fatal error: %v", fatalErr)
	}
	if len(compiled) != 0 {
		t.Errorf("compiled rules: got %d, want 0 (stale observedGeneration must not count as accepted)", len(compiled))
	}
}

// TestApplicableRulesFatalOnClusterListFailure covers the fail-closed
// contract on the cluster-policy List path: when the
// ClusterImagePolicy List fails (RBAC, cache, networking), the
// resolver must surface a fatal error rather than silently returning
// only the namespaced subset. The caller (the webhook) uses the
// fatal flag to admit unchanged instead of applying a partial set.
func TestApplicableRulesFatalOnClusterListFailure(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := squirrelv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	cli := fakeclient.NewClientBuilder().
		WithScheme(scheme).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				if _, ok := list.(*squirrelv1alpha1.ClusterImagePolicyList); ok {
					return errors.New("simulated cluster list failure")
				}
				return c.List(ctx, list, opts...)
			},
		}).
		Build()

	rules, _, fatalErr := webhook.ApplicableRules(context.Background(), cli, "default", nil)
	if fatalErr == nil {
		t.Fatalf("expected fatal error from cluster List failure; got rules=%v", rules)
	}
	if rules != nil {
		t.Errorf("rules: got %v, want nil on fatal error", rules)
	}
}

// TestApplicableRulesFatalOnNamespacedListFailure mirrors the cluster
// case for the ImagePolicy List path: a misconfigured cache or RBAC
// for namespaced policies is just as dangerous as the cluster case,
// because the missing per-namespace overrides might include a higher-
// priority rewrite or a skip that the webhook needs.
func TestApplicableRulesFatalOnNamespacedListFailure(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := squirrelv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	cli := fakeclient.NewClientBuilder().
		WithScheme(scheme).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				if _, ok := list.(*squirrelv1alpha1.ImagePolicyList); ok {
					return errors.New("simulated namespaced list failure")
				}
				return c.List(ctx, list, opts...)
			},
		}).
		Build()

	rules, _, fatalErr := webhook.ApplicableRules(context.Background(), cli, "default", nil)
	if fatalErr == nil {
		t.Fatalf("expected fatal error from namespaced List failure; got rules=%v", rules)
	}
	if rules != nil {
		t.Errorf("rules: got %v, want nil on fatal error", rules)
	}
}
