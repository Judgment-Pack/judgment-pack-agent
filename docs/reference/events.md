# Run events

## Sources

Schemas:

- `contract/schemas/run-event.json`
- `contract/schemas/tool-call.json`
- `contract/schemas/tool-result.json`

> generated; edit the sources, then run `node scripts/gen-reference.mjs`. `--check` fails where this page differs from them.

One event of a run, from the adapter to the host, in order, discriminated by type. Every kind of the Desk's AssistantEvent union (web/src/assistant/engine.ts) at the pinned commit, with its members spelled as the Desk spells them. A run ends with exactly one end and nothing follows it; a run the consumer left gets no end. Some events are the host's own, interleaved into the same stream rather than emitted by a loop: the tool gate's guardrail rewrote and refused, the shared context boundary's execution_checkpoint, budget_paused (which also replaces a loop's own budget_paused or request-limit error) and approval_requested (ADR-0008: emitted in place of an act call's tool_call where the host's approval policy answers ask), and the delegation coordinator's delegated_task. critic_decided (ADR-0010) is emitted by the adapter once per run, where the critic would start or where it is skipped. Open: the stream is an in-process TypeScript union with no reader at run time that refuses another member; the type member alone decides the kind.

See also: [tools.md](tools.md), [delegation.md](delegation.md), [critic.md](critic.md), [skills.md](skills.md), [budget.md](budget.md), [reasons.md](reasons.md), [checkpoint.md](checkpoint.md).

## The 30 kinds

| Kind | Defined in | Required beyond type |
| --- | --- | --- |
| `execution_checkpoint` | `run-event.json $defs/execution_checkpoint` | `checkpoint` |
| `budget_paused` | `run-event.json $defs/budget_paused` | `checkpoint` |
| `approval_requested` | `run-event.json $defs/approval_requested` | `callId`, `name`, `tool`, `source`, `args` |
| `compacted` | `run-event.json $defs/compacted` | `turns`, `bytes`, `digest`, `mode` |
| `recorded` | `run-event.json $defs/recorded` | `id`, `bytes` |
| `skill_loaded` | `run-event.json $defs/skill_loaded` | `skill`, `bytes` |
| `skill_unloaded` | `run-event.json $defs/skill_unloaded` | `id` |
| `retry` | `run-event.json $defs/retry` | `attempt` |
| `response_recovery` | `run-event.json $defs/response_recovery` |  |
| `delegation_requested` | `run-event.json $defs/delegation_requested` | `request` |
| `delegated_task` | `run-event.json $defs/delegated_task` | `task` |
| `user_question` | `run-event.json $defs/user_question` | `id`, `question` |
| `reasoning` | `run-event.json $defs/reasoning` | `text`, `done` |
| `message` | `run-event.json $defs/message` | `text` |
| `message_progress` | `run-event.json $defs/message_progress` | `text` |
| `tool_call` | `tool-call.json` | `name`, `args` |
| `tool_result` | `tool-result.json` | `name`, `isError`, `text` |
| `guardrail` | `run-event.json $defs/guardrail` | `tool`, `action`, `detail` |
| `thinking_unavailable` | `run-event.json $defs/thinking_unavailable` | `detail` |
| `critic_decided` | `run-event.json $defs/critic_decided` | `mode`, `ran`, `reason` |
| `critique` | `run-event.json $defs/critique` | `refuted`, `checks`, `text` |
| `proposal` | `run-event.json $defs/proposal` | `document`, `unknowns` |
| `error` | `run-event.json $defs/error` | `message` |
| `end` | `run-event.json $defs/end` |  |
| `policy_resolved` | `run-event.json $defs/policy_resolved` | `members` |
| `memory_proposed` | `run-event.json $defs/memory_proposed` | `memory` |
| `memory_recalled` | `run-event.json $defs/memory_recalled` | `ids`, `digests`, `bytes` |
| `edit_applied` | `run-event.json $defs/edit_applied` | `document`, `line`, `bytes`, `valid` |
| `edit_refused` | `run-event.json $defs/edit_refused` | `reason` |
| `output_guarded` | `run-event.json $defs/output_guarded` | `where`, `pattern`, `action` |

## Every kind with its members

### `execution_checkpoint`

The execution's state, for the host to save: at the start of a tracked run, after each tool call, tool result and question, and at the end. durable true asks the host to save before the next operation (anything but a read); absent or false means the save may be lazy. Emitted by the shared context boundary (context/managed.ts) and by the delegation coordinator, only where the session carries a current request or a saved execution.

Open: a member the schema does not name is accepted.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `execution_checkpoint` |  |
| `durable` |  | boolean |  |
| `checkpoint` | yes | `checkpoint.json` |  |

### `budget_paused`

Work paused at the request allowance: the allowance ran out, or the model asked for more with request_budget_increase. The checkpoint's status is paused; the run ends with end. Only the person's grant (a new checkpoint grant) continues it. An adapter's error with code request-limit is converted to this by the shared context boundary. On the vercel engine it is also how the turn's wall clock ends a turn (budget.json, turnMilliseconds): what is in flight is stopped, completed work is kept, and the checkpoint's reason says the turn reached its limit. Continuing it takes a request grant like any pause, so a run already at the 100-request cap cannot be continued, which the reason also says. A clock that passes while the critic runs leaves the proposal delivered with a note instead, and the run ends.

Open: a member the schema does not name is accepted.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `budget_paused` |  |
| `checkpoint` | yes | `checkpoint.json` |  |

### `approval_requested`

An act call paused for the person (ADR-0008): the model called a tool with a source whose effect class is act and whose approval is required, and the host's approval policy answered ask. The shared context boundary emits this in place of the call's tool_call, never runs the call, writes the call into the checkpoint's approval (state asked) with status paused, emits the checkpoint durable, and ends the run with end and no error: a model engine once the step's other calls are answered (its next request is never sent), an agent engine at the call (the invocation is stopped, as at a delegation handoff). The host's stream carries it, unlike delegation_requested: the person has to see the call. The host continues the paused checkpoint with the person's decision (checkpoint.json#/$defs/approvalDecision). callId is the call's id, drawn by the boundary where the run's events carry none; name the wire name the model called; tool the tool's own name at its source; source its record; args the arguments the model sent. Closed.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `approval_requested` |  |
| `callId` | yes | string (min length 1, max length 256) |  |
| `name` | yes | `tool-offer.json $defs/wireName` |  |
| `tool` | yes | string (min length 1, max length 128) |  |
| `source` | yes | `tool-offer.json $defs/sourceRecord` |  |
| `args` | yes | object |  |

### `compacted`

ADR-0016: the visible marker that one context compaction replaced complete turns. digest is the digest of the framed replacement; mode says whether the deterministic index or a model-authored summary was used.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `compacted` |  |
| `turns` | yes | integer (min 1) |  |
| `bytes` | yes | integer (min 1) |  |
| `digest` | yes | `provenance.json $defs/sha256` |  |
| `mode` | yes | enum `index`, `summary` |  |

### `recorded`

ADR-0022: a redacted evaluation corpus entry was written. id names its corpus directory and bytes is the sum of the UTF-8 bytes in its six JSON files.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `recorded` |  |
| `id` | yes | string (pattern `^[a-z0-9][a-z0-9-]{0,63}$`) |  |
| `bytes` | yes | integer (min 1) |  |

### `skill_loaded`

A skill loaded (ADR-0009): the model's load_skill call loaded it, between that call and its result. skill names it (tool-offer.json#/$defs/loadedSkill: id, name, source record, digest; never its instructions); bytes is what it counts against skillBytes (skill.json#/$defs/bytes). The checkpoint's skills carries it from here.

Open: a member the schema does not name is accepted.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `skill_loaded` |  |
| `skill` | yes | `tool-offer.json $defs/loadedSkill` |  |
| `bytes` | yes | `skill.json $defs/bytes` |  |

### `skill_unloaded`

A skill unloaded (ADR-0009): the model's unload_skill call dropped it, between that call and its result; id is the skill's id. Its block leaves every later request.

Open: a member the schema does not name is accepted.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `skill_unloaded` |  |
| `id` | yes | string (pattern `^[A-Za-z][A-Za-z0-9-]{0,23}$`) |  |

### `retry`

The adapter is sending again within this turn, attempt n of at most two. The codex engine relays the Codex bridge's recovery notices. The vercel engine emits it before it sends again a model request the provider refused (packages/harness/engines/vercel/retry.ts, retryingCall): only a failure whose outcome is refused and that failures.ts names (canRetryRefusal: the rate-limited class, a 429 or a rate-limit alias, and a 4xx carrying the overload code overloaded_error), never an unknown outcome (a 5xx, 408, 504 or a transport failure) and never a response that answered: a stream that breaks after its first byte is reported, not sent again; at most two retries, each after the longer of the backoff (1 s, then 2 s) and the provider's Retry-After, and none where Retry-After asks for more than 30 s, when the refusal is reported at once with its retryAt, nor where the retry would take the allowance's last request, which is the turn's tool-free final answer. Each attempt is a model request: counted against the allowance and recorded in the checkpoint's completions (provenance.json: attempt, failure, waitMs).

Open: a member the schema does not name is accepted.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `retry` |  |
| `attempt` | yes | integer (min 1, max 2) |  |

### `response_recovery`

The adapter is spending one tool-free request to finish a response cut short, to stop repeated work, or to ask once, within the allowance, for a proposal that could not be read, in the shape the contract reads (the reformat request). Only the vercel engine emits it.

Open: a member the schema does not name is accepted.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `response_recovery` |  |

### `delegation_requested`

The model called delegate_tasks on an engine whose run cannot wait for a tool call (codex: the provider account runs one agent run at a time, and the children need it). The adapter emits this instead of a tool_call and ends its run; the host's coordinator runs the batch and starts the response's next run with the call's result (delegated-task.json#/$defs/answer). An engine that awaits the call (vercel, agent#14) emits a tool_call and a tool_result for delegate_tasks instead, in the same run. The coordinator consumes this event: a host reading the coordinator's stream does not see it.

Open: a member the schema does not name is accepted.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `delegation_requested` |  |
| `request` | yes | `delegated-task.json $defs/request` |  |
| `callId` |  | string (min length 1, max length 256) | The agent wire's id of the delegate_tasks call (ADR-0007), where the engine has one: the Go agent engine sends it, and the coordinator keeps it in the checkpoint's handoff. The TypeScript codex engine sends none. |
| `subagent` |  | `subagent.json $defs/record` |  |

### `delegated_task`

A child task's state, whole, at each transition. Emitted by the host's delegation coordinator; on an engine that awaits the delegate call, between that call's tool_call and its tool_result.

Open: a member the schema does not name is accepted.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `delegated_task` |  |
| `task` | yes | `delegated-task.json` |  |
| `provider` |  | `provenance.json $defs/provider` |  |
| `subagent` |  | `subagent.json $defs/record` |  |

### `user_question`

The model called ask_user. The adapter emits this instead of a tool_call, ends the turn and waits; the answer comes with the next run. id is fresh per question.

Open: a member the schema does not name is accepted.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `user_question` |  |
| `id` | yes | string |  |
| `question` | yes | `question.json` |  |

### `reasoning`

The model's reasoning, as the provider streams it: deltas with done false, then the whole passage once with done true. Shown to the person, never sent to the runtime. Only the vercel engine emits it.

Open: a member the schema does not name is accepted.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `reasoning` |  |
| `text` | yes | string |  |
| `done` | yes | boolean |  |

### `message`

The model's own prose to the person, outside the proposal fence, as it wrote it. Never written by the host; absent where the model wrote nothing but the fence. Where the run's proposal could not be read, the whole final answer, prose and raw fence as written, is one message before the error: nothing the model finished is lost. On the vercel engine that is the answer before its reformat request; where the reformat request comes back as prose with no proposal, which a conversation allows, that prose is a second message, and the first answer's failure follows.

Open: a member the schema does not name is accepted.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `message` |  |
| `text` | yes | string |  |

### `message_progress`

The prose so far of the answer being written, whole each time (not a delta), with any proposal fence hidden; an empty text clears it. Only on an interactive session. taskId (agent#16): a delegated child's prose so far, under its task, forwarded by the delegation coordinator as the child writes it, between that task's delegated_task events; an empty text with its taskId clears it when the child ends. An event without taskId is the run's own answer: a host routes one with taskId to the task's view, never to the answer.

Open: a member the schema does not name is accepted.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `message_progress` |  |
| `text` | yes | string |  |
| `taskId` |  | `delegated-task.json $defs/uuid` | The delegated task (delegated-task.json id) whose child is writing this prose. |

### `tool_call`

The tool_call event: the model asked for one tool, with these arguments, and the adapter is about to hand the call to the host (callTool for a runtime tool, execute for a host tool). Emitted before the call is made, so the host can journal it; its arguments are the model's own, before the host's gate rewrites anything. A host tool with an interaction (ask_user, delegate_tasks) gets no tool_call: it gets user_question or delegation_requested instead. An act call of a tool with a source whose approval policy answers ask gets approval_requested instead (ADR-0008). Read from the AssistantEvent union (web/src/assistant/engine.ts). Open: no reader at run time refuses another member.

Open: a member the schema does not name is accepted.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `tool_call` |  |
| `callId` |  | string | Pairs the call with its tool_result. The codex engine always sets it (the server's 48-hex call id); the vercel engine sets it on an interactive session, always on an act call of a tool with a source (ADR-0008: its approval decision is tied to the call by it), and always on its refusal of a repeated call or of a call made on the final, tool-free request; never on a call to a tool the model was not offered. |
| `name` | yes | string | The tool's declared name, or the output-guarded display name for an undeclared model name (ADR-0017); dispatch still uses the original name. The codex engine offers its tools on the agent wire under aliases (desk_runtime_N, desk_host_N) and reports the real name here. A tool with a source is reported by its wire name, its own name in source.tool (ADR-0006). |
| `args` | yes | any | The arguments exactly as the model wrote them. Usually an object; a call to a tool the model was not offered carries whatever the model sent, or {}. |
| `presentation` |  | object | Host-owned presentation frozen when the tool was offered. Added by a host consumer (the research run) from the offered host tool, never by an engine and never from model arguments. |
| `presentation.provider` | yes | string |  |
| `origin` |  | const `harness` | harness: the harness made this call itself, not the model: the critic pass's own run of a declared case nothing compared yet (agent#10 review row 8; refutation.ts, compareTheRest). Absent on every call a model made. |
| `source` |  | `tool-offer.json $defs/sourceRecord` | The tool's source record (ADR-0006), on the call of a tool with a source, with tool, its own name there; name is then the wire name the model called. Absent on the Runtime's tools and the host's own. |

### `tool_result`

The tool_result event: what a tool answered, as the adapter reports it to the host after a tool_call. The model is shown the same answer, bounded (see $defs/reference). Read from the AssistantEvent union (web/src/assistant/engine.ts) as both engines emit it (engines/vercel/loop.ts, engines/codex/index.ts). Open: no reader at run time refuses another member. A failed tool is a result with isError true and the failure in structured, never an error event (toolFailureResult). The bound: a result whose JSON text, after an exact JSON text block is folded into structuredContent, is at most toolResultBytes (budget.json, 12000 by default) reaches the model whole. A larger one is retained in the execution's context ledger and the model is shown a reference envelope instead, which it reads back with the host tool the envelope names (readWith: read_window, by line or page in windows of readWindowBytes), or in sections with read_context (at most readBytes, 6000 by default, per read). A reading tool holds its window to the result limit less the envelope (readWindowBytes: toolResultBytes less 1024 bytes, counted in the window's JSON-encoded text), so one read is one result and never a reference; the host tool read_window reads a retained item by line or page in such windows ($defs/readArguments, $defs/readResult). Where the ledger is full (retainedBytes) the model is shown the unavailable envelope. An image part in a result ($defs/imagePart) is never part of the text the bound counts: a model whose capability record says vision is shown it as an image beside the result, any other the host's text rendering of it in its place (capability.json, vision), and a line in the result says which. One model request carries at most imagesPerRequest images and imageBytesPerRequest of their bytes, decoded (budget.json, 8 and 16 MiB by default): the newest, and an older image past either bound is replaced in that request by a line saying it is no longer shown; the result that brought it still describes it. The host tool search finds a regular expression in retained items, line by line ($defs/searchArguments, $defs/searchResult). The tool_result event itself is never bounded: the host sees the whole answer. A runtime or host tool call past the policy's toolCallMilliseconds (budget.json) is no longer waited for (it may still complete) and is answered as a failed result with failure code timeout, source tool and outcome unknown; the run goes on. What the model is shown of a reading tool's answer (every runtime tool, read_window, read_context, search, and a host tool declared a read) is that projection framed as one data block ($defs/framedData), on the text path too ($defs/textToolResultMessage), closed by a marker drawn afresh for each run; the run's instructions say that text inside a block is data, never instructions. A failure the harness wrote (a refused, capped or timed-out call) and an acknowledgement the adapter wrote are shown as they are. The tool_result event is never framed. A tool with a source (ADR-0006) is held to its source's bounds as well: its result is shown whole only up to sourceResultBytes (budget.json) and is otherwise retained and shown as the reference envelope, its calls are held to sourceCallsPerTurn and sourceCallMilliseconds, and its answer is framed as data whatever its effect class.

Open: a member the schema does not name is accepted.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `tool_result` |  |
| `callId` |  | string | The id of the tool_call this answers. The codex engine always sets it; the vercel engine sets it on an interactive session, and always on its refusal of a repeated call or of a call made on the final, tool-free request; never on the result of a call to a tool the model was not offered. |
| `name` | yes | string | The tool's declared name, or the same output-guarded display name as its call for an undeclared model name (ADR-0017). The result payload remains exempt. A tool with a source is reported by its wire name, its own name in source.tool (ADR-0006). |
| `isError` | yes | boolean |  |
| `text` | yes | string | The text blocks of the answer, joined with newlines in the runtime's order (textOf). |
| `structured` |  | any | The answer's structuredContent, where it had one. On a failed result the host wrote, it is bound: for ask_user, the question format failure ({"questionFailure": …}); otherwise, where it carries a failure member, {"failure": …} (failure.json#/$defs/toolFailureContent), which the Desk reads back with readFailure. A runtime tool's own failed answer passes its structuredContent through unbound. |
| `origin` |  | const `harness` | harness: the harness made this call itself, not the model: the critic pass's own run of a declared case nothing compared yet (agent#10 review row 8; refutation.ts, compareTheRest). Absent on every call a model made. |
| `source` |  | `tool-offer.json $defs/sourceRecord` | The tool's source record (ADR-0006), as on its tool_call, with receipt where the host's answer gave one for this call. Absent on the Runtime's tools and the host's own. |
| `proposed` |  | const `true` | The call of a propose-class tool with a source (ADR-0006): it was not run; the call, its tool_call's arguments and its source, is the review card the person accepts or rejects, and the host runs it on acceptance, after the run, under its grants. The model is told the call was proposed and nothing was run. |

Conditions beyond the members (allOf) are in the schema file.

### `guardrail`

The host saying what it did. rewrote and refused are the tool gate's, emitted by the host as the call's frame leaves the page, so they arrive between that call's tool_call and tool_result. narrowed is the adapter's notice, before the model is asked anything, that a tool's served schema lost a keyword on this family's wire. Also refused, with tool proposal, where the host withholds a proposal (Plan mode, or a conversation without artifact proposals). reason (ADR-0008), on a refusal of an act call of a tool with a source: approval refused, the host's approval policy answered refuse; approval pending, the call came while another act call of the run waits for the person's decision (at most one approval is pending); approval stale, the gate's own refusal of a call whose decision no longer holds for it (its arguments not those its approval was decided for, a call that reaches its run without an allow, or, on the continued run, its tool no longer offered as the person decided). None of these calls is run. skill withheld (ADR-0009), on a skill the package did not catalogue or restore, tool its name (or, for the catalogue's bounds, its source's id): its tools[] names a tool the host did not declare, its bytes pass skillBytes, its bundle does not hash to the digest the person approved, the catalogue's bounds left it out, or a skill the checkpoint had loaded is no longer declared as it was; skill budget, on a load_skill call refused past skillsPerRun, between that call and its failed result. Nothing of a withheld skill reaches the model. review refused (ADR-0010), on a request_review call refused: a second ask of the run, an ask under off or on, or one outside the author's phase; nothing is recorded and no review is asked.

Open: a member the schema does not name is accepted.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `guardrail` |  |
| `tool` | yes | string |  |
| `action` | yes | enum `rewrote`, `refused`, `narrowed` |  |
| `detail` | yes | string |  |
| `reason` |  | enum `approval refused`, `approval pending`, `approval stale`, `skill withheld`, `skill budget`, `review refused`, `policy refused`, `pace warning`, `subagent withheld`, `subagent refused`, `memory refused`, `edit refused`, `compaction refused`, `output refused`, `record refused` |  |

### `thinking_unavailable`

The reasoning tier the host asked for could not be expressed on this endpoint, said once. Only the vercel engine emits it; the sentence is the host's thinking table's.

Open: a member the schema does not name is accepted.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `thinking_unavailable` |  |
| `detail` | yes | string |  |

### `critic_decided`

The critic's decision for the run (ADR-0010; provenance.json#/$defs/criticDecision): emitted once per run, after the proposal exists and before the critic would start (and before its critique), or where the critic is skipped, a run with no proposal included, before the run's last message. mode, ran and reason always; ran means the switch allowed the critic pass (the pass may still report that it could not run, with zero checks); reasoning, the critic's tier, where the run states one; trigger under auto. The checkpoint's critic carries the same members, written durably after it.

Open: a member the schema does not name is accepted.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `critic_decided` |  |
| `mode` | yes | `tool-offer.json $defs/criticMode` |  |
| `ran` | yes | boolean |  |
| `reason` | yes | `provenance.json $defs/criticReason` |  |
| `reasoning` |  | `tool-offer.json $defs/criticReasoning` |  |
| `trigger` |  | enum `cases`, `changes-existing`, `model-asked` |  |

### `critique`

The critic pass's report, before the proposal it reviewed: refuted is decided from the runtime's own results and never from the critic's prose, true where a check came back as something other than the runtime's settled word (validate valid, experimental_evaluate evaluated) or, since agent#10, where a compared case's outcome is not the declared one, or a declared case was not compared at all (review row 1: an uncompared case is unsettled, so a critique that did not reach every declared case is never not refuted); checks lists each runtime check it ran with the runtime's status; text is the host's quote of them and of the comparisons. cases and incomplete are present where the host declared cases (session criticCases, $defs/criticCase): each comparison of a declared outcome with the runtime's ($defs/caseComparison), and the ids of the declared cases nothing compared, the harness's own calls for the uncompared included. figures: what the critic cost (provenance.json $defs/criticFigures).

Open: a member the schema does not name is accepted.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `critique` |  |
| `refuted` | yes | boolean |  |
| `checks` | yes | array of object |  |
| `checks[].tool` | yes | string |  |
| `checks[].status` | yes | string |  |
| `text` | yes | string |  |
| `cases` |  | array of `$defs/caseComparison` |  |
| `incomplete` |  | array of string | The declared cases nothing compared, by id. Any makes refuted true and is named on the proposal's critique. |
| `figures` |  | `provenance.json $defs/criticFigures` |  |
| `provider` |  | `provenance.json $defs/provider` |  |

### `proposal`

The one document the run proposes: from exactly one fenced JSON block shaped {"proposal": {"kind": "create", "document": …, "unknowns": […]}} and never from the prose around it, or from the model's call of the host tool propose_pack (tool-offer.json, $defs/proposePack), which the adapter reads itself under the same rules, the fence then being the fallback. A tool proposal beside a fenced one in the same answer is ambiguous and refused, as two fenced proposals are. critique is present only where a critic ran and reached at least one check or compared at least one declared case; note where something that ran after the proposal existed did not finish. Accept and Reject are the host's actions, not the adapter's.

Open: a member the schema does not name is accepted.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `proposal` |  |
| `document` | yes | object | A JSON object. |
| `unknowns` | yes | array of string |  |
| `critique` |  | object |  |
| `critique.refuted` | yes | boolean |  |
| `critique.incomplete` |  | array of string (min items 1) | The declared cases the critic's pass did not compare, by id (review row 1): present where any is, with refuted true, so the proposal's critique is never a plain not-refuted while a declared case is uncompared. |
| `note` |  | object | Something that ran after the proposal existed did not finish, and the proposal, being the finished work, is delivered all the same, with no critique and no error event ahead of it (refutation.ts, proposalNote). kind critic-unfinished: the critic started and did not finish (its provider error, its length cutoff, the allowance or the turn's wall clock). kind turn-unfinished: the proposal was made through propose_pack and a later request of the turn failed (the closing summary refused or cut off, the allowance, the turn's wall clock, or on the codex engine the run's own failure), so nothing after it ran, the critic included, and the run ends with the proposal. message is host-authored: one sentence saying what did not run, then the catalogue's sentence for the failure, or, for the allowance, a sentence saying it was used up (nothing pauses). failure is the failure that stopped it, classified, with source model on the vercel engine and agent on the codex engine, the turn's wall clock included. |
| `note.kind` | yes | enum `critic-unfinished`, `turn-unfinished` |  |
| `note.message` | yes | string |  |
| `note.failure` | yes | `failure.json` |  |
| `note.figures` |  | `provenance.json $defs/criticFigures` | On critic-unfinished: what the critic cost before it stopped (agent#10). |
| `edits` |  | `provenance.json $defs/edits` |  |

### `error`

The run failed. message is the catalogue's sentence for the class, or the failure's host-authored detail; failure is the classified failure. A failed tool is not an error event (tool_result with isError), and a cancelled run emits none.

Open: a member the schema does not name is accepted.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `error` |  |
| `message` | yes | string |  |
| `failure` |  | `failure.json` |  |

### `end`

The run is over. Exactly once, last.

Open: a member the schema does not name is accepted.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `end` |  |

### `policy_resolved`

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `policy_resolved` |  |
| `members` | yes | `policy.json $defs/resolved` |  |

### `memory_proposed`

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `memory_proposed` |  |
| `memory` | yes | `memory.json` |  |

### `memory_recalled`

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `memory_recalled` |  |
| `ids` | yes | array of string |  |
| `digests` | yes | array of string (pattern `^sha256:[0-9a-f]{64}$`) |  |
| `bytes` | yes | integer (min 0) |  |

### `edit_applied`

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `edit_applied` |  |
| `document` | yes | string (min length 1, max length 512) |  |
| `line` | yes | integer (min 1) |  |
| `bytes` | yes | integer (min 0) |  |
| `valid` | yes | const `true` |  |

### `edit_refused`

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `edit_refused` |  |
| `document` |  | string (min length 1, max length 512) | Offending document identifier, truncated to 512 Unicode characters. Omitted when missing, non-string or empty; reason names the invalid document identifier. |
| `reason` | yes | string (min length 1) |  |

### `output_guarded`

ADR-0017: outbound text held by a named pattern, without the matched text.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `output_guarded` |  |
| `where` | yes | enum `message`, `proposal`, `tool argument`, `memory proposal`, `draft edit` |  |
| `pattern` | yes | string (min length 1, max length 64, pattern `^[a-z][a-z0-9-]{0,63}$`) |  |
| `action` | yes | enum `redact`, `refuse` |  |
