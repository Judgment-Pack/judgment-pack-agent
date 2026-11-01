# The checkpoint

## Sources

Schemas:

- `contract/schemas/checkpoint.json`

> generated; edit the sources, then run `node scripts/gen-reference.mjs`. `--check` fails where this page differs from them.

See also: [events.md](events.md), [budget.md](budget.md), [delegation.md](delegation.md), [skills.md](skills.md), [reasons.md](reasons.md).

The recorded projection APIs and omissions are described in [run-report.md](run-report.md).

Current feature state uses checkpoint 3: task schedules/envelopes, usage budgets, per-turn routing, repeated-tool observations and runtime action approval. Feature-free writers keep checkpoint 2. Retained history, archives and captures are read-only data and cannot grant native resume, dispatch or approval authority. See [task-steps.md](task-steps.md), [repeated-tools.md](repeated-tools.md), [history-archive.md](history-archive.md), [history-capture.md](history-capture.md) and [feedback.md](feedback.md).

Active steps close with complete_plan_step and current planRevision, using acknowledged same-run evidence or not-needed with a reason. update_plan carries unchanged terminal rows. Accepted origin, bounded departures and purpose-specific drift decisions survive resume. The complete_plan_step tool name is reserved; hosts must rename conflicting tools.

Task steps use package-issued execution-scoped ids. update_plan retains ids explicitly; an omitted id means a new step. planState.revision is the canonical ordered task-content digest, while sequence orders the newest 32 bounded history entries. Storage compare-and-set revisions remain opaque host tokens. See docs/design/task-plan.md for migration and deterministic diff ordering.

## The checkpoint

A local record of one execution, carried whole on execution_checkpoint and budget_paused events and handed back to resume: the request allowance and the grants that raised it, the task plan, every operation with its exact arguments and result, the completions, and the retained context the ledger holds. On an agent capability, the delegation handoff (ADR-0007): the call, the batch's task records and the call's result. An act call paused for the person's decision, and what became of it (ADR-0008, approval). Never permissions, evidence approval or private reasoning. Read from ExecutionCheckpoint (web/src/assistant/execution/state.ts, readExecution). Versioned: structural members not named by the selected version are refused. Declared arbitrary payloads are retained whole. Rules the reader holds that JSON Schema does not state: the serialized checkpoint is at most 4 MiB (MAX_CHECKPOINT_BYTES); used is at most limit; a present requestCap is a fixed model-requests limit with no grants and must match trusted resume input; otherwise the grants chain from the first limit (MAX_TURNS for model-requests, 1 for provider-managed), each grant's from is the previous to, and limit equals the last to; operation ids and entry ids are unique; every entry id starts with the context scope and a colon; a completion's request is at most used; the prompts' request numbers increase; at most one plan step is in_progress. The loaded skills (ADR-0009): skills. The critic's decision (ADR-0010): critic.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `schema` |  | const (see schema) |  |
| `version` | yes | enum `1`, `2`, `3` | Feature-free producers retain published defaults; feature-bearing usage budget records select checkpoint 3 / event 2. |
| `digest` |  | `provenance.json $defs/recordDigest` |  |
| `retainUntil` |  | `provenance.json $defs/retainUntil` |  |
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
| `delegation` |  | `provenance.json $defs/delegationReport` | Each delegated child's figures, each batch's total and the parent's own (agent#18): written by the delegation coordinator once a batch has finished, and on an agent capability by the Go coordinator from the parent's first invocation (ADR-0007), so the response's agent runs survive a restart; kept through a resume, and refused by the harness's reader where it does not read closed. Each declared child carries its subagent id, digest and source in its figures; handoff tasks carry the same record. A withdrawn or changed queued definition is interrupted, never dispatched. |
| `handoff` |  | `$defs/handoff` | The durable delegation handoff, written and restored in both languages. Each transition is acknowledged before the next invocation. Current writers retain exact continuation framing; historical formats remain frozen. |
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
| `policy` |  | `policy.json $defs/resolved` |  |
| `crossProvider` |  | array of `provenance.json $defs/crossProvider` |  |
| `compaction` |  | `provenance.json $defs/compactionReport` | ADR-0016: the compaction mode and the replacements already made. Summary text and the digests of the original turns make a resumed request byte-identical without another compactor request. |
| `memories` |  | `provenance.json $defs/memories` |  |
| `draft` |  | `$defs/draft` |  |
| `outputGuard` |  | `provenance.json $defs/outputGuard` |  |
| `records` |  | `provenance.json $defs/recordFigures` |  |
| `sourceIdentities` |  | array of `$defs/sourceIdentity` (max items 64) | ADR-0019: per-source identity pins, latched identity withholding and service approval history, separate from figures and the latest approval. Restored on resume; re-approval starts a new run with fresh source declarations and state. |
| `steers` |  | `provenance.json $defs/steerReport` |  |
| `usage` |  | `usage.json` | ADR-0020. The run's counts so far, carried through interruption and resume and finalized before end. |
| `failover` |  | array of object (max items 64) | ADR-0020. Active connections, switch reasons and consumed chain bounds, restored before dispatch. |
| `failover[].role` | yes | string (min length 1, max length 128) |  |
| `failover[].connection` | yes | string (min length 1, max length 128) |  |
| `failover[].history` | yes | array of object (max items 256) |  |
| `failover[].history[].from` | yes | string (min length 1, max length 128) |  |
| `failover[].history[].to` | yes | string (min length 1, max length 128) |  |
| `failover[].history[].reason` | yes | enum `unavailable`, `rate-limited`, `timeout` |  |
| `failover[].consumed` | yes | integer (min 0, max 256) | Consumed switches in this role’s admitted chain; equals the history length. |
| `childUsage` |  | array of object (max items 256) | ADR-0020. Last accounted child ledgers by execution ID, used to replace contributions without double counting. |
| `childUsage[].id` | yes | string (min length 1, max length 128) |  |
| `childUsage[].usage` | yes | `usage.json` |  |
| `requestCap` |  | integer (min 1, max 20) | Optional fixed host API request cap. When present, unit is model-requests, limit equals this cap, used is at most it, and grants is empty. It must match independently trusted resume input; it cannot be extended. |
| `durable` |  | `$defs/durableBook` | Current-format acknowledged dispatch/recovery journal. Scope comes from admitted host execution/role/task, never model text. A containing commit proves recorded provenance, not current authority or external success. Credentials and private reasoning are excluded; exact required bytes that cannot be safely retained are refused before dispatch. |
| `planState` |  | `$defs/planState` | Current ordered task content SHA-256, independent history sequence and step allocator. Only newest 32 revisions retained; discarded is the explicit prefix count. Explanations/history/counters do not affect the content digest. Storage CAS revisions remain opaque host tokens. |
| `usageBudget` |  | `usage-budget.json` |  |
| `turnRoute` |  | `turn-route.json` |  |
| `repeatTools` |  | `$defs/repeatTools` |  |

Conditions beyond the members (allOf) are in the schema file.

### `checkpoint.json $defs/grant`

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `from` | yes | integer |  |
| `to` | yes | integer (max 100) |  |
| `at` | yes | string | When the person granted it; a time Date.parse reads. |

### `checkpoint.json $defs/taskStep`

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `step` | yes | string (max length 1000, pattern `\S`) |  |
| `status` | yes | enum `pending`, `in_progress`, `completed`, `not-needed` |  |
| `detail` |  | string (max length 1500) |  |
| `id` |  | string (pattern `^step-[1-9][0-9]{0,15}$`) | Package-issued execution-scoped identity; absent on legacy records only. |
| `origin` |  | `$defs/planOrigin` |  |
| `evidence` |  | `$defs/stepEvidence` |  |
| `notNeededReason` |  | string (min length 1, max length 1500, pattern `\S`) |  |
| `dependsOn` |  | array of string (pattern `^step-[1-9][0-9]{0,15}$`) (max items 19) |  |
| `budget` |  | `$defs/taskStepShare` |  |
| `delegation` |  | `subagent.json $defs/record` |  |

### `checkpoint.json $defs/operation`

One tool call as the host journaled it from the tool_call and tool_result events.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `id` | yes | string (max length 100) |  |
| `name` | yes | string (max length 150) |  |
| `args` | yes | any | The exact arguments of the call. |
| `status` | yes | enum `running`, `complete`, `failed`, `interrupted` |  |
| `result` |  | `tool-result.json $defs/mcpToolResult` |  |

### `checkpoint.json $defs/retainedEntry`

One immutable source copy the context ledger holds (RetainedEntry). value is the copy itself.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `id` | yes | string (max length 300) |  |
| `kind` | yes | enum `history`, `artifact`, `request`, `tool` |  |
| `label` | yes | string (max length 1000) |  |
| `value` | yes | any | Any JSON value. |
| `valueJSON` |  | string (max length 4194304) | Optional checkpoint 2 exact JSON.stringify-compatible source text for value, populated by strict durable publication. Its UTF-8 bytes and SHA-256 must equal bytes and digest, and its parsed value must be canonically equal to value. Restore uses this original representation after validation; absence preserves legacy exact-value digest semantics. |
| `revision` |  | string (max length 256) |  |
| `bytes` | yes | integer (min 0) | UTF-8 bytes of the value's JSON text. |
| `digest` | yes | `provenance.json $defs/sha256` | SHA-256 of the value's JSON text. |

### `checkpoint.json $defs/handoff`

One delegation handoff on an agent capability (ADR-0007). callId: the agent wire's id of the parent's delegate_tasks call. batch: the batch's number in the response. state: released, the parent's invocation returned (the account it ran on released) and no child has been dispatched; children, the batch's task records are written and its children are dispatched one at a time, each record in its state (a task running in a restored checkpoint is an admitted invocation whose outcome is unknown, never dispatched again); answered, every child finished and answer is the call's exact result, given to no run yet; continued, the response's next run has been dispatched with answer. request: the call's arguments. tasks: the batch's task records, each child's result and report among them. A restored checkpoint is continued only when the host hands it back, the person's explicit continuation. A child a checkpoint shows running is never dispatched again; a restored answered or continued handoff starts the parent's continuation again with answer, as any restored parent run starts again, and the operations a checkpoint shows committed are not repeated. Current-format continuation holds the exact safe framed answer bytes; resumed answered/continued records without reconstructible safe bytes refuse by name.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `callId` | yes | string (min length 1, max length 256) |  |
| `batch` | yes | integer (min 1, max 5) |  |
| `state` | yes | enum `released`, `children`, `answered`, `continued` |  |
| `request` | yes | `delegated-task.json $defs/request` |  |
| `tasks` | yes | array of `delegated-task.json` (max items 3) |  |
| `answer` |  | `delegated-task.json $defs/answer` |  |
| `continuation` |  | string (max length 4194304) |  |

### `checkpoint.json $defs/approval`

One act call paused for the person's decision (ADR-0008). callId: the id of its approval_requested event; name: the wire name the model called; tool: the tool's own name at its source; source: its record; args: the arguments the model sent, as parsed; the call runs with these, unless request is present (#275). state: asked, approved, refused, executed or answered (see checkpoint.json's approval). decidedAt: when the person's decision reached the run, a time Date.parse reads. note: the person's words with the decision, carried to the model only inside the continuation's data block. result: the call's result once executed, or the refused result; marker: the run's framing marker (the one that frames each description from outside the Runtime and each reading tool's answer), written with the asked approval and reused by the continued run, so the tool list on the wire is the same bytes across the pause. It is a drawn token, never taken from model output and never shown in it; absent from a checkpoint written before it was carried, where the continued run draws a fresh one. Closed.

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
| `runtime` |  | `$defs/runtimeApprovalBinding` |  |
| `request` |  | string (min length 1) | The call's arguments as the model wrote them, byte for byte, where args cannot say them (#275): args is the parsed form, so a number past what a float64 holds, a spelling, a zero, a decimal, a null, a replacement character or two names that fold together may be lost in it. The approved call runs with these bytes, after they are read back to args. Absent where args says what the model wrote, whitespace and member order aside, as in a record a published release wrote. An approval that holds neither is refused as stale rather than run with what its args may have changed. Version 3 only. |

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

### `checkpoint.json $defs/draftDocument`

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `id` | yes | `tool-offer.json $defs/draftId` |  |
| `digest` | yes | string (pattern `^sha256:[0-9a-f]{64}$`) |  |
| `edits` | yes | `provenance.json $defs/edits` |  |

### `checkpoint.json $defs/draft`

ADR-0015: package-owned drafts. Exact text, unknowns and undo history are retained in context.entries under the draft id, digest and edit count. Resume verifies the digest and continues the same draft.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `documents` | yes | array of `$defs/draftDocument` |  |

### `checkpoint.json $defs/sourceIdentity`

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `source` | yes | string (pattern `^[A-Za-z][A-Za-z0-9-]{0,23}$`) |  |
| `identityDigest` | yes | string (pattern `^sha256:[0-9a-f]{64}$`) |  |
| `withheld` | yes | boolean |  |
| `serviceAsked` | yes | boolean |  |

### `checkpoint.json $defs/durableScope`

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `execution` | yes | string (min length 1, max length 256) |  |
| `role` | yes | string (min length 1, max length 128) |  |
| `task` |  | string (min length 1, max length 256) |  |

### `checkpoint.json $defs/artifactObservation`

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `kind` | yes | enum `artifact`, `dialect` |  |
| `id` | yes | string (min length 1, max length 256) |  |
| `digest` | yes | string (pattern `^sha256:[a-f0-9]{64}$`) |  |
| `access` |  | enum `read`, `produced` |  |

### `checkpoint.json $defs/durableDispatch`

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `id` | yes | string (min length 1, max length 256) |  |
| `scope` | yes | `$defs/durableScope` |  |
| `kind` | yes | enum `model`, `agent`, `host`, `runtime` |  |
| `source` | yes | string (min length 1, max length 256) |  |
| `tool` | yes | string (min length 1, max length 256) |  |
| `fingerprint` | yes | string (pattern `^sha256:[a-f0-9]{64}$`) |  |
| `request` | yes | string (max length 4194304) |  |
| `requestDigest` | yes | string (pattern `^sha256:[a-f0-9]{64}$`) |  |
| `state` | yes | enum `claimed`, `result`, `unknown` |  |
| `read` | yes | boolean |  |
| `result` |  | any |  |
| `failure` |  | `failure.json` |  |
| `artifacts` |  | array of `$defs/artifactObservation` (max items 32) |  |
| `repeatResultJson` |  | string (min length 1, max length 4194304) | Enabled repeat detection only: exact original acknowledged result encoding, retained by its original writer before legacy result projection. |
| `planPause` |  | `$defs/nativePlanPause` |  |

### `checkpoint.json $defs/durableObservation`

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `id` | yes | string (min length 1, max length 256) |  |
| `scope` | yes | `$defs/durableScope` |  |
| `kind` | yes | enum `operation`, `artifact-read`, `artifact-produced` |  |
| `source` | yes | string (min length 1, max length 256) |  |
| `dispatchId` | yes | string (min length 1, max length 256) |  |
| `commitId` | yes | string (min length 1, max length 256) |  |
| `artifact` |  | `$defs/artifactObservation` |  |

### `checkpoint.json $defs/durableBook`

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `dispatches` | yes | array of `$defs/durableDispatch` (max items 400) |  |
| `observations` | yes | array of `$defs/durableObservation` (max items 400) |  |
| `children` | yes | array of object (max items 32) |  |
| `children[].scope` | yes | `$defs/durableScope` |  |
| `children[].checkpoint` | yes | `checkpoint.json` |  |
| `pendingHandoff` |  | object |  |
| `pendingHandoff.callId` | yes | string (min length 1, max length 256) |  |
| `pendingHandoff.request` | yes | `delegated-task.json $defs/request` |  |

### `checkpoint.json $defs/planChange`

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `id` | yes | string (pattern `^step-[1-9][0-9]{0,15}$`) |  |
| `kind` | yes | enum `added`, `removed`, `changed`, `status`, `reordered` |  |
| `before` |  | `$defs/taskStep` |  |
| `after` |  | `$defs/taskStep` |  |
| `from` |  | integer (min 0, max 19) |  |
| `to` |  | integer (min 0, max 19) |  |

### `checkpoint.json $defs/planRevision`

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `sequence` | yes | integer (min 1, max 9007199254740991) |  |
| `previous` | yes | string (pattern `^sha256:[a-f0-9]{64}$`) |  |
| `revision` | yes | string (pattern `^sha256:[a-f0-9]{64}$`) |  |
| `explanation` | yes | string (max length 2000) |  |
| `diff` | yes | array of `$defs/planChange` (max items 40) |  |

### `checkpoint.json $defs/planState`

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `revision` | yes | string (pattern `^sha256:[a-f0-9]{64}$`) |  |
| `sequence` | yes | integer (min 0, max 9007199254740991) |  |
| `nextId` | yes | integer (min 1, max 9007199254740991) |  |
| `discarded` | yes | integer (min 0, max 9007199254740991) |  |
| `history` | yes | array of `$defs/planRevision` (max items 32) |  |
| `accepted` |  | `$defs/acceptedPlan` |  |
| `driftPolicy` |  | array of enum `added`, `removed`, `changed`, `reordered` (max items 4) |  |
| `departures` |  | array of `$defs/planDepartureRecord` (max items 32) |  |
| `departuresDiscarded` |  | integer (min 0, max 9007199254740991) |  |
| `pending` |  | `$defs/pendingDeparture` |  |
| `refusals` |  | array of `$defs/planRefusal` (max items 32) |  |
| `refusalsDiscarded` |  | integer (min 0, max 9007199254740991) |  |
| `decisions` |  | array of `$defs/planDecision` (max items 32) |  |
| `decisionsDiscarded` |  | integer (min 0, max 9007199254740991) |  |
| `taskSchedule` |  | `$defs/taskSchedule` |  |
| `taskEnvelope` |  | `$defs/taskEnvelopeBinding` |  |
| `taskCoordinator` |  | `$defs/taskCoordinatorBinding` |  |

### `checkpoint.json $defs/stepEvidence`

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `operations` | yes | array of string (min length 1, max length 256) (max items 20) |  |
| `artifacts` | yes | array of object (max items 20) |  |
| `artifacts[].kind` | yes | enum `artifact`, `dialect` |  |
| `artifacts[].id` | yes | string (min length 1, max length 256) |  |
| `artifacts[].digest` | yes | string (pattern `^sha256:[a-f0-9]{64}$`) |  |
| `child` |  | `$defs/taskCompletionEvidence` |  |

### `checkpoint.json $defs/planOrigin`

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `plan` | yes | string (min length 1, max length 256) |  |
| `revision` | yes | string (pattern `^sha256:[a-f0-9]{64}$`) |  |
| `index` | yes | integer (min 0, max 19) |  |

### `checkpoint.json $defs/acceptedPlan`

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `id` | yes | string (min length 1, max length 256) |  |
| `revision` | yes | string (pattern `^sha256:[a-f0-9]{64}$`) |  |
| `sourceRevision` | yes | integer (min 1, max 9007199254740991) |  |
| `objective` | yes | string (min length 1, max length 2000) |  |
| `steps` | yes | array of string (max length 1000, pattern `\S`) (min items 1, max items 20) |  |
| `references` | yes | array of object (max items 8) |  |
| `references[].kind` | yes | enum `artifact`, `dialect`, `file` |  |
| `references[].id` | yes | string (min length 1, max length 1024) |  |
| `references[].digest` |  | string (pattern `^[a-f0-9]{64}$`) |  |
| `references[].revision` |  | string (pattern `^[a-f0-9]{64}$`) |  |
| `acceptedAt` | yes | string (min length 1, max length 100) |  |
| `validatedReferences` | yes | array of object (max items 8) |  |
| `validatedReferences[].kind` | yes | enum `artifact`, `dialect` |  |
| `validatedReferences[].id` | yes | string (min length 1, max length 256) |  |
| `validatedReferences[].digest` | yes | string (pattern `^[a-f0-9]{64}$`) |  |

### `checkpoint.json $defs/planDeparture`

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `kind` | yes | enum `added`, `removed`, `changed`, `reordered` |  |
| `step` | yes | string (min length 1, max length 100) |  |
| `index` |  | integer (min 0, max 19) |  |

### `checkpoint.json $defs/planDepartureRecord`

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `revision` | yes | string (pattern `^sha256:[a-f0-9]{64}$`) |  |
| `explanation` | yes | string (max length 2000) |  |
| `changes` | yes | array of `$defs/planDeparture` (max items 40) |  |

### `checkpoint.json $defs/planRefusal`

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `revision` | yes | string (pattern `^sha256:[a-f0-9]{64}$`) |  |
| `reason` | yes | enum `invalid-input`, `active-step-required`, `revision-changed`, `evidence-unavailable`, `missing-evidence`, `unknown-operation`, `unsuccessful-operation`, `unobserved-revision`, `wrong-scope` |  |
| `step` |  | string (min length 1, max length 100) |  |

### `checkpoint.json $defs/pendingDeparture`

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `purpose` | yes | const `plan-drift` |  |
| `callId` | yes | string (min length 1, max length 256) |  |
| `revision` | yes | string (pattern `^sha256:[a-f0-9]{64}$`) |  |
| `candidate` | yes | object |  |
| `candidate.plan` | yes | array of `$defs/taskStep` (min items 1, max items 20) |  |
| `candidate.planState` | yes | `$defs/planState` |  |
| `explanation` | yes | string (max length 2000) |  |
| `departures` | yes | array of `$defs/planDeparture` (max items 40) |  |

### `checkpoint.json $defs/planDecision`

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `callId` | yes | string (min length 1, max length 256) |  |
| `revision` | yes | string (pattern `^sha256:[a-f0-9]{64}$`) |  |
| `candidateRevision` | yes | string (pattern `^sha256:[a-f0-9]{64}$`) |  |
| `approved` | yes | boolean |  |
| `note` |  | string (max length 2000) |  |
| `nativeInvocation` |  | `$defs/nativePlanInvocation` |  |

### `checkpoint.json $defs/runtimeExecutionOwner`

The trusted host observation of the actual immutable Runtime execution. Labels are descriptive and never grant authority. Closed.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `artifactDigest` | yes | string (pattern `^sha256:[0-9a-f]{64}$`) |  |
| `processId` | yes | string (min length 1, max length 128, pattern `^[^\u0000-\u001f\u007f]+$`) |  |
| `projectId` | yes | string (min length 1, max length 128, pattern `^[^\u0000-\u001f\u007f]+$`) |  |
| `projectLabel` | yes | string (min length 1, max length 128, pattern `^[^\u0000-\u001f\u007f]+$`) |  |
| `sessionId` | yes | string (min length 1, max length 128, pattern `^[^\u0000-\u001f\u007f]+$`) |  |
| `principalDigest` | yes | string (pattern `^sha256:[0-9a-f]{64}$`) |  |
| `principalLabel` | yes | string (min length 1, max length 128, pattern `^[^\u0000-\u001f\u007f]+$`) |  |
| `scopeDigest` | yes | string (pattern `^sha256:[0-9a-f]{64}$`) |  |
| `scopeLabel` | yes | string (min length 1, max length 128, pattern `^[^\u0000-\u001f\u007f]+$`) |  |

### `checkpoint.json $defs/runtimeApprovalBinding`

Runtime-specific exact admission binding carried only by current checkpoint3/event2. A record is not a dispatch grant. Closed.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `owner` | yes | `$defs/runtimeExecutionOwner` |  |
| `admissionDigest` | yes | string (pattern `^sha256:[0-9a-f]{64}$`) |  |

### `checkpoint.json $defs/taskStepShare`

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `requests` | yes | integer (min 1, max 100) |  |

### `checkpoint.json $defs/taskScheduleBinding`

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `run` | yes | string (min length 1, max length 100, pattern `^[^\u0000-\u001f]+$`) |  |
| `execution` | yes | string (min length 1, max length 100, pattern `^[^\u0000-\u001f]+$`) |  |
| `planRevision` | yes | string (pattern `^sha256:[a-f0-9]{64}$`) |  |
| `scopeDigest` | yes | string (pattern `^sha256:[a-f0-9]{64}$`) |  |
| `unit` | yes | enum `model-requests`, `provider-managed` |  |

### `checkpoint.json $defs/taskStepClaim`

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `id` | yes | string (min length 1, max length 100, pattern `^[^\u0000-\u001f]+$`) |  |
| `requestDigest` | yes | string (pattern `^sha256:[a-f0-9]{64}$`) |  |
| `state` | yes | enum `held`, `admitted`, `unknown` |  |

### `checkpoint.json $defs/taskChildResult`

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `checkpointDigest` | yes | string (pattern `^sha256:[a-f0-9]{64}$`) |  |
| `resultDigest` | yes | string (pattern `^sha256:[a-f0-9]{64}$`) |  |
| `eventDigest` | yes | string (pattern `^sha256:[a-f0-9]{64}$`) |  |
| `commitId` | yes | string (min length 1, max length 100, pattern `^[^\u0000-\u001f]+$`) |  |

### `checkpoint.json $defs/taskChildBinding`

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `task` | yes | string (min length 1, max length 100, pattern `^[^\u0000-\u001f]+$`) |  |
| `execution` | yes | string (min length 1, max length 100, pattern `^[^\u0000-\u001f]+$`) |  |
| `planRevision` | yes | string (pattern `^sha256:[a-f0-9]{64}$`) |  |
| `profile` | yes | `subagent.json $defs/record` |  |
| `result` |  | `$defs/taskChildResult` |  |

### `checkpoint.json $defs/taskStepRow`

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `id` | yes | string (pattern `^step-[1-9][0-9]{0,15}$`) |  |
| `limit` |  | integer (min 1, max 100) |  |
| `claims` | yes | array of `$defs/taskStepClaim` (max items 100) |  |
| `child` |  | `$defs/taskChildBinding` |  |

### `checkpoint.json $defs/taskSchedule`

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `version` | yes | const `1` |  |
| `binding` | yes | `$defs/taskScheduleBinding` |  |
| `definitionDigest` | yes | string (pattern `^sha256:[a-f0-9]{64}$`) |  |
| `rows` | yes | array of `$defs/taskStepRow` (min items 1, max items 20) |  |
| `pending` |  | `$defs/taskPendingDelegation` |  |

### `checkpoint.json $defs/taskExecutionEnvelope`

Explicit immutable host-configured provider invocation ceiling. No available or spent balance is accepted.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `id` | yes | string (min length 1, max length 100) |  |
| `revision` | yes | string (pattern `^sha256:[a-f0-9]{64}$`) |  |
| `unit` | yes | const `provider-invocations` |  |
| `limit` | yes | integer (min 1, max 100) |  |

### `checkpoint.json $defs/taskEnvelopeBinding`

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `run` | yes | string (min length 1, max length 100) |  |
| `execution` | yes | string (min length 1, max length 100) |  |
| `scopeDigest` | yes | string (pattern `^sha256:[a-f0-9]{64}$`) |  |
| `configuration` | yes | `$defs/taskExecutionEnvelope` |  |

### `checkpoint.json $defs/taskPendingDelegation`

Original-writer association acknowledged before native release or model child admission. An interrupted association never grants redispatch.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `planRevision` | yes | string (pattern `^sha256:[a-f0-9]{64}$`) |  |
| `requestDigest` | yes | string (pattern `^sha256:[a-f0-9]{64}$`) |  |
| `steps` | yes | array of string (pattern `^step-[1-9][0-9]{0,15}$`) (min items 1, max items 3) |  |

### `checkpoint.json $defs/taskCompletionEvidence`

Exact acknowledged successful child evidence; caller strings alone cannot establish completion.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `task` | yes | string (min length 1, max length 100) |  |
| `checkpointDigest` | yes | string (pattern `^sha256:[a-f0-9]{64}$`) |  |
| `resultDigest` | yes | string (pattern `^sha256:[a-f0-9]{64}$`) |  |
| `eventDigest` | yes | string (pattern `^sha256:[a-f0-9]{64}$`) |  |
| `commitId` | yes | string (min length 1, max length 100) |  |

### `checkpoint.json $defs/repeatObservation`

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `dispatchId` | yes | string (min length 1, max length 100) |  |
| `keyDigest` | yes | string (pattern `^sha256:[a-f0-9]{64}$`) |  |

### `checkpoint.json $defs/repeatExecution`

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `execution` | yes | string (min length 1, max length 100) |  |
| `remindAt` | yes | integer (min 1, max 400) |  |
| `stopAt` | yes | integer (min 1, max 400) |  |
| `count` | yes | integer (min 0, max 399) |  |
| `reminded` | yes | boolean |  |
| `stopped` | yes | boolean |  |
| `seen` | yes | array of `$defs/repeatObservation` (max items 400) |  |
| `reminderDelivered` | yes | boolean |  |

### `checkpoint.json $defs/repeatTools`

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `executions` | yes | array of `$defs/repeatExecution` (min items 1, max items 33) |  |

### `checkpoint.json $defs/taskCoordinatorBinding`

Original package native-delegation ceiling and child reserve, pinned before first dispatch; separate from host taskEnvelope configuration. Retained facts permit metadata-only Plan decisions and never grant dispatch.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `version` | yes | const `1` |  |
| `owner` | yes | const `native-delegation` |  |
| `run` | yes | string (min length 1, max length 100) |  |
| `execution` | yes | string (min length 1, max length 100) |  |
| `scopeDigest` | yes | string (pattern `^sha256:[a-f0-9]{64}$`) |  |
| `limit` | yes | integer (min 1, max 100) |  |
| `childReserve` | yes | integer (min 1, max 100) |  |

### `checkpoint.json $defs/nativePlanPause`

Original invocation release and package-owned Plan pause retained by the original writer; unknown outcome remains unknown. Not a dispatch grant.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `callId` | yes | string (min length 1, max length 256) |  |
| `revision` | yes | string (pattern `^sha256:[a-f0-9]{64}$`) |  |
| `candidateRevision` | yes | string (pattern `^sha256:[a-f0-9]{64}$`) |  |

### `checkpoint.json $defs/nativePlanInvocation`

Exact earlier released Plan-pause invocation selected by an acknowledged explicit decision. It grants no allowance or automatic replay.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `dispatchId` | yes | string (min length 1, max length 256) |  |
| `requestDigest` | yes | string (pattern `^sha256:[a-f0-9]{64}$`) |  |
