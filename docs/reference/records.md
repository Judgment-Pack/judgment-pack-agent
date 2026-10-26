# Records

## Sources

Schemas:

- `contract/schemas/checkpoint.json`
- `contract/schemas/run-event.json`
- `contract/schemas/policy.json`
- `contract/schemas/run-report.json`
- `contract/schemas/run-report-v2.json`
- `contract/schemas/export.json`
- `contract/schemas/provenance.json`

Package files (`packages/harness`), with the symbols read:

- `recordReader.ts`: `RECORD_VERSIONS`

> generated; edit the sources, then run `node scripts/gen-reference.mjs`. `--check` fails where this page differs from them.

See also: [policy.md](policy.md), [checkpoint.md](checkpoint.md), [events.md](events.md), [reasons.md](reasons.md).

The host owns storage and deletion. The package writes self-describing records, refuses expired checkpoints and exports only records the host supplies; it has no store.

Feature-free writer defaults remain checkpoint 2 and event/policy 1. Feature-bearing records select checkpoint 3, event 2 and policy 2. Readers retain checkpoint 1/2 and historical unversioned event/policy layers under frozen member graphs. Newer versions and unknown structural members are refused by name. Historical reads and exports preserve supplied versions and lifecycle metadata; resume selects the applicable writer version without extending retention.

| Record | Feature-free writer default | Current schema version |
| --- | --- | --- |
| checkpoint | `2` | `3` |
| event | `1` | `2` |
| policy | `1` | `2` |

## Retained reports and history

projectRunReport / ProjectRunReport preserve report 1 for terminal checkpoint 1/2 and select report 2 for checkpoint 3. projectHistoryRunReport / ProjectHistoryRunReport also select report 2 when actual stored feedback is present, preserving the source checkpointVersion. Report 2 projects only recorded usage totals, route facts and repeated-tool outcomes; checkpoint 1/2 cannot acquire checkpoint 3 task or feature facts. Report 1 remains frozen. Neither report grants execution authority.

### Report 1

Version 1 canonical presentation of recorded terminal checkpoint facts. Arrays retain their recorded order; absent optional data stays absent. No inferred totals, timings, prices, or provider facts. A pure projector and a bounded validated-prefix builder produce the same report.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `version` | yes | const `1` |  |
| `checkpointVersion` | yes | `checkpoint.json properties/version` |  |
| `id` | yes | `checkpoint.json properties/id` |  |
| `phase` | yes | `checkpoint.json properties/phase` |  |
| `status` | yes | enum `complete`, `stopped` |  |
| `unit` | yes | `checkpoint.json properties/unit` |  |
| `used` | yes | `checkpoint.json properties/used` |  |
| `limit` | yes | `checkpoint.json properties/limit` |  |
| `requestCap` |  | `checkpoint.json properties/requestCap` |  |
| `grants` | yes | `checkpoint.json properties/grants` |  |
| `reason` |  | `checkpoint.json properties/reason` |  |
| `explanation` | yes | `checkpoint.json properties/explanation` |  |
| `prompts` |  | `checkpoint.json properties/prompts` |  |
| `sources` |  | `checkpoint.json properties/sources` |  |
| `sourceIdentities` |  | `checkpoint.json properties/sourceIdentities` |  |
| `skills` |  | `checkpoint.json properties/skills` |  |
| `critic` |  | `checkpoint.json properties/critic` |  |
| `delegation` |  | `checkpoint.json properties/delegation` |  |
| `usage` |  | `checkpoint.json properties/usage` |  |
| `childUsage` |  | `checkpoint.json properties/childUsage` |  |
| `failover` |  | `checkpoint.json properties/failover` |  |
| `completions` |  | `checkpoint.json properties/completions` |  |
| `plan` | yes | `checkpoint.json properties/plan` |  |
| `planState` |  | `checkpoint.json properties/planState` |  |

### Report 2

Version 2 closed presentation of current terminal checkpoint facts. Attributable acknowledged usage totals are present only when the source checkpoint contains an actual usage budget journal; completeness is explicit. Absence never invents zero usage.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `version` | yes | const `2` |  |
| `checkpointVersion` | yes | enum `1`, `2`, `3` |  |
| `id` | yes | `checkpoint.json properties/id` |  |
| `phase` | yes | `checkpoint.json properties/phase` |  |
| `status` | yes | enum `complete`, `stopped` |  |
| `unit` | yes | `checkpoint.json properties/unit` |  |
| `used` | yes | `checkpoint.json properties/used` |  |
| `limit` | yes | `checkpoint.json properties/limit` |  |
| `requestCap` |  | `checkpoint.json properties/requestCap` |  |
| `grants` | yes | `checkpoint.json properties/grants` |  |
| `reason` |  | `checkpoint.json properties/reason` |  |
| `explanation` | yes | `checkpoint.json properties/explanation` |  |
| `prompts` |  | `checkpoint.json properties/prompts` |  |
| `sources` |  | `checkpoint.json properties/sources` |  |
| `sourceIdentities` |  | `checkpoint.json properties/sourceIdentities` |  |
| `skills` |  | `checkpoint.json properties/skills` |  |
| `critic` |  | `checkpoint.json properties/critic` |  |
| `delegation` |  | `checkpoint.json properties/delegation` |  |
| `usage` |  | `checkpoint.json properties/usage` |  |
| `childUsage` |  | `checkpoint.json properties/childUsage` |  |
| `failover` |  | `checkpoint.json properties/failover` |  |
| `completions` |  | `checkpoint.json properties/completions` |  |
| `plan` | yes | `checkpoint.json properties/plan` |  |
| `planState` |  | `checkpoint.json properties/planState` |  |
| `usageTotals` |  | `$defs/totals` |  |
| `route` |  | `turn-route.json` |  |
| `feedback` |  | array of `feedback.json $defs/view` (min items 1, max items 20000) |  |
| `repeatTools` |  | object |  |
| `repeatTools.executions` | yes | array of object (min items 1, max items 33) |  |
| `repeatTools.executions[].execution` | yes | `checkpoint.json $defs/repeatExecution/properties/execution` |  |
| `repeatTools.executions[].remindAt` | yes | `checkpoint.json $defs/repeatExecution/properties/remindAt` |  |
| `repeatTools.executions[].stopAt` | yes | `checkpoint.json $defs/repeatExecution/properties/stopAt` |  |
| `repeatTools.executions[].count` | yes | `checkpoint.json $defs/repeatExecution/properties/count` |  |
| `repeatTools.executions[].reminded` | yes | `checkpoint.json $defs/repeatExecution/properties/reminded` |  |
| `repeatTools.executions[].reminderDelivered` | yes | `checkpoint.json $defs/repeatExecution/properties/reminderDelivered` |  |
| `repeatTools.executions[].stopped` | yes | `checkpoint.json $defs/repeatExecution/properties/stopped` |  |
| `repeatTools.policy` | yes | object |  |
| `repeatTools.policy.repeatTools.mode` | yes | `policy.json $defs/member` |  |
| `repeatTools.policy.repeatTools.remindAt` | yes | `policy.json $defs/member` |  |
| `repeatTools.policy.repeatTools.stopAt` | yes | `policy.json $defs/member` |  |

Conditions beyond the members (allOf) are in the schema file.

Retained history, archives, capture and feedback have separate typed contracts. The host supplies storage and immutable source material; imported or derived data does not establish original execution, provider authenticity or live capture authority. See [run-report.md](run-report.md), [history-archive.md](history-archive.md), [history-capture.md](history-capture.md) and [feedback.md](feedback.md).

UnknownRecordMember keeps its member pointer and bounded message on the diagnostic error. Public refusal events use fixed input-independent detail, including startup before an output guard exists.

## The export manifest

ADR records (#101). The closed export.json manifest beside the host-supplied records. The package has no record store.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `schema` | yes | const (see schema) |  |
| `version` | yes | const `1` |  |
| `digest` | yes | `provenance.json $defs/recordDigest` |  |
| `retainUntil` | yes | `provenance.json $defs/retainUntil` |  |
| `createdAt` | yes | string (max length 40, format date-time) |  |
| `packageVersion` | yes | string (min length 1, max length 64) |  |
| `policy` | yes | object |  |
| `policy.retainDays` | yes | integer (min 1, max 36500) |  |
| `policy.redactOnStore` | yes | boolean |  |
| `policy.exportFormat` | yes | const `json` |  |
| `records` | yes | array of `$defs/record` (max items 10000) |  |

### `export.json $defs/record`

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `schema` | yes | enum `https://github.com/Judgment-Pack/judgment-pack-agent/blob/main/contract/schemas/checkpoint.json`, `https://github.com/Judgment-Pack/judgment-pack-agent/blob/main/contract/schemas/provenance.json`, `https://github.com/Judgment-Pack/judgment-pack-agent/blob/main/contract/schemas/run-event.json#/$defs/critique` |  |
| `version` | yes | enum `1`, `2` |  |
| `digest` | yes | `provenance.json $defs/recordDigest` |  |
| `retainUntil` | yes | `provenance.json $defs/retainUntil` |  |
| `bytes` | yes | integer (min 1) |  |

Conditions beyond the members (allOf) are in the schema file.

### `provenance.json $defs/recordDigest`

sha256: and the lower-case SHA-256 of canonical JSON for the complete record with digest omitted.

Type: string (pattern `^sha256:[a-f0-9]{64}$`)

### `provenance.json $defs/retainUntil`

The instant after which the host deletes the record and the package refuses it for resume.

Type: string (max length 40, format date-time)

### `run-event.json $defs/export_written`

The host asked the package to produce a JSON export from records it supplied. No package store was read.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `export_written` |  |
| `records` | yes | integer (min 0) |  |
| `bytes` | yes | integer (min 1) |  |
| `digest` | yes | `provenance.json $defs/recordDigest` |  |
| `version` |  | const `1` | Event wire format version. Absent only on historical records; current writers emit 1. |
