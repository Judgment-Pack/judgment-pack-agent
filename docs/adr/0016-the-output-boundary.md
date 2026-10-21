# ADR-0016: The output boundary

Status: accepted, 2026-10-21. Issue agent#100, milestone v0.8.0. The maintainer's
accepted decision builds on [ADR-0011](0011-the-agents-policy-table.md),
[ADR-0014](0014-memory-the-write-side.md) and [ADR-0015](0015-proposal-edits.md).

## Context

A tool result can contain a credential or a machine-local path. The data rule
holds what the model reads; it does not hold what the model sends to a person
or a later tool. The package needs one outbound guard in both languages.

## Decision

Every answer (`message`), proposal, tool argument, memory proposal and draft
edit passes the output guard before delivery or execution. It does not rewrite
model input, retained tool results, recalled memory or host-supplied documents.
The guard has no authority over admission, approvals or Runtime verdicts.

The policy table adds three host-owned members, resolved run > project > table:
`outputGuard` (`off`, `redact`, `refuse`, default `redact`), `outputPatterns`
(named `{name, pattern}` additions, default empty), and `outputAllow` (pattern
names disabled for the project, default empty). Patterns never enter the prompt.
Names are stable; additions cannot replace or duplicate a base name or the
reserved refusal names `work-bound`, `proposal-validation` and `invalid-output`. Malformed
patterns and names are refused during policy resolution, including under `off`.

`contract/patterns/output.json`, version 1, holds the base patterns: AWS access
key IDs, provider keys, Google API keys, GitHub tokens, private-key blocks,
bearer tokens, and machine-local home and temporary paths. Each has a positive
and a negative handwritten vector. These are shapes, not a credential verifier.

`redact` replaces a match with `[redacted: <pattern name>]`. `refuse` stops the
outbound step and emits a guardrail with reason `output refused` naming the
pattern. `output_guarded {type, where, pattern, action}` records what was held
without recording the match. `where` identifies one of the five outbound paths;
`action` is `redact` or `refuse`. Counts by pattern name live in the report's
`outputGuard` map and `checkpoint.outputGuard`, and carry through resume.

The guard reuses search's RE2-subset compiler and Pike-machine instructions.
It scans each string once per active pattern, sharing search's 4,000,000-step
work budget across the outbound value. Overlapping matches are held together;
the earliest match supplies the label, with base order then host declaration
order breaking ties. Replacement holes are never rescanned. Exceeding the work
bound refuses the entire value, with pattern `work-bound`, without releasing a
partially checked answer. Object string values and keys are covered recursively.

A proposal redaction is presented to Runtime validation as the hole. If the
redacted proposal or draft fails validation, it is refused and recorded under
`proposal-validation`; the prior draft stays intact. Validation does not confer
approval. Draft anchors are resolved against the original draft before the
candidate is guarded, so holding an anchor cannot select a different edit.

Incomplete answer and reasoning fragments are withheld. Complete reasoning is
guarded before its completed event is released. The complete answer
passes the guard once, before delivery. Tool-call display uses a guarded copy;
the independent dispatch guard holds the actual arguments before the host or
Runtime sees them. The display copy does not double-count a dispatched redaction.

## Certification

Scenario 17 runs on every TypeScript engine and both Go adapters. A tool result
carrying a key shape can still be read by the model, but its answer, proposal,
later tool arguments, memory proposal and draft edit contain labelled holes.
Refuse stops the outbound step, an allowed name passes, and report counts survive
resume. Both language readers execute the shared vectors byte-identically.

Four mutants and their Go twins must be caught: an outbound path skipped, a base
pattern removed, the allow list ignored, and a count not recorded. Schema member
and enum probes require every addition to be carried by a handwritten vector.

## Consequences

The host owns project persistence and the allow list; the package owns the
outbound boundary. The person can see where text was held and which stable name
to allow. Enabling the default guard delays fragment delivery until the complete
answer is available. Pattern matching has a deterministic work bound, and a
bound refusal can be resolved by shortening the output or simplifying host patterns.

## Not decided here

The guard does not filter what the model reads, accept memories or proposals,
write host files, change tool admission, or change a Runtime verdict. The host's
policy controls and display of guarded counts remain host work.
