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
distinct. `researchDeclarations` carries the complete original revision inventory as
`{revision,fields:[{name,fieldJson}]}` entries. A null inventory or null fields means
unavailable, while an empty array means known empty. Exact fieldJson bytes retain all
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
Model arguments cannot select this contract. These centralized provisional pins select
only private Draft RFC0029 revision ade3914b0b81ef299e5b9a8d6147f37ff3f0976c,
schema 996cec17473987eebf1aa553536df0ce5a740971532567f411282ee70036879f and
corpus e2ac53f56cd5ab779e032d2847b49fa04438ffb9df57d456f75f2ba058dc12d2,
profile base. This exact reviewed development source was merged through Spec PR271
(squash 25b7156a1a27fd46d1b2493145a6e9fb5931a2f5); the selector retains the reviewed
head above. These pins match the experimental Runtime document role. This is Draft
integration only, with no normative adoption or product-release claim. Stable batch,
independent review, exact dependency and release gates remain required.

The Runtime port calls only get_dialect_contract, dialect_validate and dialect_check,
with the explicit private draftContract and draftProfile selectors. Discovery and
both checked reports must independently match the package contract/spec identity,
experimental-draft maturity, document-validation role, profile and limits. Discovery
must carry the matching conversionContracts entry and validate/check operations plus
declared-inventory-coverage. An old published alpha, wrong/missing feature, malformed
report, changed identity or unsupported byte ceiling refuses final success while
preserving the editable draft. Package document/pack admission remains 1 MiB and
honors lower discovered limits. No ordinary transport/parser bound is enlarged.

Validate must return document-valid in the selected experimental document scope.
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
have version1, a32hex random attempt ID, fingerprint, candidate/checked stage,
content digest, nullable previous receipt, immutable initial candidate, and optional
check evidence. New candidates additionally carry conversionContract,
researchDeclarations and retainedRead. Their schema additions are optional for old
version-1 checkpoint inspection; old checkpoints do not gain new contract support or
execution authority. A newly restored factory still requires a fresh read.
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
a detached canonical candidate, original declarations and exact bounded sample
text; its response echoes the canonical fingerprint and retained record count,
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

Published Runtime v0.39.0-alpha.1 and published Spec artifacts do not establish
this new private Draft capability. The factory does not implement the newer general
quality-target conversions/reconciliation, typed results, profile/binding/preparation
verification or arbitrary acquisition execution. Its explicit provisional document
role is separate from published support. Normative adoption, exact immutable upstream
selection, actual Runner preparation/offline/export evidence, independent review and
release gates remain required. No final Spec acceptance or unconditional final-Spec-tag
timing requirement is implied.

Handwritten shared vectors cover both language factories, exact bytes, explicit
conversions and refusals. Actual API/native adapter certification uses fixture
read/preview/retention ports and an empty decision Runtime offer, since no
Runtime decision tool is needed. It checks contextual phase refusal and the
allowlisted semantic calls. Separate bounded probes exercise the actual local
Runtime implementation on the exact supplied mapping and pack. A fixture report
is never represented as upstream semantic validation or Desk acceptance. Full
integrated checks, exact-head CI, independent review and release review remain
batch gates.
