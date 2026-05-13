// Package certs manages the webhook server's TLS material per
// docs/design/v1alpha1.md.
//
// The package is split into layers, each independently unit-testable:
//
//   - Pure cryptographic primitives (this file's siblings):
//     ECDSA P-256 key generation, self-signed CA construction,
//     CA-signed serving-cert issuance, PEM encode/decode, and a
//     NeedsRotation expiry check. None of these have any Kubernetes
//     dependency; they are pure functions of their inputs and the
//     provided clock.
//
//   - Secret round-trip (forthcoming): load the CA + serving
//     material from a Kubernetes Secret and re-issue when it is
//     missing or near expiry. Idempotent under repeated calls.
//
//   - MWC patcher (forthcoming): write the CA bundle into the
//     MutatingWebhookConfiguration's clientConfig so the apiserver
//     trusts the webhook server's serving cert.
//
//   - CertSource interface + implementations (forthcoming):
//     SelfSignedSource composes the layers above and writes the
//     serving material to disk for the webhook server. CertManagerSource
//     is a passive reader that trusts cert-manager to populate the
//     mounted Secret-projected volume.
//
//   - Runnable (forthcoming): a controller-runtime manager.Runnable
//     that periodically calls SelfSignedSource.Ensure so the cert is
//     rotated before its expiry without operator intervention.
//
// All keys are ECDSA P-256 per the operator's design choice; CA
// certificates are valid for five years, serving certificates for one
// year, and rotation triggers when a certificate is within thirty days
// of its expiry. These defaults are unconfigurable in the primitives
// layer - the SelfSignedSource exposes the threshold as a flag.
package certs
