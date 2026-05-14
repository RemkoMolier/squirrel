package manager

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

// CertSource string constants. The flag parser accepts these
// case-sensitively; ParseOptions translates them into the
// corresponding internal/certs implementation in the setup layer.
const (
	CertSourceSelfSigned  = "self-signed"
	CertSourceCertManager = "cert-manager"
)

// Design-derived defaults. They live as constants so a future ADR
// change is a single edit and so tests can reference them by name.
//
// Bind-address defaults note: ":8080" / ":8081" mean "all
// interfaces", which is the right shape inside Kubernetes where
// Prometheus scrapes /metrics via the Pod IP and the kubelet hits
// the probe ports the same way. The bind is contained by the Pod
// network namespace; the Pod's NetworkPolicy (operator-supplied)
// is the gate, not the bind address. Operators running the binary
// outside Kubernetes should override these via the --metrics-bind-
// address and --health-probe-bind-address flags to a localhost
// form. See ADR-tracked decision around upstream controller-runtime
// conventions.
const (
	DefaultMetricsAddr       = ":8080"
	DefaultProbeAddr         = ":8081"
	DefaultLeaderElectionID  = "squirrel.molier.dev"
	DefaultCertSource        = CertSourceSelfSigned
	DefaultCertDir           = "/tmp/k8s-webhook-server/serving-certs"
	DefaultWebhookSecretName = "squirrel-webhook-tls"
	DefaultMWCName           = "squirrel-image-rewrite"
	DefaultServiceName       = "squirrel-webhook"
	DefaultCommonName        = "squirrel-ca"
	DefaultWebhookPort       = 9443
	DefaultRotationInterval  = time.Hour
	DefaultRotationThreshold = 30 * 24 * time.Hour
	DefaultCAValidity        = 5 * 365 * 24 * time.Hour
	DefaultServingValidity   = 365 * 24 * time.Hour
	podNamespaceEnvVar       = "POD_NAMESPACE"
)

// Options is the value the rest of the manager package consumes.
// Construct it via ParseOptions (production) or by literal (tests);
// Validate must be called before the value is handed to SetupManager.
type Options struct {
	// MetricsAddr is the listen address (`host:port` or `:port`) for
	// the Prometheus /metrics endpoint.
	MetricsAddr string

	// ProbeAddr is the listen address for /healthz and /readyz.
	ProbeAddr string

	// LeaderElect controls whether the manager runs leader election
	// for its reconcilers. The cert-source Runnable opts out of
	// leader election independently of this flag.
	LeaderElect bool

	// LeaderElectionID is the lease object the manager elects on.
	LeaderElectionID string

	// Namespace is the operator's own namespace. The webhook handler
	// refuses to mutate Pods in this namespace as a defensive
	// complement to the MWC namespaceSelector. ParseOptions falls
	// back to the POD_NAMESPACE downward-API env var when the flag
	// is unset; Validate then requires the field be non-empty.
	Namespace string

	// ExtraReservedNamespaces lists additional namespaces the webhook
	// refuses to mutate beyond the built-in vanilla set (kube-system,
	// kube-public, kube-node-lease) and Namespace itself. Operators
	// running on opinionated distributions populate this with their
	// distro-specific control-plane namespaces; the typical baseline
	// is documented in INSTALL.md (openshift-* on OpenShift,
	// tigera-operator/calico-system on Calico, etc.). Empty on vanilla
	// clusters.
	ExtraReservedNamespaces []string

	// CertSource selects which CertSource implementation drives the
	// webhook's TLS material. Must be "self-signed" or "cert-manager".
	CertSource string

	// CertDir is the on-disk directory the webhook server reads
	// tls.crt and tls.key from. The SelfSignedSource writes the
	// material here; the cert-manager mode expects this directory to
	// be a Secret-projected volume cert-manager owns.
	CertDir string

	// WebhookSecretName is the name of the Secret the SelfSignedSource
	// reads and writes. Ignored in cert-manager mode.
	WebhookSecretName string

	// WebhookSecretNamespace is the namespace of the webhook-cert
	// Secret. ParseOptions defaults this to Namespace when the flag
	// is unset.
	WebhookSecretNamespace string

	// MWCName is the name of the MutatingWebhookConfiguration the
	// SelfSignedSource publishes the CA bundle into. Ignored in
	// cert-manager mode (cert-manager.io/inject-ca-from handles it).
	MWCName string

	// ServiceName is the in-cluster Service name through which the
	// apiserver dials the webhook. Used to derive the SubjectAltName
	// list for self-signed serving certificates.
	ServiceName string

	// CommonName is the Subject.CommonName on the self-signed CA.
	// Cosmetic; appears in `kubectl describe secret` output.
	CommonName string

	// WebhookPort is the TCP port the webhook server listens on.
	WebhookPort int

	// RotationInterval is the cadence the cert runnables call
	// CertSource.Authoritative and CertSource.Localize at.
	RotationInterval time.Duration

	// RotationThreshold is the duration before NotAfter at which the
	// SelfSignedSource rotates material. Ignored in cert-manager mode.
	RotationThreshold time.Duration

	// CAValidity is the lifetime of a freshly-minted self-signed CA.
	// Ignored in cert-manager mode.
	CAValidity time.Duration

	// ServingValidity is the lifetime of a freshly-minted serving
	// certificate. Ignored in cert-manager mode.
	ServingValidity time.Duration
}

// ParseOptions parses command-line flags and the POD_NAMESPACE
// environment variable into an Options. The returned value is NOT
// validated; callers should invoke Validate before use.
//
// args is the flag-arg slice (typically os.Args[1:]); passing it
// explicitly rather than reading os.Args directly keeps the function
// pure and unit-testable.
func ParseOptions(args []string) (*Options, error) {
	opts := &Options{}
	fs := flag.NewFlagSet("squirrel-manager", flag.ContinueOnError)

	fs.StringVar(&opts.MetricsAddr, "metrics-bind-address", DefaultMetricsAddr,
		"Address (host:port or :port) the Prometheus /metrics endpoint listens on.")
	fs.StringVar(&opts.ProbeAddr, "health-probe-bind-address", DefaultProbeAddr,
		"Address the /healthz and /readyz endpoints listen on.")
	fs.BoolVar(&opts.LeaderElect, "leader-elect", false,
		"Enable leader election so only one replica reconciles policies and writes cert material at a time. The cert local-sync runnable runs on every replica regardless.")
	fs.StringVar(&opts.LeaderElectionID, "leader-election-id", DefaultLeaderElectionID,
		"Name of the Lease the manager elects on.")

	fs.StringVar(&opts.Namespace, "namespace", "",
		"Operator's own namespace. Defaults to the POD_NAMESPACE downward-API env var; the webhook handler refuses to mutate Pods in this namespace.")
	fs.Func("reserved-namespaces", "Comma-separated list of additional namespaces the webhook must never mutate, beyond the built-in vanilla set (kube-system, kube-public, kube-node-lease) and the operator's own. Use to cover distro-specific control-plane namespaces (e.g. openshift-machine-api,openshift-etcd or istio-system,calico-system). May be passed multiple times; values accumulate.", func(s string) error {
		for _, ns := range strings.Split(s, ",") {
			if trimmed := strings.TrimSpace(ns); trimmed != "" {
				opts.ExtraReservedNamespaces = append(opts.ExtraReservedNamespaces, trimmed)
			}
		}
		return nil
	})

	fs.StringVar(&opts.CertSource, "cert-source", DefaultCertSource,
		`Cert-source mode: "self-signed" (default, mints and rotates its own material) or "cert-manager" (passive verifier of a cert-manager-owned Secret).`)
	fs.StringVar(&opts.CertDir, "cert-dir", DefaultCertDir,
		"On-disk directory the webhook server reads tls.crt / tls.key from.")

	fs.StringVar(&opts.WebhookSecretName, "webhook-secret-name", DefaultWebhookSecretName,
		"Name of the Secret the SelfSignedSource reads and writes.")
	fs.StringVar(&opts.WebhookSecretNamespace, "webhook-secret-namespace", "",
		"Namespace of the webhook-cert Secret. Defaults to --namespace.")
	fs.StringVar(&opts.MWCName, "mwc-name", DefaultMWCName,
		"Name of the MutatingWebhookConfiguration the SelfSignedSource publishes the CA bundle into.")
	fs.StringVar(&opts.ServiceName, "service-name", DefaultServiceName,
		"In-cluster Service name through which the apiserver dials the webhook; used to derive SubjectAltNames.")
	fs.StringVar(&opts.CommonName, "ca-common-name", DefaultCommonName,
		"Subject.CommonName on the self-signed CA.")
	fs.IntVar(&opts.WebhookPort, "webhook-port", DefaultWebhookPort,
		"TCP port the webhook server listens on.")

	fs.DurationVar(&opts.RotationInterval, "rotation-interval", DefaultRotationInterval,
		"Cadence at which the cert runnables call CertSource.Authoritative (leader) and CertSource.Localize (every replica).")
	fs.DurationVar(&opts.RotationThreshold, "rotation-threshold", DefaultRotationThreshold,
		"Duration before NotAfter at which the SelfSignedSource rotates material.")
	fs.DurationVar(&opts.CAValidity, "ca-validity", DefaultCAValidity,
		"Lifetime of a freshly-minted self-signed CA.")
	fs.DurationVar(&opts.ServingValidity, "serving-validity", DefaultServingValidity,
		"Lifetime of a freshly-minted serving certificate.")

	if err := fs.Parse(args); err != nil {
		return nil, fmt.Errorf("parse flags: %w", err)
	}

	// Namespace falls back to the downward-API env var. Operators
	// typically inject it via spec.template.spec.containers[].env
	// pointing at metadata.namespace.
	if opts.Namespace == "" {
		opts.Namespace = os.Getenv(podNamespaceEnvVar)
	}
	if opts.WebhookSecretNamespace == "" {
		opts.WebhookSecretNamespace = opts.Namespace
	}

	return opts, nil
}

// Validate enforces cross-field invariants and rejects values the
// rest of the wiring cannot make sense of. Production callers should
// fail-fast on a non-nil return; the manager has no useful default
// behaviour when its configuration is broken.
func (o *Options) Validate() error {
	if o == nil {
		return errors.New("Options: nil")
	}
	if o.Namespace == "" {
		return fmt.Errorf("--namespace is required (or set $%s via the downward API)", podNamespaceEnvVar)
	}
	switch o.CertSource {
	case CertSourceSelfSigned, CertSourceCertManager:
	default:
		return fmt.Errorf("--cert-source: got %q, want %q or %q", o.CertSource, CertSourceSelfSigned, CertSourceCertManager)
	}
	if o.CertDir == "" {
		return errors.New("--cert-dir must not be empty")
	}
	if o.CertSource == CertSourceSelfSigned {
		// These fields only matter to SelfSignedSource: cert-manager
		// mode does not read them, so a misconfigured cert-manager
		// deployment is not penalised for leaving them empty.
		if o.WebhookSecretName == "" {
			return errors.New("--webhook-secret-name must not be empty in self-signed mode")
		}
		if o.WebhookSecretNamespace == "" {
			return errors.New("--webhook-secret-namespace must not be empty in self-signed mode (defaults to --namespace)")
		}
		if o.MWCName == "" {
			return errors.New("--mwc-name must not be empty in self-signed mode")
		}
		if o.ServiceName == "" {
			return errors.New("--service-name must not be empty in self-signed mode")
		}
		if o.CommonName == "" {
			return errors.New("--ca-common-name must not be empty in self-signed mode")
		}
		if o.RotationThreshold <= 0 {
			return fmt.Errorf("--rotation-threshold (%s) must be positive", o.RotationThreshold)
		}
		if o.CAValidity <= 0 {
			return fmt.Errorf("--ca-validity (%s) must be positive", o.CAValidity)
		}
		if o.ServingValidity <= 0 {
			return fmt.Errorf("--serving-validity (%s) must be positive", o.ServingValidity)
		}
		if o.ServingValidity > o.CAValidity {
			// Not strictly broken (the SelfSignedSource caps the serving
			// NotAfter at the CA's NotAfter in this case) but almost
			// always a configuration mistake: a serving cert that is
			// "longer-lived" than the CA gets capped down at mint time,
			// silently producing a shorter cert than the operator
			// expected. Surface it.
			return fmt.Errorf("--serving-validity (%s) must not exceed --ca-validity (%s)", o.ServingValidity, o.CAValidity)
		}
	}
	if o.WebhookPort <= 0 || o.WebhookPort > 65535 {
		return fmt.Errorf("--webhook-port (%d) must be in (0, 65535]", o.WebhookPort)
	}
	if o.RotationInterval <= 0 {
		return fmt.Errorf("--rotation-interval (%s) must be positive", o.RotationInterval)
	}
	return nil
}

// WebhookDNSNames returns the SubjectAltName list the SelfSignedSource
// uses for its serving certificate. The names are derived from
// ServiceName and Namespace per Kubernetes' standard service DNS
// shape; operators with non-standard service exposure will need to
// extend this method (or accept additional SANs via a future flag).
//
// The namespace component is Options.Namespace (where the webhook
// Service lives and the apiserver dials), NOT WebhookSecretNamespace.
// When the two differ, a SAN derived from the Secret namespace would
// fail TLS hostname verification at handshake time because the
// apiserver dials by Service DNS, not Secret DNS.
func (o *Options) WebhookDNSNames() []string {
	return []string{
		fmt.Sprintf("%s.%s.svc", o.ServiceName, o.Namespace),
		fmt.Sprintf("%s.%s.svc.cluster.local", o.ServiceName, o.Namespace),
	}
}
