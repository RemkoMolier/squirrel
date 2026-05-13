package certs_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	runtime "k8s.io/apimachinery/pkg/runtime"
	client "sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/RemkoMolier/squirrel/internal/certs"
)

// newSourceTestClient builds a fake client that knows the corev1
// (for Secret) and admissionregistrationv1 (for MWC) schemes; both
// are required by SelfSignedSource.Ensure.
func newSourceTestClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("corev1.AddToScheme: %v", err)
	}
	if err := admissionregistrationv1.AddToScheme(scheme); err != nil {
		t.Fatalf("admissionregistrationv1.AddToScheme: %v", err)
	}
	return fakeclient.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		Build()
}

// bootstrapSource runs Authoritative then Localize against src,
// mirroring what bootstrapCerts does in production (and what the
// two cert runnables jointly accomplish at steady state). Returns
// the first non-nil error so test setups read the same way they
// did under the pre-split single-method Ensure shape.
func bootstrapSource(ctx context.Context, src *certs.SelfSignedSource) error {
	if err := src.Authoritative(ctx); err != nil {
		return err
	}
	return src.Localize(ctx)
}

func newSelfSignedTestSource(t *testing.T, cli client.Client, now time.Time) *certs.SelfSignedSource {
	t.Helper()
	src, err := certs.NewSelfSignedSource(certs.SelfSignedSourceOpts{
		Client:     cli,
		SecretKey:  testSecretKey,
		MWCName:    testMWCName,
		CommonName: "squirrel-ca",
		DNSNames:   []string{"squirrel-webhook.squirrel-system.svc"},
		CertDir:    t.TempDir(),
		Now:        func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewSelfSignedSource: %v", err)
	}
	return src
}

func TestSelfSignedSourceEnsureCreatesEverythingOnCleanStart(t *testing.T) {
	t.Parallel()

	cli := newSourceTestClient(t, mwcWithBundles(nil))
	src := newSelfSignedTestSource(t, cli, testTime)

	if err := bootstrapSource(context.Background(), src); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	// Files on disk.
	for _, name := range []string{certs.FileServingCert, certs.FileServingKey, certs.FileCACert} {
		path := filepath.Join(src.CertDir(), name)
		data, err := os.ReadFile(path) //nolint:gosec // path is t.TempDir() joined with a constant filename
		if err != nil {
			t.Errorf("ReadFile %q: %v", path, err)
			continue
		}
		if len(data) == 0 {
			t.Errorf("file %q is empty", path)
		}
	}

	// Secret in the apiserver.
	var sec corev1.Secret
	if err := cli.Get(context.Background(), testSecretKey, &sec); err != nil {
		t.Fatalf("Get Secret: %v", err)
	}
	if len(sec.Data[certs.SecretCACertKey]) == 0 {
		t.Errorf("Secret missing %s", certs.SecretCACertKey)
	}

	// MWC caBundle published.
	var mwc admissionregistrationv1.MutatingWebhookConfiguration
	if err := cli.Get(context.Background(), client.ObjectKey{Name: testMWCName}, &mwc); err != nil {
		t.Fatalf("Get MWC: %v", err)
	}
	if got, want := string(mwc.Webhooks[0].ClientConfig.CABundle), string(sec.Data[certs.SecretCACertKey]); got != want {
		t.Errorf("MWC caBundle does not match Secret ca.crt:\n got: %q\nwant: %q", got, want)
	}
}

// TestSelfSignedSourceEnsureIsIdempotent pins the no-op contract: a
// second Ensure call against the same clock leaves the Secret bytes,
// the MWC resourceVersion, and the on-disk files all unchanged.
func TestSelfSignedSourceEnsureIsIdempotent(t *testing.T) {
	t.Parallel()

	cli := newSourceTestClient(t, mwcWithBundles(nil))
	src := newSelfSignedTestSource(t, cli, testTime)

	if err := bootstrapSource(context.Background(), src); err != nil {
		t.Fatalf("Ensure (first): %v", err)
	}

	var firstSecret corev1.Secret
	if err := cli.Get(context.Background(), testSecretKey, &firstSecret); err != nil {
		t.Fatalf("Get Secret: %v", err)
	}
	var firstMWC admissionregistrationv1.MutatingWebhookConfiguration
	if err := cli.Get(context.Background(), client.ObjectKey{Name: testMWCName}, &firstMWC); err != nil {
		t.Fatalf("Get MWC: %v", err)
	}
	firstCertFile, readErr := os.ReadFile(filepath.Join(src.CertDir(), certs.FileServingCert))
	if readErr != nil {
		t.Fatalf("ReadFile tls.crt: %v", readErr)
	}

	if ensureErr := bootstrapSource(context.Background(), src); ensureErr != nil {
		t.Fatalf("Ensure (second): %v", ensureErr)
	}

	var secondSecret corev1.Secret
	if getErr := cli.Get(context.Background(), testSecretKey, &secondSecret); getErr != nil {
		t.Fatalf("Get Secret: %v", getErr)
	}
	var secondMWC admissionregistrationv1.MutatingWebhookConfiguration
	if getErr := cli.Get(context.Background(), client.ObjectKey{Name: testMWCName}, &secondMWC); getErr != nil {
		t.Fatalf("Get MWC: %v", getErr)
	}
	secondCertFile, readErr2 := os.ReadFile(filepath.Join(src.CertDir(), certs.FileServingCert))
	if readErr2 != nil {
		t.Fatalf("ReadFile tls.crt: %v", readErr2)
	}

	if firstSecret.ResourceVersion != secondSecret.ResourceVersion {
		t.Errorf("Secret resourceVersion changed on a no-op Ensure (got %q vs %q)",
			firstSecret.ResourceVersion, secondSecret.ResourceVersion)
	}
	if firstMWC.ResourceVersion != secondMWC.ResourceVersion {
		t.Errorf("MWC resourceVersion changed on a no-op Ensure (got %q vs %q)",
			firstMWC.ResourceVersion, secondMWC.ResourceVersion)
	}
	if string(firstCertFile) != string(secondCertFile) {
		t.Errorf("on-disk tls.crt changed across no-op Ensure calls")
	}
}

// TestSelfSignedSourceEnsureRotatesWhenServingExpires advances the
// clock past the serving certificate's rotation window and asserts
// that the on-disk material changes (rotation happened) while the
// MWC caBundle stays the same (CA was reused).
func TestSelfSignedSourceEnsureRotatesWhenServingExpires(t *testing.T) {
	t.Parallel()

	cli := newSourceTestClient(t, mwcWithBundles(nil))

	// Set up a short serving validity so a 45-day clock advance puts
	// us inside the 30-day rotation window.
	src, err := certs.NewSelfSignedSource(certs.SelfSignedSourceOpts{
		Client:            cli,
		SecretKey:         testSecretKey,
		MWCName:           testMWCName,
		CommonName:        "squirrel-ca",
		DNSNames:          []string{"squirrel-webhook.squirrel-system.svc"},
		CertDir:           t.TempDir(),
		ServingValidity:   60 * 24 * time.Hour,
		CAValidity:        5 * 365 * 24 * time.Hour,
		RotationThreshold: 30 * 24 * time.Hour,
		Now:               func() time.Time { return testTime },
	})
	if err != nil {
		t.Fatalf("NewSelfSignedSource: %v", err)
	}

	if ensureErr := bootstrapSource(context.Background(), src); ensureErr != nil {
		t.Fatalf("Ensure (first): %v", ensureErr)
	}
	firstServingCert, readErr := os.ReadFile(filepath.Join(src.CertDir(), certs.FileServingCert))
	if readErr != nil {
		t.Fatalf("ReadFile: %v", readErr)
	}
	firstCACert, readErr := os.ReadFile(filepath.Join(src.CertDir(), certs.FileCACert))
	if readErr != nil {
		t.Fatalf("ReadFile: %v", readErr)
	}

	// Re-construct the source against an advanced clock - the original
	// source captured testTime as its Now function. 45 days in,
	// serving cert is within 30 days of its 60-day expiry, but CA is
	// nowhere near its 5-year expiry.
	advanced, err := certs.NewSelfSignedSource(certs.SelfSignedSourceOpts{
		Client:            cli,
		SecretKey:         testSecretKey,
		MWCName:           testMWCName,
		CommonName:        "squirrel-ca",
		DNSNames:          []string{"squirrel-webhook.squirrel-system.svc"},
		CertDir:           src.CertDir(),
		ServingValidity:   60 * 24 * time.Hour,
		CAValidity:        5 * 365 * 24 * time.Hour,
		RotationThreshold: 30 * 24 * time.Hour,
		Now:               func() time.Time { return testTime.Add(45 * 24 * time.Hour) },
	})
	if err != nil {
		t.Fatalf("NewSelfSignedSource (advanced): %v", err)
	}
	if ensureErr := bootstrapSource(context.Background(), advanced); ensureErr != nil {
		t.Fatalf("Ensure (advanced): %v", ensureErr)
	}

	secondServingCert, err := os.ReadFile(filepath.Join(src.CertDir(), certs.FileServingCert))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	secondCACert, err := os.ReadFile(filepath.Join(src.CertDir(), certs.FileCACert))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	if string(firstServingCert) == string(secondServingCert) {
		t.Errorf("serving cert was not rotated despite being inside the threshold window")
	}
	if string(firstCACert) != string(secondCACert) {
		t.Errorf("CA cert rotated when only the serving cert needed rotation")
	}
}

// TestSelfSignedSourceEnsureSurfaceMWCMissing pins that a missing MWC
// surfaces as an Ensure error rather than as a silent caBundle gap;
// the manager wiring uses this signal to log loudly and back off.
func TestSelfSignedSourceEnsureSurfaceMWCMissing(t *testing.T) {
	t.Parallel()

	cli := newSourceTestClient(t) // no MWC pre-installed
	src := newSelfSignedTestSource(t, cli, testTime)

	if err := bootstrapSource(context.Background(), src); err == nil {
		t.Fatalf("expected error from missing MWC, got nil")
	}
}

func TestSelfSignedSourceWritesPermissions(t *testing.T) {
	t.Parallel()

	cli := newSourceTestClient(t, mwcWithBundles(nil))
	// Use a nested directory under t.TempDir so writeBundleToDir
	// actually creates it (t.TempDir creates with the runner's
	// umask, which is typically 0o755; we want to exercise the
	// MkdirAll(0o700) path in the source itself).
	certDir := filepath.Join(t.TempDir(), "serving-certs")
	src, srcErr := certs.NewSelfSignedSource(certs.SelfSignedSourceOpts{
		Client:     cli,
		SecretKey:  testSecretKey,
		MWCName:    testMWCName,
		CommonName: "squirrel-ca",
		DNSNames:   []string{"webhook.svc"},
		CertDir:    certDir,
		Now:        func() time.Time { return testTime },
	})
	if srcErr != nil {
		t.Fatalf("NewSelfSignedSource: %v", srcErr)
	}

	if ensureErr := bootstrapSource(context.Background(), src); ensureErr != nil {
		t.Fatalf("Ensure: %v", ensureErr)
	}

	// Files are 0600 so the private key is not world-readable.
	for _, name := range []string{certs.FileServingCert, certs.FileServingKey, certs.FileCACert} {
		path := filepath.Join(src.CertDir(), name)
		info, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatalf("Stat %q: %v", path, statErr)
		}
		if got, want := info.Mode().Perm(), os.FileMode(0o600); got != want {
			t.Errorf("file %q mode: got %o, want %o", path, got, want)
		}
	}
	// Directory mode pins the surrounding-container lockdown so a
	// later loosening of writeBundleToDir's MkdirAll permissions
	// would fail this check before reaching production.
	dirInfo, dirErr := os.Stat(src.CertDir())
	if dirErr != nil {
		t.Fatalf("Stat %q: %v", src.CertDir(), dirErr)
	}
	if got, want := dirInfo.Mode().Perm(), os.FileMode(0o700); got != want {
		t.Errorf("dir %q mode: got %o, want %o", src.CertDir(), got, want)
	}
}

func TestSelfSignedSourceRejectsInvalidOpts(t *testing.T) {
	t.Parallel()

	cli := newSourceTestClient(t)
	tests := []struct {
		name string
		mut  func(*certs.SelfSignedSourceOpts)
	}{
		{name: "nil client", mut: func(o *certs.SelfSignedSourceOpts) { o.Client = nil }},
		{name: "empty SecretKey", mut: func(o *certs.SelfSignedSourceOpts) { o.SecretKey.Name = "" }},
		{name: "empty MWCName", mut: func(o *certs.SelfSignedSourceOpts) { o.MWCName = "" }},
		{name: "empty CommonName", mut: func(o *certs.SelfSignedSourceOpts) { o.CommonName = "" }},
		{name: "empty DNSNames", mut: func(o *certs.SelfSignedSourceOpts) { o.DNSNames = nil }},
		{name: "empty CertDir", mut: func(o *certs.SelfSignedSourceOpts) { o.CertDir = "" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			opts := certs.SelfSignedSourceOpts{
				Client:     cli,
				SecretKey:  testSecretKey,
				MWCName:    testMWCName,
				CommonName: "squirrel-ca",
				DNSNames:   []string{"webhook.svc"},
				CertDir:    t.TempDir(),
			}
			tt.mut(&opts)
			if _, err := certs.NewSelfSignedSource(opts); err == nil {
				t.Errorf("expected error, got nil")
			}
		})
	}
}

// TestSelfSignedSourceAtomicWriteSurvivesConcurrentReaders does not
// model an actual concurrent reader (the webhook server) but does
// verify that the temp-file-and-rename approach leaves no leftover
// .tmp file in CertDir after Ensure returns. A regression that
// forgot to clean up on success would litter the directory and
// eventually fill the disk under repeated rotation.
func TestSelfSignedSourceAtomicWriteSurvivesConcurrentReaders(t *testing.T) {
	t.Parallel()

	cli := newSourceTestClient(t, mwcWithBundles(nil))
	src := newSelfSignedTestSource(t, cli, testTime)

	if err := bootstrapSource(context.Background(), src); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	entries, err := os.ReadDir(src.CertDir())
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if name != certs.FileServingCert && name != certs.FileServingKey && name != certs.FileCACert {
			t.Errorf("unexpected file in CertDir: %q (atomic-write left a temp file behind)", name)
		}
	}
}
