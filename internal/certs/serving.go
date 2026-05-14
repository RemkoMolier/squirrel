package certs

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"time"
)

// ServingCert is the TLS material the webhook server presents to the
// apiserver. The certificate is signed by a CA produced by
// NewSelfSignedCA and carries every DNS name through which the
// apiserver might dial the webhook service - typically
// `<service>.<namespace>.svc` and its FQDN variants.
type ServingCert struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey

	certPEM []byte
	keyPEM  []byte
}

// IssueServingCert mints a serving certificate signed by ca, valid in
// [notBefore, notAfter), with the given DNS names as SubjectAltNames.
// At least one DNS name is required; modern Go TLS verification
// ignores the certificate's Common Name when SANs are present, so
// callers should populate dnsNames with the names the apiserver
// will use to dial the webhook service.
func IssueServingCert(ca *CA, dnsNames []string, notBefore, notAfter time.Time) (*ServingCert, error) {
	if ca == nil {
		return nil, errors.New("serving cert: ca must not be nil")
	}
	if len(dnsNames) == 0 {
		return nil, errors.New("serving cert: at least one DNS name is required")
	}
	if !notBefore.Before(notAfter) {
		return nil, fmt.Errorf("serving cert notBefore (%s) must be before notAfter (%s)", notBefore, notAfter)
	}

	key, err := newPrivateKey()
	if err != nil {
		return nil, fmt.Errorf("serving key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, fmt.Errorf("serving serial: %w", err)
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: dnsNames[0]},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     append([]string(nil), dnsNames...),
		// Explicit IsCA=false (with BasicConstraintsValid=true so the
		// extension is actually emitted) is RFC 5280 hygiene for an
		// end-entity certificate. Modern Go TLS verifiers do not
		// require it, but it makes the cert unambiguous to non-Go
		// consumers and rules out a CA being substituted for a
		// serving cert by an out-of-band tool.
		BasicConstraintsValid: true,
		IsCA:                  false,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		return nil, fmt.Errorf("create serving certificate: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("parse freshly created serving certificate: %w", err)
	}
	certPEM := encodeCertPEM(cert)
	keyPEM, err := encodeKeyPEM(key)
	if err != nil {
		return nil, fmt.Errorf("encode serving key: %w", err)
	}
	return &ServingCert{cert: cert, key: key, certPEM: certPEM, keyPEM: keyPEM}, nil
}

// ParseServingCert loads a previously-persisted serving certificate
// from its PEM-encoded cert and key bytes. It rejects materials
// whose certificate is flagged as a CA and whose public key does not
// match the private key - the operator-rolled-Secret failure modes
// that would otherwise surface much later as an opaque TLS handshake
// error at the apiserver. The caller is responsible for verifying
// that the certificate is still valid against the expected CA; this
// function only validates the structural integrity of the material.
func ParseServingCert(certPEM, keyPEM []byte) (*ServingCert, error) {
	cert, err := decodeCertPEM(certPEM)
	if err != nil {
		return nil, fmt.Errorf("serving cert: %w", err)
	}
	if cert.IsCA {
		return nil, errors.New("serving cert: parsed certificate is flagged as a CA; refusing to use as a serving cert")
	}
	key, err := decodeKeyPEM(keyPEM)
	if err != nil {
		return nil, fmt.Errorf("serving key: %w", err)
	}
	certKey, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("serving cert: public key type %T is not *ecdsa.PublicKey", cert.PublicKey)
	}
	if !key.PublicKey.Equal(certKey) {
		return nil, errors.New("serving: certificate public key does not match private key")
	}
	return &ServingCert{cert: cert, key: key, certPEM: certPEM, keyPEM: keyPEM}, nil
}

// VerifyServingMaterial validates a serving cert + key pair without
// assuming the key algorithm or that the cert PEM carries exactly
// one CERTIFICATE block. It accepts:
//
//   - ECDSA and RSA keys in any of the SEC1, PKCS#1, or PKCS#8 PEM
//     encodings - cert-manager's default Issuer often emits RSA
//     plus PKCS#8.
//   - tls.crt with a leaf-plus-intermediates chain - the standard
//     cert-manager TLS Secret shape; the webhook server presents
//     the whole chain at handshake time.
//
// The verification matches ParseServingCert on the leaf: it must
// not be flagged as a CA, the key must parse, and the leaf's
// public key must equal the private key's public key. The chain
// entries (if any) are parsed for syntactic sanity but otherwise
// ignored - the apiserver verifies the chain against the MWC
// caBundle.
//
// The function does NOT return the parsed material because
// CertManagerSource (its only caller) never needs the key on the
// Go side; the webhook server reads the PEM files directly from
// CertDir via tls.X509KeyPair.
func VerifyServingMaterial(certPEM, keyPEM []byte) error {
	cert, err := decodeLeafCertPEM(certPEM)
	if err != nil {
		return fmt.Errorf("serving cert: %w", err)
	}
	if cert.IsCA {
		return errors.New("serving cert: parsed certificate is flagged as a CA; refusing to use as a serving cert")
	}
	signer, err := decodeAnyPrivateKey(keyPEM)
	if err != nil {
		return fmt.Errorf("serving key: %w", err)
	}
	type publicKeyEqualer interface {
		Equal(crypto.PublicKey) bool
	}
	certPub, ok := cert.PublicKey.(publicKeyEqualer)
	if !ok {
		return fmt.Errorf("serving cert: public key type %T does not support equality comparison", cert.PublicKey)
	}
	if !certPub.Equal(signer.Public()) {
		return errors.New("serving: certificate public key does not match private key")
	}
	return nil
}

// Cert returns the parsed x509 certificate; used by NeedsRotation and
// by tests asserting on SAN content.
func (s *ServingCert) Cert() *x509.Certificate { return s.cert }

// CertPEM returns the PEM-encoded serving certificate. The webhook
// server loads this together with KeyPEM via tls.X509KeyPair.
func (s *ServingCert) CertPEM() []byte { return s.certPEM }

// KeyPEM returns the PEM-encoded serving private key.
func (s *ServingCert) KeyPEM() []byte { return s.keyPEM }
