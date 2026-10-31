#!/usr/bin/env bash
# The parity proof for packages/harness: every file moved from the Desk, diffed against the Desk's
# original, with every difference printed and named.
#
#   scripts/parity.sh <desk-checkout>
#
# <desk-checkout> is a checkout of Judgment-Pack/judgment-pack-desk (the extraction was read from
# integration/v0.13 at the commit `desk_pin` below, which CI reads to check the Desk out). Each row of the table below is one moved file: its place in the
# package, its place in the Desk, the sha256 of its expected diff, and why it differs. A row whose
# digest and reason are "-" must be byte for byte the Desk's. A row with a reason prints its hunks
# under that reason, and its diff (the `diff -u` hunks printed below the row's header) must hash to
# the row's digest, so a hunk nobody named fails even in a file that has named ones. The run fails
# on a row marked "-" that differs, on a diff whose digest is not the row's, on a file missing from
# either side, and on a package file that is neither moved nor declared new below. Exit 0 means the
# hunks printed are the only differences. After an intended change, the failure prints the new
# digest to put in the row.
set -euo pipefail
# The Desk commit the table was made against, in full: .github/workflows/ci.yml reads this line.
desk_pin=d9251ecc272b9ac4f182fdfe652af54a0b2ead23

if [ $# -ne 1 ] || [ ! -d "$1/web/src/assistant" ]; then
  echo "usage: scripts/parity.sh <desk-checkout>   (a checkout of judgment-pack-desk)" >&2
  exit 2
fi
desk=$(cd "$1" && pwd)
root=$(cd "$(dirname "$0")/.." && pwd)
pkg=packages/harness
a=web/src/assistant

# package path | Desk path | sha256 of the expected diff, or - | reason it differs, or - for none
table() {
  cat <<EOF
$pkg/agent.ts|$a/agent.ts|315e753a221237d94dbd7b5893111cfe05049fbcb3bab20edbe0e4502b72dfcf|agent#10: a critic run may name the model the host named for the critic (AgentRequest.model); agent#10 review row 10: the critic's model passed down once per pass; review of agent#35: a delegated child's run may carry the child model too (model, delegated); ADR provider failover and the usage ledger (#104); agent#158: invocation-scoped host provider-release acknowledgement for durable handoff; v0.11: original provider request evidence for route widening
$pkg/agentTransport.ts|$a/agentTransport.ts|2274ab45c03eb2cefa483effaded5ce7112f6c52c510a92ad2a912346e67bb1c|injection points: AITarget, deskSocketURL, chassisUrl, socketProtocols, sessionBearer, whenSessionEnds; agent#10: a run's model is the bound one or, on a critic run, the host's critic model, and no other; agent#10 review row 10: a run's model other than the bound one is sent only on a critic run, the host asked once by the engine; review of agent#35: another model sent on a delegated child's run as on a critic run, the coordinator's mark never sent
$pkg/agentTransport.test.ts|$a/agentTransport.test.ts|df8c20487e36f88e9c14eea65b142a7bb90be82990aea400756669423af7630f|injection points: the Desk's two module mocks become one mock of host.ts
$pkg/completion.ts|$a/completion.ts|b3e315c377fda9312c6961e63eaf6c454d72879724e8e19c7854e2b662fd17d3|agent#9: the prompt cache reads and writes (cacheReadTokens, cacheWriteTokens) read from the SDK usage and kept; agent#104 fix round: persisted failover, delegated bindings and usage, wire token presence and exhaustion refusal
$pkg/completion.test.ts|$a/completion.test.ts|-|-
$pkg/context/context.test.ts|$a/context/context.test.ts|9ec7c51271efdda6b3203d2dd15f4505f93c9f0aa1b3e712127b5dc76ef27ff3|agent#1: the reference envelope names read_window; agent#12: the compacted summary's steps read through their data block; agent#164: admitted request values and boundary-shaped fixtures; v0.11: explicit taxonomy on host context fixtures
$pkg/context/ledger.ts|$a/context/ledger.ts|3cf5f843bc6ffdb4791ab90bff0750966f9b8aa1d8646c7bb552c6e7ba448212|declaration emit: Entry exported (a type, no runtime change) so tsc can write controller.d.ts; agent#1: the reader, read_window, among the ledger tools and named in the reference envelope (readWith); agent#5: search among the ledger tools, the attachments the host retained, an image item read whole (a reference followed to the image it names) or answered with the line saying why it cannot be shown, search naming such an item, a retained image by reference; agent#8: the entries in a store, a run's own or a chat's turn (LedgerStore, EntryStore), a read of an evicted entry saying why, a chat run's checkpoint carrying the entries it retained or read, a restore into the same chat, release and isolate; agent#15: a store may adopt an entry the run retains itself with the same content (adopt, a delegated child's view of the chat); ADR proposal edits (#110); agent#159: restore exact validated retained valueJSON source after canonical durable checkpoint publication; preserve legacy unpaired digest semantics; v0.11: exact retained values and taxonomy on context tools
$pkg/context/managed.ts|$a/context/managed.ts|55e2938aa63823dc29a456f980436e669652181f151f183d4382d2e4dc5c498b|agent#8: a run's end closes its context (a chat's turn closes, a ledger of the run's own is cleared), and the comment on how long derived context lives; agent#10: the critic's refusals put on the stream as guardrail events before the result; ADR-0006: a tool with a source's call and result decorated with its source record; ADR-0008: the approval pause at the boundary: a call refused while another act call waits said with its call, its guardrail and its refused result, the run ended paused once an act call waits, and on a continued run the decision and the approved call's result written before the first request and the continuation appended to the prompt; agent#80: a refused continuation says so once, a guardrail with reason approval refused after the written refusal; ADR-0010: the critic's decision followed by a durable checkpoint carrying it; ADR policy table (#116); ADR cross-provider review round two (#117); ADR the output boundary (#100); agent#100 fix round: guard notice fields and carry live child output counts into closed-parent checkpoints; agent#100 fix round 2: bind direct gate notices to the run output guard; agent#100 fix round 3: guard undeclared call and result identifiers before execution observation; ADR records (#101); carry of ADR-0017 into ADR-0018 (#126); ADR steering a run in flight (#105); carry of ADR-0017 into ADR-0021 (#129); carry of ADR-0018 into ADR-0021 (#129); carry of ADR-0017 into ADR-0020 (#130); carry of ADR-0018 into ADR-0020 (#130); carry of ADR-0021 into ADR-0020 (#130); ADR the evaluation corpus (#103); carry of ADR-0021 into ADR-0022 (#128); carry of ADR-0020 into ADR-0022 (#128); agent#133: close usage before end on output refusal; agent#142 review round 2: structural request reasoning boundary; agent#154: checkpoint writes use the canonical mutation boundary and detached controller reads; agent#164: admitted request values and boundary-shaped fixtures; agent#186: typed fixed-cap refusal emits error, last valid usage and end without mutating refused state; ADR-0024: versioned records and frozen historical structural readers (#165); agent#159/#158: durable root startup and acknowledged event delivery with named storage uncertainty; agent#159 callback compatibility: preserve acknowledged legacy checkpoint flags and startup refusal notices; agent#186: capture cap at public iterator creation; agent#180/#181/#182: stable task identities, evidence closure, accepted plan lineage and purpose-bound departure decisions; v0.11: original capture finalization and route widening before managed dispatch; v0.11: awaited yielded cleanup preserves pending-next cancellation; ADR-0029: a delegated run's tool_call and tool_result carry its principal (withPrincipal)
$pkg/context/policy.ts|$a/context/policy.ts|63dd90c9610d15d411073ede1e3cdee173d85f9817a61533308994928e3fda1c|the Go-side copy: policy.json read from the package's contract/; agent#1: readWindowBytes and encodedPrefix, the read window; agent#2: the turn clock and the tool-call deadline (TIME_DEFAULTS); agent#4: contextPolicy takes the capability record's window and output cap; agent#5: the images one request carries (IMAGE_DEFAULTS); agent#7: the test tool's bounds (TEST_DEFAULTS); agent#8: the bounds on a chat's index (INDEX_DEFAULTS); agent#11: memoryBytes reserved only while the host has not said memory is off; ADR-0005: the bound on the project's own instructions (PROMPT_DEFAULTS, projectInstructionsBytes); ADR-0006: SOURCE_DEFAULTS, the six bounds on tools with a source, in the policy; agent#69: contextPolicy takes no memory word, since the memoryBytes reserve is retired; v0.4.0 pre-release batch (#73): cataloguedTools and catalogueBytes, the catalogue's two bounds, in SOURCE_DEFAULTS (eight bounds); ADR-0009: SKILL_DEFAULTS, the four bounds of the skills budget, in the policy; ADR policy table (#116); ADR memory write side (#98); ADR proposal edits (#110); ADR context compaction (#99); ADR records (#101); carry of ADR-0016 into ADR-0018 (#126); ADR steering a run in flight (#105); carry of ADR-0017 into ADR-0021 (#129); carry of ADR-0018 into ADR-0021 (#129); agent#155: read the canonical nested table directly
$pkg/context/recovery.integration.test.ts|$a/context/recovery.integration.test.ts|7d618f6a74957ecb0c5ba0955ba7e91b9ec67a7dd8d3459cf0542e9e5862dbb6|agent#5 and agent#12: the fixture's window 17,408 tokens, not 16,384, because this harness sends fixed text the Desk does not (the search tool, the data rule) and the compacted summary must still hold the schema result's handle; agent#155: migrate retired critic controls and bare-text fixtures to canonical inputs; v0.11: explicit taxonomy on retained context fixtures
$pkg/context/session.ts|$a/context/session.ts|03ba81a541ae4af2fabdfe6f1bbf66a8588ae8db152553aa2e1e4eb0d5358f4b|agent#1: read_window reserved; agent#9: the execution guidance no longer appended to the prompt (each adapter places it), currentState; agent#4: the capability record resolved once for the run, the budget from it; agent#5: search reserved, the host's documents retained as attachments; agent#7: the per-turn test-call cap (claimTestCall); agent#12: the run's marker, frame, framed results read back in compaction and the summary framed; agent#8: the run's turn on a chat-scoped context opened with the run and closed at its end or a failed start, the index of earlier results counted against the prompt's allowance, the session tools that read kept, the critic isolated from the chat; the first message's text parts made in one place (opening), the index cut to the room left, at most INDEX_SHARE of the allowance, and made only for the chat's own turn; agent#11: the host's word on memory read into the policy; the person's message as the first text part where the prompt is previewed, a delegated child's objective as its first text part; the context inputs chat, runtime, memory and objective; agent#10: the critic's allow-list held below both engines (the critic phase, admit, on the wrapped callTool and host tools), its refusals as guardrail notices, the declared cases read closed; agent#10 review rows 5 and 6: the boundary narrowed for the critic (its readers admitted) and for the tool-free reformat run, a served but unoffered name refused before any call, the critic's retained message read with read_window or search; agent#14 and agent#15: the context input link, by which the delegation coordinator reaches a run's execution, and chat as a ChatScope, a delegated child's view of the chat among them; review of agent#35: the run's connection (context.connection), and the models it names asked once per run (models, modelsFor); agent#17: the context input delegationReuse, the host's word that a response reuses no earlier delegated result; the connection's capacity among what modelsFor is asked for; agent#43: the chat's read cache consulted only for a name the session offers; ADR-0005: the host's parts asked once per run (askHost, the person's message or a child's objective as the memory query, none where memory is off) and the system text from the one assembler (system), each request body's digest recorded before it is sent on the wrapped model and agent capabilities (recordPrompt), the context input memory's comment; ADR-0005 review: the agent request digested as the transport sends it, without the delegated mark, and an empty objective taken as none; ADR-0006: the tools with a source declared and named for the wire before the plan filter, offered after the context opens (openSources), load_tool beside the ledger's tools where a source is a catalogue, each source call held by the source gate, the Runtime's calls counted in its row, a source's result bound, load_tool reserved; agent#69: the input allowance takes no briefBytes or memoryBytes reserve; v0.4.0 pre-release batch (#73): the source gate shared with a delegated child through the link (ExecutionLink.sources), the tools the catalogue's bound withheld put on the stream as guardrails, the call id handed to a host tool's execute and kept by the source gate; ADR-0008: the context input approval (the person's decision), the run's approval for the source gate and where no act call is admitted (a child, Plan mode, the critic, the reformat run), the policy asked by an engine before an act call's tool_call (approval), a request refused while an act call waits (ApprovalPaused), the approved call's runner (runApproved), the guardrail's reason; v0.5.0 release review (#83): the guardrail's reason approval stale, the gate's own refusal of a call whose decision no longer holds for it; #82: the framing marker carried across an approval pause, the asked approval carrying the run's marker and a run restored from it taking it (carryMarker); ADR-0009: the skills declared and admitted before any request against the declared host tools, their sources reported beside the tools', the catalogue appended to the system text (system), the skill gate opened with its guardrails and the restore (openSkills), load_skill and unload_skill beside the context tools and reserved, load_skill's answer never cut to the result bound, the skill gate told whether the critic runs (#93), the guardrail's reasons skill withheld and skill budget; ADR-0010: the critic's switch held for the run (review, decideCritic, criticRoom), request_review offered under auto, reserved and refused outside the author's phase with the guardrail review refused; ADR policy table (#116); ADR policy table review round (#116); ADR cross-provider (#117); ADR cross-provider review round two (#117); ADR sub-agents (#97); ADR sub-agents review (#97): reconcile changed definition roles before saved-policy comparison; ADR memory write side (#98); ADR proposal edits (#110); #110 review: schema-valid malformed edit refusals; ADR context compaction (#99); ADR the output boundary (#100); carry of ADR-0016 into ADR-0017 (#124); ADR records (#101); carry of ADR-0016 into ADR-0018 (#126); carry of ADR-0017 into ADR-0018 (#126); ADR identity per tool call (#102); ADR-0019 fix round (#127): persisted identity pin, withholding and per-source approval history; carry of ADR-0017 into ADR-0019 (#127); carry of ADR-0018 into ADR-0019 (#127); ADR provider failover and the usage ledger (#104); carry of ADR-0018 into ADR-0020 (#130); carry of ADR-0019 into ADR-0020 (#130); agent#104 fix round: persisted failover, delegated bindings and usage, wire token presence and exhaustion refusal; agent#133: rebuild fallback model settings and guard compaction summaries before projection and hashing; agent#136: the Gemini model path is percent-encoded per RFC 3986 in both languages; agent#142: omitted reasoning stays omitted; source provenance; agent#142 release audit: table defaults follow discovery and compactor omission; agent#142 second release audit: dropped table defaults stay omitted downstream; request-read provenance; agent#142 review round 2: structural request reasoning boundary; agent#162: opaque delegated-role reasoning and actual discovery handoff, fresh child admission consistency and original provenance; agent#154: checkpoint writes use the canonical mutation boundary and detached controller reads; agent#155/#173: refuse retired inputs and reconcile lifecycle metadata atomically before prompt publication; agent#164: admitted request values and boundary-shaped fixtures; agent#186: capture trusted requestCap before setup, reject latched startup helpers and require a persistable capped claim before capability handoff; ADR-0024: versioned records and frozen historical structural readers (#165); agent#159/#179: scoped acknowledged dispatch and artifact provenance; verified bounded model-response quarantine and root signal forwarding; agent#159 callback compatibility: retain legacy checkpoint identity without full journal authority; agent#186: copy cap before asynchronous storage admission; agent#180/#181/#182: stable task identities, evidence closure, accepted plan lineage and purpose-bound departure decisions; v0.11: usage reservation, original capture binding, task admission, route and Runtime action ownership; original HTTP capture correlates retained tool calls and results using writer enrollment; v0.11: generated offer taxonomy annotations; agent#237: native Plan attempts retain original release ownership and publish acknowledged decision state; agent#242: an external read's receipt check, found by the call's deadline signal, handed to the read wrapper and the durable host execution, so nothing of an unverified result is recorded or retained; agent#223: the durable host execution records a call as a read by its effect (durableReadingTool), so a source act reaches the effect host; agent#246: a context.runtime with a lone UTF-16 surrogate refused at admission and the admitted Runtime copied; ADR-0029: the context input principal opened before any offer, a child under its parent's, the checkpoint's principal and principal-changed on resume, the delegation refusal's guardrail
$pkg/context/toolPolicy.ts|$a/context/toolPolicy.ts|182e5189eb6c60f0bc986768966eb2f936b737d63e4ffe5499b782e3e67cd13a|agent#8: runtime, a result that depends on the Runtime serving it, and what immutable means under a chat-scoped context
$pkg/context/toolPolicy.test.ts|$a/context/toolPolicy.test.ts|-|-
$pkg/delegation.ts|$a/delegation.ts|d88ac7a1a8d544c0ac71f434e58880507873f00edd57c0ce2573f1d16287ac6d|agent#14: the delegate call runs the batch, awaited in the parent's step on vercel, and on codex the response's next run given the call's result and nothing else; more than one batch, one at a time; the parent's execution owning the shared request count; a finished child resumed with its context; agent#15: context by reference through a view of the parent's chat, each profile's model, budget, time limit and Runtime tools, a child's test calls charged to its parent's turn; agent#13: long findings kept whole with their head and a read_window cursor, the summarize step, the tool-call cap stated in the state; agent#7 and agent#11 as before: the turn's test calls shared, a child under its parent's word on memory, its objective its request text (context.objective); review of agent#35: the delegate tool offered on an agent capability only while a child run and the parent's next run remain, and the limit stated; concurrent children's grants; why a child stopped and its failure, its own time limit winning a tie; an unoffered name refused before it counts; each answer task's objective and references; the codex continuation framed as one data block; the child model from the host's modelsFor answer for the connection; review of agent#35 after main's agent#10: a codex child's run carries the child model on the agent request; agent#16: each child's report read in its profile's shape or written by the coordinator, what its run asks of the parent returned as open questions, one more batch after them, its prose streamed under its task id; agent#17: a partial result at the time limit, one retry before the first byte, reuse of an identical task; agent#18: each child's tokens, time to first byte and duration, and the figures in the parent's checkpoint; the review of agent#39: reuse only of a child with no counted tool call, keyed with its model and kept once the batch's answer is delivered, identical tasks of a batch run once, delegationReuse; the running batch in the checkpoint's figures and the parent's own requests and runs counted where it makes them; a codex child's runs counted where admitted; the batch's concurrency from the connection's modelsFor answer; the research hand-back as an open question with a partial result; a failed run's open questions kept on its record; the child retry held to a 5xx other than 504; earlierWork's partial; the coordinator's open questions kept in the text at more; ADR-0003: the coordinator wraps the deepagents engine as it wraps the other two; agent#57: the child’s offer drawn by one function, childOffer, extracted so that the gate test drives the offer the child is given; v0.4.0 pre-release batch (#73): a child's link hands it the parent's source gate, the parent's checkpoint after a batch carries the children's source rows, the call id passed through the wrapped execute; ADR-0008: on an agent capability an act offered to a parent run only while the run that continues after its pause remains (mayAct); agent#71: modelRequests absent from an agent capability's figures (the parent's, each child's), the total from figuresTotal, which counts model requests only; #82: the zero modelRequests a v0.4.0 agent checkpoint wrote on its restored batches dropped; ADR-0009: a delegated child handed no skills; ADR-0010: on an agent capability one run held back for the critic under auto and on (mayDelegate, admitBatch, a child never taking it) and the critic's room given to the parent's context; ADR policy table (#116); ADR policy table review round (#116); ADR cross-provider (#117); ADR sub-agents (#97); ADR sub-agents review (#97): shared report minimum and bounded fallback; changed-definition explanation; ADR the output boundary (#100); agent#100 fix round: guard notice fields and carry live child output counts into closed-parent checkpoints; review of #129: a delegated child receives a fresh empty steering queue and cannot drain the parent’s queued input; agent#104 fix round: persisted failover, delegated bindings and usage, wire token presence and exhaustion refusal; agent#142 review round 1: omitted reasoning stays omitted in model, critic, fallback and delegated paths; agent#162: construct every child from admitted role reasoning rather than raw parent reasoning; agent#154: checkpoint writes use the canonical mutation boundary and detached controller reads; agent#155: delegated children use canonical critic off; agent#186: initialize the final fixed API child request cap before context admission; agent#158: acknowledged durable handoff stages and explicit host provider-release gating; agent#159/#158 callback compatibility: preserve legacy batch refusal continuation and interrupt historical queued figures; agent#186: capture cap at delegated iterator creation; integrated storage fix: closed-parent legacy continuation retains merged child output counts; v0.11: task-step selection, bounded native children and taxonomy-aware child admission; agent#237: compose native release owners and retain coordinator child reserve; agent#248: every returned parent invocation hands snapshot ownership back to the acknowledged root, so the final end keeps the retained chat scope, including a failed parent's and when its release refuses
$pkg/delegation.test.ts|$a/delegation.test.ts|39d906d0ad570267e6a09e8e180457ce6f734bcd6bf0749149560adb2b26b3da|test kept in the Desk: the case over the Desk's response history; the Desk's chat-history reader replaced by a fold of delegated_task events with its interruption rule; agent#14: the cases rewritten for one parent run (findings as the call's result; on codex the next run given the result), and new cases for one run with native tool messages, a second batch, the batch bound, resume, and codex's next run reading by reference; agent#15: the request with no copied context, and new cases for references, the profiles, the child model, six page reads and the author-test child; agent#13: long findings, the summarize step and the stated cap; review of agent#35: the cases for the codex agent-run limit, concurrent children's grants, the framed codex continuation with objectives, why a child stopped, unoffered names, each profile's offer, references one call each, readProfiles through withDelegation, resume under another profile, a child's prompt without the chat's index, and the child model from modelsFor; the codex fake of the earlier-reads case tells a child by its brief; a codex child on the host's child model through the agent transport; agent#17: each task of the helper its own objective, and each batch of the codex batch-limit case its own task, so no task is an earlier one reused; the review of agent#39 row 7: the research hand-back case asserts a complete, partial task with an open question; agent#43: a child is refused the parent's cached read of a name its profile was not offered; the model a task ran on under each connection; agent#44 review: the Codex variant of the cached-read refusal, an unoffered get_schema asked for through the agent’s tool callback; ADR-0008: a Codex parent offered an act with a source only while the run that continues after its pause remains; ADR-0010 pre-release batch (#111): the response's fifth agent run leaves no room for the critic and records budget; agent#100 fix round: guard notice fields and carry live child output counts into closed-parent checkpoints; review of #129: a delegated child receives a fresh empty steering queue and cannot drain the parent’s queued input; agent#142: an omitted reasoning preference stays omitted in the child policy; agent#162: request-boundary omission, value/provenance, profile, declared-subagent, resumed-task and admission mismatch regressions; agent#155: migrate retired critic controls and bare-text fixtures to canonical inputs; agent#164: admitted request values and boundary-shaped fixtures; agent#186: assert preinitialized child caps and no child admission with zero available requests; ADR-0024: explicit current event header expectations (#165); agent#180/#181/#182: completion tool offers, progress-only updates and provider pause controls; v0.11: current gate, task and taxonomy declaration fixtures
$pkg/delegationContract.ts|$a/delegationContract.ts|3a37e9096f7cefb6f667f41d47957908e05a2df6204f58e721f51859b21575f8|agent#14, agent#15 and agent#13: references, resume and summarize in the request and no copied context; the task record's budget, model, references, resumes, more and summarized; each profile's model, budget and Runtime tools, the author-test profile, readProfiles; MAX_TOOLS and MAX_BATCHES; the instructions and the tool's description; review of agent#35: MAX_AGENT_RUNS, AGENT_DELEGATION_INSTRUCTIONS and DELEGATION_SPENT; agent#16: the report shapes, readReport and reportInstructions, the profile's report; agent#16 to agent#18: the record's report, partial, reused, retried, tokens, firstByteMs and durationMs; the instructions on reports, open questions, partial results and reuse; ADR sub-agents (#97); v0.11: current task-step and declared child request contracts
$pkg/engine.ts|$a/engine.ts|5a4efac1dee104be57eb372fcd11089cc7be1ff182be0bb54228b865a00d3b99|injection points: Language, AssistantEngine, EndpointKind (types); agent#6: the proposal's note (ProposalNote); agent#4: a session may carry members of its endpoint's capability record; agent#5: a result's content item is open (image parts); agent#10: the declared cases (criticCases), the critique's comparisons and figures, the note's figures, the default's wording; agent#10 review rows 1 and 8: the critique's and the proposal's incomplete cases, the harness's calls marked by origin; agent#16: message_progress's taskId; ADR-0006: HostTool's source, effectClass, approval and approve, McpToolResult's receipt, and the source record and proposed on the tool_call and tool_result events; v0.4.0 pre-release batch (#73): the approval reader handed the call (ApprovalRequest: name, tool, source, args), execute handed the id of the call's events; ADR-0008: approve the host's approval policy (allow, refuse, ask), the approval_requested event, the guardrail's reason; v0.5.0 release review (#83): the guardrail's reason approval stale, the gate's own refusal of a call whose decision no longer holds for it; ADR-0009: the session's skills, the skill_loaded and skill_unloaded events, the guardrail's reasons skill withheld and skill budget; ADR-0010: the session's critic, criticProject, criticReasoning, existingPack, reviewerAssigned and the alias adversarialReview, the critic_decided event, the guardrail's reason review refused; ADR policy table (#116); ADR cross-provider (#117); ADR sub-agents (#97); ADR memory write side (#98); ADR proposal edits (#110); #110 review: optional document on malformed edit refusals; ADR context compaction (#99); ADR the output boundary (#100); carry of ADR-0016 into ADR-0017 (#124); ADR records (#101); carry of ADR-0016 into ADR-0018 (#126); carry of ADR-0017 into ADR-0018 (#126); ADR identity per tool call (#102); carry of ADR-0017 into ADR-0019 (#127); carry of ADR-0018 into ADR-0019 (#127); ADR steering a run in flight (#105); carry of ADR-0017 into ADR-0021 (#129); carry of ADR-0018 into ADR-0021 (#129); carry of ADR-0019 into ADR-0021 (#129); ADR-0010: the session's critic, criticProject, criticReasoning, existingPack and reviewerAssigned, adversarialReview an alias, the critic_decided event, the guardrail's reason review refused; ADR provider failover and the usage ledger (#104); carry of ADR-0018 into ADR-0020 (#130); carry of ADR-0019 into ADR-0020 (#130); carry of ADR-0021 into ADR-0020 (#130); ADR the evaluation corpus (#103); carry of ADR-0021 into ADR-0022 (#128); carry of ADR-0020 into ADR-0022 (#128); agent#142 review round 1: omitted reasoning stays omitted in model, critic, fallback and delegated paths; agent#142 review round 2: structural request reasoning boundary; agent#155: document retired adversarialReview refusal; agent#156: session skills comment distinguishes ad hoc children from declared subagents;agent#164: admitted request values and boundary-shaped fixtures; agent#164: admitted request values and boundary-shaped fixtures; ADR-0024: versioned records and frozen historical structural readers (#165); agent#156: session skills comment distinguishes ad hoc children from declared subagents; agent#159/#158: host-authenticated artifact observations and handoff call identity; agent#180/#181/#182: stable task identities, evidence closure, accepted plan lineage and purpose-bound departure decisions; v0.11: typed taxonomy, route, usage-budget and Runtime action session inputs; v0.11: named host-owned corpus output; ADR-0029: event 3, the principal on tool_call and tool_result, the guardrail's reason delegation refused with its refusal and principal
$pkg/engines/codex/codex.test.ts|$a/engines/codex/codex.test.ts|e1f6c171c9f3417642b434934d8069ca03b8fe47ef0c398c1e3d7e6caefd08d5|paths: conformance/ is certification/, testing/question reads the contract vector; test kept in the Desk: the case over the Desk's dialect chat tools; agent#1: the reference envelope names read_window; agent#6: the whole answer kept, the critic's note, propose_pack; agent#12: read_context's answer read through its data block; agent#25: K2 against the contract's scenario offer and the critic's, K3b through the gate (T7 write_file), an unoffered call answered refused; agent#10: the critic asked for, offered its allow-list, a seeded wrong outcome refuted; agent#29: one reformat run before the whole answer is kept; agent#10 review rows 5 and 6: the critic offered the harness's two readers, a served but unoffered name refused before any call; agent#36: the state and guidance headed by STATE_HEADER; agent#42: the previewed prompt leads with the person's message; ADR-0005: the two cases rewritten for the layered order, the host's parts at the end of the instructions and the request text before the state, without the recall rule; ADR-0005 review: the project-instructions leg on the agent wire; ADR-0010: the offer under on and off without request_review, the decision in the event order; ADR policy table (#116); ADR memory write side (#98); ADR-0014 review: alias uses the record frame; ADR proposal edits (#110); carry of ADR-0017 into ADR-0020 (#130); carry of ADR-0018 into ADR-0020 (#130); carry of ADR-0019 into ADR-0020 (#130); carry of ADR-0021 into ADR-0020 (#130); ADR-0022 proposal digest review fix (#128); carry of ADR-0020 into ADR-0022 (#128); agent#142: omitted reasoning stays omitted; source provenance; agent#155: migrate retired critic controls and bare-text fixtures to canonical inputs;agent#198: native proposal fixtures use documentJson; ADR-0024: explicit current event header expectations (#165); agent#155: migrate retired critic controls and bare-text fixtures to canonical inputs; agent#180/#181/#182: completion tool offers, progress-only updates and provider pause controls; v0.11: explicit current Runtime replay metadata and host taxonomy fixtures; v0.11: explicit read/proposal taxonomy
$pkg/engines/codex/index.ts|$a/engines/codex/index.ts|f8d0a3470e399fe67a028bfd5c60ec597d35a3f5c63cbadb9ba76a7a59a8e41d|injection points: assistantLanguageInstructions; agent#9: the agent prompt carries currentState and the execution guidance itself; agent#6: the whole answer kept, the critic's note, propose_pack; agent#2: the tool-call deadline; agent#5: an image in a result reaches the agent as the host's text rendering; agent#12: a reading tool's answer framed, the data rule in the instructions; agent#8: a chat's index after the request text in the agent prompt, in the room the allowance leaves (opening); agent#10: the critic's default from the policy table, its allow-list offered and admitted, the declared cases compared and the rest rehearsed, its model and figures; agent#25: an unoffered call put through the gate and refused; agent#29: one tool-free reformat run (reformatPrompt); agent#10 review rows 5, 6, 8 and 10: a served but unoffered name refused before any call, the reformat run narrowed, the critic offered the two readers, the harness's calls marked, counted and inside the critic's phase and clock, the critic model asked once; review of agent#35: the critic's model read from the run's modelsFor answer; agent#36: the state and guidance appended at the prompt's end headed by STATE_HEADER, which is imported from engines/contract.ts; ADR-0005: the instructions from the one assembler with the host's parts (context.system), and the author run's prompt as the model wires' first user turn: the request text or the person's message leading the preview (opening), the index, then the state; v0.4.0 pre-release batch (#73): the call id handed to a host tool's execute; ADR-0008: an act call the host's policy asks about ends the run as a delegation handoff does, approval_requested in its call's place; ADR-0010: the decision emitted where the critic would start or the run ends with no proposal, the critic run's effort the decision's tier; ADR policy table (#116); ADR cross-provider (#117); ADR sub-agents (#97); ADR proposal edits (#110); #110 review: complete draft set on failure and waiting; ADR steering a run in flight (#105); ADR provider failover and the usage ledger (#104); carry of ADR-0018 into ADR-0020 (#130); carry of ADR-0019 into ADR-0020 (#130); carry of ADR-0021 into ADR-0020 (#130); agent#142: omitted reasoning stays omitted; source provenance; agent#142 second release audit: dropped table defaults stay omitted downstream; request-read provenance; agent#142 review round 2: structural request reasoning boundary;agent#164: admitted request values and boundary-shaped fixtures; agent#198: proposal source numeric preservation, native documentJson migration and terminal source refusal recovery; agent#164: admitted request values and boundary-shaped fixtures; agent#142 review round 2: structural request reasoning boundary; agent#158: retain the host invocation call identity on delegation intent; agent#180/#181/#182: stable task identities, evidence closure, accepted plan lineage and purpose-bound departure decisions; v0.11: original private alias bindings and phase-specific native tool offers; agent#237: mark the native Plan pause before aborting for a decision
$pkg/engines/contract.ts|$a/engines/contract.ts|42d847c2a15930cfa770411181c215334c0fdde2f8e9ae9ae5ba21841391ca51|injection points: EndpointKind (type); agent#6: why a proposal was not read, the whole answer, the reformat request, propose_pack; agent#2: withDeadline, toolTimeout, limitText; agent#4: the schema shown by the record's tool-schema subset, the text tool call read by the proposal fence's closed reader, the tools described in text; agent#25: the unoffered call's refusal shared by both engines (refusedUnoffered); agent#10 review row 5: the refusal text shared (unofferedRefusal); agent#16: reportBlocks, a child's report block read by the proposal fence's reader; agent#36: STATE_HEADER, defined here for both adapters; v0.3.0 release review: the refusal text ends its detail in one period; agent#61: a detail's own trailing period is kept (an ellipsis keeps all three) and one is added only where it has none; ADR-0005: STATE_HEADER's comment names its layer, no host reading a request;ADR proposal edits (#110); agent#198: proposal source numeric preservation, native documentJson migration and terminal source refusal recovery; ADR proposal edits (#110); ADR-0005: STATE_HEADER's comment names its layer, no host reading a request; v0.11: current cancellation and boundary-owned tool handling
$pkg/engines/contract.test.ts|$a/engines/contract.test.ts|6687ab6ddde0564839ebffcb96ea2b6bdef091cb0beec0b5ca17787865bcfb05|v0.11: explicit taxonomy on the current host tool fixture
$pkg/engines/index.ts|$a/engines/index.ts|1b683f792073e75463e5ee944d71226fcabfff6033f4c09be80725470c4f885a|injection points: AssistantEngine (type); ADR-0003: the deepagents engine registered, a model engine
$pkg/engines/vercel/channel.ts|$a/engines/vercel/channel.ts|-|-
$pkg/engines/vercel/completion.test.ts|$a/engines/vercel/completion.test.ts|2e7e9e1c764392417a13bfe16456306ac2d0aaff0e5a73fedc4ae255b00e5aee|agent#155: completion fixtures use canonical critic off; ADR-0024: explicit current event header expectations (#165); v0.11: synthetic workflow taxonomy and admitted mutable Runtime fixture
$pkg/engines/vercel/engine.test.ts|$a/engines/vercel/engine.test.ts|e30fea06beeb5a8e14596453029ef837a9f4d945b47382db52b828df00569271|injection points: ASSISTANT_ENGINES, ASSISTANT_TOOLS; paths: LANGUAGES from the test host, testing/question reads the contract vector; agent#1: read_window in the offered tool lists; agent#6: the reformat request and the whole answer, the critic's note, propose_pack; agent#5: search in the offered tool lists; agent#10: the critic asked for, not started by the tier; agent#37: the run's own iterator closed with done, and the unoffered call not dispatched once the consumer has stopped; agent#38: the provider options under deskEndpoint; ADR-0010: request_review in the offer under the table's auto, the decision in the event order; ADR policy table (#116); ADR memory write side (#98); ADR proposal edits (#110); ADR records (#101); ADR provider failover and the usage ledger (#104); carry of ADR-0018 into ADR-0020 (#130); ADR-0022 proposal digest review fix (#128); carry of ADR-0020 into ADR-0022 (#128); agent#155: migrate retired critic controls and bare-text fixtures to canonical inputs;agent#198: native proposal fixtures use documentJson; ADR-0024: explicit current event header expectations (#165); agent#155: migrate retired critic controls and bare-text fixtures to canonical inputs; agent#180/#181/#182: completion tool offers, progress-only updates and provider pause controls; v0.11: explicit taxonomy on source and host tool fixtures; v0.11: schema and ceiling refusals, generated hints, and cancellation tests; v0.11: cancellation no-dispatch assertion uses an actual spy
$pkg/engines/vercel/index.ts|$a/engines/vercel/index.ts|-|-
$pkg/engines/vercel/loop.ts|$a/engines/vercel/loop.ts|058d0dc118b1d624ee9d5f80eaeef5f4ed5c5e1ac7e55510a9ae54179577f100|injection points: assistantLanguageInstructions, AssistantEngine and EndpointKind (types); agent#9: a stable prefix with the changing state placed by endpoint (the person's message on the first request, then a trailing user message or the system tail), the critic's note headed, Anthropic cache breakpoints, the tier held for the run; agent#6: the reformat request, the whole answer kept, the critic's note, propose_pack; agent#2: the model-request retry, the turn clock, the tool-call deadline; agent#4: the capability record read wherever the loop decided by family (thinking, schema subset, native or text tool calls, one call per turn, streaming quirks, the state placement), the text tool path in the loop and the critic; agent#5: images routed by vision (shown on the text path at a strict turn order), beside their results, the newest held to the per-request image bounds; agent#12: a reading tool's answer framed through toModelOutput and in the text path's result message, the data rule in the instructions and the critic's; agent#8 and agent#11: the first message's text parts from the session context (opening: the request text first, a chat's index last); agent#10: the critic's default from the policy table, its allow-list, its unoffered calls refused, the declared cases compared and the rest rehearsed, its model and figures; agent#25: the unoffered call's refusal shared with codex; agent#10 review rows 6 and 8: the critic offered the two readers and its first message read inside the pass, the harness's calls marked and counted; agent#14: delegate_tasks an awaited call in the parent's step, reported as a call and a result, with no tool-call deadline; review of agent#35: the critic's model read from the run's modelsFor answer; agent#36: STATE_HEADER imported from engines/contract.ts and re-exported; agent#37: runVercelLoop exported for its test; agent#38: provider options under the key the SDK names, ENDPOINT_OPTIONS; ADR-0005: the system text and the critic's from the one assembler with the host's parts (context.system), STATE_HEADER's comment without a host reading requests; agent#69: the third Anthropic cache breakpoint, on the first text part of the first user turn (cachedOpening); v0.4.0 pre-release batch (#73, #76): the call id handed to a host tool's execute; the third Anthropic cache breakpoint placed under trailing-user only (cachedOpening takes the placement); ADR-0008: an act call the host's policy asks about held, approval_requested in its call's place, never run; review of #79: every act call given an id, so its approval decision is its own; ADR-0009: the loaded skills' block in the critic's step note; ADR-0010: the decision emitted where the critic would start or the run ends with no proposal, the critic's tier in a thinking slot of its own; ADR policy table (#116); ADR cross-provider (#117); ADR cross-provider review round two (#117); ADR proposal edits (#110); #110 review: complete draft set on failure and waiting; ADR steering a run in flight (#105); ADR provider failover and the usage ledger (#104); carry of ADR-0021 into ADR-0020 (#130); agent#104 fix round: persisted failover, delegated bindings and usage, wire token presence and exhaustion refusal; agent#142 review round 1: omitted reasoning stays omitted in model, critic, fallback and delegated paths; agent#142 review round 2: structural request reasoning boundary; agent#154: checkpoint writes use the canonical mutation boundary and detached controller reads; agent#164: admitted request values and boundary-shaped fixtures; agent#198: proposal source numeric preservation, native documentJson migration and terminal source refusal recovery; v0.11: registered task delegation and current native-versus-model continuation handling; original HTTP capture correlates retained tool calls and results using writer enrollment
$pkg/engines/vercel/relay.ts|$a/engines/vercel/relay.ts|789564820a5cd98b08fa5695d411fb0e7a62da65b8f3030408af8b6cf88dee7d|injection points: EndpointKind (type); agent#4: the pinned reasoning member read and carried back, a whole answer asked of an endpoint that answers whole; agent#3: a whole answer that broke while read, or is not JSON, reported by class
$pkg/execution/checkArtifact.ts|$a/execution/checkArtifact.ts|-|-
$pkg/execution/checkArtifact.test.ts|$a/execution/checkArtifact.test.ts|-|-
$pkg/execution/controller.ts|$a/execution/controller.ts|bf35b15ed460f753119d15209cf829c10ed31556868310740f6802e3e7485c45|agent#9: requestState, the part of the guidance that changes between requests; agent#2: a refused attempt's row in the completion record and the attempt number per call (recordRefusal, beginAttempt); agent#8: wrapRead keeps a session read's answer in a chat-scoped context and serves an immutable one from the chat's read cache; agent#13: the tool-call cap in the request state (toolAllowance); agent#15: a delegated child's first allowance (limitTo); agent#43: the read cache serves only a name the session offers (kept.offered); ADR-0005: each request body's digest recorded before it is sent (recordPrompt); ADR-0006: the schemas load_tool loaded stated after the request state (loadedSchemas); ADR-0008: a paused approval continued only with the person's decision for its call, the decision written (approved or refused), a restored approved call answered with an unknown outcome, the approval asked (approvalAsked) and answered once the continuation request is sent (recordPrompt); review of #79: a decision on an approval already decided refused (stale-state); ADR-0009: the loaded skills' block stated last in the request state (loadedSkills); ADR proposal edits (#110); ADR records (#101); ADR steering a run in flight (#105); carry of ADR-0018 into ADR-0021 (#129); ADR provider failover and the usage ledger (#104); carry of ADR-0018 into ADR-0020 (#130); carry of ADR-0021 into ADR-0020 (#130); agent#104 fix round: persisted failover, delegated bindings and usage, wire token presence and exhaustion refusal; agent#154: checkpoint writes use the canonical mutation boundary and detached controller reads; agent#186: fixed request-cap identity, monotone usage and permanent invalid-helper refusal across claim and snapshot; ADR-0024: versioned records and frozen historical structural readers (#165); agent#159: carry durable journal state across checkpoint projection; agent#180/#181/#182: stable task identities, evidence closure, accepted plan lineage and purpose-bound departure decisions; agent#182: publish restored plan decisions through the canonical checkpoint mutation API; v0.11: current usage, task, route, repeat and canonical checkpoint ownership; agent#242: wrapRead retains or caches an external read's answer only once its receipt check verifies (admission); agent#246: arguments with a lone UTF-16 surrogate kept in neither read cache
$pkg/execution/correctArtifact.ts|$a/execution/correctArtifact.ts|-|-
$pkg/execution/correctArtifact.test.ts|$a/execution/correctArtifact.test.ts|0ca060d35437f0dd7e4a2713a0b42dcde8cce2e4a64815844135a6c74aa3bf72|agent#154: checkpoint writes use the canonical mutation boundary and detached controller reads; agent#164: admitted request values and boundary-shaped fixtures
$pkg/execution/execution.test.ts|$a/execution/execution.test.ts|8eef1b842cd11ea7161f8b63af9fa81452afb650dc71ed2aae376106eb61332b|test kept in the Desk: the case over the Desk's chat checkpoint; ADR policy table (#116); ADR provider failover and the usage ledger (#104); agent#104 fix round: persisted failover, delegated bindings and usage, wire token presence and exhaustion refusal; agent#154: checkpoint writes use the canonical mutation boundary and detached controller reads; agent#164: admitted request values and boundary-shaped fixtures; agent#180/#181/#182: completion tool offers, progress-only updates and provider pause controls; v0.11: explicit taxonomy on current execution fixtures; v0.11: explicit Runtime metadata, exported uncached workflow, and unoffered refusal; v0.11: unoffered Runtime refusal exercises the real transport gate while preserving cache and workflow counts
$pkg/execution/progress.ts|$a/execution/progress.ts|-|-
$pkg/execution/progress.test.ts|$a/execution/progress.test.ts|-|-
$pkg/execution/state.ts|$a/execution/state.ts|85d99bcfff1b1e8a9fd00982a818fbf091fe8fb96918c6daa650f7a41cd7ff20|agent#2: the completion record's attempt, failure and waitMs (CompletionRecord, readExecution); agent#7: the turn's test-call count (testCalls) carried in the checkpoint; agent#18: the delegation figures (DelegationFigures, validDelegationFigures) carried in the checkpoint; ADR-0005: the prompt digests carried in the checkpoint and read closed (PromptDigest, MAX_PROMPT_DIGESTS); ADR-0006: the checkpoint's sources, the run report by source, read closed (validSourceFigures); ADR-0008: the checkpoint's approval read closed (ApprovalRecord, validApproval), the decision's reader (readApprovalDecision), the refused result and the unknown outcome; agent#71: modelRequests optional on the figures, validDelegationFigures read by the checkpoint's unit; #82: the approval record's marker, read closed as 128 bits of hex; ADR-0009: the checkpoint's loaded skills read closed (SkillLoad, validSkillLoads), the source kind exchange, a source row's skills; ADR-0010: the checkpoint's critic, read closed; ADR policy table (#116); ADR cross-provider (#117); ADR sub-agents (#97); ADR memory write side (#98); ADR proposal edits (#110); ADR context compaction (#99); ADR the output boundary (#100); carry of ADR-0016 into ADR-0017 (#124); ADR records (#101); carry of ADR-0016 into ADR-0018 (#126); carry of ADR-0017 into ADR-0018 (#126); ADR identity per tool call (#102); ADR-0019 fix round (#127): persisted identity pin, withholding and per-source approval history; carry of ADR-0017 into ADR-0019 (#127); carry of ADR-0018 into ADR-0019 (#127); ADR steering a run in flight (#105); carry of ADR-0017 into ADR-0021 (#129); carry of ADR-0018 into ADR-0021 (#129); carry of ADR-0019 into ADR-0021 (#129); carry of ADR-0017 into ADR-0020 (#130); carry of ADR-0018 into ADR-0020 (#130); carry of ADR-0019 into ADR-0020 (#130); carry of ADR-0021 into ADR-0020 (#130); agent#104 fix round: persisted failover, delegated bindings and usage, wire token presence and exhaustion refusal; agent#142: omitted reasoning stays omitted; source provenance; agent#154: checkpoint writes use the canonical mutation boundary and detached controller reads; agent#186: read the optional persisted fixed API cap, validate its bounds and no-grant shape, and refuse extension; ADR-0024: versioned records and frozen historical structural readers (#165); agent#159: validate and retain the optional durable dispatch journal; agent#180/#181/#182: stable task identities, evidence closure, accepted plan lineage and purpose-bound departure decisions; agent#159: optional validated retained valueJSON representation; v0.11: version-three task, Runtime, usage-budget, route and repeat checkpoint members; agent#237: validate native Plan receipts and coordinator identity in checkpoint three; ADR-0029: checkpoint 4 carries exactly its principal, read closed; version-three features read at version three or four
$pkg/failures.ts|$a/failures.ts|b8deb6c05e890f1642f210dc36906267f2014a8b02586734cd0826950bd67a69|the Go-side copy: catalog.json read from the package's contract/; agent#2: the retried refusals by name (canRetryRefusal, refusalCode)
$pkg/failures.test.ts|$a/failures.test.ts|-|-
$pkg/failures.integration.test.ts|$a/failures.integration.test.ts|c2404828c1d28d6f6a31b8bf241d667ec052d210a11715f7591401ee606a4e02|agent#2: Retry-After 120, past the retry wait limit, so the refusal is still reported after one request; ADR the output boundary (#100)
$pkg/geminiSchema.ts|$a/geminiSchema.ts|-|-
$pkg/geminiSchema.test.ts|$a/geminiSchema.test.ts|8e409a5b5b90560de63bbeee2565a1d698d0a271e471e31b4bfa1e5abaed03ee|paths: conformance/ is certification/
$pkg/memory/client.ts|$a/memory/client.ts|447add57ae764bb05abe84cdaf49c22420041d6b938c52e238c3b22beec07293|injection points: answer, deskFetch
$pkg/memory/memory.test.ts|$a/memory/memory.test.tsx|86fa73104725e7beb66e2b68240f77135c9db0066288db093d36e546e2ff4634|tests kept in the Desk: the two cases over the Desk's memory settings screen
$pkg/memory/tools.ts|$a/memory/tools.ts|87d8f3649e94ced5a702294242880d6f3842e438824de5d3e90cb686e2ed70ff|v0.11: read and own-state taxonomy on package memory tools
$pkg/planning/model.ts|$a/planning/model.ts|6ad60517770f7b249ebfb5673a56501033c07613582829c9f9327a27c748b48d|agent#179: current opaque digest references with explicit historical plan-reference readers
$pkg/planning/planning.test.ts|$a/planning/planning.test.tsx|1250de0fe011e72112af7727a1fdcb07e92c1868b6eb7eba29bb44b43201e129|tests kept in the Desk: the three cases over the Desk's chat store and plan card; agent#1: read_window in the host tool list; agent#5: search in the host tool list; agent#13: the two cases over Plan mode's Runtime list rewritten, the list being the gate's reads; ADR-0010 pre-release batch (#111): the Plan mode case preserves the inherited boolean alias; ADR policy table (#116); ADR memory write side (#98); ADR provider failover and the usage ledger (#104); agent#155: migrate retired critic controls and bare-text fixtures to canonical inputs; agent#164: admitted request values and boundary-shaped fixtures; agent#180/#181/#182: completion tool offers, progress-only updates and provider pause controls; v0.11: explicit Runtime metadata and host taxonomy on phase offer fixtures
$pkg/planning/policy.ts|$a/planning/policy.ts|eb906233f116c4bee6263a25ecdc5608115279dd14a271165939758c8a7a7750|agent#13: Plan mode's Runtime reads derived from the gate's ceiling, less the evaluation and the test run; ADR-0010 pre-release batch (#111): the planning session no longer sets the boolean alias; agent#179: canonical bytes for the pinned historical plan
$pkg/planning/tools.ts|$a/planning/tools.ts|8eab06b1e82f104a668bff08e6f6b6416de66261fa20f0957e1bf5d4e45389f0|injection points: Chat and ChatStore (types), readFile, dialectAPI, PreparationReply (type); agent#179: host-resolved opaque references and asynchronous revision-checked chat plan updates; v0.11: proposal taxonomy on the package plan tool
$pkg/questionFailure.ts|$a/questionFailure.ts|dba6a633523a01fc9499eaafd49a743b7c0c85fad7b7067309f114425f9c5da0|injection points: sourceMessage
$pkg/questions.ts|$a/questions.ts|65d267a29dd648316fc8198686fc13aacda730a8b3fadbc44f0b17ef2031659f|v0.11: ask taxonomy on the package question tool; agent#265 (#264 review M4): option labels compared lowercased by toLowerCase, not by the locale (toLocaleLowerCase), so the fold is the same in every locale and in Go
$pkg/refutation.ts|$a/refutation.ts|2a18ea8380e44d84097d073fef1c7dc2ddf8c7fba52dabd148164f9d877f4b95|injection points: sourceMessage; agent#6: proposalNote, criticNote; agent#10: the critic's allow-list (CRITIC_TOOLS, criticRefusal), the default (criticRuns), the declared cases and the outcome comparison (readCriticCases, compareTheRest), the figures; agent#10 review rows 1, 2, 3 and 6: an uncompared case unsettled (incomplete), whole dispositions only to the test tool, the sets compared in any order, the critic's readers; ADR-0009: the critic may call the skill loaders (criticMayRead); ADR-0010: criticRuns removed, the decision in criticSwitch.ts; ADR cross-provider (#117)
$pkg/refutation.test.ts|$a/refutation.test.ts|-|-
$pkg/session.ts|$a/session.ts|63163ed323f057cef1efc3195fa98386a0cc811d0d6e3b473d26ab4813cecd21|injection points: aiHeaders, AITarget, deskHeaders, currentLanguage, chassisUrl, DeskWebSocketTransport, socketProtocols, socketURL, the session store (NO_SESSION_MESSAGE, NoSession, discardBody, forgetSession, refusalCode, sessionBearer, sessionEnded, whenSessionEnds), EndpointKind (type); agent#100 fix round 2: guard gate notice text before host delivery; v0.11: current paginated Runtime metadata validation and original Runtime action connector ownership
$pkg/session.test.ts|$a/session.test.ts|bbfff544a1afcae5c61152db70c15136eb277374fb74cfa6af81d1eae30c115f|injection points: the Desk's session store and language replaced by configureHarness with the test host's sessionBearer and currentLanguage; paths: conformance/ is certification/; agent#37: what ends the session and what does not (the 401 classes, an opaque redirect) and a tool result in flight at the end; agent#42: the answer's headers held to the two content headers and the valid diagnostic ones; v0.11: explicit current Runtime replay metadata fixtures
$pkg/thinking.ts|$a/thinking.ts|bed626b8f2b5a91188442ad55543d35ebf88b75ff9324675d79eeef2a19828a5|injection points: sourceMessage, EndpointKind and ThinkingTier (types); agent#4: the tier spelled in the record's reasoning kind, nothing sent where it names none or the endpoint ignores it, said once; agent#10: the tier no longer decides whether the critic runs (runsRefutation and its ruling removed); ADR cross-provider (#117); ADR cross-provider review round two (#117); agent#142 review round 1: omitted reasoning stays omitted in model, critic, fallback and delegated paths; agent#142 review round 2: structural request reasoning boundary; agent#164: admitted request values and boundary-shaped fixtures
$pkg/thinking.test.ts|$a/thinking.test.ts|e1477cf219495e93a624557888833baf901faf110e23c7484aec015b12c019db|injection points: EndpointKind and ThinkingTier (types); agent#10: the tier's refutation gate removed; agent#142 review round 2: boundary-shaped reasoning fixtures; agent#164: admitted request values and boundary-shaped fixtures
$pkg/toolGate.ts|$a/toolGate.ts|83352875a4b38854e7c11801284b2c498b8f618b162d5575ee493a0d331f7a2e|injection points: ASSISTANT_TOOLS; agent#7: the six-tool ceiling, the test tool's argument allow-list and byte bounds, the refused tools and their reasons
$pkg/toolGate.test.ts|$a/toolGate.test.ts|eb4efcc66baf03bf511869ef3a761c10853ad3b754a5bfae25973cb9c20e2d13|injection points: ASSISTANT_TOOLS; whitespace: the blank line at the end of the file, which the repository's whitespace check refuses; agent#7: the ceiling's wording; agent#53: the contract's gate vectors (tool-call/gate-frames.json, tool-offer/phase-offers.json) held to the gate and the phases' rules, a child within its parent's phase; agent#57: the gate test drives the child’s offer through delegation.ts’s childOffer; #110 review: empty offer refuses every edit name; agent#180/#181/#182: completion tool offers, progress-only updates and provider pause controls; v0.11: current Runtime offer and phase-boundary fixtures
$pkg/certification/certification/clearsItsInterval.ts|$a/conformance/certification/clearsItsInterval.ts|-|-
$pkg/certification/certification/throwsWhileDraining.ts|$a/conformance/certification/throwsWhileDraining.ts|-|-
$pkg/certification/certification/touchesAfterRun.ts|$a/conformance/certification/touchesAfterRun.ts|-|-
$pkg/certification/certification/touchesOnLoad.ts|$a/conformance/certification/touchesOnLoad.ts|-|-
$pkg/certification/conformance.test.ts|$a/conformance/conformance.test.ts|01aea22ec868d109837ded0bc1741cdc1fafac9775b257a69caf2aee2c395ece|injection points: EndpointKind and ThinkingTier (types); contract vectors: the event order and K2's offer read from contract/vectors/; agent#1: the Gemini legs count the context tools from the offer vector; agent#9: the strict-alternation leg and the endpoint capabilities a leg sets; agent#4: the text tool calls legs and the tool-call mode a leg sets; agent#7: the ceiling is six names and test_conformance is refused at the wire; agent#11: the request bodies a leg sent kept; ADR-0005: the host's parts placed in their layers and the stable prefix held on every request of every leg, memoryFor asked once, and the project-instructions leg, the recall rule's checks replaced; agent#10: the refutation legs ask for the critic, its offer is the contract's critic offer, a seeded wrong outcome refuted, no critic where not asked at any tier; agent#10 review rows 1 and 6: the incomplete case on the proposal, the critic's offer with its readers; agent#37: the recorder the fixture names is a file in this repository, run by the package's scripts; review of #47 row 1: K1b, a sealed leg with the environment's tracing variables set; agent#57: the strict-alternation code and the scenario server moved out to certification/strictAlternation.ts and certification/scenarioServer.ts; v0.3.0 release review: K1c, each of the four tracing variables alone; ADR-0005 review: the system text held to exactly the guidance through its last line, the host's section, then the tools or the state; agent#69: the placement check holds the three Anthropic cache breakpoints (cacheMisses); v0.4.0 pre-release batch (#76): the placement check made placement-aware (three Anthropic cache breakpoints under trailing-user, two under system-tail) and run over the critic's requests with CRITIC_SYSTEM as the guidance; ADR policy table (#116); ADR memory write side (#98); ADR-0014 review: alias uses the record frame; ADR the output boundary (#100); carry of ADR-0017 into ADR-0020 (#130); agent#155: migrate retired critic controls and bare-text fixtures to canonical inputs; ADR-0024: explicit current event header expectations (#165); v0.11: explicit current Runtime replay metadata and host taxonomy fixtures; v0.11: synthetic metadata preserves historical vocabulary schema
$pkg/certification/runtime.json|$a/conformance/runtime.json|a0da1cd67321a4984000433918d1e365a5c63094bdc9f60242080b96706e4970|agent#37: the recorder and the verify command in the fixture name this repository's script
$pkg/certification/scenario.json|$a/conformance/scenario.json|-|-
$pkg/certification/scriptedModel.ts|$a/conformance/scriptedModel.ts|e3d30840d69d8c277dc9adeb552b36c17c09ff98219814e543070d32a1144e14|injection points: EndpointKind (type); agent#4: an endpoint without native tool calls (toolCalls: text)
$pkg/certification/scriptedModel.test.ts|$a/conformance/scriptedModel.test.ts|-|-
$pkg/certification/scriptedServer.ts|$a/conformance/scriptedServer.ts|3c9d949c02453a2013269f00e20c90203ccf332b3cee49cbc367de823e4bef1d|v0.11: explicit current Runtime replay sidecar while frozen transport recordings stay exact
$pkg/contract/catalog.json|internal/aifailure/catalog.json|f53e602e829e19167d6669736eb2de157c5664cf123c3e407d20f70ee4d490f5|ADR policy table (#116)
$pkg/contract/policy.json|internal/aicontext/policy.json|39555a41cfd5fb343af9ab420ce3b9aca42c8ea3c32b642d9150f5b8c7923ba9|agent#10: the critic's default, adversarialReview false, which the Desk's table takes in its swap; ADR-0010: criticDefault auto and criticReasoningFloor author in place of adversarialReview; ADR policy table (#116); ADR sub-agents (#97); ADR context compaction (#99); ADR records (#101); carry of ADR-0016 into ADR-0018 (#126); ADR steering a run in flight (#105); carry of ADR-0018 into ADR-0021 (#129); ADR-0024: versioned records and frozen historical structural readers (#165)
EOF
}

# Files in the package that were not moved: the package's own, listed so nothing arrives unseen.
new_files() {
  cat <<EOF
$pkg/README.md
$pkg/package.json
$pkg/tsconfig.json
$pkg/tsconfig.build.json
$pkg/vitest.config.ts
$pkg/dialects-shapes.test.ts
$pkg/contract-copy.test.ts
$pkg/host.ts
$pkg/host.test.ts
$pkg/entries/contract.ts
$pkg/certification/index.ts
$pkg/scripts/certify.mjs
$pkg/testing/host.ts
$pkg/testing/question.ts
$pkg/testing/setup.ts
$pkg/testing/afterPolicy.ts
$pkg/context/reader.ts
$pkg/context/requestValues.ts
$pkg/context/reader.test.ts
$pkg/engines/vercel/prefix.test.ts
$pkg/capabilities.ts
$pkg/capabilities.test.ts
$pkg/images.ts
$pkg/images.test.ts
$pkg/context/pattern.ts
$pkg/context/search.ts
$pkg/context/search.test.ts
$pkg/engines/vercel/capabilities.test.ts
$pkg/engines/vercel/retry.ts
$pkg/engines/vercel/bounds.test.ts
$pkg/framing.ts
$pkg/framing.test.ts
$pkg/testCases.test.ts
$pkg/engines/failurePaths.test.ts
$pkg/testing/scripted.ts
$pkg/context/chat.ts
$pkg/context/chat.test.ts
$pkg/prompt.ts
$pkg/prompt.test.ts
$pkg/toolSources.ts
$pkg/toolSources.test.ts
$pkg/critic.test.ts
$pkg/criticDefault.test.ts
$pkg/engines/codex/reformat.test.ts
$pkg/delegationResults.test.ts
$pkg/testing/scenarioDriver.ts
$pkg/certification/recorder.test.ts
$pkg/scripts/mutation-check.mjs
$pkg/scripts/mutation-rows.json
$pkg/scripts/record-conformance.mjs
$pkg/scripts/scripted-server.mjs
$pkg/scripts/packed-smoke.mjs
$pkg/testing/sdkWarnings.ts
$pkg/certification/scenarios.test.ts
$pkg/certification/scenarios/chat-host-tools.json
$pkg/certification/scenarios/delegation.json
$pkg/certification/scenarios/memory.json
$pkg/certification/scenarios/plan-mode.json
$pkg/certification/scenarios/tool-sources.json
$pkg/certification/scenarios/approval-pause.json
$pkg/certification/scenarios/correction-loop.json
$pkg/certification/scenarios/multi-turn.json
$pkg/certification/delegation.test.ts
$pkg/engines/deepagents/index.ts
$pkg/engines/deepagents/loop.ts
$pkg/engines/deepagents/model.ts
$pkg/engines/deepagents/wire.ts
$pkg/engines/deepagents/deepagents.test.ts
$pkg/certification/scenarioServer.ts
$pkg/certification/strictAlternation.ts
$pkg/corpus.ts
$pkg/corpus.test.ts
$pkg/scripts/corpus-check.mjs
$pkg/certification/corpus.test.ts
$pkg/certification/scenarios/evaluation-corpus.json
$pkg/scripts/bundle-size.mjs
$pkg/sourceArguments.ts
$pkg/sourceArguments.test.ts
$pkg/skills.ts
$pkg/subagents.ts
$pkg/edits.ts
$pkg/edits.test.ts
$pkg/certification/proposal-edits.test.ts
$pkg/certification/scenarios/proposal-edits.json
$pkg/memories.ts
$pkg/memories.test.ts
$pkg/certification/memory-write.test.ts
$pkg/certification/scenarios/memory-write.json
$pkg/subagents.test.ts
$pkg/certification/subagents.test.ts
$pkg/certification/scenarios/subagents.json
$pkg/skills.test.ts
$pkg/certification/scenarios/skills.json
$pkg/policy.ts
$pkg/policy.test.ts
$pkg/policySession.test.ts
$pkg/engines/requestCap.test.ts
$pkg/execution/requestCap.test.ts
$pkg/contract/schemas/corpus-entry.json
$pkg/contract/policy-schema.json
$pkg/certification/scenarios/policy-table.json
$pkg/providers.test.ts
$pkg/providers-reference.test.ts
$pkg/certification/providers.test.ts
$pkg/certification/scenarios/cross-provider.json
$pkg/outputGuard.ts
$pkg/outputGuard.test.ts
$pkg/contract/output-patterns.json
$pkg/certification/output-boundary.test.ts
$pkg/certification/scenarios/provider-failover.json
$pkg/engines/vercel/retry.test.ts
$pkg/criticSwitch.ts
$pkg/criticSwitch.test.ts
$pkg/certification/scenarios/critic-switch.json
$pkg/records.ts
$pkg/records.test.ts
$pkg/certification/records.test.ts
$pkg/certification/scenarios/records.json
$pkg/certification/scenarios/identity.json
$pkg/steering.test.ts
$pkg/scripts/normalize-esm.mjs
$pkg/testing/captureGlobal.ts
$pkg/testing/captureGlobal.test.ts
$pkg/execution/mutation.ts
$pkg/execution/mutation.test.ts
$pkg/memoryLifecycle.test.ts
$pkg/testing/memory.ts
$pkg/scripts/zod-consumer.mjs
$pkg/scripts/body-parser-consumer.mjs
$pkg/scripts/undici-controls.mjs
$pkg/recordReader.ts
$pkg/recordReader.test.ts
$pkg/recordRefusal.test.ts
$pkg/contract/record-shapes.json
$pkg/descriptorSnapshot.ts
$pkg/descriptorSnapshot.test.ts
$pkg/descriptorGraphicRanges.json
$pkg/proposalNumbers.ts
$pkg/proposalNumbers.test.ts
$pkg/proposalCustody.test.ts
$pkg/certification/proposal-numbers.test.ts
$pkg/storage/chat.ts
$pkg/storage/common.ts
$pkg/storage/conformance.ts
$pkg/storage/durable.ts
$pkg/storage/engines.test.ts
$pkg/storage/handoff.ts
$pkg/storage/legacyChat.ts
$pkg/storage/model-engines.test.ts
$pkg/storage/model.test.ts
$pkg/storage/model.ts
$pkg/storage/planning.test.ts
$pkg/storage/release.ts
$pkg/storage/run.ts
$pkg/storage/storage.test.ts
$pkg/storage/writer.test.ts
$pkg/storage/writer.ts
$pkg/planning/references.ts
$pkg/storage/callback.test.ts
$pkg/execution/plan.ts
$pkg/execution/plan.test.ts
$pkg/execution/planActions.ts
$pkg/execution/planActions.test.ts
$pkg/runReport.ts
$pkg/runReport.test.ts
$pkg/storage/retained.test.ts
$pkg/askingWell.test.ts
$pkg/askingWell.ts
$pkg/certification/asking-well.test.ts
$pkg/certification/dialects.test.ts
$pkg/certification/historyCaptureReplay.test.ts
$pkg/certification/runtime-current-contract.json
$pkg/context/chatSnapshot.test.ts
$pkg/context/chatSnapshot.ts
$pkg/context/historyCompaction.test.ts
$pkg/context/runtimeActions.ts
$pkg/context/storedChat.ts
$pkg/contract/run-report-v1.json
$pkg/contract/run-report-v2.json
$pkg/corpusCandidate.ts
$pkg/dialects-compatibility.test.ts
$pkg/dialects-inventory.test.ts
$pkg/dialects-pin.test.ts
$pkg/dialects-retained.test.ts
$pkg/dialects-runtime.test.ts
$pkg/dialects.test.ts
$pkg/dialects.ts
$pkg/gatewayReceipt.test.ts
$pkg/gatewayReceipt.ts
$pkg/principal.ts
$pkg/principal.test.ts
$pkg/principalGate.test.ts
$pkg/principalSession.test.ts
$pkg/contract/record-shapes-v012.json
$pkg/contract/run-report-v3.json
$pkg/history/archive.test.ts
$pkg/history/archive.ts
$pkg/history/archiveTaskIdentity.test.ts
$pkg/history/capture.test.ts
$pkg/history/capture.ts
$pkg/history/captureCollector.test.ts
$pkg/history/captureCollector.ts
$pkg/history/capturePublication.test.ts
$pkg/history/closure.ts
$pkg/history/codec.ts
$pkg/history/corpusCheckpointView.test.ts
$pkg/history/corpusCheckpointView.ts
$pkg/history/corpusManifest.test.ts
$pkg/history/eventProjection.test.ts
$pkg/history/feedback.test.ts
$pkg/history/feedback.ts
$pkg/history/live.ts
$pkg/history/metadata.test.ts
$pkg/history/metadata.ts
$pkg/history/nativeBindings.test.ts
$pkg/history/nativeBindings.ts
$pkg/history/query.test.ts
$pkg/history/query.ts
$pkg/history/recording.test.ts
$pkg/history/recording.ts
$pkg/history/relations.test.ts
$pkg/history/relations.ts
$pkg/history/report.ts
$pkg/history/reportFeatures.test.ts
$pkg/history/reportRepeatTools.test.ts
$pkg/history/reportTaskVersions.test.ts
$pkg/history/types.ts
$pkg/history/view.test.ts
$pkg/history/view.ts
$pkg/repeatTools.contract.test.ts
$pkg/repeatTools.integration.test.ts
$pkg/repeatTools.test.ts
$pkg/repeatTools.ts
$pkg/runReportReader.test.ts
$pkg/runReportReader.ts
$pkg/runtimeActions-runtime.test.ts
$pkg/runtimeActions.test.ts
$pkg/runtimeActions.ts
$pkg/storage/history.ts
$pkg/storage/lifecycle.test.ts
$pkg/storage/liveCapture.test.ts
$pkg/storage/liveCaptureControls.test.ts
$pkg/storage/liveCaptureLifecycle.test.ts
$pkg/storage/liveCapturePrivacy.test.ts
$pkg/storage/physicalDispatch.ts
$pkg/storage/runtimeActionOwner.ts
$pkg/storage/turnRouteConsumer.test.ts
$pkg/storage/turnRouteInteractions.test.ts
$pkg/storage/turnRouteNative.test.ts
$pkg/storage/turnRouteOwner.ts
$pkg/storage/usageBudget.test.ts
$pkg/storage/usageBudget.ts
$pkg/storage/usageConsumer.test.ts
$pkg/storage/usageJSON.ts
$pkg/storage/usageJournal.test.ts
$pkg/storage/usageJournal.ts
$pkg/storage/usageReport.test.ts
$pkg/storage/usageResponse.test.ts
$pkg/storage/usageResponse.ts
$pkg/storage/writerCapture.ts
$pkg/storage/writerHistory.test.ts
$pkg/taskSteps.contract.test.ts
$pkg/taskSteps.integration.test.ts
$pkg/taskSteps.test.ts
$pkg/taskSteps.ts
$pkg/taxonomyIntegration.test.ts
$pkg/testing/dialects.ts
$pkg/toolTaxonomy.test.ts
$pkg/toolTaxonomy.ts
$pkg/turnRoute.test.ts
$pkg/turnRoute.ts
$pkg/storage/sourceApprovalClaim.test.ts
$pkg/storage/nativePlanPause.test.ts
$pkg/storage/nativePlanPause.ts
$pkg/receiptQuarantine.test.ts
$pkg/storage/lease.ts
$pkg/storage/lease.test.ts
$pkg/storage/delegateChatScope.test.ts
$pkg/storage/wait.ts
$pkg/storage/wait.test.ts
$pkg/storage/resume.ts
$pkg/storage/resume.test.ts
$pkg/storage/waitConformance.ts
$pkg/storage/waitConformance.test.ts
$pkg/storage/waitReview.test.ts
EOF
}

# sha256 of standard input, by whichever tool this system has.
digest() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum; else shasum -a 256; fi | cut -d' ' -f1
}

# The hunks of one moved file against the Desk's: `diff -u` without its two file-name lines.
hunks() {
  { diff -u --label "desk/$2" --label "$1" "$desk/$2" "$root/$1" || true; } | tail -n +3
}

echo "parity: $pkg against the Desk at $(git -C "$desk" rev-parse --short HEAD 2>/dev/null || echo 'an unversioned checkout'); the table was made against ${desk_pin:0:7}"
failed=0
same=0
differing=0
while IFS='|' read -r ours theirs expected reason; do
  if [ ! -f "$root/$ours" ]; then echo "MISSING in the package: $ours"; failed=1; continue; fi
  if [ ! -f "$desk/$theirs" ]; then echo "MISSING in the Desk: $theirs"; failed=1; continue; fi
  if cmp -s "$desk/$theirs" "$root/$ours"; then
    if [ "$reason" != "-" ]; then echo "STALE reason, the file is identical: $ours"; failed=1; fi
    same=$((same + 1))
    continue
  fi
  differing=$((differing + 1))
  actual=$(hunks "$ours" "$theirs" | digest)
  echo
  if [ "$reason" = "-" ]; then
    echo "UNEXPECTED difference: $ours"
    failed=1
  elif [ "$actual" != "$expected" ]; then
    echo "CHANGED hunks: $ours differs from the Desk by more or other than the hunks its row names"
    echo "  ($reason); the row pins sha256 $expected and this diff is $actual"
    failed=1
  else
    echo "== $ours ($reason)"
  fi
  hunks "$ours" "$theirs"
done < <(table)

# Every package file is a moved file or a declared new one.
moved=$(table | cut -d'|' -f1)
declared=$(new_files)
while IFS= read -r file; do
  if ! grep -qxF "$file" <<<"$moved" && ! grep -qxF "$file" <<<"$declared"; then
    echo "UNDECLARED package file: $file"
    failed=1
  fi
done < <(cd "$root" && git ls-files --cached --others --exclude-standard -- "$pkg")

echo
echo "parity: $same moved files identical, $differing differ for the reasons named above; $(new_files | wc -l | tr -d ' ') package files are new"
if [ "$failed" -ne 0 ]; then
  echo "parity: FAILED"
  exit 1
fi
echo "parity: every difference is named"
