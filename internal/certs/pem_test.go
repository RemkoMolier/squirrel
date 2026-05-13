package certs_test

import (
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
