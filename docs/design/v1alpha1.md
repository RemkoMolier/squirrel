# squirrel — v1alpha1 design

## Summary

Squirrel is a Kubernetes operator that rewrites container images on Pod admission according to declarative policies.
Cluster operators express their policies as Kubernetes resources of two kinds: `ClusterImagePolicy` (cluster-scoped, optionally narrowed by namespace selector) and `ImagePolicy` (namespace-scoped, applying only inside its own namespace).
The operator runs a mutating admission webhook that intercepts Pod CREATE and UPDATE requests, matches each container's image against the applicable policies, and rewrites it according to the matched rule's target.
The mechanism is purely declarative: there is no runtime hook, no DaemonSet, and no node-level component.

## Motivation

Image rewriting is a recurring operational need for clusters that do not pull images directly from upstream registries.
The most common scenarios are mirroring (redirect `docker.io/*` to a private cache), letting a specific upstream image through unchanged (skip rules), and per-namespace overrides for application owners.

Today these scenarios are addressed with general-purpose policy engines such as Kyverno and OPA Gatekeeper.
Those engines work, but they require operators to author Rego or Kyverno DSL, and the resulting policies are dense and hard to review.
A purpose-built operator can offer a smaller, more legible API surface for the specific rewriting use-case, with validation and observability tuned to it.

### Goals

- Declarative image rewriting expressed as Kubernetes resources that fit GitOps workflows.
- Two policy scopes: cluster-wide with optional namespace narrowing, and namespace-scoped overrides.
- Safe-by-default operation: namespaces opt in to the operator via a single well-known label.
- Glob-based matching with structured, templated targets that cover the common mirroring patterns.
- An extensible action enum that can grow to cover related operations such as cache pre-pull without a breaking API change.
- Strong observability: status conditions per policy, an annotation stamped on mutated pods, and Prometheus metrics for the operator itself.

### Non-Goals

- Signature or attestation verification.
  Sigstore's policy-controller addresses this and the two operators can coexist.
- Image scanning, SBOM generation, or vulnerability evaluation.
- Rewriting at registry push time.
  Squirrel operates at the consumer side, on Pod admission, never at the image source.
- Modifying images on already-running pods.
  The UPDATE webhook re-evaluates new images on Pod updates, but completed admissions are not revisited.
- High availability or active-active controllers in v0.1.0.

## Supported Kubernetes versions

Squirrel targets Kubernetes 1.33 and newer.

The baseline is conservative; the operator relies only on stable APIs:

- `admissionregistration.k8s.io/v1` for `MutatingWebhookConfiguration`, including `reinvocationPolicy: IfNeeded` and `matchConditions`.
- The automatic `kubernetes.io/metadata.name` label on every namespace.
- `CustomResourceDefinition` v1 with structural-schema validation and OpenAPI-based defaulting.
- `coordination.k8s.io/v1` leases (used in future versions for leader election; not used in v0.1.0).

Older clusters are not a tested deployment target; backporting to earlier 1.x releases is not a goal of v1alpha1.
The 1.33 floor also clears the way for an eventual transition (in v1beta1 or later) from the mutating webhook to a `MutatingAdmissionPolicy` once that API has matured; this is a future-work item, not part of v1alpha1.

## User stories

### Mirror Docker Hub to a private registry

A platform team operates a private OCI registry at `mirror.internal` and wants all Docker Hub pulls in production namespaces to be redirected to it.

```yaml
apiVersion: squirrel.molier.dev/v1alpha1
kind: ClusterImagePolicy
metadata:
  name: dockerhub-mirror
spec:
  namespaceSelector:
    matchLabels:
      tier: production
  defaultTarget:
    registry: mirror.internal
    repository: dockerhub/{repository}
    tags: ["{tag}", "{digest:short8}"]
  rules:
    - match: "docker.io/**:*"
```

### Skip a specific image cluster-wide

A security team has audited a particular base image and certified it for direct upstream consumption; it must not be rewritten by any mirror policy.

```yaml
apiVersion: squirrel.molier.dev/v1alpha1
kind: ClusterImagePolicy
metadata:
  name: allow-direct-pulls
spec:
  rules:
    - match: "docker.io/library/distroless-base:*"
      action: skip
```

### Per-namespace override

An application team owns the `payments` namespace and wants their `nginx` image redirected to a patched build, leaving the cluster-wide mirror policy in place for everything else.

```yaml
apiVersion: squirrel.molier.dev/v1alpha1
kind: ImagePolicy
metadata:
  name: payments-nginx-override
  namespace: payments
spec:
  rules:
    - match: "docker.io/library/nginx:*"
      target:
        registry: registry.internal
        repository: payments/nginx-patched
        tags: ["{tag}"]
```

### Onboarding a namespace

A cluster admin labels a namespace to opt it in to the operator.
Until the label is present, no policy applies to pods in that namespace, regardless of how the policies are scoped.

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: payments
  labels:
    squirrel.molier.dev/enabled: "true"
    tier: production
```

## API reference

### Group and version

Group: `squirrel.molier.dev`.
Version in this document: `v1alpha1`.
This version is alpha; breaking changes are permitted between alpha releases as the design matures.

### Kinds

- `ClusterImagePolicy` (cluster-scoped, short name `cip`)
- `ImagePolicy` (namespaced, short name `ip`)

Both kinds share the same `spec` shape except for the `namespaceSelector` field, which is valid only on `ClusterImagePolicy`.

### Spec

```yaml
spec:
  namespaceSelector:                    # ClusterImagePolicy only; optional
    matchLabels: {...}
    matchExpressions: [...]
  defaultAction: rewrite | skip         # optional; default: rewrite
  defaultTarget:                        # optional
    registry: <literal-or-template>     # required when defaultTarget is present
    repository: <literal-or-template>   # optional; default {repository}
    tags: ["<template>", ...]           # optional; default ["{tag}"];
                                        # tag: "..." accepted as sugar for tags: ["..."]
  rules: [Rule]                         # required, at least one entry
```

### Rule

```yaml
- match: <glob-string | match-object>   # required
  action: rewrite | skip                # optional; inherits spec.defaultAction
  target:                               # optional; meaningful only for rewrite
    registry: <literal-or-template>
    repository: <literal-or-template>
    tags: ["<template>", ...]
  priority: <int32>                     # optional; default = match-glob length
```

### Match

A match is either a glob string covering the whole image reference (`<registry>/<repository>:<tag>@<digest>`), or a structured object whose fields are individually glob-able:

```yaml
match: "docker.io/library/**:*"
```

```yaml
match:
  registry: docker.io
  repository: library/**
  tag: "*"
  digest: "*"
```

Glob alphabet (the full alphabet of `github.com/gobwas/glob` compiled with `/` as the path separator):

- `*` matches any sequence of characters not containing `/`.
- `**` matches any sequence of characters including `/`, including the empty sequence.
- `?` matches any single character not containing `/`.
- `[abc]`, `[a-z]` match a single character drawn from the set or range.
- `[!abc]` (or `[^abc]`) matches a single character outside the set.
- `{foo,bar,baz}` matches any of the comma-separated alternatives; each alternative is itself a glob.
- `\<x>` escapes `<x>` so the next character is treated as literal.
- All other characters are literal.

Exposing this richer alphabet is safe because OCI image references forbid every punctuation character that drives the extra features (`?`, `[`, `]`, `{`, `}`, `\` are not legal in any of registry, repository, tag, or digest per the OCI distribution spec), so a policy that puts one of those characters in a match field is unambiguously asking for the glob interpretation.

Structured matches default any omitted field as follows:

- `registry`: `*`
- `repository`: `**`
- `tag`: `*`
- `digest`: `*`

Image normalisation before matching is performed by `github.com/google/go-containerregistry/pkg/name` with `WithDefaultRegistry(name.DefaultRegistry)` and `WithDefaultTag(name.DefaultTag)`.
The `index.docker.io` form returned by `pkg/name` is canonicalised to `docker.io` for matching and templating purposes.
The match is always against the normalised four-tuple `(registry, repository, tag, digest)`, where the digest field is the literal string after `@` (including the algorithm prefix) or the empty string if no digest is present.

### Target

A target is a structured object that, after template expansion against the matched input, must produce a complete OCI-valid image reference.

For a rule-level `target`:

- All fields are optional individually.
- An omitted `registry` falls back to `defaultTarget.registry`.
- An omitted `repository` falls back to `defaultTarget.repository` if present, otherwise to `{repository}` (passthrough of the matched input).
- An omitted `tags` falls back to `defaultTarget.tags` if present, otherwise to `["{tag}"]`.

For `defaultTarget`:

- `registry` is required when the field is present.
- Other fields use the same passthrough defaults.

The effective target (rule's target merged over defaultTarget) must end up with `registry` defined; otherwise the rule is rejected at reconcile time and the webhook skips it.

### Template placeholders

Within target field values, the following placeholders are substituted with the corresponding component of the matched input image:

- `{registry}` — the input's registry.
- `{repository}` — the input's repository path (e.g. `library/nginx`).
- `{repository:owner}` — the first `/`-segment of the repository.
- `{repository:image}` — the last `/`-segment of the repository.
- `{repository:path}` — every segment of the repository except the last.
- `{repository:flat}` — the repository with `/` replaced by `-`.
- `{tag}` — the input's tag.
- `{digest}` — the input's digest, including the algorithm prefix (e.g. `sha256:abc…`).
- `{digest:hex}` — the hex portion of the digest, without the algorithm prefix.
- `{digest:shortN}` — the first N characters of the hex portion, where N is a positive integer no greater than the hex length (64 for sha256).
- `{digest:algo}` — the algorithm name (e.g. `sha256`).

Validity by field:

- `{digest}` is not valid inside `tags` entries because OCI tags forbid `:`; the reconciler rejects rules that use `{digest}` in a tag.
- All placeholders are case-sensitive.
- Unknown placeholders (`{foo}`, `{repository:typo}`) cause the reconciler to reject the rule.

Digest is always inherited from the matched input.
There is no `target.digest` field.
If the input carries a digest, the rewritten output carries the same digest; if the input has no digest, the output has none.

### Tag list semantics

A `tags` list is evaluated in order at admission time.
For the `rewrite` action, the first candidate that, after template expansion, produces a non-empty OCI-valid tag is used as the rewritten image's tag.
The remaining candidates are preserved by the engine for future action types (see Future Work) and otherwise ignored.

If every candidate expands to an empty or invalid value (for example, `["{tag}"]` against an input that has neither a tag nor a digest after normalisation), the rule is treated as non-applicable for this container and resolution proceeds to the next applicable rule.

### Action

```yaml
action: rewrite | skip
```

- `rewrite` (default) applies the rule's target rendering to produce a new image reference for the container.
- `skip` matches the input and terminates resolution; the image is unchanged.

The `defaultAction` field on the spec sets the action for rules that omit `action`.

### Priority

Each rule has an effective priority used during the rewrite phase of resolution.
The default value is the byte length of the normalised match-glob string; longer, more literal globs are more specific and apply first.
The optional `priority` field on a rule overrides this default.
Priority is a signed 32-bit integer, so users can express both high-priority overrides (`priority: 99999`) and low-priority fallbacks (`priority: -1`).

Priority has no effect on rules whose effective action is `skip`; skip rules are evaluated in a single unordered batch before any rewrite rule is considered.
The reconciler emits a non-fatal `PriorityIgnoredOnSkip` warning condition when `priority` is set on a skip rule.

### Status

```yaml
status:
  observedGeneration: <int64>
  conditions:
    - type: Accepted
      status: "True" | "False"
      observedGeneration: <int64>
      reason: <CamelCaseReason>
      message: <human-readable>
      lastTransitionTime: <RFC3339>
```

Standard condition reasons:

- `Accepted` — the policy is well-formed and is being applied at admission time.
- `EmptyRules` — `spec.rules` is empty or absent.
- `InvalidMatch` — a rule's match field is malformed; the message identifies the rule index.
- `InvalidPlaceholder` — a rule's target uses an unknown placeholder or `{digest}` inside `tags`.
- `MissingRegistry` — a rule's effective target has no registry (neither rule nor defaultTarget supplies one).
- `ActionTargetConflict` — a rule has `action: skip` with a non-empty `target`.
- `PriorityIgnoredOnSkip` — informational; the policy stays Accepted.

The full list of reason values is enumerated in source under `api/v1alpha1`.

## Resolution semantics

### Image normalisation

Every container image observed at admission time is parsed through `pkg/name.ParseReference` with default registry `index.docker.io` and default tag `latest`.
Parse failures cause the operator to log at info level, increment the `squirrel_unparseable_images_total` metric, and admit the pod unchanged.
On parse success the operator extracts the four-tuple `(registry, repository, tag, digest)` and canonicalises `index.docker.io` to `docker.io`.

### Rule applicability

A rule applies to a container if all of the following hold:

- The rule's policy is in the same namespace as the pod (for `ImagePolicy`), or the rule's policy is a `ClusterImagePolicy` whose `namespaceSelector` matches the pod's namespace labels (or has no `namespaceSelector`, meaning all opted-in namespaces).
- The policy has `Accepted=True` in its status.
- The rule itself passed reconcile-time validation.

### Two-phase resolution

Resolution proceeds in two phases per container:

1. **Skip phase.**
   The engine walks every applicable rule whose effective action is `skip`.
   The walk order is unspecified; all skip rules are considered equally.
   If any skip rule's match expression matches the container's normalised image, resolution terminates for this container and the image is unchanged.

2. **Rewrite phase.**
   If the skip phase did not terminate, the engine walks applicable rules whose effective action is `rewrite`, sorted as follows:

   - Effective priority, descending.
   - Tie-breaker: `ImagePolicy` rules before `ClusterImagePolicy` rules.
   - Tie-breaker: policy name, ascending lexicographic order.
   - Tie-breaker: rule index within the policy, ascending.

   For each rule in this order:

   - If the rule's match expression matches the normalised image, evaluate the rule's effective target.
   - If the target renders successfully and the resulting reference is valid per `pkg/name`, the rewrite is applied and the engine returns the rendered image.
   - If the target renders to an empty or invalid result, the rule is non-applicable for this container and resolution continues to the next rule.

   If no rewrite rule applies, the image is unchanged.

### Specificity

A structured match is normalised by joining its components in `<registry>/<repository>:<tag>@<digest>` form with field defaults filled in.
The default priority is the byte length of that normalised string, which biases longer and more literal globs to apply first.

## Validation

Reconciliation is read-only: the controllers do not write any state outside the policy resources' status subresource.

Each reconciler:

- Decodes the policy spec.
- Walks `spec.rules` in order.
- For each rule:
  - Parses the match (string-glob or structured form) and validates the glob alphabet.
  - Resolves the effective action.
  - For `rewrite` rules, computes the effective target by merging `rule.target` over `spec.defaultTarget` field-by-field, then validates that `registry` is set, that no unknown placeholders appear, that `{digest}` does not appear inside `tags`, and that `{digest:shortN}` has a numeric `N` in range.
  - For `skip` rules, rejects any non-empty `target`.
- Sets `status.observedGeneration` to the spec generation.
- Sets the `Accepted` condition based on whether every rule passed validation.

When any rule is invalid:

- The whole policy is marked `Accepted=False` with a reason identifying the offending rule index in the message.
- The webhook skips every rule in the policy until the issue is resolved.
  This is all-or-nothing per policy and avoids partial application of a malformed policy.

## Webhook behaviour

The operator registers a single `MutatingWebhookConfiguration` named `squirrel-image-rewrite` that intercepts CREATE and UPDATE operations on the `pods` resource and on its `pods/ephemeralcontainers` subresource.

### Webhook configuration

- `operations`: `["CREATE", "UPDATE"]`.
- `resources`: `["pods", "pods/ephemeralcontainers"]`.
  The subresource is registered so `kubectl debug`-style additions of an ephemeral container also go through the webhook; without it, an opted-in namespace could attach an unrewritten upstream image after the original create.
- `apiGroups`: `[""]`.
- `apiVersions`: `["v1"]`.
- `scope`: `Namespaced`.
- `sideEffects`: `None`.
- `failurePolicy`: `Ignore`.
  If the operator is unavailable, the pod is admitted without rewriting.
- `reinvocationPolicy`: `IfNeeded`.
  Required so that subsequent admission plugins that change pod images trigger re-evaluation.
- `timeoutSeconds`: `5`.

The apiserver enforces strict per-subresource mutability and the handler honours it: on `pods` requests it walks `spec.containers` and `spec.initContainers` (and writes a per-container `original-image.squirrel.molier.dev/<containerName>` annotation for each rewrite); on `pods/ephemeralcontainers` requests it walks only `spec.ephemeralContainers` and deliberately omits the annotation, since `metadata.annotations` is immutable on that subresource and emitting a patch op against it would cause the apiserver to reject the whole admission.

The MWC's `namespaceSelector` is:

```yaml
namespaceSelector:
  matchExpressions:
    - key: squirrel.molier.dev/enabled
      operator: In
      values: ["true"]
    - key: kubernetes.io/metadata.name
      operator: NotIn
      values: [kube-system, kube-public, kube-node-lease, <operator-namespace>]
```

The `kubernetes.io/metadata.name` label is set automatically on every namespace from Kubernetes 1.21 onwards.

### Admission handler

For each Pod admission request:

1. Decode the Pod object.
2. Fetch the pod's namespace and its labels from the informer cache.
3. Run the engine for each entry in `spec.containers`, `spec.initContainers`, and `spec.ephemeralContainers`.
4. For containers whose image changed, patch the Pod object via a JSON Patch.
5. For each rewritten container, stamp an `original-image.squirrel.molier.dev/<containerName>` annotation whose value is the original image string.
   Container names are unique within a Pod across `spec.containers`, `spec.initContainers`, and `spec.ephemeralContainers`, so a single prefix without a kind discriminator is unambiguous.
6. Return the patch in the admission response.

UPDATE handling re-evaluates every container in the new pod object, not only those whose image changed.
This prevents an admission-time bypass via a two-step "create with mirrored image, then patch to upstream image" sequence.

## Architecture

### Components

- `api/v1alpha1` — Go types, generated DeepCopy, and scheme registration for the two CRDs.
- `internal/imageref` — wraps `pkg/name` with the glob matcher, the template renderer, the specificity scorer, and the four-tuple extraction.
- `internal/engine` — given a Pod and the policy cache, computes the rewrite plan.
  Stateless; reads policies from the controller-runtime informer cache.
- `internal/controller` — two reconcilers (one per kind) that perform validation and surface status.
- `internal/webhook` — the admission HTTP handler that invokes the engine and produces JSON Patches.
- `internal/certs` — manages the webhook's TLS certificate; supports a self-signed mode (default) and a cert-manager-driven mode (opt-in via flag).
- `cmd/manager` — the `main` package that wires the controller-runtime Manager, registers the reconcilers, starts the webhook server, and selects the cert source.

### Sequence: admission request

```text
kube-apiserver -> webhook handler -> engine -> imageref + policy cache
                                       |
                                       v
                                rewrite plan
                                       |
                                       v
                              JSON patch + annotation
                                       |
                                       v
                              admission response -> kube-apiserver
```

### State

The only durable state is the policy resources themselves.
The operator keeps no in-memory rule cache outside controller-runtime's informer cache; reading from that cache is cheap and consistent.

## Security and safety

### Opt-in layers

Three layers guard against accidental cluster-wide rewriting:

1. The MWC `namespaceSelector` requires every namespace to carry `squirrel.molier.dev/enabled=true` and excludes the system namespaces (`kube-system`, `kube-public`, `kube-node-lease`, and the operator's own namespace).
2. A `ClusterImagePolicy`'s optional `namespaceSelector` further narrows which opted-in namespaces a given policy applies to.
3. `ImagePolicy` is namespaced and scopes rules within a single namespace, but it does *not* bypass the namespace label gate: an `ImagePolicy` in a namespace that lacks `squirrel.molier.dev/enabled=true` is inert because the webhook never runs there.
   Creating an `ImagePolicy` is the explicit per-namespace customisation; setting the label is the explicit opt-in.

### Failure policy

The webhook is configured with `failurePolicy: Ignore`.
The rationale is that an unavailable operator should never block pod admission: a failure in the rewrite path is preferable to a stalled cluster, and the configuration matches the convention for non-essential mutators.
Operators who require strict enforcement can change this to `Fail` in their installation manifest; the policy is operator-controlled, not code-controlled.

### Digest integrity

The output of every rewrite carries the input's digest unchanged.
There is no syntax in the API for changing the digest field of the rewritten reference.
This guarantees that an image pinned to a digest cannot be silently pointed at different content by any squirrel policy: the rewritten reference resolves to exactly the same content as the input.

### Certificate management

The webhook server presents a TLS certificate that the API server validates via the MWC's `caBundle` field.

In the default self-signed mode the operator generates a CA and a serving certificate at startup, stores them in a Kubernetes Secret in its own namespace, and patches the `MutatingWebhookConfiguration`'s `caBundle` field with the CA's encoded form.
Rotation happens automatically when the existing cert is within a configurable threshold of expiry.

In the cert-manager mode, selected by `--cert-source=cert-manager`, the operator does not generate or manage certificates.
A `Certificate` resource issued in the operator's namespace populates the Secret, and a `cert-manager.io/inject-ca-from` annotation on the `MutatingWebhookConfiguration` causes cert-manager to inject the CA bundle.

### RBAC

The operator's `ServiceAccount` has the following permissions:

- `get`, `list`, `watch` on `clusterimagepolicies.squirrel.molier.dev` and `imagepolicies.squirrel.molier.dev`.
- `update`, `patch` on the status subresource of those kinds.
- `get`, `list`, `watch` on `namespaces` (to evaluate `namespaceSelector` matches).
- `get`, `update`, `patch` on the `Secret` it uses for webhook certs (its own namespace only).
- `get`, `update`, `patch` on the `MutatingWebhookConfiguration` it owns (self-signed cert mode only).
- `create`, `patch` on `events` for emitting validation events.

The operator does not need privileges on `pods` or other workloads.

## Observability

### Metrics

Prometheus metrics are exposed on `/metrics`:

- `squirrel_rewrites_total{policy_kind, policy_name, namespace}`: counter; incremented per container rewrite.
- `squirrel_skips_total{policy_kind, policy_name, namespace}`: counter; incremented per container matched by a skip rule.
- `squirrel_policies{kind, status}`: gauge; one observation per policy with its `Accepted` status.
- `squirrel_invalid_rules_total{kind, policy_name, namespace, reason}`: counter; incremented when a rule fails reconcile-time validation.
- `squirrel_unparseable_images_total{namespace}`: counter; incremented when an admission image fails `pkg/name` parsing.
- `squirrel_invalid_target_renders_total{policy_kind, policy_name, namespace}`: counter; incremented when a rewrite rule's target produces an invalid render at admission time.
- `squirrel_webhook_admission_duration_seconds`: histogram; per-request handler duration.

### Health

The operator exposes `/healthz` (liveness) and `/readyz` (readiness) on a separate port from `/metrics` and from the webhook.
Readiness becomes true once the informer caches have synchronised; liveness becomes false on unrecoverable internal errors.

### Status

Per-policy status conditions are described in the API Reference section.
`status.observedGeneration` tracks the spec generation the reconciler has finished evaluating.

### Annotations

Mutated pods carry one annotation per rewritten container, keyed by container name under a dedicated prefix:

```text
original-image.squirrel.molier.dev/<containerName>: <originalImage>
```

For a Pod whose `main` container was rewritten from `docker.io/library/nginx:1.21` to `mirror.internal/dockerhub/library/nginx:1.21` and whose `init` initContainer was rewritten from `docker.io/busybox:1.36` to `mirror.internal/dockerhub/library/busybox:1.36`, the resulting annotations are:

```text
original-image.squirrel.molier.dev/main: docker.io/library/nginx:1.21
original-image.squirrel.molier.dev/init: docker.io/busybox:1.36
```

Container names are unique within a Pod across `spec.containers`, `spec.initContainers`, and `spec.ephemeralContainers`, so a single prefix without a kind discriminator is unambiguous.
The annotations are informational and useful for `kubectl describe pod` debugging.
The operator does not read them back as a source of truth for what to do — it stamps them on every rewrite and strips any user-forged variants on CREATE and on UPDATE.

## Test plan

Per ADR-0004 the project follows TDD; the design assumes tests are written ahead of the corresponding production code.

### Unit tests

- `internal/imageref` — table-driven tests over many inputs covering glob alphabet edges, default-field substitution, every placeholder including the `:shortN` and `:flat` variants, digest passthrough, and `index.docker.io`/`docker.io` canonicalisation.
- `internal/engine` — table-driven tests over many rule sets covering skip-first behaviour, priority defaulting from glob length, explicit priority override, tie-break ordering, namespace-over-cluster precedence, and "rule doesn't apply" fallthrough.
- `internal/certs` — round-trips for CA generation, Secret persistence, MWC patching, and rotation.

### envtest

- Reconciler tests for both kinds: spec validation, status transitions, and re-reconcile after spec edits.
- Reconciler rejection of empty rules, unknown placeholders, missing registry, skip+target conflict, and `{digest}` inside `tags`.

### Admission tests

- Fake admission requests against the webhook handler with table-driven cases covering every user story.
- Tests covering CREATE and UPDATE, including the UPDATE-bypass scenario (create-then-patch).

### Integration

- A kind-based smoke test in CI exercises one full policy lifecycle: install the operator, create a `ClusterImagePolicy`, schedule a pod, observe the annotation, delete the policy, observe pods are no longer rewritten.

## Graduation criteria

### v1alpha1 (current)

- API is unstable; breaking changes permitted between alpha releases.
- Multi-replica deployment (default 2) with leader election for the reconcilers and per-replica webhook serving.
  Leader election was promoted from v1beta1 into v1alpha1 because the manifests already required two replicas to keep webhook availability through routine maintenance, and leader-election RBAC is a single Lease on `coordination.k8s.io`.
- No conversion webhook.

### v1beta1

- API is frozen apart from additive, backwards-compatible changes.
- Conversion webhook in place for `v1alpha1` ↔ `v1beta1`.
- Field-level defaulting via OpenAPI in the CRD.
- Scale targets: 1000 policies, 1000 namespaces, 10 000 pods/minute admission throughput.

### v1

- API stable; deprecated fields removed only after at least one beta release.
- Production readiness criteria met as documented in the v1 design refresh.
- Operator SLA documented (admission latency p99, controller queue depth).

## Alternatives considered

### String-glob form for the action target

An earlier version of the design allowed the target to be expressed as a single glob string with positional wildcard substitution, mirroring the match.
This was rejected because the substitution semantics for mismatched wildcard counts had no clean answer and would have invited subtle misuse.
The structured target with named placeholders is verbose for the simplest cases but unambiguous everywhere else.

### Field-level passthrough as the opt-out mechanism

An earlier version proposed expressing opt-out by a target whose every field was the identity template (`{registry}/{repository}:{tag}`).
This was replaced with the explicit `skip` action, which is shorter to write and clearer in intent.

### Unified CRD with optional namespace field

A single CRD with an optional `metadata.namespace` field would have collapsed the two kinds.
This was rejected because RBAC, controller scope, and `kubectl` ergonomics are all cleaner when cluster-scoped and namespaced resources are different kinds.

### Kyverno or Gatekeeper instead of a dedicated operator

Both general-purpose policy engines can rewrite images.
A dedicated operator was chosen because the resulting policies are more legible, the validation is specific to the rewrite domain (placeholders, target completeness, action enum), and the observability surface is tuned to the use case.

### Per-policy required namespaceSelector

An earlier version of the design required `ClusterImagePolicy.spec.namespaceSelector` to be non-empty as the safety gate.
This was relaxed once the MWC-level opt-in label was introduced: with namespaces required to set `squirrel.molier.dev/enabled=true` before the webhook runs at all, the per-policy selector reverts to its natural role as a narrowing filter and may be omitted (meaning "every opted-in namespace").

## Drawbacks and known limitations

- No high availability in v0.1.0.
  A single-replica deployment is a single point of failure for the rewrite path; the `failurePolicy: Ignore` mitigation means pods admit unchanged if the operator is down.
- No splicing into repository segments.
  The structured target can replace a whole component but cannot prefix or splice into the middle of one without introducing a new placeholder.
- Digest is immutable in the output.
  This is a security property (see Digest Integrity) but it means policies cannot legitimately point a digest-pinned image at different content, even when the operator would otherwise want to.
- Per-admission webhook latency.
  Every pod admission in opted-in namespaces costs one webhook round-trip; the duration histogram tracks the impact.
- The operator's own pod and namespace must remain unaffected by its own policies; this is enforced by the MWC's `kubernetes.io/metadata.name NotIn` clause.

## Future work

- A `cache` action that uses the rest of the `tags` list (the `rewrite` action only consults the first applicable candidate).
- High-availability deployment with leader election for the controllers and the webhook server running on every replica.
- Additional placeholders, particularly registry-related ones such as `{registry:host}` and `{registry:port}`, once a concrete use case appears.
- Conversion webhook and graduation to `v1beta1`.
- Operator-side configurability of the opt-in label (`squirrel.molier.dev/enabled`).
- Splicing placeholders for repository segments if the use cases warrant the API growth.

## References

- ADR-0001: Markdown for documentation (`docs/decision/0001-use-markdown-for-documentation.md`).
- ADR-0002: MADR 3.0 for architecture decisions (`docs/decision/0002-use-madr-3-0-for-architecture-decisions.md`).
- ADR-0003: KEP-shaped design documents (`docs/decision/0003-use-kep-for-design-documents.md`).
- ADR-0004: Test-driven development (`docs/decision/0004-use-test-driven-development.md`).
- ADR-0005: golangci-lint for code quality (`docs/decision/0005-use-golangci-lint-for-code-quality.md`).
- KEP template: <https://github.com/kubernetes/enhancements/tree/master/keps/NNNN-kep-template>.
- `go-containerregistry/pkg/name`: <https://pkg.go.dev/github.com/google/go-containerregistry/pkg/name>.
- Sigstore policy-controller: <https://docs.sigstore.dev/policy-controller/overview/>.
- OCI distribution spec: <https://github.com/opencontainers/distribution-spec>.
