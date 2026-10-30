# Storage foundation

Agent accepts a host RunStore in TypeScript `ContextInput.runStore` and Go `Input.RunStore`.
The host supplies an opaque run id and explicitly chooses create or resume. Loading a run,
GET, reconnecting a viewer, and Stop grant no execution authority. The store is already bound
to the host's authorized resource; Agent derives no project paths, locks or credentials.

`load`, atomic `commit`, and bounded `readEvents` share opaque host revisions and run-scoped
cursors. A commit compares the expected revision, replaces the checkpoint when supplied,
and appends events in one transaction. Event-only commits advance the revision. A commit id
can acknowledge the exact original transaction again; different content under that id refuses.
A receipt proves that storage transaction, never current authority or external success.

One root writer owns all parent/role/child/continuation/failover transactions. A pre-dispatch
claim must be acknowledged before the host is called. Results must be acknowledged before
dependent work continues. A writer conflict, malformed receipt or unknown acknowledgement stops dispatch; it
never overwrites, rebases or retries a guessed transaction. An uncommitted refusal is delivered
outside the authoritative stored log. Events use `{version:1,scope,event}` envelopes; their
scope is trusted execution/role/child identity, independent of provider wire call ids.

Durable model calls quarantine at most 4 MiB of the original response in private memory.
The stored claim remains `claimed` while headers and body arrive. Explicit source-family terminal
evidence and an acknowledged result commit are required before either SDK can observe the response,
execute tools, accept a final answer, or send dependent requests. A private one-use claim handle
binds that acknowledgement to the writer, scope and attempt; wire ids and checkpoints cannot
recreate it. Exact response bytes are replayed transiently, never retained as provider bodies or
private reasoning. Durable response progress is therefore delayed until verification and storage
acknowledgement; calls without persistence keep their streaming behavior.

For streamed OpenAI-compatible responses, both a recognized choice finish reason and `[DONE]`
are required. Anthropic requires a recognized stop reason, closed blocks and `message_stop`;
Gemini requires an explicit recognized terminal reason or explicit blocked outcome. Whole JSON
must be complete and contain the corresponding terminal fields. EOF, local close, iterator
return and SDK advancement prove nothing. Missing, invalid, in-band-error, cancelled and oversized
responses remain unknown and close dispatch. A fully consumed non-2xx refusal is acknowledged
before the bounded read-only retry rule applies. Only a fully consumed, bounded, valid UTF-8 JSON
object with HTTP 4xx (except 408) and the existing classified `refused` outcome is a verified
refusal. Its model/read dispatch stores `state=result` together with `failure.outcome=refused`:
this means a known transport outcome, never task success or account release. On restore this
known refusal cannot substitute its old request for a later request. Malformed bodies, 408/5xx,
ordinary pending models and arbitrary unknown/refused labels remain unknown; unresolved
non-read claims retain their replay barrier. A missing or invalid acknowledgement grants no
completion or refusal authority.
Lost result acknowledgement remains sticky `StorageOutcomeUnknown`, with no dependent dispatch.
These transport completion checks never establish provider-account release or task success.

Cancellation and final persistence have distinct owners. A host that needs Stop first calls
Go `PrepareDurableInput` (TypeScript `durableSession`) and retains the returned root binding
through the actual adapter run. On an explicit Stop it calls `RunWriter.CloseDispatch(binding)`
(`closeDispatch` in TypeScript) before canceling provider work. Closure is immediate, including
while a prior transaction is awaiting its store. It prevents new dispatch and ordinary
checkpoint/event publication; it never grants a new run or replays an operation. A canceled
model callback also closes dispatch, including when its host envelope remains uncanceled.
`Failed()` / `failed` therefore includes dispatch closure and does not itself mean that a
storage acknowledgement was lost.

The same admitted writer exposes Go `FinalizeStop(hostContext, binding)` and TypeScript
`finalizeStop(binding, hostSignal)`. The host supplies a separate uncanceled persistence
context/signal. Agent caps this operation at five seconds, including writer/queue acquisition,
and honors a shorter host deadline. No detached background write is created. The host must keep
its resource writable through this acknowledgement and coordinate its outer record/revision
with the original canonical writer; terminalizing the host resource first cannot be repaired
by Agent. Loading a checkpoint or constructing a competing writer is not this Stop protocol.

Finalization derives the root from its latest acknowledged checkpoint, changes status to
`stopped`, and atomically commits that checkpoint with ordered `execution_checkpoint` and `end`
event envelopes under the same run identity and expected revision. The returned events may be
delivered only after acknowledgement. Existing exact completed results and observations remain
unchanged. A still-claimed operation becomes existing `state=unknown`; already acknowledged
results, including known refusals, retain their exact disposition. Cancellation is never model
success or proof of provider-account release. A canceled dispatch context does not attempt a
result write or poison the persistence owner: its admitted claim stays unknown for this bounded
host finalization. A canceled callback under an uncanceled envelope may acknowledge that unknown
outcome first. Late callbacks cannot replace stopped state with success. Restoring stopped state
opens no dispatch, including an explicit resume of that stopped identity.

CAS conflicts and actual failed/invalid storage acknowledgements remain latched; finalization
cannot clear them or retry a guessed transaction. A failed attempted finalization is a typed
`StorageOutcomeUnknown` (or the existing typed CAS conflict), including expiration while a
transaction outcome is unresolved. Expiration before writer/queue admission performs no stop
transaction. Hosts must honor commit cancellation; a noncooperative store cannot establish an
acknowledged stopped state by the deadline. TypeScript can return bounded uncertainty while
such a store remains outstanding; Go RunStore implementations must return when their supplied
context ends. The serialized record inventory is unchanged, including historical checkpoint 1.

The coordinator takes root snapshot ownership after the parent invocation has returned and its
active root is acknowledged. Child transactions retain that latest acknowledged root, rather
than asking the finished parent controller for its stale lifecycle. The next admitted root
continuation binds its own live snapshot again. The transfer follows every returned invocation,
on the model capability as on the agent capability, so the coordinator's final `end` records the
retained chat's scope rather than the released session's (agent#248). Child checkpoints, scoped
claims, observations, provider release proof and continuation bytes continue through the same
root revision owner.

Non-read calls reuse only a recorded result in that same trusted scope and admitted
source/tool/argument fingerprint. An unresolved claim refuses as `HostOutcomeUnknown` even
when the call might never have started. Intentional repetition needs a new host-authorized
run identity. This is conservative dispatch recovery, with no exactly-once external-effect claim.

Checkpoint version 2 carries a bounded durable journal, trusted acknowledged operation and
artifact observations, children and the four delegation handoff states. Prepared request bodies
and exact continuation frames are retained without transport headers or private reasoning.
Redaction, incompatible admitted identities, bounds, or absent exact bytes cause
`ContinuationNotRecoverable` or `ContinuationIdentityChanged` before dispatch. A result whose
commit was not acknowledged surfaces `StorageOutcomeUnknown`; it never claims the effect did not
happen. Checkpoints
remain at most 4 MiB, with at most 400 dispatches, 400 observations and 32 children. Structural
checkpoint nesting permits four record boundaries; arbitrary declared payloads stay opaque.
The record writer preserves lifecycle and computes the final digest after decoration/guarding.
Historical checkpoint version 1 inventories remain frozen.

MemoryRunStore and MemoryChatStore are ephemeral test kinds. RunStore conformance is
parameterized for host kinds: CAS, atomic failure, idempotency, ordering, cursor scope, byte
copies and bounds. It cannot certify a filesystem/database implementation Agent does not ship.
Existing checkpoint callbacks use the adapter's limited legacy profile: serialized acknowledged
checkpoint delivery, process-local CAS and no recovered event log. They preserve request-cap,
approval, output-guard and standalone legacy handoff behavior, without exact prepared-request
journaling or strict durable model-completion claims. Adapter kind selects this capability; no new
host option is required. Explicit RunStore journals cannot be imported through this profile, and
its adapter cannot construct a full journal writer. The legacy `durable:true` event flag denotes
acknowledged checkpoint delivery, never physical crash persistence or provider completion.
Ephemeral MemoryRunStore likewise proves only its stated in-process capabilities. Full RunStore
execution keeps strict claim/body/acknowledgement/recovery gates, including stopping an ambiguous
504 response; legacy callback execution retains the admitted bounded read-only fallback rule. The legacy ChatStore adapter likewise requires external writers to be
stopped. Neither adapter qualifies as a durable concurrent host kind.

TypeScript ChatStore asynchronously gets one opaque chat id, lists bounded pages, and updates
plans/interaction against an opaque revision while retaining unrelated host fields. Planning
uses that revision for CAS, never array identity or rollback over another writer. The host owns
active-run admission. New plan references contain opaque artifact/dialect ids and content
SHA-256 digests, distinct from chat/run revisions. A host-scoped resolver may supply bytes for
digest verification. Legacy file/revision locators require an explicit host migration resolver;
the next proposed plan revision rewrites them to current references. No filesystem access or
URL inference is built in. Go has the corresponding plan/reference/pin helpers; it has no ChatStore.

Desk proposals #599 and #605–613 inform these consumer boundaries. Host leases, issuer/subject
authorization, secrets/key custody, RuntimeDir and companion locations remain host owned.
Only the filesystem kind is proposed as shipped by Desk; Agent introduces no backend registry
or cloud-backend claim. Later plan/report logic can consume the stored canonical bytes and
acknowledged scoped observations, without treating model prose as evidence.

Provider account release is a separate, live host acknowledgement. `AgentCallbacks.providerRelease`
contains a unique invocation token and an acknowledgement function. A host acknowledges that token
only after actual provider release; missing, late or wrong tokens produce `ProviderReleaseUnknown`
before another child or continuation. The failure remains in the durable journal and resume refuses
a guessed release. The capability is never serialized, and a stored receipt cannot recreate it.
The current browser socket transport closes locally on abort and supplies no remote release proof;
it therefore cannot perform a durable provider-managed handoff without a host bridge supplying
this acknowledgement. Normal responses that start no child/continuation remain available.

A `pendingHandoff` is a recorded intent, never release proof. If interruption precedes the
acknowledged `released` checkpoint, resume refuses `ProviderReleaseUnknown` rather than promoting
the intent. Removing persistence while restoring a durable checkpoint refuses
`ContinuationNotRecoverable`. Callback compatibility remains single writer and cannot certify
provider release; hosts needing durable handoff use the RunStore and explicit release seam.

Retained context values bind their original JSON text, including object member order. Strict
RunStore checkpoint 2 publication adds optional `valueJSON` beside `value` after validating the
existing SHA-256 and UTF-8 byte count. Resume bounds and parses the companion, verifies canonical
semantic equality to `value` and the exact original digest/byte count, then restores the original
representation. Guarding cannot change or remove either side and still publish the old companion.
Nested child contexts follow the same rule. Companion duplication counts toward the unchanged
4 MiB checkpoint limit. Legacy records without the field retain their exact prior digest rules;
already corrupted records cannot be repaired by recomputing a digest. The limited callback adapter
preserves ordered legacy JSON delivery and does not claim the strict canonical journal contract.

A nested child record is converted only after its original supported shape, complete header digest
and execution scope validate. The detached publication copy uses the current checkpoint writer,
which gives the new representation its enclosing checkpoint 2 digest while preserving the original
`retainUntil` and retained-entry identities. Invalid or unknown-version children are never re-signed.
No operation, artifact, accepted-plan or opaque payload digest is rewritten by this conversion.

## Retained history and complete chat snapshots

The optional `HistoryRunStore` capability is one injected owner, with version 1 and the original
RunStore port. Its immutable staged objects are resource scoped; a digest does not grant access
across chats or runs. `commitHistory` (`CommitHistory` in Go) compares the run revision and an
optional chat snapshot revision in the same transaction. There is no cross-store transaction.
MemoryRunStore implements this contract under its original queue/lock, including idempotent
acknowledgements and before/after-commit faults. Legacy checkpoint callbacks expose only RunStore.

Every Memory commit has a retained row, including checkpoint-only commits. Store-assigned commit
and event sequences determine order; `recordedAt` is a host clock observation and may tie or move
backward. Revision-bound pages preserve the selected view as later commits arrive. Exact original
checkpoint/event bytes are independently content addressed. Reads detach their results. Staged
objects are invisible until a committed manifest or history row publishes them. Hosts own
unreferenced staging lifetime. This foundation honestly reports `legacy-incomplete` or
`recording-incomplete`; it does not claim full archive coverage, provider capture or replay.

A stored object is at most 4 MiB, a stage/write transaction at most 8 MiB and 256 objects/events,
and a chunked chat snapshot at most 64 MiB (16 chunks plus manifest). History writes using the
optional capability stop at 20,000 commits or 10,000 events. Existing ordinary RunStore capacity
remains unchanged. Pages contain at most 100 rows/8 MiB; object reads return at most 4 MiB.
New canonical envelopes use valid UTF-8/Unicode, exact safe integers and depth at most 64;
opaque JSON source strings preserve finite JSON.stringify numbers, absence, false, null and member
order, with the same depth/byte limits. Invalid or unsupported restore refuses before exposing
any live context. Serialization refuses capacity overflow instead of dropping or evicting state.

`ChatContext.snapshot()` / Go `Snapshot` and `ChatContext.restore(bytes)` / `RestoreChatContext`
carry the full chat: entries and ages, result/cache/gone state, pending and already-offered evictions,
task threads/reusable results, turn counter, disposal and every active turn. Identity tables preserve
shared task threads, visibility sets and eviction objects, including offered objects already removed
from the pending list. Opaque entry/exchange/question JSON has one authoritative source string;
there is no independently trusted parsed twin. Meaningful insertion order remains. Only Go map
order and visibility membership order are nonsemantic: both implementations sort these keys by UTF-16
code units. Go false membership entries normalize to absence. Same-domain snapshots roundtrip to the
same canonical bytes. Go nil task visibility (unrestricted) stays distinct from an empty restricted
map; the wire decoder recognizes it, but TypeScript restore explicitly refuses this Go-only domain
because its public TaskThread requires a Set. Active unrestricted versus restricted turns are
portable in both languages.

For durable runners, pass the same `StoredChatScope` as `context.chat` and
`context.runStore.context` (Go `Input.Chat` and `DurableRunInput.Context`). The host chooses its
chat identity and original owner. `restoreStoredChatScope` / `RestoreStoredChatScope` validates the
latest complete chat view while retaining the exact original run-to-turn binding. It refuses a
missing/closed/rebound turn. The original writer claims both run and latest chat revisions before
exposing a resumed writer. It does not increment the saved turn counter. Pinned historical reads
remain available separately through `readStoredChatSnapshot` / `ReadStoredChatSnapshot`.

A paused runner detaches its invocation ledger while retaining the logical turn for explicit
continuation; approval and allowance rules still apply. A terminal writer first freezes the real
checkpoint and its held entries, actually releases only its exact ChatTurn, captures the resulting
chat, and acknowledges the joint run/chat CAS with `context.closed:true` before delivering terminal
checkpoint/end. Other open turns remain. Generic invocation ledger cleanup follows. Failure or lost
acknowledgement prevents successful terminal delivery; no rollback, provider release or replay is
inferred. Stop continues through the original writer's bounded host-signal finalization.

A host using direct `contextualSession` / `ContextualSession` owns invocation cleanup: call its
context's close method, then explicitly await `StoredChatScope.close()` / Go `Close(ctx)` when
abandoning or finishing the logical turn outside the managed runner. This acknowledges the actual
released chat state; it does not terminalize a run checkpoint or supply execution authority.
Skipping this acknowledgement is an incomplete durable close. Ordinary unconfigured chat/run
producers and their released checkpoint2/event1/policy1/report1 bytes retain their defaults.

A durable critic keeps the existing isolation semantics: starting the independent phase really
releases the author's chat turn, making its unreferenced entries eligible for eviction, and
creates a private retrieval ledger. History version 1 records the closed author binding plus
`privateScope`; it does not reopen that author turn. The original writer jointly acknowledges
the closed chat view and a checkpoint whose ledger scope equals `privateScope` before the
first critic dispatch. Other active turns remain in the latest shared chat view.

A restart may reuse this already isolated phase exactly once only when the validated private
binding, checkpoint ledger scope, and the original writer's typed unresolved critic request
agree. It preserves that request's source, model, bytes and recovery rules. A pending author
or reformat request refuses critic entry; private metadata alone does not grant a skip. A
later independent review performs real isolation and receives a fresh scope. Completed or
stopped private runs cannot resume. TypeScript refuses the invalid phase directly; Go's
existing void phase-start API latches the refusal in the stored source, so the original
writer refuses before any physical dispatch. Failed or unknown joint acknowledgements never
become successful dispatch or terminal acknowledgements.

The wire snapshot retains ordered arbitrary JSON arrays for Go's public `TaskThread.Exchanges`
and `ReusableResult.Questions` (`[]*Value`). Go restores those existing host values without
projecting away extra fields. TypeScript's live restore preflights its required exchange and
question member types, as well as required thread visibility, before creating a live context.
It refuses unsupported Go-only row semantics or unrestricted task visibility; this is an
explicit cross-language limitation. Extra host members on otherwise valid TypeScript rows
remain intact. Both decoders can read these bounded wire forms independently of live restore.

## Run leases and fencing (#223)

This section is for the implementer of a host store kind's lease (desk #609, one lease per store
kind) and for a reader of the same records in another process (runner #147: worker restart and
fencing). It implements ADR-0025 decision 1; the vocabulary is that decision's and its amendment's.
The records are `contract/schemas/run-lease.json`, version 1, with handwritten shared vectors in
`contract/vectors/lease/`. TypeScript is `packages/harness/storage/lease.ts`; Go is
`go/context/run_lease.go`. Both read the same vectors.

**The lease.** One resource (a run id, or a chat id for a chat-scoped lease) has at most one row:
`{version:1, resource, owner, epoch, expiry, state}`. `owner` is an opaque storage identifier the
acquiring worker chooses. `expiry` is the **authoritative expiry**: the store's own clock plus the
requested duration (1 ms to 24 hours), written as UTC with exactly millisecond precision. A caller
never supplies a time, so no worker's clock can extend a lease. `state` is `held` or `released`;
expired is never stored, because the store's clock decides it at every check, and an expiry equal
to the store's time is expired. The **epoch** is a base-10 string from `1` to `999999999999999999`
(18 digits, no sign, leading zero, fraction or exponent). It fits a signed 64-bit integer exactly,
so a Postgres `bigint` column can hold it, but on the wire it is never a JSON number, so no reader
rounds it through a floating-point value. Readers refuse any other spelling as `invalid`.

**The transitions.** A store kind implements the `LeaseStore` port (Go `LeaseStore`), each
transition atomic under the same serialization as its commits:

- `acquireLease({resource, owner, expectedRevision, durationMs})` compares `expectedRevision` with
  the resource's current revision (`revision-changed`), refuses while an unexpired `held` row exists
  (`held`), and otherwise writes epoch = previous epoch + 1, or `1` for a resource never leased.
  At the largest epoch it refuses `epoch-exhausted`; it never wraps.
- `renewLease(fence, durationMs)` requires the fence to be current and sets a new expiry from the
  store's clock. A renewal after expiry refuses `expired`: an expired lease is never revived, even
  when no other worker has acquired it. Only a new acquisition, with a greater epoch, follows.
- `releaseLease(fence)` sets `released` and **keeps the row and its epoch**. Deleting the row would
  let the next acquisition start again at `1`; that reset by deleting state is what the epoch exists
  to prevent, including across a restart or a restore from backup. A store kind whose rows can be
  lost declares that rather than reusing epochs.
- `readLease(resource)` and `checkFence(fence)` are reads. `checkFence` is the authority an effect
  host checks against.

**The fence.** `{version:1, resource, owner, epoch}` is the evidence a writer presents. It is
current when the row has this epoch (`stale-epoch` otherwise, including a forged greater epoch),
this owner (`other-owner`), is not released (`released`) and is unexpired by the store's clock
(`expired`); a missing row is `no-lease`, a fence for another resource is `resource-mismatch`.
These named reasons, with `held`, `revision-changed`, `epoch-exhausted`, `invalid` and the effect
host's `already-admitted`, are the complete refusal record `{version:1, reason}`.

**Every checkpoint commit.** A lease run store also implements `commitFenced(commit, fence)` and,
when it has the history capability, `commitHistoryFenced(commit, fence)` and
`commitContextSnapshotFenced(update, commitId, fence)`. The fence check and the
revision compare are one transaction. The memory kind checks the fence when the transaction is
admitted and again immediately before publication, after its own awaits, so a lease that expires
while the store waits on its disk or network is not published. `fencedRunStore(store, fence,
effectHost)` (Go `FencedRunStore`) returns the view the durable run uses as its `RunStore` (and as
the store of its stored chat scope): every claim, result and checkpoint the writer commits through
it presents the fence, so loss of the lease closes new dispatch at the next claim. The view adds no
member to any checkpoint or event: a fenced run's stored bytes have the same shape as an unfenced
run's, and with no lease configured nothing changes. The view's chat-snapshot commits present the
run's fence too, so a driver that lost its lease cannot release, through `StoredChatScope.close()`,
the turn its takeover restores. Metadata writes (feedback, capture) are not read on resume and are
not fenced. Pass the view to the run unwrapped: effect admission is bound to the view object itself,
and a host object that only delegates to it keeps the commits fenced but loses the effect host. `fencedChatStore(store, fence)` does the same for a lease
chat store's `updateFenced`. Go has no ChatStore.

**Every effect admission.** A store compare-and-set is not protection against a duplicate
external effect: a worker can pass its claim commit, lose its lease, and still call out. So the
effect host itself receives the fence. Whether a host tool's call is a read is decided by its
effect, not by how its answer is framed: a tool with a source (gateway, MCP or custom) is a read
only when its effect class is `read`, and an owner-declared `act` or `propose` effect is never a
read. Under a fenced view, every non-read host or runtime dispatch goes through `EffectHost.dispatch(admission, perform)` (Go `Dispatch`) with
`{version:1, fence, operation, idempotencyKey}`; the effect host checks the fence at its own
admission boundary, against the store's `checkFence` or an enforcement service in front of it, and
only then performs the effect. The operation is the stable idempotency identity `{run, operation,
source, tool, requestDigest}`: the run, the acknowledged claim's id, the target and the request
digest. `idempotencyKey` is the SHA-256 of its canonical JSON. It carries no epoch, so a takeover
presents the same key for the same operation; a host that admitted that key before answers
`already-admitted` and does not perform it again. The Gateway may bind both identities (gateway
#316) but must not use the fence alone as its idempotency key. `ReferenceEffectHost` (Go
`NewReferenceEffectHost`) is the reference: it serializes admission, checks the fence, records the
key, then performs. A fenced view with no effect host refuses a non-read effect as
`effect-host-unfenced` before it runs.

**Unknown outcomes and takeover.** Expiry stops new admission; it cannot undo an admitted call. A
claim whose result was never acknowledged stays `claimed` in the stored checkpoint, and a resumed
writer refuses it as `HostOutcomeUnknown` before any dispatch, whatever its epoch. Takeover never
replays it; its own host reconciles it. Fencing is not an exactly-once claim across independent
systems.

**Certifying an effect host.** `effectHostConformance(authority => host)` (Go
`CheckEffectHostConformance`) hands the factory a lease authority the kit controls. The host must
refuse, without performing, while that authority is unavailable (unavailable ownership evidence
closes dispatch); admit a current fence once; and refuse, without performing, an identity it
admitted before (also under a takeover's fence), another owner, a stale or forged epoch, a fence
for another run than the operation's, a key that is not its operation's, an expired lease and a
released one. `certifyEffectHost(create, authority)` (Go `CertifyEffectHost`) calls the factory
once, over an authority that answers from the kit until the checks pass and from `authority` for
good after, and admits that exact instance. TypeScript reads its `dispatch` once, runs the kit
through exactly that function and admits only that function, through a frozen wrapper that also
refuses once the instance's `dispatch` is not it; Go returns a `*CertifiedEffectHost` whose
identity, not its value, is the evidence. A `ReferenceEffectHost` is evidence when the package made
it: in TypeScript its `dispatch` is sealed on the instance and its authority and admitted
identities are private fields; in Go only the pointer `NewReferenceEffectHost` returned qualifies,
not a copy of it. A subclass, or a type embedding it, is not evidence. Unattended
admission accepts only these, and only when the authority they enforce against is the run's own
store: a host over a no-op authority, over another store, or certified over another store is
refused. A declared `effectFencing: 'enforced'` is never evidence. Nothing keeps an admitted host
alive: there is no registry of hosts.

**Truthful kinds and unattended admission.** `leaseLifetime` is `durable` only when rows survive
the store's process. `MemoryRunStore` and `MemoryChatStore` implement the transitions within their
process (`process`); the callback and legacy chat adapters have no lease port at all.
`admitUnattended({store, effectHost, chatStore})` (Go `AdmitUnattended`) and `unattendedRunStore`
refuse, by the record `{version:1, reason}`, a store without the port (`store-unsupported`), a
store whose leases end with its process (`store-lifetime`), and an effect host that is missing,
non-enforcing, or neither certified nor the reference host (`effect-host-unfenced`). They never
fall back to revision checks.

**Certifying a store kind.** `runStoreLeaseConformance(now => store)` (Go
`CheckRunStoreLeaseConformance`) builds a fresh store whose lease clock is the probe's, then checks
revision-checked acquisition, the first epoch, `held`, current/forged/stale/expired/released commits,
renewal from the store clock, equal-time expiry, no revival, a strictly greater epoch after expiry
and after release, and atomic competing acquisitions. Every port the writer and the effect host
rely on (`checkFence`, `renewLease`, `releaseLease`, `commitFenced` and, with history,
`commitHistoryFenced` and `commitContextSnapshotFenced`) must refuse a stale epoch (including the
same owner's earlier epoch), another owner, a released lease and a mismatched resource; the history
ports also refuse an expired fence, at equal time and after. It certifies the port; the store's
durability across its own restarts and backups is the host's to prove.

## Durable waits, attention and the report (#224, #228)

This section is for the implementer of a host store kind's waits (one wait port per run store kind,
beside its lease port) and for a host that parks, wakes and resumes runs. It implements ADR-0025
decisions 2 and 6 as their amendment fixes them. The records are `contract/schemas/run-wait.json`,
version 1, with handwritten shared vectors in `contract/vectors/wait/`. TypeScript is
`packages/harness/storage/wait.ts`; Go is `go/context/run_wait.go`. Both read the same vectors and
derive the same report from the same export.

**What the package does and what the host does.** The package reads the pending decision from the
retained record, validates every transition and records it. The host owns scheduling (when a time
or timeout wake is delivered), its clock (the store's), wake delivery, the wake key, notifications
and processes. Nothing in the package polls, schedules, sends a notification or keeps a credential.

**The wait port.** A store kind implements `WaitStore` (Go `WaitStore`) beside `LeaseRunStore`:
`waitVersion` 1 and `waitLifetime` (`durable` only when waits outlive the store's process), and these
transitions, each atomic under the same serialization as its commits and against the same run head:

- `parkWait(park, fence)`: check the fence (the lease refusals of #223), refuse while any wait of the
  run is not yet resumed (`wait-pending`; `closed` once one ended terminally) and a head that is not
  the one the park binds (`checkpoint-changed`), stamp the deadline (below; `deadline-passed` when it
  is not after the store's time), then commit the wait (stamped with the fence's owner and epoch and
  the store's time) and its one `needs_attention` event, and release the lease, in one transaction.
  The same park again returns it; another park under the same wait id is `conflict`.
- `wakeWait(evidence)`: find the wait (`no-wait`), compare the SHA-256 of the presented key with
  the wait's `wakeKeyDigest` (`unauthenticated`), then: a duplicate (same evidence id and digest)
  returns the retained disposition; the same id with other content is `conflict`; another wake
  after the winner is `stale`; an event that is not the expected `{kind, subject}`, an answer on a
  wait that is not a question, a question event without a real answer to the retained question, a
  time wake with no `wakeAt` or a timeout with no `timeoutAt` is `mismatch`; a time or timeout
  before its store time is `not-due` (equal time is due). An event or time wake at or after the
  wait's timeout is `timed-out` and any wake at or after its deadline `deadline-passed`: only the
  timeout, or the deadline wake, ends it then. Only then is the disposition and, for a question, the
  resolution (`answered` with actor and answer digest, or `unanswered` on timeout) written. A
  `deadline` wake is due at the wait's deadline and records `expired` (below).
- `resumeWait(claim)` with `claim = {resource, wait, evidence, lease, reconciled, revalidation}`:
  the acknowledged wake must be the retained one (`unacknowledged`), claimed once
  (`already-resumed`), not a timed-out approval or budget wait (`timed-out`), not past its deadline
  (`deadline-passed`), over the unchanged head (`checkpoint-changed`), with every uncertain release
  reconciled to `released` or `not-held` (`release-uncertain`) and with a revalidation of this
  wait's own pins (`invalid`). Then, in the same transaction, the store acquires the run's lease
  from `claim.lease` against the parked revision (the lease refusals of #223) and claims the
  continuation, returning `{wait, lease}`. This is the only lease a run held by a wait is granted.
- `limitRun(limit)`: keep the run's deadline (the earliest one held wins) and shorten a held lease to
  it.
- `readWait`, `exportWaits` (the run's waits in park order with their attention events and
  deliveries), `recordDelivery` (a host's delivery attempt, kept apart from the event; a repeat
  returns the first record) and `redactWait` (the question and answer content removed, every
  identity and digest kept).

A store kind's `acquireLease` refuses a run while any of its waits is not yet resumed (`wait-pending`),
for good once one of them ended terminally (`closed`: a timed-out approval or budget wait, or an
expired one), and from the run's deadline (`deadline-passed`); every lease it grants or renews
expires no later than the run's deadline, and the lease of an approval wait's claim (its epoch) no
later than the approval's expiry.

`MemoryRunStore` implements the port within its process (`waitLifetime` `process`), so `parkRun`
on it refuses `store-lifetime`; the callback kind and a store without the port refuse
`store-unsupported`. `admitWaits` (Go `AdmitWaits`) gives the same refusals up front.

**Parking.** After a run ends paused (an asked approval or a budget pause) or after a
`user_question`, the host calls `parkRun({store, fence, wait, continuation, condition, timeoutAt,
model, limits?, wakeKeyDigest, release, question?, chat?, retainUntil?})` (Go `ParkRun`) with the
fence it ran under.
The package loads the head, reads the checkpoint with the ordinary reader, and takes the pending
decision from it: an asked approval is an approval wait, any other paused checkpoint a budget
wait. A question wait names the `user_question` event: it must be the latest question in the
run's retained event log, and its question, options and recommendation are retained whole with
their digest. A question waits for an event and has no wake time. `release` is the host's
evidence about each provider resource the run held; the paused run has already ended its
invocation (ADR-0008), and the host says whether the account was released. Any `uncertain` entry
is retained, the attention event says so, and the park is not a clean one. The park writes nothing
into the run's checkpoint or event log, so with nothing configured every byte is what it was, and a
legacy (version 1 or 2) paused checkpoint parks without being rewritten.

**Waking and resuming.** The host delivers `wakeRun(store, evidence)` (Go `WakeRun`); an error from
the store leaves the outcome unknown, and the host reads the wait to learn it. `resumeParked({store,
resource, wait, evidence, owner, durationMs, approval?, grant?, reconciled?, current?, chat?})` (Go
`ResumeParked`) refuses a wake it was not shown (`unacknowledged`), a claimed wait, a timed-out
approval or budget wait, an expired one (`deadline-passed`), and a missing decision: an approval
wait needs the person's decision for the asked call, a budget wait a grant from its paused limit,
and a question takes neither (`decision-required`). It revalidates a chat binding through the
ChatStore (`chat-changed`; Go has no ChatStore and refuses a chat-bound resume as
`chat-store-unsupported`; this check is the package's, before the claim, and a later chat CAS is the
backstop), then the pinned execution authority against the host's `current` evidence (below; none
is `evidence-missing`), and has the store check the deadline and the head and claim the continuation
with its lease in one transaction (a moved run is `checkpoint-changed`). Nothing is leased or claimed
before every check passes. It returns the new lease and fence, the claimed wait, the revalidation
and the decision (or the question's resolution); the host continues the run through
`fencedRunStore(store, fence, effectHost)` with `context.approval` (Go `Input.Approval`) or the
grant, as ADR-0008 continues a pause. A wake is never the approval or the grant.

**Attention.** Each committed park records one `needs_attention` event `{version, type, id,
resource, wait, reason, needs, subject?, condition, timeoutAt?, deadline?, releaseUncertain, at}`: `needs` is
`approval-decision`, `budget-grant` or `question-answer`, `subject` the asked call or the question
event. Its `id` is the SHA-256 of the canonical JSON of `{resource, wait}`, the identity a notifier
deduplicates by. The store records it and never sends it; `recordDelivery` keeps the host's
attempts apart from it, and a failed attempt changes no wait.

**The report.** `waitReport(store, resource, now)` (Go `WaitReportOf`) reads `exportWaits` and
lists, per wait in park order, the park (owner, epoch, subject, condition, deadline, `clean` or
`uncertain` release, and a question's content), the winning wake or timeout (evidence id and
kind), a deadline that ended the wait (evidence id), a question's `answer` (actor and answer) or
`unanswered` end, and the resume (owner and epoch), each with the store's time. It reads retained waits only, so compacting or replacing the
run's checkpoint changes nothing in it, and an export written out and read back gives the same
report (`reportFromExport`, Go `ReportFromExport`). Content of a wait whose host `retainUntil` is
at or before `now` is `{state: expired, digest}`, and a redacted wait's is `{state: redacted,
digest}`. The report carries no key, fence or decision, and nothing in it resumes a run.

## Deadlines, approval expiry and resume revalidation (#225, #227, #265 M2)

This section is for the same implementers. It implements ADR-0025 decisions 3 and 5 as their
amendment fixes them. The new records are `contract/schemas/run-resume.json`, version 1, with
handwritten shared vectors in `contract/vectors/resume/`; the wait record (`run-wait.json`, not yet
released) gains the members that carry them. TypeScript is `packages/harness/storage/resume.ts`
beside `wait.ts`; Go is `go/context/run_resume.go` beside `run_wait.go`. The package validates and
records; every time is decided by the store's clock (the host's), and nothing schedules a deadline.

**A committed park closes the run.** Until a wait is resumed it holds its run: the store refuses
`acquireLease` (`wait-pending`), a second park (`wait-pending`, for a woken but unclaimed wait as for
a parked one) and, once a wait ended terminally, both for good (`closed`). The parking driver's lease
was released in the park. The resume's claim is therefore the only way back into the run: it needs
the acknowledged wake, the person's decision, the revalidation below and the release
reconciliation, and it takes the run's next lease in the same transaction that claims the
continuation. A run that is never parked is leased as before.

**Deadlines.** Deadlines are optional host policy, absent by default. `limitRun(store, {version,
resource, run})` (Go `LimitRun`) sets a run's deadline at any time; the store keeps the earliest it
is given, shortens a held lease to it, and from then on grants and renews no lease of the run past
it. Because the durable writer commits each dispatch's claim through the fenced store before it
dispatches (both languages), and the effect host admits every non-read effect under the fence, no
model, agent, Runtime or tool dispatch of a run driven through `fencedRunStore` is made at or after
its deadline (equal time is past); a lease acquired at or after it is refused `deadline-passed`. An effect already admitted stays unknown until its own
host reconciles it; the deadline undoes nothing and never re-runs it. A park's `limits` (`run`,
`waitMs`, `approvalTtlMs`; durations from 1 ms to 30 days) stamp its `deadline` from the store's
time: the earliest of the run's deadline (this park's or the earlier one the store holds, so a new
wait never extends a run limit), the park time plus `waitMs`, and for an approval wait the park time
plus `approvalTtlMs`; `by` names it, the run's first on a tie. From the deadline no wake, answer or
claim of the wait is accepted (`deadline-passed`); the host delivers a `deadline` wake, due at the
deadline, which records `expired` and ends the wait, and its run, for good. A question its deadline
ends before an answer is `unanswered`. A deadline is a terminal outcome; a question's timeout stays
a resumable end, as before.

**Approval expiry.** An approval wait is unattended approval: `parkRun` refuses one without
`limits.approvalTtlMs` (`approval-ttl-required`) and records its binding, read from the checkpoint:
the call id, the SHA-256 of the canonical JSON of `{name, tool, args, runtime?}` the person reviews,
and its source, whose identity and descriptor are pinned with the Plan and policy identities. The
approval expires at the park time plus the TTL. A decision delivered at or after that is refused, a
claim at or after it is refused, and the lease the claim takes expires no later than it: the approved
call's first commit and its effect admission are refused at the expiry, and renewing that lease never
renews the approval. A changed descriptor of its source or a changed policy identity refuses the
approval at the revalidation (`approval-changed`), even admitted; a changed source identity or Plan
lineage refuses any resume first. Re-asking needs a newly retained request and a new decision.

**Pins and revalidation.** Every park pins the execution authority its continuation will run under:
the model the trusted host admitted the run with (`model`, which the host supplies), the identity of
the resolved policy (the SHA-256 of the canonical JSON of the checkpoint's policy members, or of `{}`),
the Plan lineage (revision and accepted revision), each source's identity digest and the asked
approval's source with its descriptor digest, and the last observed digest of each artifact or
dialect the run read or produced. `resumeParked` requires the host's `current` evidence
(`run-resume.json#/$defs/evidence`) and revalidates it before anything is claimed, in this order:
a pinned item with no current evidence is `evidence-missing`; a changed source identity is
`identity-changed` and a changed Plan lineage `lineage-changed` (each needs a new admitted run,
ADR-0019); a retired model is `model-retired`, whatever an admission says (the person decides; nothing
is substituted); a changed model or reference (descriptor or artifact) needs an explicit admission of
exactly that change, from the pin to the current value, with its actor and time (`model-changed`,
`reference-changed`, or `admission-invalid` for an admission of another change); a changed policy
identity needs the trusted host's new layer (`policy-changed`), whose identities are both kept;
an approval whose source's descriptor or policy changed cannot run (`approval-changed`); and an
admission that matches no change is `admission-invalid`. The claim records the revalidation (the pins
and evidence digests and each change with its old and new value) in the resumed wait. A refusal
claims nothing and the wait keeps holding the run. The host resolves a new policy's full layer order
(`resolvePolicy`); the package never rewrites the checkpoint, so consumed allowance stays what it was.
Pins other than the approval's source carry no descriptor: each later call still passes the existing
per-call source pins (ADR-0006, ADR-0027), and in-run model failover keeps its own rules (ADR-0020).
