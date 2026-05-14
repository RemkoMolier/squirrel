package v1alpha1_test

import (
	"encoding/json"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	runtime "k8s.io/apimachinery/pkg/runtime"

	squirrelv1alpha1 "github.com/RemkoMolier/squirrel/api/v1alpha1"
)

func TestAddToSchemeRegistersAllFourKinds(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := squirrelv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}

	known := scheme.AllKnownTypes()
	for _, kind := range []string{
		"ClusterImagePolicy",
		"ClusterImagePolicyList",
		"ImagePolicy",
		"ImagePolicyList",
	} {
		gvk := squirrelv1alpha1.GroupVersion.WithKind(kind)
		if _, ok := known[gvk]; !ok {
			t.Errorf("scheme is missing GVK %v", gvk)
		}
	}
}

func TestClusterImagePolicyRoundTripJSON(t *testing.T) {
	t.Parallel()

	in := &squirrelv1alpha1.ClusterImagePolicy{
		TypeMeta: metav1.TypeMeta{
			APIVersion: squirrelv1alpha1.GroupVersion.String(),
			Kind:       "ClusterImagePolicy",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: "dockerhub-mirror",
		},
	}

	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var out squirrelv1alpha1.ClusterImagePolicy
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if out.Name != "dockerhub-mirror" {
		t.Errorf("Name: got %q, want %q", out.Name, "dockerhub-mirror")
	}
	if got, want := out.APIVersion, squirrelv1alpha1.GroupVersion.String(); got != want {
		t.Errorf("APIVersion: got %q, want %q", got, want)
	}
	if got, want := out.Kind, "ClusterImagePolicy"; got != want {
		t.Errorf("Kind: got %q, want %q", got, want)
	}
}

func TestImagePolicyRoundTripJSON(t *testing.T) {
	t.Parallel()

	in := &squirrelv1alpha1.ImagePolicy{
		TypeMeta: metav1.TypeMeta{
			APIVersion: squirrelv1alpha1.GroupVersion.String(),
			Kind:       "ImagePolicy",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "payments-nginx-override",
			Namespace: "payments",
		},
	}

	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var out squirrelv1alpha1.ImagePolicy
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if out.Name != "payments-nginx-override" || out.Namespace != "payments" {
		t.Errorf("got Name=%q Namespace=%q, want Name=%q Namespace=%q",
			out.Name, out.Namespace, "payments-nginx-override", "payments")
	}
}

func TestDeepCopyClusterImagePolicyIsIndependent(t *testing.T) {
	t.Parallel()

	orig := &squirrelv1alpha1.ClusterImagePolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "mirror",
			Labels: map[string]string{"tier": "production"},
		},
	}

	clone := orig.DeepCopy()
	if clone == orig {
		t.Fatal("DeepCopy returned the same pointer; expected a new value")
	}
	if clone.Name != orig.Name {
		t.Errorf("clone name: got %q, want %q", clone.Name, orig.Name)
	}

	// Mutating the clone must not affect the original.
	clone.Name = "mirror-modified"
	clone.Labels["tier"] = "staging"
	if orig.Name == "mirror-modified" {
		t.Error("DeepCopy: mutation of clone.Name affected the original")
	}
	if orig.Labels["tier"] != "production" {
		t.Errorf("DeepCopy: mutation of clone.Labels affected the original; got %q, want %q",
			orig.Labels["tier"], "production")
	}
}

func TestDeepCopyImplementsRuntimeObject(t *testing.T) {
	t.Parallel()

	var _ runtime.Object = (*squirrelv1alpha1.ClusterImagePolicy)(nil)
	var _ runtime.Object = (*squirrelv1alpha1.ClusterImagePolicyList)(nil)
	var _ runtime.Object = (*squirrelv1alpha1.ImagePolicy)(nil)
	var _ runtime.Object = (*squirrelv1alpha1.ImagePolicyList)(nil)
}
