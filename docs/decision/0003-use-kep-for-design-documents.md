---
status: accepted
date: 2026-05-13
decision-makers: [RemkoMolier]
---

# Use KEP-shaped design documents to describe the system design

## Context and problem statement

ADRs capture individual decisions in isolation, but a Kubernetes operator also needs a place to describe the design as a whole: the CRD surface, the resolution semantics, the webhook behaviour, the security model, the alternatives that were rejected at the design level.
A single document per API version gives readers a coherent view that scattered ADRs cannot provide.
A format for these design documents must be chosen before the first design document is written.

## Decision drivers

- The format must be familiar to contributors from the Kubernetes operator ecosystem.
- The document must be versionable alongside the API surface it describes.
- The document must cover both intent (Motivation, User Stories, Alternatives) and detail (API Reference, Resolution Semantics, Validation).
- The document must complement ADRs without overlapping their role.

## Considered options

- KEP-shaped design documents, one per API version (<https://github.com/kubernetes/enhancements/tree/master/keps/NNNN-kep-template>)
- RFC-style standalone proposals (Rust RFCs, Python PEPs)
- API reference auto-generated from CRD types only, with no narrative design document
- Brief design discussion in the project README

## Decision outcome

Chosen option: "KEP-shaped design documents", because the format is purpose-built for Kubernetes API surfaces and is the de-facto convention across the operator ecosystem (Cluster API, cert-manager, Knative, OpenTelemetry, Helm).

Design documents live in `docs/design/`.
Each file is named after the API version it describes, for example `v1alpha1.md` or `v1beta1.md`.
A new file is created when a new API version is introduced; the old version's document is retained as long as the API version is still supported.

The KEP outline is followed, slimmed to the sections relevant for an out-of-tree operator:

- Summary
- Motivation (Goals, Non-Goals)
- User Stories
- API Reference
- Resolution Semantics
- Validation
- Webhook Behaviour
- Architecture
- Security and Safety
- Observability
- Test Plan
- Graduation Criteria
- Alternatives Considered
- Drawbacks and Known Limitations
- Future Work
- References

The Kubernetes-project-specific sections of the canonical KEP template (Production Readiness Review, Version Skew Strategy, Infrastructure Needed) are omitted; they apply to in-tree changes, not to an out-of-tree operator that ships its own release artefacts.

### Consequences

- Good, because the structure is recognisable to anyone who has read Kubernetes enhancement proposals.
- Good, because the document is versioned with the API, so historical readers can find the design that matches the version they are running.
- Good, because the Alternatives Considered section captures design-level rejections, complementing the ADRs which capture process-level rejections.
- Bad, because the document is heavier than the README that readers may initially reach for.
- Bad, because some sections (Graduation Criteria) feel premature when the only API version is `v1alpha1`.

### Confirmation

Each new API version lands with a corresponding `docs/design/<version>.md` in the same pull request or one immediately preceding it.
Reviewers check that the design document references each ADR whose decision it implements, and that the API surface in code matches the API Reference section of the design document.

## Pros and cons of the options

### KEP-shaped design documents

- Good, because the template is well-tested and battle-hardened across many operators.
- Good, because the sections map directly to the questions a new adopter asks.
- Bad, because the canonical template has more sections than a small operator strictly needs; some pruning is required.

### RFC-style standalone proposals

- Good, because they handle cross-cutting features that affect many components.
- Bad, because they are tuned for language-level evolution rather than for operator API surfaces.
- Bad, because the structure does not map as cleanly onto Kubernetes concerns (resource scope, admission, RBAC).

### API reference auto-generated from CRD types only

- Good, because there is no duplication between the code and the reference.
- Bad, because it documents only the surface, not the semantics; users need the semantics to use the API correctly.
- Bad, because it cannot capture alternatives considered or the rationale for the chosen shape.

### Brief design discussion in the project README

- Good, because new contributors read the README first.
- Bad, because the README must serve many audiences (install, quick start, contributing) and design detail crowds those audiences out.
- Bad, because the README is not naturally versioned per API.

## More information

The KEP template lives at <https://github.com/kubernetes/enhancements/tree/master/keps/NNNN-kep-template>.
This ADR establishes that the design lives in `docs/design/v1alpha1.md` (and successor files for later versions); ADRs continue to live in `docs/decision/`.
The first design document, `docs/design/v1alpha1.md`, will be written in a subsequent change and is not part of this commit.
