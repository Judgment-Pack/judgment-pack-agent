# Delegation

## Sources

Schemas:

- `contract/schemas/delegated-task.json`
- `contract/schemas/subagent.json`
- `contract/schemas/tool-offer.json`
- `contract/schemas/checkpoint.json`
- `contract/schemas/provenance.json`

Package files (`packages/harness`), with the symbols read:

- `delegationContract.ts`: `TASK_PROFILES`, `DEFAULT_TASK_BUDGET`, `TASK_BUDGET_CEILING`, `REPORT_KINDS`, `REPORT_BOUNDS`, `MAX_TOOLS`, `MAX_BATCHES`, `MAX_AGENT_RUNS`, `MAX_REFERENCES`, `CHILD_OPEN_QUESTIONS`, `DELEGATE_TOOL`, `TOKEN_MEMBERS`
- `delegation.ts`: `RESULT_LIMIT`, `DEFAULT_CHILD_CONCURRENCY`

> generated; edit the sources, then run `node scripts/gen-reference.mjs`. `--check` fails where this page differs from them.

The built-in task profiles and their offers, the limits on `delegate_tasks`, the task and report records and the accounting of an agent capability.

See also: [tools.md](tools.md), [budget.md](budget.md), [events.md](events.md), [reasons.md](reasons.md), [checkpoint.md](checkpoint.md).

## Declared sub-agents

ADR-0013: a host-declared reads-only child. The host pins pinnedDigest at approval; the package computes digest over canonical JSON of all definition members except pinnedDigest and digest. Tools are declared host wire names; skills are declared ids. Bounds only narrow the turn. Built-in profile ids cannot be declared. No nesting or child critic.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `id` | yes | string (pattern `^[A-Za-z][A-Za-z0-9-]{0,23}$`) |  |
| `name` | yes | string (min length 1, max length 64) |  |
| `purpose` | yes | string (min length 1, max length 1024) |  |
| `instructions` | yes | string (min length 1, max length 4000) |  |
| `source` | yes | any of: any / any | Where a host tool comes from (ADR-0006). Declared by the host on each host tool a source outside the host serves; the package adds tool (the tool's own name at its source) on the offer, carries the record on the tool's tool_call and tool_result events (with receipt on a result where the host's answer gave one), and counts the source in the run report (provenance.json#/$defs/sourceFigures). gateway: a gateway connection the person configured (Databricks, Azure DevOps, Drive), which may give a receipt for each call; mcp: a public MCP server the host connected, whose tool list is pinned by digest at connection time; custom: a tool the person created; exchange (ADR-0009): a skill whose bytes the host fetched from a Judgment Exchange entry, the entry's id and version the record's id and version, which a host tool may not declare. runtime is the Runtime's own row in the run report: its tools come through tools/list behind the gate, carry no declaration, and a host tool declared with kind runtime is refused. Either version or toolListDigest is present; mcp carries toolListDigest. Closed: the package refuses another member before any request (configuration). |
| `source.kind` | yes | enum `custom`, `gateway`, `mcp`, `exchange` |  |
| `source.id` | yes | string (pattern `^[A-Za-z][A-Za-z0-9-]{0,23}$`) | The source's name as the host names it (a connection, a server, the person's own tools): a letter, then letters, digits and hyphens, at most 24 characters. Every wire name of the source's tools begins with it. |
| `source.version` |  | string (min length 1, max length 64) | The source's version as the host knows it: a connection's revision, a server's version, a custom tool's revision. |
| `source.toolListDigest` |  | string (pattern `^sha256:[0-9a-f]{64}$`) |  |
| `source.trust` | yes | enum `first-party`, `connected`, `public`, `person` | The trust class the host gives the source: first-party (the Runtime, and a source the host itself runs), connected (a connection the person configured with their own grants), public (a public server), person (a tool the person wrote). Reported, never a grant: whatever the class, the source's descriptions are untrusted text and its results data, and its tools are held to their effect class. |
| `tools` | yes | array of string (min length 1, max length 128) (max items 64) |  |
| `skills` | yes | array of string (pattern `^[A-Za-z][A-Za-z0-9-]{0,23}$`) (max items 32) |  |
| `model` | yes | string (min length 1, max length 200) |  |
| `reasoning` | yes | enum `off`, `on`, `ultra`, `none`, `minimal`, `low`, `medium`, `high`, `xhigh`, `max` |  |
| `bounds` | yes | object |  |
| `bounds.modelRequests` | yes | integer (min 1, max 10) |  |
| `bounds.agentRuns` | yes | integer (min 1, max 5) |  |
| `bounds.toolCalls` | yes | integer (min 1, max 12) |  |
| `bounds.bytes` | yes | integer (min 1) |  |
| `bounds.milliseconds` | yes | integer (min 1, max 360000) |  |
| `bounds.resultBytes` | yes | integer (min 1) | Admission withholds resultBytes below 113, the mandatory report fields with the longest confidence and 16 encoded summary bytes; the notice names the supplied bound and minimum. |
| `pinnedDigest` | yes | string (pattern `^sha256:[0-9a-f]{64}$`) |  |
| `digest` |  | string (pattern `^sha256:[0-9a-f]{64}$`) |  |

### `tool-offer.json $defs/subagentCatalogueEntry`

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `id` | yes | string (pattern `^[A-Za-z][A-Za-z0-9-]{0,23}$`) |  |
| `purpose` | yes | string |  |

Declared definitions use models.subagent.<id>, reads only, narrowing bounds and a pinned canonical digest. The catalogue is bounded by cataloguedSubagents (16) and subagentCatalogueBytes (4000). Built-ins remain fixed and are never declarations.

## Built-in profiles

| Profile | Capabilities | Runtime tools | Model | Model requests | Tool calls | Milliseconds | Report | Description |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| `analyze` | none | none | `child` | 6 | 6 | 120000 | `analyze` | Analyze supplied context without calling tools. |
| `review` | `read` | none | `child` | 6 | 6 | 120000 | `review` | Independently review supplied work and permitted read-only context. |
| `research` | `read`, `research` | none | `child` | 10 | 12 | 360000 | `research` | Research one focused question using the parent’s permitted source tools. |
| `author-test` | none | `get_schema`, `list_examples`, `get_example`, `validate`, `experimental_evaluate`, `experimental_test_cases` | `parent` | 8 | 8 | 240000 | `author-test` | Validate a draft pack or run its tests with the Runtime’s read-only tools. |

A profile that declares no budget takes the default; no profile may declare more than the ceiling, member by member, and its Runtime tools stay on the gate’s ceiling ([tools.md](tools.md)).

| Budget | Model requests | Tool calls | Milliseconds |
| --- | --- | --- | --- |
| default | 6 | 6 | 120000 |
| ceiling | 10 | 12 | 360000 |

## Limits

| Constant | Value | Source comment |
| --- | --- | --- |
| `MAX_TOOLS` | 20 | The tool calls one response may make, the parent's and its delegated children's together; the context tools (list_context, read_context, read_window, search, update_plan, request_budget_increase) are not counted. Stated to the model in DELEGATION_INSTRUCTIONS and in the execution state's `toolCalls` (agent#13). |
| `MAX_BATCHES` | 4 | The batches one response may run (agent#14): more than one, each within the shared limits. |
| `MAX_AGENT_RUNS` | 5 | The agent runs one response may start on an agent capability, the parent's and its children's together. A batch ends the parent's run and a new one continues the response, so delegate_tasks is offered only while a child run and that next run both remain, and no child takes the last run. |
| `MAX_REFERENCES` | 20 | The most references one task may carry, and the longest id. |
| `CHILD_OPEN_QUESTIONS` | 4 | The open questions a child may write in its own block; the coordinator adds what its run asked of the parent. |
| `RESULT_LIMIT` | 12000 | The longest findings a task record carries, in characters; longer findings are kept whole in the ledger (agent#13). |
| `DEFAULT_CHILD_CONCURRENCY` | 2 | Children a batch runs at once on a model capability whose record names no capacity (agent#17). |

Bounds on one report:

| Member | Bound |
| --- | --- |
| `summary` | 800 |
| `findings` | 12 |
| `findingText` | 600 |
| `findingCitations` | 4 |
| `citations` | 12 |
| `citationId` | 300 |
| `quote` | 300 |
| `openQuestions` | 6 |
| `questionText` | 400 |
| `notDone` | 8 |
| `notDoneText` | 300 |
| `caseId` | 128 |
| `fix` | 600 |

## The task and its report

### `delegated-task.json $defs/uuid`

Type: string (pattern `^[a-f0-9-]{36}$`)

### `delegated-task.json $defs/time`

An ISO 8601 time as toISOString writes it; the reader accepts at most 64 characters that Date.parse reads.

Type: string (max length 64)

### `delegated-task.json $defs/state`

Type: enum `queued`, `running`, `complete`, `failed`, `interrupted`

### `delegated-task.json $defs/request`

What the model asks for with delegate_tasks (DelegationRequest): the call's arguments, and, from an engine that ends its run at the call (codex), the delegation_requested event's request. Closed: readDelegationRequest refuses a member it does not name, here and in each task. One to three tasks per batch; a response may run up to four batches (MAX_BATCHES), one at a time.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `tasks` | yes | array of `$defs/taskRequest` (min items 1, max items 3) |  |

### `delegated-task.json $defs/taskRequest`

One task, as the model asks for it. Closed. Since agent#15 the task carries no copied context: material is passed by reference.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `title` | yes | string (max length 80, pattern `\S`) |  |
| `objective` | yes | string (max length 2000, pattern `\S`) | The child's request text (agent#11): the first text part of its first message. |
| `profile` | yes | string (max length 48, pattern `\S`) | A built-in profile id, or a catalogued declared sub-agent id. The package attaches subagent, never accepting a model-supplied record or bounds. |
| `references` |  | array of string (max length 300, pattern `\S`) (max items 20) | Entries of the parent's context the child reads by reference (agent#15): a contextId from list_context or a result reference, or the id the host gave one of its documents. Resolved in the parent's chat alone; one the parent's context does not hold fails the call before any task starts. |
| `resume` |  | `$defs/uuid` | The id of a finished task of this chat to continue with its earlier context (agent#14). An id the chat holds no finished task for fails the call before any task starts. |
| `summarize` |  | boolean | Where the findings run past the task's share of the answer, the child is asked once, in one request of its budget, to summarize them within it (agent#13). |
| `subagent` |  | `subagent.json $defs/record` |  |

### `delegated-task.json $defs/profile`

A task profile (TaskProfile): what a child is told, which host-tool capabilities and which of the gate's Runtime tools it is offered, which model it runs on and its budget (agent#15), and the report shape its children write (agent#16). A host tool reaches a child only where its own delegation member names a capability the profile lists; a Runtime tool only where the profile names it and the parent's session was served it through the gate. The closed profiles are vectors/delegation/profiles.json; a feature adapter may add one (options.profiles), held by readProfiles to a budget within the ceiling (10 model requests, 12 tool calls, 360,000 ms) and Runtime tools on the gate's ceiling.

Open: a member the schema does not name is accepted.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `id` | yes | string |  |
| `description` | yes | string |  |
| `instructions` | yes | string |  |
| `capabilities` | yes | array of enum `read`, `research` |  |
| `runtimeTools` |  | array of `tool-offer.json $defs/runtimeCeiling` | The gate's Runtime tools a child of this profile is offered; none where absent. |
| `model` |  | enum `child`, `parent` | child: the child model the host names for the connection (modelsFor) where it names one, else the parent's; parent: the parent's always. Absent: child. |
| `budget` |  | `$defs/budget` | Absent: six model requests, six tool calls, 120,000 ms (DEFAULT_TASK_BUDGET). |
| `report` |  | enum `analyze`, `review`, `research`, `author-test` | The report shape its children write (agent#16): $defs/analyzeReport, reviewReport, researchReport or authorTestReport. Absent: analyze. |

### `delegated-task.json $defs/budget`

One child's limits (TaskBudget, agent#15): the model requests and the tool calls it may make, and its time limit. A batch is bounded by its children's time limits end to end; the delegate call has no tool-call deadline of its own.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `modelRequests` | yes | integer (min 1, max 10) |  |
| `toolCalls` | yes | integer (min 1, max 12) |  |
| `milliseconds` | yes | integer (min 1, max 360000) |  |

### `delegated-task.json $defs/resultCursor`

Where a child's whole findings are kept when its result is their head or a summary (agent#13): an entry of the chat's ledger, read on with read_window from cursor (tool-result.json's read cursor: line:<offset>, or line:<offset>+<column> inside a line).

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `readWith` | yes | const `read_window` |  |
| `id` | yes | string (max length 300, pattern `\S`) |  |
| `cursor` | yes | string (pattern `^line:\d{1,9}(\+\d{1,9})?$`) |  |
| `totalCharacters` | yes | integer (min 0) | The whole findings' length, in UTF-16 code units as the reader counts them. |

### `delegated-task.json $defs/answer`

What the parent is answered for a batch (agent#14): the delegate call's result, as structuredContent and its JSON text, shown to the model as one data block (framing.ts); on an engine that ends its run at the call, the JSON the response's next run is given. Each task's report and text are held to its share of one tool result, so the answer fits toolResultBytes. Closed: the coordinator writes these members and no other.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `tasks` | yes | array of `$defs/answerTask` (min items 1, max items 3) |  |
| `note` | yes | string (pattern `\S`) |  |

### `delegated-task.json $defs/answerTask`

One task of a batch's answer: its record's id, title, objective, profile, references, state, resumes, report, result, more, summarized, partial, reused and problem, what it used, and its budget. report is held to the task's share of the answer, whole where it fits; else cut by whole items (notDone, then findings from the last with the citations only they cited, then citations, then open questions, then the summary's start) and reportCut is true: the record carries the whole, and the whole text with its report block is at more. The text takes what the share leaves.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `id` | yes | `$defs/uuid` |  |
| `title` | yes | string (max length 80, pattern `\S`) |  |
| `objective` | yes | string (max length 2000, pattern `\S`) | What the task was asked (review of agent#35, row 8), so a codex parent's next run, which keeps none of the earlier run's messages, still has it. |
| `profile` | yes | string (max length 48, pattern `\S`) |  |
| `references` |  | array of string (max length 300, pattern `\S`) (max items 20) |  |
| `state` | yes | `$defs/state` |  |
| `resumes` |  | `$defs/uuid` |  |
| `report` |  | `$defs/report` |  |
| `reportCut` |  | const `true` |  |
| `result` |  | string (max length 12000, pattern `\S`) |  |
| `more` |  | `$defs/resultCursor` |  |
| `summarized` |  | const `true` |  |
| `partial` |  | const `true` |  |
| `reused` |  | `$defs/uuid` |  |
| `problem` |  | string (max length 1000, pattern `\S`) |  |
| `used` | yes | object |  |
| `used.modelRequests` | yes | integer (min 0) |  |
| `used.toolCalls` | yes | integer (min 0) |  |
| `budget` |  | `$defs/budget` |  |
| `subagent` |  | `subagent.json $defs/record` |  |

### `delegated-task.json $defs/brief`

The JSON line a child's prompt ends with (review of agent#35, row 9), after its profile's instructions and its rules: its objective, the entries it was given by reference, on a resumed task its earlier objectives and findings, and its time limit in seconds. Nothing of a reference's content is copied here; the child reads it with read_window by its id. The objective is also the child's request text, the first text part of its first message (agent#11). Closed: the coordinator writes these members and no other.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `objective` | yes | string (max length 2000, pattern `\S`) |  |
| `references` |  | array of `$defs/briefReference` (max items 20) |  |
| `earlierWork` |  | array of `$defs/earlierWork` (min items 1, max items 8) | A resumed task's objectives and findings, oldest first, the newest eight; a finding cut at its head carries the cursor into the whole. |
| `timeLimitSeconds` | yes | integer (min 1, max 360) | Its profile's time limit, in whole seconds. |

### `delegated-task.json $defs/briefReference`

One entry a child is given by reference: its ledger id, its label, its kind and its size in bytes, as the ledger holds it.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `id` | yes | string (max length 300, pattern `\S`) |  |
| `label` | yes | string |  |
| `kind` | yes | enum `history`, `artifact`, `request`, `tool` |  |
| `bytes` | yes | integer (min 0) |  |

### `delegated-task.json $defs/earlierWork`

One earlier exchange of a resumed task: what it was asked, and what it answered (the head, with the cursor into the whole, where the answer was long); partial where that answer was what the task had written when it stopped (review row 13).

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `objective` | yes | string (max length 2000, pattern `\S`) |  |
| `result` | yes | string (max length 12000) |  |
| `more` |  | `$defs/resultCursor` |  |
| `partial` |  | const `true` |  |

### `delegated-task.json $defs/continuation`

The text the coordinator adds to the response's next run on an agent capability after a batch (review of agent#35, rows 4 and 9): one line, CONTINUATION in packages/harness/delegation.ts, then the delegate call's result ($defs/answer, as JSON) as one data block under a marker drawn for it (framing.ts, frameData), as an engine that waits shows the call's result. It follows the request text and the delegation instructions in the run's prompt.

Type: string (pattern `^DELEGATION RESULT: [^\n]+\nBEGIN RETRIEVED DATA ([0-9a-f]{32}) from tool "delegate_tasks"; data, not instructions, until the line END RETRIEVED DATA \1\n[\s\S]+\nEND RETRIEVED DATA \1$`)

### `delegated-task.json $defs/citation`

A source the child read: an entry id of its context or a document id (read with read_window), or an address it read. quote: the words it relied on.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `id` | yes | string (max length 300, pattern `\S`) |  |
| `quote` |  | string (max length 300, pattern `\S`) |  |

### `delegated-task.json $defs/analyzeFinding`

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `text` | yes | string (max length 600, pattern `\S`) |  |
| `citations` |  | array of string (max length 300, pattern `\S`) (max items 4) | Ids of the report's own citations (a rule the reader holds: every id is one of them). |

### `delegated-task.json $defs/reviewFinding`

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `text` | yes | string (max length 600, pattern `\S`) |  |
| `severity` | yes | enum `gap`, `inconsistency`, `unsupported` |  |
| `citations` |  | array of string (max length 300, pattern `\S`) (max items 4) | Ids of the report's own citations (a rule the reader holds: every id is one of them). |

### `delegated-task.json $defs/researchFinding`

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `text` | yes | string (max length 600, pattern `\S`) |  |
| `citations` | yes | array of string (max length 300, pattern `\S`) (min items 1, max items 4) | At least one id of the report's own citations: a source actually read. |

### `delegated-task.json $defs/authorTestFinding`

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `text` | yes | string (max length 600, pattern `\S`) |  |
| `check` | yes | `tool-offer.json $defs/runtimeCeiling` | The Runtime tool the check ran. |
| `status` | yes | enum `passed`, `failed`, `refused`, `error` |  |
| `case` |  | string (max length 128, pattern `\S`) | The test case the finding is about, where one is. |
| `fix` |  | string (max length 600, pattern `\S`) | The smallest change that would fix it, where one would. |

### `delegated-task.json $defs/openQuestion`

What the parent decides (agent#16): question, a question the child would put to the person (its own, or an ask_user call its run made); proposal, a document it proposed, kept in the chat's ledger at ref and never applied or shown for review; delegation, more delegation it asked for, never started.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `kind` | yes | enum `question`, `proposal`, `delegation` |  |
| `text` | yes | string (max length 400, pattern `\S`) |  |
| `ref` |  | string (max length 300, pattern `\S`) | A ledger entry id, read with read_window. |

### `delegated-task.json $defs/analyzeReport`

The analyze profile's report (agent#16), as its child writes it in one fenced json block whose root has a single report member at the end of its answer, read closed (readReport). summary: what the parent reads first. confidence unstated only where the coordinator wrote the report. A child is told to write at most four openQuestions; the coordinator adds what its run asked.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `summary` | yes | string (max length 800, pattern `\S`) |  |
| `findings` | yes | array of `$defs/analyzeFinding` (max items 12) |  |
| `citations` | yes | array of `$defs/citation` (max items 12) |  |
| `openQuestions` | yes | array of `$defs/openQuestion` (max items 6) |  |
| `confidence` | yes | enum `low`, `medium`, `high`, `unstated` |  |
| `notDone` | yes | array of string (max length 300, pattern `\S`) (max items 8) | What the child did not do or could not check. |

### `delegated-task.json $defs/reviewReport`

The review profile's report (agent#16), as its child writes it in one fenced json block whose root has a single report member at the end of its answer, read closed (readReport). summary: what the parent reads first. confidence unstated only where the coordinator wrote the report. A child is told to write at most four openQuestions; the coordinator adds what its run asked.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `summary` | yes | string (max length 800, pattern `\S`) |  |
| `findings` | yes | array of `$defs/reviewFinding` (max items 12) |  |
| `citations` | yes | array of `$defs/citation` (max items 12) |  |
| `openQuestions` | yes | array of `$defs/openQuestion` (max items 6) |  |
| `confidence` | yes | enum `low`, `medium`, `high`, `unstated` |  |
| `notDone` | yes | array of string (max length 300, pattern `\S`) (max items 8) | What the child did not do or could not check. |

### `delegated-task.json $defs/researchReport`

The research profile's report (agent#16), as its child writes it in one fenced json block whose root has a single report member at the end of its answer, read closed (readReport). summary: what the parent reads first. confidence unstated only where the coordinator wrote the report. A child is told to write at most four openQuestions; the coordinator adds what its run asked.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `summary` | yes | string (max length 800, pattern `\S`) |  |
| `findings` | yes | array of `$defs/researchFinding` (max items 12) |  |
| `citations` | yes | array of `$defs/citation` (max items 12) |  |
| `openQuestions` | yes | array of `$defs/openQuestion` (max items 6) |  |
| `confidence` | yes | enum `low`, `medium`, `high`, `unstated` |  |
| `notDone` | yes | array of string (max length 300, pattern `\S`) (max items 8) | What the child did not do or could not check. |

### `delegated-task.json $defs/authorTestReport`

The author-test profile's report (agent#16), as its child writes it in one fenced json block whose root has a single report member at the end of its answer, read closed (readReport). summary: what the parent reads first. confidence unstated only where the coordinator wrote the report. A child is told to write at most four openQuestions; the coordinator adds what its run asked.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `summary` | yes | string (max length 800, pattern `\S`) |  |
| `findings` | yes | array of `$defs/authorTestFinding` (max items 12) |  |
| `citations` | yes | array of `$defs/citation` (max items 12) |  |
| `openQuestions` | yes | array of `$defs/openQuestion` (max items 6) |  |
| `confidence` | yes | enum `low`, `medium`, `high`, `unstated` |  |
| `notDone` | yes | array of string (max length 300, pattern `\S`) (max items 8) | What the child did not do or could not check. |

### `delegated-task.json $defs/report`

A task's report, in its profile's shape: one of the four (agent#16). The record names its profile; profile.report says which shape.

Type: any of: `$defs/analyzeReport` / `$defs/reviewReport` / `$defs/researchReport` / `$defs/authorTestReport`

### `delegated-task.json $defs/hostRequest`

What a host passes with a response that the delegation coordinator reads beside its other context inputs (ContextInput in packages/harness/context/session.ts), as far as reuse goes (agent#17, review row 9). delegationReuse false: this response's delegated tasks reuse no earlier result of the chat, as for a Retry the person asked to run afresh; identical tasks of one batch still run once. Absent or true: a complete result of an identical task is reused, so a Retry of the same message gets the same children's results. Open: ContextInput carries other members.

Open: a member the schema does not name is accepted.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `delegationReuse` |  | boolean |  |

## The agent capability’s accounting

Where unit is model-requests, the delegation figures count model requests and carry modelRequests on the parent, on each child and on each batch's total (agent#71); where unit is provider-managed (an agent capability) they may omit it, and nothing writes 0 there.

One response may start at most 5 agent runs on an agent capability, the parent's and the children's together (`MAX_AGENT_RUNS`). Token figures count `inputTokens`, `outputTokens`, `reasoningTokens`, `cacheReadTokens`, `cacheWriteTokens`.

### `provenance.json $defs/tokenFigures`

Token counts the provider reported, summed over requests (agent#18): each member as completion's, absent where no request reported it.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `inputTokens` |  | `$defs/tokens` |  |
| `outputTokens` |  | `$defs/tokens` |  |
| `reasoningTokens` |  | `$defs/tokens` |  |
| `cacheReadTokens` |  | `$defs/tokens` |  |
| `cacheWriteTokens` |  | `$defs/tokens` |  |

### `provenance.json $defs/childFigures`

One delegated child's figures (agent#18), as its task record carries them. modelRequests: on a model capability, its requests, a retried attempt counted; absent on an agent capability (agent#71), the provider running its own loop and reporting no request count, and agentRuns its runs. The checkpoint requires it where its unit is model-requests and leaves it optional where the unit is provider-managed (checkpoint.json, delegation). firstByteMs: from its start to the first byte of its first answer (an agent's first event), absent where none came. durationMs: from its start to its end. partial and reused as the record's. Declared children carry subagent; group these figures by id and digest for per-definition totals. Built-ins carry profile alone.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `task` | yes | `delegated-task.json $defs/uuid` |  |
| `batch` | yes | integer (min 1, max 100) | Which batch of the execution, from 1. |
| `profile` | yes | string (max length 48, pattern `\S`) |  |
| `model` |  | string (max length 200, pattern `\S`) |  |
| `state` | yes | `delegated-task.json $defs/state` |  |
| `modelRequests` |  | integer (min 0, max 100) |  |
| `toolCalls` | yes | integer (min 0, max 100) |  |
| `agentRuns` |  | integer (min 0, max 100) |  |
| `tokens` |  | `$defs/tokenFigures` |  |
| `firstByteMs` |  | integer (min 0, max 86400000) |  |
| `durationMs` | yes | integer (min 0, max 86400000) |  |
| `partial` |  | const `true` |  |
| `reused` |  | const `true` |  |
| `subagent` |  | `subagent.json $defs/record` |  |
| `problem` |  | string (max length 1000) |  |

### `provenance.json $defs/figuresTotal`

A batch's total (agent#18): its children's modelRequests, toolCalls, agentRuns and tokens summed (a token member present where any child's was), and its wall time from its first child's start to its last child's end. modelRequests counts model requests only: absent on an agent capability, where no child has one (agent#71), and required where the checkpoint's unit is model-requests (checkpoint.json, delegation).

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `modelRequests` |  | integer (min 0) |  |
| `toolCalls` | yes | integer (min 0) |  |
| `agentRuns` |  | integer (min 0) |  |
| `tokens` |  | `$defs/tokenFigures` |  |
| `durationMs` | yes | integer (min 0, max 86400000) |  |

### `provenance.json $defs/batchFigures`

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `batch` | yes | integer (min 1, max 100) |  |
| `children` | yes | array of `$defs/childFigures` (min items 1, max items 3) |  |
| `total` | yes | `$defs/figuresTotal` |  |

### `provenance.json $defs/parentFigures`

The parent's own figures beside its batches (agent#18): its model; its own model requests, the execution's used less its children's, absent on an agent capability, where the provider runs its own loop and reports no count (agent#71; required where the checkpoint's unit is model-requests, checkpoint.json, delegation); on an agent capability, its own agent runs of the response; its own token counts, from its completion rows, absent on an agent capability.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `model` | yes | string (max length 200, pattern `\S`) |  |
| `modelRequests` |  | integer (min 0) |  |
| `agentRuns` |  | integer (min 0) |  |
| `tokens` |  | `$defs/tokenFigures` |  |

### `provenance.json $defs/delegationReport`

What delegation cost in one execution (agent#18), as the checkpoint's delegation member carries it: each batch with its children's figures and its total, and the parent's own. Written by the delegation coordinator on every checkpoint of the parent after its first batch, carried through a resume as used is, and read closed by the harness's readExecution (validDelegationFigures). The Desk's report lines that show it are the Desk's (desk#499).

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `batches` | yes | array of `$defs/batchFigures` (max items 20) |  |
| `parent` | yes | `$defs/parentFigures` |  |

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
