package certs_test

import (
	"crypto/x509"
	"testing"
	"time"

	"github.com/RemkoMolier/squirrel/internal/certs"
)

// testTime is a fixed instant used as the notBefore for every test
// fixture so the resulting NotAfter values are deterministic.
var testTime = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func TestNewSelfSignedCAProducesValidCertificate(t *testing.T) {
	t.Parallel()

	ca, err := certs.NewSelfSignedCA("squirrel-ca", testTime, testTime.Add(5*365*24*time.Hour))
	if err != nil {
		t.Fatalf("NewSelfSignedCA: %v", err)
	}

	cert := ca.Cert()
	if !cert.IsCA {
		t.Errorf("IsCA: got false, want true")
	}
	if cert.KeyUsage&x509.KeyUsageCertSign == 0 {
		t.Errorf("KeyUsage missing CertSign")
	}
	if cert.KeyUsage&x509.KeyUsageCRLSign == 0 {
		t.Errorf("KeyUsage missing CRLSign")
	}
	if !cert.BasicConstraintsValid {
		t.Errorf("BasicConstraintsValid: got false, want true")
	}
	if got, want := cert.Subject.CommonName, "squirrel-ca"; got != want {
		t.Errorf("Subject.CommonName: got %q, want %q", got, want)
	}
	if !cert.NotBefore.Equal(testTime) {
		t.Errorf("NotBefore: got %s, want %s", cert.NotBefore, testTime)
	}
	if want := testTime.Add(5 * 365 * 24 * time.Hour); !cert.NotAfter.Equal(want) {
		t.Errorf("NotAfter: got %s, want %s", cert.NotAfter, want)
	}
}

func TestNewSelfSignedCAIsSelfSigned(t *testing.T) {
	t.Parallel()

	ca, err := certs.NewSelfSignedCA("squirrel-ca", testTime, testTime.Add(5*365*24*time.Hour))
	if err != nil {
		t.Fatalf("NewSelfSignedCA: %v", err)
	}

	// A CA must verify against itself. The Pool/VerifyOptions dance is
	// the same one the apiserver runs against the bundle it loaded
	// from the MWC.
	pool := x509.NewCertPool()
	pool.AddCert(ca.Cert())
	if _, err := ca.Cert().Verify(x509.VerifyOptions{
		Roots:       pool,
		CurrentTime: testTime.Add(24 * time.Hour),
	}); err != nil {
		t.Errorf("CA failed to verify against itself: %v", err)
	}
}

func TestNewSelfSignedCAEmitsUniqueSerials(t *testing.T) {
	t.Parallel()

	// 128-bit random serials should never collide in this test loop,
	// even on the smallest CI hardware; a regression to a constant or
	// to a 32-bit serial would show up here.
	seen := make(map[string]struct{}, 16)
	for i := range 16 {
		ca, err := certs.NewSelfSignedCA("squirrel-ca", testTime, testTime.Add(time.Hour))
		if err != nil {
			t.Fatalf("NewSelfSignedCA: %v", err)
		}
		serial := ca.Cert().SerialNumber.String()
		if _, ok := seen[serial]; ok {
			t.Errorf("serial %s collided on iteration %d", serial, i)
		}
		seen[serial] = struct{}{}
	}
}

func TestNewSelfSignedCARejectsBadArguments(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		cn        string
		notBefore time.Time
		notAfter  time.Time
	}{
		{name: "empty common name", cn: "", notBefore: testTime, notAfter: testTime.Add(time.Hour)},
		{name: "notBefore equals notAfter", cn: "x", notBefore: testTime, notAfter: testTime},
		{name: "notBefore after notAfter", cn: "x", notBefore: testTime.Add(time.Hour), notAfter: testTime},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := certs.NewSelfSignedCA(tt.cn, tt.notBefore, tt.notAfter); err == nil {
				t.Errorf("expected error, got nil")
			}
		})
	}
}

func TestParseCARoundTripsPEM(t *testing.T) {
	t.Parallel()

	original, err := certs.NewSelfSignedCA("squirrel-ca", testTime, testTime.Add(5*365*24*time.Hour))
	if err != nil {
		t.Fatalf("NewSelfSignedCA: %v", err)
	}
	parsed, err := certs.ParseCA(original.CertPEM(), original.KeyPEM())
	if err != nil {
		t.Fatalf("ParseCA: %v", err)
	}

	if !parsed.Cert().Equal(original.Cert()) {
		t.Errorf("parsed cert differs from original")
	}
	if string(parsed.CertPEM()) != string(original.CertPEM()) {
		t.Errorf("parsed certPEM differs from original")
	}
	if string(parsed.KeyPEM()) != string(original.KeyPEM()) {
		t.Errorf("parsed keyPEM differs from original")
	}
}

func TestParseCARejectsNonCACertificate(t *testing.T) {
	t.Parallel()

	// Mint a CA + serving cert. ParseCA should refuse to accept the
	// serving cert's PEM as a CA - protects against a Secret whose
	// `ca.crt` field was wired up to the serving cert by an
	// out-of-date manifest.
	ca, err := certs.NewSelfSignedCA("squirrel-ca", testTime, testTime.Add(time.Hour))
	if err != nil {
		t.Fatalf("NewSelfSignedCA: %v", err)
	}
	serving, err := certs.IssueServingCert(ca, []string{"webhook.svc"}, testTime, testTime.Add(time.Hour))
	if err != nil {
		t.Fatalf("IssueServingCert: %v", err)
	}
	if _, err := certs.ParseCA(serving.CertPEM(), serving.KeyPEM()); err == nil {
		t.Errorf("ParseCA accepted a non-CA certificate; want error")
	}
}

// TestParseCARejectsMismatchedKeypair pins the pair-check contract:
// a Secret whose ca.crt and ca.key come from different keypairs - the
// credible failure mode when an operator hand-rolls a Secret - must
// fail at parse time with a clear error, not much later as an opaque
// TLS handshake failure on the apiserver side.
func TestParseCARejectsMismatchedKeypair(t *testing.T) {
	t.Parallel()

	caA, err := certs.NewSelfSignedCA("ca-a", testTime, testTime.Add(time.Hour))
	if err != nil {
		t.Fatalf("NewSelfSignedCA(ca-a): %v", err)
	}
	caB, err := certs.NewSelfSignedCA("ca-b", testTime, testTime.Add(time.Hour))
	if err != nil {
		t.Fatalf("NewSelfSignedCA(ca-b): %v", err)
	}
	// Feed A's cert with B's key. The CA template is structurally
	// valid (IsCA, CertSign), so without the pair-check this would
	// silently round-trip.
	_, err = certs.ParseCA(caA.CertPEM(), caB.KeyPEM())
	if err == nil {
		t.Fatalf("ParseCA accepted a cert/key pair from different keypairs; want error")
	}
}

// TestNewSelfSignedCASetsMaxPathLenZero pins the defence-in-depth
// pathlen:0 constraint: the webhook CA may only sign end-entity
// certificates, never intermediate CAs.
func TestNewSelfSignedCASetsMaxPathLenZero(t *testing.T) {
	t.Parallel()

	ca, err := certs.NewSelfSignedCA("squirrel-ca", testTime, testTime.Add(time.Hour))
	if err != nil {
		t.Fatalf("NewSelfSignedCA: %v", err)
	}
	if !ca.Cert().MaxPathLenZero {
		t.Errorf("MaxPathLenZero: got false, want true (CA must not be permitted to sign intermediate CAs)")
	}
	if got, want := ca.Cert().MaxPathLen, 0; got != want {
		t.Errorf("MaxPathLen: got %d, want %d", got, want)
	}
}
