package manager

import (
	"errors"
	"fmt"

	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	client "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/RemkoMolier/squirrel/internal/certs"
	"github.com/RemkoMolier/squirrel/internal/controller"
	sqwebhook "github.com/RemkoMolier/squirrel/internal/webhook"
)

// PodMutatorPath is the URL path the PodMutator handler is registered
// on. It matches the path baked into the MWC's clientConfig.service
// path by the +kubebuilder:webhook marker in internal/webhook/doc.go;
// changing one without the other would break admission silently.
const PodMutatorPath = "/mutate-pod-v1"

// SetupManager wires every squirrel component into mgr against the
// provided Options. The order is deliberate:
//
//  1. Register the reconcilers so the manager knows what to watch.
//  2. Build the cert source so the rotation Runnable can be added.
//  3. Register the webhook handler.
//  4. Add the rotation Runnable (must come after the source exists).
//
// The function only configures mgr; it does NOT call mgr.Start.
// Calling Start is the orchestrator's responsibility so a single
// SetupManager call site works for the production main and for any
// future integration test that wants to bring up a partially-wired
// manager.
func SetupManager(mgr ctrl.Manager, opts *Options) error {
	if mgr == nil {
		return errors.New("SetupManager: mgr must not be nil")
	}
	if opts == nil {
		return errors.New("SetupManager: opts must not be nil")
	}

	if err := setupReconcilers(mgr); err != nil {
		return fmt.Errorf("setup reconcilers: %w", err)
	}

	// The cert source reads/writes the webhook Secret and patches the
	// MWC caBundle from the AuthoritativeRunnable. Using the cache-backed
	// mgr.GetClient() here would start a Secret informer on the first
	// Get, which needs cluster-wide (or at least namespaced) list/watch
	// on Secrets. The shipped RBAC scopes Secrets to a single
	// namespaced Role with get/create/update/patch on a single named
	// Secret - no list/watch - so the cached path would fail at
	// manager start. A direct client built from mgr.GetConfig() bypasses
	// the cache entirely (same pattern as bootstrapCerts in run.go).
	directCli, err := client.New(mgr.GetConfig(), client.Options{
		Scheme: mgr.GetScheme(),
		Mapper: mgr.GetRESTMapper(),
	})
	if err != nil {
		return fmt.Errorf("build direct client for cert source: %w", err)
	}
	src, err := NewCertSource(directCli, opts)
	if err != nil {
		return fmt.Errorf("setup cert source: %w", err)
	}

	setupWebhook(mgr, opts)

	if err := setupCertRunnables(mgr, src, opts); err != nil {
		return fmt.Errorf("setup cert runnables: %w", err)
	}

	return nil
}

// setupReconcilers wires the ClusterImagePolicy and ImagePolicy
// reconcilers into mgr. Each reconciler's SetupWithManager already
// installs the GenerationChangedPredicate that elides
// self-trigger events from status writes, so no extra plumbing is
// needed here.
func setupReconcilers(mgr ctrl.Manager) error {
	// Both reconcilers share a single EventRecorder from the manager
	// so Accepted=False outcomes show up under
	// `kubectl describe (cluster)imagepolicy`. controller-runtime
	// recently added a parallel GetEventRecorder that returns the
	// newer events.EventRecorder interface; we stay on the
	// record.EventRecorder API until the surrounding kubebuilder
	// ecosystem migrates.
	recorder := eventRecorderFor(mgr, "squirrel-controller")
	cipr := &controller.ClusterImagePolicyReconciler{
		Client:   mgr.GetClient(),
		Recorder: recorder,
	}
	if err := cipr.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("ClusterImagePolicy reconciler: %w", err)
	}
	ipr := &controller.ImagePolicyReconciler{
		Client:   mgr.GetClient(),
		Recorder: recorder,
	}
	if err := ipr.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("ImagePolicy reconciler: %w", err)
	}
	return nil
}

// NewCertSource constructs the CertSource implementation matching
// opts.CertSource. It is exported separately from SetupManager so
// tests can exercise the construction path without needing a full
// controller-runtime Manager - in particular the self-signed branch
// reaches into certs.NewSelfSignedSource, whose validation is the
// most credible source of misconfigured-Options errors at startup.
//
// Multi-instance note: production calls NewCertSource twice per
// process - once in bootstrapCerts (run.go, before mgr.Start) and
// once in SetupManager (here, for the cert runnables). Each
// invocation produces a fresh CertSource value over a fresh direct
// client. Both shipped sources are stateless (SelfSignedSource and
// CertManagerSource hold only their opts), so the duplication is
// harmless: each call returns an equivalent value. A future
// stateful source - e.g. an in-memory cache of last-observed Secret
// resourceVersion - would need either a single-construct path or a
// stateful registry; until then the two-instance pattern keeps
// run.go self-contained at bootstrap time without depending on
// mgr.GetCache().
func NewCertSource(cli client.Client, opts *Options) (certs.CertSource, error) {
	if opts == nil {
		return nil, errors.New("NewCertSource: opts must not be nil")
	}
	switch opts.CertSource {
	case CertSourceSelfSigned:
		return certs.NewSelfSignedSource(certs.SelfSignedSourceOpts{
			Client: cli,
			SecretKey: client.ObjectKey{
				Name:      opts.WebhookSecretName,
				Namespace: opts.WebhookSecretNamespace,
			},
			MWCName:           opts.MWCName,
			CommonName:        opts.CommonName,
			DNSNames:          opts.WebhookDNSNames(),
			CertDir:           opts.CertDir,
			RotationThreshold: opts.RotationThreshold,
			CAValidity:        opts.CAValidity,
			ServingValidity:   opts.ServingValidity,
		})
	case CertSourceCertManager:
		// Client deliberately unused: cert-manager owns the Secret
		// and the MWC injection. Passing the client through would
		// invite a future regression where the source starts
		// patching what is not its to patch.
		return certs.NewCertManagerSource(opts.CertDir)
	default:
		return nil, fmt.Errorf("NewCertSource: unknown cert source %q (want %q or %q)",
			opts.CertSource, CertSourceSelfSigned, CertSourceCertManager)
	}
}

// setupWebhook registers the PodMutator handler on the manager's
// webhook server at PodMutatorPath. The handler's Client is the
// cache-backed manager client; OperatorNamespace comes straight from
// opts.Namespace. The function returns no error - Register's only
// failure mode is a duplicate path, which we statically prevent by
// using a single package-level constant for the path.
func setupWebhook(mgr ctrl.Manager, opts *Options) {
	var extraReserved map[string]struct{}
	if len(opts.ExtraReservedNamespaces) > 0 {
		extraReserved = make(map[string]struct{}, len(opts.ExtraReservedNamespaces))
		for _, ns := range opts.ExtraReservedNamespaces {
			extraReserved[ns] = struct{}{}
		}
	}
	handler := &sqwebhook.PodMutator{
		Client:                  mgr.GetClient(),
		Decoder:                 admission.NewDecoder(mgr.GetScheme()),
		OperatorNamespace:       opts.Namespace,
		ExtraReservedNamespaces: extraReserved,
	}
	mgr.GetWebhookServer().Register(PodMutatorPath, &webhook.Admission{Handler: handler})
}

// setupCertRunnables wraps src in the two cert runnables and adds
// them to the manager:
//
//   - AuthoritativeRunnable opts INTO leader election so only one
//     replica writes the Secret + patches the MWC.
//   - LocalSyncRunnable opts OUT of leader election so every replica
//     keeps its own CertDir current against whatever the leader
//     wrote.
//
// Both runnables share the same source instance.
func setupCertRunnables(mgr ctrl.Manager, src certs.CertSource, opts *Options) error {
	log := mgr.GetLogger().WithName("certs")
	auth, err := certs.NewAuthoritativeRunnable(src, opts.RotationInterval, log)
	if err != nil {
		return fmt.Errorf("new authoritative runnable: %w", err)
	}
	if addErr := mgr.Add(auth); addErr != nil {
		return fmt.Errorf("add authoritative runnable: %w", addErr)
	}
	local, err := certs.NewLocalSyncRunnable(src, opts.RotationInterval, log)
	if err != nil {
		return fmt.Errorf("new local-sync runnable: %w", err)
	}
	if addErr := mgr.Add(local); addErr != nil {
		return fmt.Errorf("add local-sync runnable: %w", addErr)
	}
	return nil
}

// eventRecorderFor wraps controller-runtime's deprecated
// GetEventRecorderFor so the single SA1019 suppression has one
// home rather than being sprinkled across every call site.
// controller-runtime added a parallel GetEventRecorder that returns
// the newer events.EventRecorder interface; we stay on
// record.EventRecorder until the surrounding kubebuilder ecosystem
// migrates (it requires changing every Event() / Eventf() call to
// the regarding/related/action/note shape, which we'd want to do in
// one consolidated PR).
func eventRecorderFor(mgr ctrl.Manager, name string) record.EventRecorder {
	return mgr.GetEventRecorderFor(name) //nolint:staticcheck // SA1019: see eventRecorderFor godoc.
}
