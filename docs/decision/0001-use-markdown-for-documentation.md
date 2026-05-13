---
status: accepted
date: 2026-05-13
decision-makers: [RemkoMolier]
---

# Use Markdown for documentation, optimised for diff-friendly review

## Context and Problem Statement

Documentation for the squirrel project lives in source control alongside code, so every change is reviewed in a pull request.
The chosen documentation format needs to make those reviews easy to read and easy to comment on at the line level.
A format that produces noisy or unfocused diffs on small edits will discourage contributors from documenting changes thoroughly.

## Decision Drivers

- Pull-request diffs of documentation changes must isolate the substantive edit and not produce reflow noise that obscures it.
- The format must render natively on GitHub and in common editors without extra tooling.
- Authoring friction must be low; the format must not require unusual editors or build steps.
- The format must be familiar to contributors from the Kubernetes operator ecosystem.

## Considered Options

- Markdown (CommonMark plus GitHub Flavoured Markdown) with semantic line breaks
- Markdown with hard-wrapped lines at 80 columns
- Markdown with one paragraph per line (no internal wrapping)
- AsciiDoc
- reStructuredText
- External wiki (GitHub wiki, Confluence, or similar)

## Decision Outcome

Chosen option: "Markdown (CommonMark plus GFM) with semantic line breaks", because it produces the cleanest line-level diffs without requiring any toolchain beyond what GitHub already provides.

A semantic line break places one sentence on its own line; sentences in the same paragraph are joined by Markdown's soft-break rule when the document is rendered.
Editing a single sentence touches exactly one line, so the diff isolates the substantive change.
Hard-wrapping at a fixed column width has the opposite effect: any in-paragraph edit can cascade into several adjacent lines as the paragraph reflows around the new text.
Putting an entire paragraph on a single line keeps diffs small in line count, but produces unreadable diff hunks and prevents reviewers from leaving line-anchored comments on individual sentences.

### Conventions adopted alongside this decision

- One sentence per line; see <https://sembr.org/> for the formal description.
- ATX-style headings (`# heading`), never setext (`heading` underlined with `=` or `-`).
- `-` as the unordered list marker, consistently; never `*` or `+`.
- Fenced code blocks (triple backtick) with an explicit language hint where one applies.
- No trailing whitespace on any line; every file ends with a single trailing newline.
- No hard wrapping of prose at any column width.

### Consequences

- Good, because pull-request diffs cleanly highlight which sentence was changed.
- Good, because GitHub renders the output natively, so no build step is required.
- Good, because the conventions are widely used across the Kubernetes ecosystem (Cluster API, cert-manager, Knative).
- Bad, because writer discipline is required; Prettier and similar formatters reflow prose by default and will undo semantic line breaks if applied to Markdown files.
- Bad, because some editors hide soft line breaks visually, so the convention is less obvious from rendered output alone.

### Confirmation

The convention is enforced socially at code review.
If a linter is introduced later, it must be configured to leave prose line length alone (in `markdownlint` terms, disable `MD013` or set `line-length: false`).
Any auto-formatter applied to Markdown files must be configured with the equivalent of Prettier's `proseWrap: preserve` to avoid reflow.

## Pros and Cons of the Options

### Markdown with semantic line breaks

- Good, because per-sentence diffs make review fast and reviewer comments precise.
- Good, because no external build tool is required to render or author.
- Bad, because contributors who default to wrapping at 80 columns must adjust their habits.

### Markdown with hard-wrapped lines at 80 columns

- Good, because the source file is comfortable to read in narrow terminals.
- Bad, because editing a single sentence often reflows the entire paragraph, producing diffs that span unrelated lines.
- Bad, because reviewers cannot tell at a glance which sentence within a paragraph actually changed.

### Markdown with one paragraph per line

- Good, because a paragraph-level edit touches at most one line in the diff.
- Bad, because reviewers cannot anchor line comments on individual sentences within a paragraph.
- Bad, because very long source lines hamper readability when viewing the raw file.

### AsciiDoc

- Good, because it supports richer features such as admonitions, conditional content, and multi-file includes.
- Bad, because GitHub's rendering of AsciiDoc is less complete than its rendering of Markdown.
- Bad, because fewer contributors are fluent in the syntax.

### reStructuredText

- Good, because it has mature tooling around Python documentation.
- Bad, because GitHub renders only a subset and editor support is uneven.
- Bad, because directive syntax adds friction for short documents.

### External wiki

- Good, because edits do not require a pull request.
- Bad, because the documentation can drift from the code that exists at any given commit.
- Bad, because reviews of documentation changes are decoupled from the code review that should accompany them.

## More Information

The "semantic line breaks" convention is described at <https://sembr.org/>.
This decision applies to all documentation in the repository, including the ADRs themselves, future design documents, and any READMEs.
ADR-0002 builds on this decision by selecting MADR 3.0 as the structure for individual decision records, and ADR-0003 builds on both by selecting KEP-shaped documents for system-level design.
