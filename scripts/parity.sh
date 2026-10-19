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
$pkg/completion.ts|$a/completion.ts|-|-
$pkg/completion.test.ts|$a/completion.test.ts|-|-
$pkg/context/context.test.ts|$a/context/context.test.ts|d8ffb8050b53875e4e23db9d8d0acc9f3a7326645771adaae826cefece500b35|agent#12: the compacted summary's steps read through their data block
$pkg/context/ledger.ts|$a/context/ledger.ts|49cbcb45dc776e158c8dd617373cfbf719a072273263c352c9544a8584a10b5e|declaration emit: Entry exported (a type, no runtime change) so tsc can write controller.d.ts
$pkg/context/managed.ts|$a/context/managed.ts|-|-
$pkg/context/policy.ts|$a/context/policy.ts|f4454bd1bdbcc65edebe10778f48448066b270f272f6d2c64340f89ce2cc50d2|the Go-side copy: policy.json read from the package's contract/; agent#2: the turn clock and the tool-call deadline (TIME_DEFAULTS); agent#7: the test tool's bounds (TEST_DEFAULTS)
$pkg/context/recovery.integration.test.ts|$a/context/recovery.integration.test.ts|-|-
$pkg/context/session.ts|$a/context/session.ts|d8a919b22e4da1e182c69f59c3f5547e42e2c08fd17e6fa82caf6a907e2e58ce|agent#7: the per-turn test-call cap (claimTestCall); agent#12: the run's marker, frame, framed results read back in compaction and the summary framed
$pkg/context/toolPolicy.ts|$a/context/toolPolicy.ts|-|-
$pkg/context/toolPolicy.test.ts|$a/context/toolPolicy.test.ts|-|-
$pkg/delegation.ts|$a/delegation.ts|-|-
$pkg/delegation.test.ts|$a/delegation.test.ts|c2a97f7509cca43de3522ab5c2b00de6846f87fa54c00236d957eb4ed9aa4bb2|test kept in the Desk: the case over the Desk's response history; the Desk's chat-history reader replaced by a fold of delegated_task events with its interruption rule
$pkg/delegationContract.ts|$a/delegationContract.ts|-|-
$pkg/engine.ts|$a/engine.ts|232d695eac9dcdcda35f82880acabad78a3954816fe9241606a520f41166c46e|injection points: Language, AssistantEngine, EndpointKind (types); agent#6: the proposal's note (ProposalNote)
$pkg/engines/codex/codex.test.ts|$a/engines/codex/codex.test.ts|787f3146719642a4db6fda8aade045ba5929c75b22d48265795e594b30b8d17b|paths: conformance/ is certification/, testing/question reads the contract vector; test kept in the Desk: the case over the Desk's dialect chat tools; agent#6: the whole answer kept, the critic's note, propose_pack; agent#12: read_context's answer read through its data block
$pkg/engines/codex/index.ts|$a/engines/codex/index.ts|997b1aec3da1a94b87d80231793e4c672a943594446c636eeb9183c15cd9c108|injection points: assistantLanguageInstructions; agent#6: the whole answer kept, the critic's note, propose_pack; agent#2: the tool-call deadline; agent#12: a reading tool's answer framed, the data rule in the instructions
$pkg/engines/contract.ts|$a/engines/contract.ts|3e7a3ec10a2af6f9fe14a972b0946279e6dcf71480b1b1ef3fb36c12b8e25ed5|injection points: EndpointKind (type); agent#6: why a proposal was not read, the whole answer, the reformat request, propose_pack; agent#2: withDeadline, toolTimeout, limitText
$pkg/engines/contract.test.ts|$a/engines/contract.test.ts|-|-
$pkg/engines/index.ts|$a/engines/index.ts|6f19d51e6ae2890ff7ea6f7156ed0800ab9681c73e85d9c509d761b201813b58|injection points: AssistantEngine (type)
$pkg/engines/vercel/channel.ts|$a/engines/vercel/channel.ts|-|-
$pkg/engines/vercel/completion.test.ts|$a/engines/vercel/completion.test.ts|-|-
$pkg/engines/vercel/engine.test.ts|$a/engines/vercel/engine.test.ts|ee45c2ad5f92bf94f795a18a4d5a3709a3b9f55e1b9bd45572808557e685fc47|injection points: ASSISTANT_ENGINES, ASSISTANT_TOOLS; paths: LANGUAGES from the test host, testing/question reads the contract vector; agent#6: the reformat request and the whole answer, the critic's note, propose_pack
$pkg/engines/vercel/index.ts|$a/engines/vercel/index.ts|-|-
$pkg/engines/vercel/loop.ts|$a/engines/vercel/loop.ts|8e44794b21c4c24d955d91c388a722deb1a287b419a6e3d61ae289f0d878d1cf|injection points: assistantLanguageInstructions, AssistantEngine and EndpointKind (types); agent#6: the reformat request, the whole answer kept, the critic's note, propose_pack; agent#2: the model-request retry, the turn clock, the tool-call deadline; agent#12: a reading tool's answer framed through toModelOutput, the data rule in the instructions and the critic's
$pkg/engines/vercel/relay.ts|$a/engines/vercel/relay.ts|f6c7c2cc27588127f0a4a33f646ee358c64a3e3e3f139b55054eb57d35aca085|injection points: EndpointKind (type); agent#3: a whole answer that broke while read, or is not JSON, reported by class
$pkg/execution/checkArtifact.ts|$a/execution/checkArtifact.ts|-|-
$pkg/execution/checkArtifact.test.ts|$a/execution/checkArtifact.test.ts|-|-
$pkg/execution/controller.ts|$a/execution/controller.ts|86020c34b892cb1fbc8864464839c1028d0271bb3d8d7bd7e3bbc0bd4fda517f|agent#2: a refused attempt's row in the completion record and the attempt number per call (recordRefusal, beginAttempt)
$pkg/execution/correctArtifact.ts|$a/execution/correctArtifact.ts|-|-
$pkg/execution/correctArtifact.test.ts|$a/execution/correctArtifact.test.ts|-|-
$pkg/execution/execution.test.ts|$a/execution/execution.test.ts|83b025c05b75843046bb03e0df56094a27fc9b680e32073583fa7db2e791161c|test kept in the Desk: the case over the Desk's chat checkpoint
$pkg/execution/progress.ts|$a/execution/progress.ts|-|-
$pkg/execution/progress.test.ts|$a/execution/progress.test.ts|-|-
$pkg/execution/state.ts|$a/execution/state.ts|1bcc57a2aa31a7940fd32ae951aab294b66b78c893caa83dda72e90cb1930b1e|agent#2: the completion record's attempt, failure and waitMs (CompletionRecord, readExecution)
$pkg/failures.ts|$a/failures.ts|b8deb6c05e890f1642f210dc36906267f2014a8b02586734cd0826950bd67a69|the Go-side copy: catalog.json read from the package's contract/; agent#2: the retried refusals by name (canRetryRefusal, refusalCode)
$pkg/failures.test.ts|$a/failures.test.ts|-|-
$pkg/failures.integration.test.ts|$a/failures.integration.test.ts|bade99f8d9b602b16789f9835f1e1abdb0a0ef7a749afc41ce43838ef0095a4c|agent#2: Retry-After 120, past the retry wait limit, so the refusal is still reported after one request
$pkg/geminiSchema.ts|$a/geminiSchema.ts|-|-
$pkg/geminiSchema.test.ts|$a/geminiSchema.test.ts|8e409a5b5b90560de63bbeee2565a1d698d0a271e471e31b4bfa1e5abaed03ee|paths: conformance/ is certification/
$pkg/memory/client.ts|$a/memory/client.ts|447add57ae764bb05abe84cdaf49c22420041d6b938c52e238c3b22beec07293|injection points: answer, deskFetch
$pkg/memory/memory.test.ts|$a/memory/memory.test.tsx|86fa73104725e7beb66e2b68240f77135c9db0066288db093d36e546e2ff4634|tests kept in the Desk: the two cases over the Desk's memory settings screen
$pkg/memory/tools.ts|$a/memory/tools.ts|-|-
$pkg/planning/model.ts|$a/planning/model.ts|-|-
$pkg/planning/planning.test.ts|$a/planning/planning.test.tsx|be8953f1710d4c4dcbf21ee25983dd23dc4e111a0e1749599bfda46199f38fb7|tests kept in the Desk: the three cases over the Desk's chat store and plan card
$pkg/planning/policy.ts|$a/planning/policy.ts|-|-
$pkg/planning/tools.ts|$a/planning/tools.ts|7797f4ceb26b22e216e973ea2d1206a0ac43218a89836496ee2b2e6ec62217ef|injection points: Chat and ChatStore (types), readFile, dialectAPI, PreparationReply (type)
$pkg/questionFailure.ts|$a/questionFailure.ts|dba6a633523a01fc9499eaafd49a743b7c0c85fad7b7067309f114425f9c5da0|injection points: sourceMessage
$pkg/questions.ts|$a/questions.ts|-|-
$pkg/refutation.ts|$a/refutation.ts|63f7ede50e9c49c4a37d21d1e2b05edd20780d759ccc2344e70ba823b3cec4ed|injection points: sourceMessage; agent#6: proposalNote, criticNote
$pkg/refutation.test.ts|$a/refutation.test.ts|-|-
$pkg/session.ts|$a/session.ts|5b43df17b626eee3a98d06e959bff82eb6118ca8dd1605ca0621ba0fcef31605|injection points: aiHeaders, AITarget, deskHeaders, currentLanguage, chassisUrl, DeskWebSocketTransport, socketProtocols, socketURL, the session store (NO_SESSION_MESSAGE, NoSession, discardBody, forgetSession, refusalCode, sessionBearer, sessionEnded, whenSessionEnds), EndpointKind (type)
$pkg/session.test.ts|$a/session.test.ts|f3400e960cab12b82f9f2491827268be9aabc1b5adfb5d5a1c1c70f40cc51133|injection points: the Desk's session store and language replaced by configureHarness with the test host's sessionBearer and currentLanguage; paths: conformance/ is certification/
$pkg/thinking.ts|$a/thinking.ts|0a05ccb19f2f89cddf8efdaf4fb4fd03264219ac8469083a722b4b4a5151a892|injection points: sourceMessage, EndpointKind and ThinkingTier (types)
$pkg/thinking.test.ts|$a/thinking.test.ts|95558d0f77c51b73671db0d36f50c00cfb0108959939f3131c10029746d6cd24|injection points: EndpointKind and ThinkingTier (types)
$pkg/toolGate.ts|$a/toolGate.ts|af760477e011a6ba940325cd3679f65895a897aea401e258b48959eca213adb5|injection points: ASSISTANT_TOOLS; agent#7: the six-tool ceiling, the test tool's argument allow-list and byte bounds, the refused tools and their reasons
$pkg/toolGate.test.ts|$a/toolGate.test.ts|378089aa27c3e5e6c67776fcd4c709a1e7d412415f527d9e2d3b2be56cba62c8|injection points: ASSISTANT_TOOLS; whitespace: the blank line at the end of the file, which the repository's whitespace check refuses; agent#7: the ceiling's wording
$pkg/certification/certification/clearsItsInterval.ts|$a/conformance/certification/clearsItsInterval.ts|-|-
$pkg/certification/certification/throwsWhileDraining.ts|$a/conformance/certification/throwsWhileDraining.ts|-|-
$pkg/certification/certification/touchesAfterRun.ts|$a/conformance/certification/touchesAfterRun.ts|-|-
$pkg/certification/certification/touchesOnLoad.ts|$a/conformance/certification/touchesOnLoad.ts|-|-
$pkg/certification/conformance.test.ts|$a/conformance/conformance.test.ts|a3400d78944399a90ecc48fc6a8b65db5b37b7306c38396733a66210949d0b76|injection points: EndpointKind and ThinkingTier (types); contract vectors: the event order and K2's offer read from contract/vectors/; agent#7: the ceiling is six names and test_conformance is refused at the wire
$pkg/certification/runtime.json|$a/conformance/runtime.json|-|-
$pkg/certification/scenario.json|$a/conformance/scenario.json|-|-
$pkg/certification/scriptedModel.ts|$a/conformance/scriptedModel.ts|af92198d6032d82d4c0a57ac1aa7e1c044fc18d442b85f8ace19d3f5488e971e|injection points: EndpointKind (type)
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
$pkg/engines/vercel/retry.ts
$pkg/engines/vercel/bounds.test.ts
$pkg/framing.ts
$pkg/framing.test.ts
$pkg/testCases.test.ts
$pkg/engines/failurePaths.test.ts
$pkg/testing/scripted.ts
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
