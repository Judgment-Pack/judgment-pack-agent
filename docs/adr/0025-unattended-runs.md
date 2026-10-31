# ADR-0025: Unattended runs

Status: accepted for v0.11.0. Issue #206, including its job, goal and question
additions. This is a design decision and implementation split; it does not claim
these facilities exist in v0.11.0. It builds on ADR-0008, ADR-0018, ADR-0019 and
ADR-0024. Decision 1 (one driver and fencing) is built in v0.12.0 (#223, #253; see
its amendment). Decision 2 (durable waits and questions, #224) and decision 6 (attention
and reporting, #228) are built for v0.13.0 (see their amendment); decisions 3 and 5
(deadlines and approval expiry, #225; revalidation on every resume, #227), with the run
closure of #265 (M2), are built for v0.13.0 (see their amendment); decisions 4, 7 and 8
(#226, #229, #230) are not built and are assigned to v0.13.0.

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

Status: accepted for v0.12.0 (#223, #253).

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

### Amendment (#224, #228): waits, attention and the report as built

Status: built for v0.13.0 (#224, #228). The records are `contract/schemas/run-wait.json`
(version 1) with handwritten shared vectors in `contract/vectors/wait/`; TypeScript is
`packages/harness/storage/wait.ts`, Go is `go/context/run_wait.go`. The host-facing contract is in
`docs/design/storage.md` (Durable waits, attention and the report).

The wait is a record of its own, kept by the store beside the run: the run's checkpoint and event
formats are unchanged, and the park writes nothing into them. The wait's members are its run-scoped
identity, `reason` (`approval`, `budget` or `question`), the `pending` decision read from the
retained record (the checkpoint's asked approval, its paused allowance, or the retained
`user_question` event with its question, options and recommendation; never from the caller), the
`condition` (an exact `{kind, subject}` event, a `wakeAt` time, or both), an optional `timeoutAt`,
the `continuation` identity, the parked head (`{revision, digest}` of its checkpoint bytes), an
optional chat binding, the digest of the host's wake key, the host's provider `release` evidence
(`released`, `not-held` or `uncertain`), `parked` (owner, epoch and store time), the `attention`
event identity and `state` (`parked`, `woken`, `timed-out`, `resumed`). A question waits for an
event and has no wake time.

**Park.** One store transaction checks the fence and the run head, refuses a second parked wait
(every wait not yet resumed, since #265 M2: see the amendment of decisions 3 and 5),
commits the wait and its one `needs_attention` event, and releases the lease. The paused checkpoint
was committed under the same fence before; the park binds it by revision and digest rather than
writing it again. A repeat of the same park returns it; uncertain release evidence is retained and
the park is not clean: resuming requires the host's reconciliation of every uncertain entry.

**Wake.** The host presents bounded evidence `{resource, wait, evidence, kind, event?, answer?,
actor?, key}`; the key's SHA-256 must be the wait's, so a model-supplied name or another run's key
is refused (`unauthenticated`). Time and timeout wakes are decided by the store clock, and equal
time is due. The first valid wake commits the disposition `{evidence, kind, at, digest}` (the digest
is the evidence's without its key); a duplicate returns it, the same evidence id with other content
is `conflict`, any later wake is `stale`, and none of them changes anything. A question's event wake
carries a real answer (an offered option or non-blank text) and its host-attested actor; its timeout
records `unanswered` and never the recommended option. Approval and budget wakes carry no answer:
a wake is never an approval or a grant.

**Resume.** The host names the evidence whose acknowledgement it holds; a wake the store does not
hold is `unacknowledged` and is reconciled by reading the wait, never by dispatching. The package
requires the person's decision for the asked call (approval) or a grant from the paused limit
(budget) before anything else (`decision-required`), and the store claims the continuation once
after revalidating the head (`checkpoint-changed`), the release reconciliation
(`release-uncertain`) and, for an approval or budget wait, that it did not time out (`timed-out`),
taking a new lease against the parked revision (#223); since #265 M2 the claim takes that lease in
its own transaction. A second claim is `already-resumed`. A chat binding is revalidated through the
ChatStore before the claim (`chat-changed`); Go has no ChatStore and refuses a chat-bound resume as
`chat-store-unsupported`. The host then continues the run through the fenced
store with the returned decision, as ADR-0008 continues a pause.

**Attention and the report.** The `needs_attention` event names the wait, its reason, the action
needed (`approval-decision`, `budget-grant`, `question-answer`) with its subject, the condition and
deadline, and whether release is uncertain; its identity is the SHA-256 of `{resource, wait}`. The
store records it and never sends it; a host's delivery attempts are separate records, a repeat
returns the first and a failed one rolls back nothing. The report (`$defs/report`) lists each park,
wake, timeout, answer or unanswered end and resume with its owner, epoch, evidence, actor and store
time, read from the retained waits (`$defs/export`) and never from a prompt; in a report built with
the host's time, content past the host's `retainUntil` is `expired` (the export and `readWait` keep it
until the host redacts the wait), and a redacted wait's content is `redacted`, each with its digest kept.
The report carries no key, fence or decision and grants no resume.

Refusals are `invalid`, `nothing-pending`, `wait-pending`, `conflict`, `checkpoint-changed`,
`no-wait`, `unauthenticated`, `mismatch`, `not-due`, `stale`, `unacknowledged`, `already-resumed`,
`decision-required`, `timed-out`, `release-uncertain` and `chat-changed`; capability refusals are
`store-unsupported` (no wait or lease port), `store-lifetime` (waits or leases end with the
store's process) and `chat-store-unsupported`. Deadlines on runs and approvals (decision 3) and
resume revalidation (decision 5) are built since (their amendment below). The re-authorization,
retired-model, changed-reference and deadline attention reasons are not built: a retired model or a
changed reference is a named revalidation refusal the host reports, and a deadline ends a wait with a
recorded deadline wake.

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

### Amendment (#225, #227, #265 M2): deadlines, approval expiry, revalidation and run closure as built

Status: built for v0.13.0 (#225, #227, and #265's M2). The records are
`contract/schemas/run-resume.json` (version 1) with handwritten shared vectors in
`contract/vectors/resume/`, and members of the not yet released `run-wait.json` (version 1):
`limits`, `deadline`, `approval`, `pins`, `expired` and `revalidation`, the state `expired`, the wake
kind `deadline`, the report decision `deadline`, the attention member `deadline` and the refusals
`deadline-passed`, `closed` and `approval-ttl-required`. TypeScript is
`packages/harness/storage/resume.ts` with `wait.ts`; Go is `go/context/run_resume.go` with
`run_wait.go`. The host-facing contract is in `docs/design/storage.md` (Deadlines, approval expiry
and resume revalidation). No checkpoint, event, lease or published report format changes, and with
nothing configured the run's bytes are unchanged.

**Run closure (M2).** A committed park closes the run: until its wait is resumed, the store refuses
to lease the run (`wait-pending`) or park it again (every wait not yet resumed counts, woken ones
included), and a terminal wait (a timed-out approval or budget wait, or an expired one) closes it for
good (`closed`). Through the store's ports (since #265, its unfenced commit as well as its lease),
the resume's claim is the one way back: one store transaction checks the
acknowledged wake, the deadline, the head, the release reconciliation and a revalidation of the
wait's own pins, then acquires the run's lease against the parked revision and claims the
continuation. The package checks the decision and revalidates before that claim; nothing is leased
or claimed before every check passes.

**Deadlines (decision 3).** A run's deadline (`limitRun`) is kept by the store, the earliest wins, and
no lease of the run is granted, renewed or left held past it, so no claim commit, dispatch or effect
admission of a fenced run is accepted at or after it (equal time is past). A park's `limits` stamp
its deadline from the store's time: the earliest of the run's (a new wait never extends it), the
wait's own limit and, for an approval, its TTL. From the deadline no wake, late answer or decision
and no claim is accepted (`deadline-passed`); only a host `deadline` wake, due at the deadline, ends
the wait, terminally (`expired`; a question ends `unanswered`). An admitted effect keeps its unknown
outcome and is never re-run (the effect host's `already-admitted` holds across leases).

**Approval expiry (decision 3).** An approval wait is unattended approval and needs a bounded
positive TTL (1 ms to 30 days; absent is `approval-ttl-required`). Its binding (the call id, the
digest of the reviewed call and arguments, its source) and the pinned source, descriptor, Plan and
policy identities are what the person reviewed; the approval expires at the park time plus the TTL.
From the claim until the approved call settles (its checkpoint records it executed, refused or
answered), the expiry bounds every lease of the run, a takeover's or a re-acquired one included, and no
lease is granted at or after it; renewing never extends it. So no commit, effect admission or dispatch
of the approved call is accepted at or after the expiry, whoever holds the run, and every checkpoint
committed meanwhile must carry exactly the reviewed call (`approval-changed` otherwise). A changed descriptor of its source or a changed policy
identity refuses it (`approval-changed`), and a changed source identity or Plan lineage refuses any
resume.

**Revalidation (decision 5).** Each park pins the trusted host's admitted model, the resolved policy
identity, the Plan lineage, source identity and approval-source descriptor digests and the last
artifact revisions, read from the checkpoint. Every resume needs the host's current evidence
(`evidence-missing` without it, or without a counterpart for a pinned item) and refuses a changed
identity or lineage (a new run is needed), a retired model (an explicit decision; nothing is
substituted), a changed model or reference without its exact explicit admission, a changed policy
without the trusted host's new layer, and an approval under a changed identity. The resumed wait
records the revalidation, with every change's old and new value.

Not built here: a reauthorization wait (decision 4, #226), attention events for a retired model, a
changed reference or a deadline (they are named refusals and a recorded deadline wake), a package
transition that ends a wait a revalidation refuses for good, a Go ChatStore, and host store kinds:
the deadline, closure and claim rules live in each host store's own wait and lease ports (their
conformance kit is built since: the amendment below).

### Amendment (#265): the pre-release batch as built

Status: built for v0.13.0 (#265, the MEDIUM findings of the #264 and #266 security reviews). No
checkpoint, event, lease or published report format changes. The unreleased `run-wait.json` version 1
gains the refusals `key-reused` and `not-resumable`, a longer wake key (32 to 512 printable characters;
the readers also require 16 distinct ones), a budget wait in model requests only, and an approval wait
expired after its claim; its vectors are updated by hand, with `contract/vectors/wait/decision.json`
new.

- **A wake key belongs to one wait (#264 M1).** The host mints a fresh, high-entropy key per wait; a
  short or repetitive key is refused wherever a key is read, and the store refuses a park whose key
  digest any other wait of the store holds. So another run's or another wait's key is
  `unauthenticated`, and a cross-run wake changes nothing.
- **No pause parks that nothing can resume (#264 M3, #266 M5).** A pending plan decision and a
  provider-managed allowance are refused `not-resumable` at the park, so neither closes its run. Every
  held wait then has an exit: its wake and claim, its timeout, or its deadline wake; a host with no
  deadline set can set one with `limitRun`, which reaches the waits already parked (#266 M1). A resumed
  approval wait whose approved call has not settled is ended by a deadline wake at the approval's
  expiry (#266 N1): `expired`, the call's outcome unknown, the run closed, and no dispatch authority
  granted; the host reconciles the effect through its effect host.
- **The closure and the claim (#266 M4, M6, N2, N3).** The store's unfenced commit refuses a run a wait
  holds, as its lease does. The claim's lease ends no later than every ceiling of the run, an earlier
  unsettled approval's included, and a ceiling not after the store's time refuses the claim.
  `resumeParked` reads the store's wait and claim through the readers and refuses one that is not this
  wait resumed under a lease within the run's deadline and the approval's expiry. The commit port's
  documentation states the store's obligations: the per-run approval ceiling and binding, the check of
  every committed checkpoint, settlement in the commit's own transaction, and all of it across the
  store's restarts.
- **The wait conformance kit (#264 M6).** `runStoreWaitConformance` (Go
  `CheckRunStoreWaitConformance`) certifies a host store kind's wait port as #223's kit certifies its
  lease port: the key check, the single winner, the lease release, claim-once, the run closure, the
  deadline and approval ceilings, and the commit-time approval binding, each again after a restart of
  the store. The memory kind passes it over a fresh instance built from a copy of its state.
- **One rule in both languages (#264 M4, #266 N2).** A budget grant is dated by a store-clock time;
  blank text is only JavaScript's `\s` and U+0085; option labels fold per code point (U+0130 as `i`
  with U+0307, final sigma as sigma).

The LOW findings of both reviews ride.

### Amendment (#260): a delegated run's lease, wait and resume

[ADR-0029](0029-agent-principals.md) binds decision 1's lease and decision 5's revalidation to a
delegated run's principals. A lease and fence of a delegated run are `run-lease.json` version 2 with
`principal`, an acquisition or fence under other principals refuses `principal-changed`, and a store
kind declares that it keeps them. A wait's pins hold the binding, its claim lease holds it, and the
resume's evidence must name it: evidence of none is `evidence-missing`, another is `principal-changed`,
and no admission clears it. Undelegated runs write version 1 and the earlier pins unchanged.

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
