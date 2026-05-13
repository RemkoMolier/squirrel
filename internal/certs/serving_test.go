package certs_test

import (
	"crypto/x509"
	"testing"
	"time"

	"github.com/RemkoMolier/squirrel/internal/certs"
)

func TestIssueServingCertSignsAgainstCA(t *testing.T) {
	t.Parallel()

	ca, err := certs.NewSelfSignedCA("squirrel-ca", testTime, testTime.Add(5*365*24*time.Hour))
	if err != nil {
		t.Fatalf("NewSelfSignedCA: %v", err)
	}
	dnsNames := []string{
		"squirrel-webhook.squirrel-system.svc",
		"squirrel-webhook.squirrel-system.svc.cluster.local",
	}
	serving, err := certs.IssueServingCert(ca, dnsNames, testTime, testTime.Add(365*24*time.Hour))
	if err != nil {
		t.Fatalf("IssueServingCert: %v", err)
	}

	cert := serving.Cert()
	if cert.IsCA {
		t.Errorf("IsCA: got true, want false (serving cert must not be a CA)")
	}
	if cert.KeyUsage&x509.KeyUsageDigitalSignature == 0 {
		t.Errorf("KeyUsage missing DigitalSignature")
	}
	if cert.KeyUsage&x509.KeyUsageKeyEncipherment == 0 {
		t.Errorf("KeyUsage missing KeyEncipherment")
	}
	if got, want := len(cert.ExtKeyUsage), 1; got != want || cert.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth {
		t.Errorf("ExtKeyUsage: got %v, want [ServerAuth]", cert.ExtKeyUsage)
	}
	if got, want := cert.DNSNames, dnsNames; len(got) != len(want) {
		t.Fatalf("DNSNames: got %v, want %v", got, want)
	} else {
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("DNSNames[%d]: got %q, want %q", i, got[i], want[i])
			}
		}
	}
}

func TestIssueServingCertVerifiesAgainstCA(t *testing.T) {
	t.Parallel()

	// Round-trip the apiserver-style verification: build a pool
	// containing only the freshly-minted CA, then verify the serving
	// cert against one of its DNS names. A regression where the
	// serving cert is signed by a different key or carries the wrong
	// SAN would fail here.
	ca, err := certs.NewSelfSignedCA("squirrel-ca", testTime, testTime.Add(time.Hour))
	if err != nil {
		t.Fatalf("NewSelfSignedCA: %v", err)
	}
	serving, err := certs.IssueServingCert(ca, []string{"webhook.svc"}, testTime, testTime.Add(time.Hour))
	if err != nil {
		t.Fatalf("IssueServingCert: %v", err)
	}

	pool := x509.NewCertPool()
	pool.AddCert(ca.Cert())
	if _, err := serving.Cert().Verify(x509.VerifyOptions{
		Roots:       pool,
		DNSName:     "webhook.svc",
		CurrentTime: testTime.Add(time.Minute),
	}); err != nil {
		t.Errorf("serving cert failed to verify against its CA: %v", err)
	}
}

func TestIssueServingCertRejectsBadArguments(t *testing.T) {
	t.Parallel()

	ca, err := certs.NewSelfSignedCA("squirrel-ca", testTime, testTime.Add(time.Hour))
	if err != nil {
		t.Fatalf("NewSelfSignedCA: %v", err)
	}
	tests := []struct {
		name      string
		ca        *certs.CA
		dnsNames  []string
		notBefore time.Time
		notAfter  time.Time
	}{
		{name: "nil ca", ca: nil, dnsNames: []string{"x.svc"}, notBefore: testTime, notAfter: testTime.Add(time.Hour)},
		{name: "no DNS names", ca: ca, dnsNames: nil, notBefore: testTime, notAfter: testTime.Add(time.Hour)},
		{name: "notBefore equals notAfter", ca: ca, dnsNames: []string{"x.svc"}, notBefore: testTime, notAfter: testTime},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := certs.IssueServingCert(tt.ca, tt.dnsNames, tt.notBefore, tt.notAfter); err == nil {
				t.Errorf("expected error, got nil")
			}
		})
	}
}

func TestParseServingCertRoundTripsPEM(t *testing.T) {
	t.Parallel()

	ca, err := certs.NewSelfSignedCA("squirrel-ca", testTime, testTime.Add(time.Hour))
	if err != nil {
		t.Fatalf("NewSelfSignedCA: %v", err)
	}
	original, err := certs.IssueServingCert(ca, []string{"webhook.svc"}, testTime, testTime.Add(time.Hour))
	if err != nil {
		t.Fatalf("IssueServingCert: %v", err)
	}
	parsed, err := certs.ParseServingCert(original.CertPEM(), original.KeyPEM())
	if err != nil {
		t.Fatalf("ParseServingCert: %v", err)
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

func TestParseServingCertRejectsCACertificate(t *testing.T) {
	t.Parallel()

	// Mirror image of TestParseCARejectsNonCACertificate: feeding the
	// CA's PEM in as a serving cert must be rejected so a
	// misconfigured Secret cannot get the apiserver to verify a CA
	// cert as the webhook's serving cert.
	ca, err := certs.NewSelfSignedCA("squirrel-ca", testTime, testTime.Add(time.Hour))
	if err != nil {
		t.Fatalf("NewSelfSignedCA: %v", err)
	}
	if _, err := certs.ParseServingCert(ca.CertPEM(), ca.KeyPEM()); err == nil {
		t.Errorf("ParseServingCert accepted a CA certificate; want error")
	}
}

// TestParseServingCertRejectsMismatchedKeypair pins the pair-check
// contract: a Secret whose tls.crt and tls.key come from different
// keypairs must fail at parse time with a clear error, not much
// later as an opaque TLS handshake failure on the apiserver side.
func TestParseServingCertRejectsMismatchedKeypair(t *testing.T) {
	t.Parallel()

	ca, err := certs.NewSelfSignedCA("squirrel-ca", testTime, testTime.Add(time.Hour))
	if err != nil {
		t.Fatalf("NewSelfSignedCA: %v", err)
	}
	servingA, err := certs.IssueServingCert(ca, []string{"a.svc"}, testTime, testTime.Add(time.Hour))
	if err != nil {
		t.Fatalf("IssueServingCert(a): %v", err)
	}
	servingB, err := certs.IssueServingCert(ca, []string{"b.svc"}, testTime, testTime.Add(time.Hour))
	if err != nil {
		t.Fatalf("IssueServingCert(b): %v", err)
	}
	// Feed A's cert with B's key. Both are valid serving certs issued
	// by the same CA; without the pair-check this would silently
	// round-trip and break only at handshake time.
	if _, err := certs.ParseServingCert(servingA.CertPEM(), servingB.KeyPEM()); err == nil {
		t.Errorf("ParseServingCert accepted a cert/key pair from different keypairs; want error")
	}
}

// TestIssueServingCertSetsBasicConstraintsValid pins the explicit
// IsCA=false BasicConstraints extension on serving certs. Modern Go
// verifiers do not require it, but emitting it explicitly makes the
// material unambiguous to non-Go consumers and rules out a CA being
// substituted for a serving cert by an out-of-band tool.
func TestIssueServingCertSetsBasicConstraintsValid(t *testing.T) {
	t.Parallel()

	ca, err := certs.NewSelfSignedCA("squirrel-ca", testTime, testTime.Add(time.Hour))
	if err != nil {
		t.Fatalf("NewSelfSignedCA: %v", err)
	}
	serving, err := certs.IssueServingCert(ca, []string{"webhook.svc"}, testTime, testTime.Add(time.Hour))
	if err != nil {
		t.Fatalf("IssueServingCert: %v", err)
	}
	if !serving.Cert().BasicConstraintsValid {
		t.Errorf("BasicConstraintsValid: got false, want true")
	}
	if serving.Cert().IsCA {
		t.Errorf("IsCA: got true, want false")
	}
}
