# squirrel - image-rewrite operator for Kubernetes 1.33+

GO ?= go

# Tools are declared in go.mod via the Go 1.24 `tool` directive
# (see ADR-0005 and the `tool (...)` block in go.mod). Invoke them
# through `go tool ...` so every contributor uses the pinned version.

.PHONY: all
all: fmt lint test build

.PHONY: test
test:
	$(GO) test ./...

.PHONY: test-race
test-race:
	$(GO) test -race -count=1 ./...

.PHONY: cover
cover:
	$(GO) test -coverprofile=coverage.out ./...
	$(GO) tool cover -html=coverage.out -o coverage.html

.PHONY: lint
lint:
	$(GO) tool golangci-lint run ./...

.PHONY: lint-fix
lint-fix:
	$(GO) tool golangci-lint run --fix ./...

.PHONY: fmt
fmt:
	$(GO) tool golangci-lint fmt ./...

.PHONY: build
build:
	$(GO) build -o bin/squirrel-manager ./cmd/manager

# build-all keeps the old `go build ./...` behaviour as a compile
# check across every package, kept under a separate target so CI can
# call it without producing a binary it does not need.
.PHONY: build-all
build-all:
	$(GO) build ./...

# envtest binaries used by the integration tests. The version is
# the K8s minor exposed via KUBEBUILDER_ASSETS; CI exercises a
# matrix (1.33 floor through latest stable) by overriding
# ENVTEST_K8S_VERSION on the make invocation.
ENVTEST_K8S_VERSION ?= 1.36.0

.PHONY: envtest
envtest:
	@echo "fetching envtest assets for k8s $(ENVTEST_K8S_VERSION)"
	@$(GO) tool setup-envtest use $(ENVTEST_K8S_VERSION) -p path

.PHONY: test-integration
test-integration: envtest manifests
	KUBEBUILDER_ASSETS="$$($(GO) tool setup-envtest use $(ENVTEST_K8S_VERSION) -p path)" \
	  $(GO) test -tags=integration -count=1 -race ./internal/manager/...

.PHONY: tidy
tidy:
	$(GO) mod tidy

.PHONY: kubebuilder
kubebuilder:
	$(GO) tool kubebuilder $(ARGS)

# controller-gen generates DeepCopy methods from `+kubebuilder:` markers on
# the API types and (in a future phase) CRD, RBAC, and webhook manifests.
# Both targets are idempotent; re-run after touching api/.
.PHONY: generate
generate:
	$(GO) tool controller-gen object paths=./api/...

.PHONY: manifests
manifests:
	$(GO) tool controller-gen \
		crd \
		rbac:roleName=squirrel-manager \
		webhook \
		paths=./... \
		output:crd:artifacts:config=config/crd/bases \
		output:rbac:artifacts:config=config/rbac \
		output:webhook:artifacts:config=config/webhook

# verify-manifests runs manifests and fails when the working tree
# diverges from what was committed - the CI equivalent of "did the
# author forget to regenerate after touching a marker?".
.PHONY: verify-manifests
verify-manifests: manifests
	@if ! git diff --exit-code config/crd config/rbac config/webhook; then \
	  echo "Generated manifests are stale; run 'make manifests' and commit the result." >&2; \
	  exit 1; \
	fi

# verify is the core Go + manifest pre-PR check: lint,
# manifests-up-to-date, every unit test green. CI also runs
# envtest, markdownlint, and manifest-static-analysis on top -
# operators who want byte-identical CI semantics locally should
# pair `make verify` with `make test-integration` and
# `make markdownlint` (manifest static analysis additionally
# needs kubectl / kubeconform / kube-linter from .tool-versions).
.PHONY: verify
verify: lint verify-manifests test-race

# markdownlint is a Node.js tool. The custom rules (title-case-style
# and max-one-sentence-per-line) live in package.json devDependencies;
# `npm ci` installs them into node_modules so the markdownlint config's
# customRules lookup succeeds. The Make target is the same one CI runs.
.PHONY: markdownlint
markdownlint:
	npm ci --no-audit --no-fund
	npx markdownlint-cli2

