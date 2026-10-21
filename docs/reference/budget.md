# The context budget

## Sources

Schemas:

- `contract/schemas/budget.json`

Package files (`packages/harness`), with the symbols read:

- `context/policy.ts`: `CONTEXT_DEFAULTS`
- `prompt.ts`: `HOST_PARTS`, `BRIEF_HEADER`, `PROJECT_INSTRUCTIONS_HEADER`, `MEMORY_HEADER`
- `execution/state.ts`: `REQUEST_INCREMENT`, `MAX_REQUEST_ALLOWANCE`, `MAX_CHECKPOINT_BYTES`, `MAX_PROMPT_DIGESTS`
- `execution/progress.ts`: `REPEATED_WORK_LIMIT`
- `engines/contract.ts`: `MAX_TURNS`
- `engines/vercel/retry.ts`: `MODEL_RETRIES`, `RETRY_BACKOFF_MS`, `RETRY_WAIT_LIMIT_MS`
- `context/chat.ts`: `INDEX_SHARE`
- `images.ts`: `IMAGE_BYTES`

> generated; edit the sources, then run `node scripts/gen-reference.mjs`. `--check` fails where this page differs from them.

The context budget’s members with their defaults and what each bounds, the host’s parts of a request and the other bounds the package holds.

See also: [tools.md](tools.md), [skills.md](skills.md), [delegation.md](delegation.md), [critic.md](critic.md), [checkpoint.md](checkpoint.md), [reasons.md](reasons.md).

## The table

Read alias for policy.budget for the v0.8.0 release; retired in the next release. Legacy criticDefault, criticReasoningFloor and adversarialReview read into policy.critic. New nested members win where both are stated. The context budget one run works under: the provider-independent byte and token limits both the browser and the Desk server read from one table (internal/aicontext/policy.json, ContextPolicy in web/src/assistant/context/policy.ts, aicontext.Policy in Go). The model's context window and output reserve may be overridden per model by the assistant profile (configuration); everything else is fixed. Input bytes for one request are min(maxInputBytes, max(1024, (contextWindowTokens - outputTokens) * 3)); the request allowance takes no reserve for the host's parts: briefBytes and memoryBytes are the bounds on the brief and the recalled memory (each held to its bound, $defs/hostPart), which the adapter places itself (ADR-0005) and counts once, where they sit in the instructions (until agent#69 the adapter left both out of every request's allowance whether or not the parts were present, a reserve kept from when the server added them behind the adapter), and the server refuses before dispatch, never clipping, a request over the input bytes (context-limit, not-started). Open: the Go reader ignores a member it does not name. The request allowance, the grants and the fixed limits are in $defs. The harness adds two time bounds the Desk's table does not carry (packages/harness/context/policy.ts, TIME_DEFAULTS), each taken from the table where it does: turnMilliseconds and toolCallMilliseconds; and three bounds on the Runtime's test tool (TEST_DEFAULTS), likewise: testCallsPerTurn, testPackBytes and testMatrixBytes; and two image bounds (IMAGE_DEFAULTS), likewise: imagesPerRequest and imageBytesPerRequest; and two bounds on the index of earlier results a chat-scoped run's first message carries (INDEX_DEFAULTS, agent#8), likewise: turnIndexEntries and turnIndexBytes; and the bound on the project's own instructions (PROMPT_DEFAULTS, ADR-0005), likewise: projectInstructionsBytes. The table also states the critic's switch (ADR-0010), which the harness adds to the Desk's table: criticDefault, the mode where neither the run nor the project states one, and criticReasoningFloor, the critic's tier where the host names none; adversarialReview, the switch before ADR-0010, is read as an alias of criticDefault for one release. And the bounds on tools with a source (SOURCE_DEFAULTS, ADR-0006), likewise: toolDescriptionBytes, offeredSchemas, offeredSchemaBytes, cataloguedTools, catalogueBytes, sourceResultBytes, sourceCallsPerTurn and sourceCallMilliseconds. The skills budget (ADR-0009): skillBytes, skillsPerRun, skillsCatalogueBytes and cataloguedSkills.

| Member | Required | Type | Default | What it bounds |
| --- | --- | --- | --- | --- |
| `version` | yes | const `1` | `1` |  |
| `maxInputBytes` | yes | integer (min 1) | `180000` | Fixed transport ceiling on one request's input, whatever the window. |
| `contextWindowTokens` | yes | integer (min 4096, max 2000000) | `65536` | The model's context window, in tokens, for the byte estimate (three bytes a token). |
| `outputTokens` | yes | integer (min 1024) | `8192` | The output reserve. A request's own max_tokens, max_completion_tokens or maxOutputTokens raises it up to half the window; above half, the request is refused. |
| `toolResultBytes` | yes | integer (min 1) | `12000` | The largest tool result, as JSON text, the model is shown whole (tool-result.json). |
| `readBytes` | yes | integer (min 128) | `6000` | The largest read_context window. |
| `retainedBytes` | yes | integer (min 1) | `8388608` | What one execution's context ledger may retain in all. |
| `recentMessages` | yes | integer (min 0) | `8` | ADR-0016: the newest messages retained verbatim when summary compaction replaces older complete turns. |
| `summaryBytes` | yes | integer (min 1) | `4000` | ADR-0016: the most UTF-8 bytes of model-authored summary text retained by one context compaction. |
| `historyBytes` | yes | integer (min 0) | `32000` | Declared in the table; read by the server's research worker, not by the assistant loop. |
| `briefBytes` | yes | integer (min 0) | `8000` | The most UTF-8 bytes of the project brief the host's brief() may answer (ADR-0005; before it, the most the server added behind the adapter). A longer answer is refused before the run's first request (configuration) and nothing is sent. |
| `memoryBytes` | yes | integer (min 0) | `4000` | ADR-0014: most UTF-8 bytes of accepted, unexpired records recalled by memoryFor: sum of canonical JSON bytes before framing. The v0.8.0 bare-text alias uses text bytes. A larger answer fails configuration before any request. No reserve is taken; memory is counted where it sits. context.memory false disables recall. |
| `turnMilliseconds` |  | integer (min 1) | `900000` | One turn's wall clock, from the run's start, on the vercel engine: when it passes, what is in flight is stopped and the run pauses as budget_paused does, with its checkpoint and a reason; a proposal already made is delivered, with a note, and the run ends. Continuing the pause takes a request grant, so at the 100-request cap it cannot be continued; the reason says so. The codex engine has none of its own (the Codex bridge bounds a run at ten minutes). Not in the Desk's table; the harness's default is 900000 (fifteen minutes). |
| `toolCallMilliseconds` |  | integer (min 1) | `120000` | One runtime or host tool call's deadline, on both engines: a call past it is no longer waited for (it may still complete; a host tool is also handed the deadline's abort signal) and is answered to the model as a failed result (failure code timeout, source tool, outcome unknown), and the run goes on. propose_pack, which the adapter reads itself, and a tool that pauses the run for the person or the coordinator have none. Not in the Desk's table; the harness's default is 120000 (two minutes). |
| `imagesPerRequest` |  | integer (min 0) | `8` | The most images one model request carries, on the vercel engine (agent#5): the newest; an older image past it is replaced in that request by a line saying it is no longer shown, and the result that brought it still describes it. The codex engine's agent wire carries text and shows none. Not in the Desk's table; the harness's default is 8. |
| `imageBytesPerRequest` |  | integer (min 0) | `16777216` | The most bytes, decoded, of the images one model request carries, counted from the newest with imagesPerRequest; an older image that would pass it is replaced by the same line. Not in the Desk's table; the harness's default is 16777216 (16 MiB, one image at its own bound). |
| `testCallsPerTurn` |  | integer (min 0) | `3` | How many experimental_test_cases calls one turn may make, on both engines (context/session.ts, claimTestCall). Each call re-sends the whole pack, so a test, fix and retest loop is held to a few rounds rather than to the request allowance. A call past it is not made: the model reads a failed result (failure code tool-failed, source tool, outcome not-started) naming the limit, and the run goes on. Counted per turn: the count is the checkpoint's testCalls (checkpoint.json), carried as used is through a Retry, a budget continuation and a delegation's parent continuation, and the delegation coordinator counts the parent and its continuation together; a new turn starts at nought. Not in the Desk's table; the harness's default is 3. |
| `testPackBytes` |  | integer (min 1) | `65536` | The most UTF-8 bytes experimental_test_cases' pack text may be; the tool gate refuses a larger call on the wire (tool-call.json $defs/testCasesArguments). Not in the Desk's table; the harness's default is 65536. |
| `testMatrixBytes` |  | integer (min 1) | `65536` | The most UTF-8 bytes experimental_test_cases' matrix text may be; the tool gate refuses a larger call on the wire. Not in the Desk's table; the harness's default is 65536. |
| `adversarialReview` |  | boolean |  | The critic's default before ADR-0010, kept for one release as a read alias of criticDefault: true reads as on, false as off. Where the table states criticDefault too, criticDefault wins. The harness's table (contract/desk/policy.json) no longer carries it. |
| `criticDefault` |  | `tool-offer.json $defs/criticMode` | `"auto"` | The critic's mode where neither the run (session critic) nor the project (session criticProject) states one (ADR-0010; tool-offer.json#/$defs/criticMode): auto, so a proposal with declared cases, a proposal that changes an existing pack, or one the model asked a review of is reviewed, and a conversation turn is not. The harness adds it to the Desk's table (contract/desk/policy.json). |
| `criticReasoningFloor` |  | enum `author` | `"author"` | The critic's reasoning tier where the host names none (ADR-0010): author, the author's own tier on the run's capability, so a critic is never run at a lower tier than the author unless the host says so explicitly (session criticReasoning). The one value this release defines. The harness adds it to the Desk's table. |
| `turnIndexEntries` |  | integer (min 0) | `20` | How many earlier results the index a chat-scoped run's first message carries lists, the newest; older ones are counted in its omitted member (tool-result.json#/$defs/turnIndex, agent#8). Not in the Desk's table; the harness's default is 20. |
| `turnIndexBytes` |  | integer (min 0) | `8000` | The most UTF-8 bytes that index may be, its line and data block together (agent#8). It is also cut to the room the run's first message leaves in the prompt's allowance, and to at most a quarter of that allowance (INDEX_SHARE): rows go, oldest first, until it fits, an index with no room is not sent, and the request is never previewed and a run never fails because of it. Not in the Desk's table; the harness's default is 8000. |
| `projectInstructionsBytes` |  | integer (min 0) | `4000` | The most UTF-8 bytes of the project's own instructions the host's projectInstructions() may answer (ADR-0005): instructions at the lowest rank, placed after the package's guidance and the brief. A longer answer is refused before the run's first request (configuration) and nothing is sent. Not in the Desk's table; the harness's default is 4000. |
| `toolDescriptionBytes` |  | integer (min 1) | `1024` | The most UTF-8 bytes of a tool's description the model is shown, for a tool with a source (ADR-0006, SOURCE_DEFAULTS), and of each description inside its input schema: a longer one is cut there, and it is framed as retrieved text, never instructions. Not in the Desk's table; the harness's default is 1024. |
| `offeredSchemas` |  | integer (min 0) | `16` | The most tools with a source one run offers with their schemas (ADR-0006): past it, or past offeredSchemaBytes, sources are made catalogues, the largest first, until the rest fit (tool-offer.json#/$defs/catalogueEntry), and load_tool is offered beside them. The Runtime's tools and the host's own are not counted. Not in the Desk's table; the harness's default is 16. |
| `offeredSchemaBytes` |  | integer (min 0) | `24000` | The most UTF-8 bytes of the tools with a source one run offers with their schemas, each counted as the JSON text of its name, its framed description and its schema (ADR-0006); also the most bytes of the schemas load_tool may load in one run. Not in the Desk's table; the harness's default is 24000. |
| `cataloguedTools` |  | integer (min 0) | `64` | The most tools a catalogue holds (ADR-0006): the tools of the sources made catalogues, in the order declared, are offered as catalogue entries up to this many and catalogueBytes bytes in all; the rest are withheld, out of the offer and out of load_tool, and one guardrail event for each source says how many. Not in the Desk's table; the harness's default is 64. |
| `catalogueBytes` |  | integer (min 0) | `24000` | The most UTF-8 bytes the catalogue's entries take in all, each counted as the JSON text of its name, its description and its open schema (ADR-0006); the tools past it, or past cataloguedTools, are withheld. Not in the Desk's table; the harness's default is 24000. |
| `sourceResultBytes` |  | integer (min 1) | `8000` | The largest result of a tool with a source, as JSON text, the model is shown whole (ADR-0006): a larger one is retained in the context ledger and shown as the reference envelope (tool-result.json#/$defs/reference), at the lower of this and toolResultBytes. Not in the Desk's table; the harness's default is 8000. |
| `sourceCallsPerTurn` |  | integer (min 0) | `8` | How many calls of one source's tools one run may make (ADR-0006), counted in the run report (provenance.json#/$defs/sourceFigures). A call past it is not made: the model reads a failed result (tool-failed, source tool, outcome not-started) naming the limit. Not in the Desk's table; the harness's default is 8. |
| `sourceCallMilliseconds` |  | integer (min 1) | `30000` | One call's deadline for a tool with a source (ADR-0006), at the lower of this and toolCallMilliseconds: past it the host's execute is handed the abort and the call is answered as a failed result (timeout, source tool, outcome unknown). Not in the Desk's table; the harness's default is 30000. |
| `skillBytes` |  | integer (min 0) | `8000` | The most UTF-8 bytes one skill may take (ADR-0009): its instructions and each resource's name and text together (skill.json#/$defs/bytes). A declared skill past it is withheld at declaration with a guardrail (skill withheld). Not in the Desk's table; the harness's default is 8000. |
| `skillsPerRun` |  | integer (min 0) | `4` | The most skill loads one response may make (ADR-0009): each load_skill that loads counts, a load after an unload counts again, and the count is carried through a resume in the run report's rows. A load past it is refused (tool-failed, not-started) with a guardrail (skill budget). Not in the Desk's table; the harness's default is 4. |
| `skillsCatalogueBytes` |  | integer (min 0) | `8000` | The most UTF-8 bytes the skills catalogue's entries take in all (ADR-0009), each counted as the JSON text of its name and its line (the frame's constant lines not counted); the skills past it, or past cataloguedSkills, are withheld, one guardrail for each source. Not in the Desk's table; the harness's default is 8000. |
| `cataloguedSkills` |  | integer (min 0) | `32` | The most skills the catalogue lists (ADR-0009), in the order declared; the rest are withheld, out of the catalogue and out of load_skill, one guardrail for each source saying how many. Not in the Desk's table; the harness's default is 32. |
| `cataloguedSubagents` |  | integer (min 0) | `16` |  |
| `subagentCatalogueBytes` |  | integer (min 0) | `4000` |  |
| `memoryProposalBytes` |  | integer (min 0) | `1000` | Most UTF-8 bytes of one remember proposal text (ADR-0014). |
| `memoryProposalsPerRun` |  | integer (min 0, max 256) | `4` | Most remember proposals per run, carried through resume (ADR-0014). |
| `editsPerTurn` |  | integer (min 0, max 4194304) | `24` | Successful edits and undos per response, carried across resume. |
| `editBytes` |  | integer (min 0, max 4194304) | `8000` | UTF-8 bytes of one edit’s new text. |
| `proposalBytes` |  | integer (min 1, max 4194304) | `65536` | UTF-8 bytes of the current draft pack, bounded as the existing test pack. |

Open: a member the schema does not name is accepted. Defaults are the package's `CONTEXT_DEFAULTS`.

## Definitions

### `budget.json $defs/modelBudget`

One model's override (ModelContextBudget). Closed: both readers refuse another member.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `contextWindowTokens` | yes | integer (min 4096, max 2000000) |  |
| `outputTokens` | yes | integer (min 1024) | At least 1024 and at most half the window. |

### `budget.json $defs/configuration`

The assistant profile's context member (ContextConfiguration; aicontext.Configuration). Closed: contextConfiguration refuses another member and the Go decoder disallows unknown fields. projectBrief is a relative project file the host reads for its brief() answer, once per run (ADR-0005; before it, the server read it afresh on every dispatch); models holds at most 128 overrides by model name.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `version` | yes | const `1` |  |
| `projectBrief` |  | string (min length 1, max length 512, pattern `^(?!/)(?!.*//)(?!.*/$)(?!(.*/)?\.\.?(/\|$))[^\\:\r\n\u0000]+$`) | A relative project file: no backslash, colon, CR, LF or NUL, and no empty, . or .. segment. |
| `models` |  | object of `$defs/modelBudget` (max members 128) |  |

### `budget.json $defs/requestAllowance`

What the host tells the model about the request allowance, inside its execution guidance (ExecutionController.guidance): the counts where the host counts model requests, or a note where the provider manages its own loop. In a conversation the delegation coordinator wraps, the same state carries toolCalls beside it ($defs/toolCallAllowance). Written for the model; no reader.

Type: one of: object / object

### `budget.json $defs/toolCallAllowance`

The tool-call cap the execution state states as toolCalls (agent#13), where the delegation coordinator holds one (ExecutionController.toolAllowance): scope response, the MAX_TOOLS (20) host and Runtime tool calls one response may make, the parent's and its delegated children's together; scope task, one child's own budget (delegated-task.json#/$defs/budget). The context tools are not counted. Absent from a run the coordinator does not wrap. Written for the model; no reader.

Open: a member the schema does not name is accepted.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `unit` | yes | const `tool-calls` |  |
| `used` | yes | integer (min 0) |  |
| `limit` | yes | integer (min 1) |  |
| `remaining` | yes | integer (min 0) |  |
| `scope` | yes | enum `response`, `task` |  |
| `note` |  | string |  |

### `budget.json $defs/budgetIncreaseAnswer`

The answer of the host tool request_budget_increase. It never grants: approval-required pauses the run (budget_paused) for the person; within-allowance tells the model to carry on (more than two requests remain); unavailable is the provider-managed case.

Open: a member the schema does not name is accepted.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `status` | yes | enum `approval-required`, `within-allowance`, `unavailable` |  |
| `message` | yes | string |  |
| `remaining` |  | integer |  |

### `budget.json $defs/deskLimits`

The fixed limits, by the Desk's own constant names, at the pinned commit. Values the Desk writes as unnamed literals are stated in these descriptions instead: a checkpoint holds at most 8 grants; one delegation batch has one to three tasks; on an agent capability the delegation coordinator admits at most 5 agent runs in all, shared by the parent and its children, retries every agent run under it, the parent's included, after a busy, not-started refusal, three attempts in all, and runs one child at a time (two on a model capability); the Codex bridge bounds an agent run at ten minutes (internal/codexbridge/run.go), which the server reports as a timeout failure; the browser's agent transport ends a run at twelve minutes, a backstop behind the bridge's deadline, and refuses a frame over 2 MiB, more than 8 MiB received or 64 queued frames, and more than 20 tool calls; the Codex bridge allows two recovery notices (retry events) per run; the model relay bounds one relayed request at ten minutes and two minutes idle (relayDeadline, relayIdle), and has no deadline for a whole run. This harness (agent#13, agent#15) keeps MAX_TOOLS and RESULT_LIMIT, states MAX_TOOLS to the model (toolCallAllowance), keeps a result over the limit whole in the chat's ledger rather than failing the task, and replaces MAX_CHILD_MODELS, MAX_CHILD_TOOLS and CHILD_TIMEOUT with each profile's budget (delegated-task.json#/$defs/profile); the Desk's values stay stated here as the Desk's.

Open: a member the schema does not name is accepted.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `MAX_TURNS` |  | const `20` | The first request allowance of a model-requests execution (web/src/assistant/engines/contract.ts). |
| `REQUEST_INCREMENT` |  | const `10` | Requests one grant adds (execution/state.ts). |
| `MAX_REQUEST_ALLOWANCE` |  | const `100` | The most requests any execution may be granted (execution/state.ts). |
| `MAX_CHECKPOINT_BYTES` |  | const `4194304` | The largest checkpoint, as JSON text (execution/state.ts). |
| `MAX_CRITIC_TURNS` |  | const `4` | The critic pass's model turns (refutation.ts). |
| `MAX_TOOLS` |  | const `20` | Tool calls a delegation shares across the parent and its children (delegation.ts). |
| `MAX_CHILD_MODELS` |  | const `6` | Model requests one child may make (delegation.ts). |
| `MAX_CHILD_TOOLS` |  | const `6` | Tool calls one child may make (delegation.ts). |
| `CHILD_TIMEOUT` |  | const `120000` | One child's time limit, in milliseconds (delegation.ts). |
| `RESULT_LIMIT` |  | const `12000` | A child's result, in characters (delegation.ts). |
| `maxConversationBytes` |  | const `16777216` | The largest saved chat history document, all chats of a project (internal/desk/conversations.go). |
| `MaxPromptBytes` |  | const `200000` | The largest prompt on the agent wire (internal/codexbridge/contract.go); the instructions are held to the same number as a literal. |

### `budget.json $defs/stateLocation`

Where the execution state sits in one request on one wire (agent#9). The state is one text: the header, then the request counters ($defs/requestAllowance, and in a conversation the delegation coordinator wraps the tool-call cap, $defs/toolCallAllowance), plan and operations index the model is told, the host's changing text and the step's own instruction. It is the text at pointer (a JSON pointer into the provider body the adapter composed) from the header, which starts a line, to the end of that text; the header occurs nowhere else in the request. The critic's step note carries the same header. The state is the eighth layer of the request (ADR-0005): after the stable prefix (the package's guidance and the host's parts, $defs/hostPart), the history and the tools. On the agent wire (the Codex engine) the run is one request with one prompt string: the request text, then a chat's index of earlier results, then, after a blank line, the header, the host's changing text and, in a conversation, the execution guidance (agent#36). No host reads a request to find anything in it (ADR-0005 removed the recall rule).

Open: a member the schema does not name is accepted.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `family` | yes | enum `openai-compatible`, `anthropic`, `gemini` |  |
| `thinking` | yes | enum `off`, `on` | The run's thinking tier, as far as it chooses the placement. |
| `capabilities` | yes | `capability.json $defs/endpointCapabilities` | What the host said about the endpoint: members of its capability record (capability.json), of which the placement reads statePlacement and strictAlternation. |
| `placement` | yes | enum `trailing-user`, `system-tail` | The placement the capabilities and the tier resolve to. |
| `request` | yes | integer (min 1) | Which request of the run, counted from 1. |
| `pointer` | yes | string (pattern `^(/[^/]*)+$`) |  |
| `header` | yes | const (see schema) |  |

### `budget.json $defs/hostPart`

One of the host's parts of every request (ADR-0005; packages/harness/prompt.ts, HOST_PARTS): the member it is, the layer it is placed in, the host's injection point that answers it, the first and last lines of its block and the member of this table that bounds it. The adapter asks each injection point once per run, before its first request (memoryFor with the person's message, or a delegated child's objective, and not where the host said memory is off), and places each part it answered in the system text (the agent wire's instructions) after the package's guidance, in layer order, a blank line before each block: the header, the text, the end line. A part answered with nothing is left out; one that is not text, or is over its bound, is refused and the run fails before any request (configuration). The project's instructions are instructions at the lowest rank: they follow the package's guidance, and they cannot grant a tool or change an effect, because the offer and the gate are code. The system text through the last part is the request's stable prefix: the same bytes on every request of the run. ADR-0014: memoryFor answers memory.json#/$defs/answer: accepted records are individually framed as data under the run marker with kind and source inside; the bare-text alias lasts for v0.8.0.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `name` | yes | enum `brief`, `projectInstructions`, `memory` |  |
| `layer` | yes | integer (min 3, max 5) | The part's layer of the request: 3 the brief, 4 the project's instructions, 5 the memory, after 1 the package's guidance and 2 the Runtime's prompt (which reaches the adapter with the person's request as one text and is sent where the request goes, as the first text part of the first user turn, which carries the third Anthropic cache breakpoint). |
| `injection` | yes | enum `brief`, `projectInstructions`, `memoryFor` | The host's injection point (host.ts, HarnessHost; the Go adapter.Run's Host). |
| `header` | yes | string (min length 1) | The block's first line. |
| `end` | yes | string (min length 1) | The block's last line. |
| `bound` | yes | enum `briefBytes`, `projectInstructionsBytes`, `memoryBytes` | The member of this table that bounds the part's UTF-8 bytes. |

## The host’s three parts of every request

In their layers’ order. The header and end line are named by their constants, never given.

| Part | Layer | Injection point | Bounded by | Header constant | End line |
| --- | --- | --- | --- | --- | --- |
| `brief` | 3 | `brief` | `briefBytes` | `BRIEF_HEADER` | its end line |
| `projectInstructions` | 4 | `projectInstructions` | `projectInstructionsBytes` | `PROJECT_INSTRUCTIONS_HEADER` | its end line |
| `memory` | 5 | `memoryFor` | `memoryBytes` | `MEMORY_HEADER` | its end line |

## Other bounds in the package

| Constant | File | Value | Source comment |
| --- | --- | --- | --- |
| `REQUEST_INCREMENT` | `execution/state.ts` | 10 |  |
| `MAX_REQUEST_ALLOWANCE` | `execution/state.ts` | 100 |  |
| `MAX_CHECKPOINT_BYTES` | `execution/state.ts` | 4194304 |  |
| `MAX_PROMPT_DIGESTS` | `execution/state.ts` | 300 | The most prompt digests a checkpoint keeps, the newest: three attempts at each request of the largest allowance. |
| `REPEATED_WORK_LIMIT` | `execution/progress.ts` | 3 |  |
| `MAX_TURNS` | `engines/contract.ts` | 20 | The most model turns one session may take. |
| `MODEL_RETRIES` | `engines/vercel/retry.ts` | 2 | Retries after the first attempt: three attempts in all. |
| `RETRY_BACKOFF_MS` | `engines/vercel/retry.ts` | 1000, 2000 | The wait before retry one and retry two, where the provider asks for less. |
| `RETRY_WAIT_LIMIT_MS` | `engines/vercel/retry.ts` | 30000 | The longest `Retry-After` this engine waits out inside a turn. |
| `INDEX_SHARE` | `context/chat.ts` | 0.25 | The most of the prompt's allowance the index may take (agent#8): a quarter. |
| `IMAGE_BYTES` | `images.ts` | 16777216 | The most one image may hold, decoded: the Desk's bound on an attached document's original (`internal/desk/attachments.go`, 16 MiB). An image the ledger retains also counts against `retainedBytes`, as every item does. |
