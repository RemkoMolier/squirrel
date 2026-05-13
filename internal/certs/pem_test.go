package certs_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/RemkoMolier/squirrel/internal/certs"
)

// TestParseCARejectsMalformedPEM is a cross-cutting check that the
// PEM decode path surfaces clear errors for the credible operator
// mistakes when wiring a Secret by hand:
//
//   - empty bytes (a missing field in the Secret),
//   - garbage that contains no PEM block at all,
//   - a CERTIFICATE block whose body is not a valid DER cert.
//
// The serving variant has the same shape so we keep one consolidated
// regression guard on the CA path.
func TestParseCARejectsMalformedPEM(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cert []byte
		key  []byte
		want string
	}{
		{name: "empty cert PEM", cert: nil, key: nil, want: "no PEM block"},
		{name: "garbage cert PEM", cert: []byte("hello world"), key: nil, want: "no PEM block"},
		{name: "wrong cert block type", cert: []byte("-----BEGIN PUBLIC KEY-----\nQUE=\n-----END PUBLIC KEY-----\n"), key: nil, want: "unexpected block type"},
		{name: "corrupt cert body", cert: []byte("-----BEGIN CERTIFICATE-----\nQUE=\n-----END CERTIFICATE-----\n"), key: nil, want: "parse certificate"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := certs.ParseCA(tt.cert, tt.key)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error: got %q, want substring %q", err.Error(), tt.want)
			}
		})
	}
}

// TestParseCARejectsTrailingData covers the strict "no extra blocks"
// behaviour: if a Secret accidentally concatenates the CA cert with
// the serving cert into one field, ParseCA must surface the error
// instead of silently using only the first block.
func TestParseCARejectsTrailingData(t *testing.T) {
	t.Parallel()

	ca, err := certs.NewSelfSignedCA("squirrel-ca", testTime, testTime.Add(time.Hour))
	if err != nil {
		t.Fatalf("NewSelfSignedCA: %v", err)
	}
	concatenated := append([]byte{}, ca.CertPEM()...)
	concatenated = append(concatenated, ca.CertPEM()...)

	if _, err := certs.ParseCA(concatenated, ca.KeyPEM()); err == nil {
		t.Errorf("ParseCA accepted concatenated PEM; want error")
	}
}

// TestVerifyServingMaterialRejectsECDSACurveBelowFloor pins the
// ECDSA curve floor symmetric to the 2048-bit RSA minimum: a key
// backed by P-224 (NIST's legacy curve still accepted by some FIPS
// profiles but below the 128-bit-security-strength floor) must be
// rejected at the decode step, before any handshake math runs.
// SelfSignedSource mints P-256 and never trips this; the check
// exists for cert-manager Secrets that originated from a
// non-standard Issuer.
func TestVerifyServingMaterialRejectsECDSACurveBelowFloor(t *testing.T) {
	t.Parallel()

	key, err := ecdsa.GenerateKey(elliptic.P224(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa.GenerateKey(P224): %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "weak-curve-leaf"},
		NotBefore:    testTime,
		NotAfter:     testTime.Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})

	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})

	err = certs.VerifyServingMaterial(certPEM, keyPEM)
	if err == nil {
		t.Fatal("expected error rejecting P-224 ECDSA key, got nil")
	}
	if !strings.Contains(err.Error(), "P-256, P-384, P-521") {
		t.Errorf("error message %q does not name the accepted-curve list", err)
	}
}

// TestParseCAAcceptsTrailingWhitespace pins down that the strict
// trailing-data check tolerates the trailing newline encoding/pem
// leaves behind by default, so a Secret round-trip does not need to
// massage whitespace.
func TestParseCAAcceptsTrailingWhitespace(t *testing.T) {
	t.Parallel()

	ca, err := certs.NewSelfSignedCA("squirrel-ca", testTime, testTime.Add(time.Hour))
	if err != nil {
		t.Fatalf("NewSelfSignedCA: %v", err)
	}
	padded := append([]byte{}, ca.CertPEM()...)
	padded = append(padded, "\n  \t\n"...)
	if _, err := certs.ParseCA(padded, ca.KeyPEM()); err != nil {
		t.Errorf("ParseCA rejected PEM with trailing whitespace: %v", err)
	}
}
