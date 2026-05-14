# Installing squirrel

This document covers installing squirrel into an existing Kubernetes cluster.
For the high-level pitch and a quick example, see [`README.md`](./README.md);
for policy authoring, see [`USAGE.md`](./USAGE.md).

## Prerequisites

- A Kubernetes cluster running 1.33 or newer.
  Older versions are not a tested target.
- `kubectl` matching the cluster: at least one of the two minor versions
  Kubernetes' upstream skew policy supports (so 1.32, 1.33, or 1.34 against
  a 1.33 cluster).
  Older `kubectl` releases predate the embedded kustomize features the
  manifests rely on and fall outside the supported skew band.
- For cert-manager mode only: [cert-manager](https://cert-manager.io/) v1.0+
  installed in the cluster.
- Cluster-admin permissions during the install.
  The operator creates a Namespace, a ClusterRole, a ClusterRoleBinding, two
  CRDs, and a MutatingWebhookConfiguration — all cluster-scoped resources.

## Choosing a cert mode

Squirrel needs a TLS certificate to serve the webhook on.
Two modes are supported:

- **Self-signed** (default).
  The operator mints its own CA, issues a serving certificate from it, stores
  both in a Secret, and writes them to a volume mount inside its own pod.
  Rotation happens automatically when the serving cert is within 30 days of
  expiry.
  No cluster-wide CA infrastructure required.
- **cert-manager**.
  The operator delegates certificate lifecycle to cert-manager, which is
  configured to issue the webhook serving cert into a Secret the operator's
  pod mounts.
  The MWC's caBundle is kept current by cert-manager's `inject-ca-from`
  annotation.
  Useful when the cluster already standardises on cert-manager for serving
  certificates.

Pick self-signed unless cert-manager is already in the cluster.
Both modes target the same final state — only the source of the certificate
material differs.

## Self-signed install

```sh
kubectl apply -k config/default
```

Watch the rollout:

```sh
kubectl -n squirrel-system rollout status deployment/squirrel-manager
```

Check the manager logs once the rollout completes:

```sh
kubectl -n squirrel-system logs deployment/squirrel-manager
```

A clean start looks like the controller-runtime boot sequence:
the webhook server starts, the informer caches sync, leader
election completes, and reconcilers start.
Look for log lines along the lines of:

```text
INFO    setup    starting manager
INFO    controller-runtime.webhook    starting webhook server
INFO    controller-runtime.webhook    Serving webhook server  {"host": "", "port": 9443}
INFO    Starting Controller    {"controller": "clusterimagepolicy"}
INFO    Starting Controller    {"controller": "imagepolicy"}
INFO    successfully acquired lease squirrel-system/squirrel.molier.dev
```

(Exact strings come from controller-runtime; the operator does not
add its own `bootstrap-certs` line, so do not look for one.)

Confirm the MutatingWebhookConfiguration is in place and has a caBundle:

```sh
kubectl get mutatingwebhookconfiguration squirrel-image-rewrite -o yaml | grep caBundle
```

## cert-manager install

Install cert-manager first if it is not already present.
The official installation guide lives at
<https://cert-manager.io/docs/installation/>.

```sh
kubectl apply -k config/overlays/cert-manager
```

The overlay adds:

- An `Issuer` named `squirrel-selfsigned-issuer` in the operator's namespace.
  Production deployments typically replace this with a `ClusterIssuer` pointing
  at the organisation's CA;
  see the cert-manager docs for the supported issuer types.
- A `Certificate` named `squirrel-webhook-serving-cert` that issues the webhook
  serving material into a Secret named `squirrel-webhook-tls`.
- The `cert-manager.io/inject-ca-from` annotation on the MWC so cert-manager
  rewrites `caBundle` whenever the Certificate rotates.
- A patch on the Deployment that switches the operator to
  `--cert-source=cert-manager` and mounts the cert-manager-managed Secret in
  place of the default emptyDir.

Once applied, watch the rollout the same way as the self-signed install:

```sh
kubectl -n squirrel-system rollout status deployment/squirrel-manager
```

## Verifying the install

Opt a test namespace in to the operator and apply a no-op policy:

```sh
kubectl create namespace squirrel-smoke-test
kubectl label namespace squirrel-smoke-test squirrel.molier.dev/enabled=true

cat <<'EOF' | kubectl apply -f -
apiVersion: squirrel.molier.dev/v1alpha1
kind: ClusterImagePolicy
metadata:
  name: smoke-test
spec:
  defaultTarget:
    registry: registry.k8s.io
  rules:
    - match: docker.io/library/pause:*
      action: skip
EOF
```

Check that the operator accepted the policy:

```sh
kubectl get clusterimagepolicy smoke-test -o yaml | grep -A 3 conditions:
```

The expected output is `status: "True"` with `reason: Accepted`.

Tear down the smoke test:

```sh
kubectl delete clusterimagepolicy smoke-test
kubectl delete namespace squirrel-smoke-test
```

## Build from source

The operator ships as a `linux/amd64` and `linux/arm64` image on
`ghcr.io/remkomolier/squirrel`.
To build a fresh image from the current branch:

```sh
make build                                 # binary into bin/squirrel-manager
docker build -t my-registry/squirrel:dev . # if a Dockerfile is present
```

(The container image build is out of scope for this document; the
release-engineering pipeline handles publishing.)

Override the image in your install by editing the kustomize overlay:

```sh
cd config/default
kustomize edit set image ghcr.io/remkomolier/squirrel=my-registry/squirrel:dev
kubectl apply -k .
```

## Uninstall

Remove the operator and the kustomize-managed resources:

```sh
kubectl delete -k config/default     # or config/overlays/cert-manager
```

The Namespace and the CRDs are deleted along with the operator.
**This also deletes every `ClusterImagePolicy` and `ImagePolicy` in the
cluster** — back them up first if you intend to reinstall later.

If you only want to disable the operator without removing the policies, delete
the Deployment and the MutatingWebhookConfiguration but keep the CRDs:

```sh
kubectl -n squirrel-system delete deployment squirrel-manager
kubectl delete mutatingwebhookconfiguration squirrel-image-rewrite
```
