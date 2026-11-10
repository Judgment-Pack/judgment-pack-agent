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

The fingerprint digests the request as `ParseJSON` reads it. It is persisted and re-derived when a
stored book is read, so its form does not change, and it says two requests read alike, not that they
are one write. The decision is made from the text the row records (#275,
`contract/vectors/storage/durable-repeats.json`). The same text is the same write, and so are two
requests their parsed form says exactly (`exactArguments`), which differ only in whitespace, a
string's escapes or the order of names that do not fold together: the record answers them and they do
not run. A request that is exact, beside a recorded text that is not, is refused with
`DurableRequestConflict`: it is what that text reads as, so it may be a copy from the model's history, and
running it would be a second write on another id. A legitimate neighbour written after the text pays for that
with a named refusal in that run. Other exact numbers that differ make a different write, which runs and gets
its own row with the same fingerprint, so a legitimate integer past 2^53 runs beside a neighbour written first.
The same values written
differently (`1.0` and `1`, `-0` and `0`, a repeated name, two names `encoding/json` folds together, in
both orders) cannot be told apart: they are refused with `DurableRequestConflict`, neither run nor
answered. A row a published release wrote is judged by the same rule, from the request it recorded, and
needs no migration. A call whose text the record cannot hold (a repeated name, a number out of range, a
lone surrogate) is refused with `DurableRequestUnrecordable` before anything is claimed, unless a row a
published release wrote answers it first; retained history refuses that text, which used to reach the
writer after the claim and end the run with a storage failure. TypeScript runs a host write with the
canonical JSON of what `JSON.parse` read, so what runs is what is keyed; there, under a run store, a host
write holding an integer past 2^53 or a number read as an infinity is refused the same way, because
the write it would run is not the one the model wrote.

Every Go wire reader hands on the model's argument text: the parsed value of an Anthropic or Gemini call
keeps the source text it was read from, so the bytes that run and that a row records are the model's on
every wire. An API family writes a call back to the model from its parsed form, except where that form cannot
say the text (`wire.Value`'s `lossy`): there it writes the text, so a model that copies its history copies the
bytes that ran, on the next request and on a resumed run's first request, in a run this release started. What the
record cannot hold goes back in its parsed form, as published: a call holding it is refused, so no copy reaches a row. A call held for approval keeps its text where it must. `approval.args` is the parsed form, so
where it cannot say the model's text (`approval.request`, checkpoint 5 or 6) the approved call runs that text once it
reads back to `args`, and it goes through the same decision over the record as any other call. Whitespace and
member order are not kept: `args` hold the canonical form at every depth, which the person is shown and the
approved call runs. A host that renders or binds a stored approval reads `approvalShownArguments` and
`approvalShownDigest` (`ApprovalShownArguments` and `ApprovalShownDigest` in Go): the text as a string, since a
browser that parses and writes it again shows another number, and the digest of its bytes. Approving a call that
keeps its text needs that digest as the decision's `shownDigest`, so a host that has not moved is refused by name and
nothing runs; a call with no text kept runs the canonical form that is shown and digested, and needs none. The checkpoint's
format says what it holds (ADR-0024, ADR-0029): `principal` on 4 and 6, `approval.request` on 5 and 6, and a
reader refuses any other combination. A record with no
`request` runs from `args` only where the parsed form cannot have changed what the model wrote; otherwise
it is refused as stale, because a checkpoint a published release wrote holds nothing else. Text the record
cannot hold is refused before anyone is asked.

Checkpoint version 2 carries a bounded durable journal, trusted acknowledged operation and
artifact observations, children and the four delegation handoff states. Prepared request bodies
and exact continuation frames are retained without transport headers or private reasoning.
Redaction, incompatible admitted identities, bounds, or absent exact bytes cause
`ContinuationNotRecoverable` or `ContinuationIdentityChanged` before dispatch. A host or runtime
call's result is recorded in the form the record guard (ADR-0018) leaves of it, and the call is
answered with that same form, an external read's checked result included, so the record and what
the model is given agree and the call settles (#343). A job's start or cancel is answered from its
job record and keeps the earlier rule. A durable handoff's parent is given the continuation its
record holds. A guard refusal, or a result whose
commit was not acknowledged, surfaces `StorageOutcomeUnknown`; it never claims the effect did not
happen. Two limits remain. A request's text is guarded as text, so a result that matches only once
JSON escapes are added (`C:\Users"x` written inside a request) makes the next model or agent request
unrecordable: the run stops with `ContinuationNotRecoverable`, and nothing runs again. An immutable
external read served from its cache is checked again against its receipt over the recorded, redacted
text, so a cached read whose result held a match is withheld on a cache hit. The redactions made
before a result is stored are not counted in `records.redactions`, and no `output_guarded` event is
made of them. Checkpoints
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

Snapshots are written at version 2, which has version 1's shape and says its cache rows were keyed
only from arguments their JSON text says exactly (#271). A version 1 row is a key and an entry and
may hold the answer to other arguments than its key names, so no reader can tell it from a sound one.
A version 1 snapshot therefore still loads, and a read returns it as written. Restored, it keeps its
entries, ages and index and has no cache rows, and the next snapshot is version 2. A read its rows
would have answered runs once more: a cold cache costs one call and never a wrong answer. By ADR-0024
a new meaning takes the next enclosing version rather than a member a closed reader would refuse as
malformed: the readers of v0.11.0 and v0.12.0 refuse version 2 by its version
(`UnsupportedContextSnapshotVersion`) before they read the body
(`contract/vectors/chat/context-snapshot-v1-cache.json`).

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
These named reasons, with `held`, `revision-changed`, `epoch-exhausted`, `invalid`, the effect
host's `already-admitted` and `principal-changed`, are the complete refusal record `{version:1, reason}`.

**A delegated run's lease (ADR-0029, #260).** A lease and its fence are version 2 when they hold a
delegated run's principals: `principal`, `{sub, rootActor, host}`. `acquireLease` takes `principal`;
a store kind compares it with the row's, before the epoch moves, at every acquisition, released and
expired rows included, and refuses any difference, a version 1 row or a request with none as
`principal-changed`. `checkFence` refuses a fence whose `principal` differs from the row's. The store
kind keeps the member in its row and returns it unchanged, and declares that with the property
`readonly leasePrincipals = true` (Go, the method `LeasePrincipals() bool`): `fencedRunStore` refuses a
version 2 fence on a store that does not, and the conformance kit's `lease.principal.*` rows prove it,
over `commitFenced`, `commitHistoryFenced` and `commitContextSnapshotFenced`. The kit fails a store whose
member is anything but `true`, such as a method. A fence's `principal` must also be the delegated
run's own: the session refuses any other, either way, before it commits anything. The effect
idempotency key does not change: it names neither the principals nor the epoch.

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
refuse, without performing, every time that authority is unavailable (unavailable ownership evidence
closes dispatch); admit a current fence once, also once the authority is back; and refuse, without
performing, an identity it admitted before (also under a takeover's fence), another owner, a stale
or forged epoch, a fence for another run than the operation's, a key that is not its operation's, an
expired lease and a released one. The kit fails the authority at three points, before the first
dispatch, right after a success, and between dispatches right after refusals, and at each of them
ten times in a row: five retries of one operation and five new operations. A host must never degrade
to performing, however long the authority is down: no retry budget, circuit breaker or last good
answer opens dispatch. Those counts cannot prove that, and a host that detects the kit or changes
later is beyond any behavioral kit.

`certifyEffectHost(create, authority)` (Go `CertifyEffectHost`) calls the factory once, over an
authority that answers from the kit until the checks pass and from `authority` for good after, and
admits that exact instance. TypeScript reads its `dispatch` once, runs the kit
through exactly that function and admits only that function, through a frozen wrapper that also
refuses once the instance's `dispatch` is not it; Go returns a `*CertifiedEffectHost` whose
identity, not its value, is the evidence. A `ReferenceEffectHost` is evidence when the package made
it: in TypeScript its `dispatch` is sealed on the instance and its authority and admitted
identities are private fields; in Go only the pointer `NewReferenceEffectHost` returned qualifies,
not a copy of it. In TypeScript the class is final: constructing a subclass refuses
`effect-host-unfenced`, because its `dispatch` and `effectFencing` are sealed on the instance and an
override would never run. A host with behavior of its own delegates to a reference host and is
certified. In Go a type embedding it is not evidence. Unattended admission accepts only these, and
only when the authority they enforce against is the run's own
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
transitions, each atomic under the same serialization as its commits and against the same run head.
Everything they decide by survives the store's own restarts: the waits, their attention events and
deliveries, each run's earliest deadline, and an approved call not yet settled with its expiry and
binding (below). The package re-checks none of this in a host store; the kit below certifies it.

- `parkWait(park, fence)`: check the fence (the lease refusals of #223), refuse while any wait of the
  run is not yet resumed (`wait-pending`; `closed` once one ended terminally) and a head that is not
  the one the park binds (`checkpoint-changed`), stamp the deadline (below; `deadline-passed` when it
  is not after the store's time), refuse a `wakeKeyDigest` any other wait of the store holds
  (`key-reused`, #265 M1), then commit the wait (stamped with the fence's owner and epoch and the
  store's time) and its one `needs_attention` event, and release the lease, in one transaction. The
  same park under the same fence again returns it; another park under the same wait id, or the same
  park under another owner or epoch, is `conflict`. A refused park changes nothing and leaves the lease
  held.
- `wakeWait(evidence)`: find the wait the evidence names, and only that one (`no-wait`), compare the
  SHA-256 of the presented key with the wait's `wakeKeyDigest` (`unauthenticated`), then: a duplicate (same evidence id and digest)
  returns the retained disposition; the same id with other content is `conflict`; another wake
  after the winner is `stale`; an event that is not the expected `{kind, subject}`, an answer on a
  wait that is not a question, a question event without a real answer to the retained question, a
  time wake with no `wakeAt` or a timeout with no `timeoutAt` is `mismatch`; a time or timeout
  before its store time is `not-due` (equal time is due). An event or time wake at or after the
  wait's timeout is `timed-out` and any wake at or after its deadline, or the run's, `deadline-passed`:
  only the timeout, or the deadline wake, ends it then. A resumed wait is never woken again. Only then is the disposition and, for a question, the
  resolution (`answered` with actor and answer digest, or `unanswered` on timeout) written. A
  `deadline` wake is due at the wait's deadline and records `expired` (below); on a resumed approval
  wait whose approved call has not settled it is due at the approval's expiry (#266 N1, below).
- `resumeWait(claim)` with `claim = {resource, wait, evidence, lease, reconciled, revalidation}`:
  the acknowledged wake must be the retained one (`unacknowledged`), claimed once
  (`already-resumed`), not a timed-out approval or budget wait (`timed-out`), not past its deadline
  (`deadline-passed`), over the unchanged head (`checkpoint-changed`), with every uncertain release
  reconciled to `released` or `not-held` (`release-uncertain`) and with a revalidation of this
  wait's own pins (`invalid`). Then, in the same transaction, the store acquires the run's lease
  from `claim.lease` against the parked revision (the lease refusals of #223) and claims the
  continuation, returning `{wait, lease}`. This is the only lease a run held by a wait is granted. The
  lease ends no later than the run's deadline and any unsettled approval's expiry (this wait's, or an
  earlier one's, #266 N2), and a ceiling not after the store's time refuses the claim
  (`deadline-passed`).
- `limitRun(limit)`: keep the run's deadline (the earliest one held wins), shorten a held lease to
  it, and stamp every wait of the run not yet resumed or ended again with it (#266 M1): its deadline
  becomes the earlier of what it was and the run's, so no wake, answer, decision or claim of it is
  accepted from then and its deadline wake is due then. A run deadline set before a wait's park is
  stamped as a millisecond after the park, the earliest a committed park can carry; the store's own
  deadline still refuses from the time it names. The park's attention event keeps the deadline it
  announced.
- `readWait`, `exportWaits` (the run's waits in park order with their attention events and
  deliveries), `recordDelivery` (a host's delivery attempt, kept apart from the event; a repeat
  returns the first record) and `redactWait` (the question and answer content removed, every
  identity and digest kept).

A store kind's `acquireLease` refuses a run while any of its waits is not yet resumed (`wait-pending`),
for good once one of them ended terminally (`closed`: a timed-out approval or budget wait, or an
expired one), and from the run's deadline (`deadline-passed`), and so does its unfenced `commit`
(Go `Commit`, #266 M4); every lease it grants or renews
expires no later than the run's deadline and, from an approval wait's claim until its approved call
settles, no later than the approval's expiry. While that call is unsettled, every commit of the run
carrying a checkpoint, fenced or not, must carry exactly the reviewed call (its call id, the digest of
its name, tool, arguments and Runtime binding, and its source), or it is refused `approval-changed`;
the commit whose checkpoint records the call `executed`, `refused` or `answered` settles it in the same
transaction. These are store obligations: the commit port's checkpoint bytes are the writer's, but a
store with the wait port reads every committed checkpoint's approval itself (#266 N3), and the package
has no backstop for a store that does not.

**The wait conformance kit (#265 M6).** `runStoreWaitConformance(create)` (`storage/waitConformance`;
Go `CheckRunStoreWaitConformance`) certifies a host store kind's wait port as #223's kit certifies its
lease port. The factory receives the store clock the kit moves and, to restart, the store it built: it
returns a new instance over the same durable state. The kit drives the port directly and throws
`StorageConformanceFailure` naming the first property broken: the key check (`wait.key.*`: a reused
digest, another wait's key), the single winner (`wait.wake.*`: an event and a time wake at once), the
lease release (`wait.park.*`: a refused park changes nothing and keeps the lease, a committed one
releases it, a repeat under another fence is `conflict`), claim-once (`wait.claim.*`: two claims at
once), the run closure (`wait.closure.*`: the lease and the unfenced commit, `wait-pending` then
`closed`), the deadline and approval ceilings (`wait.deadline.*`, `wait.approval.*`,
`wait.unsettled.*`), the commit-time approval binding and its settlement (`wait.approval.binding*`,
`wait.approval.settled`), and each of them after a restart (`wait.restart.*`). The memory kind passes
it with a restart over a copy of its state, and the tests show a kind that leaves one guarantee out
failing the property that names it.

`MemoryRunStore` implements the port within its process (`waitLifetime` `process`), so `parkRun`
on it refuses `store-lifetime`; the callback kind and a store without the port refuse
`store-unsupported`. `admitWaits` (Go `AdmitWaits`) gives the same refusals up front.

**Parking.** After a run ends paused (an asked approval or a budget pause) or after a
`user_question`, the host calls `parkRun({store, fence, wait, continuation, condition, timeoutAt,
model, limits?, wakeKeyDigest, release, question?, chat?, retainUntil?})` (Go `ParkRun`) with the
fence it ran under. `wakeKeyDigest` is `wakeKeyDigest(key)` of a fresh key the host mints for this wait
alone from a cryptographic random source (32 random bytes in base64url, for example): a key is 32 to
512 printable ASCII characters with at least 16 distinct ones, which refuses a short or repetitive key
but cannot tell a guessable one, and the store refuses a digest another wait holds (#265 M1).
The package loads the head, reads the checkpoint with the ordinary reader, and takes the pending
decision from it: an asked approval is an approval wait, any other paused checkpoint in model requests
a budget wait. A pause no package input can resume is refused `not-resumable` and never parked, so it
never closes its run (#265 M3, #266 M5): a pending plan decision (plan drift), which no grant answers,
and a provider-managed allowance, which takes no grant (`extendExecution`); the readers refuse a budget
wait in any unit but model requests. A question wait names the `user_question` event: it must be the latest question in the
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
and a question takes neither (`decision-required`); a grant's `at` is a store-clock time (UTC,
millisecond precision), in both languages (#265 M4). It reads the store's wait through the reader and
refuses one that is not the wait asked for (`invalid`). It revalidates a chat binding through the
ChatStore (`chat-changed`; Go has no ChatStore and refuses a chat-bound resume as
`chat-store-unsupported`). That check is the package's, made before the claim and outside its
transaction: the store's claim does not revalidate the chat, so a chat moved after the check is not
caught at the claim, and a later compare-and-set on the chat is the only backstop (#264 M7(a)). Then the pinned execution authority against the host's `current` evidence (below; none
is `evidence-missing`), and has the store check the deadline and the head and claim the continuation
with its lease in one transaction (a moved run is `checkpoint-changed`). Nothing is leased or claimed
before every check passes. The claim's result is read back through the readers before its fence is
handed out (#266 M6): it must be this wait, resumed by the lease returned, with the revalidation the
package gave, and that lease must be the claimer's, held, for this run, and end no later than the run's
deadline and an approval's expiry the wait records; anything else is `invalid`. It returns the new lease and fence, the claimed wait, the revalidation
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
report (`reportFromExport`, Go `ReportFromExport`). In a report built with the host's `now`, content
of a wait whose host `retainUntil` is at or before it is `{state: expired, digest}`; a report built
without `now` (Go: the zero time) expires nothing. A redacted wait's content is `{state: redacted,
digest}`. `retainUntil` hides nothing else: `exportWaits` and `readWait` return the question and answer
in full until the host redacts the wait (#264 M7(b)). The report carries no key, fence or decision, and
nothing in it resumes a run. An approval wait ended after its claim (#266 N1) reports its resume and
then its deadline.

## Deadlines, approval expiry and resume revalidation (#225, #227, #265 M2)

This section is for the same implementers. It implements ADR-0025 decisions 3 and 5 as their
amendment fixes them. The new records are `contract/schemas/run-resume.json`, version 1, with
handwritten shared vectors in `contract/vectors/resume/`; the wait record (`run-wait.json`, not yet
released) gains the members that carry them. TypeScript is `packages/harness/storage/resume.ts`
beside `wait.ts`; Go is `go/context/run_resume.go` beside `run_wait.go`. The package validates and
records; every time is decided by the store's clock (the host's), and nothing schedules a deadline.

**A committed park closes the run.** Until a wait is resumed it holds its run: the store refuses
`acquireLease` (`wait-pending`), an unfenced commit (`wait-pending`, #266 M4: the attended engine over
the raw store continues nothing), a second park (`wait-pending`, for a woken but unclaimed wait as for
a parked one) and, once a wait ended terminally, all of them for good (`closed`). The parking driver's
lease was released in the park. Through the store's ports, the resume's claim is therefore the only
way back into the run; a host that writes the run's rows outside them is outside this contract. It needs
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
deadline, which records `expired` and ends the wait, and its run, for good. A run deadline set after
the park reaches the waits already parked (`limitRun` stamps them again, #266 M1), and the store's own
run deadline refuses a wake or a claim of any wait from the time it names. A question its deadline
ends before an answer is `unanswered`. A deadline is a terminal outcome; a question's timeout stays
a resumable end, as before.

**Approval expiry.** An approval wait is unattended approval: `parkRun` refuses one without
`limits.approvalTtlMs` (`approval-ttl-required`) and records its binding, read from the checkpoint:
the call id, the SHA-256 of the canonical JSON of `{name, tool, args, runtime?}` the person reviews,
and its source, whose identity and descriptor are pinned with the Plan and policy identities. The
approval expires at the park time plus the TTL. A decision delivered at or after that is refused, and
so is a claim. From the claim until the approved call settles, the expiry bounds the run, not one
lease: the claim's lease, a takeover's, a lease the claimer re-acquires and every renewal end at it,
and no lease of the run is granted at or after it (`deadline-passed`). So every commit, every effect
admission and every dispatch claim of the approved call is refused at the expiry, whoever holds the
run, and renewing or re-acquiring a lease never renews the approval. Every checkpoint the run commits
meanwhile must carry exactly the reviewed call, so a rewritten call, arguments or source is refused
(`approval-changed`) before anything can dispatch it. The commit that records the call `executed`,
`refused` or `answered` settles it, and the run is then leased as before. An approved call that has
not settled by the expiry (never dispatched, or performed with its result commit landing at or after
the expiry) can no longer be committed: no lease is granted, and a park needs one. Its terminal path is
the host's `deadline` wake on the resumed wait, due at the approval's expiry (#266 N1): it records
`expired` on the resumed wait, which reads as the call's outcome unknown, lifts the approval and closes
the run for good (`closed`). It grants no dispatch authority; the host reconciles the effect, if any,
through its effect host, whose admission record says whether it was admitted (`already-admitted` holds
across every lease). A changed descriptor of its source or a changed policy identity refuses the
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

## Run-owned jobs (#229)

This section is for the implementer of a host store kind's job port (beside its lease and wait ports) and for a host that
starts, completes and reconciles background jobs. It implements the job core of ADR-0025 decision 7, as its amendment fixes it.
The records are `contract/schemas/run-job.json`, version 1, with handwritten shared vectors in `contract/vectors/job/`.
TypeScript is `packages/harness/storage/job.ts`; Go is `go/context/run_job.go`. Nothing here adds a member to a checkpoint,
event, lease or report, and with no job tool offered the store is not asked about jobs at all.

**What the package does and what the host does.** The package registers, reads back and records. The host owns the external
job and its process, the completion key, scheduling, the clock (the store's), wake delivery, credentials and notifications.
Nothing here polls, schedules, retries or synthesizes a completion. Registration proves nothing: a job is `unproven` until a
typed answer, a host's reconciliation or a completion says otherwise.

**Offering job tools.** `jobStartTool({name, description, inputSchema, source, approve?, approval?, mint, start,
gatewayKeyScope?})` (Go `JobStartTool`) is an ordinary sourced `act` tool, under the ordinary approval, dispatch, idempotency
identity and fence. A decision approving a held start must carry `shownDigest`, a text kept or not (`ApprovalNotShown` before the
claim otherwise). `mint(job)` returns `jobKeyDigest(key)` of a fresh completion key for that job: a key has a wake key's
form, the host keeps it across its own restarts, and the store holds only its digest. `start({job, request, signal})` runs the
row's own request bytes under the effect host's admission and answers `{started: true, handle, receipt?}` or `{started:
false}`. `jobReadTool(view)` (Go `JobReadTool`) is the model's read, bound to the view's run. A run offered either needs a fenced
view of a store whose jobs, leases and waits outlive its process, with an effect host proven for that store (`admitJobs`, Go
`AdmitJobs`, which the session runs over the view's own store and host), or it refuses by name before any request. A tool of a
`gateway` source needs `gatewayKeyScope` (`job-host-unsupported`). A sub-agent is offered no job tool: a declaration naming one is
withheld, and a child session offered one refuses `child-unsupported`. A job with no row in the run's head means its rows were
restored from an earlier moment than its jobs, and the run refuses `store-inconsistent` before any request. A run under a stored
chat refuses `chat-store-unsupported` before any request too: its job wait could not resume there.

**The job port.** A store kind implements `JobStore` (Go `JobStore`): `jobVersion` 1, `jobLifetime`, and these, each atomic under
the same serialization as its commits, with everything they decide by surviving the store's own restarts.

- A registration travels as `RunCommit.job` through `commitFenced` or `commitHistoryFenced` and commits in that transaction or
  not at all. The fence is the commit's. Its identity must be the SHA-256 of `{resource, operation}` (`invalid`), a key digest any
  job or wait of the store holds refuses `key-reused`, a run's sixty-fifth job `jobs-exhausted`, and an unfenced commit carrying
  one `invalid`. A repeated commit id must carry the same registration.
- `answerJob(answer, fence)`: the typed answer, from the fence that registered or re-claimed the start, current or not
  (`unauthenticated` otherwise). The first start wins: another handle is `conflict`. A negative after a proof is `conflict`.
- `reconcileJob(evidence)`: the completion key (`unauthenticated`) and no lease. `unknown` changes nothing. `not-started` must mean
  the authority that would run it has closed it. The same evidence id with other content is `conflict`. A `started` finding's
  `handleDigested` is kept, as an answer's is.
- `completeJob(evidence)`: the completion key and no lease. In one transaction it settles the job and wakes a `parked` wait on it,
  unless that wait's timeout or deadline, or the run's, has come: then only those end it. A duplicate returns the job and wakes
  nothing; other evidence on a completed job is `stale`.
- A start or a completion on a closed job is kept as a contradiction, at most eight, reported, and it moves nothing.
- `reclaimJob(resource, job, fence)`: a re-authorization grant's re-send, under a current fence, only from `unproven`
  (`job-settled` otherwise, and the writer leaves the row and the grant as they were).
- `parkWait` of a job wait refuses a settled job (`job-settled`) and an unproven one (`job-unproven`) in the park's own
  transaction, and refuses a key digest a job holds (`key-reused`).
- `readJob(resource, job)` and `listJobs(resource, after, limit)` look up by run: another run's job does not exist here. `exportJobs`
  is the report's source.

**The job conformance kit.** `runStoreJobConformance(create)` (`storage/jobConformance`; Go `CheckRunStoreJobConformance`)
certifies the port, as the wait kit does its own: the factory receives the kit's clock and, to restart, the store it built. It
names the first property broken: `job.register.*` (the commit and the job together, the fence, an unfenced commit, the identity,
the history path), `job.key.*`, `job.wait.key`, `job.owner.*`, `job.answer.*`, `job.complete.*`, `job.reconcile.*`,
`job.contradiction.*`, `job.reclaim.*`, `job.park.*`, `job.wake.*`, `job.timeout.*`, `job.limit`, `job.list.page`,
`job.cancel.*` (a cancel's answer is the record asked of, with the handle, marker, source and tool it held, and the run's other
job untouched; a fence of another run asks nothing; a word keeps its receipt), `job.abandon.*` (each started job asked under its
stable operation, a tool's first request standing, a second abandon at a newer epoch, the lease ending by the run's deadline, a
parked run abandoned with its wait standing, no lease on a closed run or one never seen, a refused abandon moving nothing) and
`job.restart.*`, whose commit ids are presented at the current revision, so a store that forgot one would commit it. The tests
show a kind that leaves one guarantee out failing the property that names it.

**Waiting.** After the model ends its turn, or at any checkpoint at rest (`running` or `complete`), the host calls `parkRun({...,
job, condition: {event: {kind: 'job-completion', subject: job}}})`. A pending decision is `decision-required`, a stopped checkpoint
`nothing-pending`, a run at its request limit `not-resumable`, and a chat binding, or a run whose history head holds a stored chat,
`chat-store-unsupported` (Go has no chat binding). `wakeRun` refuses an event for a job wait
(`mismatch`); it takes a timeout, which does not close the run, and a deadline. `resumeParked` needs the completion's or the
timeout's evidence id and no decision, and returns `job`, read through the reader; the host passes it as `context.jobContinuation`
(Go `Input.JobContinuation`), and the first request carries one fixed line and one data block built from the record. The host opens
the continuation through the claim's fenced view (`fencedRunStore(store, resumed.fence, effectHost)`, Go `FencedRunStore`). At a
`complete` head that is required (#355): the continuation is admitted only through a view of this run whose fence is the owner and
epoch the claim recorded, so each of its commits is fenced by the claim's lease and a later lease of the run stops it; through any
other store the head is refused `RunComplete`.

**What the model reads.** A start's row holds what the package composed (`jobStartResult`): the job's identity, its state and the
words "started, not finished". `job_read` answers the state every time, with `changed` and, when nothing moved, a fixed instruction
to end the turn. A handle, a receipt and host text never reach the model. Once a start has run, nothing reads as refused input:
what it answered that could not be recorded (not typed, or conflicting with what the job holds), and whatever it threw, leaves the
row `unknown` with one fixed failure, `JobStartUnrecorded`, and the model is answered from the job as a repeat is. That holds too
where the effect host's dispatch refuses after the start ran: its answer goes to the job, and the row holds that failure. A handle or
receipt the record cannot hold (over 256 bytes, or with a control character) is kept as the SHA-256 digest of its bytes, and the job
is `started`: it can complete, and the same bytes answer the same, but the host holds the means to reach it. `handleDigested: true`
on the record says which it is, because a real handle can read the same, and a digest is never sent as the external reference. A
reconciliation's found handle and receipt are passed as the external system gave them, and the package keeps and marks them the same
way. Rows of job tools record no observation, so they are never
plan evidence; a start cited in `complete_plan_step` is held to its job, read afresh: `completed` with `succeeded` proves it whatever
the row says, and anything else refuses `unsuccessful-operation`.

**Cancelling.** `jobCancelTool({name, description, source, start, approve, approval, cancel})` (Go `JobCancelTool`) declares a start's
cancel on the start's source, with both in that source's tool list (`JOB_CANCEL_SCHEMA`, Go `JobCancelSchema`). Its arguments are exactly
`{job, generation}`. Its record is read before anyone is asked, before any claim and again before a held decision runs: `no-job` for
another source's or tool's job, an absent one or another generation, `job-settled`, `job-contradicted` (closed, with evidence since
that it ran), `job-unproven`. `jobCancelCard(job)` (Go `JobCancelCardOf`) is what a card shows: source, tool, state, generation and
`job`. `requestJobCancel` records `requested` under the current fence before the effect host is asked: a stale or expired fence asks
nothing, unlike `answerJob`. The writer reads the record before it asks, and an answer whose handle, marker, source or tool differ
from it is refused `invalid` with nothing sent. `cancel` is handed `{job, generation, source, tool, handle, operation}`, or
`handleDigest` and no `handle` where `handleDigested` marks it; a host answering an oversized handle keeps its own mapping. `operation`
is the identity the effect host admits the cancel under (its row's), the same on a retry, so a host can make `cancel` idempotent across
its own processes. `acknowledgeJobCancel` takes the host's word
by the completion key, with no lease: `accepted` or `refused`, first write wins, `no-cancel` with no request. Neither moves `state` or wakes a wait;
only a completion with `cancelled` says the job stopped. `job_read` shows `cancel`. The answer is `jobCancelResult`, from the job. The row
holds what the package composed, one of the fixed failures `JobCancelUnrecorded` and `JobCancelNotSent`, or, where the effect host or a
delegation's read refused before the cancel was handed over, that refusal; none of those words reach the model. Where the cancel was never
handed to the host, the answer says it was not sent, whatever the record asked, and once its job has settled it says so. An effect host
that refuses after the cancel was handed over leaves `JobCancelUnrecorded`: it may have been sent. A repeat while the job runs is answered
from the job and sends nothing, and its note says the request may not have been delivered; once the job has settled it is `job-settled`. A
tool's cancel recorded on a job whose row the head does not hold refuses the run `store-inconsistent` before any request: it would be
asked, and sent, again.

**Abandoning.** `abandonJobs({store, effectHost, resource, command, durationMs, cancel})` (Go `AbandonJobs`) is a host's command, standing for the
person's approval: no source policy is asked. `JobStore.abandonJobs` takes the lease under `job-abandon` at a new epoch in one transaction with a
cancel asked of each `started` job, and old workers get `stale-epoch`. What the store refuses (F-9) it refuses before it moves anything, and on a
run the store has never seen (no commit, lease, wait, job or goal record) it takes no lease and answers no jobs; a worker's lease before its
first commit is a run seen, and so is a goal's run no worker has leased yet. The package reads each job back after the transaction and
decides and builds its cancel from that record, as a tool's cancel is (#325): a job the record does not bear out (none held, another
job's, or an answer whose source, tool, or handle and marker are not the record's) is answered `invalid` and sent nothing, and the others
still go. A start that answered after the transaction is asked its cancel then, under the abandon's fence, and sent. The package refuses
the abandon whole where it cannot read the store's answer (a lease that is not the abandon's, or a malformed job) and where `readJob`
fails with an error that is not a `JobRefused`; the lease is then taken. Jobs are decided and sent one at a time, so a refusal at a later
job (a malformed job or a read error) comes after the earlier jobs' cancels were sent, and
the host treats the refusal as "some cancels may have gone". A retry of the same command answers those `already-admitted` only while the
effect host remembers their operations, and is refused again while the store keeps answering so. Each cancel goes through the effect host under `jobCancelOperation(command, job)`,
admitted under the tool `job-abandon` and the job's source, with no run commit, so a retry of the command is admitted once by the same effect host;
`cancel` is handed that operation. Past a deadline or on a closed run there is no lease: a cancel the transaction asks is `undelivered`,
nothing is sent, and the answer says so; a start that answered after the transaction is answered `undelivered` with no request on its
record. After `invalid` the host treats the store as faulty for that job, does not retry it until its record and its answer agree, and
stops the job, if it must, at the external system and reports that through `completeJob`; after `undelivered` it takes the same route. It answers each job's delivery: `sent`, `already-admitted`, `failed`, `undelivered`, `no-handle`, `settled`,
`contradicted` (closed, with evidence since that it ran), `acknowledged`, `invalid` (the record does not bear the answer out; nothing sent). None of it proves
anything stopped.

**What a retry holds, and what an abandon leaves.** The stable operation keeps a retry of the command to one admission only as long as
the effect host remembers it: the reference host's memory is its process, so after a restart a retry can send again, and a host's `cancel`
must be idempotent by `operation`. `already-admitted` means the effect host has admitted that operation before, not that anything was
delivered: after `failed` it means nothing more was sent, and a failed delivery needs a new command. An abandon
hands its one `cancel` every source's handle, so the host routes each by `tool` and `source`. And an abandon closes nothing: when its lease
ends or is released, a parked wait can be claimed and the run can go on, a new start included; a parked held start can still run once it is
approved; and a start that answers after the abandon read its record can become `started` with no cancel asked, which only a retry of
the abandon sends.

**A gateway.** `gatewayActionMembers(job, {reconcile})` (Go `GatewayActionMembers`) is what the host adds to `/act`: it takes the job
(`readJob`), which a `start` can read and a reconciliation holding no lease can too; a job its reader refuses gives no members. It
carries no owner, subject or lease. Gateway v0.24.0 is the first release that accepts these members; an older one refuses them with
HTTP 400, `request body carries a member this endpoint does not read`, before it reserves or sends anything, and only that refusal
answers a start `{started: false}`. A cancel on a gateway source presents its own action key, never the start's. `gatewayRefusalFinding(refusal)` reads
a reconciling request's refusal: only `action-key-not-dispatched` is `not-started`. What the gateway builds is in ADR-0025's amendment (gateway #352, in v0.24.0).

## One durable goal (#230)

This section is for the implementer of a host store kind's goal port (beside its lease, wait and job ports) and for a host
that runs an objective. It implements ADR-0025 decision 8, as its amendment fixes it. The record is
`contract/schemas/run-goal.json`, version 1, with handwritten shared vectors in `contract/vectors/goal/`. TypeScript is
`packages/harness/storage/goal.ts` (the record, the table and the port) with `goalCommands.ts` (the commands); Go is
`go/context/run_goal.go` with `run_goal_commands.go`. Nothing here adds a member to a checkpoint, event, lease, wait, job or
report, and with no objective created the store is not asked about one.

**What the package does and what the host does.** Every command is the host's: no command calls a model, commits to the run, or
writes a checkpoint, wait, budget or approval. The host decides who may issue one and says who did (`actor`), and owns
scheduling, clocks (the store's), notifications and processes. `actor` is a label the host attests: nothing checks it, and nothing
in the package authenticates whoever sent a command. The package reads what only it can, the head's accepted Plan and its evidence,
and hands the store the head it read; the store decides against the head it holds.

**A worker's view (#315).** A command sent through a worker's fenced view (`fencedRunStore`, `unattendedRunStore`) presents that
view's fence, the one its commits and dispatches already present, and the store decides the command only while the fence is current
for the command's run. A worker a takeover displaced is refused as its commits are: `LeaseRefused` with the lease's own reason
(`stale-epoch`, `other-owner`, `released`, `expired`, ...), or `resource-mismatch` for a fence of another run, with nothing journaled,
seized, sent or changed. A command sent through the store itself presents no fence and is the host's, as before: the host's control
plane, a pause of a run whose worker is gone, and the abandon of a stuck worker's run need no lease. The fence is enough for what it
is for: what it shuts out is a stale holder acting on a run it no longer holds, and the lease is already the only thing that names
the holder. It says nothing about who may command; that stays the host's. An abandon takes the lease itself, so the view it came
through is stale after it: retry it through the store, or through a view of the fence it answered.

**The record.** One per run: its `revision` (the count of accepted commands), its `objectives` in order, the current one last and
only it open, each kept whole (history is never overwritten, at most eight), its `journal`, `discarded` and `inspections`. An
objective's state is `active`, `achieved`, `abandoned` or `blocked`; `paused` is a disposition of an open one, and a terminal one
has none. Each state holds what proves it: `blocked` its condition, `achieved` its head, waiver and the jobs that did not succeed,
`abandoned` what it left.

**The commands.** Each is `{version, resource, command, actor, expectedRevision, kind, ...}`; `expectedRevision` is `null` for a
run with no record. `goalCommand({store, command})` (Go `RunGoalCommand`) runs `create` (`objective`), `bind` (`steps`, `head`),
`achieve` (`head`, `waive?`), `pause`, `resume`, `block` (`condition`) and `unblock`; `abandonGoal` (Go `AbandonGoal`) runs
`abandon`; `inspectGoal` is a counted read, `readGoal` a plain one and `goalReport` the report. An accepted command answers the record
and its entry; a refused one throws `GoalRefused`, journaled where the run has a record. A goal refusal's record, `{version: 1,
reason}`, has the shape of a lease refusal's, and a reason such as `invalid` is both: route a refusal by the error's name
(`GoalRefused`, `LeaseRefused`), never by its record alone (#315 L-4). A repeat of a command id answers as the
first did while its entry is in the journal, with the record as it is now; another command under it is `conflict`, and a host that
wants to try again uses a new id. A repeat of an abandon asks the jobs again only while the objective it abandoned is current, and
refuses `revision-changed` with nothing sent once another objective exists.

| Command | Accepted when | Refused |
| --- | --- | --- |
| `create` | No objective, or a terminal one; an identity never used in the run; fewer than eight; over an abandoned one, its abandon's deliveries journaled; over a terminal one, every wait of the run resumed or ended, those the last abandon left and any parked since | `objective-exists`, `objective-reused`, `goals-exhausted`, `abandon-unfinished` (#318 M-4), `wait-unresolved` (M10) |
| `bind` | The head is the command's; the package read the head's accepted Plan, the steps named in it with origins, none closed; no earlier objective of the run bound that Plan | `plan-bound`, `head-changed`, `plan-invalid`, `step-settled`, `plan-reused` (M11) |
| `achieve` | The package's finding read from the record at the command's revision; not blocked; a Plan bound; the head is the command's; the package found nothing (below); no wait of the run unresolved, a woken one included; every unsettled job, `unproven` or `started`, waived, and only unsettled ones | `revision-changed`, `objective-blocked`, `plan-unbound`, `head-changed`, a finding, `wait-pending`, `waiver-invalid`, `jobs-unsettled` |
| `pause`, `resume` | Not paused; paused | `objective-paused`, `not-paused` |
| `block`, `unblock` | Not blocked; blocked | `objective-blocked`, `not-blocked` |
| `abandon` | An open objective | `objective-terminal`, as every command on a terminal objective is. So a delivery that `failed` has no goal path to be sent again: `abandonJobs` under a new command sends it, and journals nothing on the goal |

Before every one: `revision-changed` where the expected revision is not the record's, and `no-objective` where there is none.

**What an achievement proves.** The package reads the head the command names and refuses, as the command's journaled refusal:
`decision-pending` with a decision pending (a pause, an asked approval, a pending Plan departure); `lineage-changed` where the
accepted Plan is not the one bound, or a required step is removed, has other text than the accepted step, or stands out of the
required steps' order (steps the model added, and moves among them, are no departure: M17); `step-incomplete` where a required step
is not `completed` with evidence; `evidence-refused` where its evidence does not prove it from the head's own book now, with every job
the run has read afresh, so a cited start needs its job `completed` with `succeeded`; and, for a delegated step, `history-unsupported`
on a store without history, or `evidence-refused` unless the writer's history check (`verifyTaskEvidence`) finds the transaction that
recorded its child (M16). A departure a person approved changes nothing here: a required step it changed or removed strands the
objective, which is then abandoned. A later objective in the same run cannot bind that Plan again, so new work under a new Plan is a
new run.

**The goal port.** A store kind implements `GoalStore` (Go `GoalStore`): `goalVersion` 1, `goalLifetime`, and these, each atomic
under the same serialization as its commits, with the record surviving the store's own restarts.

- `commandGoal(transition)`: decides one command against the record's revision and the run's head, waits, jobs and lease as they are
  in that transaction, and journals it. `transition` is `{version, command, plan?, refused?, read?, fence?}`. With `fence` (#315), the
  store first checks it against its own leases in the same transaction, before it reads the record for a repeat: a fence of another
  run refuses `LeaseRefused` `resource-mismatch`, one that is not current the lease's own reason, and nothing is journaled; a repeat
  under a stale fence is refused too. The fence is not part of the command's digest. A bind carries the steps the
  package read or what refused them; an achievement carries `read`, the revision of the record its finding was computed from, which
  must be the command's expected revision (`revision-changed` otherwise), and may carry a finding; the store journals a finding as the
  refusal. It answers a pause and a resume with the run's lease as it holds it, and a repeat with the record as it is now. The store
  trusts the transition it is handed: a direct caller of `commandGoal` can claim any `read` and any finding, so only the package's
  `goalCommand` composes them from what it read (#318 L-11).
- While the current objective is paused, `acquireLease` and `resumeWait` refuse `objective-paused` (M8). `renewLease`, `releaseLease`,
  fenced commits and the abandon's seizure are unchanged: a held lease runs on to its expiry, and paused is not stopped.
- From `create` on, an unfenced commit of the run refuses `lease-required`, through `commit` and `commitHistory` alike, and after the
  objective has ended too: an objective's run is leased. An objective that ends paused keeps no pause.
- `inspectGoal(input)`: the record, with the inspection counted and its last row kept; no revision moves, and a repeat of the last id
  is not counted again (M12). Nothing is written for a run with no record.
- `recordGoalDeliveries(input)`: journals an abandon's deliveries on the objective that command abandoned (`invalid` otherwise).
- `readGoal(resource)`: the record, or nothing.
- The journal holds at most 64 entries. Past it the oldest refused entry goes first, then the oldest entry, and `discarded` counts
  them, so no command is refused for size (M12).

**The goal conformance kit.** `runStoreGoalConformance(create)` (`storage/goalConformance`; Go `CheckRunStoreGoalConformance`)
certifies the port, with a run of each kind, a whole-record compare after each refusal, a two-job case with a contradiction and a
restart at the end. It names the first property broken: `goal.create.*`, `goal.repeat`, `goal.conflict`, `goal.refusal.repeat`,
`goal.unfenced`, `goal.pause.*`, `goal.resume.*`, `goal.block*`, `goal.unblock*`, `goal.bind.*`, `goal.achieve.*`, `goal.terminal`,
`goal.abandon.left`, `goal.deliveries*`, `goal.none`, `goal.inspect.*`, `goal.journal.bound`, `goal.exhausted`, `goal.owner`,
`goal.fence.*` (#315: a fenced command is decided while the fence is current, refused stale with nothing changed, and refused for a
fence of another run) and `goal.restart.*`. The tests show a kind that leaves one guarantee out failing the property that names it.

**Abandoning.** `abandonGoal({store, effectHost, command, durationMs, cancel})` abandons the objective in one goal transaction, which
records the run's waits not resumed or ended and its jobs not settled, then runs `abandonJobs` under the same command id and journals
each job's delivery. A retry is answered from the record and journals its own deliveries; the effect host admits each cancel once
while it remembers the operation. It closes no more than `abandonJobs` does: a wait it left can still be resumed, but no new objective
is created over it until it is resumed or has ended (M10).

- **A crash between the sends and their journal (#315 L-2).** The retry, from a new process, hands `cancel` the same `operation` as the
  attempt that crashed: it is the command's and the job's (`jobCancelOperation`), never the attempt's. An effect host whose admissions
  outlive its process admits it once and answers `already-admitted`; the reference host forgets on restart and lets it through again,
  so a host's `cancel` must be idempotent by `operation`. The journal holds the deliveries of each attempt that lived to write them;
  the job's record keeps the request of the attempt that crashed, under that attempt's epoch.
- **A terminal objective's jobs (#315 L-6).** `abandonGoal` on an achieved or abandoned objective refuses `objective-terminal` and sends
  nothing, so an achievement's waived jobs are stopped with `abandonJobs` under a command of the host's own.
- **A `failed` delivery (#321 L-2).** It is journaled, so the next `create` goes through although that job was never asked to stop.
  Send it again with `abandonJobs` under a new command before creating the next objective: after, it asks the new objective's jobs
  too.
- **An abandon that can never be journaled (#321 L-1).** Where `abandonJobs` always throws, or the store always refuses the
  deliveries, every later `create` on the run refuses `abandon-unfinished`, and each retry sends again to an effect host that forgot
  its admissions. The way out is a new run.

## Re-authorization waits (#226)

This section is for the same implementers. It implements ADR-0025 decision 4 as its amendment fixes
it. The new records are `contract/schemas/run-reauth.json`, version 1, with handwritten shared vectors
in `contract/vectors/reauth/`; the wait record (`run-wait.json`, not yet released) gains the reason
`reauthorization`, its pending decision and the members `reauthorization` and `reauthorized`.
TypeScript is `packages/harness/storage/reauth.ts` with the effect-host boundary in `storage/lease.ts`
and the wait transitions in `wait.ts`; Go is `go/context/run_reauth.go` with `run_lease.go` and
`run_wait.go`. Nothing is configured by default: without a credential check at the effect host no
refusal is minted, and a run's bytes are unchanged, with two exceptions that apply whether or not a check
is configured. Under a run store, a failure a tool or a host itself reports in the exact shape of the typed
failure below is recorded and reported as `unknown`, not as it says (see "The run stops at the refusal");
with no run store it still reaches the model as written. Under a fenced view, a `perform` after the dispatch
has ended runs nothing.

**The boundary.** Only the effect host can tell an operation that was never admitted from one whose
outcome is unknown, and only at its own admission boundary. The reference effect host takes an
optional credential check (`new ReferenceEffectHost(store, {credentials})`, Go
`NewReferenceEffectHostWith(store, ReferenceEffectHostOptions{Credentials})`), asked after the fence
and the idempotency identity and before the host records the admission. The check can take longer than
the lease has left, so the abort and the fence are looked at again after it, before anything is
admitted or refused: a run aborted or a lease lost during the check gets that error and no admission.
A refusal (`{cause: expired | revoked, connection, principal, permissions, requires}`: a bounded connection reference, the SHA-256 of
the principal identity, and the scope labels the credentials held and the operation needs) is returned
as `reauthorizationRequired(admission, refusal)` (Go `NewReauthorizationRequired(ctx, admission,
refusal)`): the key is not admitted and nothing is performed. A certified host mints the same way. The
package trusts the refusal only when it was minted once, for the admission it has in flight (the exact
object in TypeScript; the dispatch's own context and an equal admission in Go), before anything was
performed, with a well-formed refusal, and the dispatch then returns it, from an effect host proven
for the run's store (#253). A copy or another admission, a second mint, a mint after the dispatch or
after the operation was performed, a refusal followed by a `perform`, a refusal carrying anything else
(a token or an error member, an undigested principal, an unbounded connection) and an unproven host
give an untrusted refusal: an ordinary failure whose operation keeps its unknown outcome. A `perform`
after the dispatch has ended runs nothing (`LeaseRefused` `invalid`) and takes the trust back from a
refusal that dispatch minted. In TypeScript a dispatch may be a plain function that throws the refusal, or
throws after scheduling a `perform`: its throw ends the dispatch like any other answer. One that returns
something other than a promise behaves as an async one that resolved to that value: a value gives a `result`
row, `undefined` a `claimed` row and a storage stop, and nothing is sent again. An error a tool returns after admission (a provider's 401 included) is
never this proof. A credential check that errors admits nothing and proves nothing, and what it threw
stays with the host: the reference host replaces it with one fixed failure, `CredentialCheckFailed`
(`unknown`, `tool`, `unknown`), so a check's own typed error or detail reaches no record.

**The run stops at the refusal.** For a refusal the package trusted for this very dispatch, and only
for it, the writer records the dispatch as `unknown` with the typed failure (`authentication`, `tool`,
`not-started` and a fixed detail) and closes dispatch: nothing after it is committed and the model is
not asked again, so the run's head holds the refused operation as its last dispatch. The typed failure
on a row therefore means that refusal, which is what the park's head check and the re-send's grant
read. Any other failure of that shape, whatever wrote it (a tool, a host, an untrusted refusal, a proof
kept and returned again by a later dispatch), is recorded as `authentication`, `tool`, `unknown` with
no detail, and the run goes on as it does after any failed call. How the run ends differs by engine:
the Go adapters report the typed refusal; the Vercel and Deep Agents engines report their checkpoint
error (a storage failure, since what followed the refusal was deliberately not saved), the refusal
itself being the call's `tool_result`; the Codex engine emits no `error` event at all, it reaches `end`
after that `tool_result` and the head stays `running`. So on every engine the host asks the fenced view
the run used when the run ends, whatever the run's error or its absence: `reauthorizationOf(view)` (Go
`ReauthorizationOf`).

**Park.** `parkRun({..., reauthorization: proof})` parks a `reauthorization` wait, under the same lease
the proof was minted under (`stale` otherwise), over a head whose last dispatch is the never-admitted
operation the proof names (`unbound` otherwise): a host or Runtime effect, not a read, `unknown`, with
the typed failure and the operation's source, tool and request digest. A proof parks one wait (`used`);
one the package did not mint and trust is `untrusted`. The wait's pending decision is the operation
(its claim id, source and tool), the connection and the cause; its condition is an event (the host's
or the person's action) with no wake time; it records the disposition (`run-reauth.json`), never a
credential. The store checks the fence binding and the head again in the park's transaction and, when
the run's approved call is unsettled (#266), attaches that approval's binding and stamps its expiry as
the wait's approval deadline; the park's own limits may set `run` and `waitMs`, never
`approvalTtlMs`. The attention event needs `reauthorization` and names the connection. A timeout or a
deadline ends the wait and closes the run, as for an approval wait.

**Resume.** After the host's or the person's explicit action the host wakes the wait with its event
and resumes it with the action (`run-reauth.json#/$defs/action`) and its current evidence. A wake is
never the re-authorization (`decision-required`). `resumeParked` revalidates the pins as for every
wait (an approval under a changed policy or descriptor is `approval-changed`), then the action, whose
`actor` is a bounded reference with the connection's pattern and not free text: it is this wait's and this connection's (`invalid`); a
`replacement`, or a refresh whose principal is not the parked one, is a changed identity
(`identity-changed`: a new admitted run is needed, ADR-0019), never relabelled as a refresh; an action
from before the park is `stale`; current permissions that do not cover what the operation requires are
`permission-insufficient` (narrower ones that still cover it continue). The store's claim checks the
principal, the permissions and the action's time (not before the park, not after the store's time)
again in its transaction, records the refresh (`reauthorized`: actor, time, principal, permissions and
the action's digest) and leases the run, bounded by every ceiling of the run, an unsettled approval's
expiry included.

**The continuation.** `reauthorizedRunStore({store, effectHost, resumed})` (Go
`ReauthorizedRunStore`) is the fenced view the continuation runs on: unattended admission, the claim's
fence (which must be the run's held lease), and one grant. The writer never sends again a dispatch it
holds as unknown, except, once on that view, the operation the resumed wait names, whose recorded
failure is the typed never-admitted refusal, when the run asks for exactly that request again: it is
claimed again under its own identity and the effect host admits it for the first time. The claim clears
the typed failure before the dispatch, so a crash after it leaves a row no view can send. "Exactly" means
the bytes that were refused: the effect host admits their digest and the tool runs the bytes the call wrote,
so other bytes never run under the refused identity. The row's fingerprint is of the parsed form (#275), so
other bytes can share it, and they get what any call gets from the row: whitespace or member order answers
`HostOutcomeUnknown`, another spelling of the same values is refused with `DurableRequestConflict`, and
different numbers are another write, which runs on a row and under an identity of its own. Each way the
refused row keeps its typed failure and the grant stays for the refused bytes. TypeScript holds the same
rule for rows another writer recorded: there a call's request is the canonical form of what the tool runs,
so a grant also needs a request this run can hold exactly, and a number `JSON.parse` rounded takes none,
even where its bytes are the row's. In both, no arguments at all are the request for `{}` (#271). Nothing that
may have been admitted is ever sent again: re-authorization is never retry permission. Whether the
continuation asks for exactly that request depends on the kind of call, not on the adapter. A host
tool's request is its arguments, so it keeps its identity on every adapter when they are repeated. A
Runtime call's request carries its call id, so a new call id is a new operation on any adapter, under
its own approval policy and identity: the grant goes unused and the never-admitted row keeps its typed
failure and unknown outcome. ADR-0008's continuation restores an approved act call whose result was
never written as executed with an unknown outcome and does not send it again, whatever the
re-authorization. A new decision in the same run does not send it either: with the same arguments the
call is not run, the row stays unknown and the writer answers `HostOutcomeUnknown`. A Runtime call asked
with a new call id is a new operation under its own approval; anything else needs a new run.

**A re-send refused again.** The refresh may not have taken, so the effect host refuses the re-send too.
That refusal is trusted like the first: nothing is admitted or performed, the row holds the typed failure
again and the run stops with the proof on its view. It cannot park a second wait, and `parkRun` answers
`unbound`: the row keeps its place and the continuation's model or agent row follows it, so the operation
is no longer the head's last dispatch. This is a chosen limit. It fails safe, since nothing ran and the
grant on that view is spent, and widening the head check for it would widen what every park and every
store accepts. While the claim's lease is held the host may open another view of the same claim, which
sends the operation once more with no new action. Otherwise the operation stays an unknown outcome,
reconciled like any other, and a further attempt is a new run.

**What the host keeps.** The credentials, their refresh and the person's action are the host's; the
package receives digests, labels and bounded references only, and the wait records those. The reference
host's check error, a tool's plain error or a wrapped one with no typed failure inside, a refusal with any other
member and an undigested principal reach no checkpoint, event, wait, attention event, export, report, prompt or
error. Two things the host writes itself do. A typed failure with an authored detail, plain or wrapped, from a tool
or a certified host, reaches the checkpoint, the events and the prompt, unless it has the never-admitted shape. And the readers cannot
recognize a secret that fits a pattern: a connection, an `actor` or a label that holds one reaches the wait
and its attention event. Keeping credentials out of both is the host's. The store's obligations are certified by the wait conformance
kit's `reauth.*` properties: the fence (owner and epoch) and each way a head can fail to hold the dispatch
at the park, the attached approval and its expiry, the claim's checks, its lease bounded by that expiry
and the recorded refresh, `limitRun` keeping the approval time, the timeout and the deadline wake ending
the wait and closing the run, and the park, the claim, the attached approval, the disposition and both
endings across a restart. The kit does not exercise a repeated reauthorization park or the refusal of
`reauthorized` on another wait's claim; the memory kind is the reference for those. Refusals are
`ReauthorizationRefused` (`invalid`, `untrusted`, `used`, `stale`, `unbound`,
`permission-insufficient`), with `ResumeRefused` `identity-changed` and the wait refusals above.

## A question's answer after the run completes (#340, #341)

A run that asks the person a `user_question` and then ends `complete` can take the answer two ways. Both are
borne out by the store, never by what the host passes alone, and each hands the first request one fixed line
(`QUESTION_ANSWERED`, Go `QuestionAnswered`) and one data block: the canonical JSON of `{question,
resolution, answer?}`, where `resolution` is `answered` (with the answer) or `unanswered` (without one). The
vector is `contract/vectors/checkpoint/successor.json`.

**A claimed question wait's continuation (#341).** After `resumeParked` claims a question wait, the host
continues the same run with `context.questionContinuation` (Go `Input.QuestionContinuation`) set to the wait's
id, through the claim's fenced view. This is a fourth resume a `complete` head takes (#328): it is admitted only where
the store's `exportWaits` holds this run's question wait by that id, claimed (`resumed`) with its resolution,
and parked at the head's own revision, so a continuation delivered again after the head moved is refused
`RunComplete`, and only through a fenced view of this run whose fence is the owner and epoch the claim recorded
(#355), so each of its commits is fenced by the claim's lease. Through any other store it admits nothing. The answer handed on is the wait's resolution, never host text. A continuation with no run
store, or of a run with no head, a wait id that is not a storage id, and one beside a job continuation are
`WaitRefused` `invalid`. One that names no such wait admits nothing: a `complete` head is then admitted only as
before (#328), and any other head is `WaitRefused` `mismatch`.

**A successor run (#340).** With no wait, the host opens a new durable run with `runStore.successor =
{predecessor, question, answer}` (Go `DurableRunInput.Successor`) at the one id `successorRunId(predecessor,
question)` (Go `SuccessorRunID`): `successor-` and the SHA-256, in lowercase hex, of `jpack-successor`, the
predecessor's run id and the question's event id, each after one NUL byte. A storage id holds no control
character, so each pair has its own id, and an id with the `successor-` prefix is opened only with its
`successor`. The host passes `successor` on every open, `resume` false to make the run and true to continue it,
and presents the predecessor's request and phase (Go: through `PrepareDurableInputFor` with the
`DurableRequest`, as a session and the delegate coordinator do; `PrepareDurableInput` names no request and
makes no successor). On every open the package reads that the predecessor's head is `complete` under its own
id, that its records have not expired (`retainUntil`), that it has no usage budget and no run deadline and has not used its whole
allowance (`used` below `limit`), that the question is the latest `user_question` in its retained log, that the
answer is a real answer to it, read as a wake's answer is (an offered option or non-blank text; a
recommendation is none), and that the predecessor holds no wait, in any state, on that question. Making it
checks the request key and phase against the predecessor's, then reads the predecessor's checkpoint and
writes the successor's first checkpoint, an ordinary one: the predecessor's `unit`, `used`, `limit`, `grants`
and `requestCap` exactly, its request key, phase, context scope, principal and `retainUntil`, at the
checkpoint version those members need, with `status` `running`, and nothing else: no approval, operation,
plan, explanation, test-call count or journal entry. It is created with an expected revision of none, before
any model request or tool call, under the predecessor's lease where the store has a lease port: the package
takes the predecessor's lease (owner the successor's id, expected revision the head it read, 60 seconds within
the run's deadline) before the create, reads the predecessor's head again after it, and releases the lease on
every path. The predecessor's head and log are read and never written. `parkRun` refuses a question that has
its successor (`nothing-pending`), and reads the successor again after its park commits (`succeeded`).

**What the package does not hold.** There is no parent, question or answer link in any record: the binding is
the derived id. The answer is not recorded. Each open reads the answer the host passes again as a real answer
to the predecessor's last question, but nothing can check it against the answer the successor was made with,
so a host that must hold the answer to one value keeps it itself. The successor carries no chat or record of
the predecessor's approvals. A predecessor with a usage budget is refused `budgeted`, since its journal is not
carried, and a successor input with a usage budget or a run-store chat context is refused `invalid`. A host
that passes the chat as the input's own chat, with no run-store context and no usage budget, is not affected.
The run's deadline is not carried, so a predecessor with one has no successor, before or past it (`deadlined`,
#355). The package reads it through the store's `readRunDeadline` (Go `RunDeadlineReader.ReadRunDeadline`; the
memory stores have it): a store that holds deadlines (`limitRun`) and does not read them back is taken as holding
one, a failed read refuses as itself, and a store with no wait port holds none. A host that wants the successor
bounded sets its deadline with `limitRun` after the create.

**Refusals.** `SuccessorRefused` (`configuration` for `invalid`, `unbound`, `budgeted` and `deadlined`, `stale-state` otherwise, from
`context`, `not-started`), before any model request or tool call:

- `invalid`: `successor` is not a record whose `predecessor` and `question` are storage ids; it sits beside an
  execution, a job or question continuation, a usage budget or a chat context; the store is one writer's
  checkpoint callback, which cannot create once; or the open does not name its request (Go
  `PrepareDurableInput`, which makes no successor: a create through it is `invalid`, while a successor that exists
  opens through either it or `PrepareDurableInputFor`).
- `unbound`: the run id is not the derived one, the question is not the predecessor's latest
  `user_question`, or the answer is not a real answer to it; or the run id has the `successor-` prefix and the
  open has no `successor`.
- `budgeted`: the predecessor has a usage budget.
- `deadlined`: the predecessor has a run deadline, or its store holds deadlines and cannot read them back.
- `succeeded`: the run is made again (`resume` false over a head), its create meets a head another open made
  meanwhile, a `complete` head whose latest question has its successor is resumed another way (a job or
  question continuation, a re-authorization claim, a leg of an objective): the successor carries the allowance;
  or a park of the question finds its successor after the park commits.
- `not-complete`: the predecessor's head is missing or not `complete`.
- `exhausted`: the predecessor has used its whole allowance (`used` at or past `limit`, a provider-managed run's
  one request included): there is nothing to carry.
- `parked`: the question has a wait of its own, and is answered through it.
- `moved`: the predecessor's head is not the revision the successor was made from when the package reads it
  again after the create. Every continuation of a `complete` head this package admits is fenced (#355), so only a
  commit made outside the package can move the head while the successor holds its lease; the refusal is kept as a
  defence against such a commit. The successor's head stands, and since nothing records the revision it was made
  from, a later open of it is not refused for the move.

The bytes are `SuccessorRefused: <reason>.` (the vector's `items`). A request cap other than the predecessor's
is the request cap's own refusal (`stale-state`), a principal other than the predecessor's is
`principal-changed`, and another request key or phase is the controller's own refusal ("The request or
execution phase changed. Start a new response with the current context."), all before anything is written.
The predecessor's lease refusals reach the host as their own: `LeaseRefused` `held` (another owner's lease of
the predecessor is live), `revision-changed` and `principal-changed`, and `WaitRefused` `closed`,
`deadline-passed` and `wait-pending`; expired records are `RecordsExpired`.

**Races, and how each ends.** A successor, a question wait of the same question and an admitted continuation of
the predecessor are on different resources (the successor's head, the predecessor's wait, the predecessor's
head), and no one transaction covers them. Where the store has a lease port the successor is made under the
predecessor's lease, which a park and a claimed continuation also need, so those are serialized; the package
reads each again where it can, and every order of what the package admits fails closed:

- An admitted continuation and a successor: a continuation of a `complete` head is admitted only through a fenced
  view (a claimed job or question wait's through its claim's own, a re-authorization claim's through its view), and
  an objective's run refuses unfenced commits, so every commit it makes is fenced (#355). While its lease of the
  predecessor is live the successor is `LeaseRefused` `held` and writes nothing. Once it has lapsed the
  successor's lease is a new epoch, so the continuation's commits are `stale-epoch`, and an open of it after the
  create is `succeeded`. A commit of the predecessor made outside the package during the create refuses the
  successor `moved`; one after the re-read is not seen, since it is no continuation the package admitted.
- Two successors at once: under the predecessor's lease the later is `LeaseRefused` `held`; on a store with no
  lease port the store's create admits one, and the other is `succeeded`, having written nothing.
- A park that commits before the successor's first read: the successor is `parked` and writes nothing.
- A park that commits after that read and before the successor reads the waits again after its create: the successor's head
  exists, the successor is refused `parked` on this open and every later one, and once the wait is claimed its
  continuation is refused `succeeded`. Neither continues; the person's answer reaches no request.
- A park that read no successor and commits after that second read: the park reads the successor again after
  its commit and is refused `succeeded` (the wait stands); the successor's first open goes on, every later open
  of it is `parked`, and the wait's continuation is `succeeded`.
- A successor made before the park reads it: the park is `nothing-pending`.

A crash while the successor is made leaves it written or not: if not, the host makes it again; if so, making
it again is `succeeded` and the host continues it with `resume` true.
