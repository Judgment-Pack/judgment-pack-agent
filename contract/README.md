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
| `run-event.json` | Every event a run emits, 18 kinds discriminated by `type`, in the union's order | open | none | `web/src/assistant/engine.ts`, `AssistantEvent` |
| `tool-offer.json` | One tool offered to a run: a runtime tool as served, or a host tool's declaration; the agent-wire alias form; the runtime ceiling and reserved names | open | `$defs/agentWireTool` | `web/src/assistant/engine.ts`, `McpTool`, `HostTool`; `context/session.ts`, `contextualSession`; `internal/codexbridge/contract.go`, `Tool` |
| `tool-call.json` | The `tool_call` event; the `tools/call` params the gate inspects; the agent-wire call | open | none | `web/src/assistant/engine.ts`, `AssistantEvent`; `toolGate.ts`, `inspectOutboundFrame`; `agentTransport.ts`, `bindAgentRun` |
| `tool-result.json` | The `tool_result` event; the MCP result shape; the bound, the read-back reference and its envelopes; `read_context` arguments; the agent-wire answer | open | `$defs/readContextArguments`, `$defs/agentWireAnswer` | `web/src/assistant/engine.ts`, `McpToolResult`; `context/session.ts`, `SessionContext.result`; `context/ledger.ts`, `ContextLedger.reference`, `ContextLedger.tools` |
| `approval.json` | The host's approval state for a chat and the one grant a user decision mints; the grant request | open | `$defs/request` | `web/src/assistant/approvals/client.ts`, `ActionApproval`, `ActionGrant`; `internal/desk/assistant_approval.go`, `assistantActionGrant`, `assistantApprovalRequest` |
| `question.json` | One question to the person (`ask_user`); the answer draft; a saved question; the format-failure codes | closed | the root, `$defs/option`, `$defs/draft`, `$defs/saved` | `web/src/assistant/questions.ts`, `readUserQuestion`, `validQuestionDraft`, `readSavedQuestions`; `questionFailure.ts`, `QuestionFailureCode` |
| `delegated-task.json` | One child task's state; the delegation request; a task profile | open | `$defs/request`, `$defs/taskRequest` | `web/src/assistant/delegationContract.ts`, `DelegatedTask`, `validDelegatedTask`, `readDelegationRequest`, `TaskProfile` |
| `checkpoint.json` | The execution checkpoint: allowance, grants, plan, operations, completions, retained context | open | none | `web/src/assistant/execution/state.ts`, `ExecutionCheckpoint`, `readExecution` |
| `failure.json` | One failure: the catalogue's 23 classes, source, the `not-started`/`refused`/`unknown` outcome, HTTP metadata; a catalogue entry; a failed tool's content | open | none | `web/src/assistant/failures.ts`, `AIFailure`, `readFailure`; `internal/aifailure/failure.go`, `Failure`; `internal/aifailure/catalog.json` |
| `budget.json` | The context budget table; the per-model override and profile configuration; the request allowance the model is told; the budget-increase answer; the fixed limits by their Desk names | open | `$defs/modelBudget`, `$defs/configuration` | `internal/aicontext/policy.json`; `web/src/assistant/context/policy.ts`, `ContextPolicy`, `contextConfiguration`; `internal/aicontext/policy.go`, `Policy`, `Decode`; `execution/controller.ts`, `guidance`, `budgetTool` |
| `provenance.json` | One model request's record: request number, phase, normalized completion; the digests and engine id the Desk holds | open | none | `web/src/assistant/execution/state.ts`, `ExecutionCheckpoint.completions`; `completion.ts`, `ModelCompletion`, `readCompletion` |

## Vectors

Data the Desk's engines produce today, in `vectors/`, one JSON file per vector:

```json
{ "description": "…", "schema": "run-event.json", "source": { "desk": {…}, "files": ["…"] }, "items": [ … ] }
```

`schema` names the schema each item must validate against (a file, or a file and a `$defs`
pointer); `items` are the instances, in order where order matters; `source` names the Desk files
and symbols they were read or rendered from.

- `scenario/expense-reimbursement.json`: the Desk's recorded conformance scenario, **one file for
  the whole run**, because what the conformance session asserts is the run's order (every call
  before its result, the gate's events between them, one `end`). `steps` maps each event to the
  scenario step that produced it. It is the sequence the model-engine leg asserts (non-interactive
  session, reasoning tier off, the five runtime tools allowed), with the recorded runtime answers
  and the texts the host writes rendered from the code that writes them.
- `failures/<class>.json`: one per catalogue class, with the terminal events an adapter emits for it
  on one path the Desk actually takes, named in `source`: `error` then `end` for most; a failed
  `tool_result` for `tool-failed` and `invalid-tool-input`, which are working data and not a failed
  run; `retry` events before `retries-exhausted`; and nothing at all for `cancelled`, which is never
  an error event.
- `tool-offer/scenario-offer.json`: what the scenario's session offers the model, the host's two
  context tools and the runtime's five as recorded.
- `budget/desk-defaults.json` and `budget/desk-limits.json`: the budget table verbatim, and the fixed
  limits by their constant names.
- `question/interview.json`: the Desk's interview-question fixture.

Values the Desk generates afresh on every run (UUIDs, timestamps, digests) appear only where the
recorded data carries them; no vector carries an event the Desk does not emit.

## The Desk's own lists

`desk/catalog.json` is a byte-for-byte copy of the Desk's failure catalogue
(`internal/aifailure/catalog.json`) and `desk/inventory.json` lists, by source and symbol, the event
kinds, failure sources and outcomes, completion reasons, engine ids and runtime tool ceiling at the
pinned commit. The tests hold the schemas to both. When the Desk changes one of them, copy it again
and the tests say which schema has to follow.

## Checks

`python -m unittest discover -s tests -v` (Python 3.12, `pip install -r requirements-dev.txt`):
every schema is well formed and every `$ref` resolves; every vector validates against its schema;
every member a vector carries is stated by its schema (the schemas re-checked with every object
definition closed, so an open schema still has to name what the Desk emits); the event kinds and
failure classes equal the Desk's lists; one vector per catalogue class; a run ends once, last; the
scenario's order is the one the conformance session asserts; and this table's *Root* column matches
each schema.
