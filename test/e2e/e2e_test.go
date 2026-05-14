//go:build e2e

// End-to-end coverage on a real cluster (kind, booted by
// hack/e2e-kind.sh). Closes the gap envtest cannot reach: the full
// apiserver -> MutatingWebhookConfiguration -> TLS handshake ->
// webhook handler -> JSON Patch -> apiserver chain. envtest exercises
// every piece up to the handler; only a real cluster wires the MWC
// dispatch + TLS + reinvocation policies into the loop.
//
// The suite assumes a cluster is up and the operator has been
// rolled out via `kubectl apply -k config/default`. The orchestrator
// script handles that; the Go code here only drives the policy
// flow and asserts the resulting Pod state.
package e2e_test

import (
	"context"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	runtimepkg "k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	client "sigs.k8s.io/controller-runtime/pkg/client"

	squirrelv1alpha1 "github.com/RemkoMolier/squirrel/api/v1alpha1"
)

func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Squirrel E2E Suite (kind)")
}

var (
	e2eCfg    *runtimepkg.Scheme
	e2eClient client.Client
)

var _ = BeforeSuite(func() {
	e2eCfg = runtimepkg.NewScheme()
	Expect(corev1.AddToScheme(e2eCfg)).To(Succeed())
	Expect(squirrelv1alpha1.AddToScheme(e2eCfg)).To(Succeed())

	cfg := ctrl.GetConfigOrDie()
	cli, err := client.New(cfg, client.Options{Scheme: e2eCfg})
	Expect(err).NotTo(HaveOccurred())
	e2eClient = cli
})

var _ = Describe("Operator on a real cluster", func() {
	// Each spec scopes its own resources to a unique namespace so
	// reruns inside the same kind cluster (KEEP_CLUSTER=1) do not
	// collide. The kind cluster is ephemeral by default so name
	// collisions across runs never happen via the CI path.
	const ns = "squirrel-e2e"
	const policyName = "e2e-mirror"

	It("rewrites a matching Pod's container image and stamps the per-container annotation", func(ctx SpecContext) {
		// Opted-in namespace. The MWC's namespaceSelector matches
		// only namespaces carrying the design's label, so without
		// this the apiserver never dispatches our admission and
		// the test would assert against an un-mutated Pod.
		Expect(e2eClient.Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:   ns,
				Labels: map[string]string{squirrelv1alpha1.OptInLabel: "true"},
			},
		})).To(Succeed())
		DeferCleanup(func() {
			_ = e2eClient.Delete(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
		})

		// Cluster-scoped policy with a single rewrite rule: any
		// docker.io/** image gets repointed at mirror.internal under
		// the dockerhub/{repository}:{tag} convention from USAGE.md's
		// worked example.
		policy := &squirrelv1alpha1.ClusterImagePolicy{
			ObjectMeta: metav1.ObjectMeta{Name: policyName},
			Spec: squirrelv1alpha1.ClusterImagePolicySpec{
				DefaultTarget: &squirrelv1alpha1.Target{
					Registry:   "mirror.internal",
					Repository: "dockerhub/{repository}",
					Tags:       []string{"{tag}"},
				},
				Rules: []squirrelv1alpha1.Rule{{Match: matchJSON(`"docker.io/**:*"`)}},
			},
		}
		Expect(e2eClient.Create(ctx, policy)).To(Succeed())
		DeferCleanup(func() {
			_ = e2eClient.Delete(context.Background(), &squirrelv1alpha1.ClusterImagePolicy{ObjectMeta: metav1.ObjectMeta{Name: policyName}})
		})

		// Wait for the reconciler to mark the policy Accepted=True.
		// Until this transition lands, the webhook resolver does not
		// pick the policy up and the Pod's image would pass through
		// unchanged. 60s is generous - the reconciler is fast; this
		// covers cluster startup latency on slow CI runners.
		Eventually(func(g Gomega) {
			var p squirrelv1alpha1.ClusterImagePolicy
			g.Expect(e2eClient.Get(ctx, client.ObjectKey{Name: policyName}, &p)).To(Succeed())
			cond := apimeta.FindStatusCondition(p.Status.Conditions, squirrelv1alpha1.ConditionAccepted)
			g.Expect(cond).NotTo(BeNil())
			g.Expect(string(cond.Status)).To(Equal("True"))
		}, 60*time.Second, time.Second).Should(Succeed())

		// The webhook reads from an informer cache; even after the
		// status flip there is a beat before the webhook handler
		// sees the Accepted=True policy. Create the Pod with retry
		// so a cache-miss admission (no mutation) gets re-attempted
		// once the cache is warm. We delete-and-recreate each
		// iteration since the apiserver's failurePolicy=Ignore lets
		// the original Pod create succeed with no mutation.
		const podName = "rewrite-target"
		const originalImage = "docker.io/library/nginx:1.21"
		Eventually(func(g Gomega) {
			_ = e2eClient.Delete(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: podName, Namespace: ns}})

			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: podName, Namespace: ns},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "main", Image: originalImage}},
				},
			}
			g.Expect(e2eClient.Create(ctx, pod)).To(Succeed())

			var got corev1.Pod
			g.Expect(e2eClient.Get(ctx, client.ObjectKey{Name: podName, Namespace: ns}, &got)).To(Succeed())
			g.Expect(got.Spec.Containers).To(HaveLen(1))
			g.Expect(got.Spec.Containers[0].Image).To(HavePrefix("mirror.internal/"))
			// The image must NOT still be the input; that would mean
			// the webhook short-circuited (failurePolicy=Ignore on a
			// not-yet-warm resolver). The HavePrefix check above
			// usually catches this, but pin the negative case too
			// for clarity.
			g.Expect(got.Spec.Containers[0].Image).NotTo(Equal(originalImage))

			// Per-container annotation key shape: prefix + container
			// name. Pinned as a literal here (rather than imported
			// from internal/webhook) so the e2e binary can build
			// without dragging the internal package into a test
			// module the operator image does not need.
			const annKey = "original-image.squirrel.molier.dev/main"
			g.Expect(got.Annotations).To(HaveKeyWithValue(annKey, originalImage))
			// Belt-and-braces: walking annotations for any prefix key
			// that mismatches the trusted container would catch a
			// regression where the handler stamps under the wrong key.
			for k := range got.Annotations {
				if strings.HasPrefix(k, "original-image.squirrel.molier.dev/") && k != annKey {
					Fail("unexpected prefix annotation: " + k)
				}
			}
		}, 90*time.Second, 3*time.Second).Should(Succeed())
	})
})

// matchJSON builds the raw-JSON payload Rule.Match expects. The
// argument is the literal JSON value (including its quotes for the
// string form) so callers see "docker.io/**:*" exactly as it would
// appear in a YAML policy.
func matchJSON(raw string) apiextensionsv1.JSON {
	return apiextensionsv1.JSON{Raw: []byte(raw)}
}
