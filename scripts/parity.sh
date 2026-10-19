#!/usr/bin/env bash
# The parity proof for packages/harness: every file moved from the Desk, diffed against the Desk's
# original, with every difference printed and named.
#
#   scripts/parity.sh <desk-checkout>
#
# <desk-checkout> is a checkout of Judgment-Pack/judgment-pack-desk (the extraction was read from
# integration/v0.13 at d9251ec). Each row of the table below is one moved file: its place in the
# package, its place in the Desk, and why it differs. A row whose reason is "-" must be byte for
# byte the Desk's. A row with a reason prints its hunks under that reason. The run fails on a row
# marked "-" that differs, on a file missing from either side, and on a package file that is
# neither moved nor declared new below. Exit 0 means the hunks printed are the only differences.
set -euo pipefail

if [ $# -ne 1 ] || [ ! -d "$1/web/src/assistant" ]; then
  echo "usage: scripts/parity.sh <desk-checkout>   (a checkout of judgment-pack-desk)" >&2
  exit 2
fi
desk=$(cd "$1" && pwd)
root=$(cd "$(dirname "$0")/.." && pwd)
pkg=packages/harness
a=web/src/assistant

# package path | Desk path | reason it differs, or - for none
table() {
  cat <<EOF
$pkg/agent.ts|$a/agent.ts|-
$pkg/agentTransport.ts|$a/agentTransport.ts|injection points: AITarget, deskSocketURL, chassisUrl, socketProtocols, sessionBearer, whenSessionEnds
$pkg/agentTransport.test.ts|$a/agentTransport.test.ts|injection points: the Desk's two module mocks become one mock of host.ts
$pkg/completion.ts|$a/completion.ts|-
$pkg/completion.test.ts|$a/completion.test.ts|-
$pkg/context/context.test.ts|$a/context/context.test.ts|-
$pkg/context/ledger.ts|$a/context/ledger.ts|declaration emit: Entry exported (a type, no runtime change) so tsc can write controller.d.ts
$pkg/context/managed.ts|$a/context/managed.ts|-
$pkg/context/policy.ts|$a/context/policy.ts|the Go-side copy: policy.json read from the package's contract/
$pkg/context/recovery.integration.test.ts|$a/context/recovery.integration.test.ts|-
$pkg/context/session.ts|$a/context/session.ts|-
$pkg/context/toolPolicy.ts|$a/context/toolPolicy.ts|-
$pkg/context/toolPolicy.test.ts|$a/context/toolPolicy.test.ts|-
$pkg/delegation.ts|$a/delegation.ts|-
$pkg/delegation.test.ts|$a/delegation.test.ts|test kept in the Desk: the case over the Desk's response history; the Desk's chat-history reader replaced by a fold of delegated_task events with its interruption rule
$pkg/delegationContract.ts|$a/delegationContract.ts|-
$pkg/engine.ts|$a/engine.ts|injection points: Language, AssistantEngine, EndpointKind (types)
$pkg/engines/codex/codex.test.ts|$a/engines/codex/codex.test.ts|paths: conformance/ is certification/, testing/question reads the contract vector; test kept in the Desk: the case over the Desk's dialect chat tools
$pkg/engines/codex/index.ts|$a/engines/codex/index.ts|injection points: assistantLanguageInstructions
$pkg/engines/contract.ts|$a/engines/contract.ts|injection points: EndpointKind (type)
$pkg/engines/contract.test.ts|$a/engines/contract.test.ts|-
$pkg/engines/index.ts|$a/engines/index.ts|injection points: AssistantEngine (type)
$pkg/engines/vercel/channel.ts|$a/engines/vercel/channel.ts|-
$pkg/engines/vercel/completion.test.ts|$a/engines/vercel/completion.test.ts|-
$pkg/engines/vercel/engine.test.ts|$a/engines/vercel/engine.test.ts|injection points: ASSISTANT_ENGINES, ASSISTANT_TOOLS; paths: LANGUAGES from the test host, testing/question reads the contract vector
$pkg/engines/vercel/index.ts|$a/engines/vercel/index.ts|-
$pkg/engines/vercel/loop.ts|$a/engines/vercel/loop.ts|injection points: assistantLanguageInstructions, AssistantEngine and EndpointKind (types)
$pkg/engines/vercel/relay.ts|$a/engines/vercel/relay.ts|injection points: EndpointKind (type)
$pkg/execution/checkArtifact.ts|$a/execution/checkArtifact.ts|-
$pkg/execution/checkArtifact.test.ts|$a/execution/checkArtifact.test.ts|-
$pkg/execution/controller.ts|$a/execution/controller.ts|-
$pkg/execution/correctArtifact.ts|$a/execution/correctArtifact.ts|-
$pkg/execution/correctArtifact.test.ts|$a/execution/correctArtifact.test.ts|-
$pkg/execution/execution.test.ts|$a/execution/execution.test.ts|test kept in the Desk: the case over the Desk's chat checkpoint
$pkg/execution/progress.ts|$a/execution/progress.ts|-
$pkg/execution/progress.test.ts|$a/execution/progress.test.ts|-
$pkg/execution/state.ts|$a/execution/state.ts|-
$pkg/failures.ts|$a/failures.ts|the Go-side copy: catalog.json read from the package's contract/
$pkg/failures.test.ts|$a/failures.test.ts|-
$pkg/failures.integration.test.ts|$a/failures.integration.test.ts|-
$pkg/geminiSchema.ts|$a/geminiSchema.ts|-
$pkg/geminiSchema.test.ts|$a/geminiSchema.test.ts|paths: conformance/ is certification/
$pkg/memory/client.ts|$a/memory/client.ts|injection points: answer, deskFetch
$pkg/memory/memory.test.ts|$a/memory/memory.test.tsx|tests kept in the Desk: the two cases over the Desk's memory settings screen
$pkg/memory/tools.ts|$a/memory/tools.ts|-
$pkg/planning/model.ts|$a/planning/model.ts|-
$pkg/planning/planning.test.ts|$a/planning/planning.test.tsx|tests kept in the Desk: the three cases over the Desk's chat store and plan card
$pkg/planning/policy.ts|$a/planning/policy.ts|-
$pkg/planning/tools.ts|$a/planning/tools.ts|injection points: Chat and ChatStore (types), readFile, dialectAPI, PreparationReply (type)
$pkg/questionFailure.ts|$a/questionFailure.ts|injection points: sourceMessage
$pkg/questions.ts|$a/questions.ts|-
$pkg/refutation.ts|$a/refutation.ts|injection points: sourceMessage
$pkg/refutation.test.ts|$a/refutation.test.ts|-
$pkg/session.ts|$a/session.ts|injection points: aiHeaders, AITarget, deskHeaders, currentLanguage, chassisUrl, DeskWebSocketTransport, socketProtocols, socketURL, the session store (NO_SESSION_MESSAGE, NoSession, discardBody, forgetSession, refusalCode, sessionBearer, sessionEnded, whenSessionEnds), EndpointKind (type)
$pkg/thinking.ts|$a/thinking.ts|injection points: sourceMessage, EndpointKind and ThinkingTier (types)
$pkg/thinking.test.ts|$a/thinking.test.ts|injection points: EndpointKind and ThinkingTier (types)
$pkg/toolGate.ts|$a/toolGate.ts|injection points: ASSISTANT_TOOLS
$pkg/toolGate.test.ts|$a/toolGate.test.ts|injection points: ASSISTANT_TOOLS; whitespace: the blank line at the end of the file, which the repository's whitespace check refuses
$pkg/certification/certification/clearsItsInterval.ts|$a/conformance/certification/clearsItsInterval.ts|-
$pkg/certification/certification/throwsWhileDraining.ts|$a/conformance/certification/throwsWhileDraining.ts|-
$pkg/certification/certification/touchesAfterRun.ts|$a/conformance/certification/touchesAfterRun.ts|-
$pkg/certification/certification/touchesOnLoad.ts|$a/conformance/certification/touchesOnLoad.ts|-
$pkg/certification/conformance.test.ts|$a/conformance/conformance.test.ts|injection points: EndpointKind and ThinkingTier (types); contract vectors: the event order and K2's offer read from contract/vectors/
$pkg/certification/runtime.json|$a/conformance/runtime.json|-
$pkg/certification/scenario.json|$a/conformance/scenario.json|-
$pkg/certification/scriptedModel.ts|$a/conformance/scriptedModel.ts|injection points: EndpointKind (type)
$pkg/certification/scriptedModel.test.ts|$a/conformance/scriptedModel.test.ts|-
$pkg/certification/scriptedServer.ts|$a/conformance/scriptedServer.ts|-
$pkg/contract/catalog.json|internal/aifailure/catalog.json|-
$pkg/contract/policy.json|internal/aicontext/policy.json|-
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
EOF
}

echo "parity: $pkg against the Desk at $(git -C "$desk" rev-parse --short HEAD 2>/dev/null || echo 'an unversioned checkout')"
failed=0
same=0
differing=0
while IFS='|' read -r ours theirs reason; do
  if [ ! -f "$root/$ours" ]; then echo "MISSING in the package: $ours"; failed=1; continue; fi
  if [ ! -f "$desk/$theirs" ]; then echo "MISSING in the Desk: $theirs"; failed=1; continue; fi
  if cmp -s "$desk/$theirs" "$root/$ours"; then
    if [ "$reason" != "-" ]; then echo "STALE reason, the file is identical: $ours"; failed=1; fi
    same=$((same + 1))
    continue
  fi
  differing=$((differing + 1))
  echo
  if [ "$reason" = "-" ]; then
    echo "UNEXPECTED difference: $ours"
    failed=1
  else
    echo "== $ours ($reason)"
  fi
  diff -u --label "desk/$theirs" --label "$ours" "$desk/$theirs" "$root/$ours" | tail -n +3 || true
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
