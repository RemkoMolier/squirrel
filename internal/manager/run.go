package manager

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	runtime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	client "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	squirrelv1alpha1 "github.com/RemkoMolier/squirrel/api/v1alpha1"
)

// Run validates opts, builds the controller-runtime Manager that
// matches them, hands it to SetupManager, and starts everything.
// Run blocks until ctx is cancelled or the manager terminates with
// an error.
//
// The cmd/manager shim is the typical caller: parses flags into
// Options and calls Run with a signal-handled context. Production
// integration tests can call Run against a synthetic Options just
// as easily.
func Run(ctx context.Context, opts *Options) error {
	if err := opts.Validate(); err != nil {
		return fmt.Errorf("validate options: %w", err)
	}

	cfg, err := ctrl.GetConfig()
	if err != nil {
		return fmt.Errorf("load kubeconfig: %w", err)
	}

	scheme, err := buildScheme()
	if err != nil {
		return fmt.Errorf("build scheme: %w", err)
	}

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: opts.MetricsAddr},
		HealthProbeBindAddress: opts.ProbeAddr,
		LeaderElection:         opts.LeaderElect,
		LeaderElectionID:       opts.LeaderElectionID,
		// LeaderElectionNamespace defaults to the namespace the manager's
		// in-cluster ServiceAccount runs in, which is the operator's own
		// namespace - aligned with where the Lease should live.
		//
		// LeaderElectionReleaseOnCancel: a clean shutdown (SIGTERM
		// from the kubelet during a rolling update) deletes the
		// Lease so the incoming replica can grab leadership on its
		// first pass instead of waiting out the lease duration. The
		// trade-off documented by controller-runtime is that
		// non-graceful exits (panics, hard kills) STILL leave a
		// lease behind that expires on its own; that is exactly the
		// behaviour the LeaseDuration is sized for. Gated on
		// opts.LeaderElect: without leader election, releasing the
		// lease has no meaning and controller-runtime may surface
		// surprising warnings.
		LeaderElectionReleaseOnCancel: opts.LeaderElect,
		WebhookServer: webhook.NewServer(webhook.Options{
			Port:    opts.WebhookPort,
			CertDir: opts.CertDir,
			// CertName / KeyName default to tls.crt / tls.key, which
			// matches certs.FileServingCert / certs.FileServingKey.
		}),
	})
	if err != nil {
		return fmt.Errorf("new manager: %w", err)
	}

	if err := mgr.AddHealthzCheck("ping", healthz.Ping); err != nil {
		return fmt.Errorf("add healthz: %w", err)
	}
	// /readyz returns 200 only once the controller-runtime cache has
	// synced. Before sync the webhook resolver would see an empty
	// policy set and silently admit Pods whose images should have
	// been rewritten - shallow readiness would let the kubelet route
	// admissions through the webhook while it is still incapable of
	// answering them correctly.
	if err := mgr.AddReadyzCheck("informers-synced", cacheSyncReadyz(mgr)); err != nil { //nolint:contextcheck // cacheSyncReadyz deliberately ignores request context; see its doc comment.
		return fmt.Errorf("add readyz: %w", err)
	}

	// Bootstrap the cert material synchronously, before mgr.Start.
	// controller-runtime starts the webhook server early in its
	// startup sequence; the webhook server's cert watcher reads
	// tls.crt / tls.key from CertDir as soon as it boots, so if
	// those files are not on disk yet the webhook server fails
	// before the AuthoritativeRunnable's first leader-elected tick
	// could mint them. Running an initial Authoritative + Localize
	// here closes that race. The AuthoritativeRunnable then performs
	// idempotent rotation checks on the leader; the LocalSyncRunnable
	// keeps every replica's CertDir current.
	if err := bootstrapCerts(ctx, cfg, scheme, opts); err != nil {
		return fmt.Errorf("bootstrap certs: %w", err)
	}

	if err := SetupManager(mgr, opts); err != nil {
		return fmt.Errorf("setup manager: %w", err)
	}

	if err := mgr.Start(ctx); err != nil {
		return fmt.Errorf("manager exited: %w", err)
	}
	return nil
}

// bootstrapCertsTimeout bounds how long a single bootstrap attempt
// (Authoritative + Localize) is allowed to run before the operator
// gives up and lets the orchestrator restart it. 30s is well above
// a healthy apiserver's response time; the deadline trips only when
// something is structurally wrong with the connection.
const bootstrapCertsTimeout = 30 * time.Second

// bootstrapCerts runs the initial Authoritative + Localize pass
// against a non-cached client built directly from cfg. The manager's
// own client cannot be used here because its cache has not started
// (mgr.Start is what starts the cache, and we run before that);
// reads would fail. A direct client bypasses the cache.
//
// In self-signed mode the Authoritative call mints the CA + serving
// material and publishes the CA into the MWC; Localize then writes
// the bundle to CertDir so the webhook server has TLS material on
// boot. In cert-manager mode Authoritative is a no-op and Localize
// verifies the mounted on-disk material parses; if cert-manager has
// not populated the Secret yet, bootstrap fails and Run returns the
// error rather than letting the manager start with no TLS.
//
// Multi-replica race: bootstrap runs on every replica before
// mgr.Start, so before leader election fires. The race is absorbed
// by certs.Ensure's Get-or-Create on the Secret - the apiserver
// serialises the Create; losers see AlreadyExists, re-read, and
// converge on the winner's material. After bootstrap the
// AuthoritativeRunnable runs only on the leader, so subsequent
// writes never race.
func bootstrapCerts(ctx context.Context, cfg *rest.Config, scheme *runtime.Scheme, opts *Options) error {
	direct, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return fmt.Errorf("direct client: %w", err)
	}
	src, err := NewCertSource(direct, opts)
	if err != nil {
		return fmt.Errorf("new cert source: %w", err)
	}
	// Bootstrap is synchronous and blocks mgr.Start, so an unbounded
	// apiserver hang here would deadlock startup forever - the
	// outer signal-handled context only cancels on SIGTERM, and a
	// liveness probe is not yet listening because mgr.Start has not
	// been called. A 30s deadline is well above an apiserver's
	// healthy response time, surfaces "operator cannot reach the
	// apiserver" as a startup failure, and lets the orchestrator's
	// restart loop retry rather than wedging the pod indefinitely.
	bootCtx, cancel := context.WithTimeout(ctx, bootstrapCertsTimeout)
	defer cancel()
	if err := src.Authoritative(bootCtx); err != nil {
		return fmt.Errorf("initial authoritative: %w", err)
	}
	if err := src.Localize(bootCtx); err != nil {
		return fmt.Errorf("initial localize: %w", err)
	}
	return nil
}

// buildScheme registers the API groups the manager uses:
//
//   - core/v1: Namespace (webhook OptInLabel check) and Secret
//     (SelfSignedSource read/write).
//   - admissionregistration/v1: the MutatingWebhookConfiguration the
//     SelfSignedSource patches.
//   - squirrel.molier.dev/v1alpha1: the policy kinds the reconcilers
//     watch.
func buildScheme() (*runtime.Scheme, error) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("add corev1: %w", err)
	}
	if err := admissionregistrationv1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("add admissionregistration/v1: %w", err)
	}
	if err := squirrelv1alpha1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("add squirrel/v1alpha1: %w", err)
	}
	return scheme, nil
}

// cacheSyncReadyz returns a healthz.Checker that fails until the
// manager's cache has synced every informer. Before sync, the
// webhook resolver's List calls return empty slices, which would
// silently admit Pods whose images should have been rewritten -
// reporting "not ready" until the cache is hot avoids that window.
//
// Implementation: controller-runtime's cache.Cache exposes a
// blocking WaitForCacheSync. The earlier "pre-cancelled context
// converts WaitForCacheSync into a non-blocking poll" approach
// was nondeterministic: WaitForCacheSync internally selects on
// {cache-started, ctx.Done()}, so an already-cancelled context
// could observe the ctx.Done() branch even after sync completed.
// In practice that left the Pod NotReady intermittently and the
// MWC Service intermittently empty.
//
// The current implementation:
//
//  1. Latches via an atomic.Bool. Once we observe a successful
//     sync, every subsequent check returns nil without re-entering
//     WaitForCacheSync. Informer caches do not "un-sync" inside
//     a running controller-runtime manager, so the latch is
//     correct and avoids the race entirely.
//  2. Uses a short timeout (not a pre-cancelled context) for the
//     pre-latch check. 100ms is well below the kubelet's probe
//     cadence; if WaitForCacheSync would block longer we simply
//     report not-ready and the kubelet retries.
//
// The per-probe context.WithTimeout allocation is intentional, not
// a perf bug: any shared context would either be already-cancelled
// (the pattern this code was rewritten to avoid) or require an
// external goroutine to refresh its deadline. The pre-latch window
// is short (a few kubelet probe cycles at boot) and the latch
// fast-path skips context creation entirely afterwards, so the
// allocation has no measurable cost in steady state.
func cacheSyncReadyz(mgr ctrl.Manager) healthz.Checker {
	var synced atomic.Bool
	return func(_ *http.Request) error { //nolint:contextcheck // intentional: probe owns its own timeout context; request context is irrelevant.
		if synced.Load() {
			return nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		if mgr.GetCache().WaitForCacheSync(ctx) {
			synced.Store(true)
			return nil
		}
		return errors.New("informer cache has not synced yet")
	}
}
