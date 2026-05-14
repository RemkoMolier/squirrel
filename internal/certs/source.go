package certs

import (
	"context"
)

// CertSource is the contract every cert mode satisfies.
//
// The model is split deliberately into apiserver-authoritative and
// local-reflective halves: the apiserver-side work (mint/rotate the
// Secret, patch the MutatingWebhookConfiguration's caBundle) runs
// only on the elected leader, so two concurrent replicas can never
// race the apiserver. Every replica still has to reflect that state
// onto its own on-disk CertDir so the webhook server can load TLS;
// that local mirror is read-only against the apiserver and runs on
// every replica.
//
// The split eliminates the loser-of-race code paths (resourceVersion
// Conflict retries on Secret Update, idempotency short-circuits on
// MWC patch) that the earlier single-method design needed.
type CertSource interface {
	// Authoritative mints or rotates apiserver-side state. For
	// SelfSignedSource it Get-or-Creates the Secret, rotates the
	// material when it is missing/unparseable/near expiry, and
	// patches the MutatingWebhookConfiguration's caBundle so the
	// apiserver trusts the new CA. For CertManagerSource it is a
	// no-op: cert-manager owns the Secret and the caBundle
	// injection.
	//
	// Authoritative is wired behind leader election in the manager.
	// During bootstrap it is also called on every replica (before
	// leader election fires) because the webhook server cannot
	// start until tls.crt/tls.key are on disk; that race is absorbed
	// by the apiserver-serialised Create + AlreadyExists short
	// circuit, not by a Conflict-retry loop.
	Authoritative(ctx context.Context) error

	// Localize reads the apiserver-side state and reflects it onto
	// CertDir, then sanity-checks the on-disk bundle. For
	// SelfSignedSource it Gets the Secret, parses the four PEM
	// blobs, and writes tls.crt/tls.key/ca.crt to CertDir
	// atomically. For CertManagerSource it parses the cert-manager
	// volume-mounted PEM files and verifies their validity window.
	//
	// Localize runs on every replica (leader and followers alike)
	// so each pod's webhook server stays in sync with the canonical
	// apiserver-side material. It performs no apiserver writes.
	Localize(ctx context.Context) error

	// CertDir is the directory on disk where the webhook server
	// finds tls.crt and tls.key. The directory is created lazily
	// by Localize when it does not yet exist.
	CertDir() string
}

// Standard file names inside CertDir. The names match
// controller-runtime's webhook.Server defaults (CertName/KeyName), so
// the manager wiring in Phase 7 does not need to override them.
const (
	FileServingCert = "tls.crt"
	FileServingKey  = "tls.key"
	FileCACert      = "ca.crt"
)
