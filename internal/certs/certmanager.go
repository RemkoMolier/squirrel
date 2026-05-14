package certs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// CertManagerSource is the passive CertSource used when cert-manager
// manages the webhook's TLS material.
//
// In cert-manager mode the operator's Deployment mounts the
// cert-manager-issued Secret as a projected volume at CertDir, and
// cert-manager rotates the contents in place. The MWC carries a
// cert-manager.io/inject-ca-from annotation, so cert-manager itself
// patches the caBundle: this source never reaches the apiserver.
//
// Ensure is a verification step rather than a mutation: it asserts
// that the mounted files exist, parse, and are within their
// validity window, so the manager fails fast on a misconfigured
// deployment rather than letting the webhook server crash on its
// first request. Parsing goes through VerifyServingMaterial, which
// accepts ECDSA and RSA keys in any of SEC1, PKCS#1, or PKCS#8 -
// the format matrix cert-manager Issuers can produce, including
// the RSA + PKCS#8 default that the SelfSignedSource's stricter
// decoder would reject.
type CertManagerSource struct {
	certDir string

	// now is the clock the validity-window check reads. Nil uses
	// time.Now; tests inject a fixed instant to pin the
	// expired/not-yet-valid branches.
	now func() time.Time
}

// CertManagerOption configures optional knobs on a CertManagerSource.
// Production callers pass none; tests inject a fixed clock for the
// validity-window check via WithCertManagerNow.
type CertManagerOption func(*CertManagerSource)

// WithCertManagerNow overrides the source's clock. Pass a function
// that returns a fixed instant in tests; production should leave
// this unset so the source reads time.Now.
func WithCertManagerNow(now func() time.Time) CertManagerOption {
	return func(s *CertManagerSource) { s.now = now }
}

// NewCertManagerSource validates certDir and returns a configured
// source. It does not read the directory yet; call Ensure to do
// that.
func NewCertManagerSource(certDir string, opts ...CertManagerOption) (*CertManagerSource, error) {
	if certDir == "" {
		return nil, errors.New("CertManagerSource: certDir must not be empty")
	}
	s := &CertManagerSource{certDir: certDir}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

// nowFn returns the source's clock (default time.Now) so the
// validity-window check is testable without freezing the
// process clock.
func (s *CertManagerSource) nowFn() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// CertDir implements CertSource. Returns the same value passed in
// to NewCertManagerSource.
func (s *CertManagerSource) CertDir() string { return s.certDir }

// Authoritative implements CertSource. cert-manager owns the Secret
// and patches the MWC caBundle via its inject-ca-from annotation, so
// this source never reaches the apiserver - the method is a no-op.
// Keeping it on the interface (rather than introducing a separate
// "writeable" sub-interface) keeps the manager wiring uniform across
// the two source modes.
func (s *CertManagerSource) Authoritative(_ context.Context) error { return nil }

// Localize implements CertSource. The source reads the three
// expected files (tls.crt, tls.key, ca.crt) and parses them; an
// error means cert-manager has not populated the Secret yet, the
// volume mount is misconfigured, or the material is corrupt.
//
// The CA file is optional: cert-manager Issuers backed by an
// external CA do not always provide ca.crt alongside the serving
// material. The function therefore tolerates a missing ca.crt but
// rejects a present-but-corrupt one.
//
// ctx is part of the CertSource contract so future implementations
// (e.g. fsnotify-driven reload) can honour cancellation; the current
// implementation only performs synchronous filesystem reads, so the
// argument is unused.
func (s *CertManagerSource) Localize(_ context.Context) error {
	// The paths are constructed from the operator-controlled certDir
	// (a Pod volume mount) joined with package-level constant
	// filenames; there is no user input on the read path.
	servingCertPath := filepath.Join(s.certDir, FileServingCert)
	servingCert, err := os.ReadFile(servingCertPath) //nolint:gosec // path is operator-controlled CertDir joined with a constant filename
	if err != nil {
		return fmt.Errorf("read %s: %w", servingCertPath, err)
	}
	servingKeyPath := filepath.Join(s.certDir, FileServingKey)
	servingKey, readErr := os.ReadFile(servingKeyPath) //nolint:gosec // path is operator-controlled CertDir joined with a constant filename
	if readErr != nil {
		return fmt.Errorf("read %s: %w", servingKeyPath, readErr)
	}
	// VerifyServingMaterial (not ParseServingCert) so the key matrix
	// matches what cert-manager Issuers emit: the default Issuer
	// produces RSA keys in PKCS#8, which the SelfSignedSource's own
	// stricter ECDSA/SEC1 decoder would reject.
	if parseErr := VerifyServingMaterial(servingCert, servingKey); parseErr != nil {
		return fmt.Errorf("parse %s: %w", servingCertPath, parseErr)
	}
	// Validity-window check: cert-manager rotates by issuing a fresh
	// Secret and the volume mount picks it up, but a misconfigured
	// Issuer (or a manual Secret) can leave an expired or not-yet-
	// valid cert on disk. The apiserver would reject the handshake
	// and every webhook call would fall through failurePolicy=Ignore
	// until cert-manager catches up. Failing fast here surfaces the
	// problem as a manager startup error instead of as silent
	// admit-unchanged decisions.
	leaf, leafErr := decodeLeafCertPEM(servingCert)
	if leafErr != nil {
		return fmt.Errorf("parse %s: %w", servingCertPath, leafErr)
	}
	now := s.nowFn()
	if now.Before(leaf.NotBefore) {
		return fmt.Errorf("serving cert %s: not yet valid (NotBefore=%s, now=%s)", servingCertPath, leaf.NotBefore, now)
	}
	if now.After(leaf.NotAfter) {
		return fmt.Errorf("serving cert %s: expired (NotAfter=%s, now=%s)", servingCertPath, leaf.NotAfter, now)
	}

	caCertPath := filepath.Join(s.certDir, FileCACert)
	caCert, caErr := os.ReadFile(caCertPath) //nolint:gosec // path is operator-controlled CertDir joined with a constant filename
	switch {
	case errors.Is(caErr, os.ErrNotExist):
		// cert-manager Issuers that delegate to an external CA may
		// not write ca.crt; the apiserver gets the bundle from the
		// MWC injection annotation instead.
		return nil
	case caErr != nil:
		return fmt.Errorf("read %s: %w", caCertPath, caErr)
	}
	if _, parseErr := decodeLeafCertPEM(caCert); parseErr != nil {
		return fmt.Errorf("parse %s: %w", caCertPath, parseErr)
	}
	return nil
}
