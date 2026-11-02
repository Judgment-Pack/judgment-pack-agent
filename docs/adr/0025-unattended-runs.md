# ADR-0025: Unattended runs

Status: accepted for v0.11.0. Issue #206, including its job, goal and question
additions. This is a design decision and implementation split; it does not claim
these facilities exist in v0.11.0. It builds on ADR-0008, ADR-0018, ADR-0019 and
ADR-0024. Decision 1 (one driver and fencing) is built in v0.12.0 (#223, #253; see
its amendment). Decision 2 (durable waits and questions, #224) and decision 6 (attention
and reporting, #228) are built for v0.13.0 (see their amendment); decisions 3 and 5
(deadlines and approval expiry, #225; revalidation on every resume, #227), with the run
closure of #265 (M2), are built for v0.13.0 (see their amendment); decision 4 (re-authorization,
#226) is built for v0.13.0 (see its amendment); decision 7's job core is built for v0.13.0 (#229), and its
cancellation and decision 8 (#230) are designed in the amendment after decision 8 and assigned to v0.13.0.

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
resume revalidation (decision 5) are built since (their amendment below), and so is the
re-authorization reason (decision 4, #226, its amendment above). The retired-model, changed-reference
and deadline attention reasons are not built: a retired model or a
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

### Amendment (#226): re-authorization as built

Status: built for v0.13.0 (#226). The records are `contract/schemas/run-reauth.json` (version 1) with
handwritten shared vectors in `contract/vectors/reauth/`, and members of the not yet released
`run-wait.json` (version 1): the reason and attention need `reauthorization`, the pending
`reauthorization` decision, and `reauthorization` and `reauthorized`. TypeScript is
`packages/harness/storage/reauth.ts` with `lease.ts` and `wait.ts`; Go is `go/context/run_reauth.go`
with `run_lease.go` and `run_wait.go`. The host-facing contract is in `docs/design/storage.md`
(Re-authorization waits). No checkpoint, event, lease or published report format changes. With no credential
check configured no refusal is minted and a run's bytes are unchanged, with two exceptions that apply with or
without a check. Under a run store, a tool's own failure of exactly the typed never-admitted shape is recorded
and reported as `authentication`, `tool`, `unknown` with no detail; with no run store it still reaches the
model as written. Under a fenced view, a `perform` after the dispatch has ended runs nothing.

**The trusted disposition.** The effect host's admission boundary is where never-admitted is known: the
reference effect host's optional credential check runs after the fence and the idempotency identity and
before it records the admission, with the abort and the fence checked again after it, and a refusal (`expired` or `revoked`, a bounded connection
reference, the principal's SHA-256, the scope labels held and required) is minted against the exact
admission in flight; nothing is admitted or performed. The package trusts it only when minted once,
for that live admission, before anything was performed, well formed, returned by the dispatch, from an
effect host proven for the run's store (#253); anything else, and any error after admission, is an
ordinary failure whose outcome stays unknown. A late `perform` runs nothing and takes the trust back. A
credential check's own error is replaced by one fixed failure. The writer records the typed failure
(`authentication`, `tool`, `not-started`) only for a refusal trusted for that very dispatch, and then
closes dispatch, so the head holds the refused operation as its last dispatch and the model is not asked
again. Any other failure of that shape is recorded as `authentication`, `tool`, `unknown`, so the typed
failure on a row can only mean that refusal: the park's head check and the re-send's grant both read it.

**Park and resume.** A trusted proof parks one `reauthorization` wait under the lease it was minted
under, over that head (`stale`, `unbound`, `used`, `untrusted` otherwise), waiting for the host's or
the person's action as an event; the store attaches the binding and expiry of the run's unsettled
approval, if any. Resume needs the action (`decision-required` without it), revalidates the pins
(#227) and then the action: a replacement or another principal is `identity-changed` (a new admitted
run, ADR-0019), an action from before the park is `stale`, permissions that no longer cover what the
operation requires are `permission-insufficient`; the store's claim checks them again against its clock
and records the refresh, and its lease is bounded by every ceiling of the run.

**No retry.** The continuation's fenced view (`reauthorizedRunStore`) carries one grant: the writer
sends again only the never-admitted operation the wait names, once, under its own identity, and only if
the run asks for exactly that request (in Go byte for byte, since a tool runs the bytes the call wrote and
the row's fingerprint is of the parsed form, #275); its claim clears the typed failure before the dispatch,
and nothing that may have been admitted is ever sent again. A host tool's request is its arguments, so it
keeps its identity on every adapter. A Runtime call's request carries its call id, so a new call id is a
new operation on any adapter. ADR-0008 still restores an approved call whose result was never written as
executed with an unknown outcome; re-authorization does not re-send it, and a new decision in the same
run with the same arguments sends nothing.

**A refused re-send does not wait again.** It is trusted and stops the run, but its row is no longer the
head's last dispatch, so the park answers `unbound`; the view's grant is spent, and another view of the
claim may send it once while the lease is held. Otherwise it stays unknown. We keep this limit rather
than read "last dispatch" as "last host or Runtime dispatch": it fails safe, and the wider head check
would apply to every park and every store.

The wait conformance kit gains the `reauth.*` properties: the fence (owner and epoch) and each way a head
can fail at the park, the claim's refresh (required, the parked principal, covering permissions, its
time), the attached approval, its expiry and the claim's bound, `limitRun` keeping that time, the timeout
and deadline wakes, and these across restarts. It does not exercise a repeated reauthorization park or
`reauthorized` on another wait's claim.

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

Not built here: a reauthorization wait (decision 4, #226; built since, its amendment above), attention events for a retired model, a
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

### Amendment (#229, #230): run-owned jobs and the goal

Status: the design below was reviewed before any code (the record is on #229). Three stacked changes build it: the
job core (built for v0.13.0, this amendment's first part), cancellation and the fresh fence of an abandon (#229), and
the objective (#230). Parts marked "next" are decided here and not built yet. The records are
`contract/schemas/run-job.json` (version 1) with handwritten vectors in `contract/vectors/job/`, and members of the not
yet released `run-wait.json` (version 1): the reason, pending and attention need `job`. TypeScript is
`packages/harness/storage/job.ts` with `wait.ts` and `writer.ts`; Go is `go/context/run_job.go` with `run_wait.go` and
`run_writer.go`. The host-facing contract is in `docs/design/storage.md` (Run-owned jobs).

**Formats.** No checkpoint member, event, lease or report version. Each optional checkpoint member would double the
presence-exact versions, so the records live beside the run: a job names its dispatch row, and the run names neither.
Start and read rows are ordinary rows at whatever version the run holds; generation is always 1, and readers refuse any
other (a restart is a new job through an ordinary row). Every reader that could be handed a job record refuses it, at the
v0.12.0 tag and now, in both languages: `checkRecord`, `readExecution`, `readRunEvent`, `readRunReport` and its display
form, `readRunArchive`, `readRunLease`, `readLeaseFence`, `readEffectAdmission`, and now `readRunWait` and `readWaitExport`.
`run-wait.json` is absent at every tag. A v0.12.0 driver re-sends no unknown row and cannot see jobs: do not downgrade. An
archive of a run with jobs needs the job export too, and a store backs its jobs up with its runs.

**The job's states.** Its identity is the SHA-256 of `{resource, operation}`, the claim row's id; never the model's.

| Transition | Who | Evidence |
| --- | --- | --- |
| none → `unproven` | Writer, in the claim's own commit (`RunCommit.job`), before admission | The fence, in that transaction. Identity follows the run and operation. A key digest no job or wait of the store holds (`key-reused`), at most 64 per run (`jobs-exhausted`). Refused: no row and no job, the call's answer is the refusal, and the run goes on. Unfenced: `invalid` |
| `unproven` → `started`, `not-started` | Writer on the host's typed answer, after the receipt check | The fence that registered or re-claimed it, current or not (M2). The handle and a bounded receipt reference go only to the job. A throw or an untyped answer changes nothing |
| `unproven` → `started`, `not-started`, or nothing on `unknown` | Host, `reconcileJob` | The completion key, no lease, an evidence id. First write wins. `not-started` means the authority that would run it has closed it |
| `unproven`, `started` → `completed` | Host, `completeJob` | The completion key, no lease, an evidence id, outcome `succeeded`, `failed`, `cancelled` or `unknown`. The same transaction wakes a `parked` wait on the job, unless its timeout or deadline has come |
| `unproven` stays, re-claimed | Writer, under a re-authorization grant | Current fence, only from `unproven` (`job-settled`, and the grant is not spent) |
| `not-started`, `completed` | Terminal | Same evidence: retained. Same id, other content: `conflict`. Later completion: `stale`. A started answer, finding or completion after `not-started`: kept as a contradiction, reported, and it moves nothing |

**Waiting.** `parkRun({job})` is the host's wait port; the model has no wait tool. A job park accepts a checkpoint at rest
(`running` or `complete`), refuses a pending decision (`decision-required`) and a stopped checkpoint (`nothing-pending`). It
waits for that job's completion only, with no `wakeAt`, and refuses a chat binding this release (`chat-store-unsupported`), whether
the host passes `chat` or the run's history head holds a stored chat: a person's next message would make the resume `chat-changed`,
and nothing ends such a wait short of a deadline. A run at its request limit is refused `not-resumable`: a claim would be spent
and the continuation could make no request, so the model would never be told. The store's
park transaction refuses a settled job (`job-settled`) and an unproven one (`job-unproven`), which closes the lost wake-up.
`wakeWait` takes only a timeout, which does not close the run, and a deadline. `resumeParked` returns the job as its record
says now; the host hands it to `context.jobContinuation`, and the run's first request carries one fixed line and one data
block built from the record. No checkpoint member, and no text from the host.

**What the model sees.** A start's row holds what the package composed: the job's identity, its state and the words
"started, not finished". `job_read` always answers the state from the record (no handle, receipt or host text), and says
`changed: false` with a fixed instruction not to poll when nothing moved. Another run's job, an absent one and a malformed id
all answer `no-job`; Go refuses repeated and folded names on the raw bytes. The rows of job tools record no observation, so
they are never plan evidence. A start's row proves itself only through its job: `complete_plan_step` reads the job afresh,
`completed` with `succeeded` proves it whatever the row says, and anything else is `unsuccessful-operation`. Citing an
unrelated successful row remains #181's published limit.

**The paths.**

| Crossing, from | Parsed | Recorded; shown to a person | Effect host gets | Repeat, resume | Bytes differ? |
| --- | --- | --- | --- | --- | --- |
| Start, model | `args`, for policy and the card only | Row `request` and digest; the job copies them from the claim. Card: `approvalShownArguments` | Admission over the digest; `start` gets the row's bytes | From the job, whatever the row holds; only with the job record lost does a result answer. Unknown is never re-sent | Refused: a held start needs `shownDigest`, text kept or not, before the claim (`ApprovalNotShown`). What was shown is the row's bytes |
| Start's answer, host | Typed, or the job stays unproven | Handle and receipt on the job; the row holds the composed result | Nothing | The first start wins; another handle is `conflict` | No: nothing of it reaches the row or the model |
| Job id, model | Strict: only `job`, a digest | Read row. Shown: the projection | Nothing | Never cached; an unchanged read says so. The run comes from the view's fence | Spelling only: the lookup is (run, job) |
| Completion, reconciliation, host | Closed: ids, enums, digests; no number or free text | Canonical form without the key; its digest; report | Nothing | Retained answer; a replay wakes nothing | No: only the canonical form survives |

**Lease and takeover.** An unproven job is unchanged by takeover and expiry: its row is never re-sent, and a host reconciles
it by key with no lease, so a closed or paused run can still reconcile. Any other job is unchanged by all three: the run owns
it, its key and reads hold, and a completion needs no lease. Under another subject a delegated run is `principal-changed`
(#363 as built), and an undelegated one stays `unproven` by the gateway's answer below.

**Gateway #352 M6, undelegated.** Nothing such a run holds can be verified by a gateway: lease and epoch live in the run
store. The agent presents no owner, subject or lease, only `gatewayActionMembers(job)`: `{actionKey: <idempotency key
without "sha256:">, keyScope: "authority"}`, plus `reconcile: true` when reconciling. A delegated run sends what #363 built,
with no `keyScope`. The gateway's record of the key's first scope is to prove the same subject, which is safe because the key
hashes the run and a random operation; anything else stays `unknown`. The writer never re-sends an unknown row, so
reconciling is the only re-presentation. Findings: a retained answer is parsed by the host like the first answer (a `response`
is not a commit; untyped is `unknown`); `action-key-not-dispatched` is `not-started`; anything else, `action-key-scope-conflict`
included, is `unknown`. No gateway accepts either member until #352 ships: each refuses them (a closed body), so nothing is sent, and the
host answers such a start `not-started` from that exact refusal. Nor does any gateway send `action-key-not-dispatched` yet, and it
must not without the durable closure below. The host keeps `decision` and `cites` for its reconciling request: no record here holds
them.

The gateway builds, on `/act`:
1. `keyScope: "authority"`: before reserving, exclusively create a durable per-authority index of the key's first scope,
   by a requester that may reserve or by a closing reconcile. It outlives compaction. Any request on that key under another
   scope, flagged or not, gets `action-key-scope-conflict` before anything is reserved or sent. Unflagged keys keep §1.2b.
2. `reconcile: true`: the same content checks; it never reserves and never dispatches, a freed attempt included, since the
   approval may have expired. It answers retained evidence or read-back in the sender's scope. **Where it would answer that
   nothing was dispatched, it first closes the key, exclusively and durably: an index and a closed marker for a key it has
   never seen, a closed marker for a reservation with nothing begun, and only then answers `action-key-not-dispatched`.** An
   index with no reservation answers unknown, never not-dispatched.
3. `attempt()` and `claimDispatchUnder` refuse a closed key (`action-key-closed`), which is what makes the agent's
   `not-started` true for good. A mutation row guards the no-dispatch branch at `claimDispatchUnder`.
4. Compaction and retention text for closed entries, and retention that outlasts a job's whole life.
5. Tests: a reconcile inside the reservation-to-marker window across two processes; a late first request after closure; P4c
   plain and reconciling, one mutation; a same-subject takeover; a restart.

**What a host must do.**

| Host must | Otherwise |
| --- | --- |
| Give job, lease and wait ports outliving its process, and an effect host proven for that store (`admitJobs`) | `store-unsupported`, `store-lifetime`, `effect-host-unfenced`, before any offer |
| Decide every held start with `shownDigest` (`approvalShownDigest`), text kept or not: a new surface has no unmoved host | `ApprovalNotShown`, before the claim: no job, nothing runs, and the pause stands |
| Declare `gatewayKeyScope` on a start tool of a `gateway` source | `job-host-unsupported` |
| Declare no sub-agent with a job tool | The definition is withheld; a child session offered one refuses `child-unsupported` |
| Mint a completion key per job, store only its digest here, and keep the key across its own restarts | `key-reused` or `invalid` before any row; a lost key means the job can never be completed |
| Answer `start` in the typed form, with a handle and a receipt of at most 256 bytes | Anything else leaves the job unproven, or keeps what it had. The row holds a fixed `unknown` failure that says it was not re-sent, and the model is answered from the job. A handle or receipt the record cannot hold is kept as the digest of its bytes, and the job is `started` |
| Complete only through `completeJob`; hand the wake's evidence id to `resumeParked` | Event `wakeWait` is `mismatch`; `unacknowledged` |
| Report `not-started` only when the authority has closed the start. A reference effect host cannot, its admitted set being process memory, so it answers `unknown` | A later completion is kept as a contradiction |
| Keep `decision` and `cites`, and back up job and run records together | A takeover elsewhere gets `action-key-conflict`. A job with no row in the head refuses `store-inconsistent` before any offer; a row with its job lost is no evidence |
| On `job-settled`, `job-unproven` or `no-job` at a park: continue or reconcile; none of them holds the run | The run stays leased until the host acts |

**Published behaviour.** With nothing configured no byte moves: no tool, no store call, and `RunStore` and `WaitStore` keep
their signatures. A store without the port refuses `store-unsupported` only when a job tool is offered.

**Next: cancellation (#229).** Cancellation never moves `state`. Its arguments are `{job, generation}`, and a start and its
cancel tool are declared as a pair: `job.source` must be the row's source, or `no-job`, before anyone is asked and before the
claim (H5). `no-job`, a settled job and one with no handle refuse before the card. A card reader over the record shows source,
tool, state, generation and operation digest (M4). `requested` is recorded by the writer under the fence before admission;
what is sent is the record's job, generation and handle, never model bytes; `acknowledged` (`accepted`, `refused`) comes from
the host by key. Only `completed` with `cancelled` says it stopped. The rows of cancel tools are not evidence.

**Next: abandon (#229, #230).** Abandon's transaction takes the lease under a reserved owner at a new epoch rather than
lowering it, and returns that fence. Old workers get `stale-epoch`. Cancels go through `EffectHost.dispatch` with the stable
operation `sha256({command, job, generation})` and no run commit. The command stands for the person's approval, and that is
stated as the one exception to decision 7's approval boundary. Past a deadline no lease can be granted, so `requested` stays
undelivered and says so.

**Next: the objective (#230).** Host commands only, each with id, actor and expected revision; no model call and no run
commit. `create` makes one from none or from terminal, under an identity never used in this run; there is no `replace`. `create`
over a terminal objective refuses while a wait parked before the abandon is unresolved (M10). `bind` takes the head's accepted
Plan, nonempty unique steps with origins, and refuses a step already `completed` or a plan an earlier objective bound (M11).
`achieve` needs head revision and digest; the accepted Plan as bound, nothing pending, no `changed` or `removed` departure on a
required step and no change in their relative order (M17); each required step `completed`, its evidence re-verified from the
head's book, and a delegated step by the writer's history check, a store without history refusing (M16); a start cited as
evidence needs its job `completed`, `succeeded`. Unsettled jobs block `achieve` unless the command waives them by id, and the
waiver is journaled; settled jobs that did not succeed are listed on the achievement. `pause` and `resume` move a disposition,
never a state: `acquireLease` and `resumeWait` refuse `objective-paused`, and the answer reports the held lease's owner and
expiry, so paused is not read as stopped. An objective requires a leased run, chosen at `create`. Resume writes no checkpoint,
wait, budget or approval. A pause longer than an approval's TTL ends that wait and closes the run for good. `inspect` is a read
with a count and a last row; capacity is reserved so a state-changing command is never refused for size (M12).

**A held start and #277.** `shownDigest` on the decision is carried through a parked approval (`decisionFor`, and
`resumeParked`'s answer; Go passes the decision whole), and a start's decision must carry it, text kept or not, at the
continuation's opening, before the claim. With #277's one canonical form the digest it proves is the row's `requestDigest`.

**How the design review was answered.**

| Finding | Change |
| --- | --- |
| H1 a negative reconcile is not stable | `not-started` means closed at the authority: the gateway closes the key before it answers, and a later completion or start is kept as a contradiction and reported. Obligation stated; kit rows `job.contradiction.*` |
| H2 a grant re-sends a closed job | The re-claim is a store transition under the fence, only from unproven; `job-settled` otherwise, and the grant is not spent |
| H3 job rows pass as evidence | Job tools' `successful` is false, so no observation is recorded. Achievement lists every job that did not succeed (next) |
| H4 `rebindJob` is an unregistered start | Cut. Generation is 1, and readers refuse any other |
| H5 a cancel reaches another source | Next: tools declared as a pair, `job.source` is the row's source, before the card and the claim |
| M1 reconcile needs a fence it cannot get | Reconcile is by the completion key, with no lease, first write wins |
| M2 a late answer is lost | The answer is taken from the fence that admitted the start, current or not, and whatever the call's signal says |
| M3 evidence at the wrong time | The proof reads the job: `completed`, `succeeded` proves it whatever the row says; anything else `unsuccessful-operation` |
| M4 a cancel's row and card | Next: `{job, generation}`, a card reader, refused before the card |
| M5 refusals strand a row | Registration is in the claim's own transaction: no window, no `unbound`, no store parse of the head. A key is checked before the claim, and `shownDigest` at the continuation's opening, before the claim |
| M6 `poll-refused` does not stop polling | Cut: it always answers, says unchanged and says not to poll |
| M7 a leg's end and a resume | Statuses, the pending-decision refusal, the job on the resume, the fixed continuation block; chat-bound job waits refused |
| M8 pause goes too far | Next: only `acquireLease` and `resumeWait`, and the answer reports the held lease |
| M9 abandon's delivery | Next: a fresh fence and stable cancel identities |
| M10, M11, M12 | Next: as stated under the objective; `replace` and one row per `inspect` cut |
| M13 nothing identifies a start row | Start rows are package-composed and record no observation, so a lost job record leaves them no evidence. The upgrade guide says what an archive needs |
| M14 the gateway mapping | Answers parsed as the first was; `decision` and `cites` are the host's; retention in the gateway's list; a gateway's refusal of the members mapped by the host |
| M15 the start's answer | Its own row in the paths; the package composes the row's result; `started` after `admit` |
| M16, M17 | Next: as stated under the objective |
| M18 host obligations | The table above; chat-bound waits refused rather than kept current |
| L1 two "keys" | "Completion key" and "action key" throughout |
| L2 | Next: `achieve` under `paused` is independent; `create` resets; a terminal objective has no disposition |
| L3 | In the gateway's list |
| L4, L8 | Job tools are host-kind only, and a Go-written text fails closed in TypeScript |
| L5 | `receipt` is a bounded reference |
| L6 | Job records hold ids and digests; a handle is the host's reference. No redaction in this release |
| L7 | A delegated run sends no `keyScope` |
| Scope | Three stacked changes; `rebindJob`, `job_list`, the renew and unfenced-commit refusals, `replace`, one row per `inspect` and chat-bound job waits cut |

**The clean room's findings on the job core, in this amendment.** The record is on the first PR.

| Finding | What changed |
| --- | --- |
| H-1 a start that ran was reported as invalid input, and a retry started a second job | After `perform` returns, nothing reads as refused input. The row holds one fixed failure (`unknown`, `tool`, `unknown`, `JobStartUnrecorded`, pinned in the job vectors), and the model is answered from the job, as a repeat is. A refused re-claim on a closed or started job is answered the same way, with its grant unspent. What `start` throws is no answer, so its text reaches neither the row nor the model. A handle or receipt the record cannot hold is kept as the digest of its bytes |
| M-1 the kit passed fourteen stores that lose a job or a completion | It drives both commit paths, reads every job and wait whole across its restart, and holds re-claims, answers and parks across runs and fences. Each of those stores is a negative |
| M-2 the session did not hold the effect host | `attachJobs` runs `admitJobs` over the view's own store and host, before any offer |
| M-3 a contradicted job said nothing is running | Its own fixed note |
| M-4 a run at its limit parked | `not-resumable` |
| M-5 a run under a stored chat parked | `chat-store-unsupported`, read from the history head |
| M-6 jobs and rows restored from different moments | `store-inconsistent`, before any offer |
| M-7 Go's dispatch travelled in its context | Stripped where the receipt admission is |
| M-8 a child and a job tool | The declaration is withheld in both languages; `child-unsupported` in both sessions |
| M-9 statements only the unbuilt gateway makes true | Reworded. `gatewayActionMembers` takes the job, which a `start` and a leaseless reconciliation can both read |
| L-1, L-7 | Host text as above; a repeated contradiction id with other content is `conflict`, and an answer under another run's fence is refused, each now held by the kit |

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
