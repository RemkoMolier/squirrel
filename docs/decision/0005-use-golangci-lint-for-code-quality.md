---
status: accepted
date: 2026-05-13
decision-makers: [RemkoMolier]
---

# Use golangci-lint with a curated linter set for code quality

## Context and problem statement

Static analysis catches whole categories of bug before code reaches review.
Go's standard `go vet` covers a narrow set of issues, and `gopls` extends that set in editors so contributors already see many warnings while writing code.
Continuous integration must catch at least the same issues that `gopls` shows in editors, plus the broader set used across mature Kubernetes ecosystem operator repositories, so that nothing surfaces only at review time.
The project needs a single tool to configure, invoke locally, and run in CI.

## Decision drivers

- The set of checks run in CI must be a superset of what `gopls` already shows in the IDE, so contributors do not see warnings in CI that they did not see while authoring.
- The set must match conventions from comparable Kubernetes operator repositories (controller-runtime, Cluster API, cert-manager, kueue) so that contributors familiar with the ecosystem see familiar warnings.
- The tool must be invokable identically locally and in CI, with a single source of truth for configuration.
- The configuration must be reviewable; the chosen linters and their rationale belong in source control next to the code they constrain.
- The set must be tractable; linters whose false-positive rate exceeds their true-finding rate are excluded.

## Considered options

- `golangci-lint` with a curated linter set in `.golangci.yaml`
- `staticcheck` alone (the strongest single linter outside `go vet`)
- `go vet` alone (the standard Go toolchain only)
- Per-linter invocation in CI without an umbrella tool
- No linting

## Decision outcome

Chosen option: "golangci-lint with a curated linter set", because it is the only option that runs one configuration file across many linters, matches the practice of every comparable Kubernetes operator project, and can be configured to be a strict superset of the analyzers `gopls` runs by default.

The configuration lives at the repository root in `.golangci.yaml`.
The curated linter set is selected against three rules:

- Stay close to the baselines used by controller-runtime, Cluster API, cert-manager, and kueue.
- Enable every linter whose analyzer is also enabled in `gopls`'s default set, so CI cannot fail on an issue the IDE did not surface.
- Reject any linter whose false-positive rate dominates its true-finding rate on this codebase.

The starting set is the golangci-lint default plus the following additions, with rationale:

- `errcheck`, `govet` (with the `shadow` check), `gosimple`, `ineffassign`, `staticcheck`, `unused`: the golangci-lint default; covers the core `go vet` and `staticcheck` analyzers that `gopls` shows by default.
- `bodyclose`: enforces closing of HTTP response bodies; relevant once the webhook server or any outbound HTTP is introduced.
- `contextcheck`: enforces that `context.Context` is propagated through call chains; controller and webhook code is context-heavy.
- `errorlint`: catches `fmt.Errorf` calls that should use `%w`, and `errors.Is` / `errors.As` misuses; matches what `gopls` flags via the `errorsas` analyzer.
- `exhaustive`: required because the action enum (`rewrite`, `skip`, future `cache`) is the kind of switch where a missing case is a defect.
- `gocheckcompilerdirectives`: validates `//go:` directives at compile boundaries.
- `gocritic`: bundles a broad set of best-practice checks used widely in Kubernetes-adjacent projects.
- `gosec`: surface for security-relevant issues such as command-injection patterns or weak TLS configuration; the webhook server runs TLS, so this matters.
- `importas`: enforces consistent import aliases; the Kubernetes ecosystem relies on this heavily (`metav1`, `corev1`, etc.) and inconsistent aliasing makes diff review noisier.
- `misspell`: catches typos in identifiers, comments, and string literals.
- `nakedret`: bans naked returns in long functions; reduces a class of subtle bugs in reconciler code.
- `nolintlint`: validates `//nolint:` directives, so suppressions remain narrow and justified.
- `prealloc`: surfaces slices that should be preallocated; relevant for hot paths in the matching engine.
- `predeclared`: prevents shadowing of built-in identifiers such as `len`, `new`, `error`.
- `revive`: replaces `golint`; configured to a moderate ruleset (exported-identifier docs, var-naming, package-comments).
- `stylecheck`: stylistic checks beyond `gosimple`.
- `thelper`: enforces `t.Helper()` in test helpers so failures point at the right call site.
- `unconvert`: removes redundant type conversions.
- `unparam`: surfaces unused function parameters.
- `whitespace`: catches stray leading or trailing whitespace inside functions.

Coverage of the `gopls` default analyzer set is achieved by the combination of `govet` (with `shadow`), `staticcheck`, `gosimple`, `unused`, `ineffassign`, `predeclared`, and `revive`.
Specific `gopls` analyzers (`printf`, `nilness`, `copylocks`, `lostcancel`, `unusedresult`, `deprecated`, `shift`, `structtag`, `unmarshal`, `unreachable`, `unsafeptr`, `unusedwrite`, `unusedparams`, `simplifyrange`, `simplifyslice`, `simplifycompositelit`, `useany`) all map to checks within the linters above.
The `.golangci.yaml` file lists these mappings in a comment block so future contributors can audit drift.

### Consequences

- Good, because `gopls` warnings in the IDE match CI failures, so no submit produces surprise lint output.
- Good, because the shared baseline with controller-runtime, Cluster API, cert-manager, and kueue lowers the onboarding cost for ecosystem contributors.
- Good, because the configuration is a single file in source control, reviewable like any other code.
- Good, because `nolintlint` keeps suppressions honest by requiring a justification on every `//nolint:` directive.
- Bad, because the curated set must be kept current as both `golangci-lint` and `gopls` evolve; a periodic review is required to keep the mapping accurate.
- Bad, because some legitimate code still requires `//nolint:` suppressions for specific findings, which adds review burden when those suppressions are added.

### Confirmation

CI runs `golangci-lint run ./...` and the build fails on any lint error.
The repository ships editor configuration (`.vscode/settings.json` and equivalents) that points `gopls` at the same analyzer set, so the IDE and CI converge.
A periodic review (at least once per minor release of `golangci-lint` or `gopls`) updates the curated set; the review is itself recorded as a follow-up ADR if it changes the set materially.

## Pros and cons of the options

### golangci-lint with a curated linter set

- Good, because the umbrella tool runs many linters with one configuration file.
- Good, because the project's linter choices are documented and reviewable.
- Good, because the tool is widely used across Kubernetes ecosystem operators, so the configuration patterns are well-trodden.
- Bad, because the curated set is now another piece of the project that must be maintained.

### staticcheck alone

- Good, because `staticcheck` is the single highest-signal Go linter and needs no per-project configuration.
- Bad, because it does not cover the broader categories (context propagation, error wrapping, exhaustive switches) that the linters above add.

### go vet alone

- Good, because it is part of the Go toolchain and needs no extra dependency.
- Bad, because it covers only a narrow set of issues; CI would still fail to catch many problems `gopls` already shows in editors.

### Per-linter invocation in CI without an umbrella tool

- Good, because each linter can be updated independently.
- Bad, because the CI configuration grows with each linter, and the linter list lives in CI rather than source.
- Bad, because there is no shared cache across linters; CI runs are slower than the umbrella case.

### No linting

- Good, because there is no process overhead.
- Bad, because every category of issue these linters catch arrives instead at code review or in production.

## More information

The reference configurations the curated set draws from are:

- controller-runtime: <https://github.com/kubernetes-sigs/controller-runtime/blob/main/.golangci.yml>
- Cluster API: <https://github.com/kubernetes-sigs/cluster-api/blob/main/.golangci.yml>
- cert-manager: <https://github.com/cert-manager/cert-manager/blob/master/.golangci.yaml>
- kueue: <https://github.com/kubernetes-sigs/kueue/blob/main/.golangci.yaml>

The `gopls` default analyzer set is documented at <https://github.com/golang/tools/blob/master/gopls/doc/analyzers.md>.
The `.golangci.yaml` file itself will land alongside the first Go code in the repository; this ADR establishes the policy, the file establishes the configuration.
