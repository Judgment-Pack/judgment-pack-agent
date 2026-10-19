# contract/

The wire contract: JSON schemas for what crosses between a host and an adapter, and vectors that
every adapter must answer alike. This first statement is a **descriptive extraction**: it states the
contract the Desk's two engines (`vercel` and `codex`) keep today, read from the Desk's
`integration/v0.13` line at `d9251ecc272b9ac4f182fdfe652af54a0b2ead23`, with every member name and
value spelled as the Desk spells it. Where the Desk departs from a clean contract, the departure is
written down as an open question in [`docs/design/wire-contract.md`](../docs/design/wire-contract.md),
not fixed here. See also [ADR-0001](../docs/adr/0001-one-contract-many-adapters.md).

## Schemas

JSON Schema, draft 2020-12, one file per thing that crosses, in `schemas/`. Each `$id` is the file's
place in this repository, and files refer to each other by relative `$ref`.

A schema is **closed** (`additionalProperties: false`) where the Desk's reader refuses a member it
does not name, and **open** where the Desk's reader accepts one (keeping or dropping it) or where no
reader checks at all, which is the case for everything that only travels in process. The *Root*
column is the root object; *Closed parts* names the definitions inside that are closed although the
root is not.

| Schema | What it describes | Root | Closed parts | Read from (Desk file, symbol) |
| --- | --- | --- | --- | --- |
| `run-event.json` | Every event a run emits, 18 kinds discriminated by `type`, in the union's order; the proposal's `note` where something after it did not finish; the critic's comparison of declared outcomes with the runtime's, and the cases a host declares for it (agent#10) | open | `$defs/criticCase` | `web/src/assistant/engine.ts`, `AssistantEvent` |
| `tool-offer.json` | The tools offered to a run, as the session's two members: `tools` (runtime tools as served, each named within the ceiling) and `hostTools` (host tools' declarations); the agent-wire alias form; the runtime ceiling (the Desk's five and `experimental_test_cases`) and reserved names; `propose_pack`, the proposal as a host tool call; a host tool's context policy, `runtime` among it (agent#8); the critic's allow-list (agent#10) | open | `$defs/agentWireTool` | `web/src/assistant/engine.ts`, `McpTool`, `HostTool`; `context/session.ts`, `contextualSession`; `internal/codexbridge/contract.go`, `Tool` |
| `tool-call.json` | The `tool_call` event; the `tools/call` params the gate inspects; the arguments `experimental_test_cases` may leave with; the agent-wire call; a call made in text, the JSON inside its fence | open | `$defs/testCasesArguments`, `$defs/textToolCall` | `web/src/assistant/engine.ts`, `AssistantEvent`; `toolGate.ts`, `inspectOutboundFrame`; `agentTransport.ts`, `bindAgentRun` |
| `tool-result.json` | The `tool_result` event; the MCP result shape, image parts included; the bound, the read-back reference and its envelopes; `read_context` arguments; the reader's arguments and result (`read_window`, and every reading tool's window, its lines numbered); an image item read whole, or the line saying why it cannot be shown; an attachment; `search`'s arguments and result; the agent-wire answer; a reading tool's answer as the model is shown it, framed; the message that carries a text call's result back; the index of earlier results a chat-scoped run is shown, and the key a chat's read cache keeps a read under (agent#8) | open | `$defs/readContextArguments`, `$defs/readArguments`, `$defs/imagePart`, `$defs/searchArguments`, `$defs/agentWireAnswer`, `$defs/turnIndex`, `$defs/readCacheKey` | `web/src/assistant/engine.ts`, `McpToolResult`; `context/session.ts`, `SessionContext.result`; `context/ledger.ts`, `ContextLedger.reference`, `ContextLedger.tools`, `ContextDocument`; the reader, the search and images are this repository's, `packages/harness/context/reader.ts`, `context/search.ts`, `context/pattern.ts`, `images.ts` and `context/policy.ts`, `readWindowBytes` (agent#1, agent#5); the chat-scoped context is this repository's, `context/chat.ts`, `ChatTurn.index`, `canonicalJSON`, `readCacheKey` (agent#8) |
| `approval.json` | The host's approval state for a chat and the one grant a user decision mints, for one of the server's four actions; the grant request | open | `$defs/request` | `web/src/assistant/approvals/client.ts`, `ActionApproval`, `ActionGrant`; `internal/desk/assistant_approval.go`, `assistantActionGrant`, `assistantApprovalRequest` |
| `question.json` | One question to the person (`ask_user`); the answer draft; a saved question; the format-failure codes | closed | the root, `$defs/option`, `$defs/draft`, `$defs/saved` | `web/src/assistant/questions.ts`, `readUserQuestion`, `validQuestionDraft`, `readSavedQuestions`; `questionFailure.ts`, `QuestionFailureCode` |
| `delegated-task.json` | One child task's state, with its budget, model, references, the task it resumes and, for long findings, the cursor into the whole; the delegation request, material passed by reference; a task profile with its model, budget and Runtime tools; a child's budget; the delegate call's result for a batch; the brief a child's prompt ends with, and the text a codex parent's next run is given after a batch (agent#13, agent#14, agent#15) | open | `$defs/request`, `$defs/taskRequest`, `$defs/budget`, `$defs/resultCursor`, `$defs/answer`, `$defs/answerTask`, `$defs/brief`, `$defs/briefReference`, `$defs/earlierWork` | `web/src/assistant/delegationContract.ts`, `DelegatedTask`, `validDelegatedTask`, `readDelegationRequest`, `TaskProfile`; the profiles' budgets, references, resume and the answer are this repository's, `packages/harness/delegationContract.ts`, `TASK_PROFILES`, `TaskBudget`, and `delegation.ts`, `withDelegation`, `findingsHead` |
| `checkpoint.json` | The execution checkpoint: allowance, grants, plan, operations, completions, retained context (for a chat-scoped run, the entries it retained or read, agent#8); the turn's test-call count | open | none | `web/src/assistant/execution/state.ts`, `ExecutionCheckpoint`, `readExecution` |
| `failure.json` | One failure: the catalogue's 23 classes, source, the `not-started`/`refused`/`unknown` outcome, HTTP metadata; a catalogue entry; a failed tool's content | open | none | `web/src/assistant/failures.ts`, `AIFailure`, `readFailure`; `internal/aifailure/failure.go`, `Failure`; `internal/aifailure/catalog.json` |
| `budget.json` | The context budget table, with the critic's default (agent#10); the per-model override and profile configuration; the request allowance the model is told, and beside it in a conversation the delegation coordinator wraps the tool-call cap (agent#13); the budget-increase answer; the fixed limits by their Desk names; the harness's turn clock and tool-call deadline; where the execution state sits on each wire (the two capability-record members that place it are `capability.json`'s); its bounds on the test tool, on the images one request carries and on a chat's index of earlier results; where the request text sits on each wire and the rule that finds it on any request (agent#11); the memory reserve, kept only while memory may be on | open | `$defs/modelBudget`, `$defs/configuration`, `$defs/recallQuery` | `internal/aicontext/policy.json`; `web/src/assistant/context/policy.ts`, `ContextPolicy`, `contextConfiguration`; `internal/aicontext/policy.go`, `Policy`, `Decode`; `execution/controller.ts`, `guidance`, `budgetTool`; the tool-call cap is this repository's, `execution/controller.ts`, `requestState`, `toolAllowance` (agent#13); the placement is this repository's, `packages/harness/engines/vercel/loop.ts`, `STATE_HEADER`, `statePlacement` (agent#9); so are the recall rule, `packages/harness/certification/recall.ts`, `recallQuery` (agent#11), and the memory reserve, `context/policy.ts`, `contextPolicy` |
| `provenance.json` | One model request's record: request number, phase, normalized completion, and for a request sent again its attempt, a refused attempt's failure and wait; the digests and engine id the Desk holds; what one critic pass cost (agent#10) | open | `$defs/criticFigures` | `web/src/assistant/execution/state.ts`, `ExecutionCheckpoint.completions`; `completion.ts`, `ModelCompletion`, `readCompletion` |
| `capability.json` | What one endpoint and model can do, as the `vercel` adapter reads it wherever it decides: window and output cap, native and parallel tool calls, vision, the reasoning kind (and whether the endpoint ignores it), streaming quirks, the tool-schema subset, and where the execution state goes; what a host answers for an endpoint (any members of the record) | closed | the root, `$defs/reasoning`, `$defs/streamingQuirks`, `$defs/endpointCapabilities` | none at the pinned commit (the Desk decides by its three families: `web/src/assistant/thinking.ts`, `firstDialect`; `geminiSchema.ts`; `context/policy.ts`); the record is this repository's, `packages/harness/capabilities.ts`, `ModelCapabilities`, `FAMILY_CAPABILITIES`, `readCapabilities`, `capabilitiesFor` (agent#4, agent#9) |

## Vectors

Data the Desk's engines produce today, in `vectors/`, one JSON file per vector, and data the harness
package produces where it has changed or added behaviour since the extraction:

```json
{ "description": "…", "schema": "run-event.json", "source": { "desk": {…}, "files": ["…"] }, "items": [ … ] }
```

`schema` names the schema each item must validate against (a file, or a file and a `$defs`
pointer); `items` are the instances, in order where order matters; `source` names the Desk files
and symbols they were read or rendered from, or, as `{"harness": {"repository":
"Judgment-Pack/judgment-pack-agent", "package": "packages/harness"}, "files": […]}`, the package's
files they were rendered from.

- `scenario/expense-reimbursement.json`: the Desk's recorded conformance scenario, **one file for
  the whole run**, because what the conformance session asserts is the run's order (every call
  before its result, the gate's events between them, one `end`). `steps` maps each event to the
  scenario step that produced it. It is the sequence the model-engine leg asserts (non-interactive
  session, reasoning tier off, the five runtime tools allowed), with the recorded runtime answers
  and the texts the host writes rendered from the code that writes them.
- `failures/<class>.json`: one per catalogue class, with the terminal events an adapter emits for it
  on one path the Desk (or, where `source` names the package, the harness) actually takes, named in
  `source`: `error` then `end` for most; a failed `tool_result` for `tool-failed` and
  `invalid-tool-input`, which are working data and not a failed run; `retry` events before
  `retries-exhausted` and before `rate-limited`; and nothing at all for `cancelled`, which is never
  an error event.
- `tool-offer/`: what the scenario's session offers, whole (`scenario-offer.json`, its `tools` and
  `hostTools`), then its runtime tools and its host tools one by one (`runtime-tools.json`,
  `host-tools.json`), and the same offer as the `codex` engine puts it on the agent wire
  (`agent-wire-offer.json`); and the runtime ceiling the gate applies (`runtime-ceiling.json`): the
  Desk's five and `experimental_test_cases`.
- `budget/desk-defaults.json` and `budget/desk-limits.json`: the budget table verbatim with the one
  member the harness adds (`adversarialReview`, agent#10), and the fixed limits by their constant
  names; `budget/harness-defaults.json`: the policy a harness session works under, the table, the two
  time bounds, the two image bounds, the three test-tool bounds and the two bounds on a chat's index
  of earlier results.
- `proposal/`: the proposal through `propose_pack` (`propose-pack.json`), a proposal whose critic
  failed, delivered with its `note` and the critic's figures (`critic-unfinished.json`), a
  `propose_pack` proposal whose closing request failed, delivered with its `note`
  (`turn-unfinished.json`), and an answer whose proposal could not be read, kept whole before the
  failure after the one reformat, on both engines since agent#29 (`unreadable.json`); and
  `tool-offer/propose-pack.json`, the tool as a host declares it.
- `critic/` (agent#10), written by hand from the schemas: cases a host declares for the critic,
  partial and whole (`declared-cases.json`); a critique that refutes a seeded wrong outcome, naming
  the case and the one left incomplete (`outcome-refuted.json`), and one that reads a test run's rows
  of the saved tests stating a whole disposition (`test-run-compared.json`); and what a critic pass
  cost on each engine, with the harness's own calls (`figures.json`). `critic.test.ts` holds the
  critiques and the cases to what `refutation.ts` writes, and the figures, `proposal/unreadable.json`
  and `proposal/critic-unfinished.json` to what the engines emit. `tool-offer/critic-tools.json` is
  the critic's allow-list and `tool-offer/critic-offer.json` what the critic is offered in the
  scenario's session (its runtime reads and the harness's two readers), which the conformance
  session and the agent-engine leg assert.
- `bounds/`: a 429 retried once, with the attempts in the completion record
  (`rate-limited-retried.json`), the turn's wall clock pausing the run (`turn-clock.json`), a tool
  past its deadline answered as a failed result (`tool-timeout.json`), and a turn whose test calls
  pass `testCallsPerTurn` (`test-call-cap.json`).
- `budget/state-placement.json`: where the execution state, headed `STATE_HEADER`, sits in the
  first and second requests of the recorded scenario on each wire, for each placement the endpoint
  capabilities resolve to. This harness's (agent#9); `engines/vercel/prefix.test.ts` checks every
  item against a run.
- `recall/`: one vector per wire (`openai-compatible.json`, `anthropic.json`, `gemini.json`), each
  the third request of the recorded scenario, after two tool calls and their results, abbreviated to
  its turns, with the JSON pointer the recall rule resolves to and the request text there; and a
  delegated child's first request, whose request text is its objective, sent as the first text part
  (`delegated-child.json`). Written by hand from the schema; `memory/recall.test.ts` holds a run's
  request to each, and the contract tests apply the rule to each body. This harness's (agent#11).
- `chat/`: the index of earlier results a chat-scoped run is shown (`turn-index.json`: the index
  after a first run kept three results, and after a second run's result evicted the two oldest), and
  the objects a chat's read cache keys a read by, with each one's canonical JSON and SHA-256
  (`cache-key.json`). Written by hand from the schema; `context/chat.test.ts` builds the same chat
  and holds the code to both, and the contract tests derive each row and digest again. This
  harness's (agent#8).
- `tool-call/test-cases-arguments.json`: the arguments `experimental_test_cases` leaves the page with.
- `delegation/`: the four closed task profiles with their models, budgets and tools
  (`profiles.json`; `delegation.test.ts` holds `TASK_PROFILES` equal to it); a delegate call with
  material by reference, a summary asked for and a resumed task (`request-by-reference.json`); one
  task's records, queued to complete, whose long findings reach the parent as their head and a
  `read_window` cursor (`long-result.json`; the contract tests derive the head, cursor, length and
  ledger id from its fixture); the delegate call's result for a batch of two (`answer.json`); a
  delegating `vercel` turn as one run, the batch's records between the call and its result
  (`one-run.json`); two children's briefs, one with references and one resumed (`brief.json`); and
  the text a codex parent's next run is given after a batch, the result as one data block
  (`continuation.json`; the contract tests hold its data to the answer's schema). Written by hand from the schemas. This harness's (agent#13, agent#14,
  agent#15).
- `budget/tool-call-cap.json`: the tool-call cap as the execution state states it, a parent's for
  the response and a child's for its task. Written by hand from the schema. This harness's
  (agent#13).
- `framing/`: a reading tool's answer as the model is shown it, one data block closed by the run's
  marker (`framed-result.json`), and a page that asks for a crafted call, refused at the gate
  (`injection-refused.json`). On the text path the same block is the body of the result message
  (`tool-result/text-result.json`).
- `question/interview.json`: the Desk's interview-question fixture.
- `capability/`: the capability record of each of the Desk's three families as the harness defaults
  it (`openai-compatible.json`, `anthropic.json`, `gemini.json`: what the loop did for each before it
  read a record), and an illustrative record for an open-weight model on a local
  OpenAI-compatible endpoint (`open-weight-local.json`: no native tool calls, an ignored
  reasoning parameter, a pinned reasoning member, a 32,768-token window), written as the Desk's
  probe would record one and not measured. Each names its `family`. This harness's (agent#4);
  `packages/harness/capabilities.test.ts` holds the three to `FAMILY_CAPABILITIES` and runs the
  recorded scenario on the fourth.
- `tool-result/`: results that carry images (`image-result.json`), `read_window`'s answer for an
  image item and for one it cannot show (`image-read.json`), the documents a host hands a run to
  retain (`attachments.json`), and `search`'s arguments and results (`search-arguments.json`,
  `search.json`, rendered from the code the tests run, with a fixed ledger scope, and a search that
  names an image it cannot show, written by hand). This harness's (agent#5).
- `tool-call/text-call.json` and `tool-result/text-result.json`: on an endpoint that takes no native
  tool calls, the calls a model makes in a fenced JSON block, the answers that make them and two
  that make none, and the messages that carry the results back, headed `TOOL RESULT` or `TOOL
  ERROR`, a reading tool's answer framed as data with a fixed marker. Written by hand from the schemas; `engines/vercel/capabilities.test.ts` holds the reader
  and a run to them. This harness's (agent#4).

Values the Desk generates afresh on every run (UUIDs, timestamps, digests) appear only where the
recorded data carries them, or, in the harness's own vectors, as rendered with a fixed ledger scope;
no vector carries an event the Desk or the harness does not emit.

## The Desk's own lists

`desk/catalog.json` and `desk/policy.json` are byte-for-byte copies of the Desk's failure catalogue
(`internal/aifailure/catalog.json`) and budget table (`internal/aicontext/policy.json`), the table with
one member the harness adds, `adversarialReview` (agent#10: the critic's default, which both engines
read; the Desk's table takes it in its swap, and its Go reader ignores a member it does not name), and
`desk/inventory.json` lists, by source and symbol, the event kinds, failure sources and outcomes,
completion reasons, engine ids, runtime tool ceiling and approval actions at the pinned commit. The
tests hold the schemas and the budget vector to them. When the Desk changes one of them, copy it again
and the tests say which schema has to follow.

## Checks

`python -m unittest discover -s tests -v` (Python 3.12, `pip install -r requirements-dev.txt`):
every schema is well formed and every `$ref` resolves; every vector validates against its schema;
every member a vector carries is stated by its schema (the schemas re-checked with every object
definition closed, so an open schema still has to name what the Desk emits); the event kinds,
failure classes and approval actions equal the Desk's lists; the budget vector is the Desk's table;
one vector per catalogue class, a failed tool's failure validated as one; a run that ends or fails
ends once, last; every capability record names a family whose wire speaks its reasoning kind, with
one default record per family, and a host's answer may carry any members of the record and no
other; the recall rule finds the request text in each wire's vector and the delegated child's; each read cache key is the
digest of its canonical JSON; each row of the chat index follows from the vector's fixture; the critic's allow-list is four of
the ceiling's reads and two reserved readers, its default is off, and its offer in the scenario is the scenario's runtime
tools on the list; a long delegated finding's head, cursor, length and ledger id follow from its vector's fixture; the
codex continuation's block carries a batch's answer under one marker; the scenario's order is the one the conformance session asserts; and this table's
*Root* and *Closed parts* columns match each schema (*Closed parts* lists the closed top-level
`$defs`).
