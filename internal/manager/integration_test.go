//go:build integration

// Reconciler-side integration specs against the envtest-backed
// apiserver. The Ginkgo suite entry lives in
// integration_suite_test.go; this file owns the Describe blocks
// that exercise ClusterImagePolicy + ImagePolicy reconcilers and
// the CRD's CEL XValidation gates.
package manager_test

import (
	"context"
	"encoding/json"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	types "k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	client "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	squirrelv1alpha1 "github.com/RemkoMolier/squirrel/api/v1alpha1"
	"github.com/RemkoMolier/squirrel/internal/controller"
)

// matchJSON builds a Rule.Match payload from a glob-string form.
// json.Marshal on a string cannot fail in practice; the helper
// panics on the impossible error path so callers do not have to
// thread Ginkgo's Expect through every fixture.
func matchJSON(s string) apiextensionsv1.JSON {
	raw, err := json.Marshal(s)
	if err != nil {
		panic("matchJSON: json.Marshal on a string returned an error: " + err.Error())
	}
	return apiextensionsv1.JSON{Raw: raw}
}

// ptrTrue returns *bool pointing at true, the shape
// controller-runtime's optional config fields expect.
func ptrTrue() *bool {
	v := true
	return &v
}

// startManagerForSpec brings up a controller-runtime Manager
// against the suite-shared envtest config and returns it. Caller
// is responsible for SetupWithManager + mgr.Start under a context
// they cancel via DeferCleanup. Each spec gets its own manager so
// informer caches stay isolated.
func startManagerForSpec(ctx context.Context) ctrl.Manager {
	GinkgoHelper()
	mgr, err := ctrl.NewManager(suiteCfg, ctrl.Options{
		Scheme:     suiteScheme,
		Metrics:    metricsserver.Options{BindAddress: "0"},
		Controller: config.Controller{SkipNameValidation: ptrTrue()},
	})
	Expect(err).NotTo(HaveOccurred(), "NewManager")
	return mgr
}

// runManager starts mgr in a goroutine and registers a cleanup
// that cancels its context on spec teardown. Returns once the
// cache has synced; specs can use the returned client immediately.
func runManager(ctx context.Context, mgr ctrl.Manager) client.Client {
	GinkgoHelper()
	go func() {
		defer GinkgoRecover()
		// mgr.Start returns nil on graceful context cancel; any other
		// error is a test bug worth surfacing.
		if err := mgr.Start(ctx); err != nil && ctx.Err() == nil {
			Fail("mgr.Start: " + err.Error())
		}
	}()
	Expect(mgr.GetCache().WaitForCacheSync(ctx)).To(BeTrue(), "cache sync")
	return mgr.GetClient()
}

var _ = Describe("ClusterImagePolicy reconciler", func() {
	// Each It builds its own manager so the informer caches do not
	// bleed across specs.

	It("lands Accepted=False on a malformed policy and Accepted=True on a clean one", func(ctx SpecContext) {
		mgr := startManagerForSpec(ctx)
		Expect((&controller.ClusterImagePolicyReconciler{
			Client:   mgr.GetClient(),
			Recorder: mgr.GetEventRecorderFor("squirrel-controller-test"),
		}).SetupWithManager(mgr)).To(Succeed())

		mgrCtx, cancel := context.WithCancel(ctx)
		DeferCleanup(cancel)
		cli := runManager(mgrCtx, mgr)

		// Malformed: rewrite rule with no registry anywhere.
		bad := &squirrelv1alpha1.ClusterImagePolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "integ-bad"},
			Spec: squirrelv1alpha1.ClusterImagePolicySpec{
				Rules: []squirrelv1alpha1.Rule{{Match: matchJSON("docker.io/library/nginx:1.21")}},
			},
		}
		Expect(cli.Create(ctx, bad)).To(Succeed())
		Eventually(func(g Gomega) {
			var p squirrelv1alpha1.ClusterImagePolicy
			g.Expect(cli.Get(ctx, client.ObjectKey{Name: "integ-bad"}, &p)).To(Succeed())
			cond := apimeta.FindStatusCondition(p.Status.Conditions, squirrelv1alpha1.ConditionAccepted)
			g.Expect(cond).NotTo(BeNil())
			g.Expect(string(cond.Status)).To(Equal("False"))
		}, 15*time.Second, 100*time.Millisecond).Should(Succeed())

		good := &squirrelv1alpha1.ClusterImagePolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "integ-good"},
			Spec: squirrelv1alpha1.ClusterImagePolicySpec{
				DefaultTarget: &squirrelv1alpha1.Target{Registry: "mirror.internal"},
				Rules:         []squirrelv1alpha1.Rule{{Match: matchJSON("docker.io/library/nginx:1.21")}},
			},
		}
		Expect(cli.Create(ctx, good)).To(Succeed())
		Eventually(func(g Gomega) {
			var p squirrelv1alpha1.ClusterImagePolicy
			g.Expect(cli.Get(ctx, client.ObjectKey{Name: "integ-good"}, &p)).To(Succeed())
			cond := apimeta.FindStatusCondition(p.Status.Conditions, squirrelv1alpha1.ConditionAccepted)
			g.Expect(cond).NotTo(BeNil())
			g.Expect(string(cond.Status)).To(Equal("True"))
		}, 15*time.Second, 100*time.Millisecond).Should(Succeed())
	})

	It("transitions Accepted=True->False on a spec edit and stamps observedGeneration at the new generation", func(ctx SpecContext) {
		// Pins the re-reconcile-on-update path that the existing
		// "fresh create" specs cannot reach. After an Accepted=True
		// policy is edited into an invalid shape:
		//   1) The reconciler must observe the new generation,
		//      re-validate, and flip Accepted to False.
		//   2) status.observedGeneration AND the Accepted condition's
		//      ObservedGeneration must both equal metadata.generation
		//      so the resolver's isAccepted() check correctly rejects
		//      the stale Accepted=True from generation 1.
		// Without (2) the webhook would happily keep applying the old
		// generation's rules after operators edited the spec to
		// disable them, which is the worst sort of stale-state bug.
		mgr := startManagerForSpec(ctx)
		Expect((&controller.ClusterImagePolicyReconciler{
			Client:   mgr.GetClient(),
			Recorder: mgr.GetEventRecorderFor("squirrel-controller-test"),
		}).SetupWithManager(mgr)).To(Succeed())

		mgrCtx, cancel := context.WithCancel(ctx)
		DeferCleanup(cancel)
		cli := runManager(mgrCtx, mgr)

		const name = "integ-transition"
		good := &squirrelv1alpha1.ClusterImagePolicy{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: squirrelv1alpha1.ClusterImagePolicySpec{
				DefaultTarget: &squirrelv1alpha1.Target{Registry: "mirror.internal"},
				Rules:         []squirrelv1alpha1.Rule{{Match: matchJSON("docker.io/library/nginx:1.21")}},
			},
		}
		Expect(cli.Create(ctx, good)).To(Succeed())
		Eventually(func(g Gomega) {
			var p squirrelv1alpha1.ClusterImagePolicy
			g.Expect(cli.Get(ctx, client.ObjectKey{Name: name}, &p)).To(Succeed())
			cond := apimeta.FindStatusCondition(p.Status.Conditions, squirrelv1alpha1.ConditionAccepted)
			g.Expect(cond).NotTo(BeNil())
			g.Expect(string(cond.Status)).To(Equal("True"))
			g.Expect(cond.ObservedGeneration).To(Equal(p.Generation))
			g.Expect(p.Status.ObservedGeneration).To(Equal(p.Generation))
		}, 15*time.Second, 100*time.Millisecond).Should(Succeed())

		// Drop DefaultTarget so the rule no longer has any registry
		// to resolve to: validation flips Accepted to False.
		Eventually(func(g Gomega) {
			var p squirrelv1alpha1.ClusterImagePolicy
			g.Expect(cli.Get(ctx, client.ObjectKey{Name: name}, &p)).To(Succeed())
			p.Spec.DefaultTarget = nil
			g.Expect(cli.Update(ctx, &p)).To(Succeed())
		}, 5*time.Second, 100*time.Millisecond).Should(Succeed())

		Eventually(func(g Gomega) {
			var p squirrelv1alpha1.ClusterImagePolicy
			g.Expect(cli.Get(ctx, client.ObjectKey{Name: name}, &p)).To(Succeed())
			g.Expect(p.Generation).To(BeNumerically(">=", 2),
				"spec edit must bump metadata.generation")
			cond := apimeta.FindStatusCondition(p.Status.Conditions, squirrelv1alpha1.ConditionAccepted)
			g.Expect(cond).NotTo(BeNil())
			g.Expect(string(cond.Status)).To(Equal("False"))
			g.Expect(cond.ObservedGeneration).To(Equal(p.Generation),
				"Accepted condition's ObservedGeneration must catch up to the new spec generation")
			g.Expect(p.Status.ObservedGeneration).To(Equal(p.Generation),
				"status.observedGeneration must catch up to the new spec generation")
		}, 15*time.Second, 100*time.Millisecond).Should(Succeed())
	})

	It("treats a delete as a no-op and keeps reconciling subsequent policies under the same name", func(ctx SpecContext) {
		// Pins the NotFound branch of Reconcile. The reconciler must:
		//   1) Return cleanly (no orphan status write) when the
		//      object has been deleted between watch-event and
		//      Reconcile entry.
		//   2) Continue to handle subsequent Reconcile calls for
		//      objects sharing the deleted name - the per-policy
		//      metric series cleanup must not leave any internal
		//      state that would mistake the new-UID resurrected
		//      policy for the deleted one.
		// Recreating the same name with an *invalid* spec is the
		// strongest assertion here: any leftover state from the first
		// (Accepted=True) lifetime would manifest as the second
		// lifetime never flipping to Accepted=False.
		mgr := startManagerForSpec(ctx)
		Expect((&controller.ClusterImagePolicyReconciler{
			Client:   mgr.GetClient(),
			Recorder: mgr.GetEventRecorderFor("squirrel-controller-test"),
		}).SetupWithManager(mgr)).To(Succeed())

		mgrCtx, cancel := context.WithCancel(ctx)
		DeferCleanup(cancel)
		cli := runManager(mgrCtx, mgr)

		const name = "integ-delete"

		good := &squirrelv1alpha1.ClusterImagePolicy{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: squirrelv1alpha1.ClusterImagePolicySpec{
				DefaultTarget: &squirrelv1alpha1.Target{Registry: "mirror.internal"},
				Rules:         []squirrelv1alpha1.Rule{{Match: matchJSON("docker.io/library/nginx:1.21")}},
			},
		}
		Expect(cli.Create(ctx, good)).To(Succeed())
		var firstUID types.UID
		Eventually(func(g Gomega) {
			var p squirrelv1alpha1.ClusterImagePolicy
			g.Expect(cli.Get(ctx, client.ObjectKey{Name: name}, &p)).To(Succeed())
			cond := apimeta.FindStatusCondition(p.Status.Conditions, squirrelv1alpha1.ConditionAccepted)
			g.Expect(cond).NotTo(BeNil())
			g.Expect(string(cond.Status)).To(Equal("True"))
			firstUID = p.UID
		}, 15*time.Second, 100*time.Millisecond).Should(Succeed())
		Expect(firstUID).NotTo(BeEmpty())

		Expect(cli.Delete(ctx, good)).To(Succeed())
		Eventually(func(g Gomega) {
			var p squirrelv1alpha1.ClusterImagePolicy
			err := cli.Get(ctx, client.ObjectKey{Name: name}, &p)
			g.Expect(apierrors.IsNotFound(err)).To(BeTrue(), "policy must be gone after Delete")
		}, 15*time.Second, 100*time.Millisecond).Should(Succeed())

		// Recreate the same name with an invalid spec. A leftover
		// Accepted=True from the first lifetime would mean the
		// resurrected policy never flips to Accepted=False; that
		// would be the regression this spec guards against.
		bad := &squirrelv1alpha1.ClusterImagePolicy{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: squirrelv1alpha1.ClusterImagePolicySpec{
				Rules: []squirrelv1alpha1.Rule{{Match: matchJSON("docker.io/library/nginx:1.21")}},
			},
		}
		Expect(cli.Create(ctx, bad)).To(Succeed())
		Eventually(func(g Gomega) {
			var p squirrelv1alpha1.ClusterImagePolicy
			g.Expect(cli.Get(ctx, client.ObjectKey{Name: name}, &p)).To(Succeed())
			g.Expect(p.UID).NotTo(Equal(firstUID),
				"resurrected policy must have a fresh UID from the apiserver")
			cond := apimeta.FindStatusCondition(p.Status.Conditions, squirrelv1alpha1.ConditionAccepted)
			g.Expect(cond).NotTo(BeNil())
			g.Expect(string(cond.Status)).To(Equal("False"),
				"resurrected invalid policy must be rejected on its own merits")
		}, 15*time.Second, 100*time.Millisecond).Should(Succeed())
	})
})

var _ = Describe("ImagePolicy reconciler", func() {
	// Mirrors the cluster-scoped suite for the namespaced sibling.
	// The two reconcilers share the underlying validation layer but
	// have independent SetupWithManager wiring and namespace-scoped
	// status semantics.

	It("lands Accepted=False on a malformed policy and Accepted=True on a clean one", func(ctx SpecContext) {
		mgr := startManagerForSpec(ctx)
		Expect((&controller.ImagePolicyReconciler{
			Client:   mgr.GetClient(),
			Recorder: mgr.GetEventRecorderFor("squirrel-controller-test"),
		}).SetupWithManager(mgr)).To(Succeed())

		mgrCtx, cancel := context.WithCancel(ctx)
		DeferCleanup(cancel)
		cli := runManager(mgrCtx, mgr)

		const ns = "integ-imagepolicy"
		Expect(cli.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})).To(Succeed())

		bad := &squirrelv1alpha1.ImagePolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "integ-bad", Namespace: ns},
			Spec: squirrelv1alpha1.ImagePolicySpec{
				Rules: []squirrelv1alpha1.Rule{{Match: matchJSON("docker.io/library/nginx:1.21")}},
			},
		}
		Expect(cli.Create(ctx, bad)).To(Succeed())
		Eventually(func(g Gomega) {
			var p squirrelv1alpha1.ImagePolicy
			g.Expect(cli.Get(ctx, client.ObjectKey{Namespace: ns, Name: "integ-bad"}, &p)).To(Succeed())
			cond := apimeta.FindStatusCondition(p.Status.Conditions, squirrelv1alpha1.ConditionAccepted)
			g.Expect(cond).NotTo(BeNil())
			g.Expect(string(cond.Status)).To(Equal("False"))
		}, 15*time.Second, 100*time.Millisecond).Should(Succeed())

		good := &squirrelv1alpha1.ImagePolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "integ-good", Namespace: ns},
			Spec: squirrelv1alpha1.ImagePolicySpec{
				DefaultTarget: &squirrelv1alpha1.Target{Registry: "mirror.internal"},
				Rules:         []squirrelv1alpha1.Rule{{Match: matchJSON("docker.io/library/nginx:1.21")}},
			},
		}
		Expect(cli.Create(ctx, good)).To(Succeed())
		Eventually(func(g Gomega) {
			var p squirrelv1alpha1.ImagePolicy
			g.Expect(cli.Get(ctx, client.ObjectKey{Namespace: ns, Name: "integ-good"}, &p)).To(Succeed())
			cond := apimeta.FindStatusCondition(p.Status.Conditions, squirrelv1alpha1.ConditionAccepted)
			g.Expect(cond).NotTo(BeNil())
			g.Expect(string(cond.Status)).To(Equal("True"))
		}, 15*time.Second, 100*time.Millisecond).Should(Succeed())
	})
})

var _ = Describe("ClusterImagePolicy CRD CEL gates", func() {
	// Apiserver-side enforcement (no reconciler required). Each spec
	// creates a direct client off suiteCfg, attempts a Create with
	// a known-bad spec, and asserts the apiserver rejects it.
	// Without this coverage a regression that breaks the CEL
	// expression (or pushes its cost back over budget) would slip
	// past unit tests entirely: the CRD would install but a known-
	// bad spec would unexpectedly succeed.

	var cli client.Client
	BeforeEach(func() {
		var err error
		cli, err = client.New(suiteCfg, client.Options{Scheme: suiteScheme})
		Expect(err).NotTo(HaveOccurred())
	})

	mkRule := func(target *squirrelv1alpha1.Target) squirrelv1alpha1.Rule {
		return squirrelv1alpha1.Rule{
			Match:  matchJSON("docker.io/library/nginx:1.21"),
			Target: target,
		}
	}

	It("rejects both tag and tags set on the same target", func(ctx SpecContext) {
		policy := &squirrelv1alpha1.ClusterImagePolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "integ-both-tag-forms"},
			Spec: squirrelv1alpha1.ClusterImagePolicySpec{
				Rules: []squirrelv1alpha1.Rule{mkRule(&squirrelv1alpha1.Target{
					Registry: "mirror.internal",
					Tag:      "stable",
					Tags:     []string{"latest"},
				})},
			},
		}
		Expect(cli.Create(ctx, policy)).NotTo(Succeed())
	})

	It("rejects a tag exceeding MaxLength=256", func(ctx SpecContext) {
		long := make([]byte, 257)
		for i := range long {
			long[i] = 'x'
		}
		policy := &squirrelv1alpha1.ClusterImagePolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "integ-long-tag"},
			Spec: squirrelv1alpha1.ClusterImagePolicySpec{
				Rules: []squirrelv1alpha1.Rule{mkRule(&squirrelv1alpha1.Target{
					Registry: "mirror.internal",
					Tag:      string(long),
				})},
			},
		}
		Expect(cli.Create(ctx, policy)).NotTo(Succeed())
	})

	It("rejects a tags slice exceeding MaxItems=16", func(ctx SpecContext) {
		tags := make([]string, 17)
		for i := range tags {
			tags[i] = "tag"
		}
		policy := &squirrelv1alpha1.ClusterImagePolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "integ-too-many-tags"},
			Spec: squirrelv1alpha1.ClusterImagePolicySpec{
				Rules: []squirrelv1alpha1.Rule{mkRule(&squirrelv1alpha1.Target{
					Registry: "mirror.internal",
					Tags:     tags,
				})},
			},
		}
		Expect(cli.Create(ctx, policy)).NotTo(Succeed())
	})

	It("rejects a rules slice exceeding MaxItems=128", func(ctx SpecContext) {
		rules := make([]squirrelv1alpha1.Rule, 129)
		for i := range rules {
			rules[i] = mkRule(nil)
		}
		policy := &squirrelv1alpha1.ClusterImagePolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "integ-too-many-rules"},
			Spec: squirrelv1alpha1.ClusterImagePolicySpec{
				DefaultTarget: &squirrelv1alpha1.Target{Registry: "mirror.internal"},
				Rules:         rules,
			},
		}
		Expect(cli.Create(ctx, policy)).NotTo(Succeed())
	})

	// Positive form of the tag/tags exclusion gate: with the CEL rule
	// reading !(has(self.tag) && size(self.tag) > 0 && has(self.tags) &&
	// size(self.tags) > 0), a target that EXPLICITLY sets tag="" and
	// tags=[] (both present-but-empty) is permitted because both
	// size() checks return zero. Without this positive spec a future
	// CEL tweak that changed presence semantics (e.g. dropping the
	// size() guards in favour of has()) could regress to rejecting
	// the empty-empty form silently; the rejection cases alone do not
	// distinguish "both set" from "tag empty, tags empty".
	It("accepts an explicitly-empty tag and tags pair (both size 0)", func(ctx SpecContext) {
		policy := &squirrelv1alpha1.ClusterImagePolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "integ-empty-tag-and-tags"},
			Spec: squirrelv1alpha1.ClusterImagePolicySpec{
				Rules: []squirrelv1alpha1.Rule{mkRule(&squirrelv1alpha1.Target{
					Registry: "mirror.internal",
					Tag:      "",
					Tags:     []string{},
				})},
			},
		}
		Expect(cli.Create(ctx, policy)).To(Succeed())
	})
})
