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
$pkg/context/context.test.ts|$a/context/context.test.ts|4e3433cb73aa6bbe78b7225472198e6620d116ff3ff73df65740c5b9ce3e1128|agent#1: the reference envelope names read_window
$pkg/context/ledger.ts|$a/context/ledger.ts|e7b17e98dc903b1036f636db8b06333d1d49249b1a5dbf4db013cba59287c34f|declaration emit: Entry exported (a type, no runtime change) so tsc can write controller.d.ts; agent#1: the reader, read_window, among the ledger tools and named in the reference envelope (readWith)
$pkg/context/managed.ts|$a/context/managed.ts|-|-
$pkg/context/policy.ts|$a/context/policy.ts|1e64cd928ef29f1aed790082e61a7ba14fd46f7b2bfc240f788a6da2a8a6edc7|the Go-side copy: policy.json read from the package's contract/; agent#1: readWindowBytes and encodedPrefix, the read window; agent#2: the turn clock and the tool-call deadline (TIME_DEFAULTS)
$pkg/context/recovery.integration.test.ts|$a/context/recovery.integration.test.ts|-|-
$pkg/context/session.ts|$a/context/session.ts|13f336a37d5d281e2dbb9e937e9585fde210628eddfcb454f5f3ee5e52b47ced|agent#1: read_window reserved; agent#9: the execution guidance no longer appended to the prompt (each adapter places it), currentState
$pkg/context/toolPolicy.ts|$a/context/toolPolicy.ts|-|-
$pkg/context/toolPolicy.test.ts|$a/context/toolPolicy.test.ts|-|-
$pkg/delegation.ts|$a/delegation.ts|-|-
$pkg/delegation.test.ts|$a/delegation.test.ts|c2a97f7509cca43de3522ab5c2b00de6846f87fa54c00236d957eb4ed9aa4bb2|test kept in the Desk: the case over the Desk's response history; the Desk's chat-history reader replaced by a fold of delegated_task events with its interruption rule
$pkg/delegationContract.ts|$a/delegationContract.ts|-|-
$pkg/engine.ts|$a/engine.ts|232d695eac9dcdcda35f82880acabad78a3954816fe9241606a520f41166c46e|injection points: Language, AssistantEngine, EndpointKind (types); agent#6: the proposal's note (ProposalNote)
$pkg/engines/codex/codex.test.ts|$a/engines/codex/codex.test.ts|1848b19c50d4fbdddabb52f4261bddb29150158c41677889ac7f0098a8b27eae|paths: conformance/ is certification/, testing/question reads the contract vector; test kept in the Desk: the case over the Desk's dialect chat tools; agent#1: the reference envelope names read_window; agent#6: the whole answer kept, the critic's note, propose_pack
$pkg/engines/codex/index.ts|$a/engines/codex/index.ts|53cdbb95e71cb3dc07d99aefb354a944e8c64165527f897984d017ea7a574c9b|injection points: assistantLanguageInstructions; agent#9: the agent prompt carries currentState and the execution guidance itself; agent#6: the whole answer kept, the critic's note, propose_pack; agent#2: the tool-call deadline
$pkg/engines/contract.ts|$a/engines/contract.ts|3e7a3ec10a2af6f9fe14a972b0946279e6dcf71480b1b1ef3fb36c12b8e25ed5|injection points: EndpointKind (type); agent#6: why a proposal was not read, the whole answer, the reformat request, propose_pack; agent#2: withDeadline, toolTimeout, limitText
$pkg/engines/contract.test.ts|$a/engines/contract.test.ts|-|-
$pkg/engines/index.ts|$a/engines/index.ts|6f19d51e6ae2890ff7ea6f7156ed0800ab9681c73e85d9c509d761b201813b58|injection points: AssistantEngine (type)
$pkg/engines/vercel/channel.ts|$a/engines/vercel/channel.ts|-|-
$pkg/engines/vercel/completion.test.ts|$a/engines/vercel/completion.test.ts|-|-
$pkg/engines/vercel/engine.test.ts|$a/engines/vercel/engine.test.ts|d96e938946be6cf9f764d534762c1a8de3d2854c687b5fab548e6506f6040a8b|injection points: ASSISTANT_ENGINES, ASSISTANT_TOOLS; paths: LANGUAGES from the test host, testing/question reads the contract vector; agent#1: read_window in the offered tool lists; agent#6: the reformat request and the whole answer, the critic's note, propose_pack
$pkg/engines/vercel/index.ts|$a/engines/vercel/index.ts|-|-
$pkg/engines/vercel/loop.ts|$a/engines/vercel/loop.ts|fdae56dbd66a236e51eb6f1828199034fea535847ffc7dd29e02f1b22d84b63e|injection points: assistantLanguageInstructions, AssistantEngine and EndpointKind (types); agent#9: a stable prefix with the changing state placed by endpoint (the person's message on the first request, then a trailing user message or the system tail), the critic's note headed, Anthropic cache breakpoints, the tier held for the run; agent#6: the reformat request, the whole answer kept, the critic's note, propose_pack; agent#2: the model-request retry, the turn clock, the tool-call deadline
$pkg/engines/vercel/relay.ts|$a/engines/vercel/relay.ts|a4cf2c7f1ab2de26ebc85fbca87774f86ea6a385d69e57fd66d2cf8a20b7ef88|injection points: EndpointKind (type)
$pkg/execution/checkArtifact.ts|$a/execution/checkArtifact.ts|-|-
$pkg/execution/checkArtifact.test.ts|$a/execution/checkArtifact.test.ts|-|-
$pkg/execution/controller.ts|$a/execution/controller.ts|64657afbb459154d521c54eeb758a599853dabc89e9117cba0675aba5a45456c|agent#9: requestState, the part of the guidance that changes between requests; agent#2: a refused attempt's row in the completion record and the attempt number per call (recordRefusal, beginAttempt)
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
$pkg/planning/planning.test.ts|$a/planning/planning.test.tsx|f071a06c39c415066fd50a9e82e4303d470999b1db53a4a8e33932c7db443c69|tests kept in the Desk: the three cases over the Desk's chat store and plan card; agent#1: read_window in the host tool list
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
$pkg/toolGate.ts|$a/toolGate.ts|ed7f72456dd7d84783628c57af30f405f00ef1276d2d36e3a921c1e6d91589a4|injection points: ASSISTANT_TOOLS
$pkg/toolGate.test.ts|$a/toolGate.test.ts|052142bb695e6a249e68c316f1df2cd8167a94b8fa2e6a44125669dcd2b392b6|injection points: ASSISTANT_TOOLS; whitespace: the blank line at the end of the file, which the repository's whitespace check refuses
$pkg/certification/certification/clearsItsInterval.ts|$a/conformance/certification/clearsItsInterval.ts|-|-
$pkg/certification/certification/throwsWhileDraining.ts|$a/conformance/certification/throwsWhileDraining.ts|-|-
$pkg/certification/certification/touchesAfterRun.ts|$a/conformance/certification/touchesAfterRun.ts|-|-
$pkg/certification/certification/touchesOnLoad.ts|$a/conformance/certification/touchesOnLoad.ts|-|-
$pkg/certification/conformance.test.ts|$a/conformance/conformance.test.ts|9a53f21c0236f9c22d75fdcb894db1856f66dd82fbf8e84dcc1499f2b62a0ed2|injection points: EndpointKind and ThinkingTier (types); contract vectors: the event order and K2's offer read from contract/vectors/; agent#1: the Gemini legs count the context tools from the offer vector; agent#9: the strict-alternation leg and the endpoint capabilities a leg sets
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
$pkg/context/reader.ts
$pkg/context/reader.test.ts
$pkg/engines/vercel/prefix.test.ts
$pkg/engines/vercel/retry.ts
$pkg/engines/vercel/bounds.test.ts
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
