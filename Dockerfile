# Multi-stage build for the squirrel-manager binary. The image is
# used by the `make e2e-kind` flow (and the matching CI job) to load
# the operator into an ephemeral kind cluster for end-to-end
# coverage. The runtime stage is the same distroless static-nonroot
# layer the production Deployment templates expect (UID/GID 65532)
# so what e2e exercises is what production runs.
#
# Build args:
#   - GO_VERSION: pinned to the same Go release .tool-versions sets,
#     so a local `docker build .` produces a binary byte-identical
#     to `go build ./cmd/manager` modulo the cross-compile flags.
#   - TARGETOS / TARGETARCH: BuildKit auto-populates these from
#     --platform; defaults match the e2e and the in-cluster runtime
#     (linux/amd64). Set --platform=linux/arm64 to cross-compile.

ARG GO_VERSION=1.26
FROM golang:${GO_VERSION} AS builder

WORKDIR /workspace

# Cache module downloads in a dedicated layer: a change to source
# files invalidates the COPY-source layer but not the go.mod / go.sum
# layer, so repeated builds during development avoid re-fetching the
# whole module graph.
COPY go.mod go.sum ./
RUN go mod download

COPY api/ api/
COPY cmd/ cmd/
COPY internal/ internal/

ARG TARGETOS=linux
ARG TARGETARCH=amd64

# Static, stripped, trimmed-path binary. CGO_ENABLED=0 + the
# distroless runtime base together produce a fully-static image with
# no glibc/musl dependency.
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags='-w -s' -o manager ./cmd/manager

# Runtime: distroless static + nonroot. No shell, no package manager.
# The UID and GID are pinned to 65532 to match the
# securityContext.runAsUser in config/manager/deployment.yaml; the
# e2e Pod must run with the same identity the production manifest
# documents so any UID-related regression (volume permissions,
# fsGroup) surfaces in CI rather than at first install.
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/manager /manager
USER 65532:65532
ENTRYPOINT ["/manager"]
