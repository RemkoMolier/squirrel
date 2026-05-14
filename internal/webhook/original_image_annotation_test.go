package webhook

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestOriginalImageAnnotationKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		container, want string
	}{
		{"main", "original-image.squirrel.molier.dev/main"},
		{"init-db", "original-image.squirrel.molier.dev/init-db"},
		{"a.b.c", "original-image.squirrel.molier.dev/a.b.c"},
	}
	for _, tt := range tests {
		got := originalImageAnnotationKey(tt.container)
		if got != tt.want {
			t.Errorf("originalImageAnnotationKey(%q) = %q, want %q", tt.container, got, tt.want)
		}
	}
}

func TestHasOriginalImagePrefix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		key  string
		want bool
	}{
		{"original-image.squirrel.molier.dev/main", true},
		{"original-image.squirrel.molier.dev/", false}, // bare prefix has no container name; not squirrel-written
		{"squirrel.molier.dev/rewrites", false},        // legacy single-annotation key
		{"original-image.squirrel.molier.dev", false},  // missing trailing slash
		{"", false},
	}
	for _, tt := range tests {
		got := hasOriginalImagePrefix(tt.key)
		if got != tt.want {
			t.Errorf("hasOriginalImagePrefix(%q) = %t, want %t", tt.key, got, tt.want)
		}
	}
}

func TestContainerNamesInPod(t *testing.T) {
	t.Parallel()

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "test"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "main"},
				{Name: "sidecar"},
			},
			InitContainers: []corev1.Container{
				{Name: "init"},
			},
			EphemeralContainers: []corev1.EphemeralContainer{
				{EphemeralContainerCommon: corev1.EphemeralContainerCommon{Name: "debug"}},
			},
		},
	}
	got := containerNamesInPod(pod)
	want := []string{"main", "sidecar", "init", "debug"}
	if len(got) != len(want) {
		t.Fatalf("containerNamesInPod: got %d, want %d (got=%v)", len(got), len(want), got)
	}
	for _, name := range want {
		if _, ok := got[name]; !ok {
			t.Errorf("container %q missing from set", name)
		}
	}
}
