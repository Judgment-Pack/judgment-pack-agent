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

When `redactOnStore` is true, the complete record passes the output guard before its
digest is calculated. A match therefore becomes the same labelled hole the outbound
boundary writes, and the digest binds the redacted value. No unredacted copy is
returned as the written record. `redactOnStore: false` changes only this storage pass;
the ordinary output boundary still applies.

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
  four record members. `provenance.json` defines the digest and lifetime shapes.
- closed `export.json` defines the manifest and its record entries.
- `run-event.json` adds `export_written` and guardrail reason `records expired`.
- handwritten vectors in `contract/vectors/records/` carry all three record kinds,
  the manifest and the expired-resume refusal.

## Certification

Scenario 18 runs on every TypeScript engine and both Go adapters. It checks the four
members on every written record, a labelled hole when storage redaction matches, exact
manifest-to-record digests, and refusal of an expired checkpoint. Four mutants must be
caught: lifetime omitted, storage redaction skipped, a manifest digest changed, and an
expired resume admitted. Their Go twins run separately on a disposable copy.

## Consequences

The host remains the only store, exporter requester and deleter. A record is portable
and independently checkable, while an old checkpoint remains readable during the
transition. Retention is an enforcement boundary on resume, not permission to keep a
record: a host may delete earlier. Export signatures and repository review records
remain future or host-owned work.
