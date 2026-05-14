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
//   - Secret round-trip (secret.go): load the CA + serving material
//     from a Kubernetes Secret and re-issue when it is missing,
//     unparseable, or near expiry. Idempotent under repeated calls;
//     race-resistant when multiple replicas Ensure the same Secret
//     concurrently (the loser of the Create reads the winner's
//     material).
//
//   - MWC patcher (mwc.go): write the CA bundle into the
//     MutatingWebhookConfiguration's clientConfig so the apiserver
//     trusts the webhook server's serving cert. Idempotent: no Update
//     issued when every webhook entry already carries the requested
//     bytes.
//
//   - CertSource interface + implementations (source.go,
//     selfsigned.go, certmanager.go): the contract every cert mode
//     satisfies. Two halves: Authoritative writes apiserver state
//     (Secret + MWC) and runs only on the leader; Localize reads
//     apiserver state and reflects it onto CertDir, running on
//     every replica. SelfSignedSource composes Ensure +
//     PatchMWCaBundle on the authoritative side and atomic temp-
//     file-rename writes on the localize side. CertManagerSource is
//     a passive verifier - Authoritative is a no-op and Localize
//     parses what cert-manager dropped onto the volume mount.
//
//   - Runnables (runnable.go): two controller-runtime
//     manager.Runnables. AuthoritativeRunnable opts INTO leader
//     election and drives CertSource.Authoritative on a timer so
//     only one replica writes the apiserver-side material.
//     LocalSyncRunnable opts OUT of leader election and drives
//     CertSource.Localize on the same cadence so every replica's
//     local CertDir stays current. The split eliminates the
//     resourceVersion-Conflict races the earlier single-runnable
//     design had to absorb.
//
// All keys are ECDSA P-256 per the operator's design choice; CA
// certificates are valid for five years, serving certificates for one
// year, and rotation triggers when a certificate is within thirty days
// of its expiry. These defaults are unconfigurable in the primitives
// layer - the SelfSignedSource exposes the threshold as a flag.
//
// controller-gen's rbac generator only honours markers at package
// scope, so the apiserver permissions Ensure / PatchMWCaBundle
// require are declared here rather than next to the call sites.
// The Secret verbs are namespace-scoped (namespace=squirrel-system),
// so controller-gen emits a Role + RoleBinding rather than a
// ClusterRole - the SelfSignedSource only ever touches the
// webhook-cert Secret in the operator's own namespace, and a
// ClusterRole would have granted read/write of every Secret in
// the cluster.
//
// The mutatingwebhookconfigurations rule is restricted to the
// design's MWC name via resourceNames, so the manager cannot read
// or modify any other MWC even if its credentials leaked. The
// matching get/list rule without resourceNames lets controller-
// runtime cache the resource (List is not name-filterable), but
// the actual write access is name-scoped. The same pattern is
// applied to the webhook-cert Secret: get/update/patch are scoped
// to the named Secret squirrel-webhook-tls via resourceNames; the
// separate create rule is unscoped because Kubernetes RBAC does
// not honour resourceNames on `create` (a permission to create a
// specific name is structurally impossible). The namespace
// restriction (squirrel-system) bounds the create-any leak to a
// single namespace the operator already owns.
//
// +kubebuilder:rbac:groups="",namespace=squirrel-system,resources=secrets,verbs=create
// +kubebuilder:rbac:groups="",namespace=squirrel-system,resources=secrets,resourceNames=squirrel-webhook-tls,verbs=get;update;patch
// +kubebuilder:rbac:groups=admissionregistration.k8s.io,resources=mutatingwebhookconfigurations,verbs=get;list;watch
// +kubebuilder:rbac:groups=admissionregistration.k8s.io,resources=mutatingwebhookconfigurations,resourceNames=squirrel-image-rewrite,verbs=update;patch
package certs
