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
responses remain unknown and stop the writer. A fully consumed non-2xx refusal is acknowledged
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
