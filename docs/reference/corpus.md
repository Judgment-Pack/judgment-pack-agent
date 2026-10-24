# The evaluation corpus

## Sources

Schemas:

- `contract/schemas/corpus-entry.json`
- `contract/schemas/run-event.json`

> generated; edit the sources, then run `node scripts/gen-reference.mjs`. `--check` fails where this page differs from them.

ADR-0022 records a run only after the output guard redacts the complete entry. Replay answers from the recording and compares decisions and outcomes, never model prose or a live provider.

## Entry manifest

ADR-0022. The closed manifest for one redacted recorded run. The named files are the complete portable entry; replay compares expected decisions and outcomes, never model prose.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `id` | yes | string (pattern `^[a-z0-9][a-z0-9-]{0,63}$`) |  |
| `packageVersion` | yes | string (min length 1, max length 64) |  |
| `policy` | yes | object | The resolved policy under which the recording ran. |
| `files` | yes | object |  |
| `files.requests` | yes | const `requests.json` |  |
| `files.toolResults` | yes | const `tool-results.json` |  |
| `files.events` | yes | const `events.json` |  |
| `files.checkpoint` | yes | const `checkpoint.json` |  |
| `expected` | yes | object |  |
| `expected.file` | yes | const `expected.json` |  |
| `tolerance` | yes | array of string (max length 256, pattern `^/(timings\|drawnMarkers)(/.*)?$`) (max items 64) | JSON pointers whose values may differ on replay. Only timing values and randomly drawn markers may be named. |

### `run-event.json $defs/recorded`

ADR-0022: a redacted evaluation corpus entry was written. id names its corpus directory and bytes is the sum of the UTF-8 bytes in its six JSON files.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `recorded` |  |
| `id` | yes | string (pattern `^[a-z0-9][a-z0-9-]{0,63}$`) |  |
| `bytes` | yes | integer (min 1) |  |
| `version` |  | const `1` | Event wire format version. Absent only on historical records; current writers emit 1. |

### `run-event.json $defs/guardrail`

The host saying what it did. rewrote and refused are the tool gate's, emitted by the host as the call's frame leaves the page, so they arrive between that call's tool_call and tool_result. narrowed is the adapter's notice, before the model is asked anything, that a tool's served schema lost a keyword on this family's wire. Also refused, with tool proposal, where the host withholds a proposal (Plan mode, or a conversation without artifact proposals). reason (ADR-0008), on a refusal of an act call of a tool with a source: approval refused, the host's approval policy answered refuse; approval pending, the call came while another act call of the run waits for the person's decision (at most one approval is pending); approval stale, the gate's own refusal of a call whose decision no longer holds for it (its arguments not those its approval was decided for, a call that reaches its run without an allow, or, on the continued run, its tool no longer offered as the person decided). None of these calls is run. skill withheld (ADR-0009), on a skill the package did not catalogue or restore, tool its name (or, for the catalogue's bounds, its source's id): its tools[] names a tool the host did not declare, its bytes pass skillBytes, its bundle does not hash to the digest the person approved, the catalogue's bounds left it out, or a skill the checkpoint had loaded is no longer declared as it was; skill budget, on a load_skill call refused past skillsPerRun, between that call and its failed result. Nothing of a withheld skill reaches the model. review refused (ADR-0010), on a request_review call refused: a second ask of the run, an ask under off or on, or one outside the author's phase; nothing is recorded and no review is asked.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `guardrail` |  |
| `tool` | yes | string |  |
| `action` | yes | enum `rewrote`, `refused`, `narrowed` |  |
| `detail` | yes | string |  |
| `reason` |  | enum `approval refused`, `approval pending`, `approval stale`, `skill withheld`, `skill budget`, `review refused`, `policy refused`, `pace warning`, `subagent withheld`, `subagent refused`, `memory refused`, `edit refused`, `compaction refused`, `output refused`, `records expired`, `identity changed`, `approval downgraded`, `steer refused`, `failover refused`, `record refused`, `record version unsupported`, `record member unsupported`, `plan update refused` |  |
| `version` |  | const `1` | Event wire format version. Absent only on historical records; current writers emit 1. |
