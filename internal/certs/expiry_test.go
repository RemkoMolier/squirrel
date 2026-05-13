package certs_test

import (
	"crypto/x509"
	"testing"
	"time"

	"github.com/RemkoMolier/squirrel/internal/certs"
)

func TestNeedsRotationNilCertificate(t *testing.T) {
	t.Parallel()

	// A nil certificate models "no Secret data yet" or "the Secret is
	// missing the ca.crt field" - either way the source must rotate.
	if !certs.NeedsRotation(nil, 30*24*time.Hour, testTime) {
		t.Errorf("nil cert: got false, want true")
	}
}

func TestNeedsRotationTable(t *testing.T) {
	t.Parallel()

	const threshold = 30 * 24 * time.Hour
	notAfter := testTime.Add(60 * 24 * time.Hour) // expires 60 days after testTime

	tests := []struct {
		name string
		now  time.Time
		want bool
	}{
		{name: "well before threshold window", now: testTime.Add(10 * 24 * time.Hour), want: false},
		{name: "just outside threshold window", now: notAfter.Add(-threshold - time.Second), want: false},
		{name: "exactly at threshold boundary", now: notAfter.Add(-threshold), want: true},
		{name: "inside threshold window", now: notAfter.Add(-time.Hour), want: true},
		{name: "exactly at expiry", now: notAfter, want: true},
		{name: "past expiry", now: notAfter.Add(time.Hour), want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cert := &x509.Certificate{NotAfter: notAfter}
			if got := certs.NeedsRotation(cert, threshold, tt.now); got != tt.want {
				t.Errorf("NeedsRotation: got %t, want %t", got, tt.want)
			}
		})
	}
}
