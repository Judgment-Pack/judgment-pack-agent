# ADR-0027: Tool taxonomy

Status: accepted for the v0.11.0 foundation. Selected upstream producer alphas are published and verified; routing has bounded focused acceptance. Final Agent integrated CI, independent review and artifact pairing remain release prerequisites. Implements the foundation of #214; amends ADR-0006 and ADR-0009.

Every owner declares an operation and an effect. Host declarations carry `taxonomy: {operation,effect,checkKind?,experimental?}`. This deliberately avoids reinterpreting the existing internal `HostTool.effect` (`read`, `proposal`, `workflow`, `memory`, `plan`). Runtime MCP tools publish the same four canonical keys directly in `_meta`; Agent maps the served metadata to its internal taxonomy. The selected [Runtime v0.39.0-alpha.1](https://github.com/Judgment-Pack/judgment-pack-runtime/releases/tag/v0.39.0-alpha.1) publishes the companion metadata from #329; final Agent artifact pairing remains required. Existing names, including `ask_user` and experimental Runtime names, are preserved. New names use `verb_noun`; experimental status is metadata.

Operation families are discover, read, answer, check, transform, evaluate, propose, act, run, own-state and ask. Effects are pure, read, read-external, propose and act, in that strength order. Check requires one fixed subkind: validate, check, verify, test, analyze or advise; other operations forbid checkKind. Experimental is an optional boolean.

Compatibility: discover and answer accept pure/read/read-external; read accepts read/read-external; check accepts pure/read/read-external. Transform and ask require pure. Evaluate permits pure/read/act, declaring its actual strongest effect; current Runtime evaluation reads project state and can append audit, so it requires act. Propose requires propose; act requires act. Run permits read/read-external/propose/act. A dispatcher that can only hold proposals uses run/propose; any reachable acting child requires run/act. Its summary preserves the strongest effect and complete child-family profile; the selected leaf retains its own approval gate. Own-state permits pure/read for in-run bookkeeping or reading working state; changing an external resource requires act. Pure means no external I/O; ask's question transport and run-owned bookkeeping grant no resource access. Answer means generated text, never a stored fact; check findings are non-binding. Only the actual served Runtime path may declare evaluate, and only its existing gated evaluation name; first-party trust on a host tool confers no such authority.

Missing or malformed declarations and contradictory compatibility projections are configuration refusals before provider work. `effectClass` remains only a checked legacy execution projection: pure/read/read-external map to read, propose to propose, act to act. No field or tool name supplies a fallback. Gateway and MCP sources cannot declare local pure/read effects; acquired external data requires read-external. Trust labels remain descriptive and validated against their existing closed vocabulary; they never authorize an operation. Host limits withhold tools outside a subset of declared operations/effects and cannot relabel them. An act cannot lower its default required approval to none.

Default approvals are none for pure/read/read-external/propose, required for act; proposals remain held for review. Derived MCP hints are readOnly/idempotent for effects other than act, destructive for act, and openWorld for read-external/act. Conservative hints convey no authority. Review offers discover/read/check; work adds propose/act. Both always offer a valid pure ask, even when a family limit excludes it; asking grants nothing else. The family/profile helpers now serve the implemented #172 routing path, with bounded focused evidence and final integrated checks still pending.

External reads require a host-owned receipt verifier at declaration. Before success publication the verifier receives canonical immutable source/descriptor identity, exact source tool name, arguments captured before execution, result without receipt and the receipt. Missing, oversized, invalid, mismatched or unverified receipts withhold the result with a tool-failed/unknown outcome: the executor ran, its source-call accounting remains consumed, and the error never authorizes an automatic retry. The host verifies signatures and binding to all supplied evidence under its own trust roots; Agent does not claim cryptographic verification from a callback's presence or a source label. No credential handling is added.

All new source admissions require an approved taxonomy-bound tool-list digest, including gateway and custom sources formerly admitted by version alone. New approved legacy tool-list pins include taxonomy with name, description and input schema. Changing classification requires approval of a new digest. The selected [Gateway v0.22.0-alpha.1](https://github.com/Judgment-Pack/judgment-pack-gateway/releases/tag/v0.22.0-alpha.1) publishes compatible owner taxonomy from #324. Producer publication and bounded source pairing are verified; final Agent artifact pairing remains required. Snapshots without that metadata still refuse, and local host strings do not become signed snapshot evidence. Historical snapshots and saved record bytes are unchanged.

Skills grant nothing. Optional kind is method/knowledge/procedure/review; absent is unclassified. Optional tags contain at most eight strings, each at most 32 UTF-16 code units; tags and kind are untrusted, digest-bound, and cannot grant authority. The profile is computed from admitted declared tool references: strongest effect and the ordered union of operation families. Empty skills are pure with no families. Unknown or unadmitted tools withhold the skill. Author/model supplied profiles are refused by the closed declaration. Catalogue lines show the computed profile and preset filters use it. The bundle pin binds kind/tags when present and ordered dependency taxonomies when tools are nonempty, so a dependency change triggers the existing person re-approval flow.

The implemented [Runtime action approval path](../design/runtime-action-approval.md) requires an explicit trusted host port and the original durable RunStore. Acting Runtime metadata is refused without that seam. With it, absent host policy asks for an exact person decision; checkpoint 3 and event 2 bind the pending call, and acknowledged continuation plus fresh authority checks gate dispatch. Acting evaluation still requires `rehearsal: true`; rehearsal rewriting never relabels the owner’s act capability or changes Runtime audit behavior. Paired focused approval-flow evidence does not complete final Agent artifact pairing, integrated CI or independent review; those remain release gates.

No taxonomy/profile field is added to a checkpoint, source event, loaded-skill event or policy record. Resume recomputes admitted profiles and compares the existing stored bundle pin. Frozen historical record inventories and fixtures remain unchanged; live tool offers and skill declaration schemas change together in both languages. Broader record format changes require a separate version decision.


Gateway V2 source admission requires independently approved platform, binding, operation,
endpoint and the complete owner taxonomy map. The complete original canonical snapshot bytes
remain the toolListDigest pin. A host may select an execution subset without reducing the full
approved projection. Changed snapshot bytes, including metadata for an unselected sibling,
refuse dispatch until deliberate reapproval. The host observer must describe current execution;
reading an old file alone cannot prove the upstream server remains unchanged.

The built-in Gateway verifier accepts only v3 acquisitions for read-external declarations.
It verifies Ed25519 under an independently pinned public key and authority, the signed keyId,
source/operation, exact call and statement commitments, canonical artifact digest and equivalent
text/structured envelopes. Every execution receives a private unpredictable receipt session;
the host bridge passes it as tools/call `_meta["io.judgment-pack/session"]`. Only initial
callIndex 0 and prevSignature null are accepted. The nonce never enters tool arguments or the
model offer. Deadline/cancellation wrappers preserve it without changing the parent signal or
context. Uncertain acquisitions are never retried automatically. This verifier cannot execute
an approved action or mint an action receipt; action routes keep their existing policy gates.

A remote acquisition signs its endpoint and server name/version. A stdio acquisition omits
transport/server and signs its executable adapter identity/version/digest instead. The optional
independent adapter pin checks all three values; when omitted, executable identity is explicitly
unverified. Receipt signatures do not authenticate descriptor snapshot bytes or taxonomy. The
proof combines independently approved snapshot/current-execution guards with the signed call
and result. Signed descriptor provenance remains separate work. A custom non-Gateway verifier
must be explicit; failed built-in Gateway verification never falls back to it.

Fixed receipt creation, one-use authorization consumption and usage accounting necessary for an
already admitted read remain read-external bookkeeping only when bound to the same admitted
principal/connection/scope and when they grant no new authority or independently change customer
resources/configuration. Token refresh requires evidence that identity/scope are preserved;
opaque scope metadata alone does not prove this. SQL writes, arbitrary MCP mutations, new
identities/scopes, binding/revocation/configuration changes and other resource mutations remain
act. Conservative MCP idempotency hints never authorize retry.

## Amendment: an external result is not context until its receipt verifies (agent#242)

Status: accepted for v0.12.0. Amends the receipt paragraph above; both languages, held to the hand-written `contract/vectors/tool-result/receipt-quarantine.json`.

v0.11.0 verified an external read's receipt only after the call had passed through the durable writer and the read wrapper. A result whose receipt then failed was already recorded as a successful durable result and retained in the chat's ledger: the source event said it was withheld while `list_context`, `read_context`, `read_window` and `search` returned it, later model requests carried it, a checkpoint written meanwhile held it, and its durable row was offered and accepted as Plan evidence.

The boundary is now: nothing from an external result is reusable context, or successful source evidence, until its receipt verifies. The source gate makes the receipt check before the call runs and hands it down the call (on the call's context in Go, by its deadline signal in TypeScript). The durable writer runs it after the host's execute and before it records anything of the result; the read wrapper runs it before it retains or caches anything; the gate runs it last only where nothing below did (a result served without a call). It runs once, and every later caller is given its first outcome. A result whose receipt is rejected, missing or not verified within the source's deadline (expired), including an error result, is recorded once as the call's uncertain outcome: state `unknown`, its failure the gate's refusal (`tool-failed`, `unknown`, the withheld text above), with no result bytes, observation or repeat record. It is never retained, so no reader lists or returns it, no later request or index carries it, and no Plan step is offered it or accepts it. The physical execution stays once-only and is not retried; its source-call accounting stays consumed. The check's time is the rest of the source's deadline, as before: when it runs out the result is withheld, not reported as a timeout, and since the execution itself ended in time its outcome is still recorded without closing the run's dispatch.

Quarantine choice: an unverified result is not listed at all. The failed call stays inspectable through what already records it: its `tool_result` event (`isError`, `tool-failed`, `unknown`, the withheld text), its durable dispatch row (`unknown`, with that failure) and, where the run lists its operations for the model (the API adapters; a native run's single invocation lists none), the operation record the context tools list as `<tool> · failed`, whose result is the gate's refusal. No listing state, record member or version is added, and no frozen record format changes. A checkpoint written while a receipt check runs (by another call's commit) holds only the call's claim, so a run restored from it holds no part of the result. Calls in flight together are checked each on its own; a verified result is retained as before.
