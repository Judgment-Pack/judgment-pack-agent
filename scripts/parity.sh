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
$pkg/agent.ts|$a/agent.ts|-|-
$pkg/agentTransport.ts|$a/agentTransport.ts|2eebc32ad049919fa652a600e0cb0ed8d3220562a0cf963b6f87e90e4d38503a|injection points: AITarget, deskSocketURL, chassisUrl, socketProtocols, sessionBearer, whenSessionEnds
$pkg/agentTransport.test.ts|$a/agentTransport.test.ts|df8c20487e36f88e9c14eea65b142a7bb90be82990aea400756669423af7630f|injection points: the Desk's two module mocks become one mock of host.ts
$pkg/completion.ts|$a/completion.ts|b2bf689ca718a8a8d1481fdce145d42b52f3b6f8ff20d64b54f35fe3d1d3d46e|agent#9: the prompt cache reads and writes (cacheReadTokens, cacheWriteTokens) read from the SDK usage and kept
$pkg/completion.test.ts|$a/completion.test.ts|-|-
$pkg/context/context.test.ts|$a/context/context.test.ts|2d0d52597eba4331879b66a585fec3d99fd3e89a3b6c91f64ea88ae610e61927|agent#1: the reference envelope names read_window; agent#12: the compacted summary's steps read through their data block
$pkg/context/ledger.ts|$a/context/ledger.ts|86c8641cb6914db260d48bccbd4bbb2da40281db95559866cb0fb58d595b177c|declaration emit: Entry exported (a type, no runtime change) so tsc can write controller.d.ts; agent#1: the reader, read_window, among the ledger tools and named in the reference envelope (readWith); agent#5: search among the ledger tools, the attachments the host retained, an image item read whole (a reference followed to the image it names) or answered with the line saying why it cannot be shown, search naming such an item, a retained image by reference; agent#8: the entries in a store, a run's own or a chat's turn (LedgerStore, EntryStore), a read of an evicted entry saying why, a chat run's checkpoint carrying the entries it retained or read, a restore into the same chat, release and isolate; agent#15: a store may adopt an entry the run retains itself with the same content (adopt, a delegated child's view of the chat)
$pkg/context/managed.ts|$a/context/managed.ts|5f6b2332c9e38987cf4a400593cec94818093dda577bf69d4a48745706cf102a|agent#8: a run's end closes its context (a chat's turn closes, a ledger of the run's own is cleared), and the comment on how long derived context lives
$pkg/context/policy.ts|$a/context/policy.ts|56557a9dcab0e84a3dba69d097f261e634657a395d09a4dc3e26a0606832b659|the Go-side copy: policy.json read from the package's contract/; agent#1: readWindowBytes and encodedPrefix, the read window; agent#2: the turn clock and the tool-call deadline (TIME_DEFAULTS); agent#4: contextPolicy takes the capability record's window and output cap; agent#5: the images one request carries (IMAGE_DEFAULTS); agent#7: the test tool's bounds (TEST_DEFAULTS); agent#8: the bounds on a chat's index (INDEX_DEFAULTS); agent#11: memoryBytes reserved only while the host has not said memory is off
$pkg/context/recovery.integration.test.ts|$a/context/recovery.integration.test.ts|288ed58c3c24838e24e4dc686b88f00af7a7ea4d53c5ec353d65597f662aa56c|agent#5 and agent#12: the fixture's window 17,408 tokens, not 16,384, because this harness sends fixed text the Desk does not (the search tool, the data rule) and the compacted summary must still hold the schema result's handle
$pkg/context/session.ts|$a/context/session.ts|c025b77e27dc65ad1d387f2eeb8b80b32cddbc33830633437688939209bec909|agent#1: read_window reserved; agent#9: the execution guidance no longer appended to the prompt (each adapter places it), currentState; agent#4: the capability record resolved once for the run, the budget from it; agent#5: search reserved, the host's documents retained as attachments; agent#7: the per-turn test-call cap (claimTestCall); agent#12: the run's marker, frame, framed results read back in compaction and the summary framed; agent#8: the run's turn on a chat-scoped context opened with the run and closed at its end or a failed start, the index of earlier results counted against the prompt's allowance, the session tools that read kept, the critic isolated from the chat; the first message's text parts made in one place (opening; the agent prompt's index, indexed), the index cut to the room left, at most INDEX_SHARE of the allowance, and made only for the chat's own turn; agent#11: the host's word on memory read into the policy; the person's message as the first text part where the prompt is previewed, a delegated child's objective as its first text part; the context inputs chat, runtime, memory and objective; agent#14 and agent#15: the context input link, by which the delegation coordinator reaches a run's execution, and chat as a ChatScope, a delegated child's view of the chat among them
$pkg/context/toolPolicy.ts|$a/context/toolPolicy.ts|182e5189eb6c60f0bc986768966eb2f936b737d63e4ffe5499b782e3e67cd13a|agent#8: runtime, a result that depends on the Runtime serving it, and what immutable means under a chat-scoped context
$pkg/context/toolPolicy.test.ts|$a/context/toolPolicy.test.ts|-|-
$pkg/delegation.ts|$a/delegation.ts|88941b55d92e1667475bfb353cc2d22c44a24e844aec3d978487130d4607b971|agent#14: the delegate call runs the batch, awaited in the parent's step on vercel, and on codex the response's next run given the call's result and nothing else; more than one batch, one at a time; the parent's execution owning the shared request count; a finished child resumed with its context; agent#15: context by reference through a view of the parent's chat, each profile's model, budget, time limit and Runtime tools, a child's test calls charged to its parent's turn; agent#13: long findings kept whole with their head and a read_window cursor, the summarize step, the tool-call cap stated in the state; agent#7 and agent#11 as before: the turn's test calls shared, a child under its parent's word on memory, its objective its request text (context.objective)
$pkg/delegation.test.ts|$a/delegation.test.ts|32d68598a16ee066085480e1aff7639ed756d19da1d01b6786a14c6ab229d729|test kept in the Desk: the case over the Desk's response history; the Desk's chat-history reader replaced by a fold of delegated_task events with its interruption rule; agent#14: the cases rewritten for one parent run (findings as the call's result; on codex the next run given the result), and new cases for one run with native tool messages, a second batch, the batch bound, resume, and codex's next run reading by reference; agent#15: the request with no copied context, and new cases for references, the profiles, the child model, six page reads and the author-test child; agent#13: long findings, the summarize step and the stated cap
$pkg/delegationContract.ts|$a/delegationContract.ts|e20a50e207521e855c8cb01523c4aa48d1e4ca153ae72da5c9fb8caa3bbeb8b3|agent#14, agent#15 and agent#13: references, resume and summarize in the request and no copied context; the task record's budget, model, references, resumes, more and summarized; each profile's model, budget and Runtime tools, the author-test profile, readProfiles; MAX_TOOLS and MAX_BATCHES; the instructions and the tool's description
$pkg/engine.ts|$a/engine.ts|ba2caefc5fa1a12270716a102817af216033c963a2a801dcb284ad4205dff0f8|injection points: Language, AssistantEngine, EndpointKind (types); agent#6: the proposal's note (ProposalNote); agent#4: a session may carry members of its endpoint's capability record; agent#5: a result's content item is open (image parts)
$pkg/engines/codex/codex.test.ts|$a/engines/codex/codex.test.ts|f7c5b0d559f129e3da80b3838b45089d8f9814a661456b6c2b49f1d0443554e8|paths: conformance/ is certification/, testing/question reads the contract vector; test kept in the Desk: the case over the Desk's dialect chat tools; agent#1: the reference envelope names read_window; agent#6: the whole answer kept, the critic's note, propose_pack; agent#12: read_context's answer read through its data block
$pkg/engines/codex/index.ts|$a/engines/codex/index.ts|bec0871976dae1cf66d18e756489782211aacb3f640f9474a8afa34a61a13023|injection points: assistantLanguageInstructions; agent#9: the agent prompt carries currentState and the execution guidance itself; agent#6: the whole answer kept, the critic's note, propose_pack; agent#2: the tool-call deadline; agent#5: an image in a result reaches the agent as the host's text rendering; agent#12: a reading tool's answer framed, the data rule in the instructions; agent#8: a chat's index at the end of the agent prompt, in the room the allowance leaves
$pkg/engines/contract.ts|$a/engines/contract.ts|1315bc657069bc56e63c1504db245a879220e8d6765100ac58ace0305703b233|injection points: EndpointKind (type); agent#6: why a proposal was not read, the whole answer, the reformat request, propose_pack; agent#2: withDeadline, toolTimeout, limitText; agent#4: the schema shown by the record's tool-schema subset, the text tool call read by the proposal fence's closed reader, the tools described in text
$pkg/engines/contract.test.ts|$a/engines/contract.test.ts|-|-
$pkg/engines/index.ts|$a/engines/index.ts|6f19d51e6ae2890ff7ea6f7156ed0800ab9681c73e85d9c509d761b201813b58|injection points: AssistantEngine (type)
$pkg/engines/vercel/channel.ts|$a/engines/vercel/channel.ts|-|-
$pkg/engines/vercel/completion.test.ts|$a/engines/vercel/completion.test.ts|-|-
$pkg/engines/vercel/engine.test.ts|$a/engines/vercel/engine.test.ts|4fe6ad9c4bfaa194e877ae5c9d48891d40bab4d011c9475aba0957dfe5f654cc|injection points: ASSISTANT_ENGINES, ASSISTANT_TOOLS; paths: LANGUAGES from the test host, testing/question reads the contract vector; agent#1: read_window in the offered tool lists; agent#6: the reformat request and the whole answer, the critic's note, propose_pack; agent#5: search in the offered tool lists
$pkg/engines/vercel/index.ts|$a/engines/vercel/index.ts|-|-
$pkg/engines/vercel/loop.ts|$a/engines/vercel/loop.ts|b1b2b3f7aa3bd44e56466ca3f83a57e99b571e2f69b600f0ccccfa17971b1628|injection points: assistantLanguageInstructions, AssistantEngine and EndpointKind (types); agent#9: a stable prefix with the changing state placed by endpoint (the person's message on the first request, then a trailing user message or the system tail), the critic's note headed, Anthropic cache breakpoints, the tier held for the run; agent#6: the reformat request, the whole answer kept, the critic's note, propose_pack; agent#2: the model-request retry, the turn clock, the tool-call deadline; agent#4: the capability record read wherever the loop decided by family (thinking, schema subset, native or text tool calls, one call per turn, streaming quirks, the state placement), the text tool path in the loop and the critic; agent#5: images routed by vision (shown on the text path at a strict turn order), beside their results, the newest held to the per-request image bounds; agent#12: a reading tool's answer framed through toModelOutput and in the text path's result message, the data rule in the instructions and the critic's; agent#8 and agent#11: the first message's text parts from the session context (opening: the request text first, a chat's index last); agent#14: delegate_tasks an awaited call in the parent's step, reported as a call and a result, with no tool-call deadline
$pkg/engines/vercel/relay.ts|$a/engines/vercel/relay.ts|789564820a5cd98b08fa5695d411fb0e7a62da65b8f3030408af8b6cf88dee7d|injection points: EndpointKind (type); agent#4: the pinned reasoning member read and carried back, a whole answer asked of an endpoint that answers whole; agent#3: a whole answer that broke while read, or is not JSON, reported by class
$pkg/execution/checkArtifact.ts|$a/execution/checkArtifact.ts|-|-
$pkg/execution/checkArtifact.test.ts|$a/execution/checkArtifact.test.ts|-|-
$pkg/execution/controller.ts|$a/execution/controller.ts|bd3b394ed22bf42416db0e0c1b3de4a2d8aac3b961df46c632cb273ebd55665a|agent#9: requestState, the part of the guidance that changes between requests; agent#2: a refused attempt's row in the completion record and the attempt number per call (recordRefusal, beginAttempt); agent#8: wrapRead keeps a session read's answer in a chat-scoped context and serves an immutable one from the chat's read cache; agent#13: the tool-call cap in the request state (toolAllowance); agent#15: a delegated child's first allowance (limitTo)
$pkg/execution/correctArtifact.ts|$a/execution/correctArtifact.ts|-|-
$pkg/execution/correctArtifact.test.ts|$a/execution/correctArtifact.test.ts|-|-
$pkg/execution/execution.test.ts|$a/execution/execution.test.ts|83b025c05b75843046bb03e0df56094a27fc9b680e32073583fa7db2e791161c|test kept in the Desk: the case over the Desk's chat checkpoint
$pkg/execution/progress.ts|$a/execution/progress.ts|-|-
$pkg/execution/progress.test.ts|$a/execution/progress.test.ts|-|-
$pkg/execution/state.ts|$a/execution/state.ts|17562390282e43022c62c3af788cf30119302e8dd0e73e6f01ee1c0eaf3ccb8e|agent#2: the completion record's attempt, failure and waitMs (CompletionRecord, readExecution); agent#7: the turn's test-call count (testCalls) carried in the checkpoint
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
$pkg/refutation.ts|$a/refutation.ts|63f7ede50e9c49c4a37d21d1e2b05edd20780d759ccc2344e70ba823b3cec4ed|injection points: sourceMessage; agent#6: proposalNote, criticNote
$pkg/refutation.test.ts|$a/refutation.test.ts|-|-
$pkg/session.ts|$a/session.ts|5b43df17b626eee3a98d06e959bff82eb6118ca8dd1605ca0621ba0fcef31605|injection points: aiHeaders, AITarget, deskHeaders, currentLanguage, chassisUrl, DeskWebSocketTransport, socketProtocols, socketURL, the session store (NO_SESSION_MESSAGE, NoSession, discardBody, forgetSession, refusalCode, sessionBearer, sessionEnded, whenSessionEnds), EndpointKind (type)
$pkg/session.test.ts|$a/session.test.ts|f3400e960cab12b82f9f2491827268be9aabc1b5adfb5d5a1c1c70f40cc51133|injection points: the Desk's session store and language replaced by configureHarness with the test host's sessionBearer and currentLanguage; paths: conformance/ is certification/
$pkg/thinking.ts|$a/thinking.ts|a6fc264737190ac85f4a9de54323d12b9700b3a7abd2a6e7fd6782d94c3a5c54|injection points: sourceMessage, EndpointKind and ThinkingTier (types); agent#4: the tier spelled in the record's reasoning kind, nothing sent where it names none or the endpoint ignores it, said once
$pkg/thinking.test.ts|$a/thinking.test.ts|95558d0f77c51b73671db0d36f50c00cfb0108959939f3131c10029746d6cd24|injection points: EndpointKind and ThinkingTier (types)
$pkg/toolGate.ts|$a/toolGate.ts|83352875a4b38854e7c11801284b2c498b8f618b162d5575ee493a0d331f7a2e|injection points: ASSISTANT_TOOLS; agent#7: the six-tool ceiling, the test tool's argument allow-list and byte bounds, the refused tools and their reasons
$pkg/toolGate.test.ts|$a/toolGate.test.ts|378089aa27c3e5e6c67776fcd4c709a1e7d412415f527d9e2d3b2be56cba62c8|injection points: ASSISTANT_TOOLS; whitespace: the blank line at the end of the file, which the repository's whitespace check refuses; agent#7: the ceiling's wording
$pkg/certification/certification/clearsItsInterval.ts|$a/conformance/certification/clearsItsInterval.ts|-|-
$pkg/certification/certification/throwsWhileDraining.ts|$a/conformance/certification/throwsWhileDraining.ts|-|-
$pkg/certification/certification/touchesAfterRun.ts|$a/conformance/certification/touchesAfterRun.ts|-|-
$pkg/certification/certification/touchesOnLoad.ts|$a/conformance/certification/touchesOnLoad.ts|-|-
$pkg/certification/conformance.test.ts|$a/conformance/conformance.test.ts|2a8f935dfd1a96fc0c71966337edaac5a7ad5ad669ee6326d9966e071f406210|injection points: EndpointKind and ThinkingTier (types); contract vectors: the event order and K2's offer read from contract/vectors/; agent#1: the Gemini legs count the context tools from the offer vector; agent#9: the strict-alternation leg and the endpoint capabilities a leg sets; agent#4: the text tool calls legs and the tool-call mode a leg sets; agent#7: the ceiling is six names and test_conformance is refused at the wire; agent#11: the recall rule applied to every request of every leg, the request bodies a leg sent kept
$pkg/certification/runtime.json|$a/conformance/runtime.json|-|-
$pkg/certification/scenario.json|$a/conformance/scenario.json|-|-
$pkg/certification/scriptedModel.ts|$a/conformance/scriptedModel.ts|e3d30840d69d8c277dc9adeb552b36c17c09ff98219814e543070d32a1144e14|injection points: EndpointKind (type); agent#4: an endpoint without native tool calls (toolCalls: text)
$pkg/certification/scriptedModel.test.ts|$a/conformance/scriptedModel.test.ts|-|-
$pkg/certification/scriptedServer.ts|$a/conformance/scriptedServer.ts|-|-
$pkg/contract/catalog.json|internal/aifailure/catalog.json|-|-
$pkg/contract/policy.json|internal/aicontext/policy.json|-|-
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
$pkg/certification/recall.ts
$pkg/memory/recall.test.ts
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
