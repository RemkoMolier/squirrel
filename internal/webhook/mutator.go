package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	client "sigs.k8s.io/controller-runtime/pkg/client"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
	admission "sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	squirrelv1alpha1 "github.com/RemkoMolier/squirrel/api/v1alpha1"
	"github.com/RemkoMolier/squirrel/internal/engine"
	"github.com/RemkoMolier/squirrel/internal/metrics"
)

// OriginalImageAnnotationPrefix is the annotation-key prefix the
// mutator stamps on Pods it has rewritten. One annotation per
// rewritten container, with the container name as the name suffix
// and the original image as the value:
//
//	original-image.squirrel.molier.dev/<containerName> = <originalImage>
//
// The dedicated prefix (rather than `squirrel.molier.dev/<name>`)
// gives the `<containerName>` portion the full 63-char K8s
// annotation-name budget; container names are themselves limited
// to 63 chars (DNS-1123 label), and K8s enforces uniqueness across
// the containers / initContainers / ephemeralContainers lists, so
// no kind discriminator is needed.
//
// Operators read the rewrite trail via `kubectl describe pod`:
// each annotation shows the original image squirrel rewrote from,
// alongside the container's current `spec.image` (the result).
const OriginalImageAnnotationPrefix = "original-image.squirrel.molier.dev/"

// originalImageAnnotationKey builds the annotation key for a
// container. K8s guarantees the container name fits the 63-char
// annotation name part on its own; we do not truncate or hash.
func originalImageAnnotationKey(containerName string) string {
	return OriginalImageAnnotationPrefix + containerName
}

// hasOriginalImagePrefix reports whether annotationKey is a
// rewrite-trail annotation written by squirrel. Used to strip
// user-supplied trails on CREATE and to enumerate squirrel's own
// prior-pass annotations on UPDATE.
//
// The check rejects the bare prefix (no container name suffix): a
// container name is required for the annotation to refer to anything
// real, so a bare-prefix key is by definition not squirrel-written
// and is left alone by the strip + carry-forward loops.
func hasOriginalImagePrefix(annotationKey string) bool {
	return len(annotationKey) > len(OriginalImageAnnotationPrefix) &&
		strings.HasPrefix(annotationKey, OriginalImageAnnotationPrefix)
}

// OptInLabel re-exports the api/v1alpha1 constant so existing
// callers (mutator_test.go, integration tests) keep working without
// importing api/v1alpha1 directly. The handler defensively re-checks
// the label so a missing or misconfigured MWC selector cannot
// silently grant the operator privileges on non-opted-in namespaces.
const OptInLabel = squirrelv1alpha1.OptInLabel

// PodMutator is the controller-runtime admission.Handler that applies
// squirrel policies to Pod admissions. It is invoked from the
// MutatingWebhookConfiguration described in docs/design/v1alpha1.md,
// which gates the webhook on `squirrel.molier.dev/enabled=true` at the
// namespace level.
//
// IMPORTANT - manifest patch required: controller-tools' webhook
// marker does not support `namespaceSelector` or `objectSelector`
// fields, so the MWC generated from the package-level marker in
// doc.go has NO namespace gate. The design's selector (opt-in label
// + system-namespace exclusion) must be added by Phase 8 manifest
// overlays (kustomize patch on the generated
// MutatingWebhookConfiguration). The handler defensively re-checks
// the OptInLabel on the target namespace AND refuses to mutate Pods
// in any of the reserved namespaces (kube-system, kube-public,
// kube-node-lease, and the operator's own namespace) so a missing
// or misconfigured manifest patch cannot grant the operator
// privileges over control-plane workloads.
//
// The webhook also registers for the pods/ephemeralcontainers
// subresource: `kubectl debug` adds an ephemeral container via that
// subresource rather than a normal Pod update, so a rule that only
// targets `pods` would let an unrewritten upstream image into an
// opted-in namespace after the original create.
//
// The MutatingWebhookConfiguration and namespace RBAC are emitted
// from package-level +kubebuilder:webhook and +kubebuilder:rbac
// markers in doc.go; controller-gen's webhook and rbac generators
// only pick markers up at package scope.
type PodMutator struct {
	// Client is a controller-runtime cache-backed reader used to
	// look up the target namespace's labels and the applicable
	// policies.
	Client client.Reader

	// Decoder turns the admission.Request's raw bytes into a typed
	// corev1.Pod.
	Decoder admission.Decoder

	// OperatorNamespace is the namespace squirrel itself runs in.
	// The handler refuses to mutate Pods in this namespace as the
	// defensive complement to the design's MWC namespaceSelector:
	// rewriting the operator's own pods at startup is a credible
	// foot-gun (the cache may not yet be populated, the mutated
	// image may not be pullable, etc.). When empty the operator-
	// namespace gate is disabled - acceptable in tests, never in
	// production manifests.
	OperatorNamespace string

	// ExtraReservedNamespaces lists additional namespaces the handler
	// refuses to mutate beyond the built-in vanilla set (kube-system,
	// kube-public, kube-node-lease) and the operator's own. Operators
	// running on opinionated distributions populate this with their
	// distro-specific control-plane namespaces - e.g. openshift-* on
	// OpenShift, tigera-operator + calico-system on Calico, linkerd
	// or istio-system on service-mesh installs. Empty in test
	// fixtures and on vanilla clusters; production wiring threads
	// it from the manager's --reserved-namespaces flag.
	ExtraReservedNamespaces map[string]struct{}

	// compileCache memoises compilePolicyRules output for this
	// handler instance. nil at construction time and lazily
	// initialised on the first Handle call. Owning the cache here
	// rather than relying on the package-level fallback keeps the
	// cache lifecycle tied to the handler that mutates against it -
	// the natural fit for multi-handler topologies a future cluster
	// shape may want (per-tenant operators, dry-run sidecars). Tests
	// that need a fresh cache per case set this field explicitly
	// via NewMutatorTestEnv-style helpers.
	compileCache *lru.Cache[compileCacheKey, compileCacheValue]

	// compileCacheOnce gates the lazy initialisation so concurrent
	// Handle goroutines don't race on the first call.
	compileCacheOnce sync.Once
}

// ensureCompileCache returns the per-handler cache, lazily creating
// one if the field was left nil at construction. Concurrent Handle
// calls observe the same cache via the sync.Once gate.
func (m *PodMutator) ensureCompileCache() *lru.Cache[compileCacheKey, compileCacheValue] {
	m.compileCacheOnce.Do(func() {
		if m.compileCache == nil {
			m.compileCache = newCompileCache(compileCacheSize)
		}
	})
	return m.compileCache
}

// reservedNamespaceReasonSystem and reservedNamespaceReasonOperator
// are the label values for metrics.ReservedNamespaceSkipsTotal.
// Defining them as consts here (rather than at the metrics package)
// keeps the strings co-located with the only call site that emits
// them.
const (
	reservedNamespaceReasonSystem   = "system"
	reservedNamespaceReasonOperator = "operator"
	reservedNamespaceReasonExtra    = "extra"
)

// isExtraReservedNamespace reports whether ns appears in the
// operator-configured extra reserved list. Separated from
// isReservedNamespace so the metric-emitting Handle path can
// distinguish the extra category from the system one without
// re-walking the same maps.
func isExtraReservedNamespace(extra map[string]struct{}, ns string) bool {
	_, ok := extra[ns]
	return ok
}

// systemNamespaces is the set of Kubernetes-reserved namespaces the
// handler refuses to mutate as a defensive complement to the
// design's MWC namespaceSelector. These namespaces hold control-plane
// workloads whose images must never be rewritten by an application
// policy. The MWC overlay (Phase 8) already excludes them; this is
// the in-handler fallback when the overlay is missing or misapplied.
var systemNamespaces = map[string]struct{}{
	"kube-system":     {},
	"kube-public":     {},
	"kube-node-lease": {},
}

// isReservedNamespace reports whether ns is a system namespace, the
// operator's own namespace, or one of the operator-configured extra
// reserved namespaces. All three categories admit unchanged regardless
// of the OptInLabel.
func (m *PodMutator) isReservedNamespace(ns string) bool {
	if _, ok := systemNamespaces[ns]; ok {
		return true
	}
	if m.OperatorNamespace != "" && ns == m.OperatorNamespace {
		return true
	}
	if _, ok := m.ExtraReservedNamespaces[ns]; ok {
		return true
	}
	return false
}

// ephemeralContainersSubresource is the value of admission.Request's
// SubResource field when the request is for `kubectl debug`-style
// ephemeral-container additions. The apiserver enforces strict
// mutability per subresource:
//
//   - On the main `pods` resource spec.ephemeralContainers is
//     immutable; emitting a patch op on that slice would cause
//     pod-strategy validation to reject the admission, so an
//     unrelated update to a Pod whose ephemeral container predates
//     the policy would fail because of the webhook.
//   - On the `pods/ephemeralcontainers` subresource only
//     spec.ephemeralContainers is mutable; emitting a patch op on
//     spec.containers or spec.initContainers would be rejected
//     symmetrically.
//
// Handle branches on this constant so the patches it emits only
// ever target fields the apiserver will accept on that path.
const ephemeralContainersSubresource = "ephemeralcontainers"

// Handle implements admission.Handler. The flow is:
//
//  1. Decode the Pod from the admission request.
//  2. Look up the target namespace's labels from the cache. A
//     missing namespace (race between the MWC selector evaluation and
//     this lookup) admits the Pod unchanged.
//  3. Build the applicable rule slice via ApplicableRules.
//  4. Walk the containers the request is allowed to mutate and run
//     engine.Resolve on each. On the main `pods` resource that is
//     spec.containers and spec.initContainers; on the
//     `pods/ephemeralcontainers` subresource it is only
//     spec.ephemeralContainers. The apiserver enforces the inverse
//     immutability on each path - emitting a patch op on a slice
//     the subresource does not own would cause pod-strategy
//     validation to reject the whole admission. On UPDATE the
//     handler skips containers whose image is unchanged vs
//     req.OldObject: re-evaluating an unchanged image lets an
//     ill-formed catch-all rule (e.g. `target.repository:
//     "{registry}/{repository}"`) compound on every UPDATE,
//     rewriting `mirror.internal/foo/bar:1` into
//     `mirror.internal/mirror.internal/foo/bar:1` and so on. The
//     design's "create with mirrored image, then patch to upstream"
//     bypass is still closed - if the user changes the image on
//     UPDATE, the new image is re-evaluated and rewritten just like
//     on CREATE.
//  5. For every container whose image actually changed, set the new
//     image and record the rewrite. Skip outcomes and no-op
//     resolutions do not patch.
//  6. If anything changed, stamp the rewrites annotation and return
//     a JSON Patch computed against the original raw bytes.
func (m *PodMutator) Handle(ctx context.Context, req admission.Request) admission.Response {
	start := time.Now()
	defer func() {
		metrics.WebhookAdmissionDurationSeconds.Observe(time.Since(start).Seconds())
	}()

	pod := &corev1.Pod{}
	if err := m.Decoder.Decode(req, pod); err != nil {
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("decode pod: %w", err))
	}

	// On UPDATE the apiserver supplies the pre-update Pod in
	// req.OldObject. The handler decodes it so the container loops
	// can skip images the user did not change - re-evaluating an
	// unchanged image is the failure mode the per-rule loop comment
	// describes (catch-all repository templates recursively rewrite
	// their own output on every UPDATE). On CREATE OldObject is
	// empty and oldPod stays nil, which the loop helpers treat as
	// "everything is new" and fall through to the unconditional
	// rewrite path.
	var oldPod *corev1.Pod
	if req.Operation == admissionv1.Update && len(req.OldObject.Raw) > 0 {
		oldPod = &corev1.Pod{}
		if err := m.Decoder.DecodeRaw(req.OldObject, oldPod); err != nil {
			return admission.Errored(http.StatusBadRequest, fmt.Errorf("decode old pod: %w", err))
		}
	}

	// priorRewrites is the trusted record of containers this operator
	// has already rewritten on a previous admission. The skip set is
	// sourced ONLY from req.OldObject.Annotations on UPDATE:
	//
	//   - On CREATE, no prior pod exists, so any
	//     original-image.squirrel.molier.dev/<containerName> annotation
	//     on the incoming Pod is necessarily user-supplied. Trusting it
	//     would let a user with create-Pod permission forge "this image
	//     was already rewritten" entries and bypass otherwise-applicable
	//     rules. We always recompute from scratch on CREATE and strip
	//     any forged prefix annotations before stamping the trusted
	//     ones.
	//
	//   - On UPDATE, the OldObject annotation reflects the previous
	//     admitted state. Because every webhook call - including
	//     reinvocations - originates from the same chain of admission
	//     decisions we made, an entry there is by construction our own
	//     write. We trust it as the audit record for the prior pass.
	//
	// Reinvocation note: with reinvocationPolicy=IfNeeded the
	// apiserver may call the webhook again during the same Pod CREATE
	// after another mutating webhook modifies the object. Because we
	// ignore the in-flight annotation, that second pass re-evaluates
	// every container - which is safe when no rule matches its own
	// rewritten output. Operators who author such catch-all rules
	// (e.g. `**:* -> mirror.internal/{registry}/{repository}:{tag}`)
	// should exclude the mirror prefix from the match expression to
	// keep the rule idempotent.
	// priorRewrites maps containerName -> originalImage for each
	// per-container annotation present on OldObject. CREATE leaves
	// the map nil; UPDATE populates it from oldPod.Annotations.
	var priorRewrites map[string]string
	if oldPod != nil {
		for k, v := range oldPod.Annotations {
			if hasOriginalImagePrefix(k) {
				if priorRewrites == nil {
					priorRewrites = map[string]string{}
				}
				priorRewrites[strings.TrimPrefix(k, OriginalImageAnnotationPrefix)] = v
			}
		}
	}

	// A namespaced resource without a namespace is structurally
	// impossible from a well-formed apiserver request, but the
	// MutatingWebhookConfiguration also targets pods/status and
	// pods/ephemeralcontainers - subresources whose AdmissionReview
	// shape has historically been a source of surprises. An empty
	// req.Namespace here would fall through the OptInLabel lookup
	// (Get on an empty-name Namespace) into the admit-unchanged path
	// while also counting toward NamespaceLookupFailuresTotal - a
	// misleading metric attribution that hides a real misconfiguration.
	// Reject explicitly so the apiserver surfaces a 400 instead.
	if req.Namespace == "" {
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("admission request has empty namespace; refusing to evaluate"))
	}

	// Defensive complement to the design's MWC namespaceSelector:
	// the system namespaces (kube-system, kube-public,
	// kube-node-lease) and the operator's own namespace must never
	// be mutated even if their OptInLabel is set, since rewriting
	// control-plane workloads is far more dangerous than missing a
	// rewrite. The check fires before the namespace lookup so a
	// reserved namespace cannot be mutated even when the cache is
	// uninitialised.
	if m.isReservedNamespace(req.Namespace) {
		reason := reservedNamespaceReasonSystem
		switch {
		case m.OperatorNamespace != "" && req.Namespace == m.OperatorNamespace:
			reason = reservedNamespaceReasonOperator
		case isExtraReservedNamespace(m.ExtraReservedNamespaces, req.Namespace):
			// Distribution-specific control-plane namespaces (e.g.
			// openshift-*, istio-system, calico-system). Distinct
			// from the vanilla `system` category so operators on
			// opinionated distros can alert on misconfigured
			// distro namespaces independently.
			reason = reservedNamespaceReasonExtra
		}
		metrics.ReservedNamespaceSkipsTotal.WithLabelValues(reason).Inc()
		return admission.Allowed("reserved namespace; admitting unchanged")
	}

	// The MWC's namespaceSelector means the webhook should only run
	// in opted-in namespaces, but controller-tools cannot put the
	// selector on the generated MWC (see the doc comment above).
	// Re-check the OptInLabel here so the handler is safe regardless
	// of how the MWC is configured. A missing namespace (race
	// between selector match and this lookup) admits unchanged -
	// failurePolicy=Ignore would do the same on any other failure
	// path.
	var ns corev1.Namespace
	if err := m.Client.Get(ctx, client.ObjectKey{Name: req.Namespace}, &ns); err != nil {
		if apierrors.IsNotFound(err) {
			return admission.Allowed("namespace not found; admitting unchanged")
		}
		// failurePolicy=Ignore on the MWC tells the apiserver to admit
		// when the webhook is unreachable, but it does NOT downgrade a
		// 500 response into an admit - a denial is a denial. A
		// transient namespace-lookup failure (cache stale, apiserver
		// glitch) must therefore admit unchanged here, matching the
		// resolver paths that admit on a fatal List failure. The
		// metric is the operator-facing signal that the silent admit
		// happened; without it the cluster looks healthy.
		metrics.NamespaceLookupFailuresTotal.Inc()
		return admission.Allowed("could not look up namespace; admitting unchanged")
	}
	if ns.Labels[OptInLabel] != "true" {
		// Bump the counter so enforcement-mode operators can alert
		// when squirrel decides "no policies applicable" for a
		// namespace they expected to be enforced. The series is
		// labelled by namespace so a missing opt-in label on a
		// specific namespace shows up as a high-cardinality target
		// for the alert, not as a cluster-wide blanket count.
		metrics.UnoptedNamespaceSkipsTotal.WithLabelValues(req.Namespace).Inc()
		return admission.Allowed("namespace not opted in; admitting unchanged")
	}

	// A non-nil fatalErr means the resolver could not determine
	// the full policy set, in which case applying a partial set
	// could let an image past a missing cluster-wide skip rule.
	// Admit unchanged - failurePolicy=Ignore would do the same on
	// any other failure path. The metric is the operator-facing
	// signal that this silent admit happened.
	//
	// perRuleErrs surfaces rules that failed to compile at admission
	// time despite the reconciler having Accepted the parent policy
	// (defence-in-depth - the reconciler's ValidateGlob should have
	// caught these earlier). Each rule contributes one increment to
	// squirrel_invalid_admission_rules_total; the source's RuleSource
	// fields drive the labels.
	rules, perRuleErrs, fatalErr := applicableRulesWith(ctx, m.Client, req.Namespace, ns.Labels, m.ensureCompileCache())
	if fatalErr != nil {
		metrics.FatalResolverFailuresTotal.Inc()
		return admission.Allowed("could not list policies; admitting unchanged")
	}
	if n := len(perRuleErrs); n > 0 {
		// Each entry carries the failing policy + rule index already
		// formatted by ApplicableRules; log it so operators can
		// identify the offending policy from /metrics. The metric
		// help line points operators here, so this is the canonical
		// place to surface the detail.
		log := ctrllog.FromContext(ctx).WithName("admission")
		metrics.InvalidAdmissionRulesTotal.Add(float64(n))
		for _, err := range perRuleErrs {
			log.Info("rule compile failed at admission time; skipping rule", "namespace", req.Namespace, "error", err.Error())
		}
	}
	if len(rules) == 0 {
		return admission.Allowed("no applicable rules")
	}
	// Sort rules once per admission rather than once per container.
	// engine.Resolve handles the sort lazily on every call; for a
	// Pod with several containers and a non-trivial policy set the
	// repeated O(n log n) sort dominates admission latency.
	prepared := engine.Prepare(rules)

	patched := pod.DeepCopy()
	// rewrites accumulates the per-container original images this
	// admission pass rewrote: map[containerName]originalImage.
	// Container names are unique within a Pod across containers /
	// initContainers / ephemeralContainers (K8s enforces this),
	// so the kind discriminator does not need to live in the key.
	rewrites := map[string]string{}

	// The apiserver enforces strict mutability rules per subresource
	// in pkg/registry/core/pod/strategy.go:
	//
	//   - On the main `pods` resource, spec.ephemeralContainers is
	//     immutable: a regular CREATE/UPDATE that emits a patch op on
	//     that slice (e.g. on a Pod whose ephemeral container was
	//     added by `kubectl debug` before the policy was installed)
	//     is rejected by pod-strategy validation, so an unrelated
	//     update on such a Pod would fail because of the webhook.
	//
	//   - On the `pods/ephemeralcontainers` subresource only
	//     spec.ephemeralContainers is mutable; touching the other
	//     slices is rejected for the symmetric reason.
	//
	// Branch on req.SubResource so the patches the handler emits
	// only ever target fields the apiserver will accept on that path.
	// imageChanged reports whether the named container's image is
	// new vs the matching slot in oldPod. Matching by container name
	// (not by slice index) is the right contract: the apiserver
	// permits Pod containers to be reordered on UPDATE in some flows,
	// and an index-based match would treat the reordered slice as
	// every-image-changed and re-rewrite already-mirrored images.
	// Lookup is linear over the oldPod slice; container counts per
	// Pod are typically <10 so a map is overkill. On CREATE oldPod
	// is nil and every container is considered changed.
	oldImageByName := func(kind, name string) (string, bool) {
		if oldPod == nil {
			return "", false
		}
		switch kind {
		case "containers":
			for _, c := range oldPod.Spec.Containers {
				if c.Name == name {
					return c.Image, true
				}
			}
		case "initContainers":
			for _, c := range oldPod.Spec.InitContainers {
				if c.Name == name {
					return c.Image, true
				}
			}
		case "ephemeralContainers":
			for _, c := range oldPod.Spec.EphemeralContainers {
				if c.Name == name {
					return c.Image, true
				}
			}
		}
		return "", false
	}
	imageChanged := func(kind, name, newImage string) bool {
		oldImage, ok := oldImageByName(kind, name)
		if !ok {
			// Container did not exist in the pre-update Pod (or this
			// is a CREATE) - treat as changed so the engine runs.
			return true
		}
		return oldImage != newImage
	}

	// alreadyRewrittenByUs verifies a candidate prefix annotation
	// against the engine. The check is the trust-but-verify guard
	// against reinvocation-driven recursion: with reinvocationPolicy
	// =IfNeeded, the apiserver may call us a second time during the
	// same CREATE after another mutating webhook touches the Pod. On
	// that second pass the in-flight Pod carries our own annotation
	// from the first pass AND its container image is already our
	// rewritten output. A naive re-run of the engine would match the
	// rule again (a catch-all "**:* -> mirror.internal/{registry}/..."
	// matches its own output) and recurse to
	// "mirror.internal/mirror.internal/...".
	//
	// We instead verify: if patched.Annotations claims container X
	// was originally Y, and running the engine on Y produces the
	// current spec image for X, then this container is our own work
	// from a prior pass and we must NOT re-rewrite it. The check
	// re-runs the same engine the attacker would have to fool, so
	// forging the annotation cannot bypass an applicable rule - the
	// attacker would have to find a Y that the engine maps to a
	// desired bypass image, which the typical mirror-prepending rule
	// makes structurally impossible (every output carries the mirror
	// prefix, so no Y exists that maps to a non-mirror image).
	// verifiedPriorRewrite returns (claimedOriginal, true) when the
	// in-flight Pod's prefix annotation for the named container
	// passes a re-run through the engine: i.e. when running the
	// engine on the claimed original produces the container's
	// current spec image. The second return is false when there is
	// no annotation, when the engine errors or produces a different
	// output, or when the claimed original equals the current image
	// (no rewrite happened on the prior pass, so no recursion risk).
	verifiedPriorRewrite := func(name, currentImage string) (string, bool) {
		if patched.Annotations == nil {
			return "", false
		}
		claimedOriginal, ok := patched.Annotations[originalImageAnnotationKey(name)]
		if !ok {
			return "", false
		}
		if claimedOriginal == currentImage {
			return "", false
		}
		dec, err := engine.ResolvePrepared(claimedOriginal, prepared)
		if err != nil {
			return "", false
		}
		if dec.Action != engine.ActionRewrite {
			return "", false
		}
		if dec.RewrittenImage != currentImage {
			return "", false
		}
		return claimedOriginal, true
	}

	// After the security refactor the annotation is purely an audit
	// trail: the per-rewrite skip decision is driven by imageChanged
	// (which compares the current image against the prior pod's
	// same-named container). The OldObject annotation is preserved
	// across UPDATEs via mergeRewrites so operators reading
	// `kubectl describe pod` always see the original image squirrel
	// rewrote from, even after many UPDATE cycles.
	if req.SubResource == ephemeralContainersSubresource {
		for i := range patched.Spec.EphemeralContainers {
			// EphemeralContainer embeds EphemeralContainerCommon,
			// which in turn carries the Image field - the same
			// pointer dance works on all three container kinds.
			name := patched.Spec.EphemeralContainers[i].Name
			img := patched.Spec.EphemeralContainers[i].Image
			if !imageChanged("ephemeralContainers", name, img) {
				continue
			}
			if claimed, ok := verifiedPriorRewrite(name, img); ok {
				// Keep the existing annotation by marking this container
				// as already-rewritten by us. The hygiene loop below
				// treats this as trusted and preserves the annotation;
				// no RewritesTotal increment because nothing new was
				// rewritten on this admission pass.
				rewrites[name] = claimed
				continue
			}
			recordRewrite(ctx, req.Namespace, &patched.Spec.EphemeralContainers[i].Image, "ephemeralContainers", name, prepared, rewrites)
		}
	} else {
		for i := range patched.Spec.Containers {
			name := patched.Spec.Containers[i].Name
			img := patched.Spec.Containers[i].Image
			if !imageChanged("containers", name, img) {
				continue
			}
			if claimed, ok := verifiedPriorRewrite(name, img); ok {
				// Keep the existing annotation by marking this container
				// as already-rewritten by us. The hygiene loop below
				// treats this as trusted and preserves the annotation;
				// no RewritesTotal increment because nothing new was
				// rewritten on this admission pass.
				rewrites[name] = claimed
				continue
			}
			recordRewrite(ctx, req.Namespace, &patched.Spec.Containers[i].Image, "containers", name, prepared, rewrites)
		}
		for i := range patched.Spec.InitContainers {
			name := patched.Spec.InitContainers[i].Name
			img := patched.Spec.InitContainers[i].Image
			if !imageChanged("initContainers", name, img) {
				continue
			}
			if claimed, ok := verifiedPriorRewrite(name, img); ok {
				// Keep the existing annotation by marking this container
				// as already-rewritten by us. The hygiene loop below
				// treats this as trusted and preserves the annotation;
				// no RewritesTotal increment because nothing new was
				// rewritten on this admission pass.
				rewrites[name] = claimed
				continue
			}
			recordRewrite(ctx, req.Namespace, &patched.Spec.InitContainers[i].Image, "initContainers", name, prepared, rewrites)
		}
	}

	// Annotation hygiene applies on every admission that lands on
	// the main `pods` resource (annotations are immutable on the
	// ephemeralcontainers subresource, so writes there are skipped).
	//
	//   - CREATE: any original-image.squirrel.molier.dev/* on the
	//     incoming Pod is user-supplied (we have not admitted this
	//     Pod yet) and may forge entries pointing at images squirrel
	//     never rewrote. The trusted set on CREATE is just `rewrites`
	//     (priorRewrites is nil) - any prefix annotation whose
	//     container name is not in `rewrites` is forged and must be
	//     stripped.
	//   - UPDATE: priorRewrites came from OldObject above and is
	//     trusted as squirrel's prior work. The trusted set is
	//     priorRewrites ∪ rewrites. Any prefix annotation on the
	//     incoming Pod whose container name is in neither is a
	//     forgery against an unchanged container - common because
	//     PATCH-on-Pods is widely granted - and must be stripped.
	//     Same is true for trusted entries whose container has been
	//     removed from the spec; we keep the audit trail in sync
	//     with reality.
	//
	// Same code path handles both Operations. The CREATE case falls
	// out naturally: priorRewrites is nil, so the trusted set
	// collapses to `rewrites`, and a Pod with no rules matching
	// any container gets every prefix annotation stripped.
	mutated := false
	if req.SubResource != ephemeralContainersSubresource {
		liveContainers := containerNamesInPod(patched)
		// Strip user-supplied or stale prefix annotations.
		for k := range patched.Annotations {
			if !hasOriginalImagePrefix(k) {
				continue
			}
			name := strings.TrimPrefix(k, OriginalImageAnnotationPrefix)
			_, trustedPrior := priorRewrites[name]
			_, trustedThisPass := rewrites[name]
			_, live := liveContainers[name]
			if (!trustedPrior && !trustedThisPass) || !live {
				delete(patched.Annotations, k)
				mutated = true
			}
		}
		// Carry the trusted prior trail forward for containers that
		// still exist in the spec.
		for name, original := range priorRewrites {
			if _, ok := liveContainers[name]; !ok {
				continue
			}
			if patched.Annotations == nil {
				patched.Annotations = map[string]string{}
			}
			key := originalImageAnnotationKey(name)
			if patched.Annotations[key] != original {
				patched.Annotations[key] = original
				mutated = true
			}
		}
		// Stamp this pass's new rewrites.
		for name, original := range rewrites {
			if _, ok := liveContainers[name]; !ok {
				continue
			}
			if patched.Annotations == nil {
				patched.Annotations = map[string]string{}
			}
			key := originalImageAnnotationKey(name)
			if patched.Annotations[key] != original {
				patched.Annotations[key] = original
				mutated = true
			}
		}
	}

	if len(rewrites) == 0 && !mutated {
		return admission.Allowed("no images required rewriting")
	}

	patchedBytes, err := json.Marshal(patched)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, fmt.Errorf("marshal patched pod: %w", err))
	}
	return admission.PatchResponseFromRaw(req.Object.Raw, patchedBytes)
}

// recordRewrite runs the engine on one container image. When the
// resolution produces an actual change the image pointer is updated
// in-place and a `<kind>[<idx>]=<from>-><to>` entry is appended to the
// caller's rewrites slice. Skip outcomes, no-match outcomes, and
// no-op rewrites where the rendered image equals the input leave both
// the image and the rewrites slice unchanged.
//
// An unparseable input image is silently ignored: the webhook's
// failurePolicy is Ignore, so a malformed image must not block
// admission. The unparseable counter is the operator-facing signal
// that this happened so a flood of misconfigured Pods is visible
// through /metrics without being a deny.
func recordRewrite(ctx context.Context, namespace string, image *string, kind string, containerName string, prepared engine.PreparedRules, rewrites map[string]string) {
	dec, err := engine.ResolvePrepared(*image, prepared)
	if err != nil {
		metrics.UnparseableImagesTotal.WithLabelValues(namespace).Inc()
		// Log the underlying parse failure with kind+name+image so
		// operators can correlate the metric bump against the
		// offending container. failurePolicy=Ignore turns this into
		// admit-unchanged; without the log line the metric is the
		// only signal and operators cannot triage past "something is
		// unparseable somewhere".
		ctrllog.FromContext(ctx).WithName("admission").Info(
			"engine could not parse container image; admitting unchanged",
			"namespace", namespace, "kind", kind, "container", containerName, "image", *image, "error", err.Error(),
		)
		return
	}
	// Decision.InvalidRenders is populated regardless of the eventual
	// Action: rules that matched but rendered invalidly are recorded
	// even when a lower-priority rule ultimately wins, since each
	// failure is its own operational signal. The namespace label is
	// the admission namespace (where the Pod was admitted), not the
	// policy's own namespace - a ClusterImagePolicy match would
	// otherwise emit namespace="" and lose the location signal
	// operators alert on. The policy is already identified by its
	// policy_kind + policy_name labels.
	for _, src := range dec.InvalidRenders {
		metrics.InvalidTargetRendersTotal.WithLabelValues(policyKindLabel(src), src.Name, namespace).Inc()
	}
	if dec.Action == engine.ActionSkip {
		metrics.SkipsTotal.WithLabelValues(policyKindLabel(dec.Source), dec.Source.Name, namespace).Inc()
		return
	}
	if dec.Action != engine.ActionRewrite || dec.RewrittenImage == *image {
		return
	}
	// Annotation entry records ONLY the original image; the rewritten
	// image is whatever the Pod's spec carries at this slot after the
	// patch lands. Operators read both via `kubectl describe pod`:
	// the annotation gives the squirrel-attributable "before" value,
	// the container spec gives the current "after".
	// rewrites is keyed by container name; the kind suffix is not
	// needed because container names are unique within a Pod (the
	// kind parameter is still used in the parse-failure log line
	// above so operators can correlate which kind of container
	// produced the unparseable image).
	rewrites[containerName] = *image
	*image = dec.RewrittenImage
	metrics.RewritesTotal.WithLabelValues(policyKindLabel(dec.Source), dec.Source.Name, namespace).Inc()
}

// containerNamesInPod returns the set of container names across
// all three container kinds in the Pod spec. Used to constrain
// annotation writes to containers that actually exist - K8s
// enforces uniqueness across the three kinds, so a flat name set
// is sufficient.
func containerNamesInPod(pod *corev1.Pod) map[string]struct{} {
	out := map[string]struct{}{}
	for _, c := range pod.Spec.Containers {
		out[c.Name] = struct{}{}
	}
	for _, c := range pod.Spec.InitContainers {
		out[c.Name] = struct{}{}
	}
	for _, c := range pod.Spec.EphemeralContainers {
		out[c.Name] = struct{}{}
	}
	return out
}

// policyKindLabel maps a RuleSource's Scope to the metric label
// matching the kubebuilder kind name (ClusterImagePolicy or
// ImagePolicy). The label values are stable across versions and
// match the kind labels Prometheus operators write alerting rules
// against.
func policyKindLabel(src engine.RuleSource) string {
	if src.Scope == engine.ScopeCluster {
		return metrics.PolicyKindCluster
	}
	return metrics.PolicyKindNamespaced
}
