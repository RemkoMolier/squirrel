#!/usr/bin/env bash
# Orchestrates the kind-based e2e flow: boot an ephemeral cluster,
# load the manager image, apply config/default, wait for the operator
# Deployment to be Ready, then run the Go-side spec under the `e2e`
# build tag. CI calls this script via `make e2e-kind`; local devs do
# the same. Set KEEP_CLUSTER=1 to skip teardown on success (handy
# when triaging a failed run on a still-running cluster).
set -euo pipefail

KIND_CLUSTER=${KIND_CLUSTER:-squirrel-e2e}
# Default IMG matches config/manager/deployment.yaml's image: field so
# the Deployment finds the kind-loaded image without a kustomize image
# override. Both Makefile docker-build and this script honour the
# override; keeping the default aligned with the manifest is what makes
# `make e2e-kind` work zero-config.
IMG=${IMG:-ghcr.io/remkomolier/squirrel:dev}
KEEP_CLUSTER=${KEEP_CLUSTER:-0}

# Tools we depend on. Fail-fast with a clear hint rather than letting
# the script die mid-way through a long-running operation.
require() {
  local bin=$1 hint=$2
  if ! command -v "$bin" >/dev/null 2>&1; then
    echo "e2e-kind: '$bin' not on PATH. $hint" >&2
    exit 2
  fi
}
require kind "Install kind 0.24+ (https://kind.sigs.k8s.io/) or set up via mise."
require kubectl "Install kubectl matching .tool-versions or set up via mise."
require docker "Docker daemon must be reachable for kind."
require go "Go 1.26+ from .tool-versions."

cleanup() {
  if [ "$KEEP_CLUSTER" = "1" ]; then
    echo "e2e-kind: KEEP_CLUSTER=1; leaving cluster $KIND_CLUSTER and resources in place." >&2
    return
  fi
  echo "e2e-kind: tearing down cluster $KIND_CLUSTER" >&2
  kind delete cluster --name "$KIND_CLUSTER" >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "e2e-kind: creating kind cluster $KIND_CLUSTER"
kind create cluster --name "$KIND_CLUSTER" --wait 60s >/dev/null

# Use the kind cluster's kubeconfig for everything that follows.
KUBECONFIG=$(kind get kubeconfig --name "$KIND_CLUSTER")
export KUBECONFIG_CONTENT=$KUBECONFIG
KUBECONFIG_FILE=$(mktemp)
echo "$KUBECONFIG" > "$KUBECONFIG_FILE"
export KUBECONFIG=$KUBECONFIG_FILE

echo "e2e-kind: loading manager image $IMG into kind"
kind load docker-image "$IMG" --name "$KIND_CLUSTER" >/dev/null

echo "e2e-kind: applying config/default"
# `kubectl kustomize ... | apply -f -` rather than `apply -k` so the
# command surface matches what the CI manifests-static-analysis job
# exercises (which renders kustomize via stdin too).
kubectl kustomize config/default | kubectl apply --server-side=true --field-manager=squirrel-e2e -f -

echo "e2e-kind: waiting for operator Deployment to be Available"
# 5 min cap: cluster scheduling + image pull (from local kind cache,
# instant) + cert bootstrap + reconciler warm-up + Deployment rollout
# completes well under this in steady state; CI runners under load
# occasionally need 2-3 min.
kubectl -n squirrel-system rollout status deployment/squirrel-manager --timeout=5m

echo "e2e-kind: waiting for MWC caBundle to be populated"
# The leader's Authoritative tick patches the MWC caBundle as part of
# bootstrap; until that completes the apiserver doesn't trust the
# webhook and admissions silently fall through failurePolicy=Ignore.
# Poll until the bundle is non-empty.
for _ in $(seq 1 60); do
  bundle=$(kubectl get mutatingwebhookconfiguration squirrel-image-rewrite -o jsonpath='{.webhooks[0].clientConfig.caBundle}' 2>/dev/null || true)
  if [ -n "$bundle" ]; then
    break
  fi
  sleep 2
done
if [ -z "$bundle" ]; then
  echo "e2e-kind: MWC caBundle never populated; operator logs below" >&2
  kubectl -n squirrel-system logs -l app.kubernetes.io/name=squirrel-manager --tail=200 >&2 || true
  exit 1
fi

echo "e2e-kind: running e2e specs"
go test -tags=e2e -count=1 -timeout=10m ./test/e2e/...
