# ADR-0018: Records

Status: accepted, 2026-10-21. Issue agent#101, milestone v0.8.0. The maintainer's
accepted decision builds on [ADR-0011](0011-the-agents-policy-table.md) and
[ADR-0017](0017-the-output-boundary.md).

## Context

Checkpoints, run-report provenance and critiques crossed the package boundary without
a decided lifetime, a common identity or an export shape. A host could persist them,
but the package did not say when they expired or bind their contents to a digest.

## Decision

The host declares `records: {retainDays, redactOnStore, exportFormat}` in each policy
layer. Defaults are 90 days, `true` and `json`. Every new checkpoint, provenance row
and critique carries `schema`, `version`, `digest` and `retainUntil`. The digest is
`sha256:` plus SHA-256 of canonical JSON for the complete record with `digest` omitted.
Readers continue to admit old records without the four members for the v0.8.0
transition; every record this package writes carries them.

When `redactOnStore` is true, the records writer scans only model-derived members
before calculating the complete record's digest. One list in each language owns the
scanned paths: critique text, plan steps and explanations, notice/pause
reasons, operation and approval arguments and undeclared names, proposed memory text,
compaction summary text, retained draft proposal text and unknowns, and retained
assistant answers and tool-call arguments.
Declared tool names use the same run offer as delivery, including host, harness and
server tools. Tool-result payloads, host input, metadata, identifiers, digests,
counts, timestamps and the guard's own labels are exempt. A large exempt payload is
stored whole without spending the guard's scan budget. Each selected member has
its own work budget; if one exceeds it, nothing from that record is written. A
schema-valid guardrail with tool `records`, reason `output refused` and detail
`Output refused: work-bound.` reports the refusal, and ordinary execution continues.
A required approval checkpoint still has to succeed before an approved action runs. A match therefore becomes the same labelled hole the outbound
boundary writes, and the digest binds the redacted value. No unredacted copy is
returned as the written record. `redactOnStore: false` changes only this storage pass;
the ordinary output boundary still applies.
The guard call site marks storage passes so their per-pattern redactions enter the closed
`records.redactions` figures on each record, while unmarked calls count delivered output
under ADR-0017 without mixing the two channels.

`retainUntil` is calculated when the record is first written. A resumed checkpoint
keeps its original lifetime rather than extending it. A resume at or after that instant
is refused before a request or tool dispatch with the guardrail reason `records
expired`. The host deletes expired records; the package neither schedules nor performs
deletion.

`export.json` is a closed manifest. It lists each host-supplied record's schema,
version, digest, lifetime and canonical byte count, plus the records policy, package
version, export time and the manifest's own digest. The JSON export is that manifest
beside exact copies of the records the host handed the package. The package has no
store and cannot discover another record. A completed export reports
`export_written {type, records, bytes, digest}`; its digest binds the complete canonical
export. An export attestation is outside this ADR; a host may sign the export as it
signs a release.

Repository pull-request review records are also outside this ADR. They belong to the
repository process, not to a run's records lifecycle.

## Contract

- `policy.json` adds the closed `records` section and its three defaults.
- `checkpoint.json`, `provenance.json` and `run-event.json#/$defs/critique` name the
  four record members and closed `records.redactions` figures. `provenance.json` defines
  the digest, lifetime and record-figures shapes.
- closed `export.json` defines the manifest and its record entries.
- `run-event.json` adds `export_written` and guardrail reason `records expired`.
- handwritten vectors in `contract/vectors/records/` carry all three record kinds,
  the manifest and the expired-resume refusal.

## Certification

Scenario 18 runs on every TypeScript engine and both Go adapters. It checks the four
members on every written record, a labelled hole and separate per-pattern figures when
storage redaction matches, preservation of declared names and large exempt payloads,
atomic refusal of an oversized model-derived member with a schema-valid event, exact
manifest-to-record digests, and refusal of an expired checkpoint. Five mutants must be
caught: lifetime omitted, storage redaction skipped, a manifest digest changed, an
expired resume admitted, and the record-marker channel ignored. Their Go twins run
separately on a disposable copy.

## Consequences

The host remains the only store, exporter requester and deleter. A record is portable
and independently checkable, while an old checkpoint remains readable during the
transition. Retention is an enforcement boundary on resume, not permission to keep a
record: a host may delete earlier. Export signatures and repository review records
remain future or host-owned work.

The store-side guard exempts only exact labels emitted by that guard during the run; label-shaped model text is scanned and counted like other text, and compaction summaries are guarded before projection and hashing so stored and resumed bytes agree.

**2026-11-09 (#343).** A durable host or runtime call's result is no longer stored whole: the writer
puts it in the form this storage pass leaves of it before it is stored, records that form and answers
the call with it, so the record and what the model is given agree. Those redactions are not counted in
`records.redactions`, which still counts only what the pass itself changes when a record is written,
and no `output_guarded` event is made of them. A guard refusal still ends the call as an unknown outcome.

**2026-11-10 (#352).** In a durable run, reads that have no dispatch row get the same form. A Runtime
read through `callTool` is answered in the form the run's writer records a call's result in, and kept in
the chat and cached in that form. An entry in the chat's read cache may have been kept by a run that records
nothing, or restored from a stored chat. A durable run is served such an entry only where this pass leaves
it unchanged. If the pass would change it, the entry is not served from the cache: the read runs again,
as on a cache miss, and its recorded form replaces the entry in the cache. If the guard refuses it, the
call fails with that refusal, and nothing is served or run. Checking at read time, not at write time, also
covers entries kept before this change. A chat entry a durable run reads by id (`read_window`,
`read_context`, a search of one item, an image), and one its restored checkpoint referenced, joins the
run's context in the form this pass leaves of it: the run reads that form, its checkpoint carries it,
and the chat keeps its own entry. If the guard refuses the entry, that refusal is the read's error, and
the entry is not made the run's. A run that records nothing keeps and serves reads as the Runtime gave
them, as before. A read a durable run kept is in the recorded form, though, so a later run on the same
chat that records nothing is served that form from the cache.
`handoff.answer` is now a scanned member, as `handoff.continuation` is. A resumed run checks only that an
answer is present and gives the parent the continuation, so nothing is delivered from the answer itself.
A durable row's failure (`durable.dispatches.*.failure`) is stored with the form this pass leaves of its
`detail` alone, cut to the thousand UTF-16 code units a detail may hold without splitting a surrogate
pair; its other members are the catalogue's and stay as they are. Where a host or Runtime call's failure
reaches a model, the call fails with that same form. In Go the call's error is wrapped in
`failure.RecordedError`, which `failure.Normalize` reads before anything it wraps. In TypeScript the
`AIFailureError` the failure was read from is given the form in place, with its message, so a check of
the error's identity still holds. A model's own failure is only stored in that form. If the guard
refuses the detail, the call ends as `StorageOutcomeUnknown` and nothing of the failure is stored.
