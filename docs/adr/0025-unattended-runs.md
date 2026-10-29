# ADR-0025: Unattended runs

Status: accepted for v0.11.0. Issue #206, including its job, goal and question
additions. This is a design decision and implementation split; it does not claim
these facilities exist in v0.11.0. It builds on ADR-0008, ADR-0018, ADR-0019 and
ADR-0024.

## Boundary and compatibility

The package validates transitions and records what happened. The host owns durable
storage, scheduling, clocks, wake delivery, credential refresh, process management
and notifications. No scheduler, background polling model, credential store or
provider-account service is added to the package.

Current RunStore revision checks serialize storage writes. They do not stop two
workers from producing external effects before either commits. An old receipt is
not current authority. The new unattended capability therefore requires a store and
all relevant dispatch hosts that implement the contract below; an in-memory store
or single-writer callback cannot claim durable multi-process fencing. They may
exercise the same state machine within their declared lifetime. Unsupported hosts
refuse unattended admission rather than silently falling back to revision checks.

These facilities are explicit opt-ins. With none configured, existing requests,
checkpoint/event bytes, pauses and question behavior remain unchanged. Enabling a
facility requires the record versions that implement it. ADR-0024's published
inventories stay frozen; readers refuse unsupported formats before acting. New
writers, readers, schemas and shared vectors ship together in both languages.
Historical approval/budget pauses are not rewritten on read. An explicit migration
at a supported continuation boundary may create a new-format checkpoint while
retaining the original evidence and recording the transition.

## 1. One driver and fencing

A resource-scoped store atomically acquires a lease against the expected run
revision and records its owner, expiry and monotonically increasing epoch. Renewal
requires the current owner and epoch; an expired lease cannot be revived by a late
heartbeat. Acquisition after expiry creates a higher epoch. Lease time and expiry
are decided by the authoritative host/store clock, not a model argument or a
worker's assertion. Clock uncertainty or unavailable ownership evidence closes
new dispatch. Epochs must have an exact portable encoding with bounded size, never
rounded through a floating-point counter or reset by deleting local state.

Every checkpoint commit and physical dispatch carries the admitted run identity,
execution scope and epoch. The store and the receiving effect host atomically
check current ownership/expiry at their admission boundary. A client-side check
followed by an unguarded external action is insufficient. Where a resource cannot
enforce that boundary, the host must serialize through an enforcement service or
refuse the unattended capability for that resource. A lease grants neither tool
permission nor approval, and children do not acquire competing root ownership.

Expiry prevents new admission; it cannot undo a call already admitted. Loss of
ownership stops new model/tool dispatch and cancels owned work where supported.
An in-flight effect or unacknowledged result remains unknown until reconciled by
its authoritative host. Takeover never automatically replays it. Fencing is not a
claim of exactly-once effects across independent systems.

An operation's idempotency identity is separate from its lease epoch: it binds the
run, stable operation identity, target and request digest and survives takeover.
A new epoch cannot turn the same uncertain operation into a new action. Gateway
#316 may bind both identities, but must not use the fencing token alone as its
idempotency key. Storage commit identity also remains distinct.

### Amendment (#223): the lease contract as built

The names this decision left open are fixed here and in `contract/schemas/run-lease.json`
(version 1), and are used unchanged by the store kinds that implement them (desk #609) and the
readers of the same records (runner #147). The lease's members are **owner**, **expiry** (the
authoritative expiry: the store clock plus a requested duration of 1 ms to 24 hours, UTC with
exactly millisecond precision), **epoch** and `state` (`held` or `released`; expired is decided by
the store clock at each check and never stored, and equal time is expired), keyed by `resource`.
The **fence** is `{resource, owner, epoch}`: the evidence presented at every checkpoint commit and
received by the effect host at every admission. The epoch's exact bounded encoding is a canonical
base-10 string from `1` to `999999999999999999`; acquiring past it refuses `epoch-exhausted`. A
release keeps the epoch. The effect's idempotency identity is `{run, operation, source, tool,
requestDigest}`, keyed by the SHA-256 of its canonical JSON, with no epoch. Refusals are named:
`invalid`, `no-lease`, `held`, `revision-changed`, `stale-epoch`, `other-owner`, `expired`,
`released`, `epoch-exhausted`, `resource-mismatch` and `already-admitted`; capability refusals of
unattended admission are `store-unsupported`, `store-lifetime` and `effect-host-unfenced`. A store
declares `leaseLifetime` `durable` or `process`; an effect host declares `effectFencing` `enforced`
or `unsupported`. The fence adds no member to any checkpoint or event format. The host-facing
contract is in `docs/design/storage.md` (Run leases and fencing).

## 2. Durable waits and questions

A wait records a unique run-scoped identity, reason, exact expected event/subject
and/or wake time, optional timeout, continuation identity and pending decision.
An event-or-time wait uses the first valid atomic transition. The host delivers
bounded, authenticated wake evidence; a model-supplied event name, job identifier
or timestamp is not proof. Duplicate delivery returns the retained disposition;
a stale, conflicting or cross-run wake changes nothing. Unknown acknowledgement
is reconciled by reading, never by dispatching the continuation again.

Parking atomically commits the checkpoint, wait and attention event before it
releases the lease. No provider account or executable tool call is held open to
wait for a person. If shutdown/release evidence is uncertain, that uncertainty is
retained and the host cannot claim a clean resumable park. Notification delivery
uses the committed event identity for deduplication; failure to send a notification
does not roll back a committed park. Waking records a reason and obtains a new
lease before revalidating and dispatching. The model never polls for wake events.

Approval and budget pauses become wait reasons in the new opt-in format, while
preserving their existing distinct grants. A wake is not approval or allowance.
`ask_user` is a question wait: retain the question, options, recommendation and
original task. Record a real chosen option or free text with host-attested actor
and answer time. A timeout ends the question **unanswered**. It never chooses the
recommended option, manufactures consent or treats a request for help as an
answer. Question answers remain distinct from tool approval. Reports and exports
retain both the question and its resolution; retention/redaction remains explicit.

## 3. Deadlines and approval expiry

Run and wait deadlines are optional policy values, absent by default. The earliest
applicable run, wait or approval deadline wins; a new wait cannot extend a run
limit. The host schedules expiry and supplies authoritative time evidence. Before
new physical dispatch and on every continuation, the package validates that the
relevant deadline has not elapsed. Deadline expiry stops new work and records the
reason; cancellation requests do not prove an already-started effect was undone.

In unattended approval mode, the host must supply a bounded positive approval TTL;
absence refuses that mode rather than creating an indefinite approval. Store the
resulting expiry with the exact call, arguments, source/identity/descriptor pins,
plan purpose/revision and policy that the person reviewed. An expired or changed
approval cannot execute. Re-asking requires a newly retained request and a real new
decision; renewal of a lease does not renew approval. Equal-time expiry is expired.

## 4. Re-authorization

Only a trusted, invocation-bound host disposition can establish that a call was
never admitted because credentials expired or were revoked. That disposition may
park the run as `needs_reauthorization`, naming a bounded connection reference
and the unsent operation. Credentials, tokens and raw provider errors never enter
the wait, prompt, report or exported bundle.

If dispatch may have occurred, preserve its unknown outcome and reconcile it;
reauthorization is not retry permission. After an explicit person/host action,
resume rechecks the actual principal, approved source/descriptor pins, permissions
and pending operation. Refreshing credentials for the same admitted identity can
continue only after those checks. A changed identity retains ADR-0019's withholding
and new-run reapproval rule; it is not silently relabelled as a refresh. A narrower
permission set can still make the pending operation inadmissible.

## 5. Revalidation on every resume

Before any Runtime, model or host-tool dispatch, validate current ownership,
deadlines, pending decisions, authorized artifact revisions, source/identity/tool
pins and the exact execution/Plan lineage. Observation of current bytes is not
permission to adopt them. A changed reference parks for an explicit decision;
read failure or missing evidence never restores old authority by default.

Keep the pinned model and provider family. A retired model parks for attention;
there is no silent replacement. ADR-0020's existing same-family in-run failover
retains its own bounds. A person-approved model replacement is an explicit new
admission/continuation with recorded identity and affected request approvals
revalidated, never a mutation of historical request bytes.

Use pinned policy unless the trusted host supplies a newer layer. Resolve the full
layer order again, record old/new identities and differences, and revalidate all
pending operations. A new layer is not an implicit source/tool/budget grant or a
way to erase consumed usage. Incompatible changes require a fresh admitted run.
Pre-bootstrap decisions use the package-owned acknowledgement seam from #219;
metadata enrollment from #218 cannot stand in for that durable transition.

## 6. Attention and reporting

Each committed park emits one `needs_attention` event with its wait identity,
reason and the bounded action or evidence needed to proceed. Reasons include
approval, budget, question, reauthorization, retired model, changed reference and
deadline. Delivery may be repeated by the host; the recorded event is not duplicated.
No event itself sends mail or another notification. A deadline that terminates the
run records its terminal disposition rather than promising a resumable park.

The run report and #207 export list every park/wake, deadline, decision and answer
with their actual identities, actor/time provenance and unknown outcomes. They
must not reconstruct omitted history from the compacted prompt. Redacted or
expired content is explicitly unavailable. No report or feedback item authorizes
resume, and #208 feedback is never injected as model input.

## 7. Run-owned background jobs

A job is registered through a package-owned transition before the start operation
can be admitted. Its stable owner, operation identity, request digest and external
receipt/handle are retained across restart. An uncertain start stays uncertain;
registration alone does not prove an external job exists. Idempotent reconciliation
and fencing follow decision 1. Run-scoped read/list/wait/cancel ports verify ownership;
an identifier supplied by the model cannot access another run's jobs.

Completion arrives through an authenticated host event bound to the registered
job and generation, and can satisfy a wait. Replayed, foreign or stale completion
cannot wake the run. Cancellation records requested versus acknowledged outcome;
requesting it never fabricates completion. All effectful start/cancel operations
retain ordinary approval and dispatch boundaries. Workflow #137 remains deferred
until Runner's real records exist; this decision does not invent their payloads.

## 8. One durable goal

A run may have one objective with state `active`, `achieved`, `abandoned` or `blocked`.
The objective and its required Plan steps/evidence survive restart. Host commands
inspect, pause, resume or abandon through acknowledged CAS transitions without a
model turn. Paused is a scheduling disposition, not another completion state;
blocked records an unresolved condition, not success. Resuming does not grant
budget, tool authority or a new interpretation of a pending approval.

Achievement requires the named steps' evidence-linked completion under #181 and
current accepted Plan lineage, not the model saying it is done. An empty or
ambiguous evidence obligation cannot be vacuously marked achieved. Record every
command, its actor/time and outcome in the journal and report. Replacing a terminal
objective requires an explicit new objective identity/transition; history is never
overwritten. Abandoning requests cancellation of owned jobs and retains uncertain
results; it is not proof that external effects stopped.

## Implementation split and acceptance

The v0.11.0 deliverable is this reviewed decision and the following separately
tracked build items. Issues #223–#230 are assigned to **v0.12.0**; #206 closes
with the v0.11.0 release. This does not move any existing v0.11.0 implementation
issue. No implementation is claimed
from this ADR's acceptance.

| Build item | Depends on | Required evidence |
| --- | --- | --- |
| [Lease and fencing](https://github.com/Judgment-Pack/judgment-pack-agent/issues/223) | RunStore, #160, #217 | Competing workers/takeover; expired/stale commit and dispatch refusal at actual host admission; unknown in-flight result not replayed; disabled-byte parity. |
| [Durable wait and pause/question integration](https://github.com/Judgment-Pack/judgment-pack-agent/issues/224) | Lease/fencing, #219 | Atomic park/wake; duplicate and cross-run delivery refusal; real restart; no held provider; unanswered question never auto-selects; old pauses retained. |
| [Deadlines and approval expiry](https://github.com/Judgment-Pack/judgment-pack-agent/issues/225) | Durable waits | Controlled host clock, equal-time expiry, late approval/ref changes, no timeout grant; in-flight cancellation uncertainty retained. |
| [Re-authorization wait](https://github.com/Judgment-Pack/judgment-pack-agent/issues/226) | Durable waits, #216 | Bound never-admitted proof versus unknown dispatch, same-identity refresh, changed identity refusal, no credential publication. |
| [Resume revalidation](https://github.com/Judgment-Pack/judgment-pack-agent/issues/227) | Lease/waits, #182, #218/#219 | Real bootstrap closed until acknowledgement; changed model/policy/reference/identity; exact continuation and no new implicit authority. |
| [Attention journal/report/export](https://github.com/Judgment-Pack/judgment-pack-agent/issues/228) | Durable waits, #207/#208/#215 | One recorded attention event per park, duplicate delivery, complete retained park/wake/question provenance and explicit redaction. |
| [Run-owned background jobs](https://github.com/Judgment-Pack/judgment-pack-agent/issues/229) | Lease/waits | Owner isolation, unknown start reconciliation, restart/cancel, stale completion refusal, no model polling. |
| [Durable goal commands](https://github.com/Judgment-Pack/judgment-pack-agent/issues/230) | Plan evidence, waits, jobs | Zero-model CAS commands, evidence-required achievement, competing/duplicate command refusal, restart/report parity. |

Each build item needs both-language schemas/readers and shared handwritten vectors,
real store/adapter paths, relevant safeguard-removal tests, and opt-out byte parity.
Future protocol shapes and policy keys are introduced under new appropriate record
versions; this ADR adds no members to current published formats. Host integration,
including Desk #669/#670, remains with its owner and needs its own verification.
Global scheduling, cross-tenant ownership, notification delivery guarantees,
credential storage, automatic model substitution, unrestricted retries, and a
universal exactly-once guarantee are outside the package's scope.
