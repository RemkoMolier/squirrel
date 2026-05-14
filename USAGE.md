# Using squirrel

This document covers writing policies and observing what the operator does.
For installation, see [`INSTALL.md`](./INSTALL.md);
for the design rationale, see [`docs/design/v1alpha1.md`](./docs/design/v1alpha1.md).

## Opting a namespace in

Squirrel only mutates Pods in namespaces that explicitly opt in.
Label the namespace:

```sh
kubectl label namespace dev squirrel.molier.dev/enabled=true
```

The label is the safety gate.
Without it, no Pod in the namespace is touched, regardless of how many policies
match its images.
The Kubernetes-reserved namespaces (`kube-system`, `kube-public`,
`kube-node-lease`) and the operator's own namespace (`squirrel-system`) are
hard-coded out of scope and ignored even if the label is set.

## The two policy kinds

**`ClusterImagePolicy`** — cluster-scoped.
Optionally narrowed by a `namespaceSelector`;
when the selector is omitted, the policy applies to every opted-in namespace.

**`ImagePolicy`** — namespace-scoped.
Lives inside one namespace and applies only there.

Both kinds carry the same `spec.rules` shape and the same target template
grammar.
Both report their state through an `Accepted` status condition.
At admission time the operator merges all applicable policies, sorts rules by
priority, and rewrites each container's image according to the first matching
rule.

## A minimal policy

```yaml
apiVersion: squirrel.molier.dev/v1alpha1
kind: ClusterImagePolicy
metadata:
  name: dockerhub-mirror
spec:
  defaultTarget:
    registry: mirror.internal
    repository: dockerhub/{repository}
  rules:
    - match: "docker.io/**:*"
```

This rewrites every `docker.io` image in every opted-in namespace to
`mirror.internal/dockerhub/<original-repo>:<original-tag>`.
The `match` is a glob over the full image reference;
the rule has no `target` of its own, so it inherits `spec.defaultTarget`.

## Match expressions

A match can be expressed in two equivalent ways.

### Glob-string form

```yaml
- match: "docker.io/library/nginx:1.21"
```

The string is split on the standard OCI reference separators:
`<registry>/<repository>[:<tag>][@<digest>]`.
Each piece is a glob against the corresponding part of the image.

### Structured form

```yaml
- match:
    registry: docker.io
    repository: library/nginx
    tag: "1.21"
```

Equivalent to the string form above.
The structured form is preferred when any field contains characters that would
be ambiguous in the string form — most commonly a digest, which itself contains
a colon (`sha256:...`) that the string-form parser would split on.

### Glob alphabet

Both forms use the same glob alphabet (a strict subset of `gobwas/glob`):

| Pattern        | Matches                                                                |
| -------------- | ---------------------------------------------------------------------- |
| `*`            | Any sequence of characters not containing `/`.                         |
| `**`           | Any sequence of characters, including `/`.                             |
| `?`            | Exactly one character not equal to `/`.                                |
| `[abc]`        | Any one of the characters in the set.                                  |
| `[a-z]`        | Any one character in the range.                                        |
| `[!abc]`       | Any one character outside the set.                                     |
| `{a,b,c}`      | Any of the comma-separated alternatives (each itself a glob).          |
| `\<x>`         | Literal `<x>` (escapes a metacharacter).                               |

OCI reference syntax forbids every metacharacter, so a pattern that contains
one is unambiguously asking for the glob interpretation.

`?` and `[...]` require exactly one character — they do not match the empty
field on a digest-pinned image or an image with no digest.
Use `*` (or omit the field) to match the empty case.

## Target templates

A target produces the rewritten image:

```yaml
target:
  registry: mirror.internal
  repository: dockerhub/{repository}
  tag: "{tag}"            # the original tag, unchanged
```

The placeholders the operator substitutes:

| Placeholder | Substitution |
| --- | --- |
| `{registry}` | The original image's registry. |
| `{repository}` | The full repository path (e.g. `library/nginx`). |
| `{repository:owner}` | The first path segment (e.g. `library`). |
| `{repository:image}` | The last path segment (e.g. `nginx`). |
| `{repository:path}` | Repository minus the final `/<image>` (e.g. `library` for `library/nginx`, `a/b/c` for `a/b/c/nginx`, empty for a single-segment repo). |
| `{repository:flat}` | Repository with `/` replaced by `-` (e.g. `library-nginx`). |
| `{tag}` | The original tag (empty for digest-pinned images). |
| `{digest}` | The original digest (empty for tag-only images). |
| `{digest:hex}` | The digest hex bytes (no `sha256:` prefix). |
| `{digest:shortN}` | The first N hex bytes (e.g. `{digest:short8}`). |
| `{digest:algo}` | The digest algorithm (e.g. `sha256`). |

Digests on the input image are never overridden — even when no target template
references `{digest}`, the original digest passes through to the rewritten
image unchanged.

## Actions

Each rule has an `action`:

- `rewrite` (the default).
  Run the target template, produce the rewritten image, swap it in.
- `skip`.
  Match this image but leave it untouched — useful when a single image needs
  to bypass an otherwise-cluster-wide rule.

Skip rules are evaluated before rewrite rules, regardless of priority.
A skip rule that matches always wins.

## Priority and tie-breaking

When multiple `rewrite` rules match the same image, the operator picks the
one with the highest effective priority.
Priority defaults to the byte-length of the normalised match expression —
longer, more-literal patterns score higher and beat shorter wildcards.
An explicit `priority` field on a rule overrides the default.

Tie-breakers, in order:

1. Higher priority wins.
2. Namespaced `ImagePolicy` wins over `ClusterImagePolicy`.
3. Lexicographically earlier policy name wins.
4. Earlier rule index within the policy wins.

The order is deterministic;
two admissions of the same Pod against the same policy set always produce the
same rewritten image.

## Cookbook

### Mirror Docker Hub

```yaml
apiVersion: squirrel.molier.dev/v1alpha1
kind: ClusterImagePolicy
metadata:
  name: dockerhub-mirror
spec:
  defaultTarget:
    registry: mirror.internal
    repository: dockerhub/{repository}
  rules:
    - match: "docker.io/**:*"
```

### Let one upstream image through unchanged

Skip rules combine well with mirror policies — apply both, the skip rule wins
for the named image.

```yaml
apiVersion: squirrel.molier.dev/v1alpha1
kind: ClusterImagePolicy
metadata:
  name: allow-distroless
spec:
  rules:
    - match: "gcr.io/distroless/**:*"
      action: skip
```

### Per-namespace override

The team running the `payments` namespace wants their `nginx` image to come
from a hardened internal registry instead of the cluster-wide mirror:

```yaml
apiVersion: squirrel.molier.dev/v1alpha1
kind: ImagePolicy
metadata:
  namespace: payments
  name: hardened-nginx
spec:
  rules:
    - match:
        registry: docker.io
        repository: library/nginx
      target:
        registry: registry.internal
        repository: payments/nginx-patched
        tag: "{tag}"
```

`ImagePolicy` beats `ClusterImagePolicy` on tie-break, so this rule wins inside
`payments` while the cluster-wide mirror still applies everywhere else.

### Multi-tag fallback

The target's `tags` field is a list of templates;
the operator picks the first one that produces a valid OCI tag (so missing
substitutions can disqualify a candidate).

```yaml
target:
  registry: mirror.internal
  repository: dockerhub/{repository}
  tags:
    - "{tag}-mirrored"     # preferred: mirrored tag namespace
    - "{tag}"              # fallback: original tag
```

## Observing what the operator did

### Status conditions

Every policy carries an `Accepted` condition.
Either `True` (validation passed; rules are live) or `False` (one or more rules
were rejected — the policy is ignored at admission).

```sh
kubectl get clusterimagepolicy dockerhub-mirror -o yaml
```

```yaml
status:
  observedGeneration: 3
  conditions:
    - type: Accepted
      status: "True"
      reason: Accepted
      observedGeneration: 3
      message: all rules pass validation
```

The reason field surfaces the first error when validation fails;
the full list is in `message`.
The webhook only applies policies whose `Accepted=True` reflects the current
generation (`observedGeneration == metadata.generation`).
A spec edit that has not yet been reconciled does not take effect at
admission, even if the previous generation was accepted.

### Per-container annotation

Every rewritten container gets a dedicated annotation under the `original-image.squirrel.molier.dev/` prefix, with the container name as the suffix and the original image as the value:

```text
original-image.squirrel.molier.dev/<containerName>: <originalImage>
```

For a Pod whose `main` container was rewritten from `docker.io/library/nginx:1.21` to `mirror.internal/dockerhub/library/nginx:1.21`:

```text
original-image.squirrel.molier.dev/main: docker.io/library/nginx:1.21
```

Container names are unique within a Pod across `spec.containers`, `spec.initContainers`, and `spec.ephemeralContainers`, so a single prefix without a kind discriminator is unambiguous.

`kubectl describe pod` is the simplest way to read these annotations.

The annotations are informational — the webhook never reads them back.
They are deliberately omitted on the `pods/ephemeralcontainers` subresource (`kubectl debug`-style requests), where `metadata.annotations` is immutable; the rewrite still happens on the new ephemeral container, but no annotation is stamped.

## Troubleshooting

**The webhook is doing nothing in my namespace.**
Confirm the opt-in label is set:

```sh
kubectl get namespace dev -o jsonpath='{.metadata.labels}'
```

The MWC's `namespaceSelector` excludes anything without
`squirrel.molier.dev/enabled=true`.

**My policy shows `Accepted=False`.**
Read the message:

```sh
kubectl get clusterimagepolicy dockerhub-mirror -o jsonpath='{.status.conditions[?(@.type=="Accepted")]}'
```

The reason field names the first offence
(`InvalidMatch`, `InvalidPlaceholder`, `MissingRegistry`, etc.);
the message lists every error in the order the validator collected them.

**My policy is Accepted but the image is not being rewritten.**
Check the pod's per-container `original-image.squirrel.molier.dev/<containerName>` annotations; if there is no such annotation for the container in question, the rule did not match.
Common causes:

- The namespace is not opted in.
- A higher-priority `skip` rule matched the same image.
- A more-specific match in another policy outscores yours on the tie-break.
