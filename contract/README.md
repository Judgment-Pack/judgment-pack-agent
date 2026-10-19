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
| `run-event.json` | Every event a run emits, 18 kinds discriminated by `type`, in the union's order; the proposal's `note` where something after it did not finish | open | none | `web/src/assistant/engine.ts`, `AssistantEvent` |
| `tool-offer.json` | The tools offered to a run, as the session's two members: `tools` (runtime tools as served, each named within the ceiling) and `hostTools` (host tools' declarations); the agent-wire alias form; the runtime ceiling (the Desk's five and `experimental_test_cases`) and reserved names; `propose_pack`, the proposal as a host tool call | open | `$defs/agentWireTool` | `web/src/assistant/engine.ts`, `McpTool`, `HostTool`; `context/session.ts`, `contextualSession`; `internal/codexbridge/contract.go`, `Tool` |
| `tool-call.json` | The `tool_call` event; the `tools/call` params the gate inspects; the arguments `experimental_test_cases` may leave with; the agent-wire call | open | `$defs/testCasesArguments` | `web/src/assistant/engine.ts`, `AssistantEvent`; `toolGate.ts`, `inspectOutboundFrame`; `agentTransport.ts`, `bindAgentRun` |
| `tool-result.json` | The `tool_result` event; the MCP result shape; the bound, the read-back reference and its envelopes; `read_context` arguments; the reader's arguments and result (`read_window`, and every reading tool's window); the agent-wire answer; a reading tool's answer as the model is shown it, framed | open | `$defs/readContextArguments`, `$defs/readArguments`, `$defs/agentWireAnswer` | `web/src/assistant/engine.ts`, `McpToolResult`; `context/session.ts`, `SessionContext.result`; `context/ledger.ts`, `ContextLedger.reference`, `ContextLedger.tools`; the reader is this repository's, `packages/harness/context/reader.ts` and `context/policy.ts`, `readWindowBytes` (agent#1) |
| `approval.json` | The host's approval state for a chat and the one grant a user decision mints, for one of the server's four actions; the grant request | open | `$defs/request` | `web/src/assistant/approvals/client.ts`, `ActionApproval`, `ActionGrant`; `internal/desk/assistant_approval.go`, `assistantActionGrant`, `assistantApprovalRequest` |
| `question.json` | One question to the person (`ask_user`); the answer draft; a saved question; the format-failure codes | closed | the root, `$defs/option`, `$defs/draft`, `$defs/saved` | `web/src/assistant/questions.ts`, `readUserQuestion`, `validQuestionDraft`, `readSavedQuestions`; `questionFailure.ts`, `QuestionFailureCode` |
| `delegated-task.json` | One child task's state; the delegation request; a task profile | open | `$defs/request`, `$defs/taskRequest` | `web/src/assistant/delegationContract.ts`, `DelegatedTask`, `validDelegatedTask`, `readDelegationRequest`, `TaskProfile` |
| `checkpoint.json` | The execution checkpoint: allowance, grants, plan, operations, completions, retained context | open | none | `web/src/assistant/execution/state.ts`, `ExecutionCheckpoint`, `readExecution` |
| `failure.json` | One failure: the catalogue's 23 classes, source, the `not-started`/`refused`/`unknown` outcome, HTTP metadata; a catalogue entry; a failed tool's content | open | none | `web/src/assistant/failures.ts`, `AIFailure`, `readFailure`; `internal/aifailure/failure.go`, `Failure`; `internal/aifailure/catalog.json` |
| `budget.json` | The context budget table; the per-model override and profile configuration; the request allowance the model is told; the budget-increase answer; the fixed limits by their Desk names; the harness's turn clock and tool-call deadline; the endpoint capabilities that place the execution state, and where the state sits on each wire; its bounds on the test tool | open | `$defs/modelBudget`, `$defs/configuration` | `internal/aicontext/policy.json`; `web/src/assistant/context/policy.ts`, `ContextPolicy`, `contextConfiguration`; `internal/aicontext/policy.go`, `Policy`, `Decode`; `execution/controller.ts`, `guidance`, `budgetTool`; the placement is this repository's, `packages/harness/host.ts`, `EndpointCapabilities`, and `engines/vercel/loop.ts`, `STATE_HEADER`, `statePlacement` (agent#9) |
| `provenance.json` | One model request's record: request number, phase, normalized completion, and for a request sent again its attempt, a refused attempt's failure and wait; the digests and engine id the Desk holds | open | none | `web/src/assistant/execution/state.ts`, `ExecutionCheckpoint.completions`; `completion.ts`, `ModelCompletion`, `readCompletion` |

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
- `budget/desk-defaults.json` and `budget/desk-limits.json`: the budget table verbatim, and the fixed
  limits by their constant names; `budget/harness-defaults.json`: the policy a harness session works
  under, the table, the two time bounds and the three test-tool bounds.
- `proposal/`: the proposal through `propose_pack` (`propose-pack.json`), a proposal whose critic
  failed, delivered with its `note` (`critic-unfinished.json`), a `propose_pack` proposal whose
  closing request failed, delivered with its `note` (`turn-unfinished.json`), and an answer whose
  proposal could not be read, kept whole before the failure (`unreadable.json`); and
  `tool-offer/propose-pack.json`, the tool as a host declares it.
- `bounds/`: a 429 retried once, with the attempts in the completion record
  (`rate-limited-retried.json`), the turn's wall clock pausing the run (`turn-clock.json`), a tool
  past its deadline answered as a failed result (`tool-timeout.json`), and a turn whose test calls
  pass `testCallsPerTurn` (`test-call-cap.json`).
- `budget/state-placement.json`: where the execution state, headed `STATE_HEADER`, sits in the
  first and second requests of the recorded scenario on each wire, for each placement the endpoint
  capabilities resolve to. This harness's (agent#9); `engines/vercel/prefix.test.ts` checks every
  item against a run.
- `tool-call/test-cases-arguments.json`: the arguments `experimental_test_cases` leaves the page with.
- `framing/`: a reading tool's answer as the model is shown it, one data block closed by the run's
  marker (`framed-result.json`), and a page that asks for a crafted call, refused at the gate
  (`injection-refused.json`).
- `question/interview.json`: the Desk's interview-question fixture.

Values the Desk generates afresh on every run (UUIDs, timestamps, digests) appear only where the
recorded data carries them; no vector carries an event the Desk or the harness does not emit.

## The Desk's own lists

`desk/catalog.json` and `desk/policy.json` are byte-for-byte copies of the Desk's failure catalogue
(`internal/aifailure/catalog.json`) and budget table (`internal/aicontext/policy.json`), and
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
ends once, last; the scenario's order is the one the conformance session asserts; and this table's
*Root* and *Closed parts* columns match each schema (*Closed parts* lists the closed top-level
`$defs`).
