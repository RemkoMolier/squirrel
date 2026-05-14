package certs

import (
	"crypto/x509"
	"time"
)

// NeedsRotation reports whether cert should be rotated given a
// rotation threshold and the current time. The check is conservative:
// a nil certificate is treated as "must rotate" so the SelfSignedSource
// can pass a freshly-loaded *x509.Certificate without nil-checking
// first.
//
// Rotation fires when the certificate's NotAfter is in the past, when
// it is within threshold of now (so the operator regenerates before
// the apiserver starts rejecting handshakes), and trivially when the
// certificate is nil.
func NeedsRotation(cert *x509.Certificate, threshold time.Duration, now time.Time) bool {
	if cert == nil {
		return true
	}
	return !now.Add(threshold).Before(cert.NotAfter)
}
