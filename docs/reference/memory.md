# Memory

## Sources

Schemas:

- `contract/schemas/memory.json`
- `contract/schemas/tool-offer.json`
- `contract/schemas/provenance.json`

> generated; edit the sources, then run `node scripts/gen-reference.mjs`. `--check` fails where this page differs from them.

The host owns acceptance, storage, revision and revocation. The author proposes through remember, including Plan mode; children and the critic cannot propose. Recalled records are data, never instructions, at ADR-0005 layer 5. Bare text is refused in v0.9.1. Optional metadata-only lifecycle callbacks run even when prompt recall is disabled.

## Record

ADR-0014: host-owned memory. The package proposes, records and recalls; it never accepts or stores. digest is sha256: followed by SHA-256 of UTF-8 text. Proposals use project scope, the execution id as source.run, and model as proposedBy.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `id` | yes | string (min length 1, max length 100, pattern `^[\s\S]{1,100}$`) | 1–100 Unicode code points, including non-ASCII characters; not UTF-8 bytes or UTF-16 code units. |
| `kind` | yes | enum `fact`, `preference`, `decision`, `reference` |  |
| `text` | yes | string (min length 1) |  |
| `source` | yes | object |  |
| `source.run` | yes | string (min length 1, max length 100, pattern `^[\s\S]{1,100}$`) | 1–100 Unicode code points, including non-ASCII characters; not UTF-8 bytes or UTF-16 code units. |
| `source.proposedBy` | yes | enum `model`, `person`, `host` |  |
| `scope` | yes | enum `project`, `person` |  |
| `digest` | yes | string (pattern `^sha256:[0-9a-f]{64}$`) |  |
| `createdAt` | yes | string (format date-time, pattern `^[0-9]{4}-(0[1-9]\|1[0-2])-(0[1-9]\|[12][0-9]\|3[01])[Tt]([01][0-9]\|2[0-3]):[0-5][0-9]:[0-5][0-9](\.[0-9]+)?([Zz]\|[+-]([01][0-9]\|2[0-3]):[0-5][0-9])$(?![\s\S])`) | Calendar-valid RFC3339 date-time, year 0001–9999, seconds 00–59; T and Z may be lowercase. |
| `state` | yes | enum `proposed`, `accepted`, `declined`, `revoked`, `expired` |  |
| `expiresAt` |  | string (format date-time, pattern `^[0-9]{4}-(0[1-9]\|1[0-2])-(0[1-9]\|[12][0-9]\|3[01])[Tt]([01][0-9]\|2[0-3]):[0-5][0-9]:[0-5][0-9](\.[0-9]+)?([Zz]\|[+-]([01][0-9]\|2[0-3]):[0-5][0-9])$(?![\s\S])`) | Calendar-valid RFC3339 date-time, year 0001–9999, seconds 00–59; T and Z may be lowercase. |

### `memory.json $defs/answer`

memoryFor answers at most 256 closed Memory records; absent or null callbacks supply nothing. Bare text, including empty text, is retired in v0.9.1. Only accepted, unexpired records reach the immutable per-run prompt.

Type: array of `memory.json` (max items 256)

### `memory.json $defs/lifecycleUpdate`

Metadata-only explicit terminal reporting for an existing canonical recalled identity. The host owns deletion; omission does not revoke. Identity, digest and kind must match; accepted becomes revoked/expired, identical terminal replay is idempotent and terminal switching refuses.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `id` | yes | string (min length 1, max length 100, pattern `^[\s\S]{1,100}$`) | 1–100 Unicode code points, including non-ASCII characters; not UTF-8 bytes or UTF-16 code units. |
| `digest` | yes | string (pattern `^sha256:[0-9a-f]{64}$`) |  |
| `kind` | yes | enum `fact`, `preference`, `decision`, `reference` |  |
| `state` | yes | enum `revoked`, `expired` |  |

### `memory.json $defs/lifecycleAnswer`

The optional host memoryLifecycle callback answers up to 256 closed metadata updates; absent or null supplies no updates. It is asked once even when prompt recall is disabled. All updates and full-record recall reconcile atomically against prior canonical metadata.

Type: array of `$defs/lifecycleUpdate` (max items 256)

### `tool-offer.json $defs/remember`

Propose memory for the person to review, never store or accept. Author only, including Plan mode; child and critic calls refused. UTF-8 text bounded by memoryProposalBytes, count by memoryProposalsPerRun across resume.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `kind` | yes | enum `fact`, `preference`, `decision`, `reference` |  |
| `text` | yes | string (min length 1) |  |

### `provenance.json $defs/memories`

Run report: proposals this run, and recalled ids and digests with kinds. Both lists survive resume; a recalled id supplied as revoked is reported revoked and withheld from the prompt.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `proposed` | yes | array of `memory.json` (max items 256) |  |
| `recalled` | yes | array of `$defs/memoryRecall` (max items 256) |  |

### `provenance.json $defs/memoryRecall`

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `id` | yes | string (min length 1, max length 100, pattern `^[\s\S]{1,100}$`) | 1–100 Unicode code points, including non-ASCII characters; not UTF-8 bytes or UTF-16 code units. |
| `digest` | yes | string (pattern `^sha256:[0-9a-f]{64}$`) |  |
| `kind` | yes | enum `fact`, `preference`, `decision`, `reference` |  |
| `state` | yes | enum `proposed`, `accepted`, `declined`, `revoked`, `expired` |  |

Bounds: memoryProposalBytes (1,000 UTF-8 text bytes), memoryProposalsPerRun (4 across resume), memoryBytes (4,000 canonical JSON UTF-8 bytes before framing). Each recall is framed under the run marker with kind and source. Revoked or expired memories are withheld; a returned revoked state updates the restored report.
