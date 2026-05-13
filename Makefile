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
	$(GO) build ./...

.PHONY: tidy
tidy:
	$(GO) mod tidy

.PHONY: kubebuilder
kubebuilder:
	$(GO) tool kubebuilder $(ARGS)

# markdownlint is a Node.js tool. The custom rules (title-case-style
# and max-one-sentence-per-line) live in package.json devDependencies;
# `npm ci` installs them into node_modules so the markdownlint config's
# customRules lookup succeeds. The Make target is the same one CI runs.
.PHONY: markdownlint
markdownlint:
	npm ci --no-audit --no-fund
	npx markdownlint-cli2

