package certs

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	client "sigs.k8s.io/controller-runtime/pkg/client"
)

// CertManagerInjectAnnotation is the annotation cert-manager looks
// for on a MutatingWebhookConfiguration to drive its CA bundle
// injection. The squirrel SelfSignedSource refuses to overwrite a
// caBundle when this annotation is present so a misconfiguration
// (self-signed mode active while cert-manager owns the MWC) fails
// loudly instead of silently overwriting cert-manager's writes.
const CertManagerInjectAnnotation = "cert-manager.io/inject-ca-from"

// PatchMWCaBundle reads the named MutatingWebhookConfiguration and
// rewrites the squirrel webhook entry's clientConfig.caBundle to
// caBundle. The Update is idempotent: when the squirrel entry
// already carries the requested bytes the function returns nil
// without issuing an API call.
//
// Why "the squirrel entry" rather than "every entry": squirrel
// today registers a single webhook in its own MWC, but the patcher
// is deliberately defensive against a future configuration where
// the squirrel webhook coexists with unrelated entries in a shared
// MWC. The squirrelWebhookName const captures the design's webhook
// name; the patcher matches by that name and leaves other entries
// untouched.
//
// Why the cert-manager guard: when the MWC carries
// cert-manager.io/inject-ca-from, cert-manager owns the caBundle.
// Returning an error here surfaces the misconfiguration (operator
// running in self-signed mode while the manifest is wired for
// cert-manager) instead of silently fighting cert-manager's writes.
//
// Concurrent-replica race: none. Periodic calls to this function
// run only on the elected leader (see certs.AuthoritativeRunnable)
// and the bootstrap call happens once per replica before mgr.Start.
// A resourceVersion Conflict from an external concurrent writer
// (e.g. an admin editing the MWC) bubbles up to the runnable, which
// retries on its next tick - no internal retry loop is needed.
func PatchMWCaBundle(ctx context.Context, c client.Client, mwcName string, caBundle []byte) error {
	if mwcName == "" {
		return errors.New("PatchMWCaBundle: mwcName must not be empty")
	}
	if len(caBundle) == 0 {
		return errors.New("PatchMWCaBundle: caBundle must not be empty")
	}

	var mwc admissionregistrationv1.MutatingWebhookConfiguration
	if err := c.Get(ctx, client.ObjectKey{Name: mwcName}, &mwc); err != nil {
		return fmt.Errorf("get MutatingWebhookConfiguration %q: %w", mwcName, err)
	}

	if _, ok := mwc.Annotations[CertManagerInjectAnnotation]; ok {
		return fmt.Errorf("MutatingWebhookConfiguration %q carries %s; cert-manager owns the caBundle - the operator must not run in self-signed mode against a cert-manager-managed MWC",
			mwcName, CertManagerInjectAnnotation)
	}

	changed, found := setSquirrelCABundle(&mwc, caBundle)
	if !found {
		return fmt.Errorf("MutatingWebhookConfiguration %q has no webhook entry named %q",
			mwcName, squirrelWebhookName)
	}
	if !changed {
		return nil
	}

	if err := c.Update(ctx, &mwc); err != nil {
		return fmt.Errorf("update MutatingWebhookConfiguration %q: %w", mwcName, err)
	}
	return nil
}

// squirrelWebhookName matches the webhook entry's `name` produced by
// the +kubebuilder:webhook marker in internal/webhook/doc.go. The
// patcher rewrites only this entry; other entries in a shared MWC
// are left untouched.
const squirrelWebhookName = "mutate-pods.squirrel.molier.dev"

// setSquirrelCABundle locates the squirrel webhook entry by name and
// overwrites its caBundle. Returns (changed, found). changed is true
// when the entry's existing caBundle differed from the requested
// bytes; found is false when no entry named squirrelWebhookName
// exists, which is a manifest-shape error the caller surfaces.
func setSquirrelCABundle(mwc *admissionregistrationv1.MutatingWebhookConfiguration, caBundle []byte) (changed, found bool) {
	for i := range mwc.Webhooks {
		if mwc.Webhooks[i].Name != squirrelWebhookName {
			continue
		}
		found = true
		if !bytes.Equal(mwc.Webhooks[i].ClientConfig.CABundle, caBundle) {
			mwc.Webhooks[i].ClientConfig.CABundle = append([]byte(nil), caBundle...)
			changed = true
		}
		// Single squirrel entry per MWC; no need to keep walking.
		break
	}
	return changed, found
}
