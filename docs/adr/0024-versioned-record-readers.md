# ADR-0024: versioned record readers

Status: accepted for v0.10.0. Implements #165.

A browser and server can run different harness releases. A version decides whether either
reader understands a saved record before it interprets the body. Package release numbers do
not decide record compatibility.

The wire versions written by this release are checkpoint 2, run event 1 and policy layer 1.
Both languages also read checkpoint 1 and historical events and policy layers with no version.
Missing checkpoint versions and explicit zero, negative, fractional or nonnumeric versions are
invalid. These are the supported historical formats; this decision promises no other history.
A positive supported integer above the kind's current version is refused by the named
`UnsupportedRecordVersion` error before body interpretation. An unknown structural member is
refused by `UnknownRecordMember`. Their bounded guardrail reasons are respectively
`record version unsupported` and `record member unsupported`; a malformed input cannot supply
text for the refusal event. The unknown-member event detail is the fixed sentence
`Record contains an unsupported member.` The diagnostic error retains its member pointer
(`member` in TypeScript, `Member` in Go) and bounded diagnostic message for callers; neither
is copied into the public refusal event, including startup before an output guard exists.
No provider request or tool action follows refused admission.

Schema-owned integers use the shared finite IEEE-754 parsed-value domain: integral values
between -(2^53-1) and 2^53-1, with versions positive. Integral JSON spellings such as 2.0 and
2e0 are equivalent. Fractional, unsafe and nonfinite parsed values are invalid. This is not
lossless lexical validation: a near-integer token such as 1.00000000000000001 rounds to 1 in
both readers. Go normalizes only a detached decoding copy at schema-owned integer leaves;
opaque payload number tokens, supplied objects, lifecycle metadata and digest strings remain
unchanged. TypeScript's public APIs continue to accept parsed values.

Every structural object is read against the selected format's member inventory, including
nested completions, policy members and source records. Declared arbitrary values and maps keep
their contents: tool arguments, provider tool-result payloads, retained context values, proposal
documents and policy values. These are named payload positions, not permission to add structural
members. Policy-layer version metadata never participates in policy precedence or becomes a
resolved member. The enclosing checkpoint or policy_resolved event versions its resolved map.

The historical inventory in `contract/records/historical-shapes.json` is frozen. Its digest is
held by both reader tests. Current inventories are generated from the schemas by
`scripts/record-shapes.mjs` and packaged identically for TypeScript and Go. Unreleased checkpoint
2 additions cannot become known members of published checkpoint 1 by changing a current schema.
The structural walk follows references, selects event variants, checks member names, types,
required members and enums, and refuses work beyond its depth and visit bounds. Existing
execution and policy admission checks still hold their operational and value constraints.

A read does not upgrade the version or rewrite lifecycle metadata. In particular a historical
policy member with absent source remains absent in the returned record; runtime admission
continues to provide its layer semantics. Writers stamp the applicable current version before
computing the digest. A resumed checkpoint's next snapshot is version 2, with a newly computed
digest and the original retainUntil. The digest still covers canonical JSON of the complete
record with digest alone omitted. Provenance, critique and export-manifest versions remain 1.
An export retains the supplied record's version and canonical body; its manifest entry names
that actual version. Old readers cannot be made to emit this release's named refusal: upgrade
readers before handing them new records, and do not downgrade a host while retaining newer
records it cannot read.

The TypeScript public readRunEvent/readPolicyLayer APIs and the Go public record-root JSON
decoders share this contract. Final event writer seams stamp version 1, including startup
refusals, direct gate notices, usage/end events and corpus events. General generated contract
leaf types remain shape codecs; this decision does not turn every leaf into a full JSON Schema
validator. Shared positive and refusal vectors, both language readers, writer checks and
interrupted-record tests hold these claims.

Stable plan IDs, revisions, final report structure, evidence completion and accepted departures
remain separate decisions. They can now change a current wire format deliberately.
