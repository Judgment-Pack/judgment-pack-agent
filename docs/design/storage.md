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
continuation binds its own live snapshot again. Child checkpoints, scoped claims, observations,
provider release proof and continuation bytes continue through the same root revision owner.

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
