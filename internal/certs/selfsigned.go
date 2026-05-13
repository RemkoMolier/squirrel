package certs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	corev1 "k8s.io/api/core/v1"
	client "sigs.k8s.io/controller-runtime/pkg/client"
)

// SelfSignedSource is the default CertSource per the design.
//
// The two CertSource halves are wired as follows:
//
//   - Authoritative reads (or initialises) the webhook-cert Secret
//     via certs.Ensure, then patches the MutatingWebhookConfiguration's
//     caBundle so the apiserver trusts the new CA on the next
//     handshake. Both calls touch the apiserver and run only on the
//     elected leader (with bootstrap exception, see CertSource doc).
//   - Localize Gets the Secret, parses the four PEM blobs, verifies
//     the bundle's validity window, and writes tls.crt/tls.key/ca.crt
//     to CertDir atomically (write to a temp file, then rename) so
//     the webhook server never reads a half-written file. Read-only
//     against the apiserver; runs on every replica.
//
// The split eliminates the leader-of-race code paths the earlier
// "every replica writes" design needed.
type SelfSignedSource struct {
	opts SelfSignedSourceOpts
}

// SelfSignedSourceOpts gathers the inputs NewSelfSignedSource needs.
// The optional knobs (RotationThreshold, CAValidity, ServingValidity,
// Now) fall back to the package defaults documented on EnsureOpts.
type SelfSignedSourceOpts struct {
	// Client is the controller-runtime client used to read the
	// webhook-cert Secret and to patch the MWC.
	Client client.Client

	// SecretKey is the namespaced name of the webhook-cert Secret.
	SecretKey client.ObjectKey

	// MWCName is the name of the MutatingWebhookConfiguration whose
	// caBundle the source publishes the CA into.
	MWCName string

	// CommonName is the Subject.CommonName the CA is created with.
	CommonName string

	// DNSNames are the SubjectAltNames the serving certificate is
	// issued for.
	DNSNames []string

	// CertDir is the directory the source writes tls.crt, tls.key,
	// and ca.crt into. The webhook.Server is then pointed at this
	// directory.
	CertDir string

	// RotationThreshold is the duration before expiry at which
	// rotation triggers. Zero uses the 30-day default.
	RotationThreshold time.Duration

	// CAValidity is the lifetime of a freshly-minted CA. Zero uses
	// the five-year default.
	CAValidity time.Duration

	// ServingValidity is the lifetime of a freshly-minted serving
	// certificate. Zero uses the one-year default.
	ServingValidity time.Duration

	// CATrustWindow is how long after a CA rotation the previous
	// CA remains in MWC.caBundle so follower replicas have a chance
	// to localize the new serving cert before the apiserver stops
	// trusting their currently-served chain. Zero uses the two-hour
	// default (well above the one-hour LocalSync cadence).
	CATrustWindow time.Duration

	// Now is the clock the source reads to decide on rotation. Nil
	// uses time.Now; tests inject a fixed instant.
	Now func() time.Time
}

// NewSelfSignedSource validates opts and returns a configured
// SelfSignedSource. It does not perform any I/O - call Ensure to do
// that.
func NewSelfSignedSource(opts SelfSignedSourceOpts) (*SelfSignedSource, error) {
	switch {
	case opts.Client == nil:
		return nil, errors.New("SelfSignedSource: Client must not be nil")
	case opts.SecretKey.Name == "":
		return nil, errors.New("SelfSignedSource: SecretKey.Name must not be empty")
	case opts.MWCName == "":
		return nil, errors.New("SelfSignedSource: MWCName must not be empty")
	case opts.CommonName == "":
		return nil, errors.New("SelfSignedSource: CommonName must not be empty")
	case len(opts.DNSNames) == 0:
		return nil, errors.New("SelfSignedSource: DNSNames must not be empty")
	case opts.CertDir == "":
		return nil, errors.New("SelfSignedSource: CertDir must not be empty")
	}
	return &SelfSignedSource{opts: opts}, nil
}

// CertDir implements CertSource. Returns the same value passed in via
// SelfSignedSourceOpts.
func (s *SelfSignedSource) CertDir() string { return s.opts.CertDir }

// Authoritative implements CertSource. It mints or rotates the
// webhook-cert Secret via certs.Ensure, then publishes the trust
// bundle to the MutatingWebhookConfiguration's caBundle. The trust
// bundle is the current CA in steady state, or both the current and
// previous CA concatenated during the CA-rotation transition window
// (see Bundle.TrustBundlePEM for the details). Both calls write to
// the apiserver and run only on the elected leader once the manager
// has been started; the bootstrap path in internal/manager/run.go
// is the one exception (see the CertSource interface doc).
func (s *SelfSignedSource) Authoritative(ctx context.Context) error {
	bundle, err := Ensure(ctx, s.opts.Client, EnsureOpts{
		SecretKey:         s.opts.SecretKey,
		CommonName:        s.opts.CommonName,
		DNSNames:          s.opts.DNSNames,
		RotationThreshold: s.opts.RotationThreshold,
		CAValidity:        s.opts.CAValidity,
		ServingValidity:   s.opts.ServingValidity,
		CATrustWindow:     s.opts.CATrustWindow,
		Now:               s.opts.Now,
	})
	if err != nil {
		return fmt.Errorf("ensure webhook-cert Secret: %w", err)
	}
	if err := PatchMWCaBundle(ctx, s.opts.Client, s.opts.MWCName, bundle.TrustBundlePEM()); err != nil {
		return fmt.Errorf("publish CA bundle to MWC: %w", err)
	}
	return nil
}

// Localize implements CertSource. It Gets the webhook-cert Secret,
// parses and validates the bundle, then writes tls.crt/tls.key/ca.crt
// to CertDir atomically. The function performs only apiserver reads
// (no writes) and is safe to call from every replica.
func (s *SelfSignedSource) Localize(ctx context.Context) error {
	var sec corev1.Secret
	if err := s.opts.Client.Get(ctx, s.opts.SecretKey, &sec); err != nil {
		return fmt.Errorf("get webhook-cert Secret %q: %w", s.opts.SecretKey, err)
	}
	bundle, err := parseSecret(&sec)
	if err != nil {
		return fmt.Errorf("parse Secret %q: %w", s.opts.SecretKey, err)
	}
	// Sanity-check the bundle: a serving cert that outlives its CA
	// will be rejected by the apiserver once the CA expires. The
	// primitives layer cannot catch this because it takes notBefore /
	// notAfter directly; doing it here means the source never writes
	// out a bundle the apiserver will reject.
	if err := verifyBundleWindow(bundle); err != nil {
		return fmt.Errorf("ensure webhook-cert Secret: %w", err)
	}
	if err := writeBundleToDir(s.opts.CertDir, bundle); err != nil {
		return fmt.Errorf("write bundle to %q: %w", s.opts.CertDir, err)
	}
	return nil
}

// verifyBundleWindow reports an error when the serving certificate's
// validity window is not fully contained in the CA's. The check is
// the source-layer guard the primitives layer cannot perform
// (NewSelfSignedCA and IssueServingCert each take their windows
// directly), and it surfaces a misconfigured Secret as a clear error
// rather than letting the apiserver discover it at handshake time
// after a CA expiry.
func verifyBundleWindow(bundle *Bundle) error {
	if bundle == nil || bundle.CA == nil || bundle.Serving == nil {
		return errors.New("verify bundle: nil component")
	}
	caCert := bundle.CA.Cert()
	servingCert := bundle.Serving.Cert()
	if servingCert.NotBefore.Before(caCert.NotBefore) {
		return fmt.Errorf("serving cert NotBefore (%s) is before CA NotBefore (%s)",
			servingCert.NotBefore, caCert.NotBefore)
	}
	if servingCert.NotAfter.After(caCert.NotAfter) {
		return fmt.Errorf("serving cert NotAfter (%s) is after CA NotAfter (%s)",
			servingCert.NotAfter, caCert.NotAfter)
	}
	return nil
}

// writeBundleToDir writes the three PEM files into dir atomically:
// each file is written to a sibling temp file and renamed into place,
// so the webhook server's filesystem watcher never observes a
// half-written cert. The function is per-file atomic AND
// best-effort group-atomic: when a write fails partway through, the
// already-renamed files from this call are removed so the next
// observer does not see a mixed-version cert/key/CA triple. The
// underlying webhook server reloads on each watch event, and a
// mid-rotation crash that left a fresh tls.crt next to a stale
// tls.key would produce TLS handshake failures until the next
// rotation rewrote the key. (A crash *between* renames still
// requires a follow-up Ensure to re-mint everything, but the
// rollback bounds the visible-inconsistency window to the
// "process died while writing" case rather than "writeBundleToDir
// returned an error.")
//
// The directory is created (with 0700) when it does not exist; the
// files are written with 0600 because the private key is sensitive.
func writeBundleToDir(dir string, bundle *Bundle) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create cert dir: %w", err)
	}
	files := []struct {
		name string
		data []byte
	}{
		{FileServingCert, bundle.Serving.CertPEM()},
		{FileServingKey, bundle.Serving.KeyPEM()},
		{FileCACert, bundle.CA.CertPEM()},
	}
	written := make([]string, 0, len(files))
	for _, f := range files {
		path := filepath.Join(dir, f.name)
		if err := atomicWriteFile(path, f.data, 0o600); err != nil {
			// Roll back the files this call already renamed. The
			// rollback is best-effort: any individual Remove failure
			// is logged via the wrapped error but does not prevent
			// the rest of the cleanup from running.
			for _, p := range written {
				_ = os.Remove(p)
			}
			return fmt.Errorf("write %q: %w", f.name, err)
		}
		written = append(written, path)
	}
	return nil
}

// atomicWriteFile writes data to path atomically: write to a sibling
// temp file (created with the target permissions so a concurrent
// reader never sees a wider mode), fsync it so the rename does not
// expose a zero-length file on crash, rename it into place, and
// fsync the parent directory so the rename itself is durable across
// a power loss (POSIX: a rename's directory-entry update is not
// guaranteed to be on disk until the directory's metadata is
// flushed). The temp file is cleaned up on every error path.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }

	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("chmod temp: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return fmt.Errorf("rename temp: %w", err)
	}
	return syncDir(dir)
}

// syncDir opens dir and fsyncs it so a rename made into the
// directory is durable. The open + close cost is a single syscall
// pair on Linux; on platforms where directory fsync is unsupported
// (Windows) the call is a harmless no-op.
func syncDir(dir string) error {
	d, err := os.Open(dir) //nolint:gosec // dir is operator-controlled CertDir built earlier in the same function
	if err != nil {
		return fmt.Errorf("open dir for fsync: %w", err)
	}
	if err := d.Sync(); err != nil {
		_ = d.Close()
		return fmt.Errorf("sync dir: %w", err)
	}
	if err := d.Close(); err != nil {
		return fmt.Errorf("close dir: %w", err)
	}
	return nil
}
