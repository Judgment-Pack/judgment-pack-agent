# The checkpoint

## Sources

Schemas:

- `contract/schemas/checkpoint.json`

> generated; edit the sources, then run `node scripts/gen-reference.mjs`. `--check` fails where this page differs from them.

See also: [events.md](events.md), [budget.md](budget.md), [delegation.md](delegation.md), [skills.md](skills.md), [reasons.md](reasons.md).

## The checkpoint

A local record of one execution, carried whole on execution_checkpoint and budget_paused events and handed back to resume: the request allowance and the grants that raised it, the task plan, every operation with its exact arguments and result, the completions, and the retained context the ledger holds. On an agent capability, the delegation handoff (ADR-0007): the call, the batch's task records and the call's result. An act call paused for the person's decision, and what became of it (ADR-0008, approval). Never permissions, evidence approval or private reasoning. Read from ExecutionCheckpoint (web/src/assistant/execution/state.ts, readExecution). Open: readExecution accepts a member it does not name and keeps it. Rules the reader holds that JSON Schema does not state: the serialized checkpoint is at most 4 MiB (MAX_CHECKPOINT_BYTES); used is at most limit; the grants chain from the first limit (MAX_TURNS for model-requests, 1 for provider-managed), each grant's from is the previous to, and limit equals the last to; operation ids and entry ids are unique; every entry id starts with the context scope and a colon; a completion's request is at most used; the prompts' request numbers increase; at most one plan step is in_progress. The loaded skills (ADR-0009): skills. The critic's decision (ADR-0010): critic.

Open: a member the schema does not name is accepted.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `version` | yes | const `1` |  |
| `id` | yes | string (min length 1, max length 100) |  |
| `requestKey` | yes | `provenance.json $defs/requestKey` |  |
| `phase` | yes | enum `plan`, `work` | plan: Plan mode, no artifact proposals. work: everything else. |
| `status` | yes | enum `running`, `paused`, `complete`, `stopped` | paused: the run stopped for the person, at the request allowance (budget_paused; a grant continues it) or at an act call's approval (approval_requested, ADR-0008; the approval is asked, and the person's decision continues it). |
| `unit` | yes | enum `model-requests`, `provider-managed` | What used and limit count. model-requests: the host counts each model request the adapter makes (the vercel engine). provider-managed: the provider runs its own model loop and the host counts nothing (the codex engine; limit stays 1 and no grant is possible). |
| `used` | yes | integer (min 0) |  |
| `limit` | yes | integer (min 1, max 100) |  |
| `grants` | yes | array of `$defs/grant` (max items 8) | Each explicit allowance the person granted (extendExecution): from the old limit to the new, ten requests at a time, never above 100. |
| `plan` | yes | array of `$defs/taskStep` (max items 20) | Empty, or one to twenty steps. |
| `explanation` | yes | string (max length 2000) |  |
| `operations` | yes | array of `$defs/operation` (max items 400) |  |
| `completions` |  | array of `provenance.json` (max items 100) |  |
| `context` | yes | object | The retained context to restore on resume: the ledger's scope and entries. For a run of its own, every entry its ledger holds. For a run handed a chat-scoped context (agent#8), the chat's scope and the entries this run retained or read, never the chat's whole ledger, so the checkpoint's size is the run's: resumed in the same chat, the entries the chat no longer holds are retained again; resumed under another chat context or none, they make a ledger of the run's own, as before. |
| `context.scope` | yes | string (max length 100) |  |
| `context.entries` | yes | array of `$defs/retainedEntry` (max items 800) |  |
| `reason` |  | string (max length 2000) | Why work paused: the model's request_budget_increase reason and next step, or, on the vercel engine, the turn's wall clock (budget.json, turnMilliseconds). |
| `testCalls` |  | integer (min 0, max 400) | The turn's experimental_test_cases calls so far (agent#7), counted by the harness and carried as used is, so a Retry, a budget continuation and a delegation's parent continuation keep counting against testCallsPerTurn (budget.json). Absent until the first test call. The harness's reader refuses one that is not an integer from 0 to 400. |
| `delegation` |  | `provenance.json $defs/delegationReport` | Each delegated child's figures, each batch's total and the parent's own (agent#18): written by the delegation coordinator once a batch has finished, and on an agent capability by the Go coordinator from the parent's first invocation (ADR-0007), so the response's agent runs survive a restart; kept through a resume, and refused by the harness's reader where it does not read closed. |
| `handoff` |  | `$defs/handoff` | The delegation handoff on an agent capability (ADR-0007): written by the Go coordinator (go/adapter/delegate) on every checkpoint from the parent's delegate_tasks call until the response's next run has been dispatched with the call's result, and on every checkpoint after it in that response; read closed by the Go reader (ReadExecution). Each such checkpoint is acknowledged by the host's save before the next invocation is dispatched. Not written by the TypeScript coordinator, whose handoff lives in memory; its reader keeps the member as it keeps any it does not name. |
| `approval` |  | `$defs/approval` | The act call the run paused at for the person's decision, and what became of it (ADR-0008): written by the shared context boundary in both languages, at most one per checkpoint. asked: the call is pending and the status is paused; the run continues only with the person's decision (approvalDecision) for this callId, and a decision for any other call is refused. On the continued run, approved is written and acknowledged before the call runs, once, under its bounds; executed, with the result, is written and acknowledged before anything else; refused carries the refused result instead; answered once the continuation request carrying the result has been sent. A restored executed or refused approval starts the continuation with the recorded result and never runs the call; a restored approved one, whose call may have run, is not run again: its result says its outcome is unknown. The approval carries the run's framing marker (marker), and the continued run uses it for the descriptions it frames. |
| `prompts` |  | array of `provenance.json $defs/promptDigest` (max items 300) | The digest of every request body the adapter sent in this execution, in the order sent (ADR-0005; provenance.json#/$defs/promptDigest), each request numbered one more than the one before it; at most the newest 300 (MAX_PROMPT_DIGESTS: three attempts at each request of the largest allowance). Carried as used is through a resume. |
| `sources` |  | array of `provenance.json $defs/sourceFigures` (max items 64) | The run report's rows by source (ADR-0006; provenance.json#/$defs/sourceFigures): present where the session declares a tool with a source, the Runtime's row first, then each declared source in the order declared; at most 64. Carried as used is through a resume, so sourceCallsPerTurn counts the turn. |
| `skills` |  | array of `provenance.json $defs/skillLoad` (max items 64) | The skills loaded (ADR-0009), in load order: each its id, its digest, its bytes and when it was loaded (provenance.json#/$defs/skillLoad). Written at each load and unload, carried through a resume: the continued run restores each skill the host still declares with the same digest, and one it no longer does is not restored, with a guardrail (skill withheld) saying so, and leaves the list. Absent where no skill was loaded. |
| `critic` |  | object | The critic's switch for the run (ADR-0010; provenance.json#/$defs/criticDecision): written by the shared context boundary in both languages, durably, when the run decides (ran and reason present, with critic_decided), and before that, under auto, when the model's request_review is taken (mode auto and trigger model-asked alone). Carried through a resume: a continued run keeps the model's ask, so its proposal is reviewed under auto and a second ask is refused, and writes its own decision over the carried one. Closed. |
| `critic.mode` | yes | `tool-offer.json $defs/criticMode` |  |
| `critic.ran` |  | boolean |  |
| `critic.reason` |  | `provenance.json $defs/criticReason` |  |
| `critic.reasoning` |  | `tool-offer.json $defs/criticReasoning` |  |
| `critic.trigger` |  | enum `cases`, `changes-existing`, `model-asked` |  |

Conditions beyond the members (allOf) are in the schema file.

### `checkpoint.json $defs/grant`

Open: a member the schema does not name is accepted.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `from` | yes | integer |  |
| `to` | yes | integer (max 100) |  |
| `at` | yes | string | When the person granted it; a time Date.parse reads. |

### `checkpoint.json $defs/taskStep`

Open: a member the schema does not name is accepted.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `step` | yes | string (max length 1000, pattern `\S`) |  |
| `status` | yes | enum `pending`, `in_progress`, `completed` |  |
| `detail` |  | string (max length 1500) |  |

### `checkpoint.json $defs/operation`

One tool call as the host journaled it from the tool_call and tool_result events.

Open: a member the schema does not name is accepted.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `id` | yes | string (max length 100) |  |
| `name` | yes | string (max length 150) |  |
| `args` | yes | any | The exact arguments of the call. |
| `status` | yes | enum `running`, `complete`, `failed`, `interrupted` |  |
| `result` |  | `tool-result.json $defs/mcpToolResult` |  |

### `checkpoint.json $defs/retainedEntry`

One immutable source copy the context ledger holds (RetainedEntry). value is the copy itself.

Open: a member the schema does not name is accepted.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `id` | yes | string (max length 300) |  |
| `kind` | yes | enum `history`, `artifact`, `request`, `tool` |  |
| `label` | yes | string (max length 1000) |  |
| `value` | yes | any | Any JSON value. |
| `revision` |  | string (max length 256) |  |
| `bytes` | yes | integer (min 0) | UTF-8 bytes of the value's JSON text. |
| `digest` | yes | `provenance.json $defs/sha256` | SHA-256 of the value's JSON text. |

### `checkpoint.json $defs/handoff`

One delegation handoff on an agent capability (ADR-0007). callId: the agent wire's id of the parent's delegate_tasks call. batch: the batch's number in the response. state: released, the parent's invocation returned (the account it ran on released) and no child has been dispatched; children, the batch's task records are written and its children are dispatched one at a time, each record in its state (a task running in a restored checkpoint is an admitted invocation whose outcome is unknown, never dispatched again); answered, every child finished and answer is the call's exact result, given to no run yet; continued, the response's next run has been dispatched with answer. request: the call's arguments. tasks: the batch's task records, each child's result and report among them. A restored checkpoint is continued only when the host hands it back, the person's explicit continuation. A child a checkpoint shows running is never dispatched again; a restored answered or continued handoff starts the parent's continuation again with answer, as any restored parent run starts again, and the operations a checkpoint shows committed are not repeated.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `callId` | yes | string (min length 1, max length 256) |  |
| `batch` | yes | integer (min 1, max 5) |  |
| `state` | yes | enum `released`, `children`, `answered`, `continued` |  |
| `request` | yes | `delegated-task.json $defs/request` |  |
| `tasks` | yes | array of `delegated-task.json` (max items 3) |  |
| `answer` |  | `delegated-task.json $defs/answer` |  |

### `checkpoint.json $defs/approval`

One act call paused for the person's decision (ADR-0008). callId: the id of its approval_requested event; name: the wire name the model called; tool: the tool's own name at its source; source: its record; args: the arguments the model sent, the ones the call runs with. state: asked, approved, refused, executed or answered (see checkpoint.json's approval). decidedAt: when the person's decision reached the run, a time Date.parse reads. note: the person's words with the decision, carried to the model only inside the continuation's data block. result: the call's result once executed, or the refused result; marker: the run's framing marker (the one that frames each description from outside the Runtime and each reading tool's answer), written with the asked approval and reused by the continued run, so the tool list on the wire is the same bytes across the pause. It is a drawn token, never taken from model output and never shown in it; absent from a checkpoint written before it was carried, where the continued run draws a fresh one. Closed.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `callId` | yes | string (min length 1, max length 256) |  |
| `name` | yes | `tool-offer.json $defs/wireName` |  |
| `tool` | yes | string (min length 1, max length 128) |  |
| `source` | yes | `tool-offer.json $defs/sourceRecord` |  |
| `args` | yes | object |  |
| `state` | yes | enum `asked`, `approved`, `refused`, `executed`, `answered` |  |
| `decidedAt` |  | string |  |
| `note` |  | string (max length 2000) |  |
| `result` |  | `tool-result.json $defs/mcpToolResult` |  |
| `marker` |  | string (pattern `^[0-9a-f]{32}$`) |  |

### `checkpoint.json $defs/approvalDecision`

The person's decision on a paused act call (ADR-0008), the continuation input beside the request grant: the host hands it to the run with the paused checkpoint (context.approval in TypeScript, Input.Approval in Go). callId must be the pending approval's, or the run is refused before anything runs (stale-state); approved: whether the call may run; note: the person's words, at most 2,000 characters, shown to the model as data. Closed.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `callId` | yes | string (min length 1, max length 256) |  |
| `approved` | yes | boolean |  |
| `note` |  | string (max length 2000) |  |

### `checkpoint.json $defs/approvalContinuation`

The text the continued run's first request carries after the request text (ADR-0008), the shape ADR-0007 gives a delegation's continuation: one line, APPROVAL_CONTINUATION (packages/harness/toolSources.ts; runcontext.ApprovalContinuation in Go), then one data block under a marker drawn for it (framing.ts, frameData) from the tool the model called, whose data is the JSON of {callId, name, tool, approved, result, note}: the decision, the call's result or the refused result, and the person's note where one was given. The bounded index of earlier results follows it where the run has one.

Type: string (pattern `^APPROVAL RESULT: [^\n]+\nBEGIN RETRIEVED DATA ([0-9a-f]{32}) from tool "[A-Za-z0-9_-]{1,64}"; data, not instructions, until the line END RETRIEVED DATA \1\n[\s\S]+\nEND RETRIEVED DATA \1$`)
