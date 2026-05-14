package controller_test

import (
	"context"
	"testing"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	runtime "k8s.io/apimachinery/pkg/runtime"
	types "k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	client "sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"

	squirrelv1alpha1 "github.com/RemkoMolier/squirrel/api/v1alpha1"
	"github.com/RemkoMolier/squirrel/internal/controller"
)

// Reconciler tests rely on controller-runtime's fake client to exercise
// the validate-then-status-update flow. The fake client honours the
// status subresource via WithStatusSubresource, which is enough to
// pin the Accepted condition behaviour. An envtest harness (kube
// apiserver + etcd, per ADR-0004's guidance) would additionally cover
// CRD-level structural validation but is not required here because the
// reconciler is read-only on the spec and its only write is the
// well-known status subresource. envtest can be added in a follow-up
// alongside the Phase 5 webhook tests.

const cipName = "test-policy"

func newClusterImagePolicyTestEnv(t *testing.T, policy *squirrelv1alpha1.ClusterImagePolicy) (client.Client, *controller.ClusterImagePolicyReconciler) {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := squirrelv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}

	cli := fakeclient.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(policy).
		WithStatusSubresource(&squirrelv1alpha1.ClusterImagePolicy{}).
		Build()

	return cli, &controller.ClusterImagePolicyReconciler{Client: cli}
}

func reconcileCIP(t *testing.T, r *controller.ClusterImagePolicyReconciler) {
	t.Helper()
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: cipName}})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
}

func TestClusterImagePolicyReconcileAccepted(t *testing.T) {
	t.Parallel()

	policy := &squirrelv1alpha1.ClusterImagePolicy{
		ObjectMeta: metav1.ObjectMeta{Name: cipName, Generation: 1},
		Spec: squirrelv1alpha1.ClusterImagePolicySpec{
			DefaultTarget: &squirrelv1alpha1.Target{Registry: "mirror.internal"},
			Rules: []squirrelv1alpha1.Rule{
				{Match: matchString(t, "docker.io/**:*")},
			},
		},
	}
	cli, r := newClusterImagePolicyTestEnv(t, policy)
	reconcileCIP(t, r)

	var got squirrelv1alpha1.ClusterImagePolicy
	if err := cli.Get(context.Background(), client.ObjectKey{Name: cipName}, &got); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status.ObservedGeneration != 1 {
		t.Errorf("ObservedGeneration: got %d, want 1", got.Status.ObservedGeneration)
	}
	cond := apimeta.FindStatusCondition(got.Status.Conditions, squirrelv1alpha1.ConditionAccepted)
	if cond == nil {
		t.Fatal("Accepted condition missing")
	}
	if cond.Status != metav1.ConditionTrue {
		t.Errorf("Status: got %q, want True", cond.Status)
	}
	if cond.Reason != squirrelv1alpha1.ReasonAccepted {
		t.Errorf("Reason: got %q, want %q", cond.Reason, squirrelv1alpha1.ReasonAccepted)
	}
	if cond.ObservedGeneration != 1 {
		t.Errorf("Condition.ObservedGeneration: got %d, want 1", cond.ObservedGeneration)
	}
}

func TestClusterImagePolicyReconcileRejectsEmptyRules(t *testing.T) {
	t.Parallel()

	policy := &squirrelv1alpha1.ClusterImagePolicy{
		ObjectMeta: metav1.ObjectMeta{Name: cipName, Generation: 1},
		// No rules.
	}
	cli, r := newClusterImagePolicyTestEnv(t, policy)
	reconcileCIP(t, r)

	var got squirrelv1alpha1.ClusterImagePolicy
	if err := cli.Get(context.Background(), client.ObjectKey{Name: cipName}, &got); err != nil {
		t.Fatalf("Get: %v", err)
	}
	cond := apimeta.FindStatusCondition(got.Status.Conditions, squirrelv1alpha1.ConditionAccepted)
	if cond == nil {
		t.Fatal("Accepted condition missing")
	}
	if cond.Status != metav1.ConditionFalse {
		t.Errorf("Status: got %q, want False", cond.Status)
	}
	if cond.Reason != squirrelv1alpha1.ReasonEmptyRules {
		t.Errorf("Reason: got %q, want %q", cond.Reason, squirrelv1alpha1.ReasonEmptyRules)
	}
}

func TestClusterImagePolicyReconcileSurfacesInvalidPlaceholder(t *testing.T) {
	t.Parallel()

	policy := &squirrelv1alpha1.ClusterImagePolicy{
		ObjectMeta: metav1.ObjectMeta{Name: cipName, Generation: 1},
		Spec: squirrelv1alpha1.ClusterImagePolicySpec{
			Rules: []squirrelv1alpha1.Rule{{
				Match: matchString(t, "docker.io/**:*"),
				Target: &squirrelv1alpha1.Target{
					Registry: "{repos}", // unknown placeholder
				},
			}},
		},
	}
	cli, r := newClusterImagePolicyTestEnv(t, policy)
	reconcileCIP(t, r)

	var got squirrelv1alpha1.ClusterImagePolicy
	if err := cli.Get(context.Background(), client.ObjectKey{Name: cipName}, &got); err != nil {
		t.Fatalf("Get: %v", err)
	}
	cond := apimeta.FindStatusCondition(got.Status.Conditions, squirrelv1alpha1.ConditionAccepted)
	if cond == nil {
		t.Fatal("Accepted condition missing")
	}
	if cond.Status != metav1.ConditionFalse || cond.Reason != squirrelv1alpha1.ReasonInvalidPlaceholder {
		t.Errorf("Condition: got Status=%q Reason=%q, want False/InvalidPlaceholder", cond.Status, cond.Reason)
	}
}

// TestClusterImagePolicyReconcileRejectsInvalidNamespaceSelector
// covers the failure mode the CRD schema does not catch: a structurally
// valid LabelSelector that fails to compile through
// LabelSelectorAsSelector at admission time (here an unsupported
// MatchExpressions operator). Without this validation the policy
// would land Accepted=True and the webhook would silently skip it,
// leaving operators chasing a policy that the CRD says is fine yet
// never applies.
func TestClusterImagePolicyReconcileRejectsInvalidNamespaceSelector(t *testing.T) {
	t.Parallel()

	policy := &squirrelv1alpha1.ClusterImagePolicy{
		ObjectMeta: metav1.ObjectMeta{Name: cipName, Generation: 1},
		Spec: squirrelv1alpha1.ClusterImagePolicySpec{
			NamespaceSelector: &metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{{
					Key:      "tier",
					Operator: metav1.LabelSelectorOperator("BogusOp"),
					Values:   []string{"production"},
				}},
			},
			DefaultTarget: &squirrelv1alpha1.Target{Registry: "mirror.internal"},
			Rules:         []squirrelv1alpha1.Rule{{Match: matchString(t, "docker.io/**:*")}},
		},
	}
	cli, r := newClusterImagePolicyTestEnv(t, policy)
	reconcileCIP(t, r)

	var got squirrelv1alpha1.ClusterImagePolicy
	if err := cli.Get(context.Background(), client.ObjectKey{Name: cipName}, &got); err != nil {
		t.Fatalf("Get: %v", err)
	}
	cond := apimeta.FindStatusCondition(got.Status.Conditions, squirrelv1alpha1.ConditionAccepted)
	if cond == nil {
		t.Fatal("Accepted condition missing")
	}
	if cond.Status != metav1.ConditionFalse || cond.Reason != squirrelv1alpha1.ReasonInvalidNamespaceSelector {
		t.Errorf("Condition: got Status=%q Reason=%q, want False/InvalidNamespaceSelector", cond.Status, cond.Reason)
	}
}

func TestClusterImagePolicyReconcileIsIdempotent(t *testing.T) {
	t.Parallel()

	policy := &squirrelv1alpha1.ClusterImagePolicy{
		ObjectMeta: metav1.ObjectMeta{Name: cipName, Generation: 1},
		Spec: squirrelv1alpha1.ClusterImagePolicySpec{
			DefaultTarget: &squirrelv1alpha1.Target{Registry: "mirror.internal"},
			Rules: []squirrelv1alpha1.Rule{
				{Match: matchString(t, "docker.io/**:*")},
			},
		},
	}
	cli, r := newClusterImagePolicyTestEnv(t, policy)

	// First reconcile: writes status.
	reconcileCIP(t, r)
	var afterFirst squirrelv1alpha1.ClusterImagePolicy
	if err := cli.Get(context.Background(), client.ObjectKey{Name: cipName}, &afterFirst); err != nil {
		t.Fatalf("Get: %v", err)
	}
	rvFirst := afterFirst.ResourceVersion

	// Second reconcile against the same generation: must not write.
	reconcileCIP(t, r)
	var afterSecond squirrelv1alpha1.ClusterImagePolicy
	if err := cli.Get(context.Background(), client.ObjectKey{Name: cipName}, &afterSecond); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if afterSecond.ResourceVersion != rvFirst {
		t.Errorf("idempotency violated: ResourceVersion changed from %q to %q on a no-op reconcile",
			rvFirst, afterSecond.ResourceVersion)
	}
}

func TestClusterImagePolicyReconcileReactsToSpecChange(t *testing.T) {
	t.Parallel()

	policy := &squirrelv1alpha1.ClusterImagePolicy{
		ObjectMeta: metav1.ObjectMeta{Name: cipName, Generation: 1},
		// Initial state: invalid (no rules).
	}
	cli, r := newClusterImagePolicyTestEnv(t, policy)
	reconcileCIP(t, r)

	// Sanity: initial state surfaces EmptyRules.
	var initial squirrelv1alpha1.ClusterImagePolicy
	if err := cli.Get(context.Background(), client.ObjectKey{Name: cipName}, &initial); err != nil {
		t.Fatalf("Get: %v", err)
	}
	cond := apimeta.FindStatusCondition(initial.Status.Conditions, squirrelv1alpha1.ConditionAccepted)
	if cond == nil || cond.Reason != squirrelv1alpha1.ReasonEmptyRules {
		t.Fatalf("initial state expected Accepted=False/EmptyRules, got %+v", cond)
	}

	// Operator edits the spec: adds a valid rule, generation bumps.
	initial.Generation = 2
	initial.Spec.DefaultTarget = &squirrelv1alpha1.Target{Registry: "mirror.internal"}
	initial.Spec.Rules = []squirrelv1alpha1.Rule{
		{Match: matchString(t, "docker.io/**:*")},
	}
	if err := cli.Update(context.Background(), &initial); err != nil {
		t.Fatalf("Update: %v", err)
	}

	reconcileCIP(t, r)

	var updated squirrelv1alpha1.ClusterImagePolicy
	if err := cli.Get(context.Background(), client.ObjectKey{Name: cipName}, &updated); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if updated.Status.ObservedGeneration != 2 {
		t.Errorf("ObservedGeneration: got %d, want 2", updated.Status.ObservedGeneration)
	}
	cond = apimeta.FindStatusCondition(updated.Status.Conditions, squirrelv1alpha1.ConditionAccepted)
	if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != squirrelv1alpha1.ReasonAccepted {
		t.Errorf("after spec fix: got %+v, want True/Accepted", cond)
	}
}

func TestClusterImagePolicyReconcileMissingPolicyIsNoOp(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := squirrelv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	cli := fakeclient.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&squirrelv1alpha1.ClusterImagePolicy{}).
		Build()
	r := &controller.ClusterImagePolicyReconciler{Client: cli}

	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "does-not-exist"}})
	if err != nil {
		t.Errorf("Reconcile on missing policy: got err %v, want nil (deleted between watch and reconcile is normal)", err)
	}
}
