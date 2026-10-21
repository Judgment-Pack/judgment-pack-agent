# Records

## Sources

Schemas:

- `contract/schemas/export.json`
- `contract/schemas/provenance.json`
- `contract/schemas/run-event.json`

> generated; edit the sources, then run `node scripts/gen-reference.mjs`. `--check` fails where this page differs from them.

See also: [policy.md](policy.md), [checkpoint.md](checkpoint.md), [events.md](events.md), [reasons.md](reasons.md).

The host owns storage and deletion. The package writes self-describing records, refuses expired checkpoints and exports only records the host supplies; it has no store.

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
| `version` | yes | const `1` |  |
| `digest` | yes | `provenance.json $defs/recordDigest` |  |
| `retainUntil` | yes | `provenance.json $defs/retainUntil` |  |
| `bytes` | yes | integer (min 1) |  |

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
