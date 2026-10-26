# Run report

Use Agent's version 1 run report when presenting recorded terminal execution facts. Its contract is [run-report.json](../../contract/schemas/run-report.json); nested members reuse the checkpoint and provenance schemas.

Desk consumers [#620](https://github.com/Judgment-Pack/judgment-pack-desk/issues/620), [#548](https://github.com/Judgment-Pack/judgment-pack-desk/issues/548), [#565](https://github.com/Judgment-Pack/judgment-pack-desk/issues/565), [#575](https://github.com/Judgment-Pack/judgment-pack-desk/issues/575) and [#599](https://github.com/Judgment-Pack/judgment-pack-desk/issues/599) can share this projection instead of maintaining separate prompt, source, skill, critic or accounting summaries.

## APIs

TypeScript, from `@judgment-pack/harness/runReport`:

```ts
const report = projectRunReport(checkpoint)
const text = canonicalRunReport(report)
const retained = readRunReport(text) // unknown JSON value, JSON text, or UTF-8 Uint8Array
// Optional corroboration of observations already fetched by the host:
const corroborated = buildRunReport(checkpoint, head, pages)
```

Go, from `go/context` (package `runcontext`):

```go
report, err := runcontext.ProjectRunReport(checkpoint) // contract.Checkpoint -> *Value
text, err := runcontext.CanonicalRunReport(report)
corroborated, err := runcontext.BuildRunReport(checkpoint, head, pages)
retained, err := runcontext.ParseRunReport(bytes) // untrusted UTF-8 JSON
detached, err := runcontext.ReadRunReport(value) // existing *Value
```

Check each Go error before using its result. These TypeScript APIs are also exported from `@judgment-pack/harness/contract`. Canonical serializers sort object member names and preserve every array's recorded order. They return the same UTF-8 text in both languages. Serialization does **not** validate unknown retained input; use the strict reader first.

The strict reader validates report version 1 directly against the frozen v0.10.0 report field graph and its nested semantics. It accepts complete/stopped reports from checkpoint versions 1 and 2, preserves every optional member and array position, and returns detached data. It neither constructs a checkpoint nor grants execution/resume authority. Historical absence stays absent, explicit empty arrays/ledgers remain present, and zero remains zero. The frozen graph is included in the npm package; callers do not need to locate missing external schema references.

Unknown/missing/invalid members, unsupported versions, duplicate JSON names, malformed UTF-8, unpaired Unicode surrogates and nonfinite/unsafe or fractional numeric values are refused with `RunReportInvalid` and fixed text: `RunReportInvalid: the retained run report is invalid or unsupported.` All report v1 numeric domains are integers. Byte readers check decimal numeric spellings before conversion, including fractional spellings that would otherwise round to a safe integer. Already-rounded JavaScript numbers or Go `Value` numbers cannot reveal the caller's earlier spelling; use the byte/text reader at the retained-input boundary. Accepted canonical report text round-trips exactly; noncanonical whitespace, object key order and exactly representable integral decimal/exponent spellings are normalized by the serializer.

JSON object input must contain only enumerable own data properties on plain objects and dense arrays. Accessors, functions, custom prototypes, hidden/symbol members, sparse arrays and cycles are refused without invoking getters or `toJSON`. JavaScript proxies may execute their own reflection traps; this API does not isolate caller code. UTF-8 byte views are decoded as bytes, without consulting custom serialization.

Encoded input and canonical report output are each limited to 4 MiB. Parsing/copying stops beyond depth 64 or 4,194,304 JSON values and uses `RunReportOverflow`; no partial report is returned. Every JSON value requires at least one byte, so the node bound admits every report within the byte ceiling. Supported Plan semantics forbid a pending candidate from carrying another pending candidate; the deepest supported frozen report shape is 13 levels. These bounds therefore preserve supported reports, including large child ledgers. Reads are pure and emit no execution events.

Report v1's supported member graph is frozen at source `e00d2b11d11af4dabb0a4de6c1a5e7322bd8527e` in `contract/records/run-report-v1.json`. `scripts/run-report-shapes.mjs` copies/checks runtime mirrors and is included by the existing record-shapes check. Future report members require a new report version and a deliberate migration; expanding a checkpoint schema cannot widen this retained reader.

## Recorded members

| Members | Meaning |
| --- | --- |
| `version`, `checkpointVersion` | Report format 1 and original checkpoint format 1 or 2. |
| `id`, `phase`, `status` | Execution identity, phase and terminal status (`complete` or `stopped`). |
| `unit`, `used`, `limit`, `requestCap`, `grants` | Recorded request allowance facts; optional fixed cap remains optional. |
| `reason`, `explanation` | Recorded terminal reason and plan explanation. |
| `prompts` | Whole prompt digest rows in dispatch order. |
| `sources`, `sourceIdentities` | Whole source figures, including reported skill counts/identities, and source identity state. |
| `skills` | Whole loaded-skill rows in load order. |
| `critic` | Whole recorded decision, reason, tier and trigger where present. |
| `delegation`, `childUsage` | Recorded child, batch and parent figures plus separate child usage ledgers. |
| `usage`, `failover`, `completions` | Recorded usage rows, connection history and provider-neutral completion records. |
| `plan`, `planState` | Whole task steps and revision/provenance/evidence/departure/decision state from the checkpoint. |

Absence means no value was recorded. An empty array, empty ledger, empty reason or explicit zero remains distinguishable from absence. Counts and timings are never invented or recomputed. In particular, provider token counts do not establish monetary cost or subscription quota consumption.

## Supplied pages and limits

The supplied-page builder accepts actual RunHead and RunEventPage values. Fetch pages from the beginning in the same host-bound root resource, following each returned cursor. It validates envelope structure, checkpoint semantics, page framing, distinct opaque cursors, final canonical root checkpoint/head association and the final cursor. It never orders cursors by text and does no I/O.

Pages do not contain predecessor proof. The builder cannot independently prove that initial/interior events were not omitted or reordered, or authenticate the host. Child events need not appear in bounded retained child histories. The returned report contains only terminal checkpoint facts; supplied observations add no report authority or inferred totals. The checkpoint-only projector has no journal guarantee. Neither API exposes raw journal, request or context payloads.

Canonical report bytes and supplied event bytes each have a 4 MiB limit; the builder accepts at most 400 events and the store's existing 100 events per page. Overflow is refused, never silently truncated. TypeScript throws and Go returns `RunReportInputInvalid`, `RunReportPrefixInvalid` or `RunReportOverflow`, with fixed bounded error messages. These pure readers emit no events and change no execution/store state.

See [the design](../design/run-report.md) for the complete source/exclusion inventory and evidence limitations.

## Current history feedback reports

The read-only history feedback projection uses report2 when actual stored feedback is present, retaining the real source `checkpointVersion` (1, 2 or 3). Existing checkpoint1/2 projections without feedback retain their old report bytes. Report1 and its frozen reader graph remain unchanged.

A report2 labelled checkpoint1/2 cannot contain checkpoint3-only step `dependsOn`, `budget`, `delegation` or `evidence.child`, or Plan-state `taskSchedule`/`taskEnvelope`. This includes steps retained in revision diffs and pending candidates and the candidate's Plan state. Whole schedule state, including pending child associations and acknowledged child-result labels, is checkpoint3-only. Historical step IDs/origins, operation/artifact evidence and pending Plan drift stay permitted. The schema and both language readers enforce these source-version restrictions for native and derived presentation reads.

Usage totals and route facts likewise require a real checkpoint3 source that contains those facts; history projections do not fill absent facts with defaults. Imported or redacted original-feedback digest labels describe internal edit-chain consistency. They establish neither original execution/provider authenticity nor resume/admission authority.

Report2 optionally presents actual enabled repeat tracking as `repeatTools`. Its execution rows contain only `execution`, `remindAt`, `stopAt`, `count`, `reminded`, `reminderDelivered` and `stopped`; `policy` contains only the actual resolved `repeatTools.mode`, `repeatTools.remindAt` and `repeatTools.stopAt` members with their stored provenance. The source must be checkpoint3 with enabled policy and matching thresholds/outcomes. Dispatch observations, key digests, raw arguments/results and unrelated policy members are excluded. Legacy/off absence stays absent; readers never create defaults or acknowledge execution from these summaries. Report1 remains frozen.
