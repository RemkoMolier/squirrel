package certs

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	client "sigs.k8s.io/controller-runtime/pkg/client"
)

// Standard data-key names inside the webhook-cert Secret.
//
// The `tls.crt`/`tls.key` pair follows the kubernetes.io/tls
// convention so the webhook server's TLS material is the value the
// Pod-spec volume projection mounts at the conventional paths.
// `ca.crt` and `ca.key` extend the shape to carry the CA material
// the self-signed source needs to detect "already up to date" and
// to re-issue the serving certificate on rotation without regenerating
// the trust anchor. The CA private key is the reason the Secret is
// Opaque rather than kubernetes.io/tls (that type rejects fields
// outside the standard pair).
const (
	SecretCACertKey      = "ca.crt"
	SecretCAKeyKey       = "ca.key"
	SecretServingCertKey = "tls.crt"
	SecretServingKeyKey  = "tls.key"

	// SecretPreviousCACertKey holds the CA certificate from the most
	// recent rotation. During the CA-rotation transition window the
	// MWC.caBundle carries both the new CA (ca.crt) and the previous
	// one (previous-ca.crt) so follower replicas - which still serve
	// the old chain on disk until their LocalSync tick reads the new
	// Secret - keep handshaking successfully. After the transition
	// window expires the next Authoritative tick drops this key and
	// re-patches the MWC down to a single CA. Absent in steady state.
	SecretPreviousCACertKey = "previous-ca.crt"

	// caTrustWindowAnnotation records the deadline (RFC 3339) past
	// which previous-ca.crt is no longer needed; the next leader tick
	// after this instant trims the trust window.
	caTrustWindowAnnotation = "squirrel.molier.dev/ca-trust-window-until"
)

// Default validity and rotation values applied when EnsureOpts leaves
// the corresponding field at its zero value. These match the design's
// numbers in docs/design/v1alpha1.md.
const (
	defaultCAValidity        = 5 * 365 * 24 * time.Hour
	defaultServingValidity   = 365 * 24 * time.Hour
	defaultRotationThreshold = 30 * 24 * time.Hour

	// defaultCATrustWindow bounds how long after a CA rotation the
	// MWC.caBundle keeps trusting the previous CA. Sized to comfortably
	// cover the default LocalSync interval (one hour) plus a margin for
	// followers that briefly missed a tick due to apiserver flakiness.
	defaultCATrustWindow = 2 * time.Hour
)

// Bundle is the parsed CA + serving material from a webhook-cert
// Secret. The SelfSignedSource returns this from every Ensure call so
// callers (the MWC patcher, the on-disk writer) work off a single
// snapshot of the material.
//
// PreviousCA and PreviousTrustUntil carry the CA-rotation transition
// state. They are nil/zero in steady state. They are set immediately
// after a CA rotation so MWC.caBundle can briefly trust both the new
// and the previous CA - long enough for every follower replica to
// have ticked its LocalSyncRunnable and pulled the new serving cert
// onto disk, eliminating the TLS-staleness window described in the
// fourth-round review.
type Bundle struct {
	CA      *CA
	Serving *ServingCert

	// PreviousCACertPEM is the prior CA's certificate in PEM form,
	// kept in MWC.caBundle until PreviousTrustUntil so follower
	// replicas have a window to localize the new serving cert before
	// the apiserver stops trusting their currently-served chain. Nil
	// in steady state. Only the cert is preserved (not the private
	// key); the prior CA never signs again - it only validates
	// in-flight handshakes during the transition.
	PreviousCACertPEM []byte

	// PreviousTrustUntil is the deadline past which PreviousCACertPEM
	// can be dropped from the MWC trust bundle. Zero when
	// PreviousCACertPEM is nil.
	PreviousTrustUntil time.Time
}

// TrustBundlePEM returns the bytes to publish in MWC.caBundle. In
// steady state this is just the current CA cert in PEM form. During
// the CA-rotation transition window, the previous CA cert is
// concatenated so the apiserver trusts both chains while followers
// converge on the new serving cert.
func (b *Bundle) TrustBundlePEM() []byte {
	if b == nil || b.CA == nil {
		return nil
	}
	current := b.CA.CertPEM()
	if len(b.PreviousCACertPEM) == 0 {
		return current
	}
	out := make([]byte, 0, len(current)+len(b.PreviousCACertPEM))
	out = append(out, current...)
	out = append(out, b.PreviousCACertPEM...)
	return out
}

// EnsureOpts configures an Ensure call.
//
// SecretKey, CommonName, and DNSNames are required. The validity and
// rotation values, and the clock, fall back to design defaults when
// zero so production code does not have to spell them out.
type EnsureOpts struct {
	// SecretKey is the namespaced name of the webhook-cert Secret.
	SecretKey client.ObjectKey

	// CommonName is the Subject.CommonName the CA is created with.
	CommonName string

	// DNSNames are the SubjectAltNames the serving certificate is
	// issued for. At least one is required; typical values are the
	// webhook service's <name>.<namespace>.svc and its
	// .svc.cluster.local FQDN.
	DNSNames []string

	// RotationThreshold makes Ensure re-issue material once an
	// existing certificate is within this duration of its expiry.
	// Zero means "use the package default" (30 days).
	RotationThreshold time.Duration

	// CAValidity is the lifetime of a freshly-minted CA. Zero means
	// "use the package default" (five years).
	CAValidity time.Duration

	// ServingValidity is the lifetime of a freshly-minted serving
	// cert. Zero means "use the package default" (one year).
	ServingValidity time.Duration

	// CATrustWindow is how long after a CA rotation the previous
	// CA stays in MWC.caBundle so follower replicas can finish
	// pulling the new serving cert onto disk before the apiserver
	// stops trusting their old chain. Zero means "use the package
	// default" (two hours, well above the one-hour LocalSync
	// cadence).
	CATrustWindow time.Duration

	// Now is the clock the function reads to compute validity windows
	// and to decide on rotation. Nil means time.Now; tests inject a
	// fixed instant.
	Now func() time.Time
}

// Ensure reads the named Secret, validates its material, and rewrites
// the Secret in place when the material is missing, unparseable, or
// within RotationThreshold of expiry. The returned Bundle is the
// up-to-date material the caller can hand to the MWC patcher and the
// on-disk writer.
//
// Concurrent-write semantics: two replicas racing on the initial
// Create both return a valid Bundle. The loser observes
// AlreadyExists, re-reads the Secret, and uses the winner's material.
// Concurrent rotation writes are race-resistant via the Secret's
// resourceVersion (the K8s apiserver rejects a stale update); the
// caller is expected to retry under the controller-runtime reconcile
// loop, so this function does not retry internally.
func Ensure(ctx context.Context, c client.Client, opts EnsureOpts) (*Bundle, error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	now := opts.now()

	var sec corev1.Secret
	getErr := c.Get(ctx, opts.SecretKey, &sec)
	switch {
	case apierrors.IsNotFound(getErr):
		return createFreshSecret(ctx, c, opts, now)
	case getErr != nil:
		return nil, fmt.Errorf("get Secret %q: %w", opts.SecretKey, getErr)
	}

	existing, parseErr := parseSecret(&sec)
	switch {
	case parseErr != nil:
		// Material is corrupt or missing a field. Re-mint everything;
		// the Update keeps the same resourceVersion guard a concurrent
		// rotation would need.
		return rewriteSecret(ctx, c, &sec, opts, now, nil)
	case !bundleUsableForOpts(existing, opts):
		// The bundle parses but cannot be used against the current
		// configuration: the serving cert is signed by a different
		// CA, or its SANs no longer cover the requested DNSNames
		// (e.g. a Secret restored from a different install or a
		// service-DNS change between deploys). Without this check the
		// MWC patcher would publish a CA that cannot verify the
		// presented serving cert. Re-mint everything; the MWC then
		// gets the new bundle.
		return rewriteSecret(ctx, c, &sec, opts, now, nil)
	case NeedsRotation(existing.CA.Cert(), opts.rotationThreshold(), now):
		// The CA itself is within the rotation window. Mint a fresh
		// CA + serving pair; the MWC patcher then re-publishes the
		// new bundle.
		return rewriteSecret(ctx, c, &sec, opts, now, nil)
	case NeedsRotation(existing.Serving.Cert(), opts.rotationThreshold(), now):
		// Only the serving cert is expiring. Re-use the existing CA
		// so the apiserver does not need a new bundle.
		return rewriteSecret(ctx, c, &sec, opts, now, existing.CA)
	case existing.PreviousCACertPEM != nil && !existing.PreviousTrustUntil.IsZero() && !now.Before(existing.PreviousTrustUntil):
		// CA-rotation trust window has expired. The previous CA is no
		// longer needed in MWC.caBundle; drop it from the Secret so
		// the next PatchMWCaBundle call narrows the trust set to the
		// new CA alone. We Update the existing Secret in place rather
		// than re-mint anything; only the previous-ca.crt key and the
		// trust-window annotation change.
		return trimTrustWindow(ctx, c, &sec, existing)
	default:
		return existing, nil
	}
}

// bundleUsableForOpts reports whether the parsed bundle is still
// fit for purpose against opts:
//
//   - The serving certificate must chain to the CA stored alongside
//     it. A mismatch (e.g. ca.key replaced without re-issuing
//     tls.crt, or a Secret restored from a different install) would
//     leave the MWC patcher publishing a CA the serving cert cannot
//     be verified against, breaking the webhook handshake.
//   - The serving certificate's SubjectAltNames must exactly match
//     opts.DNSNames. A drift (e.g. the operator's manifest added a
//     new service FQDN between deploys) means the apiserver's
//     dial-by-hostname would fail with a "certificate doesn't match
//     host" error; rotating the serving cert is the right response.
//
// The check is intentionally strict: any divergence - including
// a pure superset where the existing cert covers every requested
// SAN plus extras - triggers rotation rather than silently letting
// the existing material through. The trade-off is a brief CA
// re-mint window on every operator-edited DNSNames change (added or
// removed), in exchange for never carrying a stale principal-set on
// the serving certificate. The CA reuse path is taken inside
// rewriteSecret when the divergence is SAN-only and the existing CA
// is still in-window, so the rotation cost is one new serving cert,
// not a full CA refresh.
func bundleUsableForOpts(b *Bundle, opts EnsureOpts) bool {
	if b == nil || b.CA == nil || b.Serving == nil {
		return false
	}
	if !sansEqual(b.Serving.Cert().DNSNames, opts.DNSNames) {
		return false
	}
	pool := x509.NewCertPool()
	pool.AddCert(b.CA.Cert())
	// Use a time inside the cert's own validity window so the
	// signature check does not double-count expiry; expiry is the
	// NeedsRotation gate's job.
	_, err := b.Serving.Cert().Verify(x509.VerifyOptions{
		Roots:       pool,
		KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		CurrentTime: b.Serving.Cert().NotBefore.Add(time.Second),
	})
	return err == nil
}

// sansEqual reports whether got and want carry the same set of DNS
// names (order does not matter; both slices are defensively copied
// before sorting so neither caller observes a mutation).
func sansEqual(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	a := append([]string(nil), got...)
	b := append([]string(nil), want...)
	sort.Strings(a)
	sort.Strings(b)
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// createFreshSecret mints both a CA and a serving cert and Creates the
// Secret. AlreadyExists means a concurrent replica won the race; in
// that case the function re-reads the Secret and returns the winner's
// material instead of overwriting it.
func createFreshSecret(
	ctx context.Context,
	c client.Client,
	opts EnsureOpts,
	now time.Time,
) (*Bundle, error) {
	bundle, data, err := mintBundleData(opts, now, nil)
	if err != nil {
		return nil, err
	}
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      opts.SecretKey.Name,
			Namespace: opts.SecretKey.Namespace,
		},
		Type: corev1.SecretTypeOpaque,
		Data: data,
	}
	if err := c.Create(ctx, sec); err != nil {
		if apierrors.IsAlreadyExists(err) {
			// Another replica got there first. Use what they wrote.
			var winner corev1.Secret
			if getErr := c.Get(ctx, opts.SecretKey, &winner); getErr != nil {
				return nil, fmt.Errorf("re-read Secret %q after AlreadyExists: %w", opts.SecretKey, getErr)
			}
			parsed, parseErr := parseSecret(&winner)
			if parseErr != nil {
				return nil, fmt.Errorf("parse Secret %q written by concurrent replica: %w", opts.SecretKey, parseErr)
			}
			return parsed, nil
		}
		return nil, fmt.Errorf("create Secret %q: %w", opts.SecretKey, err)
	}
	return bundle, nil
}

// rewriteSecret mints fresh material (re-using reuseCA when non-nil
// so a serving-only rotation does not bump the trust anchor) and
// Updates the Secret in place. There is no internal retry on
// resourceVersion Conflict: periodic rotation runs only on the
// elected leader (see certs.AuthoritativeRunnable) so concurrent
// replicas never race. A Conflict from an external concurrent
// writer (e.g. an admin editing the Secret) bubbles up to the
// runnable, which retries on its next tick.
//
// A NotFound on Update means the Secret was deleted between
// certs.Ensure's Get and our Update - rare (an admin or operator
// kubectl deletion) but possible. We fall through to
// createFreshSecret rather than bubbling the error so the rotation
// converges in this tick rather than waiting one full interval for
// the next tick's Get-NotFound -> Create path.
func rewriteSecret(
	ctx context.Context,
	c client.Client,
	sec *corev1.Secret,
	opts EnsureOpts,
	now time.Time,
	reuseCA *CA,
) (*Bundle, error) {
	bundle, data, err := mintBundleData(opts, now, reuseCA)
	if err != nil {
		return nil, err
	}
	// CA-rotation transition. reuseCA==nil means we are minting a
	// fresh CA (full rotation), not just a new serving cert. The old
	// CA - still in sec.Data[SecretCACertKey] until the upcoming
	// overwrite - must remain in MWC.caBundle for opts.caTrustWindow()
	// so that follower replicas still serving the old chain on disk
	// keep handshaking until their LocalSync tick pulls the new
	// serving cert in. We preserve it in the Secret under
	// SecretPreviousCACertKey and stamp the deadline as an annotation;
	// SelfSignedSource.Authoritative reads both back to assemble the
	// trust bundle in TrustBundlePEM. On a serving-only rotation
	// (reuseCA != nil) the CA is unchanged and no transition state
	// is needed; we drop any leftover transition state from a prior
	// rotation in the same step.
	if reuseCA == nil {
		if oldCACert, ok := sec.Data[SecretCACertKey]; ok && len(oldCACert) > 0 {
			data[SecretPreviousCACertKey] = append([]byte(nil), oldCACert...)
			until := now.Add(opts.caTrustWindow())
			bundle.PreviousCACertPEM = data[SecretPreviousCACertKey]
			bundle.PreviousTrustUntil = until
			if sec.Annotations == nil {
				sec.Annotations = map[string]string{}
			}
			sec.Annotations[caTrustWindowAnnotation] = until.Format(time.RFC3339)
		}
	} else {
		// Serving-only rotation drops any stale transition state so
		// the next steady-state Update does not carry it forever.
		delete(sec.Annotations, caTrustWindowAnnotation)
	}
	if sec.Type == "" {
		sec.Type = corev1.SecretTypeOpaque
	}
	sec.Data = data
	updateErr := c.Update(ctx, sec)
	switch {
	case updateErr == nil:
		return bundle, nil
	case apierrors.IsNotFound(updateErr):
		return createFreshSecret(ctx, c, opts, now)
	default:
		return nil, fmt.Errorf("update Secret %q: %w", opts.SecretKey, updateErr)
	}
}

// trimTrustWindow removes the CA-rotation transition state from sec
// and Updates the Secret. Returns the trimmed bundle (no
// PreviousCACertPEM, zero PreviousTrustUntil) so the caller can
// re-emit MWC.caBundle as the single new CA.
func trimTrustWindow(
	ctx context.Context,
	c client.Client,
	sec *corev1.Secret,
	existing *Bundle,
) (*Bundle, error) {
	delete(sec.Data, SecretPreviousCACertKey)
	delete(sec.Annotations, caTrustWindowAnnotation)
	if err := c.Update(ctx, sec); err != nil {
		// NotFound here would mean the Secret was deleted out from
		// under us; the next Authoritative tick's Get-NotFound path
		// will mint a fresh Secret. No transition state to preserve.
		return nil, fmt.Errorf("update Secret %q to trim CA trust window: %w", sec.Name, err)
	}
	trimmed := *existing
	trimmed.PreviousCACertPEM = nil
	trimmed.PreviousTrustUntil = time.Time{}
	return &trimmed, nil
}

// mintBundleData mints a fresh Bundle (re-using reuseCA when non-nil)
// and returns it alongside the corresponding Secret data map.
//
// The serving certificate's NotAfter is capped at the CA's NotAfter
// so a serving-only rotation against an aging CA never produces a
// leaf that outlives its issuer. Without this cap, a CA with
// (RotationThreshold < remaining < ServingValidity) of life left
// would mint a serving cert past ca.NotAfter, which
// verifyBundleWindow then rejects - the persisted Secret would be
// poisoned until the CA itself finally enters its rotation window
// and the full re-mint path runs.
func mintBundleData(opts EnsureOpts, now time.Time, reuseCA *CA) (*Bundle, map[string][]byte, error) {
	// Back-date NotBefore to absorb the typical clock skew between
	// the operator's node and the apiserver / clients that will
	// verify the handshake. Without this, an operator clock that
	// runs even a few seconds ahead of the apiserver produces
	// transient "certificate not yet valid" failures during the
	// freshly-minted-bundle window. cert-manager applies the same
	// five-minute back-date for the same reason.
	const clockSkewBackdate = 5 * time.Minute
	notBefore := now.Add(-clockSkewBackdate)

	ca := reuseCA
	if ca == nil {
		fresh, err := NewSelfSignedCA(opts.CommonName, notBefore, now.Add(opts.caValidity()))
		if err != nil {
			return nil, nil, fmt.Errorf("mint CA: %w", err)
		}
		ca = fresh
	}
	servingNotAfter := now.Add(opts.servingValidity())
	if caExp := ca.Cert().NotAfter; servingNotAfter.After(caExp) {
		servingNotAfter = caExp
	}
	serving, err := IssueServingCert(ca, opts.DNSNames, notBefore, servingNotAfter)
	if err != nil {
		return nil, nil, fmt.Errorf("issue serving cert: %w", err)
	}
	data := map[string][]byte{
		SecretCACertKey:      ca.CertPEM(),
		SecretCAKeyKey:       ca.KeyPEM(),
		SecretServingCertKey: serving.CertPEM(),
		SecretServingKeyKey:  serving.KeyPEM(),
	}
	return &Bundle{CA: ca, Serving: serving}, data, nil
}

// parseSecret decodes the four PEM data fields into a Bundle. Returns
// an error if any field is missing or fails to parse; the caller
// regenerates the Secret in that case.
func parseSecret(sec *corev1.Secret) (*Bundle, error) {
	if sec == nil {
		return nil, errors.New("parse secret: nil")
	}
	caCert, ok := sec.Data[SecretCACertKey]
	if !ok {
		return nil, fmt.Errorf("parse secret: missing %q", SecretCACertKey)
	}
	caKey, ok := sec.Data[SecretCAKeyKey]
	if !ok {
		return nil, fmt.Errorf("parse secret: missing %q", SecretCAKeyKey)
	}
	servingCert, ok := sec.Data[SecretServingCertKey]
	if !ok {
		return nil, fmt.Errorf("parse secret: missing %q", SecretServingCertKey)
	}
	servingKey, ok := sec.Data[SecretServingKeyKey]
	if !ok {
		return nil, fmt.Errorf("parse secret: missing %q", SecretServingKeyKey)
	}
	ca, err := ParseCA(caCert, caKey)
	if err != nil {
		return nil, fmt.Errorf("parse secret CA: %w", err)
	}
	serving, err := ParseServingCert(servingCert, servingKey)
	if err != nil {
		return nil, fmt.Errorf("parse secret serving: %w", err)
	}
	b := &Bundle{CA: ca, Serving: serving}
	// Optional CA-rotation transition state. A malformed previous-CA
	// cert or unparseable timestamp is treated as "no transition state"
	// rather than as a hard failure: the worst-case behaviour is that
	// the bundle gets re-emitted without the trust window, which is
	// the design's failure-open posture for a defence-in-depth feature.
	if prevPEM, ok := sec.Data[SecretPreviousCACertKey]; ok && len(prevPEM) > 0 {
		// Only validate that the bytes parse as a certificate; we
		// never need the private key (the previous CA never signs
		// anything new). decodeCertPEM is the same primitive parseCA
		// uses, just without the key requirement.
		if _, certErr := decodeCertPEM(prevPEM); certErr == nil {
			b.PreviousCACertPEM = append([]byte(nil), prevPEM...)
		}
	}
	if v, ok := sec.Annotations[caTrustWindowAnnotation]; ok {
		if t, tErr := time.Parse(time.RFC3339, v); tErr == nil {
			b.PreviousTrustUntil = t
		}
	}
	return b, nil
}

// validate fails fast on the EnsureOpts fields that have no sensible
// default.
func (o *EnsureOpts) validate() error {
	if o.SecretKey.Name == "" {
		return errors.New("EnsureOpts.SecretKey.Name must not be empty")
	}
	if o.SecretKey.Namespace == "" {
		// A namespace-less ObjectKey defaults to "" through
		// controller-runtime, which some apiserver code paths will
		// then resolve to "default" - almost never what an operator
		// intends for a webhook-cert Secret. Reject at the helper
		// boundary so the misconfiguration is impossible to express.
		return errors.New("EnsureOpts.SecretKey.Namespace must not be empty")
	}
	if o.CommonName == "" {
		return errors.New("EnsureOpts.CommonName must not be empty")
	}
	if len(o.DNSNames) == 0 {
		return errors.New("EnsureOpts.DNSNames must not be empty")
	}
	return nil
}

func (o *EnsureOpts) now() time.Time {
	if o.Now == nil {
		return time.Now()
	}
	return o.Now()
}

func (o *EnsureOpts) rotationThreshold() time.Duration {
	if o.RotationThreshold == 0 {
		return defaultRotationThreshold
	}
	return o.RotationThreshold
}

func (o *EnsureOpts) caValidity() time.Duration {
	if o.CAValidity == 0 {
		return defaultCAValidity
	}
	return o.CAValidity
}

func (o *EnsureOpts) servingValidity() time.Duration {
	if o.ServingValidity == 0 {
		return defaultServingValidity
	}
	return o.ServingValidity
}

func (o *EnsureOpts) caTrustWindow() time.Duration {
	if o.CATrustWindow == 0 {
		return defaultCATrustWindow
	}
	return o.CATrustWindow
}
