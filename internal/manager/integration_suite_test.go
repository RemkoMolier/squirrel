//go:build integration

// Ginkgo entrypoint for the envtest-backed integration suite.
//
// Single Test* function executes every Describe/It block; envtest
// + etcd start once per `go test` invocation in BeforeSuite and
// stop in AfterSuite. Specs share the *rest.Config and CRD load,
// but each It block stands up its own controller-runtime Manager
// so tests stay independent (per-Manager cache, per-Manager
// controllers, no cross-test informer leakage).
package manager_test

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	runtimepkg "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	client "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	squirrelv1alpha1 "github.com/RemkoMolier/squirrel/api/v1alpha1"
	"github.com/RemkoMolier/squirrel/internal/controller"
)

// suiteCfg is the rest.Config the envtest BeforeSuite hook
// populates. Specs read it (must not write); it is nil-safe to
// reference inside a Ginkgo node because BeforeSuite has already
// run by the time any It block executes.
var (
	suiteCfg    *rest.Config
	suiteEnv    *envtest.Environment
	suiteScheme *runtimepkg.Scheme
)

// TestManagerIntegration is the single Go test entrypoint Ginkgo
// hangs every spec off. `go test -tags integration ./internal/manager`
// runs this function; RunSpecs then dispatches to each It block.
func TestManagerIntegration(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Manager Integration Suite")
}

var _ = BeforeSuite(func() {
	suiteScheme = runtimepkg.NewScheme()
	Expect(apiextensionsv1.AddToScheme(suiteScheme)).To(Succeed())
	Expect(corev1.AddToScheme(suiteScheme)).To(Succeed())
	Expect(squirrelv1alpha1.AddToScheme(suiteScheme)).To(Succeed())

	suiteEnv = &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join(repoRootForSuite(), "config", "crd", "bases"),
		},
		ErrorIfCRDPathMissing: true,
	}

	cfg, err := suiteEnv.Start()
	Expect(err).NotTo(HaveOccurred(), "envtest.Start")
	Expect(cfg).NotTo(BeNil())
	suiteCfg = cfg
})

var _ = AfterSuite(func() {
	if suiteEnv != nil {
		Expect(suiteEnv.Stop()).To(Succeed())
	}
})

// repoRootForSuite returns the absolute path to the repository
// root, computed from this file's location. envtest needs the
// CRD manifests by absolute path; `go test`'s working directory
// is the package being tested, which is two levels below the
// repo root.
func repoRootForSuite() string {
	_, here, _, ok := runtime.Caller(0)
	if !ok {
		panic("runtime.Caller failed in repoRootForSuite")
	}
	return filepath.Join(filepath.Dir(here), "..", "..")
}

// acceptedTrue reports whether the named ClusterImagePolicy carries
// an Accepted=True condition. Returns false (with no error) until
// the condition appears, which is exactly the shape Gomega's
// Eventually wants: poll until truthy or time out.
func acceptedTrue(ctx context.Context, cli client.Client, name string) bool {
	var p squirrelv1alpha1.ClusterImagePolicy
	if err := cli.Get(ctx, client.ObjectKey{Name: name}, &p); err != nil {
		return false
	}
	cond := apimeta.FindStatusCondition(p.Status.Conditions, squirrelv1alpha1.ConditionAccepted)
	return cond != nil && string(cond.Status) == "True"
}

// bringUpMgrWithCIPReconciler is the per-spec scaffold the webhook
// specs share: stand up a new Manager against suiteCfg, register
// the ClusterImagePolicy reconciler (so policies in the cache
// resolve), start the Manager under a cancellable context, and
// return the running Manager + its client. The mgr's context is
// cancelled via DeferCleanup so each spec leaves no goroutines
// behind.
func bringUpMgrWithCIPReconciler(ctx context.Context) (ctrl.Manager, client.Client) {
	GinkgoHelper()
	mgr, err := ctrl.NewManager(suiteCfg, ctrl.Options{
		Scheme:     suiteScheme,
		Metrics:    metricsserver.Options{BindAddress: "0"},
		Controller: config.Controller{SkipNameValidation: ptrTrue()},
	})
	Expect(err).NotTo(HaveOccurred())

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
	Expect(mgr.GetCache().WaitForCacheSync(mgrCtx)).To(BeTrue())
	return mgr, mgr.GetClient()
}
