package manager

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"k8s.io/client-go/rest"

	"github.com/RemkoMolier/squirrel/internal/certs"
)

// TestBootstrapCertsCertManagerMode pins the invariant that
// bootstrapCerts performs an initial CertSource.Ensure in cert-manager
// mode against a non-cached client built directly from cfg, succeeding
// when the on-disk material is valid. A future refactor that moves the
// bootstrap below SetupManager (so the cache-backed Client gets used
// before the manager has started its cache) would break the
// SelfSignedSource's first deploy; this test pins the cert-manager
// path that does not need a live apiserver, which is enough to catch
// the ordering regression without standing up envtest.
func TestBootstrapCertsCertManagerMode(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeTestBundle(t, dir)

	opts := &Options{
		CertSource: CertSourceCertManager,
		CertDir:    dir,
	}
	scheme, err := buildScheme()
	if err != nil {
		t.Fatalf("buildScheme: %v", err)
	}
	// rest.Config is consumed only by client.New, which builds the
	// client lazily; in cert-manager mode the CertManagerSource never
	// makes an API call, so the placeholder host suffices.
	cfg := &rest.Config{Host: "https://example.invalid"}

	if err := bootstrapCerts(context.Background(), cfg, scheme, opts); err != nil {
		t.Fatalf("bootstrapCerts: %v", err)
	}
}

// writeTestBundle mints a CA + leaf and writes the three files the
// CertManagerSource expects. Mirrors the cert-manager Secret shape so
// the bootstrap path exercises the same parsing the production volume
// mount would.
func writeTestBundle(t *testing.T, dir string) {
	t.Helper()
	now := time.Now()
	ca, err := certs.NewSelfSignedCA("test-ca", now, now.Add(365*24*time.Hour))
	if err != nil {
		t.Fatalf("NewSelfSignedCA: %v", err)
	}
	leaf, err := certs.IssueServingCert(ca, []string{"webhook.svc"}, now, now.Add(60*24*time.Hour))
	if err != nil {
		t.Fatalf("IssueServingCert: %v", err)
	}
	files := []struct {
		name string
		data []byte
	}{
		{certs.FileServingCert, leaf.CertPEM()},
		{certs.FileServingKey, leaf.KeyPEM()},
		{certs.FileCACert, ca.CertPEM()},
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f.name), f.data, 0o600); err != nil {
			t.Fatalf("write %s: %v", f.name, err)
		}
	}
}
