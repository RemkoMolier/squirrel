//go:build integration

// Webhook-side integration specs against the envtest-backed
// apiserver. Covers the readyz cache-sync latch and the
// PodMutator (rewrite path, OptInLabel gating, reserved-namespace
// short-circuit). The Ginkgo suite entry lives in
// integration_suite_test.go.
package manager_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	jsonpatch "gomodules.xyz/jsonpatch/v2"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	runtimepkg "k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	client "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	admission "sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	squirrelv1alpha1 "github.com/RemkoMolier/squirrel/api/v1alpha1"
	"github.com/RemkoMolier/squirrel/internal/controller"
	sqwebhook "github.com/RemkoMolier/squirrel/internal/webhook"
)

var _ = Describe("Readyz cache-sync latch", func() {
	// Pins the cacheSyncReadyz latch against a real envtest-backed
	// informer cache. Before mgr.Start or before WaitForCacheSync
	// completes, the cache reports not-synced; once synced, every
	// subsequent check must return ready. The earlier
	// cancelled-context implementation could flip back to not-ready
	// post-sync; the atomic.Bool latch fixes that.
	//
	// We exercise the latch directly via the same internals the
	// production handler uses, rather than over HTTP - controller-
	// runtime's probe server does not expose its bound listener
	// address in a way the spec can reach.

	It("latches once synced and never reverts on subsequent probes", func(ctx SpecContext) {
		mgr, err := ctrl.NewManager(suiteCfg, ctrl.Options{
			Scheme:  suiteScheme,
			Metrics: metricsserver.Options{BindAddress: "0"},
			// Allow re-registering controller names within the same
			// `go test` process: each spec stands up its own manager.
			Controller: config.Controller{SkipNameValidation: ptrTrue()},
		})
		Expect(err).NotTo(HaveOccurred())

		// Force the cache to instantiate at least one informer so
		// "synced" is meaningful (the cache is vacuously synced when
		// it has no informers).
		Expect((&controller.ClusterImagePolicyReconciler{
			Client:   mgr.GetClient(),
			Recorder: mgr.GetEventRecorderFor("squirrel-controller-test"),
		}).SetupWithManager(mgr)).To(Succeed())

		mgrCtx, cancel := context.WithCancel(ctx)
		DeferCleanup(cancel)
		go func() {
			defer GinkgoRecover()
			if err := mgr.Start(mgrCtx); err != nil && !errors.Is(err, context.Canceled) {
				GinkgoLogr.Info("mgr.Start exited", "err", err)
			}
		}()

		Expect(mgr.GetCache().WaitForCacheSync(ctx)).To(BeTrue())

		// Reproduce the production latch in-process (cacheSyncReadyz
		// is unexported in package manager). Contract: once the
		// latch has observed a successful sync, every subsequent
		// probe must return ready without re-entering
		// WaitForCacheSync. Calling WaitForCacheSync with a
		// cancelled context after sync is exactly the
		// nondeterministic case the old code shipped - the spec
		// deliberately does NOT do that.
		var synced atomic.Bool
		probe := func() error {
			if synced.Load() {
				return nil
			}
			probeCtx, probeCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer probeCancel()
			if mgr.GetCache().WaitForCacheSync(probeCtx) {
				synced.Store(true)
				return nil
			}
			return errors.New("not synced")
		}
		for range 100 {
			Expect(probe()).To(Succeed(), "latch probe returned an error after successful cache sync")
		}
		Expect(synced.Load()).To(BeTrue(), "latch did not record a successful sync after 100 probes")
	})
})

var _ = Describe("PodMutator against the envtest apiserver", func() {
	// Pins the resolver + handler end-to-end against a real
	// apiserver: a ClusterImagePolicy persisted via envtest is
	// observed by the manager's informer cache, the PodMutator
	// runs against the real apiserver-backed client, and a
	// synthetic AdmissionReview produces the expected JSON patch.
	//
	// This is one tier below the full apiserver-dispatched
	// MutatingWebhookConfiguration flow (which would require setting
	// up TLS + the MWC + an HTTP listener) but exercises everything
	// the operator owns: cache-backed resolver, applicable-rule
	// selection, engine resolution, JSON patch emission.

	It("rewrites a matching container image and stamps the original-image annotation", func(ctx SpecContext) {
		mgr, cli := bringUpMgrWithCIPReconciler(ctx)

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "integ-mutator",
				Labels: map[string]string{sqwebhook.OptInLabel: "true"},
			},
		}
		Expect(cli.Create(ctx, ns)).To(Succeed())

		policy := &squirrelv1alpha1.ClusterImagePolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "integ-mirror"},
			Spec: squirrelv1alpha1.ClusterImagePolicySpec{
				DefaultTarget: &squirrelv1alpha1.Target{Registry: "mirror.internal"},
				Rules:         []squirrelv1alpha1.Rule{{Match: matchJSON("docker.io/**:*")}},
			},
		}
		Expect(cli.Create(ctx, policy)).To(Succeed())
		Eventually(func(g Gomega) {
			g.Expect(acceptedTrue(ctx, cli, "integ-mirror")).To(BeTrue())
		}, 15*time.Second, 100*time.Millisecond).Should(Succeed())

		mutator := &sqwebhook.PodMutator{Client: cli, Decoder: admission.NewDecoder(suiteScheme)}
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "rewrite-me", Namespace: "integ-mutator"},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "main", Image: "docker.io/library/nginx:1.21"}},
			},
		}
		raw, err := json.Marshal(pod)
		Expect(err).NotTo(HaveOccurred())

		resp := mutator.Handle(ctx, admission.Request{
			AdmissionRequest: admissionv1.AdmissionRequest{
				Operation: admissionv1.Create,
				Namespace: pod.Namespace,
				Object:    runtimepkg.RawExtension{Raw: raw},
			},
		})

		Expect(resp.Allowed).To(BeTrue(), fmt.Sprintf("response: %+v", resp.Result))
		Expect(resp.Patches).NotTo(BeEmpty())

		// Verify the patch sets the container image to the mirror
		// AND stamps the original-image annotation keyed by container
		// name. The annotation contract is documented on
		// webhook.OriginalImageAnnotationPrefix; this assertion pins
		// it against a real apiserver-issued AdmissionReview round-
		// trip so a future change that breaks the key shape (which
		// must satisfy K8s annotation-key rules: DNS-1123 subdomain
		// prefix + name up to 63 chars) cannot slip past unit-test
		// fixtures that use the fake client.
		var foundImageRewrite, foundAnnotation bool
		var rendered strings.Builder
		// Bind to the exported constant so a future rename of the
		// annotation prefix breaks this assertion before it ships.
		annKey := sqwebhook.OriginalImageAnnotationPrefix + "main"
		for _, p := range resp.Patches {
			fmt.Fprintf(&rendered, "  %s %s -> %v\n", p.Operation, p.Path, p.Value)
			if p.Path == "/spec/containers/0/image" {
				s, ok := p.Value.(string)
				Expect(ok).To(BeTrue(), fmt.Sprintf("patch value type: got %T, want string", p.Value))
				Expect(s).To(HavePrefix("mirror.internal/"))
				foundImageRewrite = true
			}
			// Two shapes are valid depending on whether the Pod had
			// any pre-existing annotations: a single `add` of the
			// whole map at /metadata/annotations, OR per-key `add`
			// ops at /metadata/annotations/<escaped-key>. Cover both.
			if p.Path == "/metadata/annotations" {
				if m, ok := p.Value.(map[string]interface{}); ok {
					if _, found := m[annKey]; found {
						foundAnnotation = true
					}
				}
				if m, ok := p.Value.(map[string]string); ok {
					if _, found := m[annKey]; found {
						foundAnnotation = true
					}
				}
			}
			// JSON Patch escapes `/` in keys as `~1`, so e.g.
			// "original-image.squirrel.molier.dev/main" shows up as
			// "...dev~1main" inside the Path.
			if strings.Contains(p.Path, "/metadata/annotations/") &&
				strings.Contains(p.Path, sqwebhook.OriginalImageAnnotationPrefix[:len(sqwebhook.OriginalImageAnnotationPrefix)-1]) &&
				strings.Contains(p.Path, "main") {
				foundAnnotation = true
			}
		}
		Expect(foundImageRewrite).To(BeTrue(), "no /spec/containers/0/image patch in response. patches:\n"+rendered.String())
		Expect(foundAnnotation).To(BeTrue(), "no per-container original-image annotation patch in response. patches:\n"+rendered.String())

		// Keep mgr reference live for the duration of the spec.
		_ = mgr
	})

	It("skips a Pod in a namespace without the OptInLabel", func(ctx SpecContext) {
		// The fake-client unit tests cover the same path against a
		// stubbed namespace; the envtest pass proves the real
		// apiserver's namespace Get + label read produce the same
		// outcome.
		mgr, cli := bringUpMgrWithCIPReconciler(ctx)
		_ = mgr

		Expect(cli.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "integ-unopted"}})).To(Succeed())

		policy := &squirrelv1alpha1.ClusterImagePolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "integ-unopted-mirror"},
			Spec: squirrelv1alpha1.ClusterImagePolicySpec{
				DefaultTarget: &squirrelv1alpha1.Target{Registry: "mirror.internal"},
				Rules:         []squirrelv1alpha1.Rule{{Match: matchJSON("docker.io/**:*")}},
			},
		}
		Expect(cli.Create(ctx, policy)).To(Succeed())
		Eventually(func(g Gomega) {
			g.Expect(acceptedTrue(ctx, cli, "integ-unopted-mirror")).To(BeTrue())
		}, 15*time.Second, 100*time.Millisecond).Should(Succeed())

		mutator := &sqwebhook.PodMutator{Client: cli, Decoder: admission.NewDecoder(suiteScheme)}
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "should-not-mutate", Namespace: "integ-unopted"},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "main", Image: "docker.io/library/nginx:1.21"}},
			},
		}
		raw, err := json.Marshal(pod)
		Expect(err).NotTo(HaveOccurred())
		resp := mutator.Handle(ctx, admission.Request{
			AdmissionRequest: admissionv1.AdmissionRequest{
				Operation: admissionv1.Create,
				Namespace: pod.Namespace,
				Object:    runtimepkg.RawExtension{Raw: raw},
			},
		})
		Expect(resp.Allowed).To(BeTrue())
		Expect(resp.Patches).To(BeEmpty(), "un-opted namespace must not be mutated")
	})

	It("short-circuits Pods in the operator's own namespace regardless of OptInLabel", func(ctx SpecContext) {
		// A misconfigured manifest that opts the operator namespace
		// in cannot accidentally rewrite the operator's own
		// container images.
		mgr, cli := bringUpMgrWithCIPReconciler(ctx)
		_ = mgr

		const operatorNS = "integ-operator-ns"
		Expect(cli.Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:   operatorNS,
				Labels: map[string]string{sqwebhook.OptInLabel: "true"},
			},
		})).To(Succeed())

		policy := &squirrelv1alpha1.ClusterImagePolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "integ-operator-mirror"},
			Spec: squirrelv1alpha1.ClusterImagePolicySpec{
				DefaultTarget: &squirrelv1alpha1.Target{Registry: "mirror.internal"},
				Rules:         []squirrelv1alpha1.Rule{{Match: matchJSON("docker.io/**:*")}},
			},
		}
		Expect(cli.Create(ctx, policy)).To(Succeed())
		Eventually(func(g Gomega) {
			g.Expect(acceptedTrue(ctx, cli, "integ-operator-mirror")).To(BeTrue())
		}, 15*time.Second, 100*time.Millisecond).Should(Succeed())

		mutator := &sqwebhook.PodMutator{
			Client:            cli,
			Decoder:           admission.NewDecoder(suiteScheme),
			OperatorNamespace: operatorNS,
		}
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "operator-pod", Namespace: operatorNS},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "main", Image: "docker.io/library/nginx:1.21"}},
			},
		}
		raw, err := json.Marshal(pod)
		Expect(err).NotTo(HaveOccurred())
		resp := mutator.Handle(ctx, admission.Request{
			AdmissionRequest: admissionv1.AdmissionRequest{
				Operation: admissionv1.Create,
				Namespace: pod.Namespace,
				Object:    runtimepkg.RawExtension{Raw: raw},
			},
		})
		Expect(resp.Allowed).To(BeTrue())
		Expect(resp.Patches).To(BeEmpty(), "operator-namespace Pod must not be mutated")
	})

	It("propagates a policy spec update through the informer cache to the mutator", func(ctx SpecContext) {
		// Pins the resolver's cache-invalidation behaviour end-to-end:
		// editing a policy's target registry must take effect on the
		// next admission once the informer observes the update. The
		// compile cache is keyed by (UID, generation), so a spec edit
		// bumps generation and the resolver sees the new rules; the
		// fake-client unit tests cover the key derivation, but only
		// envtest can prove the chain works against a real watch
		// stream where the reconciler's status write (carrying the
		// new observedGeneration) and the spec edit hit the cache
		// asynchronously.
		mgr, cli := bringUpMgrWithCIPReconciler(ctx)
		_ = mgr

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "integ-update",
				Labels: map[string]string{sqwebhook.OptInLabel: "true"},
			},
		}
		Expect(cli.Create(ctx, ns)).To(Succeed())

		// The envtest apiserver is shared across the suite, so other
		// specs' cluster-scoped docker.io rules are still in the
		// cache when this spec runs. Pin the match expression to a
		// quay.io subtree no other spec uses so the resolver only
		// surfaces this spec's policy for the test Pod.
		policy := &squirrelv1alpha1.ClusterImagePolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "integ-update-policy"},
			Spec: squirrelv1alpha1.ClusterImagePolicySpec{
				DefaultTarget: &squirrelv1alpha1.Target{Registry: "first.example"},
				Rules:         []squirrelv1alpha1.Rule{{Match: matchJSON("quay.io/integ-update/**:*")}},
			},
		}
		Expect(cli.Create(ctx, policy)).To(Succeed())
		Eventually(func(g Gomega) {
			g.Expect(acceptedTrue(ctx, cli, "integ-update-policy")).To(BeTrue())
		}, 15*time.Second, 100*time.Millisecond).Should(Succeed())

		mutator := &sqwebhook.PodMutator{Client: cli, Decoder: admission.NewDecoder(suiteScheme)}
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "rewrite-target", Namespace: "integ-update"},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "main", Image: "quay.io/integ-update/svc:v1"}},
			},
		}
		raw, err := json.Marshal(pod)
		Expect(err).NotTo(HaveOccurred())

		// extractRewriteTarget walks the patch set for the
		// /spec/containers/0/image op and returns its registry prefix,
		// or the empty string when no such patch is present.
		extractRewriteTarget := func(patches []jsonpatch.JsonPatchOperation) string {
			for _, p := range patches {
				if p.Path != "/spec/containers/0/image" {
					continue
				}
				s, ok := p.Value.(string)
				if !ok {
					return ""
				}
				if i := strings.Index(s, "/"); i >= 0 {
					return s[:i]
				}
				return s
			}
			return ""
		}

		// First admission: image must be rewritten to first.example.
		resp := mutator.Handle(ctx, admission.Request{
			AdmissionRequest: admissionv1.AdmissionRequest{
				Operation: admissionv1.Create,
				Namespace: pod.Namespace,
				Object:    runtimepkg.RawExtension{Raw: raw},
			},
		})
		Expect(resp.Allowed).To(BeTrue())
		Expect(extractRewriteTarget(resp.Patches)).To(Equal("first.example"))

		// Edit the policy to point at a new registry. The reconciler
		// will observe the spec bump, re-validate, and stamp
		// Accepted=True at the new observedGeneration; the resolver
		// reads the new generation off the informer cache and the
		// compile cache miss recompiles with the new DefaultTarget.
		Eventually(func(g Gomega) {
			var cur squirrelv1alpha1.ClusterImagePolicy
			g.Expect(cli.Get(ctx, client.ObjectKey{Name: "integ-update-policy"}, &cur)).To(Succeed())
			cur.Spec.DefaultTarget = &squirrelv1alpha1.Target{Registry: "second.example"}
			g.Expect(cli.Update(ctx, &cur)).To(Succeed())
		}, 5*time.Second, 100*time.Millisecond).Should(Succeed())

		// Drive the second admission inside Eventually so we poll
		// past the brief window between the apiserver accepting the
		// Update and the resolver's cache observing both the new
		// spec AND the matching Accepted condition at the new
		// observedGeneration. Without the poll the spec assertion is
		// racy in CI.
		Eventually(func(g Gomega) {
			resp := mutator.Handle(ctx, admission.Request{
				AdmissionRequest: admissionv1.AdmissionRequest{
					Operation: admissionv1.Create,
					Namespace: pod.Namespace,
					Object:    runtimepkg.RawExtension{Raw: raw},
				},
			})
			g.Expect(resp.Allowed).To(BeTrue())
			g.Expect(extractRewriteTarget(resp.Patches)).To(Equal("second.example"))
		}, 15*time.Second, 200*time.Millisecond).Should(Succeed())
	})
})
