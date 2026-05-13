package certs_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RemkoMolier/squirrel/internal/certs"
)

// writeBundleAsCertManagerWould mints a CA + serving cert and writes
// the PEM material into dir using the standard file names a
// cert-manager Secret projection would produce.
func writeBundleAsCertManagerWould(t *testing.T, dir string) {
	t.Helper()
	ca, err := certs.NewSelfSignedCA("test-ca", testTime, testTime.Add(365*24*time.Hour))
	if err != nil {
		t.Fatalf("NewSelfSignedCA: %v", err)
	}
	serving, err := certs.IssueServingCert(ca, []string{"webhook.svc"}, testTime, testTime.Add(60*24*time.Hour))
	if err != nil {
		t.Fatalf("IssueServingCert: %v", err)
	}
	for _, f := range []struct {
		name string
		data []byte
	}{
		{certs.FileServingCert, serving.CertPEM()},
		{certs.FileServingKey, serving.KeyPEM()},
		{certs.FileCACert, ca.CertPEM()},
	} {
		if err := os.WriteFile(filepath.Join(dir, f.name), f.data, 0o600); err != nil {
			t.Fatalf("write %s: %v", f.name, err)
		}
	}
}

func TestCertManagerSourceEnsureAcceptsValidMount(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeBundleAsCertManagerWould(t, dir)

	src, err := certs.NewCertManagerSource(dir, certs.WithCertManagerNow(func() time.Time { return testTime }))
	if err != nil {
		t.Fatalf("NewCertManagerSource: %v", err)
	}
	if err := src.Localize(context.Background()); err != nil {
		t.Errorf("Ensure: unexpected error %v", err)
	}
}

// TestCertManagerSourceEnsureTolerateMissingCA covers the
// external-Issuer case: a cert-manager Issuer that delegates to an
// outside CA does not always materialise ca.crt alongside the
// serving material. The source must accept the partial Secret since
// the apiserver gets the CA bundle through the MWC injection
// annotation instead.
func TestCertManagerSourceEnsureTolerateMissingCA(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeBundleAsCertManagerWould(t, dir)
	if err := os.Remove(filepath.Join(dir, certs.FileCACert)); err != nil {
		t.Fatalf("remove ca.crt: %v", err)
	}

	src, err := certs.NewCertManagerSource(dir, certs.WithCertManagerNow(func() time.Time { return testTime }))
	if err != nil {
		t.Fatalf("NewCertManagerSource: %v", err)
	}
	if err := src.Localize(context.Background()); err != nil {
		t.Errorf("Ensure: unexpected error %v", err)
	}
}

func TestCertManagerSourceEnsureRejectsMissingServingCert(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeBundleAsCertManagerWould(t, dir)
	if err := os.Remove(filepath.Join(dir, certs.FileServingCert)); err != nil {
		t.Fatalf("remove tls.crt: %v", err)
	}

	src, err := certs.NewCertManagerSource(dir, certs.WithCertManagerNow(func() time.Time { return testTime }))
	if err != nil {
		t.Fatalf("NewCertManagerSource: %v", err)
	}
	if err := src.Localize(context.Background()); err == nil {
		t.Errorf("expected error, got nil")
	}
}

func TestCertManagerSourceEnsureRejectsMissingServingKey(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeBundleAsCertManagerWould(t, dir)
	if err := os.Remove(filepath.Join(dir, certs.FileServingKey)); err != nil {
		t.Fatalf("remove tls.key: %v", err)
	}

	src, err := certs.NewCertManagerSource(dir, certs.WithCertManagerNow(func() time.Time { return testTime }))
	if err != nil {
		t.Fatalf("NewCertManagerSource: %v", err)
	}
	if err := src.Localize(context.Background()); err == nil {
		t.Errorf("expected error, got nil")
	}
}

func TestCertManagerSourceEnsureRejectsCorruptServingCert(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeBundleAsCertManagerWould(t, dir)
	if err := os.WriteFile(filepath.Join(dir, certs.FileServingCert), []byte("not a PEM"), 0o600); err != nil {
		t.Fatalf("overwrite tls.crt: %v", err)
	}

	src, err := certs.NewCertManagerSource(dir, certs.WithCertManagerNow(func() time.Time { return testTime }))
	if err != nil {
		t.Fatalf("NewCertManagerSource: %v", err)
	}
	if err := src.Localize(context.Background()); err == nil {
		t.Errorf("expected error, got nil")
	}
}

func TestCertManagerSourceEnsureRejectsCorruptCA(t *testing.T) {
	t.Parallel()

	// Partial CA must error (rather than silently treat it as missing)
	// so a half-written rotation surfaces as a hard failure.
	dir := t.TempDir()
	writeBundleAsCertManagerWould(t, dir)
	if err := os.WriteFile(filepath.Join(dir, certs.FileCACert), []byte("not a PEM"), 0o600); err != nil {
		t.Fatalf("overwrite ca.crt: %v", err)
	}

	src, err := certs.NewCertManagerSource(dir, certs.WithCertManagerNow(func() time.Time { return testTime }))
	if err != nil {
		t.Fatalf("NewCertManagerSource: %v", err)
	}
	if err := src.Localize(context.Background()); err == nil {
		t.Errorf("expected error, got nil")
	}
}

func TestNewCertManagerSourceRejectsEmptyDir(t *testing.T) {
	t.Parallel()

	if _, err := certs.NewCertManagerSource(""); err == nil {
		t.Errorf("expected error, got nil")
	}
}

// TestCertManagerSourceEnsureRejectsExpiredCert pins the validity-
// window check: a misconfigured Issuer (or a stale manual Secret)
// can leave an expired cert on disk; the apiserver would reject the
// TLS handshake and every webhook call would fall through
// failurePolicy=Ignore until cert-manager rotates. Failing fast at
// Ensure surfaces the problem as a clear manager startup error.
func TestCertManagerSourceEnsureRejectsExpiredCert(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeBundleAsCertManagerWould(t, dir)

	// The bundle is minted with notBefore=testTime,
	// notAfter=testTime+60d. A clock 90 days past testTime is
	// strictly after notAfter.
	afterExpiry := testTime.Add(90 * 24 * time.Hour)
	src, err := certs.NewCertManagerSource(dir, certs.WithCertManagerNow(func() time.Time { return afterExpiry }))
	if err != nil {
		t.Fatalf("NewCertManagerSource: %v", err)
	}
	err = src.Localize(context.Background())
	if err == nil {
		t.Fatal("expected expired-cert error, got nil")
	}
	if !strings.Contains(err.Error(), "expired") {
		t.Errorf("error %q does not mention 'expired'", err)
	}
}

// TestCertManagerSourceEnsureRejectsNotYetValidCert pins the
// symmetric branch: a Secret minted with a future notBefore (clock
// skew between cert-manager and the operator, or a deliberately
// post-dated cert) must also fail Ensure rather than silently passing.
func TestCertManagerSourceEnsureRejectsNotYetValidCert(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeBundleAsCertManagerWould(t, dir)

	// 30 days before testTime (the cert's notBefore).
	beforeNotBefore := testTime.Add(-30 * 24 * time.Hour)
	src, err := certs.NewCertManagerSource(dir, certs.WithCertManagerNow(func() time.Time { return beforeNotBefore }))
	if err != nil {
		t.Fatalf("NewCertManagerSource: %v", err)
	}
	err = src.Localize(context.Background())
	if err == nil {
		t.Fatal("expected not-yet-valid-cert error, got nil")
	}
	if !strings.Contains(err.Error(), "not yet valid") {
		t.Errorf("error %q does not mention 'not yet valid'", err)
	}
}

// TestCertManagerSourceEnsureAcceptsRSAPKCS8 covers the format
// matrix cert-manager Issuers produce: RSA keys encoded as PKCS#8
// is the default emission for many of the built-in Issuers and the
// SelfSignedSource's stricter SEC1-only decoder rejects them. The
// CertManagerSource must accept this material since it is owned by
// cert-manager, not by squirrel.
func TestCertManagerSourceEnsureAcceptsRSAPKCS8(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeRSACertManagerBundle(t, dir)

	src, err := certs.NewCertManagerSource(dir, certs.WithCertManagerNow(func() time.Time { return testTime }))
	if err != nil {
		t.Fatalf("NewCertManagerSource: %v", err)
	}
	if err := src.Localize(context.Background()); err != nil {
		t.Errorf("Ensure: unexpected error %v (cert-manager mode must accept RSA + PKCS#8)", err)
	}
}

// TestCertManagerSourceEnsureAcceptsCertificateChain covers the
// standard cert-manager TLS Secret shape: tls.crt carries the leaf
// followed by intermediate CA certificates in a single PEM file.
// The webhook server happily presents the whole chain at handshake
// time, so VerifyServingMaterial must accept it - rejecting trailing
// CERTIFICATE blocks would refuse a perfectly valid Secret and
// break cert-manager mode at startup.
func TestCertManagerSourceEnsureAcceptsCertificateChain(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeRSACertManagerBundle(t, dir)

	// Append the CA cert as an intermediate after the leaf in
	// tls.crt. This is the shape cert-manager Issuers produce for
	// issuers that chain to a real intermediate.
	servingPath := filepath.Join(dir, certs.FileServingCert)
	leaf, err := os.ReadFile(servingPath) //nolint:gosec // path is t.TempDir() joined with a constant
	if err != nil {
		t.Fatalf("ReadFile leaf: %v", err)
	}
	caPath := filepath.Join(dir, certs.FileCACert)
	chainEntry, err := os.ReadFile(caPath) //nolint:gosec // path is t.TempDir() joined with a constant
	if err != nil {
		t.Fatalf("ReadFile chain: %v", err)
	}
	combined := append(append([]byte(nil), leaf...), chainEntry...)
	if writeErr := os.WriteFile(servingPath, combined, 0o600); writeErr != nil { //nolint:gosec // path is t.TempDir() joined with a constant
		t.Fatalf("WriteFile chain: %v", writeErr)
	}

	src, err := certs.NewCertManagerSource(dir, certs.WithCertManagerNow(func() time.Time { return testTime }))
	if err != nil {
		t.Fatalf("NewCertManagerSource: %v", err)
	}
	if err := src.Localize(context.Background()); err != nil {
		t.Errorf("Ensure: unexpected error %v (cert-manager mode must accept leaf+chain in tls.crt)", err)
	}
}

// TestCertManagerSourceEnsureAcceptsRSAPKCS1 covers the legacy
// PKCS#1 ("RSA PRIVATE KEY") encoding alongside the default PKCS#8
// path. A cert-manager Issuer chained to a private CA may still emit
// PKCS#1 keys; decodeAnyPrivateKey claims to support all three
// formats (SEC1, PKCS#1, PKCS#8) and this test pins the PKCS#1 arm
// so the dispatch matrix in pem.go does not regress.
func TestCertManagerSourceEnsureAcceptsRSAPKCS1(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeRSAPKCS1CertManagerBundle(t, dir)

	src, err := certs.NewCertManagerSource(dir, certs.WithCertManagerNow(func() time.Time { return testTime }))
	if err != nil {
		t.Fatalf("NewCertManagerSource: %v", err)
	}
	if err := src.Localize(context.Background()); err != nil {
		t.Errorf("Ensure: unexpected error %v (cert-manager mode must accept RSA + PKCS#1)", err)
	}
}

// writeRSAPKCS1CertManagerBundle is the PKCS#1 sibling of
// writeRSACertManagerBundle: same RSA leaf material, but the
// private key block is marshalled via MarshalPKCS1PrivateKey
// ("RSA PRIVATE KEY" block type) rather than PKCS#8.
func writeRSAPKCS1CertManagerBundle(t *testing.T, dir string) {
	t.Helper()

	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey (CA): %v", err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             testTime,
		NotAfter:              testTime.Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("CreateCertificate (CA): %v", err)
	}

	servingKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey (serving): %v", err)
	}
	servingTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "webhook.svc"},
		NotBefore:    testTime,
		NotAfter:     testTime.Add(60 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"webhook.svc"},
	}
	servingDER, err := x509.CreateCertificate(rand.Reader, servingTemplate, caTemplate, &servingKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("CreateCertificate (serving): %v", err)
	}

	servingKeyDER := x509.MarshalPKCS1PrivateKey(servingKey)

	writes := []struct {
		name    string
		blkType string
		bytes   []byte
	}{
		{certs.FileServingCert, "CERTIFICATE", servingDER},
		{certs.FileServingKey, "RSA PRIVATE KEY", servingKeyDER},
		{certs.FileCACert, "CERTIFICATE", caDER},
	}
	for _, w := range writes {
		data := pem.EncodeToMemory(&pem.Block{Type: w.blkType, Bytes: w.bytes})
		if err := os.WriteFile(filepath.Join(dir, w.name), data, 0o600); err != nil {
			t.Fatalf("write %s: %v", w.name, err)
		}
	}
}

// writeRSACertManagerBundle mints an RSA CA + serving cert pair and
// writes them to dir in the encoding cert-manager's default Issuer
// produces: tls.crt is a CERTIFICATE block, tls.key is a PKCS#8
// PRIVATE KEY block. ca.crt is the issuing CA certificate.
func writeRSACertManagerBundle(t *testing.T, dir string) {
	t.Helper()

	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey (CA): %v", err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             testTime,
		NotAfter:              testTime.Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("CreateCertificate (CA): %v", err)
	}

	servingKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey (serving): %v", err)
	}
	servingTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "webhook.svc"},
		NotBefore:    testTime,
		NotAfter:     testTime.Add(60 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"webhook.svc"},
	}
	servingDER, err := x509.CreateCertificate(rand.Reader, servingTemplate, caTemplate, &servingKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("CreateCertificate (serving): %v", err)
	}

	// PKCS#8 for the RSA private key, matching cert-manager's
	// default emission. SEC1 ("RSA PRIVATE KEY") is also a legal
	// format but we exercise the PKCS#8 path here because that's
	// the more likely cert-manager output.
	servingKeyDER, err := x509.MarshalPKCS8PrivateKey(servingKey)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey: %v", err)
	}

	writes := []struct {
		name    string
		blkType string
		bytes   []byte
	}{
		{certs.FileServingCert, "CERTIFICATE", servingDER},
		{certs.FileServingKey, "PRIVATE KEY", servingKeyDER},
		{certs.FileCACert, "CERTIFICATE", caDER},
	}
	for _, w := range writes {
		data := pem.EncodeToMemory(&pem.Block{Type: w.blkType, Bytes: w.bytes})
		if err := os.WriteFile(filepath.Join(dir, w.name), data, 0o600); err != nil {
			t.Fatalf("write %s: %v", w.name, err)
		}
	}
}
