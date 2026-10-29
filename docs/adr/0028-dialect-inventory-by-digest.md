# ADR-0028: Dialect authoring carries the original inventory by digest

Status: accepted for v0.12.0. Issue #243. Amends the dialect authoring contract of #171
(`docs/design/dialect-authoring.md`), which no earlier ADR recorded; this ADR is the
first decision record for dialect authoring and records only this change.

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
