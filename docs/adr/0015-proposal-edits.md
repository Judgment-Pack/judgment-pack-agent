# ADR-0015: Proposal edits

Status: accepted, 2026-10-21. Issue agent#110, milestone v0.8.0. The maintainer's
accepted decision builds on [ADR-0011](0011-the-agents-policy-table.md), the
reader and search, and [ADR-0009](0009-skills.md)'s placement of loaded blocks.

## Context

A model currently sends a complete proposal through `propose_pack`. Revising one
passage requires sending the whole document again. A draft needs exact, bounded
edits without acquiring a host file, a tool effect or an approval.

## Decision

The package holds one draft per document of the pack, the pack as the set. Each
is one JSON text, addressed as `draft:<document>` by `search` and `read_window`.
The host may supply its last accepted documents through `context.acceptedDocuments`
(`Context.AcceptedDocuments` in Go). Otherwise the first `propose_pack` opens the
draft. A run with neither a supplied draft nor `propose_pack` proposes nothing,
as before. Fenced proposals retain their existing fallback path.

Four reserved tools change the package draft only:

| Tool | Arguments | Result |
| --- | --- | --- |
| `propose_edit` | `document`, `old_text`, `new_text` | Replace the unique anchor. |
| `propose_delete` | `document`, `old_text` | Delete the unique anchor. |
| `propose_insert` | `document`, `after_anchor`, `text` | Insert immediately after the unique anchor. |
| `undo_edit` | `document` | Undo that document's last remaining edit once. |

Anchors are nonempty literal text and must occur exactly once, including overlapping
occurrences. Absence or ambiguity is refused with the occurrence count. Insertion
has no line-number form. The reported anchor line is one-based. UTF-8 bytes of the
new text are reported; deletion and undo add no supplied text and report zero.

Every candidate, including undo, is checked as JSON and sent through the existing
gated Runtime `validate` call. Only a `valid` result replaces the draft. A malformed,
invalid, unavailable or failed check leaves the prior text and undo stack unchanged
and emits `edit_refused {type, document, reason}` plus a guardrail with reason
`edit refused`. For a missing, non-string or empty identifier, `document` is
omitted and `reason` names the invalid document identifier. Nonempty identifiers
are truncated to 512 Unicode characters. A successful edit emits `edit_applied {type, document, line, bytes,
valid}`. Calls are serialized so concurrent edits cannot validate one revision and
replace another. A child and the critic never edit, including unoffered calls.

The policy table adds `budget.editsPerTurn` (24), `budget.editBytes` (8,000 UTF-8
bytes of new text), and `budget.proposalBytes` (65,536 UTF-8 bytes, the existing
pack bound). Successful edits and undos count across resume. The pack's draft
texts together obey the proposal bound. The budget read alias carries these
members for v0.8.0.

The proposal at the end is the whole draft with `edits[]` provenance: the operation,
document, anchor line, bytes and validation result. The person accepts a whole
document. Interrupted and waiting turns deliver the complete set, each with its
edit provenance. The critic reviews the whole proposal; edits are provenance only.
No edit changes a verdict, a tool effect, an approval or a host file.

The draft rides after history, with loaded schemas and skills in the execution
state. System instructions and tool declarations remain byte-identical across
edits. On the provider-managed agent wire this state rides with the run's prompt;
subsequent tools read the current package draft directly.

`checkpoint.draft` carries `{documents: [{id, digest, edits}]}`. Exact text,
unknowns and the undo stack live in the checkpoint's retained context, indexed by
draft id, text digest and edit count. Resume checks the digest and restores that
revision and stack. The host persists the checkpoint; the package never opens a
file. The original accepted document remains host-owned.

## Certification

Scenario 15 runs on every TypeScript engine and both Go adapters: search, read,
edit and validation; the whole edited proposal and its provenance; an ambiguous
anchor refused with its count; a schema-breaking edit leaving the draft unchanged;
undo; resume continuing the draft; child and critic refusals; and a byte-identical
stable prefix. Handwritten vectors cover each decided behavior and the contract
members. Five mutants must be caught: uniqueness removed, validation skipped,
edit list dropped, draft placed in system text, and undo applied twice. The Go
twins run on a disposable copy.

## Consequences

The package can revise a proposal without granting persistence or narrowing the
person's review to a patch. Validation costs a Runtime call per edit; retained
revisions and undo history consume the existing context and checkpoint budgets.
The Desk's diff beside the whole document and naming of its accepted origin are
follow-up work. Critic rounds and severity classes remain outside this decision.

Both certifications execute the shared call vectors in `contract/vectors/edits/`.
Go hosts register `OfferedEditTools(session)` through the existing offer mechanism;
the gate admits only names in the phase offer and has no edit exception.
