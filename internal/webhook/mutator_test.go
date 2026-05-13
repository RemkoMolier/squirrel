package webhook_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"

	jsonpatch "github.com/evanphx/json-patch/v5"
	promtestutil "github.com/prometheus/client_golang/prometheus/testutil"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	runtime "k8s.io/apimachinery/pkg/runtime"
	client "sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"
	interceptor "sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	admission "sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	squirrelv1alpha1 "github.com/RemkoMolier/squirrel/api/v1alpha1"
	"github.com/RemkoMolier/squirrel/internal/metrics"
	"github.com/RemkoMolier/squirrel/internal/webhook"
)

// testNamespace is the namespace every mutator test runs against;
// keeping it package-private makes assertions readable and lets the
// `default` fake-client fixture live in one place.
const testNamespace = "default"

// newMutatorTestEnv builds a fake client (with corev1 + squirrel
// schemes) preloaded with the given objects, and returns the mutator
// wired to it. The target namespace (testNamespace) is always
// installed so the handler can find it.
func newMutatorTestEnv(t *testing.T, extras ...client.Object) *webhook.PodMutator {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("corev1.AddToScheme: %v", err)
	}
	if err := squirrelv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("squirrel.AddToScheme: %v", err)
	}

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   testNamespace,
			Labels: map[string]string{"squirrel.molier.dev/enabled": "true"},
		},
	}
	objs := append([]client.Object{ns}, extras...)

	cli := fakeclient.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&squirrelv1alpha1.ClusterImagePolicy{}, &squirrelv1alpha1.ImagePolicy{}).
		Build()

	return &webhook.PodMutator{
		Client:  cli,
		Decoder: admission.NewDecoder(scheme),
	}
}

// createRequest builds a CREATE admission.Request against the given
// pod. The Namespace field on the request mirrors the pod's
// namespace.
func createRequest(t *testing.T, pod *corev1.Pod) admission.Request {
	t.Helper()
	raw, err := json.Marshal(pod)
	if err != nil {
		t.Fatalf("marshal pod: %v", err)
	}
	return admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Operation: admissionv1.Create,
			Namespace: pod.Namespace,
			Object:    runtime.RawExtension{Raw: raw},
		},
	}
}

// updateRequest builds an admission.Request for an UPDATE: both the
// new and the pre-update Pod are encoded. Real apiserver requests
// always supply OldObject on UPDATE; the helper mirrors that shape
// so the handler exercises its "skip unchanged images" path.
func updateRequest(t *testing.T, newPod, oldPod *corev1.Pod) admission.Request {
	t.Helper()
	newRaw, err := json.Marshal(newPod)
	if err != nil {
		t.Fatalf("marshal new pod: %v", err)
	}
	oldRaw, err := json.Marshal(oldPod)
	if err != nil {
		t.Fatalf("marshal old pod: %v", err)
	}
	return admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Operation: admissionv1.Update,
			Namespace: newPod.Namespace,
			Object:    runtime.RawExtension{Raw: newRaw},
			OldObject: runtime.RawExtension{Raw: oldRaw},
		},
	}
}

// mirrorPolicy returns an Accepted ClusterImagePolicy that rewrites
// every docker.io image to mirror.internal preserving the repository
// and tag.
func mirrorPolicy() *squirrelv1alpha1.ClusterImagePolicy {
	return &squirrelv1alpha1.ClusterImagePolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "dockerhub-mirror", Generation: 1},
		Spec: squirrelv1alpha1.ClusterImagePolicySpec{
			DefaultTarget: &squirrelv1alpha1.Target{Registry: "mirror.internal"},
			Rules:         []squirrelv1alpha1.Rule{{Match: match("docker.io/**:*")}},
		},
		Status: squirrelv1alpha1.ClusterImagePolicyStatus{
			Conditions: []metav1.Condition{acceptedCondition(1)},
		},
	}
}

func podWithContainers(namespace string, images ...string) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: namespace,
		},
		Spec: corev1.PodSpec{
			Containers: make([]corev1.Container, len(images)),
		},
	}
	for i, img := range images {
		pod.Spec.Containers[i] = corev1.Container{
			Name:  "c" + strconv.Itoa(i),
			Image: img,
		}
	}
	return pod
}

func TestPodMutatorAllowsWhenNoPoliciesInstalled(t *testing.T) {
	t.Parallel()

	mutator := newMutatorTestEnv(t)
	pod := podWithContainers("default", "nginx:1.21")
	resp := mutator.Handle(context.Background(), createRequest(t, pod))

	if !resp.Allowed {
		t.Fatalf("Allowed: got false, want true (no policies should never block admission)")
	}
	if len(resp.Patches) != 0 {
		t.Errorf("Patches: got %d, want 0", len(resp.Patches))
	}
}

func TestPodMutatorAllowsWhenNoContainerMatches(t *testing.T) {
	t.Parallel()

	mutator := newMutatorTestEnv(t, mirrorPolicy())
	// gcr.io does not match the docker.io/**:* policy.
	pod := podWithContainers("default", "gcr.io/google_containers/etcd:3.5")
	resp := mutator.Handle(context.Background(), createRequest(t, pod))

	if !resp.Allowed {
		t.Fatalf("Allowed: got false, want true")
	}
	if len(resp.Patches) != 0 {
		t.Errorf("Patches: got %d, want 0", len(resp.Patches))
	}
}

func TestPodMutatorRewritesMatchingContainer(t *testing.T) {
	t.Parallel()

	mutator := newMutatorTestEnv(t, mirrorPolicy())
	pod := podWithContainers("default", "docker.io/library/nginx:1.21")
	resp := mutator.Handle(context.Background(), createRequest(t, pod))

	if !resp.Allowed {
		t.Fatalf("Allowed: got false, want true")
	}
	if len(resp.Patches) == 0 {
		t.Fatal("Patches: got 0, want >=1 (image should be rewritten)")
	}

	patched := applyPatchToPod(t, pod, resp)
	if got, want := patched.Spec.Containers[0].Image, "mirror.internal/library/nginx:1.21"; got != want {
		t.Errorf("Container image: got %q, want %q", got, want)
	}
	key := webhook.OriginalImageAnnotationPrefix + "c0"
	if got, want := patched.Annotations[key], "docker.io/library/nginx:1.21"; got != want {
		t.Errorf("annotation %q: got %q, want %q", key, got, want)
	}
}

// TestPodMutatorRewritesContainersAndInitContainersOnNormalAdmission
// covers the standard `pods` CREATE/UPDATE path: spec.containers and
// spec.initContainers are rewritten, spec.ephemeralContainers must
// NOT be (the apiserver makes that slice immutable on this path; any
// patch op against it would cause pod-strategy validation to reject
// the whole admission and break unrelated updates to Pods that have
// pre-existing ephemeral containers).
func TestPodMutatorRewritesContainersAndInitContainersOnNormalAdmission(t *testing.T) {
	t.Parallel()

	mutator := newMutatorTestEnv(t, mirrorPolicy())
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "main", Image: "docker.io/library/nginx:1.21"},
			},
			InitContainers: []corev1.Container{
				{Name: "init", Image: "docker.io/library/busybox:1.36"},
			},
			// Pre-existing ephemeral container - this would have been
			// added via the pods/ephemeralcontainers subresource at some
			// earlier point. On the regular pods path the apiserver
			// will not accept patches against this slice, so the
			// webhook must leave it alone.
			EphemeralContainers: []corev1.EphemeralContainer{
				{
					EphemeralContainerCommon: corev1.EphemeralContainerCommon{
						Name:  "debug",
						Image: "docker.io/library/alpine:3",
					},
				},
			},
		},
	}
	resp := mutator.Handle(context.Background(), createRequest(t, pod))
	if !resp.Allowed || len(resp.Patches) == 0 {
		t.Fatalf("expected patched response, got Allowed=%t Patches=%d", resp.Allowed, len(resp.Patches))
	}

	patched := applyPatchToPod(t, pod, resp)
	if got, want := patched.Spec.Containers[0].Image, "mirror.internal/library/nginx:1.21"; got != want {
		t.Errorf("main container: got %q, want %q", got, want)
	}
	if got, want := patched.Spec.InitContainers[0].Image, "mirror.internal/library/busybox:1.36"; got != want {
		t.Errorf("init container: got %q, want %q", got, want)
	}
	if got, want := patched.Spec.EphemeralContainers[0].Image, "docker.io/library/alpine:3"; got != want {
		t.Errorf("ephemeral container: got %q, want %q (must be untouched on the normal pods path)", got, want)
	}

	// Per-container annotations: keys for the containers we
	// rewrote should exist; the ephemeral container - which the
	// apiserver would reject a patch op for on this path - must
	// have no annotation.
	for _, container := range []string{"main", "init"} {
		key := webhook.OriginalImageAnnotationPrefix + container
		if _, ok := patched.Annotations[key]; !ok {
			t.Errorf("annotation %q missing", key)
		}
	}
	debugKey := webhook.OriginalImageAnnotationPrefix + "debug"
	if _, ok := patched.Annotations[debugKey]; ok {
		t.Errorf("annotation %q present; must be absent on the normal pods path", debugKey)
	}
}

// TestPodMutatorEphemeralContainersSubresourceLeavesContainersUntouched
// covers the subresource-mutability rule. `kubectl debug` adds an
// ephemeral container via the pods/ephemeralcontainers subresource,
// not a normal Pod update. On that subresource the apiserver only
// accepts mutations to spec.ephemeralContainers; any patch op
// touching spec.containers or spec.initContainers would be rejected
// by pod-strategy validation. The handler must therefore skip the
// regular/init loops when SubResource=="ephemeralcontainers" - even
// when those containers carry images the policy would otherwise
// rewrite (the common case of `kubectl debug` against a Pod created
// before the operator was installed).
func TestPodMutatorEphemeralContainersSubresourceLeavesContainersUntouched(t *testing.T) {
	t.Parallel()

	mutator := newMutatorTestEnv(t, mirrorPolicy())
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: testNamespace},
		Spec: corev1.PodSpec{
			// Pre-existing un-mirrored container (created before the
			// policy/operator). Without the subresource gate, the
			// handler would emit a patch op on spec.containers[0].image
			// here and the apiserver would reject the whole admission.
			Containers: []corev1.Container{
				{Name: "main", Image: "docker.io/library/nginx:1.21"},
			},
			InitContainers: []corev1.Container{
				{Name: "init", Image: "docker.io/library/busybox:1.36"},
			},
			// The new ephemeral container being added by `kubectl
			// debug`. This is the only field the subresource allows
			// the webhook to mutate.
			EphemeralContainers: []corev1.EphemeralContainer{
				{
					EphemeralContainerCommon: corev1.EphemeralContainerCommon{
						Name:  "debug",
						Image: "docker.io/library/alpine:3",
					},
				},
			},
		},
	}
	raw, err := json.Marshal(pod)
	if err != nil {
		t.Fatalf("marshal pod: %v", err)
	}
	req := admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Operation: admissionv1.Update,
			Namespace: pod.Namespace,
			// Literal matches webhook.ephemeralContainersSubresource;
			// the constant is package-private on purpose, and pinning
			// the apimachinery value here documents the contract.
			SubResource: "ephemeralcontainers",
			Object:      runtime.RawExtension{Raw: raw},
		},
	}
	resp := mutator.Handle(context.Background(), req)
	if !resp.Allowed {
		t.Fatalf("Allowed: got false, want true")
	}

	patched := applyPatchToPod(t, pod, resp)

	// The ephemeral container's image is the only one the handler is
	// allowed to rewrite on this subresource.
	if got, want := patched.Spec.EphemeralContainers[0].Image, "mirror.internal/library/alpine:3"; got != want {
		t.Errorf("ephemeral container: got %q, want %q", got, want)
	}
	if got, want := patched.Spec.Containers[0].Image, "docker.io/library/nginx:1.21"; got != want {
		t.Errorf("regular container: got %q, want %q (must be untouched on ephemeralcontainers subresource)", got, want)
	}
	if got, want := patched.Spec.InitContainers[0].Image, "docker.io/library/busybox:1.36"; got != want {
		t.Errorf("init container: got %q, want %q (must be untouched on ephemeralcontainers subresource)", got, want)
	}

	// metadata.annotations is immutable on the
	// pods/ephemeralcontainers subresource: emitting a patch op
	// against it would cause pod-strategy validation to reject the
	// whole admission and break `kubectl debug`. The handler must
	// therefore leave annotations untouched on this path, even
	// though that means operators lose the squirrel original-image
	// breadcrumb for debug containers.
	for k := range patched.Annotations {
		if strings.HasPrefix(k, webhook.OriginalImageAnnotationPrefix) {
			t.Errorf("annotation %q present; must not be set on ephemeralcontainers subresource", k)
		}
	}
}

func TestPodMutatorMixedContainersOnlyPatchesMatches(t *testing.T) {
	t.Parallel()

	mutator := newMutatorTestEnv(t, mirrorPolicy())
	pod := podWithContainers("default",
		"docker.io/library/nginx:1.21",
		"gcr.io/google_containers/etcd:3.5",
		"docker.io/library/redis:7",
	)
	resp := mutator.Handle(context.Background(), createRequest(t, pod))
	if !resp.Allowed || len(resp.Patches) == 0 {
		t.Fatalf("expected patched response")
	}
	patched := applyPatchToPod(t, pod, resp)

	if got, want := patched.Spec.Containers[0].Image, "mirror.internal/library/nginx:1.21"; got != want {
		t.Errorf("container 0 (matched): got %q, want %q", got, want)
	}
	if got, want := patched.Spec.Containers[1].Image, "gcr.io/google_containers/etcd:3.5"; got != want {
		t.Errorf("container 1 (no match): got %q, want %q (must be untouched)", got, want)
	}
	if got, want := patched.Spec.Containers[2].Image, "mirror.internal/library/redis:7"; got != want {
		t.Errorf("container 2 (matched): got %q, want %q", got, want)
	}
}

func TestPodMutatorSkipRuleDoesNotPatch(t *testing.T) {
	t.Parallel()

	skipPolicy := &squirrelv1alpha1.ClusterImagePolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "allow-direct", Generation: 1},
		Spec: squirrelv1alpha1.ClusterImagePolicySpec{
			Rules: []squirrelv1alpha1.Rule{{
				Match:  match("docker.io/library/distroless-base:*"),
				Action: squirrelv1alpha1.ActionSkip,
			}},
		},
		Status: squirrelv1alpha1.ClusterImagePolicyStatus{
			Conditions: []metav1.Condition{acceptedCondition(1)},
		},
	}
	mutator := newMutatorTestEnv(t, mirrorPolicy(), skipPolicy)
	pod := podWithContainers("default", "docker.io/library/distroless-base:1")
	resp := mutator.Handle(context.Background(), createRequest(t, pod))

	if !resp.Allowed {
		t.Fatalf("Allowed: got false, want true")
	}
	if len(resp.Patches) != 0 {
		t.Errorf("Patches: got %d, want 0 (skip rule must beat the mirror rule and leave the image alone)", len(resp.Patches))
	}
}

func TestPodMutatorUpdateRewritesOnlyChangedImages(t *testing.T) {
	t.Parallel()

	// Real K8s UPDATE admissions always carry OldObject. The handler
	// uses that to skip containers whose image is unchanged - the
	// "create with mirrored image, then patch to upstream" bypass is
	// still closed because the upstream patch *changes* the image,
	// so the engine re-evaluates and rewrites it.
	mutator := newMutatorTestEnv(t, mirrorPolicy())
	oldPod := podWithContainers("default",
		"mirror.internal/library/nginx:1.21", // unchanged
		"docker.io/library/redis:6",          // pre-update image
	)
	newPod := podWithContainers("default",
		"mirror.internal/library/nginx:1.21", // unchanged
		"docker.io/library/redis:7",          // user-changed
	)
	resp := mutator.Handle(context.Background(), updateRequest(t, newPod, oldPod))

	if !resp.Allowed || len(resp.Patches) == 0 {
		t.Fatalf("expected patched response on UPDATE")
	}
	patched := applyPatchToPod(t, newPod, resp)
	if got, want := patched.Spec.Containers[1].Image, "mirror.internal/library/redis:7"; got != want {
		t.Errorf("container 1 (user-changed): got %q, want %q", got, want)
	}
	if got, want := patched.Spec.Containers[0].Image, "mirror.internal/library/nginx:1.21"; got != want {
		t.Errorf("container 0 (unchanged): got %q, want %q", got, want)
	}
}

// catchAllMirrorPolicy nests the registry+repository into a single
// mirror path. Crucially the rewritten output (mirror.internal/<reg>/
// <repo>:<tag>) itself matches the rule again - prepending another
// mirror.internal on each re-evaluation. The handler must short-circuit
// on UPDATE when the image is unchanged, or this rule compounds.
func catchAllMirrorPolicy() *squirrelv1alpha1.ClusterImagePolicy {
	return &squirrelv1alpha1.ClusterImagePolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "catch-all-mirror", Generation: 1},
		Spec: squirrelv1alpha1.ClusterImagePolicySpec{
			DefaultTarget: &squirrelv1alpha1.Target{
				Registry:   "mirror.internal",
				Repository: "{registry}/{repository}",
			},
			Rules: []squirrelv1alpha1.Rule{{Match: match("**:*")}},
		},
		Status: squirrelv1alpha1.ClusterImagePolicyStatus{
			Conditions: []metav1.Condition{acceptedCondition(1)},
		},
	}
}

// TestPodMutatorCreateStripsUserSuppliedRewritesAnnotationOnNoOp
// pins the audit-trail provenance contract: on CREATE the in-flight
// original-image annotations are necessarily user-supplied (the
// operator has not admitted this Pod yet). Even when no policy
// matches and no rewrite is performed, the handler emits a JSON
// Patch to strip every original-image annotation so `kubectl describe
// pod` cannot show attacker-controlled "squirrel rewrote X -> Y" trails.
func TestPodMutatorCreateStripsUserSuppliedRewritesAnnotationOnNoOp(t *testing.T) {
	t.Parallel()

	mutator := newMutatorTestEnv(t, mirrorPolicy())
	// gcr.io image: the docker.io/** policy does not match it.
	pod := podWithContainers("default", "gcr.io/google_containers/etcd:3.5")
	forgedKey := webhook.OriginalImageAnnotationPrefix + "c0"
	pod.Annotations = map[string]string{
		forgedKey: "docker.io/library/nginx:1.21",
	}
	resp := mutator.Handle(context.Background(), createRequest(t, pod))

	if !resp.Allowed {
		t.Fatalf("Allowed: got false, want true")
	}
	if len(resp.Patches) == 0 {
		t.Fatalf("Patches: 0, want >=1 (the user-supplied annotation must be removed)")
	}
	patched := applyPatchToPod(t, pod, resp)
	if got := patched.Annotations[forgedKey]; got != "" {
		t.Errorf("annotation %q: got %q, want empty (user-supplied annotation must be removed on CREATE)", forgedKey, got)
	}
}

// TestPodMutatorCreateIgnoresUserSuppliedRewritesAnnotation pins
// the policy-bypass guard: on CREATE any original-image annotation
// is necessarily user-supplied. The handler must ignore it and
// re-run the engine on every container - otherwise a user with
// create-Pod permission could forge entries claiming "this image was
// already rewritten" and bypass the applicable rules.
func TestPodMutatorCreateIgnoresUserSuppliedRewritesAnnotation(t *testing.T) {
	t.Parallel()

	mutator := newMutatorTestEnv(t, mirrorPolicy())
	// Forged annotation claiming the image is its own original.
	pod := podWithContainers("default", "docker.io/library/nginx:1.21")
	pod.Annotations = map[string]string{
		webhook.OriginalImageAnnotationPrefix + "c0": "docker.io/library/nginx:1.21",
	}
	resp := mutator.Handle(context.Background(), createRequest(t, pod))

	if !resp.Allowed {
		t.Fatalf("Allowed: got false, want true")
	}
	if len(resp.Patches) == 0 {
		t.Errorf("Patches: got 0, want >=1 (engine must re-evaluate; user annotation is not trusted)")
	}
	patched := applyPatchToPod(t, pod, resp)
	if got, want := patched.Spec.Containers[0].Image, "mirror.internal/library/nginx:1.21"; got != want {
		t.Errorf("Container image: got %q, want %q", got, want)
	}
}

// TestPodMutatorCreateDoesNotRecursivelyRewriteOnReinvocation pins
// the fix for the reinvocation-recursion bug: the apiserver's
// reinvocationPolicy=IfNeeded lets another mutating webhook touch a
// Pod after our first pass and trigger a second admission of the
// same CREATE. On that second pass the in-flight Pod carries our
// own prefix annotation from the first pass AND its container image
// is already the engine's output. A naive handler would re-run a
// catch-all mirror rule against the rewritten image and produce
// "mirror.internal/mirror.internal/..." unbounded recursion.
//
// The trust-but-verify guard: when the annotation says the original
// image was Y and running the engine on Y produces the current spec
// image, treat the container as already-rewritten and skip the
// engine for it. Forging the annotation cannot bypass the policy
// because the verifier re-runs the same engine.
func TestPodMutatorCreateDoesNotRecursivelyRewriteOnReinvocation(t *testing.T) {
	t.Parallel()

	mutator := newMutatorTestEnv(t, catchAllMirrorPolicy())
	// First-pass output: catch-all rule "**:* -> mirror.internal/
	// {registry}/{repository}:{tag}" applied to docker.io/library/
	// nginx:1 yields mirror.internal/docker.io/library/nginx:1.
	mirrored := "mirror.internal/docker.io/library/nginx:1"
	// Simulate the in-flight state on a reinvocation: the Pod is now
	// the post-first-pass state (image already mirrored, our prefix
	// annotation present pointing at the original).
	pod := podWithContainers("default", mirrored)
	pod.Annotations = map[string]string{
		webhook.OriginalImageAnnotationPrefix + "c0": "docker.io/library/nginx:1",
	}

	resp := mutator.Handle(context.Background(), createRequest(t, pod))
	if !resp.Allowed {
		t.Fatalf("Allowed: got false, want true")
	}
	// The container image must NOT change on reinvocation. No /image
	// patch should be emitted; the annotation should also remain
	// stable.
	patched := applyPatchToPod(t, pod, resp)
	if got, want := patched.Spec.Containers[0].Image, mirrored; got != want {
		t.Errorf("Container image after reinvocation: got %q, want %q (no recursion)", got, want)
	}
	if got, want := patched.Annotations[webhook.OriginalImageAnnotationPrefix+"c0"], "docker.io/library/nginx:1"; got != want {
		t.Errorf("original-image annotation lost on reinvocation: got %q, want %q", got, want)
	}
}

// TestPodMutatorCreateStripsForgedAnnotationThatDoesNotVerify pins
// the security half of trust-but-verify: a user-forged annotation
// whose claimed original does NOT, when run through the engine,
// produce the current image is treated as forged - the annotation
// is stripped and the engine runs normally. This is the attack
// vector the verifier guards against.
func TestPodMutatorCreateStripsForgedAnnotationThatDoesNotVerify(t *testing.T) {
	t.Parallel()

	mutator := newMutatorTestEnv(t, mirrorPolicy())
	// Forged annotation: claims original was "decoy" but the
	// container's actual image is docker.io/library/nginx:1.21.
	// Running engine on "decoy" does not produce
	// docker.io/library/nginx:1.21, so the annotation does not
	// verify - the handler must strip it and rewrite normally.
	pod := podWithContainers("default", "docker.io/library/nginx:1.21")
	pod.Annotations = map[string]string{
		webhook.OriginalImageAnnotationPrefix + "c0": "decoy/forged:1",
	}

	resp := mutator.Handle(context.Background(), createRequest(t, pod))
	if !resp.Allowed {
		t.Fatalf("Allowed: got false, want true")
	}
	patched := applyPatchToPod(t, pod, resp)
	if got, want := patched.Spec.Containers[0].Image, "mirror.internal/library/nginx:1.21"; got != want {
		t.Errorf("Container image: got %q, want %q (engine must run normally when annotation does not verify)", got, want)
	}
	if got, want := patched.Annotations[webhook.OriginalImageAnnotationPrefix+"c0"], "docker.io/library/nginx:1.21"; got != want {
		t.Errorf("original-image annotation: got %q, want %q (forged value must be overwritten with the real original)", got, want)
	}
}

func TestPodMutatorUpdateDoesNotRecursivelyRewriteAlreadyMirroredImage(t *testing.T) {
	t.Parallel()

	// Catch-all mirror rule whose rewritten output matches the rule
	// itself. A naive UPDATE handler that re-evaluates unchanged
	// images would prepend mirror.internal again on every metadata
	// patch, growing mirror.internal/mirror.internal/.../docker.io/...
	// without bound. The handler must skip the unchanged image.
	mutator := newMutatorTestEnv(t, catchAllMirrorPolicy())
	mirrored := "mirror.internal/docker.io/library/nginx:1"
	oldPod := podWithContainers("default", mirrored)
	newPod := podWithContainers("default", mirrored)
	// Simulate an unrelated metadata change between old and new.
	newPod.Annotations = map[string]string{"unrelated": "change"}
	resp := mutator.Handle(context.Background(), updateRequest(t, newPod, oldPod))

	if !resp.Allowed {
		t.Fatalf("Allowed: got false, want true")
	}
	if len(resp.Patches) != 0 {
		t.Errorf("Patches: got %d, want 0 (unchanged image must not be re-evaluated)", len(resp.Patches))
	}
}

// TestPodMutatorUpdateTrustsOldObjectRewritesAnnotation pins the
// positive case for the policy-bypass guard: the rewrites
// annotation on the OldObject is by construction operator-written
// (only an UPDATE can have an OldObject, and the operator owns
// admission writes), so the handler trusts it. Same scenario as
// the recursive-rewrite test above but with the trust signal
// explicit: the prior pass's annotation is what makes the
// "already mirrored" decision auditable.
func TestPodMutatorUpdateTrustsOldObjectRewritesAnnotation(t *testing.T) {
	t.Parallel()

	mutator := newMutatorTestEnv(t, catchAllMirrorPolicy())
	mirrored := "mirror.internal/docker.io/library/nginx:1"
	oldPod := podWithContainers("default", mirrored)
	oldPod.Annotations = map[string]string{
		webhook.OriginalImageAnnotationPrefix + "c0": "docker.io/library/nginx:1",
	}
	newPod := podWithContainers("default", mirrored)
	newPod.Annotations = map[string]string{
		webhook.OriginalImageAnnotationPrefix + "c0": "docker.io/library/nginx:1",
	}
	resp := mutator.Handle(context.Background(), updateRequest(t, newPod, oldPod))

	if !resp.Allowed {
		t.Fatalf("Allowed: got false, want true")
	}
	if len(resp.Patches) != 0 {
		t.Errorf("Patches: got %d, want 0 (OldObject's annotation marks this container as already rewritten)", len(resp.Patches))
	}
}

// TestPodMutatorUpdateStripsUserForgedOriginalImageAnnotation pins
// the no-op UPDATE forgery guard: an attacker with PATCH-on-Pods
// permission (which is widely granted, unlike CREATE) can
// `kubectl annotate pod foo original-image.squirrel.molier.dev/c0=evil/forged:1`
// on a Pod whose container image is unchanged. Before this fix the
// handler took an early no-op path on UPDATE (image unchanged →
// engine not run → rewrites empty; OldObject also unannotated →
// priorRewrites empty), leaving the forged annotation in place on
// the final Pod. `kubectl describe pod` would then attribute audit
// content to squirrel that squirrel never wrote.
//
// The fix strips any prefix annotation whose container name is in
// neither priorRewrites nor this pass's rewrites. This test forges
// such an annotation on an UPDATE where no rewrite fires and asserts
// it is removed.
func TestPodMutatorUpdateStripsUserForgedOriginalImageAnnotation(t *testing.T) {
	t.Parallel()

	// Policy that does NOT match the container's image, so the
	// engine produces no rewrites on this admission.
	mutator := newMutatorTestEnv(t, mirrorPolicy())
	const forgedKey = webhook.OriginalImageAnnotationPrefix + "c0"

	// OldObject: no prefix annotations - clean prior state.
	oldPod := podWithContainers("default", "gcr.io/google_containers/etcd:3.5")
	// NewObject: same image (unchanged), but with a forged annotation
	// the attacker added via PATCH.
	newPod := podWithContainers("default", "gcr.io/google_containers/etcd:3.5")
	newPod.Annotations = map[string]string{
		forgedKey: "docker.io/library/nginx:1.21",
	}

	resp := mutator.Handle(context.Background(), updateRequest(t, newPod, oldPod))
	if !resp.Allowed {
		t.Fatalf("Allowed: got false, want true")
	}
	if len(resp.Patches) == 0 {
		t.Fatalf("Patches: 0, want >=1 (forged annotation must be stripped)")
	}
	patched := applyPatchToPod(t, newPod, resp)
	if got := patched.Annotations[forgedKey]; got != "" {
		t.Errorf("annotation %q: got %q, want empty (UPDATE must strip user-forged prefix annotations)", forgedKey, got)
	}
}

// identityRewritePolicy returns a policy whose target template
// renders to the same canonical reference as the input. Pins the
// recordRewrite short-circuit: when engine.Resolve.RewrittenImage
// equals the input image (e.g. the target template happens to be an
// identity), no rewrite is recorded and no annotation entry is
// emitted. The webhook treats this as a successful match-but-no-op
// outcome.
func identityRewritePolicy() *squirrelv1alpha1.ClusterImagePolicy {
	return &squirrelv1alpha1.ClusterImagePolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "identity", Generation: 1},
		Spec: squirrelv1alpha1.ClusterImagePolicySpec{
			DefaultTarget: &squirrelv1alpha1.Target{
				Registry:   "docker.io",
				Repository: "library/nginx",
				Tags:       []string{"1.21"},
			},
			Rules: []squirrelv1alpha1.Rule{{Match: match("docker.io/library/nginx:1.21")}},
		},
		Status: squirrelv1alpha1.ClusterImagePolicyStatus{
			Conditions: []metav1.Condition{acceptedCondition(1)},
		},
	}
}

func TestPodMutatorIdentityRewriteIsANoOp(t *testing.T) {
	t.Parallel()

	mutator := newMutatorTestEnv(t, identityRewritePolicy())
	pod := podWithContainers("default", "docker.io/library/nginx:1.21")
	resp := mutator.Handle(context.Background(), createRequest(t, pod))

	if !resp.Allowed {
		t.Fatalf("Allowed: got false, want true")
	}
	if len(resp.Patches) != 0 {
		t.Errorf("Patches: got %d, want 0 (rule matched but rendered to the same image - must not patch)", len(resp.Patches))
	}
}

func TestPodMutatorReturnsBadRequestOnDecodeFailure(t *testing.T) {
	t.Parallel()

	mutator := newMutatorTestEnv(t)
	req := admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Operation: admissionv1.Create,
			Namespace: "default",
			Object:    runtime.RawExtension{Raw: []byte("not-a-pod")},
		},
	}
	resp := mutator.Handle(context.Background(), req)
	if resp.Allowed {
		t.Errorf("Allowed: got true, want false on malformed payload")
	}
	if resp.Result == nil || resp.Result.Code != http.StatusBadRequest {
		t.Errorf("Result: got %+v, want status 400", resp.Result)
	}
}

func TestPodMutatorAllowsWhenNamespaceMissing(t *testing.T) {
	t.Parallel()

	// Wire the mutator with no namespace pre-installed; the lookup
	// will fail with IsNotFound.
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("corev1.AddToScheme: %v", err)
	}
	if err := squirrelv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("squirrel.AddToScheme: %v", err)
	}
	cli := fakeclient.NewClientBuilder().WithScheme(scheme).Build()
	mutator := &webhook.PodMutator{Client: cli, Decoder: admission.NewDecoder(scheme)}

	pod := podWithContainers("ghost", "docker.io/library/nginx:1.21")
	resp := mutator.Handle(context.Background(), createRequest(t, pod))
	if !resp.Allowed {
		t.Errorf("Allowed: got false, want true (missing namespace must not block admission)")
	}
	if len(resp.Patches) != 0 {
		t.Errorf("Patches: got %d, want 0", len(resp.Patches))
	}
}

// TestPodMutatorSkipsWhenNamespaceNotOptedIn is the defence-in-depth
// regression guard for the design's namespace opt-in gate. The MWC's
// namespaceSelector is supposed to keep non-opted-in namespaces out,
// but controller-tools cannot put the selector on the generated MWC,
// so the handler re-checks the OptInLabel itself. A misconfigured
// manifest patch (forgotten or wrong) cannot grant the operator the
// authority to mutate pods in a namespace that never opted in.
func TestPodMutatorSkipsWhenNamespaceNotOptedIn(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("corev1.AddToScheme: %v", err)
	}
	if err := squirrelv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("squirrel.AddToScheme: %v", err)
	}
	cli := fakeclient.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespace /* no OptInLabel */}},
			mirrorPolicy(),
		).
		WithStatusSubresource(&squirrelv1alpha1.ClusterImagePolicy{}, &squirrelv1alpha1.ImagePolicy{}).
		Build()
	mutator := &webhook.PodMutator{Client: cli, Decoder: admission.NewDecoder(scheme)}

	pod := podWithContainers(testNamespace, "docker.io/library/nginx:1.21")
	resp := mutator.Handle(context.Background(), createRequest(t, pod))
	if !resp.Allowed {
		t.Fatalf("Allowed: got false, want true")
	}
	if len(resp.Patches) != 0 {
		t.Errorf("Patches: got %d, want 0 (namespace lacks the opt-in label)", len(resp.Patches))
	}
}

// TestPodMutatorSkipsReservedNamespaces is the defence-in-depth
// regression guard for the design's system-namespace exclusion. The
// MWC's namespaceSelector is supposed to keep kube-system,
// kube-public, kube-node-lease, and the operator's own namespace out,
// but controller-tools cannot put that exclusion on the generated
// MWC. The handler refuses to mutate Pods in any of those
// namespaces regardless of how the OptInLabel is set, so an
// accidentally-labelled control-plane namespace cannot have its
// images rewritten.
//
// Also pins the squirrel_reserved_namespace_skips_total counter:
// every short-circuit must emit a labelled increment so an operator
// can alert on a misconfigured MWC selector that lets reserved-
// namespace admissions through to the handler.
func TestPodMutatorSkipsReservedNamespaces(t *testing.T) {
	// Not t.Parallel: the test reads + asserts on a process-global
	// Prometheus counter (metrics.ReservedNamespaceSkipsTotal), and
	// running concurrently with other tests that increment the same
	// counter would make the delta assertion racey.

	const operatorNS = "squirrel-system"
	reserved := []string{"kube-system", "kube-public", "kube-node-lease", operatorNS}
	for _, ns := range reserved {
		t.Run(ns, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := corev1.AddToScheme(scheme); err != nil {
				t.Fatalf("corev1.AddToScheme: %v", err)
			}
			if err := squirrelv1alpha1.AddToScheme(scheme); err != nil {
				t.Fatalf("squirrel.AddToScheme: %v", err)
			}
			// Even with the OptInLabel set, the handler must not
			// mutate. The Namespace fixture carries the label so a
			// regression that only checks the label (not the reserved
			// list) would mutate and fail this test.
			cli := fakeclient.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(
					&corev1.Namespace{
						ObjectMeta: metav1.ObjectMeta{
							Name:   ns,
							Labels: map[string]string{webhook.OptInLabel: "true"},
						},
					},
					mirrorPolicy(),
				).
				WithStatusSubresource(&squirrelv1alpha1.ClusterImagePolicy{}, &squirrelv1alpha1.ImagePolicy{}).
				Build()
			mutator := &webhook.PodMutator{
				Client:            cli,
				Decoder:           admission.NewDecoder(scheme),
				OperatorNamespace: operatorNS,
			}

			// Snapshot the per-label counter; the assertion checks
			// the delta so concurrent test runs that happen to share
			// the process-global counter cannot mask the increment.
			wantLabel := "system"
			if ns == operatorNS {
				wantLabel = "operator"
			}
			before := promtestutil.ToFloat64(metrics.ReservedNamespaceSkipsTotal.WithLabelValues(wantLabel))

			pod := podWithContainers(ns, "docker.io/library/nginx:1.21")
			resp := mutator.Handle(context.Background(), createRequest(t, pod))
			if !resp.Allowed {
				t.Fatalf("Allowed: got false, want true")
			}
			if len(resp.Patches) != 0 {
				t.Errorf("Patches: got %d, want 0 (reserved namespace must not be mutated even when opted in)", len(resp.Patches))
			}

			after := promtestutil.ToFloat64(metrics.ReservedNamespaceSkipsTotal.WithLabelValues(wantLabel))
			if got := after - before; got != 1 {
				t.Errorf("ReservedNamespaceSkipsTotal[%q] delta: got %v, want 1", wantLabel, got)
			}
		})
	}
}

// TestPodMutatorAdmitsUnchangedOnNamespaceLookupError covers the
// not-IsNotFound branch of the namespace fetch. failurePolicy=Ignore
// on the MWC only converts unreachable-webhook outcomes into admits;
// it does not downgrade a webhook-returned denial. A transient
// apiserver/cache failure must therefore admit unchanged here -
// otherwise an unrelated lookup blip would block Pod creation in
// every opted-in namespace.
func TestPodMutatorAdmitsUnchangedOnNamespaceLookupError(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("corev1.AddToScheme: %v", err)
	}
	if err := squirrelv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("squirrel.AddToScheme: %v", err)
	}
	cli := fakeclient.NewClientBuilder().
		WithScheme(scheme).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*corev1.Namespace); ok {
					return errors.New("simulated apiserver failure")
				}
				return c.Get(ctx, key, obj, opts...)
			},
		}).
		Build()
	mutator := &webhook.PodMutator{Client: cli, Decoder: admission.NewDecoder(scheme)}

	pod := podWithContainers(testNamespace, "docker.io/library/nginx:1.21")
	resp := mutator.Handle(context.Background(), createRequest(t, pod))
	if !resp.Allowed {
		t.Errorf("Allowed: got false, want true (transient namespace lookup error must admit unchanged)")
	}
	if len(resp.Patches) != 0 {
		t.Errorf("Patches: got %d, want 0 (no policy state was determined)", len(resp.Patches))
	}
}

// TestPodMutatorAdmitsUnchangedOnFatalResolverError covers the
// fail-closed-on-partial-policy-set contract: when ApplicableRules
// reports an infrastructure failure (a List failed; cache/RBAC
// misconfigured for one of the policy kinds), the handler must NOT
// apply the partial subset of rules that came back - a missing
// cluster-wide skip rule could otherwise let an image past the gate.
// failurePolicy=Ignore is on the side of "admit unchanged" for every
// other failure mode and so should this be.
func TestPodMutatorAdmitsUnchangedOnFatalResolverError(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("corev1.AddToScheme: %v", err)
	}
	if err := squirrelv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("squirrel.AddToScheme: %v", err)
	}
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   testNamespace,
			Labels: map[string]string{"squirrel.molier.dev/enabled": "true"},
		},
	}
	cli := fakeclient.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ns).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				if _, ok := list.(*squirrelv1alpha1.ClusterImagePolicyList); ok {
					return errors.New("simulated list failure")
				}
				return c.List(ctx, list, opts...)
			},
		}).
		Build()
	mutator := &webhook.PodMutator{Client: cli, Decoder: admission.NewDecoder(scheme)}

	pod := podWithContainers(testNamespace, "docker.io/library/nginx:1.21")
	resp := mutator.Handle(context.Background(), createRequest(t, pod))
	if !resp.Allowed {
		t.Errorf("Allowed: got false, want true (fatal resolver error must not block admission)")
	}
	if len(resp.Patches) != 0 {
		t.Errorf("Patches: got %d, want 0 (partial policy set must not be applied)", len(resp.Patches))
	}
}

// applyPatchToPod decodes the admission response's JSON Patch operations
// and applies them to the original pod, returning the resulting Pod
// for assertions. The fake client doesn't materialise the patched
// object - the response contains the patches the apiserver would
// apply - so the test must walk that path explicitly.
func applyPatchToPod(t *testing.T, original *corev1.Pod, resp admission.Response) *corev1.Pod {
	t.Helper()

	origBytes, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal original: %v", err)
	}
	patchBytes, err := json.Marshal(resp.Patches)
	if err != nil {
		t.Fatalf("marshal patches: %v", err)
	}

	patch, err := jsonpatch.DecodePatch(patchBytes)
	if err != nil {
		t.Fatalf("decode patch: %v", err)
	}
	patchedBytes, err := patch.Apply(origBytes)
	if err != nil {
		t.Fatalf("apply patch: %v", err)
	}
	var patched corev1.Pod
	if err := json.Unmarshal(patchedBytes, &patched); err != nil {
		t.Fatalf("unmarshal patched: %v", err)
	}
	return &patched
}
