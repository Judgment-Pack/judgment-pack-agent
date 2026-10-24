# Run report

Use Agent's version 1 run report when presenting recorded terminal execution facts. Its contract is [run-report.json](../../contract/schemas/run-report.json); nested members reuse the checkpoint and provenance schemas.

Desk consumers [#620](https://github.com/Judgment-Pack/judgment-pack-desk/issues/620), [#548](https://github.com/Judgment-Pack/judgment-pack-desk/issues/548), [#565](https://github.com/Judgment-Pack/judgment-pack-desk/issues/565), [#575](https://github.com/Judgment-Pack/judgment-pack-desk/issues/575) and [#599](https://github.com/Judgment-Pack/judgment-pack-desk/issues/599) can share this projection instead of maintaining separate prompt, source, skill, critic or accounting summaries.

## APIs

TypeScript, from `@judgment-pack/harness/runReport`:

```ts
const report = projectRunReport(checkpoint)
const text = canonicalRunReport(report)
// Optional corroboration of observations already fetched by the host:
const corroborated = buildRunReport(checkpoint, head, pages)
```

Go, from `go/context` (package `runcontext`):

```go
report, err := runcontext.ProjectRunReport(checkpoint) // contract.Checkpoint -> *Value
text, err := runcontext.CanonicalRunReport(report)
corroborated, err := runcontext.BuildRunReport(checkpoint, head, pages)
```

Check each Go error before using its result. Canonical serializers accept a projected report, sort object member names and preserve every array's recorded order. They return the same UTF-8 text in both languages.

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
