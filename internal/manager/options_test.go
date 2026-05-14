package manager_test

import (
	"strings"
	"testing"
	"time"

	"github.com/RemkoMolier/squirrel/internal/manager"
)

func TestParseOptionsAppliesDesignDefaults(t *testing.T) {
	// t.Setenv is incompatible with t.Parallel, but the env-mutating
	// tests are fast enough that running them sequentially adds no
	// meaningful runtime.

	// No flags, no POD_NAMESPACE: every default should match the
	// constants the design pins. Namespace is intentionally empty
	// here - Validate checks that, ParseOptions does not.
	t.Setenv("POD_NAMESPACE", "")

	opts, err := manager.ParseOptions(nil)
	if err != nil {
		t.Fatalf("ParseOptions: %v", err)
	}

	checks := []struct {
		name string
		got  any
		want any
	}{
		{"MetricsAddr", opts.MetricsAddr, manager.DefaultMetricsAddr},
		{"ProbeAddr", opts.ProbeAddr, manager.DefaultProbeAddr},
		{"LeaderElect", opts.LeaderElect, false},
		{"LeaderElectionID", opts.LeaderElectionID, manager.DefaultLeaderElectionID},
		{"CertSource", opts.CertSource, manager.DefaultCertSource},
		{"CertDir", opts.CertDir, manager.DefaultCertDir},
		{"WebhookSecretName", opts.WebhookSecretName, manager.DefaultWebhookSecretName},
		{"MWCName", opts.MWCName, manager.DefaultMWCName},
		{"ServiceName", opts.ServiceName, manager.DefaultServiceName},
		{"CommonName", opts.CommonName, manager.DefaultCommonName},
		{"WebhookPort", opts.WebhookPort, manager.DefaultWebhookPort},
		{"RotationInterval", opts.RotationInterval, manager.DefaultRotationInterval},
		{"RotationThreshold", opts.RotationThreshold, manager.DefaultRotationThreshold},
		{"CAValidity", opts.CAValidity, manager.DefaultCAValidity},
		{"ServingValidity", opts.ServingValidity, manager.DefaultServingValidity},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, c.got, c.want)
		}
	}
}

func TestParseOptionsFallsBackToPodNamespaceEnv(t *testing.T) {
	// No --namespace flag; POD_NAMESPACE points at the operator's
	// downward-API-injected namespace. WebhookSecretNamespace then
	// also falls back to it.
	t.Setenv("POD_NAMESPACE", "squirrel-system")

	opts, err := manager.ParseOptions(nil)
	if err != nil {
		t.Fatalf("ParseOptions: %v", err)
	}
	if got, want := opts.Namespace, "squirrel-system"; got != want {
		t.Errorf("Namespace: got %q, want %q (POD_NAMESPACE fallback)", got, want)
	}
	if got, want := opts.WebhookSecretNamespace, "squirrel-system"; got != want {
		t.Errorf("WebhookSecretNamespace: got %q, want %q (defaults to Namespace)", got, want)
	}
}

func TestParseOptionsFlagOverridesEnv(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "from-env")

	opts, err := manager.ParseOptions([]string{"--namespace=from-flag"})
	if err != nil {
		t.Fatalf("ParseOptions: %v", err)
	}
	if got, want := opts.Namespace, "from-flag"; got != want {
		t.Errorf("Namespace: got %q, want %q (flag must beat env)", got, want)
	}
}

func TestParseOptionsExplicitSecretNamespaceWins(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "squirrel-system")

	opts, err := manager.ParseOptions([]string{"--webhook-secret-namespace=elsewhere"})
	if err != nil {
		t.Fatalf("ParseOptions: %v", err)
	}
	if got, want := opts.WebhookSecretNamespace, "elsewhere"; got != want {
		t.Errorf("WebhookSecretNamespace: got %q, want %q", got, want)
	}
}

func TestParseOptionsAcceptsOverrides(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "")

	args := []string{
		"--metrics-bind-address=:9100",
		"--health-probe-bind-address=:9101",
		"--leader-elect=true",
		"--leader-election-id=other-lease",
		"--namespace=acme",
		"--cert-source=cert-manager",
		"--cert-dir=/var/run/certs",
		"--webhook-secret-name=other-tls",
		"--webhook-secret-namespace=cert-mgr-system",
		"--mwc-name=other-mwc",
		"--service-name=other-svc",
		"--ca-common-name=other-ca",
		"--webhook-port=8443",
		"--rotation-interval=2h",
		"--rotation-threshold=72h",
		"--ca-validity=43800h",
		"--serving-validity=720h",
	}
	opts, err := manager.ParseOptions(args)
	if err != nil {
		t.Fatalf("ParseOptions: %v", err)
	}

	if opts.MetricsAddr != ":9100" {
		t.Errorf("MetricsAddr override missed: %q", opts.MetricsAddr)
	}
	if !opts.LeaderElect {
		t.Errorf("LeaderElect override missed")
	}
	if opts.CertSource != "cert-manager" {
		t.Errorf("CertSource override missed: %q", opts.CertSource)
	}
	if opts.WebhookPort != 8443 {
		t.Errorf("WebhookPort override missed: %d", opts.WebhookPort)
	}
	if opts.RotationInterval != 2*time.Hour {
		t.Errorf("RotationInterval override missed: %s", opts.RotationInterval)
	}
}

func TestParseOptionsReportsBadFlag(t *testing.T) {
	t.Parallel()

	_, err := manager.ParseOptions([]string{"--this-flag-does-not-exist=x"})
	if err == nil {
		t.Errorf("expected error, got nil")
	}
}

func TestValidateRequiresNamespace(t *testing.T) {
	t.Parallel()

	opts := validOptions()
	opts.Namespace = ""
	err := opts.Validate()
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "namespace") {
		t.Errorf("error: got %q, want substring \"namespace\"", err)
	}
}

func TestValidateRejectsUnknownCertSource(t *testing.T) {
	t.Parallel()

	opts := validOptions()
	opts.CertSource = "bogus"
	if err := opts.Validate(); err == nil {
		t.Errorf("expected error, got nil")
	}
}

func TestValidateRejectsBadValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		mut  func(*manager.Options)
	}{
		{name: "empty CertDir", mut: func(o *manager.Options) { o.CertDir = "" }},
		{name: "empty WebhookSecretName", mut: func(o *manager.Options) { o.WebhookSecretName = "" }},
		{name: "empty WebhookSecretNamespace", mut: func(o *manager.Options) { o.WebhookSecretNamespace = "" }},
		{name: "empty MWCName", mut: func(o *manager.Options) { o.MWCName = "" }},
		{name: "empty ServiceName", mut: func(o *manager.Options) { o.ServiceName = "" }},
		{name: "empty CommonName", mut: func(o *manager.Options) { o.CommonName = "" }},
		{name: "zero WebhookPort", mut: func(o *manager.Options) { o.WebhookPort = 0 }},
		{name: "too-high WebhookPort", mut: func(o *manager.Options) { o.WebhookPort = 65536 }},
		{name: "zero RotationInterval", mut: func(o *manager.Options) { o.RotationInterval = 0 }},
		{name: "zero RotationThreshold", mut: func(o *manager.Options) { o.RotationThreshold = 0 }},
		{name: "zero CAValidity", mut: func(o *manager.Options) { o.CAValidity = 0 }},
		{name: "zero ServingValidity", mut: func(o *manager.Options) { o.ServingValidity = 0 }},
		{name: "serving longer than CA", mut: func(o *manager.Options) {
			o.ServingValidity = 10 * time.Hour
			o.CAValidity = 1 * time.Hour
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			opts := validOptions()
			tt.mut(opts)
			if err := opts.Validate(); err == nil {
				t.Errorf("expected error, got nil")
			}
		})
	}
}

// TestValidateAcceptsEmptySelfSignedFieldsInCertManagerMode pins
// that the self-signed-only fields are not required in cert-manager
// mode. An operator who picked cert-manager would otherwise be
// penalised for not configuring fields the mode never reads.
func TestValidateAcceptsEmptySelfSignedFieldsInCertManagerMode(t *testing.T) {
	t.Parallel()

	opts := validOptions()
	opts.CertSource = manager.CertSourceCertManager
	opts.WebhookSecretName = ""
	opts.MWCName = ""
	opts.ServiceName = ""
	opts.CommonName = ""
	if err := opts.Validate(); err != nil {
		t.Errorf("Validate: unexpected error %v", err)
	}
}

func TestValidateAcceptsValidOptions(t *testing.T) {
	t.Parallel()

	if err := validOptions().Validate(); err != nil {
		t.Errorf("validOptions: unexpected error %v", err)
	}
}

func TestWebhookDNSNamesDerivation(t *testing.T) {
	t.Parallel()

	opts := validOptions()
	opts.ServiceName = "squirrel-webhook"
	opts.Namespace = "squirrel-system"
	got := opts.WebhookDNSNames()
	want := []string{
		"squirrel-webhook.squirrel-system.svc",
		"squirrel-webhook.squirrel-system.svc.cluster.local",
	}
	if len(got) != len(want) {
		t.Fatalf("DNSNames: got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("DNSNames[%d]: got %q, want %q", i, got[i], want[i])
		}
	}
}

// TestWebhookDNSNamesUseServiceNamespaceNotSecretNamespace pins the
// fix for a real misconfiguration: when --webhook-secret-namespace
// differs from --namespace (e.g. cert-manager secrets live in the
// cert-manager namespace), SANs derived from the Secret namespace
// would fail TLS hostname verification because the apiserver dials
// the Service in --namespace, not the Secret's namespace.
func TestWebhookDNSNamesUseServiceNamespaceNotSecretNamespace(t *testing.T) {
	t.Parallel()

	opts := validOptions()
	opts.ServiceName = "squirrel-webhook"
	opts.Namespace = "squirrel-system"
	opts.WebhookSecretNamespace = "cert-manager"
	got := opts.WebhookDNSNames()
	for _, dns := range got {
		if !strings.Contains(dns, "squirrel-system") {
			t.Errorf("DNS name %q does not contain Service namespace squirrel-system", dns)
		}
		if strings.Contains(dns, "cert-manager") {
			t.Errorf("DNS name %q must not be derived from Secret namespace cert-manager", dns)
		}
	}
}

// validOptions returns an Options that passes Validate; tests mutate
// a single field to assert their failure path.
func validOptions() *manager.Options {
	return &manager.Options{
		MetricsAddr:            manager.DefaultMetricsAddr,
		ProbeAddr:              manager.DefaultProbeAddr,
		LeaderElect:            false,
		LeaderElectionID:       manager.DefaultLeaderElectionID,
		Namespace:              "squirrel-system",
		CertSource:             manager.DefaultCertSource,
		CertDir:                manager.DefaultCertDir,
		WebhookSecretName:      manager.DefaultWebhookSecretName,
		WebhookSecretNamespace: "squirrel-system",
		MWCName:                manager.DefaultMWCName,
		ServiceName:            manager.DefaultServiceName,
		CommonName:             manager.DefaultCommonName,
		WebhookPort:            manager.DefaultWebhookPort,
		RotationInterval:       manager.DefaultRotationInterval,
		RotationThreshold:      manager.DefaultRotationThreshold,
		CAValidity:             manager.DefaultCAValidity,
		ServingValidity:        manager.DefaultServingValidity,
	}
}
