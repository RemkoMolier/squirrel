package certs_test

import (
	"context"
	"strings"
	"testing"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	runtime "k8s.io/apimachinery/pkg/runtime"
	client "sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/RemkoMolier/squirrel/internal/certs"
)

const testMWCName = "squirrel-image-rewrite"

// newMWCTestClient builds a fake client with the
// admissionregistration scheme and the supplied objects pre-installed.
func newMWCTestClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := admissionregistrationv1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	return fakeclient.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		Build()
}

// mwcWithBundles builds a MutatingWebhookConfiguration named
// testMWCName, carrying the given webhook caBundle values. The
// first entry is always named the design's squirrel webhook
// (mutate-pods.squirrel.molier.dev) so PatchMWCaBundle's
// name-scoped patch finds it. Additional entries get synthetic
// names to model a shared MWC with unrelated webhooks; the patcher
// must leave those entries' caBundles untouched.
func mwcWithBundles(bundles ...[]byte) *admissionregistrationv1.MutatingWebhookConfiguration {
	whs := make([]admissionregistrationv1.MutatingWebhook, len(bundles))
	for i, b := range bundles {
		name := "mutate-pods.squirrel.molier.dev"
		if i > 0 {
			name = "wh" + string(rune('a'+i)) + ".example.com"
		}
		whs[i] = admissionregistrationv1.MutatingWebhook{
			Name: name,
			ClientConfig: admissionregistrationv1.WebhookClientConfig{
				CABundle: b,
			},
		}
	}
	return &admissionregistrationv1.MutatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: testMWCName},
		Webhooks:   whs,
	}
}

func TestPatchMWCaBundleUpdatesEmptyBundle(t *testing.T) {
	t.Parallel()

	cli := newMWCTestClient(t, mwcWithBundles(nil))
	want := []byte("fake-pem")

	if err := certs.PatchMWCaBundle(context.Background(), cli, testMWCName, want); err != nil {
		t.Fatalf("PatchMWCaBundle: %v", err)
	}

	var got admissionregistrationv1.MutatingWebhookConfiguration
	if err := cli.Get(context.Background(), client.ObjectKey{Name: testMWCName}, &got); err != nil {
		t.Fatalf("Get after PatchMWCaBundle: %v", err)
	}
	if g := got.Webhooks[0].ClientConfig.CABundle; string(g) != string(want) {
		t.Errorf("CABundle: got %q, want %q", g, want)
	}
}

func TestPatchMWCaBundleOverwritesStaleBundle(t *testing.T) {
	t.Parallel()

	cli := newMWCTestClient(t, mwcWithBundles([]byte("old-pem")))
	want := []byte("new-pem")

	if err := certs.PatchMWCaBundle(context.Background(), cli, testMWCName, want); err != nil {
		t.Fatalf("PatchMWCaBundle: %v", err)
	}

	var got admissionregistrationv1.MutatingWebhookConfiguration
	if err := cli.Get(context.Background(), client.ObjectKey{Name: testMWCName}, &got); err != nil {
		t.Fatalf("Get after PatchMWCaBundle: %v", err)
	}
	if g := got.Webhooks[0].ClientConfig.CABundle; string(g) != string(want) {
		t.Errorf("CABundle: got %q, want %q", g, want)
	}
}

// TestPatchMWCaBundleIsIdempotent pins the no-op contract: when the
// MWC already carries the requested bytes the function must not issue
// an Update. We verify by checking resourceVersion - the fake client
// bumps it on every Update.
func TestPatchMWCaBundleIsIdempotent(t *testing.T) {
	t.Parallel()

	want := []byte("fake-pem")
	cli := newMWCTestClient(t, mwcWithBundles(want))

	var before admissionregistrationv1.MutatingWebhookConfiguration
	if err := cli.Get(context.Background(), client.ObjectKey{Name: testMWCName}, &before); err != nil {
		t.Fatalf("Get before PatchMWCaBundle: %v", err)
	}

	if err := certs.PatchMWCaBundle(context.Background(), cli, testMWCName, want); err != nil {
		t.Fatalf("PatchMWCaBundle: %v", err)
	}

	var after admissionregistrationv1.MutatingWebhookConfiguration
	if err := cli.Get(context.Background(), client.ObjectKey{Name: testMWCName}, &after); err != nil {
		t.Fatalf("Get after PatchMWCaBundle: %v", err)
	}
	if before.ResourceVersion != after.ResourceVersion {
		t.Errorf("ResourceVersion bumped on a no-op Patch; got before=%q after=%q",
			before.ResourceVersion, after.ResourceVersion)
	}
}

// TestPatchMWCaBundleLeavesUnrelatedEntriesUntouched pins the
// name-scoped contract: when the MWC carries multiple webhook
// entries, only the squirrel entry (mutate-pods.squirrel.molier.dev)
// is rewritten. Entries from co-tenanted webhooks must keep their
// caBundle unchanged so the patcher cannot corrupt other operators'
// TLS material if it ever shares an MWC.
func TestPatchMWCaBundleLeavesUnrelatedEntriesUntouched(t *testing.T) {
	t.Parallel()

	// Three entries: index 0 is the squirrel webhook (stale), 1 and
	// 2 are unrelated webhooks with their own pre-existing bundles.
	want := []byte("fake-pem")
	other := []byte("other-operator-pem")
	stale := []byte("old-pem")
	cli := newMWCTestClient(t, mwcWithBundles(stale, other, other))

	if err := certs.PatchMWCaBundle(context.Background(), cli, testMWCName, want); err != nil {
		t.Fatalf("PatchMWCaBundle: %v", err)
	}

	var got admissionregistrationv1.MutatingWebhookConfiguration
	if err := cli.Get(context.Background(), client.ObjectKey{Name: testMWCName}, &got); err != nil {
		t.Fatalf("Get after PatchMWCaBundle: %v", err)
	}
	if string(got.Webhooks[0].ClientConfig.CABundle) != string(want) {
		t.Errorf("Webhooks[0].CABundle (squirrel): got %q, want %q", got.Webhooks[0].ClientConfig.CABundle, want)
	}
	for i := 1; i < len(got.Webhooks); i++ {
		if string(got.Webhooks[i].ClientConfig.CABundle) != string(other) {
			t.Errorf("Webhooks[%d].CABundle (unrelated): got %q, want %q (must not be touched)", i, got.Webhooks[i].ClientConfig.CABundle, other)
		}
	}
}

func TestPatchMWCaBundleReturnsErrorWhenMWCMissing(t *testing.T) {
	t.Parallel()

	cli := newMWCTestClient(t)
	err := certs.PatchMWCaBundle(context.Background(), cli, "does-not-exist", []byte("fake-pem"))
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !apierrors.IsNotFound(err) {
		t.Errorf("expected NotFound error, got %v", err)
	}
}

// TestPatchMWCaBundleErrorsOnMWCMissingSquirrelEntry covers the
// manifest-shape error: an MWC that does not carry a webhook entry
// named "mutate-pods.squirrel.molier.dev" is structurally wrong for
// squirrel's deployment, so the patcher returns an error instead of
// silently issuing a no-op Update. (The previous "no webhooks at all
// is a no-op success" behaviour was changed when PatchMWCaBundle
// became name-scoped to defend against shared-MWC corruption.)
func TestPatchMWCaBundleErrorsOnMWCMissingSquirrelEntry(t *testing.T) {
	t.Parallel()

	cli := newMWCTestClient(t, mwcWithBundles())

	var before admissionregistrationv1.MutatingWebhookConfiguration
	if err := cli.Get(context.Background(), client.ObjectKey{Name: testMWCName}, &before); err != nil {
		t.Fatalf("Get: %v", err)
	}

	if err := certs.PatchMWCaBundle(context.Background(), cli, testMWCName, []byte("fake-pem")); err == nil {
		t.Fatal("expected error, got nil (an MWC without the squirrel entry must fail loudly)")
	}

	var after admissionregistrationv1.MutatingWebhookConfiguration
	if err := cli.Get(context.Background(), client.ObjectKey{Name: testMWCName}, &after); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if before.ResourceVersion != after.ResourceVersion {
		t.Errorf("ResourceVersion bumped on a no-op Patch against an empty Webhooks slice")
	}
}

// TestPatchMWCaBundleRefusesCertManagerManagedMWC pins the
// security guard: when the MWC carries cert-manager.io/inject-ca-from,
// cert-manager owns the caBundle and a self-signed-mode patch
// would silently fight cert-manager's writes. Returning an error
// surfaces the misconfiguration (operator running in self-signed
// mode against a cert-manager-wired manifest) at startup instead.
func TestPatchMWCaBundleRefusesCertManagerManagedMWC(t *testing.T) {
	t.Parallel()

	mwc := mwcWithBundles(nil)
	mwc.Annotations = map[string]string{
		certs.CertManagerInjectAnnotation: "squirrel-system/squirrel-webhook-tls",
	}
	cli := newMWCTestClient(t, mwc)

	err := certs.PatchMWCaBundle(context.Background(), cli, testMWCName, []byte("fake-pem"))
	if err == nil {
		t.Fatal("expected error rejecting a cert-manager-managed MWC, got nil")
	}
	if !strings.Contains(err.Error(), certs.CertManagerInjectAnnotation) {
		t.Errorf("error %q does not mention the cert-manager.io/inject-ca-from annotation", err)
	}
}

func TestPatchMWCaBundleRejectsBadArguments(t *testing.T) {
	t.Parallel()

	cli := newMWCTestClient(t)
	tests := []struct {
		name     string
		mwcName  string
		caBundle []byte
	}{
		{name: "empty mwc name", mwcName: "", caBundle: []byte("x")},
		{name: "nil ca bundle", mwcName: testMWCName, caBundle: nil},
		{name: "empty ca bundle", mwcName: testMWCName, caBundle: []byte{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if err := certs.PatchMWCaBundle(context.Background(), cli, tt.mwcName, tt.caBundle); err == nil {
				t.Errorf("expected error, got nil")
			}
		})
	}
}
