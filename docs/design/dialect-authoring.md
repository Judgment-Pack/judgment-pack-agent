# Certified dialect authoring

Agent #171 supplies `dialectTools(ports)` in TypeScript and
`NewDialectTools(ports)` in Go's context package. Install a factory once per chat
scope and pass its returned HostTools to the normal contextual model or native
adapter. A newly constructed/restored factory requires another read.

`read_dialect` is first-party `read/read`, effect `read`, with read delegation.
`propose_dialect` is first-party `propose/propose`, effect `proposal`, without
delegation. Existing contextual admission, phase policy, authorization, tool
invocation, writer accounting and tool call/result events apply. Go's final
contextual declarations use the normal enrollment acknowledgment. Outside
ToolSource proposals retain their existing rule: their callbacks never execute.
No name-based source exception or additional execution authority is introduced.
Plan admits the read only; critic, reformat and delegated children cannot propose.
A host offering these tools still controls whether the model may propose at all.

The read accepts opaque `id` and declared `packKey`. The host supplies the
resource revision, exact pack JSON, optional exact existing dialect JSON, current
conversions, complete fact/evidence inventory metadata, columns and bounded
samples. The host revision must cover the retained records, column metadata and
original declarations; preview must read that exact retained revision. The package computes SHA-256 over each original byte string; no
parse/stringify step defines identity. An absent dialect has an explicit null
digest. Sample `valueJson` remains exact privately; a displayed prefix carries
`truncated`. A prefix is not complete JSON or a typed answer. Absence, null,
false, zero, empty strings, integer precision and decimal spellings remain
distinct. `researchIndex` carries the complete original revision inventory by digest
as a bounded top index, `{digest,revisions:[{revision,fields,digest,bytes}]}`; each
revision's field index and the declarations themselves are read through the read port in
pages (see "Paging original declarations" below). A null index or null fields means
unavailable, while no revisions means known empty. Exact fieldJson bytes retain all
eight historical types (text, integer, decimal, boolean, text-list, range, string,
number), opaque original members and numeric spellings. Unknown types stay inspectable
and become per-record preview issues. Column `research` remains optional legacy editor
metadata; it never supplies or fabricates the complete inventory. Present-day requested
types or one arbitrary record cannot supply original declarations. A host revision
must cover every retained original revision and the actual preview must use it.

A proposal supplies the read identity plus complete replacement fact and evidence
bindings and a summary. Every fact names an existing column, a unique current
pack target and an explicit host conversion: text, decimal, number, boolean with
distinct exact spellings, or research-value. Evidence binds explicit availability
columns to declared requirements. Overlapping targets and invented columns are
refused. Unbound pack inputs become explicit `unmapped`/`unmappedEvidence` review
items. Unchanged bindings must be repeated.

New candidates use integer version 2 and converted-copy fact leaves with source
`/retained` and `conversion:{contractVersion:"1",column,kind,...}`. Evidence leaves
use `availability:{contractVersion:"1",column}`. Boolean spellings are in the
checked document. The `conversions` sidecar is redundant editor metadata derived
from those same bindings, not independent executable policy. The canonical default
envelope includes `provider:""`, `facts:null`, `evidence:null` and no empty sources.
Existing dialect bytes remain unchanged; the factory publishes no named artifact.

A trusted host may provide `retainedRead:{documentJson,occurrence,framePointer}`.
Occurrence is exactly `{kind:"case"}` or `{kind:"source",name}`; a source must
already have a unique copy read. Only the selected case arrays or source read.copy
arrays are replaced. Source order, profile, profileDigest, arguments, parameters,
unwrap and every unselected declaration remain in the checked identity. The exact
original template text is also retained in the candidate/checkpoints. The package
constructs no frame, acquisition grant, receipt, binding, source profile or review
approval. Runtime validates the actual resulting full document, including global
unique target ownership. Unsupported templates fail instead of falling back to case.
The host must select the occurrence that owns its requested targets; retained
unselected targets that conflict with a replacement or unmapped declaration are refused
by Runtime. This is bounded selected-occurrence authoring, not a general multi-document editor.

Default case policy remains inspectable for imported, generated, mixed and zero-row
collections, matching Desk's proposal behavior. It is a declaration to review and
preview, not an assertion that generated data was acquired as case input. A generated
preparation must use its actual generated source/read occurrence under the existing
acquisition, binding, source order, unwrap, receipt and review rules. A future Desk
adapter must provide that trusted binding; this package does not implement the cutover.
Imported or metadata research-value, missing declarations or undeclared research
fields are deterministic issues for actual preview rows, not blanket authoring
refusals. Planned columns with zero rows establish neither type nor readiness.

The trusted host must explicitly opt into `DIALECT_RETAINED_COLUMN_DRAFT` in
TypeScript or `DialectRetainedColumnDraft()` in Go through ports.conversionContract.
These helper names remain for API compatibility and select the accepted companion at
draft maturity, not general Core support. Model arguments cannot select this contract.
The exact published Spec distribution is `0.3.0-rc.2`, signed source commit
`bcac15b7537ce21879201b5e5dfe795496225121`, schema
`0cb49f238c78391cb163a65474cc9af962df2bb1024a55c5310c2b784b2f0557` and corpus
`17b16ff7a9b74fe4cc81eb6f6129a6aabdd9834621d9e756aa2d4c1a3f245863`,
contract `retained-column/1`, profile `base`, role `document-validation`.
The companion source is `spec/retained-column.md`; RFC0029 remains its informative
design record. The verified Spec RC2 archives are published. This package's selected
identity does not establish a released Runtime or Runner implementation, final Core
conformance, preparation verification, acquisition authenticity or execution authority.
Stable batch, independent review, exact dependency and release gates remain required.

The Runtime port calls only get_dialect_contract, dialect_validate and dialect_check,
with the explicit draftContract and draftProfile selectors. These selector names remain
for transport compatibility with the opt-in draft-maturity role. Discovery and
both checked reports must independently match the package contract/spec identity,
draft maturity, the exact companion source URL, document-validation role, profile and limits. Discovery
must carry the matching conversionContracts entry and validate/check operations plus
declared-inventory-coverage. An old published alpha, wrong/missing feature, malformed
report, changed identity or unsupported byte ceiling refuses final success while
preserving the editable draft. Package document/pack admission remains 1 MiB and
honors lower discovered limits. No ordinary transport/parser bound is enlarged.

Validate must return document-valid in the selected experimental document scope. The
`experimental-retained-column-base-*` validationScope strings remain compatible;
they do not override the exact published Spec identity or its draft maturity.
Coverage must bind the same canonical document/digest and exact raw pack SHA-256;
host inventory metadata must match its entries. Only complete or review-required
coverage can proceed. Encoding is never validation; document/coverage success checks
no conversion values, source truth, acquisition binding, execution or preparation.

The package retains a bounded editable draft before Runtime discovery. A second
checkpoint retains the same initial candidate and actual validate/check transport
JSON after a check returns, including refused results, before conversion preview
and currentness. A transport exception or cancellation before a returned result
leaves the pending draft. Malformed or oversized returned evidence produces a
bounded authored `report-unavailable` failure with no clipped report or completeness
claim. Each result is at most4MiB, at most2results are retained, the initial
candidate is at most4MiB, and the complete serialized checkpoint is at most8MiB.
If its envelope cannot fit, the original candidate and authored failure remain.
The result list is diagnostic data; its presence does not assert semantic validity.

`retainDraft(checkpoint,signal)` is required in both port interfaces. Checkpoints
have version 2 (version 1 before #243), a32hex random attempt ID, fingerprint, candidate/checked stage,
content digest, nullable previous receipt, immutable initial candidate, and optional
check evidence. New candidates additionally carry conversionContract,
researchIndex and retainedRead, and their checkpoints have version 2. Version-1
checkpoints written by v0.11.0 carry inline researchDeclarations instead and still read; the
schema refuses a version-2 checkpoint without researchIndex or with researchDeclarations,
and a version-1 checkpoint with researchIndex. Their schema additions are optional for old
version-1 checkpoint inspection; the schema preserves the exact historical private-Draft tuple as an alternative whole
identity for inspection. Mixed old/new identity members are refused. Existing checkpoint
bytes, digests, reports and receipts are never rewritten or rebound. Historical inspection
does not authorize the old tuple for a new proposal or grant execution authority. A newly
restored factory still requires the current trusted opt-in, a fresh read and fresh checks.
Acknowledgments echo exact fingerprint, stage, digest and prior
receipt with `status: draft` and a bounded receipt. Checked must reference the
acknowledged candidate receipt. A final review proposal separately carries
`draft: {attemptId,fingerprint,candidateReceipt,checkedReceipt,checkedDigest}`;
its acknowledgment must echo this exact lineage. Initial document spelling never
changes behind an acknowledged receipt, even when Runtime supplies different
canonical final bytes. The final candidate fingerprint remains the canonical
mapping/conversion/sample identity, separate from the attempt fingerprint.

Only accepted validation and coverage, deterministic conversion preview, and an
exact revision/document/pack reread allow final `retain`. Its receipt truthfully
means the proposal completed this pipeline for human review. The preview receives
a detached canonical candidate with the original inventory index and exact bounded
sample text; a host preview reads the declarations themselves through the paged reader; its response echoes the canonical fingerprint and retained record count,
with bounded issue summaries. It does not acquire data or infer values from prose.
Port calls receive ten-second cancellation deadlines checked at await boundaries;
Go ports must honor their context. No rollback or best-effort cancellation write
is added. Failed preview/currentness leaves the checked draft inspectable/editable
and never produces final success.

Each known-completed explicit proposal creates a fresh attempt and review artifact,
including identical proposals, as Desk does. At most32attempts are admitted per
factory, each with2draft writes and1final write. A candidate-keyed busy/unknown
latch is checked before a new nonce or any further Runtime/preview/final effects.
Thrown, canceled, missing or mismatched write acknowledgment means unknown outcome
and prevents identical reentry from retrying; no nonce bypass exists. This memory
is local to the factory. Hosts own durable restart uncertainty and reconciliation;
creating a new factory does not prove an uncertain write did not occur.

Drafts explicitly support inspection, human review and editing. They grant no save,
publish, approval or execution authority. Host review must freshly validate edited
mapping and preview, check saved revision and current pack digest, then enforce
its own compare-and-swap and approval. A change after the final tool read remains
possible; neither a draft nor final proposal promises that a future save is current.

Read-only Desk commit `605630a535a25292a40926ba72e269de03b951e7` supplies
digest-bound reference bodies in `contract/testdata/dialect-authoring/desk-reference.json`.
Focused tests execute its actual propose body with package `checkArtifact`, and
actual preview/save bodies, with explicit bounded host I/O fixtures. Successful,
failed check, transport, preview/currentness and explicit repeat traces preserve
its inspect/review/edit affordance. Existing review lets a person edit a failed
draft; the failed report is a warning, while fresh validation/preview, pack digest
and revision CAS independently gate Save. A future Desk adapter can upsert one
existing update-dialect card for candidate/checked/final lineage without duplicate
cards or marking it applied. Fixtures do not mount React, execute a browser, or
wire that adapter; actual Desk cutover remains separately owned and unverified.

Published Runtime v0.39.0-alpha.1 does not establish this selected RC2 capability.
The factory does not implement the newer general quality-target conversions/reconciliation,
typed results, profile/binding/preparation verification or arbitrary acquisition execution.
Spec companion acceptance and publication are established for the selected identity;
actual released Runtime and Runner support, producer/history admission, complete
preparation/offline/export evidence, independent review and release gates remain separate.
Final Core milestone 2 and adopter-feedback disposition remain open.

Handwritten shared vectors cover both language factories, exact bytes, explicit
conversions and refusals. Actual API/native adapter certification uses fixture
read/preview/retention ports and an empty decision Runtime offer, since no
Runtime decision tool is needed. It checks contextual phase refusal and the
allowlisted semantic calls. Separate bounded probes exercise the actual local
Runtime implementation on the exact supplied mapping and pack. A fixture report
is never represented as upstream semantic validation or Desk acceptance. Full
integrated checks, exact-head CI, independent review and release review remain
batch gates.

## Paging original declarations (#243, #252)

The complete original inventory no longer travels inline. It travels as a two-level index
by digest, and nothing in it is dropped, clipped or reordered.

**The top index.** A host's read returns `researchIndex`, the top index. It lists every
original revision in order as `{revision,fields,digest,bytes}`:

- `fields` is the revision's field count;
- `digest` and `bytes` are the SHA-256 (`sha256:` and lowercase hex) and canonical byte
  length of that revision's field index;
- all three are null when the revision's declarations are unavailable.

The top index's own `digest` is the SHA-256 of the canonical JSON of `revisions`.

**Revision field indexes.** A revision field index is `{revision,fields:[{name,digest}]}`:
the ordered field names, each with the SHA-256 of the exact `fieldJson` bytes.

**Host helpers.** In TypeScript, `dialectResearchIndex(revisions)` computes the top index from
a complete inventory, and `dialectFieldIndex(revision)` computes one revision's field index.
The Go helpers are `DialectResearchIndexOf(revisions)` and `DialectFieldIndexOf(revision)`.
A TypeScript snapshot that still carries inline `researchDeclarations` is refused with that
reason; Go's snapshot no longer has the member.

The read port serves the rest in pages.

**Revision field index pages.** In TypeScript the member is `readFieldIndex(request,
signal)`; in Go it is `ReadFieldIndex(ctx, request)`. A request names:

- the read's `id`, `packKey` and bound collection `revision`;
- the `inventoryDigest`;
- `entry`, the top entry's position.

The page echoes all five and carries `fieldIndex`. The factory reads a field index only for
a revision with at least one field. An empty revision's field index, `{"fields":[],
"revision":…}`, is checked against its entry without a read.

**Declaration pages.** In TypeScript the member is `readDeclarations(request, signal)`; in Go
it is `ReadDeclarations(ctx, request)`. A request names:

- the read's `id`, `packKey` and bound collection `revision`;
- the `inventoryDigest`;
- a `cursor`, a position in the flattened order: revisions in order, then their fields in
  the order their field indexes list them, skipping unavailable revisions;
- a `limit` of at most 1,000 declarations.

The page echoes all five binding members and returns `declarations`, each
`{revision,name,fieldJson}`, where `revision` is the original revision.

**Host page rules.** A host returns at least one declaration and at most `limit`. It keeps
each page's canonical JSON within 1 MiB, and may return fewer than `limit` to stay within the
byte bound.

**Port calls.** A declaration page reads only the field indexes of the revisions it spans, so
no step holds more than one page of index and declarations. A revision that spans two pages
has its field index read twice.

The factory checks every page before using it, and refuses, as tool input failures:

- a top index whose digest differs from its entries, a malformed top index, or one over its
  bound;
- a top entry whose field index is over the page bound;
- a field index page bound to another collection revision, pack, id, inventory digest or
  entry, or over 1 MiB of canonical JSON;
- a field index whose canonical length or SHA-256 differs from its top entry's;
- a field index that names another revision, holds another field count, or has a malformed
  field;
- a declaration page bound to another collection revision, pack, id, inventory digest or
  cursor;
- an empty declaration page while declarations remain, or a declaration out of index order
  (a missing declaration);
- a declaration whose SHA-256 differs from its indexed digest;
- a declaration page over 1 MiB of canonical JSON or over its `limit`;
- a declaration that is not the named field's original declaration.

The factory also refuses a top index that names a field without the field index member, or
a declaration without the declaration page member.

**The model surface.** `read_dialect` pages the inventory once and shows the model a summary,
`researchInventory`: the digest, the declaration count, and per revision its field count or
null. With `cursor` it returns one checked page and `nextCursor`, which is null at the end.
A cursor read requires a prior read of the same identity and inventory, and refuses once
either has changed. `propose_dialect` pages and checks the complete inventory again before
its first draft.

**Binding.** The candidate, both draft checkpoints, the preview and the final proposal carry
the top index, so their fingerprints and digests bind the inventory digest. After preview,
the currentness read must still show the candidate's inventory digest; otherwise the
proposal is refused before `retain`.

**Host consumers.** Consumers that need declarations, such as a preview implementation or
review, read them with one of:

- `readDialectDeclarations({readFieldIndex,readDeclarations}, {id,packKey,revision}, index,
  signal)` in TypeScript;
- `ReadDialectDeclarations(ctx, readFieldIndex, readDeclarations, id, packKey, revision,
  index)` in Go.

Both apply the same checks and return the complete inventory.

**Bounds** (ADR-0028 carries the arithmetic):

- The top index is at most 2 MiB of canonical JSON (`indexBytes`, `DialectIndexBytes`).
  - Every top index the shape admits fits. That is 1,000 revisions with 160-byte labels,
    each escaped sixfold, which take at most 1,089,098 bytes.
  - A Desk collection holds at most 1,000 records, so at most 1,000 original revisions. Its
    top index takes at most 284,098 bytes.
  - The issue's 450 revisions of 30 fields take 77,390 bytes. Their one-level index took
    2,180,240.
- A revision field index and its page are at most 1 MiB (`pageBytes`, `DialectPageBytes`).
  - A Desk recipe (30 fields, 64-byte names) takes at most 4,956 bytes.
  - 1,000 fields with 160-byte names that need no escaping take 255,186.
- Declaration pages are at most 1,000 declarations (`pageDeclarations`,
  `DialectPageDeclarations`) and 1 MiB. Canonical escaping at most doubles a declaration, so
  a page always holds 31 of the largest.
- The snapshot, candidate (4 MiB), draft (8 MiB), result (2 MiB) and proposal bounds keep
  their values. Their inventory share fell from up to 2 MiB to the top index.

ADR-0028 records the decision and its #252 amendment.
