#!/usr/bin/env bash
# The parity proof for packages/harness: every file moved from the Desk, diffed against the Desk's
# original, with every difference printed and named.
#
#   scripts/parity.sh <desk-checkout>
#
# <desk-checkout> is a checkout of Judgment-Pack/judgment-pack-desk (the extraction was read from
# integration/v0.13 at d9251ec). Each row of the table below is one moved file: its place in the
# package, its place in the Desk, the sha256 of its expected diff, and why it differs. A row whose
# digest and reason are "-" must be byte for byte the Desk's. A row with a reason prints its hunks
# under that reason, and its diff (the `diff -u` hunks printed below the row's header) must hash to
# the row's digest, so a hunk nobody named fails even in a file that has named ones. The run fails
# on a row marked "-" that differs, on a diff whose digest is not the row's, on a file missing from
# either side, and on a package file that is neither moved nor declared new below. Exit 0 means the
# hunks printed are the only differences. After an intended change, the failure prints the new
# digest to put in the row.
set -euo pipefail

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
$pkg/agent.ts|$a/agent.ts|ed21e087c3bf477d2a7547382d41cce76d33677757eedf9730570bd25837dc8b|agent#10: a critic run may name the model the host named for the critic (AgentRequest.model); agent#10 review row 10: the critic's model passed down once per pass; review of agent#35: a delegated child's run may carry the child model too (model, delegated)
$pkg/agentTransport.ts|$a/agentTransport.ts|2274ab45c03eb2cefa483effaded5ce7112f6c52c510a92ad2a912346e67bb1c|injection points: AITarget, deskSocketURL, chassisUrl, socketProtocols, sessionBearer, whenSessionEnds; agent#10: a run's model is the bound one or, on a critic run, the host's critic model, and no other; agent#10 review row 10: a run's model other than the bound one is sent only on a critic run, the host asked once by the engine; review of agent#35: another model sent on a delegated child's run as on a critic run, the coordinator's mark never sent
$pkg/agentTransport.test.ts|$a/agentTransport.test.ts|df8c20487e36f88e9c14eea65b142a7bb90be82990aea400756669423af7630f|injection points: the Desk's two module mocks become one mock of host.ts
$pkg/completion.ts|$a/completion.ts|b2bf689ca718a8a8d1481fdce145d42b52f3b6f8ff20d64b54f35fe3d1d3d46e|agent#9: the prompt cache reads and writes (cacheReadTokens, cacheWriteTokens) read from the SDK usage and kept
$pkg/completion.test.ts|$a/completion.test.ts|-|-
$pkg/context/context.test.ts|$a/context/context.test.ts|2d0d52597eba4331879b66a585fec3d99fd3e89a3b6c91f64ea88ae610e61927|agent#1: the reference envelope names read_window; agent#12: the compacted summary's steps read through their data block
$pkg/context/ledger.ts|$a/context/ledger.ts|86c8641cb6914db260d48bccbd4bbb2da40281db95559866cb0fb58d595b177c|declaration emit: Entry exported (a type, no runtime change) so tsc can write controller.d.ts; agent#1: the reader, read_window, among the ledger tools and named in the reference envelope (readWith); agent#5: search among the ledger tools, the attachments the host retained, an image item read whole (a reference followed to the image it names) or answered with the line saying why it cannot be shown, search naming such an item, a retained image by reference; agent#8: the entries in a store, a run's own or a chat's turn (LedgerStore, EntryStore), a read of an evicted entry saying why, a chat run's checkpoint carrying the entries it retained or read, a restore into the same chat, release and isolate; agent#15: a store may adopt an entry the run retains itself with the same content (adopt, a delegated child's view of the chat)
$pkg/context/managed.ts|$a/context/managed.ts|57f28614ffe8fefef0418a5c86a28886dd81bf40d01e3abf577fa7a682b3ac4b|agent#8: a run's end closes its context (a chat's turn closes, a ledger of the run's own is cleared), and the comment on how long derived context lives; agent#10: the critic's refusals put on the stream as guardrail events before the result; ADR-0006: a tool with a source's call and result decorated with its source record; ADR-0008: the approval pause at the boundary: a call refused while another act call waits said with its call, its guardrail and its refused result, the run ended paused once an act call waits, and on a continued run the decision and the approved call's result written before the first request and the continuation appended to the prompt
$pkg/context/policy.ts|$a/context/policy.ts|a68070fe135201f95c94e5d5e6441029d588991667e7bec0a08a25a05f600b76|the Go-side copy: policy.json read from the package's contract/; agent#1: readWindowBytes and encodedPrefix, the read window; agent#2: the turn clock and the tool-call deadline (TIME_DEFAULTS); agent#4: contextPolicy takes the capability record's window and output cap; agent#5: the images one request carries (IMAGE_DEFAULTS); agent#7: the test tool's bounds (TEST_DEFAULTS); agent#8: the bounds on a chat's index (INDEX_DEFAULTS); agent#11: memoryBytes reserved only while the host has not said memory is off; ADR-0005: the bound on the project's own instructions (PROMPT_DEFAULTS, projectInstructionsBytes); ADR-0006: SOURCE_DEFAULTS, the six bounds on tools with a source, in the policy; agent#69: contextPolicy takes no memory word, since the memoryBytes reserve is retired; v0.4.0 pre-release batch (#73): cataloguedTools and catalogueBytes, the catalogue's two bounds, in SOURCE_DEFAULTS (eight bounds)
$pkg/context/recovery.integration.test.ts|$a/context/recovery.integration.test.ts|288ed58c3c24838e24e4dc686b88f00af7a7ea4d53c5ec353d65597f662aa56c|agent#5 and agent#12: the fixture's window 17,408 tokens, not 16,384, because this harness sends fixed text the Desk does not (the search tool, the data rule) and the compacted summary must still hold the schema result's handle
$pkg/context/session.ts|$a/context/session.ts|45319ae7eacf210424f7acd0d98271fb217a4553c9f28856095694c7c35a247f|agent#1: read_window reserved; agent#9: the execution guidance no longer appended to the prompt (each adapter places it), currentState; agent#4: the capability record resolved once for the run, the budget from it; agent#5: search reserved, the host's documents retained as attachments; agent#7: the per-turn test-call cap (claimTestCall); agent#12: the run's marker, frame, framed results read back in compaction and the summary framed; agent#8: the run's turn on a chat-scoped context opened with the run and closed at its end or a failed start, the index of earlier results counted against the prompt's allowance, the session tools that read kept, the critic isolated from the chat; the first message's text parts made in one place (opening), the index cut to the room left, at most INDEX_SHARE of the allowance, and made only for the chat's own turn; agent#11: the host's word on memory read into the policy; the person's message as the first text part where the prompt is previewed, a delegated child's objective as its first text part; the context inputs chat, runtime, memory and objective; agent#10: the critic's allow-list held below both engines (the critic phase, admit, on the wrapped callTool and host tools), its refusals as guardrail notices, the declared cases read closed; agent#10 review rows 5 and 6: the boundary narrowed for the critic (its readers admitted) and for the tool-free reformat run, a served but unoffered name refused before any call, the critic's retained message read with read_window or search; agent#14 and agent#15: the context input link, by which the delegation coordinator reaches a run's execution, and chat as a ChatScope, a delegated child's view of the chat among them; review of agent#35: the run's connection (context.connection), and the models it names asked once per run (models, modelsFor); agent#17: the context input delegationReuse, the host's word that a response reuses no earlier delegated result; the connection's capacity among what modelsFor is asked for; agent#43: the chat's read cache consulted only for a name the session offers; ADR-0005: the host's parts asked once per run (askHost, the person's message or a child's objective as the memory query, none where memory is off) and the system text from the one assembler (system), each request body's digest recorded before it is sent on the wrapped model and agent capabilities (recordPrompt), the context input memory's comment; ADR-0005 review: the agent request digested as the transport sends it, without the delegated mark, and an empty objective taken as none; ADR-0006: the tools with a source declared and named for the wire before the plan filter, offered after the context opens (openSources), load_tool beside the ledger's tools where a source is a catalogue, each source call held by the source gate, the Runtime's calls counted in its row, a source's result bound, load_tool reserved; agent#69: the input allowance takes no briefBytes or memoryBytes reserve; v0.4.0 pre-release batch (#73): the source gate shared with a delegated child through the link (ExecutionLink.sources), the tools the catalogue's bound withheld put on the stream as guardrails, the call id handed to a host tool's execute and kept by the source gate; ADR-0008: the context input approval (the person's decision), the run's approval for the source gate and where no act call is admitted (a child, Plan mode, the critic, the reformat run), the policy asked by an engine before an act call's tool_call (approval), a request refused while an act call waits (ApprovalPaused), the approved call's runner (runApproved), the guardrail's reason
$pkg/context/toolPolicy.ts|$a/context/toolPolicy.ts|182e5189eb6c60f0bc986768966eb2f936b737d63e4ffe5499b782e3e67cd13a|agent#8: runtime, a result that depends on the Runtime serving it, and what immutable means under a chat-scoped context
$pkg/context/toolPolicy.test.ts|$a/context/toolPolicy.test.ts|-|-
$pkg/delegation.ts|$a/delegation.ts|fbad0484639223717f84ef4e6c2a6f68b894868912aa5cd2d629c7330fa00d54|agent#14: the delegate call runs the batch, awaited in the parent's step on vercel, and on codex the response's next run given the call's result and nothing else; more than one batch, one at a time; the parent's execution owning the shared request count; a finished child resumed with its context; agent#15: context by reference through a view of the parent's chat, each profile's model, budget, time limit and Runtime tools, a child's test calls charged to its parent's turn; agent#13: long findings kept whole with their head and a read_window cursor, the summarize step, the tool-call cap stated in the state; agent#7 and agent#11 as before: the turn's test calls shared, a child under its parent's word on memory, its objective its request text (context.objective); review of agent#35: the delegate tool offered on an agent capability only while a child run and the parent's next run remain, and the limit stated; concurrent children's grants; why a child stopped and its failure, its own time limit winning a tie; an unoffered name refused before it counts; each answer task's objective and references; the codex continuation framed as one data block; the child model from the host's modelsFor answer for the connection; review of agent#35 after main's agent#10: a codex child's run carries the child model on the agent request; agent#16: each child's report read in its profile's shape or written by the coordinator, what its run asks of the parent returned as open questions, one more batch after them, its prose streamed under its task id; agent#17: a partial result at the time limit, one retry before the first byte, reuse of an identical task; agent#18: each child's tokens, time to first byte and duration, and the figures in the parent's checkpoint; the review of agent#39: reuse only of a child with no counted tool call, keyed with its model and kept once the batch's answer is delivered, identical tasks of a batch run once, delegationReuse; the running batch in the checkpoint's figures and the parent's own requests and runs counted where it makes them; a codex child's runs counted where admitted; the batch's concurrency from the connection's modelsFor answer; the research hand-back as an open question with a partial result; a failed run's open questions kept on its record; the child retry held to a 5xx other than 504; earlierWork's partial; the coordinator's open questions kept in the text at more; ADR-0003: the coordinator wraps the deepagents engine as it wraps the other two; agent#57: the child’s offer drawn by one function, childOffer, extracted so that the gate test drives the offer the child is given; v0.4.0 pre-release batch (#73): a child's link hands it the parent's source gate, the parent's checkpoint after a batch carries the children's source rows, the call id passed through the wrapped execute; ADR-0008: on an agent capability an act offered to a parent run only while the run that continues after its pause remains (mayAct)
$pkg/delegation.test.ts|$a/delegation.test.ts|c02c6be2f2eb88c0e9fa3544ffd0312839f77c33427e13c6b1bc7a29eaaf86f9|test kept in the Desk: the case over the Desk's response history; the Desk's chat-history reader replaced by a fold of delegated_task events with its interruption rule; agent#14: the cases rewritten for one parent run (findings as the call's result; on codex the next run given the result), and new cases for one run with native tool messages, a second batch, the batch bound, resume, and codex's next run reading by reference; agent#15: the request with no copied context, and new cases for references, the profiles, the child model, six page reads and the author-test child; agent#13: long findings, the summarize step and the stated cap; review of agent#35: the cases for the codex agent-run limit, concurrent children's grants, the framed codex continuation with objectives, why a child stopped, unoffered names, each profile's offer, references one call each, readProfiles through withDelegation, resume under another profile, a child's prompt without the chat's index, and the child model from modelsFor; the codex fake of the earlier-reads case tells a child by its brief; a codex child on the host's child model through the agent transport; agent#17: each task of the helper its own objective, and each batch of the codex batch-limit case its own task, so no task is an earlier one reused; the review of agent#39 row 7: the research hand-back case asserts a complete, partial task with an open question; agent#43: a child is refused the parent's cached read of a name its profile was not offered; the model a task ran on under each connection; agent#44 review: the Codex variant of the cached-read refusal, an unoffered get_schema asked for through the agent’s tool callback; ADR-0008: a Codex parent offered an act with a source only while the run that continues after its pause remains
$pkg/delegationContract.ts|$a/delegationContract.ts|dea4d2a74e7ac3f8b51fd3d6b587fbd1dae2a355d6348fd34a0ac78e6429fede|agent#14, agent#15 and agent#13: references, resume and summarize in the request and no copied context; the task record's budget, model, references, resumes, more and summarized; each profile's model, budget and Runtime tools, the author-test profile, readProfiles; MAX_TOOLS and MAX_BATCHES; the instructions and the tool's description; review of agent#35: MAX_AGENT_RUNS, AGENT_DELEGATION_INSTRUCTIONS and DELEGATION_SPENT; agent#16: the report shapes, readReport and reportInstructions, the profile's report; agent#16 to agent#18: the record's report, partial, reused, retried, tokens, firstByteMs and durationMs; the instructions on reports, open questions, partial results and reuse
$pkg/engine.ts|$a/engine.ts|0b9f63d3f44cb06e5ddf7f98ed77f4536b8f6a6db303081f3340b04c7e79a032|injection points: Language, AssistantEngine, EndpointKind (types); agent#6: the proposal's note (ProposalNote); agent#4: a session may carry members of its endpoint's capability record; agent#5: a result's content item is open (image parts); agent#10: the declared cases (criticCases), the critique's comparisons and figures, the note's figures, the default's wording; agent#10 review rows 1 and 8: the critique's and the proposal's incomplete cases, the harness's calls marked by origin; agent#16: message_progress's taskId; ADR-0006: HostTool's source, effectClass, approval and approve, McpToolResult's receipt, and the source record and proposed on the tool_call and tool_result events; v0.4.0 pre-release batch (#73): the approval reader handed the call (ApprovalRequest: name, tool, source, args), execute handed the id of the call's events; ADR-0008: approve the host's approval policy (allow, refuse, ask), the approval_requested event, the guardrail's reason
$pkg/engines/codex/codex.test.ts|$a/engines/codex/codex.test.ts|db09f35f3aff1533507ecbf4446f3d4edfae0040ae67434d49edd09c4f4a712a|paths: conformance/ is certification/, testing/question reads the contract vector; test kept in the Desk: the case over the Desk's dialect chat tools; agent#1: the reference envelope names read_window; agent#6: the whole answer kept, the critic's note, propose_pack; agent#12: read_context's answer read through its data block; agent#25: K2 against the contract's scenario offer and the critic's, K3b through the gate (T7 write_file), an unoffered call answered refused; agent#10: the critic asked for, offered its allow-list, a seeded wrong outcome refuted; agent#29: one reformat run before the whole answer is kept; agent#10 review rows 5 and 6: the critic offered the harness's two readers, a served but unoffered name refused before any call; agent#36: the state and guidance headed by STATE_HEADER; agent#42: the previewed prompt leads with the person's message; ADR-0005: the two cases rewritten for the layered order, the host's parts at the end of the instructions and the request text before the state, without the recall rule; ADR-0005 review: the project-instructions leg on the agent wire
$pkg/engines/codex/index.ts|$a/engines/codex/index.ts|2b6f25e782f016b3f49a454e3bfbe8c509874c34c00eaf1d910744be0b748ffc|injection points: assistantLanguageInstructions; agent#9: the agent prompt carries currentState and the execution guidance itself; agent#6: the whole answer kept, the critic's note, propose_pack; agent#2: the tool-call deadline; agent#5: an image in a result reaches the agent as the host's text rendering; agent#12: a reading tool's answer framed, the data rule in the instructions; agent#8: a chat's index after the request text in the agent prompt, in the room the allowance leaves (opening); agent#10: the critic's default from the policy table, its allow-list offered and admitted, the declared cases compared and the rest rehearsed, its model and figures; agent#25: an unoffered call put through the gate and refused; agent#29: one tool-free reformat run (reformatPrompt); agent#10 review rows 5, 6, 8 and 10: a served but unoffered name refused before any call, the reformat run narrowed, the critic offered the two readers, the harness's calls marked, counted and inside the critic's phase and clock, the critic model asked once; review of agent#35: the critic's model read from the run's modelsFor answer; agent#36: the state and guidance appended at the prompt's end headed by STATE_HEADER, which is imported from engines/contract.ts; ADR-0005: the instructions from the one assembler with the host's parts (context.system), and the author run's prompt as the model wires' first user turn: the request text or the person's message leading the preview (opening), the index, then the state; v0.4.0 pre-release batch (#73): the call id handed to a host tool's execute; ADR-0008: an act call the host's policy asks about ends the run as a delegation handoff does, approval_requested in its call's place
$pkg/engines/contract.ts|$a/engines/contract.ts|76c1aa0ea8b6e61bf1474230be8b81adf79b3e625d64b5010391ca979a039e8d|injection points: EndpointKind (type); agent#6: why a proposal was not read, the whole answer, the reformat request, propose_pack; agent#2: withDeadline, toolTimeout, limitText; agent#4: the schema shown by the record's tool-schema subset, the text tool call read by the proposal fence's closed reader, the tools described in text; agent#25: the unoffered call's refusal shared by both engines (refusedUnoffered); agent#10 review row 5: the refusal text shared (unofferedRefusal); agent#16: reportBlocks, a child's report block read by the proposal fence's reader; agent#36: STATE_HEADER, defined here for both adapters; v0.3.0 release review: the refusal text ends its detail in one period; agent#61: a detail's own trailing period is kept (an ellipsis keeps all three) and one is added only where it has none; ADR-0005: STATE_HEADER's comment names its layer, no host reading a request
$pkg/engines/contract.test.ts|$a/engines/contract.test.ts|-|-
$pkg/engines/index.ts|$a/engines/index.ts|1b683f792073e75463e5ee944d71226fcabfff6033f4c09be80725470c4f885a|injection points: AssistantEngine (type); ADR-0003: the deepagents engine registered, a model engine
$pkg/engines/vercel/channel.ts|$a/engines/vercel/channel.ts|-|-
$pkg/engines/vercel/completion.test.ts|$a/engines/vercel/completion.test.ts|-|-
$pkg/engines/vercel/engine.test.ts|$a/engines/vercel/engine.test.ts|f3f1d117ee325523dad555db90eac2aa7198fbc463dac24c9a6ad41167963c65|injection points: ASSISTANT_ENGINES, ASSISTANT_TOOLS; paths: LANGUAGES from the test host, testing/question reads the contract vector; agent#1: read_window in the offered tool lists; agent#6: the reformat request and the whole answer, the critic's note, propose_pack; agent#5: search in the offered tool lists; agent#10: the critic asked for, not started by the tier; agent#37: the run's own iterator closed with done, and the unoffered call not dispatched once the consumer has stopped; agent#38: the provider options under deskEndpoint
$pkg/engines/vercel/index.ts|$a/engines/vercel/index.ts|-|-
$pkg/engines/vercel/loop.ts|$a/engines/vercel/loop.ts|9a4206e93cdd9e60035ea467f8c84986d1ea8b16df5a3ab238706cb17f67531f|injection points: assistantLanguageInstructions, AssistantEngine and EndpointKind (types); agent#9: a stable prefix with the changing state placed by endpoint (the person's message on the first request, then a trailing user message or the system tail), the critic's note headed, Anthropic cache breakpoints, the tier held for the run; agent#6: the reformat request, the whole answer kept, the critic's note, propose_pack; agent#2: the model-request retry, the turn clock, the tool-call deadline; agent#4: the capability record read wherever the loop decided by family (thinking, schema subset, native or text tool calls, one call per turn, streaming quirks, the state placement), the text tool path in the loop and the critic; agent#5: images routed by vision (shown on the text path at a strict turn order), beside their results, the newest held to the per-request image bounds; agent#12: a reading tool's answer framed through toModelOutput and in the text path's result message, the data rule in the instructions and the critic's; agent#8 and agent#11: the first message's text parts from the session context (opening: the request text first, a chat's index last); agent#10: the critic's default from the policy table, its allow-list, its unoffered calls refused, the declared cases compared and the rest rehearsed, its model and figures; agent#25: the unoffered call's refusal shared with codex; agent#10 review rows 6 and 8: the critic offered the two readers and its first message read inside the pass, the harness's calls marked and counted; agent#14: delegate_tasks an awaited call in the parent's step, reported as a call and a result, with no tool-call deadline; review of agent#35: the critic's model read from the run's modelsFor answer; agent#36: STATE_HEADER imported from engines/contract.ts and re-exported; agent#37: runVercelLoop exported for its test; agent#38: provider options under the key the SDK names, ENDPOINT_OPTIONS; ADR-0005: the system text and the critic's from the one assembler with the host's parts (context.system), STATE_HEADER's comment without a host reading requests; agent#69: the third Anthropic cache breakpoint, on the first text part of the first user turn (cachedOpening); v0.4.0 pre-release batch (#73, #76): the call id handed to a host tool's execute; the third Anthropic cache breakpoint placed under trailing-user only (cachedOpening takes the placement); ADR-0008: an act call the host's policy asks about held, approval_requested in its call's place, never run; review of #79: every act call given an id, so its approval decision is its own
$pkg/engines/vercel/relay.ts|$a/engines/vercel/relay.ts|789564820a5cd98b08fa5695d411fb0e7a62da65b8f3030408af8b6cf88dee7d|injection points: EndpointKind (type); agent#4: the pinned reasoning member read and carried back, a whole answer asked of an endpoint that answers whole; agent#3: a whole answer that broke while read, or is not JSON, reported by class
$pkg/execution/checkArtifact.ts|$a/execution/checkArtifact.ts|-|-
$pkg/execution/checkArtifact.test.ts|$a/execution/checkArtifact.test.ts|-|-
$pkg/execution/controller.ts|$a/execution/controller.ts|f6e0fe647c7aed81c45ad8b1a8a7c0a1a794b4f3e7756aa8506c57b66bd54374|agent#9: requestState, the part of the guidance that changes between requests; agent#2: a refused attempt's row in the completion record and the attempt number per call (recordRefusal, beginAttempt); agent#8: wrapRead keeps a session read's answer in a chat-scoped context and serves an immutable one from the chat's read cache; agent#13: the tool-call cap in the request state (toolAllowance); agent#15: a delegated child's first allowance (limitTo); agent#43: the read cache serves only a name the session offers (kept.offered); ADR-0005: each request body's digest recorded before it is sent (recordPrompt); ADR-0006: the schemas load_tool loaded stated after the request state (loadedSchemas); ADR-0008: a paused approval continued only with the person's decision for its call, the decision written (approved or refused), a restored approved call answered with an unknown outcome, the approval asked (approvalAsked) and answered once the continuation request is sent (recordPrompt); review of #79: a decision on an approval already decided refused (stale-state)
$pkg/execution/correctArtifact.ts|$a/execution/correctArtifact.ts|-|-
$pkg/execution/correctArtifact.test.ts|$a/execution/correctArtifact.test.ts|-|-
$pkg/execution/execution.test.ts|$a/execution/execution.test.ts|83b025c05b75843046bb03e0df56094a27fc9b680e32073583fa7db2e791161c|test kept in the Desk: the case over the Desk's chat checkpoint
$pkg/execution/progress.ts|$a/execution/progress.ts|-|-
$pkg/execution/progress.test.ts|$a/execution/progress.test.ts|-|-
$pkg/execution/state.ts|$a/execution/state.ts|7af055c5df72e5bd4ff30e60e5828c14c2c6aa99ca46ecf3c809aed7f6b650cb|agent#2: the completion record's attempt, failure and waitMs (CompletionRecord, readExecution); agent#7: the turn's test-call count (testCalls) carried in the checkpoint; agent#18: the delegation figures (DelegationFigures, validDelegationFigures) carried in the checkpoint; ADR-0005: the prompt digests carried in the checkpoint and read closed (PromptDigest, MAX_PROMPT_DIGESTS); ADR-0006: the checkpoint's sources, the run report by source, read closed (validSourceFigures); ADR-0008: the checkpoint's approval read closed (ApprovalRecord, validApproval), the decision's reader (readApprovalDecision), the refused result and the unknown outcome
$pkg/failures.ts|$a/failures.ts|b8deb6c05e890f1642f210dc36906267f2014a8b02586734cd0826950bd67a69|the Go-side copy: catalog.json read from the package's contract/; agent#2: the retried refusals by name (canRetryRefusal, refusalCode)
$pkg/failures.test.ts|$a/failures.test.ts|-|-
$pkg/failures.integration.test.ts|$a/failures.integration.test.ts|bade99f8d9b602b16789f9835f1e1abdb0a0ef7a749afc41ce43838ef0095a4c|agent#2: Retry-After 120, past the retry wait limit, so the refusal is still reported after one request
$pkg/geminiSchema.ts|$a/geminiSchema.ts|-|-
$pkg/geminiSchema.test.ts|$a/geminiSchema.test.ts|8e409a5b5b90560de63bbeee2565a1d698d0a271e471e31b4bfa1e5abaed03ee|paths: conformance/ is certification/
$pkg/memory/client.ts|$a/memory/client.ts|447add57ae764bb05abe84cdaf49c22420041d6b938c52e238c3b22beec07293|injection points: answer, deskFetch
$pkg/memory/memory.test.ts|$a/memory/memory.test.tsx|86fa73104725e7beb66e2b68240f77135c9db0066288db093d36e546e2ff4634|tests kept in the Desk: the two cases over the Desk's memory settings screen
$pkg/memory/tools.ts|$a/memory/tools.ts|-|-
$pkg/planning/model.ts|$a/planning/model.ts|-|-
$pkg/planning/planning.test.ts|$a/planning/planning.test.tsx|a9ed37eeabc97d0f26219b5f588abb67e96340fca0ba77e6554788610efef910|tests kept in the Desk: the three cases over the Desk's chat store and plan card; agent#1: read_window in the host tool list; agent#5: search in the host tool list; agent#13: the two cases over Plan mode's Runtime list rewritten, the list being the gate's reads
$pkg/planning/policy.ts|$a/planning/policy.ts|19b020538a5aa5aee9675a7555528f6fb29665eae02ffefb1a28692e27066af4|agent#13: Plan mode's Runtime reads derived from the gate's ceiling, less the evaluation and the test run
$pkg/planning/tools.ts|$a/planning/tools.ts|7797f4ceb26b22e216e973ea2d1206a0ac43218a89836496ee2b2e6ec62217ef|injection points: Chat and ChatStore (types), readFile, dialectAPI, PreparationReply (type)
$pkg/questionFailure.ts|$a/questionFailure.ts|dba6a633523a01fc9499eaafd49a743b7c0c85fad7b7067309f114425f9c5da0|injection points: sourceMessage
$pkg/questions.ts|$a/questions.ts|-|-
$pkg/refutation.ts|$a/refutation.ts|67fbeab25bd3bd992ec798cac68f6d1325afd8082336b619b0bdb205745265bf|injection points: sourceMessage; agent#6: proposalNote, criticNote; agent#10: the critic's allow-list (CRITIC_TOOLS, criticRefusal), the default (criticRuns), the declared cases and the outcome comparison (readCriticCases, compareTheRest), the figures; agent#10 review rows 1, 2, 3 and 6: an uncompared case unsettled (incomplete), whole dispositions only to the test tool, the sets compared in any order, the critic's readers
$pkg/refutation.test.ts|$a/refutation.test.ts|-|-
$pkg/session.ts|$a/session.ts|5b43df17b626eee3a98d06e959bff82eb6118ca8dd1605ca0621ba0fcef31605|injection points: aiHeaders, AITarget, deskHeaders, currentLanguage, chassisUrl, DeskWebSocketTransport, socketProtocols, socketURL, the session store (NO_SESSION_MESSAGE, NoSession, discardBody, forgetSession, refusalCode, sessionBearer, sessionEnded, whenSessionEnds), EndpointKind (type)
$pkg/session.test.ts|$a/session.test.ts|34924a4a79163f9e5ff8a8ec179e934c176cb435deceb08dc31a416141f6a952|injection points: the Desk's session store and language replaced by configureHarness with the test host's sessionBearer and currentLanguage; paths: conformance/ is certification/; agent#37: what ends the session and what does not (the 401 classes, an opaque redirect) and a tool result in flight at the end; agent#42: the answer's headers held to the two content headers and the valid diagnostic ones
$pkg/thinking.ts|$a/thinking.ts|d633f808c0c6f9fea5cbcd8c283f70da492f0dd4813543dfa0cf613ad65ad491|injection points: sourceMessage, EndpointKind and ThinkingTier (types); agent#4: the tier spelled in the record's reasoning kind, nothing sent where it names none or the endpoint ignores it, said once; agent#10: the tier no longer decides whether the critic runs (runsRefutation and its ruling removed)
$pkg/thinking.test.ts|$a/thinking.test.ts|2f680347de20e72d884ef797a34875f100af1d0d24e80c7a8f74c49b0dfad3f3|injection points: EndpointKind and ThinkingTier (types); agent#10: the tier's refutation gate removed
$pkg/toolGate.ts|$a/toolGate.ts|83352875a4b38854e7c11801284b2c498b8f618b162d5575ee493a0d331f7a2e|injection points: ASSISTANT_TOOLS; agent#7: the six-tool ceiling, the test tool's argument allow-list and byte bounds, the refused tools and their reasons
$pkg/toolGate.test.ts|$a/toolGate.test.ts|56ed5ec30901cf819370683926556e82ce27a6feb938e39900c7bd55c4145f75|injection points: ASSISTANT_TOOLS; whitespace: the blank line at the end of the file, which the repository's whitespace check refuses; agent#7: the ceiling's wording; agent#53: the contract's gate vectors (tool-call/gate-frames.json, tool-offer/phase-offers.json) held to the gate and the phases' rules, a child within its parent's phase; agent#57: the gate test drives the child’s offer through delegation.ts’s childOffer
$pkg/certification/certification/clearsItsInterval.ts|$a/conformance/certification/clearsItsInterval.ts|-|-
$pkg/certification/certification/throwsWhileDraining.ts|$a/conformance/certification/throwsWhileDraining.ts|-|-
$pkg/certification/certification/touchesAfterRun.ts|$a/conformance/certification/touchesAfterRun.ts|-|-
$pkg/certification/certification/touchesOnLoad.ts|$a/conformance/certification/touchesOnLoad.ts|-|-
$pkg/certification/conformance.test.ts|$a/conformance/conformance.test.ts|a8cc808917acb751442db978ee441a239af0ffe513f6148e552cd710b5d2d0a9|injection points: EndpointKind and ThinkingTier (types); contract vectors: the event order and K2's offer read from contract/vectors/; agent#1: the Gemini legs count the context tools from the offer vector; agent#9: the strict-alternation leg and the endpoint capabilities a leg sets; agent#4: the text tool calls legs and the tool-call mode a leg sets; agent#7: the ceiling is six names and test_conformance is refused at the wire; agent#11: the request bodies a leg sent kept; ADR-0005: the host's parts placed in their layers and the stable prefix held on every request of every leg, memoryFor asked once, and the project-instructions leg, the recall rule's checks replaced; agent#10: the refutation legs ask for the critic, its offer is the contract's critic offer, a seeded wrong outcome refuted, no critic where not asked at any tier; agent#10 review rows 1 and 6: the incomplete case on the proposal, the critic's offer with its readers; agent#37: the recorder the fixture names is a file in this repository, run by the package's scripts; review of #47 row 1: K1b, a sealed leg with the environment's tracing variables set; agent#57: the strict-alternation code and the scenario server moved out to certification/strictAlternation.ts and certification/scenarioServer.ts; v0.3.0 release review: K1c, each of the four tracing variables alone; ADR-0005 review: the system text held to exactly the guidance through its last line, the host's section, then the tools or the state; agent#69: the placement check holds the three Anthropic cache breakpoints (cacheMisses); v0.4.0 pre-release batch (#76): the placement check made placement-aware (three Anthropic cache breakpoints under trailing-user, two under system-tail) and run over the critic's requests with CRITIC_SYSTEM as the guidance
$pkg/certification/runtime.json|$a/conformance/runtime.json|a0da1cd67321a4984000433918d1e365a5c63094bdc9f60242080b96706e4970|agent#37: the recorder and the verify command in the fixture name this repository's script
$pkg/certification/scenario.json|$a/conformance/scenario.json|-|-
$pkg/certification/scriptedModel.ts|$a/conformance/scriptedModel.ts|e3d30840d69d8c277dc9adeb552b36c17c09ff98219814e543070d32a1144e14|injection points: EndpointKind (type); agent#4: an endpoint without native tool calls (toolCalls: text)
$pkg/certification/scriptedModel.test.ts|$a/conformance/scriptedModel.test.ts|-|-
$pkg/certification/scriptedServer.ts|$a/conformance/scriptedServer.ts|-|-
$pkg/contract/catalog.json|internal/aifailure/catalog.json|-|-
$pkg/contract/policy.json|internal/aicontext/policy.json|f25c904e39887f87f4632ee7e2f5435a704f84fdf9de2aa4c7d2cddc70b7109c|agent#10: the critic's default, adversarialReview false, which the Desk's table takes in its swap
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
$pkg/host.ts
$pkg/host.test.ts
$pkg/entries/contract.ts
$pkg/certification/index.ts
$pkg/scripts/certify.mjs
$pkg/testing/host.ts
$pkg/testing/question.ts
$pkg/testing/setup.ts
$pkg/context/reader.ts
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
$pkg/scripts/bundle-size.mjs
$pkg/sourceArguments.ts
$pkg/sourceArguments.test.ts
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

echo "parity: $pkg against the Desk at $(git -C "$desk" rev-parse --short HEAD 2>/dev/null || echo 'an unversioned checkout')"
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
