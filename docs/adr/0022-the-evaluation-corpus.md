# ADR-0022: The evaluation corpus

Status: accepted, 2026-10-21. Issue agent#103, milestone v0.8.0. The maintainer's
accepted decision builds on the policy table ([ADR-0011](0011-the-agents-policy-table.md)),
the output boundary (ADR-0017), and self-describing records (ADR-0018).

## Context

Certification scenarios prove named behaviours, but a release also needs stable examples of whole
runs as policy, output protection and records evolve. Provider prose cannot be a stable oracle, and
a replay that reaches a provider would be neither deterministic nor safe for release review.

## Decision

A recorded run is one `corpus/<id>/` entry. Its closed manifest names `requests.json`,
`tool-results.json`, `events.json`, `checkpoint.json` and `expected.json`, records the package
version and resolved policy, and lists its tolerance. The tolerance may name only timings and
randomly drawn markers. The output guard redacts requests, tool results, events and checkpoint
before the package writes any file. If it cannot, the package writes nothing and emits a guardrail
whose reason is `record refused`.

A host opts in with `record: true`. A successful write emits `recorded {type, id, bytes}`. The host
hands the entry to the maintainer, who adds the reviewed entry here. An intentionally changed
expectation is updated in the behaviour-changing pull request with its reason, as a parity row is.

`expected.json` contains the gate's decisions, the proposal digest, the critic's verdict and
structured checks from the preceding `critique` event, refusals, and outcomes. Every proposal event
carries `digest`, `sha256:` followed by the SHA-256 of the proposal's canonical JSON with `digest`
omitted. Replay compares only those members, after removing allowed
tolerances. It never compares a model's prose. `harness-check corpus` and the TypeScript twin replay
every entry with requests and tool results answered by the recording. Neither replay path accepts a
live-provider capability.

Agent request response bodies carry ordered `steps`, each holding an `event` or `tool` callback with lower-case wire member names; replay awaits each callback through the adapter, while legacy events-only seed bodies remain readable. TypeScript records these through `record: true`; Go hosts use `adapter.RecordAgentRun` before passing the captured request and body to `WriteCorpusEntry`.

A recording keeps a steered run's `steered` events as host facts, and replay delivers each recorded
steer at the same request boundary so that the newly emitted stream is compared at that boundary.

## Certification

Scenario 22 runs on every TypeScript engine and both Go adapters. Both seed entries—one model
engine and one agent capability—replay byte-identically in compared members. A tampered expectation
fails with the differing member's JSON pointer, and a scripted run recorded with `record: true`
round-trips into replay. Three mutants are caught: comparison reading prose, ignored tolerance, and
recording that skips output redaction. The Go twins run on disposable copies under the clone.

## Consequences

The corpus is review material, not provider output blessed as truth. It catches release drift at
the decisions and outcomes the package owns while allowing model versions to phrase answers
differently. Entries can contain sensitive material only after the same output boundary used for
host-visible records has redacted it and a maintainer has reviewed the files.
