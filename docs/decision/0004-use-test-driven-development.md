---
status: accepted
date: 2026-05-13
decision-makers: [RemkoMolier]
---

# Use test-driven development for production code

## Context and problem statement

The squirrel codebase will evolve through several API versions (`v1alpha1` to `v1beta1` to `v1`) and the surrounding implementation will be refactored repeatedly as the design matures.
Tests written after the code tend to confirm the implementation that exists rather than the requirements the code is supposed to meet; tests written first force the API to be designed from the caller's perspective and ensure every public behaviour has a regression test.
The project needs a development discipline that produces durable test coverage as a natural by-product of writing features and fixing bugs.

## Decision drivers

- The public API surface must be designed from the consumer's perspective, not retro-fitted to the implementation.
- Every documented behaviour must have a regression test so refactoring is safe.
- Bug fixes must include a regression test that proves the bug existed before the fix.
- Test coverage must accrue as a side-effect of feature work, not as a separate, deferrable activity.
- The discipline must be lightweight enough to apply to small changes.

## Considered options

- Test-driven development (red, green, refactor)
- Test-after-write (write the code first, add tests before merge)
- Behaviour-driven development with Gherkin-style specification files
- No formal testing discipline

## Decision outcome

Chosen option: "Test-driven development", because it is the only option that produces a failing test before the code that fixes it, which is the only mechanism that guarantees the test would have caught the bug it claims to cover.

TDD on this project is applied as the red-green-refactor loop at whatever level of the test pyramid is the right fit for the behaviour being introduced.

- For pure, unit-testable code (image-reference parsing, glob matching, target rendering, rule resolution), TDD means a failing unit test before the production code.
- For controller reconciliation logic, TDD means a failing envtest test before the reconciler change.
- For mutating-webhook admission behaviour, TDD means a failing admission test (driven by a fake `admission.Request`) before the handler change.

Bug fixes begin with a failing regression test that reproduces the bug on the unfixed code; the fix is the smallest change that makes that test pass.

Table-driven tests are the preferred style for testing pure functions, so the same test scaffolding can cover many input combinations and so that new cases can be added without restating set-up.

### Consequences

- Good, because the API surface is naturally consumer-friendly: the test is the first consumer.
- Good, because every documented behaviour has a regression test from the moment it lands.
- Good, because refactoring is safe; the existing tests catch regressions when code is restructured.
- Good, because reviewers can read the test alongside the implementation in the same pull request and confirm the change does what its commit message claims.
- Bad, because writing a failing test first requires discipline and is slower per change than writing code-then-tests.
- Bad, because some scaffolding (envtest harness, admission-request fakes) must exist before the corresponding TDD loop becomes practical; the cost of that scaffolding lands on the first contributor who needs it.

### Confirmation

Pull requests that introduce behaviour include the test that exercises that behaviour in the same change.
Pull requests that fix bugs include a regression test that fails without the fix and passes with it.
Reviewers check that the test was authored alongside the code, not after merging.
CI runs `go test ./...` and the build fails on any test failure.

## Pros and cons of the options

### Test-driven development

- Good, because the failing test is the executable specification of the change.
- Good, because the discipline guarantees regression coverage by construction.
- Bad, because it is slower per change than writing code first.
- Bad, because some categories of behaviour (UI, hard-to-fake external systems) are awkward to drive test-first, although this is a minor concern for an operator with no UI.

### Test-after-write

- Good, because each individual feature ships slightly faster.
- Bad, because tests written after the code tend to mirror the implementation rather than the requirements, so they fail to catch design mistakes.
- Bad, because once a feature works, the pressure to ship outweighs the pressure to backfill tests, so tests are often skipped under deadline.

### Behaviour-driven development with Gherkin

- Good, because the specification is readable by non-engineers.
- Bad, because the Gherkin layer adds tooling and grammar overhead with no extra precision for a developer-facing tool such as a Kubernetes operator.
- Bad, because the audience for behaviour specifications in this project is the engineers writing the code, who are better served by Go tests.

### No formal testing discipline

- Good, because there is no process overhead.
- Bad, because coverage will be uneven and lag behind code.
- Bad, because there is no defence against unintended regressions during refactoring.

## More information

This decision establishes the *discipline*; the tooling and CI surface that supports it are covered by ADR-0005 (golangci-lint and CI configuration).
The design document at `docs/design/v1alpha1.md` will reference the Test Plan section that this discipline produces.
