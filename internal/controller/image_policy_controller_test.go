package controller_test

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
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

// These tests mirror the ClusterImagePolicy reconciler tests. The two
// reconcilers share validation and status-update plumbing; the
// difference is only the kind (and the namespaced scope, exercised
// here via the test namespace).

const (
	ipName      = "test-policy"
	ipNamespace = "payments"
)

func newImagePolicyTestEnv(t *testing.T, policy *squirrelv1alpha1.ImagePolicy, extra ...client.Object) (client.Client, *controller.ImagePolicyReconciler) {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := squirrelv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(corev1): %v", err)
	}

	objs := append([]client.Object{policy}, extra...)
	cli := fakeclient.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&squirrelv1alpha1.ImagePolicy{}).
		Build()

	return cli, &controller.ImagePolicyReconciler{Client: cli}
}

func reconcileIP(t *testing.T, r *controller.ImagePolicyReconciler) {
	t.Helper()
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: ipName, Namespace: ipNamespace}})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
}

func TestImagePolicyReconcileAccepted(t *testing.T) {
	t.Parallel()

	policy := &squirrelv1alpha1.ImagePolicy{
		ObjectMeta: metav1.ObjectMeta{Name: ipName, Namespace: ipNamespace, Generation: 1},
		Spec: squirrelv1alpha1.ImagePolicySpec{
			Rules: []squirrelv1alpha1.Rule{{
				Match: matchString(t, "docker.io/library/nginx:*"),
				Target: &squirrelv1alpha1.Target{
					Registry:   "registry.internal",
					Repository: "payments/nginx-patched",
					Tags:       []string{"{tag}"},
				},
			}},
		},
	}
	cli, r := newImagePolicyTestEnv(t, policy)
	reconcileIP(t, r)

	var got squirrelv1alpha1.ImagePolicy
	if err := cli.Get(context.Background(), client.ObjectKey{Name: ipName, Namespace: ipNamespace}, &got); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status.ObservedGeneration != 1 {
		t.Errorf("ObservedGeneration: got %d, want 1", got.Status.ObservedGeneration)
	}
	cond := apimeta.FindStatusCondition(got.Status.Conditions, squirrelv1alpha1.ConditionAccepted)
	if cond == nil {
		t.Fatal("Accepted condition missing")
	}
	if cond.Status != metav1.ConditionTrue || cond.Reason != squirrelv1alpha1.ReasonAccepted {
		t.Errorf("Condition: got Status=%q Reason=%q, want True/Accepted", cond.Status, cond.Reason)
	}
}

// TestImagePolicyReconcileWarnsOnUnoptedNamespace pins the inert-
// policy warning: a well-formed ImagePolicy in a namespace that
// lacks the squirrel.molier.dev/enabled=true label is Accepted=True
// (the spec is valid) but with Reason=NamespaceNotOptedIn so
// operators reading `kubectl describe imagepolicy` can immediately
// see the policy is inert and why. The MWC namespaceSelector gates
// the webhook on the label, so an unlabeled namespace's policies
// never fire at admission time.
func TestImagePolicyReconcileWarnsOnUnoptedNamespace(t *testing.T) {
	t.Parallel()

	policy := &squirrelv1alpha1.ImagePolicy{
		ObjectMeta: metav1.ObjectMeta{Name: ipName, Namespace: ipNamespace, Generation: 1},
		Spec: squirrelv1alpha1.ImagePolicySpec{
			Rules: []squirrelv1alpha1.Rule{{
				Match: matchString(t, "docker.io/library/nginx:*"),
				Target: &squirrelv1alpha1.Target{
					Registry: "registry.internal",
				},
			}},
		},
	}
	// Namespace exists but has NO opt-in label.
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ipNamespace}}
	cli, r := newImagePolicyTestEnv(t, policy, ns)
	reconcileIP(t, r)

	var got squirrelv1alpha1.ImagePolicy
	if err := cli.Get(context.Background(), client.ObjectKey{Name: ipName, Namespace: ipNamespace}, &got); err != nil {
		t.Fatalf("Get: %v", err)
	}
	cond := apimeta.FindStatusCondition(got.Status.Conditions, squirrelv1alpha1.ConditionAccepted)
	if cond == nil {
		t.Fatal("Accepted condition missing")
	}
	if cond.Status != metav1.ConditionTrue {
		t.Errorf("Status: got %q, want True (spec is valid; warning is informational)", cond.Status)
	}
	if cond.Reason != squirrelv1alpha1.ReasonNamespaceNotOptedIn {
		t.Errorf("Reason: got %q, want %q", cond.Reason, squirrelv1alpha1.ReasonNamespaceNotOptedIn)
	}
	if !strings.Contains(cond.Message, "opt-in label") {
		t.Errorf("Message: got %q, want mention of the opt-in label", cond.Message)
	}
}

// TestImagePolicyReconcileNoWarningOnOptedInNamespace pins the
// positive path: when the namespace carries the opt-in label, the
// reconciler reports Reason=Accepted with no inert warning.
func TestImagePolicyReconcileNoWarningOnOptedInNamespace(t *testing.T) {
	t.Parallel()

	policy := &squirrelv1alpha1.ImagePolicy{
		ObjectMeta: metav1.ObjectMeta{Name: ipName, Namespace: ipNamespace, Generation: 1},
		Spec: squirrelv1alpha1.ImagePolicySpec{
			Rules: []squirrelv1alpha1.Rule{{
				Match: matchString(t, "docker.io/library/nginx:*"),
				Target: &squirrelv1alpha1.Target{
					Registry: "registry.internal",
				},
			}},
		},
	}
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   ipNamespace,
			Labels: map[string]string{squirrelv1alpha1.OptInLabel: "true"},
		},
	}
	cli, r := newImagePolicyTestEnv(t, policy, ns)
	reconcileIP(t, r)

	var got squirrelv1alpha1.ImagePolicy
	if err := cli.Get(context.Background(), client.ObjectKey{Name: ipName, Namespace: ipNamespace}, &got); err != nil {
		t.Fatalf("Get: %v", err)
	}
	cond := apimeta.FindStatusCondition(got.Status.Conditions, squirrelv1alpha1.ConditionAccepted)
	if cond == nil {
		t.Fatal("Accepted condition missing")
	}
	if cond.Reason != squirrelv1alpha1.ReasonAccepted {
		t.Errorf("Reason: got %q, want %q (opted-in namespace must not produce the inert warning)", cond.Reason, squirrelv1alpha1.ReasonAccepted)
	}
}

func TestImagePolicyReconcileRejectsEmptyRules(t *testing.T) {
	t.Parallel()

	policy := &squirrelv1alpha1.ImagePolicy{
		ObjectMeta: metav1.ObjectMeta{Name: ipName, Namespace: ipNamespace, Generation: 1},
	}
	cli, r := newImagePolicyTestEnv(t, policy)
	reconcileIP(t, r)

	var got squirrelv1alpha1.ImagePolicy
	if err := cli.Get(context.Background(), client.ObjectKey{Name: ipName, Namespace: ipNamespace}, &got); err != nil {
		t.Fatalf("Get: %v", err)
	}
	cond := apimeta.FindStatusCondition(got.Status.Conditions, squirrelv1alpha1.ConditionAccepted)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != squirrelv1alpha1.ReasonEmptyRules {
		t.Errorf("Condition: got %+v, want False/EmptyRules", cond)
	}
}

// TestImagePolicyReconcileDoesNotValidateNamespaceSelector pins the
// asymmetry between the two reconcilers: only ClusterImagePolicy has
// a NamespaceSelector spec field, so the namespaced reconciler must
// never produce a ReasonInvalidNamespaceSelector condition. The
// reconciler relies on the field being structurally absent from the
// ImagePolicy type rather than calling ValidateNamespaceSelector
// at all; this test pins that absence so a future refactor that
// shared the cluster-scoped validation pipeline accidentally does
// not start raising the wrong reason on namespaced policies.
func TestImagePolicyReconcileDoesNotValidateNamespaceSelector(t *testing.T) {
	t.Parallel()

	policy := &squirrelv1alpha1.ImagePolicy{
		ObjectMeta: metav1.ObjectMeta{Name: ipName, Namespace: ipNamespace, Generation: 1},
		Spec: squirrelv1alpha1.ImagePolicySpec{
			Rules: []squirrelv1alpha1.Rule{{
				Match: matchString(t, "docker.io/library/nginx:*"),
				Target: &squirrelv1alpha1.Target{
					Registry: "mirror.internal",
				},
			}},
		},
	}
	cli, r := newImagePolicyTestEnv(t, policy)
	reconcileIP(t, r)

	var got squirrelv1alpha1.ImagePolicy
	if err := cli.Get(context.Background(), client.ObjectKey{Name: ipName, Namespace: ipNamespace}, &got); err != nil {
		t.Fatalf("Get: %v", err)
	}
	cond := apimeta.FindStatusCondition(got.Status.Conditions, squirrelv1alpha1.ConditionAccepted)
	if cond == nil {
		t.Fatal("Accepted condition missing")
	}
	if cond.Reason == squirrelv1alpha1.ReasonInvalidNamespaceSelector {
		t.Errorf("Reason: got %q on an ImagePolicy; the namespaced reconciler must not emit selector validation reasons", cond.Reason)
	}
}

func TestImagePolicyReconcileSurfacesActionTargetConflict(t *testing.T) {
	t.Parallel()

	policy := &squirrelv1alpha1.ImagePolicy{
		ObjectMeta: metav1.ObjectMeta{Name: ipName, Namespace: ipNamespace, Generation: 1},
		Spec: squirrelv1alpha1.ImagePolicySpec{
			Rules: []squirrelv1alpha1.Rule{{
				Match:  matchString(t, "docker.io/library/distroless-base:*"),
				Action: squirrelv1alpha1.ActionSkip,
				Target: &squirrelv1alpha1.Target{Registry: "mirror.internal"},
			}},
		},
	}
	cli, r := newImagePolicyTestEnv(t, policy)
	reconcileIP(t, r)

	var got squirrelv1alpha1.ImagePolicy
	if err := cli.Get(context.Background(), client.ObjectKey{Name: ipName, Namespace: ipNamespace}, &got); err != nil {
		t.Fatalf("Get: %v", err)
	}
	cond := apimeta.FindStatusCondition(got.Status.Conditions, squirrelv1alpha1.ConditionAccepted)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != squirrelv1alpha1.ReasonActionTargetConflict {
		t.Errorf("Condition: got %+v, want False/ActionTargetConflict", cond)
	}
}

func TestImagePolicyReconcileIsIdempotent(t *testing.T) {
	t.Parallel()

	policy := &squirrelv1alpha1.ImagePolicy{
		ObjectMeta: metav1.ObjectMeta{Name: ipName, Namespace: ipNamespace, Generation: 1},
		Spec: squirrelv1alpha1.ImagePolicySpec{
			Rules: []squirrelv1alpha1.Rule{{
				Match:  matchString(t, "docker.io/**:*"),
				Target: &squirrelv1alpha1.Target{Registry: "mirror.internal"},
			}},
		},
	}
	cli, r := newImagePolicyTestEnv(t, policy)
	reconcileIP(t, r)

	var afterFirst squirrelv1alpha1.ImagePolicy
	if err := cli.Get(context.Background(), client.ObjectKey{Name: ipName, Namespace: ipNamespace}, &afterFirst); err != nil {
		t.Fatalf("Get: %v", err)
	}
	rvFirst := afterFirst.ResourceVersion

	reconcileIP(t, r)

	var afterSecond squirrelv1alpha1.ImagePolicy
	if err := cli.Get(context.Background(), client.ObjectKey{Name: ipName, Namespace: ipNamespace}, &afterSecond); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if afterSecond.ResourceVersion != rvFirst {
		t.Errorf("idempotency violated: ResourceVersion changed from %q to %q on a no-op reconcile",
			rvFirst, afterSecond.ResourceVersion)
	}
}

func TestImagePolicyReconcileMissingPolicyIsNoOp(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := squirrelv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	cli := fakeclient.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&squirrelv1alpha1.ImagePolicy{}).
		Build()
	r := &controller.ImagePolicyReconciler{Client: cli}

	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "does-not-exist", Namespace: "other"}})
	if err != nil {
		t.Errorf("Reconcile on missing policy: got err %v, want nil", err)
	}
}
