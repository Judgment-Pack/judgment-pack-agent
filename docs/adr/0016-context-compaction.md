# ADR-0016: Context compaction

Status: accepted, 2026-10-21. Issue agent#99, milestone v0.8.0. The maintainer's
accepted decision builds on [ADR-0005](0005-the-prompt-is-the-adapters.md), the
turn index, and [ADR-0011](0011-the-agents-policy-table.md).

## Context

The harness already replaces completed exchanges with a deterministic, bounded
index when a model request reaches its input allowance. That index preserves
tool-result handles, but long conversations also benefit from a short prose
account without allowing model-authored history to become instructions.

## Decision

The policy table gains `compaction: index | summary`, defaulting to `index`.
Index mode is byte-identical to the existing turn index. In summary mode, turns
older than `budget.recentMessages` are sent to the connection selected by
`models.compactor`, by default the author's connection at `low` reasoning. The
answer is limited to `budget.summaryBytes` (4,000 UTF-8 bytes).

The compactor receives only conversational prose and the digests of the turns it
replaces. Tool calls and results are not summarised; their retained ledger paths
remain in the deterministic index. The package places the prose, digests and
index together in a data block under the run's random marker, headed as historical
summary data and never placed in system instructions. Consequently summary text
such as “ignore the data rule” grants no tool or gate authority.

Each replacement emits `compacted {type, turns, bytes, digest, mode}` for the
person-facing marker. The host retains the full turns. `checkpoint.compaction`
carries the selected mode and each generated summary with its replaced-turn
digests, so resume reuses the stored bytes and makes no second compactor request.
A failed or empty compactor response emits a guardrail whose reason is
`compaction refused`, records the failure detail, falls back to the index and
continues the run.

Compaction changes only history after the stable prompt prefix. It does not move
the prefix, change a gate decision, or give the model a compaction tool. Deep
Agents' own summarisation remains disabled as required by
[ADR-0003](0003-the-deep-agents-adapter.md); all engines use this shared boundary.

## Certification

Scenario 16 runs on every TypeScript engine and both Go adapters. It compares
index mode to the earlier bytes, checks a framed bounded summary, its turn
digests, unchanged prefix and marker event, forces a failed compactor and index
fallback, resumes to identical request bytes, and puts “ignore the data rule” in
the summary without changing what the gate admits. Handwritten vectors cover an
index compaction, a summary, a refusal and a checkpoint. Four mutants must be
caught: unframed summary, missing digests, removed fallback and moved prefix. The
Go twins run on disposable copies.

## Consequences

Summary mode adds one bounded model request when compaction first occurs and may
lose prose detail, while the retained turns and index keep exact material
available by reference. Index remains the default and has no new model cost.
The Desk's visual treatment of the `compacted` marker is follow-up work.

## Amendment: compactor reasoning discovery (2026-10-23)

The compactor's table `low` setting follows ADR-0011's table-default rule. It remains
`low` without discovery or when advertised, resolves to the discovered default when
`low` is not advertised, and is omitted when discovery advertises no default. The
compactor request carries the resulting value and provenance through checkpoint resume;
an explicit run or project compactor value remains subject to capability refusal.
