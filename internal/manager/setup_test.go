package manager_test

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	runtime "k8s.io/apimachinery/pkg/runtime"
	client "sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/RemkoMolier/squirrel/internal/manager"
)

// newSetupTestClient returns a fake controller-runtime client with
// the corev1 scheme installed. The cert sources only touch Secrets
// (corev1) and MutatingWebhookConfigurations through their owners;
// NewCertSource itself never reaches the apiserver so the bare
// corev1 scheme is sufficient for its construction path.
func newSetupTestClient(t *testing.T) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	return fakeclient.NewClientBuilder().WithScheme(scheme).Build()
}

func TestNewCertSourceSelfSigned(t *testing.T) {
	t.Parallel()

	cli := newSetupTestClient(t)
	opts := validOptions()
	opts.CertDir = t.TempDir()

	src, err := manager.NewCertSource(cli, opts)
	if err != nil {
		t.Fatalf("NewCertSource: %v", err)
	}
	if src == nil {
		t.Fatalf("NewCertSource: got nil source")
	}
	if got, want := src.CertDir(), opts.CertDir; got != want {
		t.Errorf("CertDir: got %q, want %q", got, want)
	}
}

func TestNewCertSourceCertManager(t *testing.T) {
	t.Parallel()

	opts := validOptions()
	opts.CertSource = manager.CertSourceCertManager
	opts.CertDir = t.TempDir()

	// Client is intentionally nil: cert-manager mode does not need
	// one (cert-manager owns the Secret and MWC). NewCertSource
	// must not require a client in this branch.
	src, err := manager.NewCertSource(nil, opts)
	if err != nil {
		t.Fatalf("NewCertSource: %v", err)
	}
	if src == nil {
		t.Fatalf("NewCertSource: got nil source")
	}
	if got, want := src.CertDir(), opts.CertDir; got != want {
		t.Errorf("CertDir: got %q, want %q", got, want)
	}
}

func TestNewCertSourceUnknownMode(t *testing.T) {
	t.Parallel()

	opts := validOptions()
	opts.CertSource = "bogus"
	if _, err := manager.NewCertSource(nil, opts); err == nil {
		t.Errorf("expected error, got nil")
	}
}

func TestNewCertSourceRejectsNilOpts(t *testing.T) {
	t.Parallel()

	if _, err := manager.NewCertSource(nil, nil); err == nil {
		t.Errorf("expected error, got nil")
	}
}

// TestNewCertSourceSurfacesSelfSignedValidationErrors confirms that
// NewCertSource propagates the underlying certs.NewSelfSignedSource
// validation - if Options.Validate is the operator's first line of
// defence, NewCertSource is the second.
func TestNewCertSourceSurfacesSelfSignedValidationErrors(t *testing.T) {
	t.Parallel()

	cli := newSetupTestClient(t)
	opts := validOptions()
	opts.WebhookSecretName = "" // would fail certs.SelfSignedSource validation

	if _, err := manager.NewCertSource(cli, opts); err == nil {
		t.Errorf("expected error, got nil")
	}
}

// TestNewCertSourcePassesThroughTimingOptions threads the validity
// and rotation-threshold values through into the SelfSignedSource
// indirectly via Ensure-time behaviour. We use a very short
// CAValidity and a far-in-the-past time would be ideal, but since
// the source's Ensure path needs the apiserver, we only check the
// construction path here - the actual rotation pinning belongs to
// the certs package tests.
func TestNewCertSourcePassesThroughTimingOptions(t *testing.T) {
	t.Parallel()

	cli := newSetupTestClient(t)
	opts := validOptions()
	opts.CertDir = t.TempDir()
	opts.RotationThreshold = 7 * 24 * time.Hour
	opts.CAValidity = 365 * 24 * time.Hour
	opts.ServingValidity = 30 * 24 * time.Hour

	src, err := manager.NewCertSource(cli, opts)
	if err != nil {
		t.Fatalf("NewCertSource: %v", err)
	}
	if src == nil {
		t.Fatalf("NewCertSource: nil source")
	}
}
