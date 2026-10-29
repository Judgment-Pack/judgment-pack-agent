# ADR-0028: Dialect authoring carries the original inventory by digest

Status: accepted for v0.12.0. Issue #243. Amends the dialect authoring contract of #171
(`docs/design/dialect-authoring.md`), which no earlier ADR recorded; this ADR is the
first decision record for dialect authoring and records only this change. Amended for
#252 (below): the index has two levels, and its top level is bounded for every accepted
collection.

## Context

Dialect authoring requires the complete original declaration inventory: every retained
original revision with every ordered field and its exact declaration. v0.11.0 carried
that inventory inline in the read snapshot and in every candidate, checkpoint and
proposal, all bounded at 4 MiB. A reachable, accepted Desk collection of 150 retained
revisions with 30 text fields and 1,000-byte descriptions has a 4,947,643-byte inventory,
so the factory refused it at the snapshot bound before any authoring began.

## Decision

The inventory travels as a bounded index by digest, and declarations are read on demand
through the read port in pages.

- The index lists, per original revision in order, each field's name and the SHA-256 of
  its exact declaration bytes in the repository's `sha256:<hex>` form, plus a digest over
  the canonical JSON of all revisions. Nothing is dropped, sampled or reordered; an
  unavailable revision keeps null fields.
- The snapshot, candidate, both draft checkpoints, the preview candidate and the final
  proposal carry the index instead of the declarations. Their fingerprints and digests
  therefore bind the inventory digest; the currentness read after preview must show the
  candidate's inventory digest, so a candidate built over one inventory cannot be retained
  against another.
- Declarations enter only through the host's read port: its paged member answers a
  request bound to the collection revision and inventory digest, from a cursor in index
  order, with at most 1,000 declarations and 1 MiB of canonical JSON. Every declaration is
  checked against its indexed digest and order before use; a mismatch, a missing
  declaration, another revision or inventory, or an oversized page refuses in the
  factory's existing tool-input refusal style. No other access is added.
- The model-facing read shows an inventory summary and pages declarations with `cursor`
  and `nextCursor`; its schema and description say so.
- The index is bounded at 2 MiB of canonical JSON, half the unchanged 4 MiB snapshot
  bound: about 8,200 declarations at 160-byte names and about 16,700 at 30-byte names,
  each up to 16 KiB. Every existing bound keeps its value.

## Versioning

Draft checkpoints and review proposals written by this factory have version 2 and carry
`researchIndex`; they never carry `researchDeclarations`. Version-1 checkpoints written by
v0.11.0 keep their inline `researchDeclarations` and still read: the schema keeps their
shape and refuses a version-1 checkpoint with `researchIndex`, a version-2 checkpoint
without it, and a version-2 checkpoint with `researchDeclarations`. The Go and TypeScript
candidate types keep `researchDeclarations` as an optional member for reading version-1
records only. A TypeScript host snapshot that still carries inline `researchDeclarations`
is refused with that reason; Go's snapshot type no longer has the member.

## Rejected

Raising the byte bounds, narrowing the supported domain, clipping descriptions, dropping
old revisions, and substituting a current-only or sampled inventory each lose required
behaviour or provenance; none is adopted.

## Amendment: a two-level index bounded for every accepted collection (#252)

### Context

The decision above still holds: complete original provenance, by digest, read through the
read port. Its one-level index, however, was checked whole before any page was read. A
collection the Desk accepts (450 retained revisions, 30 text fields each, 64-byte names,
4,920,611 bytes, under the Desk's 8,388,608-byte limit) has a one-level index of
`98 + 4,842·N + Σdigits(1..N)` = 2,180,240 bytes at N = 450, over the 2,097,152-byte cap.
Shortening revision labels cannot fix it; the field entries alone exceed the cap.

### Decision

The index has two levels. Nothing is dropped, clipped or narrowed.

- The **top index** (`researchIndex`) is what the snapshot, candidate, both draft
  checkpoints, the preview candidate and the final proposal carry. Per original revision, in
  order, it holds `{revision, fields, digest, bytes}`. `fields` is the revision's field count,
  and `digest` and `bytes` are the SHA-256 and canonical byte length of that revision's
  **revision field index**. All three are null when the revision's declarations are
  unavailable. Its own `digest` is the SHA-256 of the canonical JSON of `revisions`, the
  whole-inventory digest that every fingerprint and currentness check already binds.
- A **revision field index** is `{revision, fields:[{name, digest}]}`: the ordered field names
  and the SHA-256 of each exact declaration, as the one-level index held them. It is read
  through the read port's new member, `readFieldIndex(request, signal)` in TypeScript or
  `ReadFieldIndex(ctx, request)` in Go. A request names the read's `id`, `packKey`, collection
  `revision`, `inventoryDigest` and `entry` (the top entry's position). The page echoes all
  five and carries `fieldIndex`. The factory refuses the page when:
  - it is over 1 MiB of canonical JSON, or bound to another revision, inventory or entry;
  - its field index's canonical length or SHA-256 differs from the top entry's `bytes` or
    `digest`;
  - it names another revision, or holds another field count;
  - it has a malformed field name or digest.
  An empty revision (`fields` 0) is not read; its field index `{"fields":[],"revision":…}` is
  still checked against its entry. A top entry whose field index is over the page bound is
  refused at the read.
- Declaration pages are unchanged. Their cursor counts the flattened order the field indexes
  give. A page reads only the field indexes of the revisions it spans, so no step holds more
  than one page of index and declarations. `readDialectDeclarations` / `ReadDialectDeclarations`
  take the field index reader beside the page reader.
- The model-facing summary is unchanged: `researchInventory` shows the digest, the
  declaration count and each revision's field count, now taken from the top index.

### Bound proof

The host domain is the Desk's accepted collection:

- **Revisions.** A collection holds at most 1,000 records, the Desk's per-collection record
  limit. Every original revision in the inventory is the original revision of at least one
  record, so N ≤ 1,000. This contract's existing structural limit is also 1,000 revisions.
  The 8,388,608-byte limit alone would not bound N tightly enough: a minimal revision costs
  a few hundred bytes of collection. The revision count therefore rests on the record limit.
- **Fields.** A recipe has 1 to 30 fields. Field names match `[a-z][a-zA-Z0-9_]{0,63}`, so
  they are at most 64 ASCII bytes and never need escaping. This contract allows 1,000 fields
  and 160-byte names.
- **Labels.** Revision labels are at most 160 bytes, this contract's limit.

Canonical costs, counted from the encoding:

- An available top entry costs 117 bytes plus the digits of `bytes` and `fields` plus its
  escaped label. An unavailable entry costs 56 bytes plus its label. The top index wrapper
  costs 99 bytes, and N entries add N − 1 commas.
- A revision field index costs 27 bytes plus its escaped label, plus 94 bytes and the escaped
  name per field, plus F − 1 commas.
- Canonical escaping costs at most six bytes per byte (`\u0001`).

| index | arithmetic | bytes | cap |
| --- | --- | --- | --- |
| largest top index the shape admits | 99 + 1,000 × (117 + 7 + 4 + 960) + 999 | 1,089,098 | 2,097,152 |
| largest Desk top index | 99 + 1,000 × (117 + 4 + 2 + 160) + 999 | 284,098 | 2,097,152 |
| the issue's family, top index | measured | 77,390 | 2,097,152 |
| the issue's family, one-level (#250) | 98 + 4,842 × 450 + 1,242 | 2,180,240 | refused |
| largest Desk revision field index | 27 + 160 + 30 × (94 + 64) + 29 | 4,956 | 1,048,576 |
| largest field index with unescaped names | 27 + 160 + 1,000 × (94 + 160) + 999 | 255,186 | 1,048,576 |

- **Top index.** Every top index the shape admits fits under the unchanged 2 MiB cap, even
  with every label escaped sixfold. The cap never refuses an admitted index; it only bounds
  what a host may send.
- **Field index pages.** A field index page adds its binding members: at most 7,066 bytes,
  with `id` and `packKey` of 512 characters and the 128-byte revision, all escaped sixfold.
  So a Desk page is at most 12,022 bytes, and a page of unescaped names at most 262,252
  bytes. Only a field index of names that need escaping, near 1,000 fields of 160 bytes, could
  exceed the page bound; such an index is refused by name. Desk names never need escaping.
- **Envelopes.** The snapshot, candidate, drafts and proposal carry the top index where #250
  carried an index of up to 2,097,152 bytes. Their inventory share is now at most 1,089,098
  bytes (284,098 for a Desk collection). With the same other content, every envelope is
  smaller than #250 allowed, and no cap changes. Declaration pages keep their bounds.

The Python contract test checks this arithmetic. Go and TypeScript measure the same sizes
from the `bounds` rows of `paging.json` and drive these cases:

- the issue's family (N = 450);
- the largest accepted Desk collection (1,000 revisions × 30 fields, 160-byte labels, 64-byte
  names);
- the largest unescaped field index;
- the largest admitted top index;
- a top index over its cap;
- a field index over the page bound;
- 1,001 revisions inside the byte cap, refused by the 1,000-revision limit the proof rests on;
- revision labels made only of U+0085 or only of U+FEFF, refused as blank in both languages.

The first three are read, proposed and retained with every page and envelope measured. A
label with U+0085 and U+FEFF beside a letter is accepted.

A field index whose canonical length differs from its entry's `bytes`, with the right digest,
is refused (a paging case). Blank text is one explicit code point set in both languages: the
union of `String.prototype.trim` and Go's `strings.TrimSpace`, so U+0085 and U+FEFF are blank
in both.

### Versioning

The version-2 shape of #243 is in no release, so it changes in place. `researchIndex` holds
the top index. The revision field index travels only through the read port, so
`dialect-draft.json` drops `indexField`. `dialect-authoring.json` gains `indexField`,
`fieldIndex`, `fieldIndexRequest` and `fieldIndexPage`. There is no version 3. Version-1
records written by v0.11.0 still read as before.

### Rejected

- **Raising the index cap.** Any fixed one-level cap is exceeded by the field count times the
  revision count. The two-level index is bounded by the revision count alone.
- **A third level, or paging the top index.** The proof shows the top index fits for every
  index the shape admits, so another level adds reads without covering anything more.
- **Narrowing labels or names, dropping old revisions, or a current-only inventory.** Each
  loses required provenance.
