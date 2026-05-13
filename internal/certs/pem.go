package certs

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
)

// PEM block types. Constants rather than literals so a typo breaks
// compilation rather than silently producing material the apiserver
// will reject.
const (
	pemTypeCertificate = "CERTIFICATE"
	pemTypeECPrivKey   = "EC PRIVATE KEY"
	pemTypeRSAPrivKey  = "RSA PRIVATE KEY"
	pemTypePKCS8Key    = "PRIVATE KEY"
)

// encodeCertPEM returns the PEM-encoded form of the given x509
// certificate. encoding/pem cannot fail when writing to a
// bytes.Buffer-equivalent backing slice, so the helper returns just
// the bytes.
func encodeCertPEM(cert *x509.Certificate) []byte {
	return pem.EncodeToMemory(&pem.Block{
		Type:  pemTypeCertificate,
		Bytes: cert.Raw,
	})
}

// encodeKeyPEM returns the PEM-encoded SEC1 form of the given ECDSA
// private key. PKCS#8 would also work for ECDSA P-256, but SEC1
// matches what cert-manager emits into webhook-cert Secrets, so
// staying on SEC1 keeps the on-disk format stable across cert
// sources.
func encodeKeyPEM(key *ecdsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshal EC private key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{
		Type:  pemTypeECPrivKey,
		Bytes: der,
	}), nil
}

// decodeCertPEM parses the first CERTIFICATE block in data and
// returns the resulting x509.Certificate. The function rejects
// trailing data of any block type to surface accidental
// concatenation of multiple files (a common operator error when
// hand-rolling Secret materials). Use decodeLeafCertPEM instead
// when the input may legitimately carry a leaf + intermediates
// chain (the cert-manager-emitted tls.crt shape).
func decodeCertPEM(data []byte) (*x509.Certificate, error) {
	block, rest := pem.Decode(data)
	if block == nil {
		return nil, errors.New("decode certificate PEM: no PEM block found")
	}
	if block.Type != pemTypeCertificate {
		return nil, fmt.Errorf("decode certificate PEM: unexpected block type %q, want %q", block.Type, pemTypeCertificate)
	}
	if len(trimWhitespace(rest)) != 0 {
		return nil, errors.New("decode certificate PEM: trailing data after CERTIFICATE block")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse certificate: %w", err)
	}
	return cert, nil
}

// decodeLeafCertPEM parses the first CERTIFICATE block in data and
// returns it, tolerating one or more trailing CERTIFICATE blocks
// (the intermediates of a chain). Trailing data that is not a
// CERTIFICATE block is rejected so an accidentally-concatenated key
// or pasted-in garbage still surfaces as an error. cert-manager-
// issued TLS Secrets follow this leaf-plus-chain shape on tls.crt;
// the webhook server happily presents the whole chain at handshake
// time, so the verifier must accept it.
func decodeLeafCertPEM(data []byte) (*x509.Certificate, error) {
	block, rest := pem.Decode(data)
	if block == nil {
		return nil, errors.New("decode certificate PEM: no PEM block found")
	}
	if block.Type != pemTypeCertificate {
		return nil, fmt.Errorf("decode certificate PEM: unexpected block type %q, want %q", block.Type, pemTypeCertificate)
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse leaf certificate: %w", err)
	}
	// Walk the chain: every subsequent block must be a CERTIFICATE
	// (intermediates), otherwise the trailing data is something the
	// operator did not intend to be there.
	rest = trimWhitespace(rest)
	for len(rest) > 0 {
		next, after := pem.Decode(rest)
		if next == nil {
			return nil, errors.New("decode certificate PEM: trailing non-PEM data after CERTIFICATE block(s)")
		}
		if next.Type != pemTypeCertificate {
			return nil, fmt.Errorf("decode certificate PEM: unexpected trailing block type %q (chain entries must be %q)",
				next.Type, pemTypeCertificate)
		}
		// We don't keep the intermediate parsed - the apiserver
		// verifies the chain against its own MWC caBundle. Parsing
		// is just a syntax sanity check.
		if _, err := x509.ParseCertificate(next.Bytes); err != nil {
			return nil, fmt.Errorf("parse chain certificate: %w", err)
		}
		rest = trimWhitespace(after)
	}
	return leaf, nil
}

// decodeKeyPEM parses the first EC PRIVATE KEY block in data and
// returns the resulting ECDSA private key. This is the strict
// decoder used by ParseCA and the SelfSignedSource's own material
// round-trip; cert-manager-emitted Secrets go through
// decodeAnyPrivateKey instead, which accepts the broader format
// matrix cert-manager Issuers produce.
func decodeKeyPEM(data []byte) (*ecdsa.PrivateKey, error) {
	block, rest := pem.Decode(data)
	if block == nil {
		return nil, errors.New("decode key PEM: no PEM block found")
	}
	if block.Type != pemTypeECPrivKey {
		return nil, fmt.Errorf("decode key PEM: unexpected block type %q, want %q", block.Type, pemTypeECPrivKey)
	}
	if len(trimWhitespace(rest)) != 0 {
		return nil, errors.New("decode key PEM: trailing data after EC PRIVATE KEY block")
	}
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse EC private key: %w", err)
	}
	return key, nil
}

// decodeAnyPrivateKey parses the first private-key PEM block in data
// in any of the three standard webhook-server formats:
//
//   - SEC1 ("EC PRIVATE KEY"): the format SelfSignedSource emits.
//   - PKCS#1 ("RSA PRIVATE KEY"): legacy OpenSSL-style RSA keys.
//   - PKCS#8 ("PRIVATE KEY"): the modern any-algorithm encoding;
//     cert-manager's default Issuer often emits this for RSA keys.
//
// The return value satisfies crypto.Signer (every supported key
// algorithm does), so callers compare public-key equality via
// Signer.Public().
func decodeAnyPrivateKey(data []byte) (crypto.Signer, error) {
	block, rest := pem.Decode(data)
	if block == nil {
		return nil, errors.New("decode key PEM: no PEM block found")
	}
	if len(trimWhitespace(rest)) != 0 {
		return nil, errors.New("decode key PEM: trailing data after key block")
	}
	switch block.Type {
	case pemTypeECPrivKey:
		key, err := x509.ParseECPrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse EC private key: %w", err)
		}
		return key, nil
	case pemTypeRSAPrivKey:
		key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse RSA (PKCS#1) private key: %w", err)
		}
		return key, nil
	case pemTypePKCS8Key:
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse PKCS#8 private key: %w", err)
		}
		signer, ok := key.(crypto.Signer)
		if !ok {
			return nil, fmt.Errorf("decode key PEM: PKCS#8 key type %T does not implement crypto.Signer", key)
		}
		return signer, nil
	default:
		return nil, fmt.Errorf("decode key PEM: unexpected block type %q (want %q, %q, or %q)",
			block.Type, pemTypeECPrivKey, pemTypeRSAPrivKey, pemTypePKCS8Key)
	}
}

// trimWhitespace returns data with leading/trailing ASCII whitespace
// stripped. pem.Decode leaves any trailing newline behind in `rest`,
// which a strict "no trailing data" check would flag as garbage; this
// helper distinguishes "literally nothing left" from "another block
// follows" without pulling in unicode.
func trimWhitespace(data []byte) []byte {
	start := 0
	for start < len(data) && isWhitespace(data[start]) {
		start++
	}
	end := len(data)
	for end > start && isWhitespace(data[end-1]) {
		end--
	}
	return data[start:end]
}

func isWhitespace(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\r':
		return true
	}
	return false
}
