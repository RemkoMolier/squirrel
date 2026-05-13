---
status: accepted
date: 2026-05-13
decision-makers: [RemkoMolier]
---

# Use MADR 3.0 for architecture decision records

## Context and Problem Statement

The squirrel project will accumulate architectural decisions as it grows.
Future contributors need a reliable way to understand why choices were made and what alternatives were rejected, without having to reconstruct the discussion from chat logs or pull-request threads.
A format for capturing these decisions must be structured enough to be searchable and skimmable, yet light enough that contributors will actually write them.

## Decision Drivers

- The format must encourage capture of alternatives considered, not just the final decision.
- Each decision should be a small, focused artefact that can be reviewed in a single pull request.
- The format must be expressible in Markdown so that ADR-0001 applies to ADR files as well.
- The format must be familiar to contributors from the broader software architecture community.

## Considered Options

- MADR 3.0 (<https://adr.github.io/madr/>)
- Nygard's original ADR format (Context, Decision, Status, Consequences)
- Y-Statements (one-paragraph decisions in a fixed template)
- RFC-style standalone proposals (long-form, one decision per multi-page document)
- No ADRs; rely on commit messages and pull-request descriptions

## Decision Outcome

Chosen option: "MADR 3.0", because it produces decision records that are structured, complete, and short enough to be written under modest reviewer pressure.

ADR files live in `docs/decision/`.
Each file is named `NNNN-short-title.md`, where `NNNN` is a four-digit sequence number starting at `0001`.
A number is allocated when the ADR is merged; pull requests that conflict on a number are rebased as part of the merge process.
Each file uses the MADR 3.0 template with at least these sections present: Context and Problem Statement, Considered Options, Decision Outcome (including Consequences and Confirmation), and Pros and Cons of the Options.
The template's optional sections (Decision Drivers, More Information) are included whenever they add value.

ADR status follows the MADR vocabulary: `proposed`, `accepted`, `rejected`, `deprecated`, or `superseded by ADR-NNNN`.
A superseded ADR is never deleted; the superseding ADR links back to it and the superseded ADR is updated only to change its status line.

### Consequences

- Good, because new decisions land alongside the change that motivated them, with their alternatives documented.
- Good, because the file structure is greppable; a contributor can search `docs/decision/` for prior art on any topic.
- Good, because the MADR 3.0 template is widely recognised and contributors who have used it elsewhere need no onboarding.
- Bad, because the full template has more boilerplate than Nygard's original; very short decisions can feel over-structured.
- Bad, because writers may be tempted to skip the Pros and Cons section, hiding the analysis the format is designed to capture.

### Confirmation

Pull requests that introduce architectural decisions include a new ADR.
Reviewers verify that each new ADR uses the MADR 3.0 structure, has its required sections populated, and is numbered sequentially from the previous merged ADR.

## Pros and Cons of the Options

### MADR 3.0

- Good, because the template is explicit about capturing alternatives and consequences.
- Good, because each ADR is short enough to review alongside the code change it accompanies.
- Bad, because the template includes more headings than the simplest formats; it can feel heavy for a one-line decision.

### Nygard's original ADR format

- Good, because the structure is minimal (four headings) and very fast to write.
- Bad, because it does not require listing alternatives, so they are often omitted.
- Bad, because the absence of explicit Consequences and Confirmation sections leads to under-documentation of trade-offs.

### Y-Statements

- Good, because they are extremely compact (one paragraph each).
- Bad, because the compressed form loses the alternatives and the reasoning chain.

### RFC-style standalone proposals

- Good, because they handle large, multi-faceted decisions well.
- Bad, because they are too heavy for the small, recurring decisions that ADRs are designed for.
- Bad, because they overlap with the design-document role addressed in ADR-0003.

### No ADRs

- Good, because there is no documentation overhead.
- Bad, because architectural context is lost as soon as the original contributors move on.
- Bad, because reviewers cannot tell which prior decisions a change implicitly overturns.

## More Information

The MADR 3.0 template and rationale are at <https://adr.github.io/madr/>.
This ADR is itself written in MADR 3.0 format, so the format is self-documenting from ADR-0002 onwards.
ADR-0001 establishes the underlying Markdown conventions that ADR files follow.
ADR-0003 establishes the complementary format used for system-level design documents.
