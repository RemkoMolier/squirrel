package certs

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"fmt"
)

// newPrivateKey returns a fresh ECDSA P-256 private key. P-256 is the
// modern default for short-lived webhook serving certificates and is
// supported by every Kubernetes apiserver shipped in the past decade;
// the SAN-only X.509 profile this package emits has no reason to pick
// anything else.
func newPrivateKey() (*ecdsa.PrivateKey, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate ECDSA P-256 key: %w", err)
	}
	return key, nil
}
