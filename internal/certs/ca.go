package certs

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"math/big"
	"time"
)

// CA is a self-signed Certificate Authority used to sign the webhook
// server's serving certificate. Callers obtain a *CA either by
// generating a fresh one (NewSelfSignedCA) or by parsing the
// previously-persisted PEM material (ParseCA); both forms expose the
// same accessors so the SelfSignedSource code path is identical
// regardless of whether the CA was just minted or just loaded.
type CA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey

	certPEM []byte
	keyPEM  []byte
}

// NewSelfSignedCA returns a fresh CA whose certificate is valid in
// [notBefore, notAfter), CN=commonName, IsCA=true, with KeyUsage set
// to CertSign|CRLSign per RFC 5280's recommendation for a root CA.
// The caller chooses the validity window so test code can pin time
// without monkey-patching the clock.
func NewSelfSignedCA(commonName string, notBefore, notAfter time.Time) (*CA, error) {
	if commonName == "" {
		return nil, errors.New("CA common name must not be empty")
	}
	if !notBefore.Before(notAfter) {
		return nil, fmt.Errorf("CA notBefore (%s) must be before notAfter (%s)", notBefore, notAfter)
	}

	key, err := newPrivateKey()
	if err != nil {
		return nil, fmt.Errorf("CA key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, fmt.Errorf("CA serial: %w", err)
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		// MaxPathLenZero=true encodes a pathlen:0 BasicConstraints
		// extension, so this CA can only issue end-entity certificates -
		// not intermediate CAs. The webhook CA only ever signs its own
		// serving cert, so the restriction is defence-in-depth against
		// a compromised CA key being used to issue further sub-CAs.
		MaxPathLenZero: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("create CA certificate: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("parse freshly created CA certificate: %w", err)
	}
	certPEM := encodeCertPEM(cert)
	keyPEM, err := encodeKeyPEM(key)
	if err != nil {
		return nil, fmt.Errorf("encode CA key: %w", err)
	}
	return &CA{cert: cert, key: key, certPEM: certPEM, keyPEM: keyPEM}, nil
}

// ParseCA loads a previously-persisted CA from its PEM-encoded
// certificate and key bytes. It rejects materials whose certificate
// is not flagged as a CA, whose KeyUsage does not permit CertSign,
// or whose private key does not match the certificate - the
// operator-rolled-Secret failure modes that would otherwise surface
// much later as an opaque TLS handshake error at the apiserver.
func ParseCA(certPEM, keyPEM []byte) (*CA, error) {
	cert, err := decodeCertPEM(certPEM)
	if err != nil {
		return nil, fmt.Errorf("CA cert: %w", err)
	}
	if !cert.IsCA {
		return nil, errors.New("CA cert: parsed certificate is not flagged as a CA")
	}
	if cert.KeyUsage&x509.KeyUsageCertSign == 0 {
		return nil, errors.New("CA cert: KeyUsage does not include CertSign; cannot be used to issue serving certificates")
	}
	key, err := decodeKeyPEM(keyPEM)
	if err != nil {
		return nil, fmt.Errorf("CA key: %w", err)
	}
	certKey, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("CA cert: public key type %T is not *ecdsa.PublicKey", cert.PublicKey)
	}
	if !key.PublicKey.Equal(certKey) {
		return nil, errors.New("CA: certificate public key does not match private key")
	}
	return &CA{cert: cert, key: key, certPEM: certPEM, keyPEM: keyPEM}, nil
}

// Cert returns the parsed x509 certificate. The webhook server never
// needs this directly; the MWC patcher uses it to detect "already
// up to date" without re-decoding the PEM.
func (c *CA) Cert() *x509.Certificate { return c.cert }

// CertPEM returns the PEM-encoded CA certificate. This is the value
// that goes into the MutatingWebhookConfiguration's caBundle field
// and into the ca.crt key of the webhook-cert Secret.
func (c *CA) CertPEM() []byte { return c.certPEM }

// KeyPEM returns the PEM-encoded CA private key. The key is stored
// in the webhook-cert Secret so the leader can re-issue the serving
// certificate on rotation without minting a fresh CA (which would
// require the apiserver to re-trust a new bundle).
func (c *CA) KeyPEM() []byte { return c.keyPEM }

// randomSerial returns a random 128-bit serial number suitable for an
// x509 certificate. RFC 5280 requires the serial to be at least 64
// bits of randomness; 128 bits gives ample headroom against
// collisions across CA regenerations.
func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	n, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, fmt.Errorf("read random serial: %w", err)
	}
	return n, nil
}
