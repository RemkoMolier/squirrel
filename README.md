# squirrel

A Kubernetes operator that rewrites container images on Pod admission according
to declarative policies.

## What it does

Squirrel intercepts Pod CREATE and UPDATE admissions and rewrites each
container's `image` field according to two new resource kinds:

- `ClusterImagePolicy` (cluster-scoped, optionally narrowed by a namespace
  selector) — declare a policy that applies across the cluster.
- `ImagePolicy` (namespace-scoped) — declare a policy that overrides the cluster
  defaults inside one namespace.

Each policy is a set of match-and-target rules.
Matches use a glob syntax over the four parts of an OCI reference (registry,
repository, tag, digest);
targets use simple placeholder templates to compose the rewritten image
(`{registry}`, `{repository}`, `{tag}`, `{digest}`).
A `skip` action lets the operator explicitly leave an image untouched.

The operator is purely declarative.
No runtime hook, no DaemonSet, no node-level component — only a controller and
a mutating admission webhook.

## Why

Image rewriting is a recurring operational need: redirecting `docker.io/*` to a
private mirror, letting one specific upstream image through unchanged,
overriding the cluster default for a single team's namespace.
These are doable today in general-purpose policy engines (Kyverno, Gatekeeper),
but they require the operator to author Rego or Kyverno DSL — and the resulting
policies are dense and hard to review.

Squirrel offers a smaller, purpose-built API for the image-rewriting case,
with validation, observability, and a glob syntax tuned to it.

## Status

`v1alpha1`.
The API kinds, the engine, the webhook, the cert-source layer, and the
kustomize overlays are in place; the operator runs and works against
Kubernetes 1.33+.
The API may change in incompatible ways before a `v1` GA.

## Install

The fastest path:

```sh
kubectl apply -k config/default
```

This deploys the operator in self-signed cert mode — the operator mints and
rotates its own webhook serving certificate.

For clusters that already run [cert-manager](https://cert-manager.io/), apply
the cert-manager overlay instead:

```sh
kubectl apply -k config/overlays/cert-manager
```

See [`INSTALL.md`](./INSTALL.md) for prerequisites, verification steps, and the
build-from-source path.

## Quick example

Opt the `dev` namespace in to the operator:

```sh
kubectl label namespace dev squirrel.molier.dev/enabled=true
```

Apply a cluster-wide rewrite that mirrors every `docker.io` image through
`mirror.internal/dockerhub`:

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

Now any Pod in `dev` whose image starts with `docker.io/` will have it rewritten — `docker.io/library/nginx:1.21` becomes `mirror.internal/dockerhub/library/nginx:1.21` — and the original image is recorded on the Pod under a per-container `original-image.squirrel.molier.dev/<containerName>` annotation.

The full policy authoring guide, including match-expression syntax, target
templates, and worked examples for common patterns, is in
[`USAGE.md`](./USAGE.md).
The detailed design rationale lives in
[`docs/design/v1alpha1.md`](./docs/design/v1alpha1.md).

## Architecture decisions

Significant design decisions are captured as MADR 3.0 records under
[`docs/decision/`](./docs/decision):

- [ADR-0001](./docs/decision/0001-use-markdown-for-documentation.md) — Markdown
  for documentation.
- [ADR-0002](./docs/decision/0002-use-madr-3-0-for-architecture-decisions.md) —
  MADR 3.0 for ADRs.
- [ADR-0003](./docs/decision/0003-use-kep-for-design-documents.md) — KEP shape
  for design docs.
- [ADR-0004](./docs/decision/0004-use-test-driven-development.md) — TDD.
- [ADR-0005](./docs/decision/0005-use-golangci-lint-for-code-quality.md) —
  golangci-lint v2.
- [ADR-0006](./docs/decision/0006-kustomize-layout-default-and-overlays.md) —
  kustomize default + cert-manager overlay layout.

## Development

The project pins every contributor-facing tool version in
[`.tool-versions`](./.tool-versions): Go, Node (for markdownlint), kubectl,
kubeconform, and kube-linter.
CI uses the same file via `jdx/mise-action`, so local runs and CI runs share
byte-identical toolchains.

If you have [mise](https://mise.jdx.dev/) or asdf installed:

```sh
mise install         # or `asdf install`
```

Without mise/asdf, install the versions in `.tool-versions` manually.
The `make verify` target documents the core local pre-PR checks; CI
additionally runs envtest, markdownlint, and manifest static analysis
on top.

```sh
make help        # list available targets
make test        # run unit tests
make lint        # run golangci-lint
make manifests   # regenerate CRDs / RBAC / MWC
make build       # build the manager binary
make verify      # core Go + manifest checks (subset of CI)
```

Per [ADR-0004](./docs/decision/0004-use-test-driven-development.md) the project
follows test-driven development; every change ships with the tests that pin
its contract.

## License

Licensed under the [Apache License, Version 2.0](./LICENSE).
