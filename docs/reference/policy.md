# The policy table

## Sources

Schemas:

- `contract/schemas/policy.json`

> generated; edit the sources, then run `node scripts/gen-reference.mjs`. `--check` fails where this page differs from them.

See also: [budget.md](budget.md), [critic.md](critic.md), [events.md](events.md), [checkpoint.md](checkpoint.md).

## The policy table

ADR-0011. Closed partial host layers: run > project > table, resolved per member. Models default to the connected author and its tier; critic keeps the author floor. Dynamic roles inherit the author unless explicitly set. Unset members without a table or host-derived default are refused at admission. Model-visible text never supplies a layer.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `budget` |  | object |  |
| `budget.version` |  | const `1` | Default: `1`. |
| `budget.maxInputBytes` |  | integer (min 1) | Fixed transport ceiling on one request's input, whatever the window. Default: `180000`. |
| `budget.contextWindowTokens` |  | integer (min 4096, max 2000000) | The model's context window, in tokens, for the byte estimate (three bytes a token). Default: `65536`. |
| `budget.outputTokens` |  | integer (min 1024) | The output reserve. A request's own max_tokens, max_completion_tokens or maxOutputTokens raises it up to half the window; above half, the request is refused. Default: `8192`. |
| `budget.toolResultBytes` |  | integer (min 1) | The largest tool result, as JSON text, the model is shown whole (tool-result.json). Default: `12000`. |
| `budget.readBytes` |  | integer (min 128) | The largest read_context window. Default: `6000`. |
| `budget.retainedBytes` |  | integer (min 1) | What one execution's context ledger may retain in all. Default: `8388608`. |
| `budget.recentMessages` |  | integer (min 0) | Declared in the table; read by no assistant code today. Default: `8`. |
| `budget.historyBytes` |  | integer (min 0) | Declared in the table; read by the server's research worker, not by the assistant loop. Default: `32000`. |
| `budget.briefBytes` |  | integer (min 0) | The most UTF-8 bytes of the project brief the host's brief() may answer (ADR-0005; before it, the most the server added behind the adapter). A longer answer is refused before the run's first request (configuration) and nothing is sent. Default: `8000`. |
| `budget.memoryBytes` |  | integer (min 0) | The most UTF-8 bytes of recalled project memory the host's memoryFor(query) may answer (ADR-0005; before it, the most the server added behind the adapter); a longer answer is refused as a brief's is. Nothing is reserved for it in a run's input allowance: a recalled memory is counted once, where it sits in the instructions. A run whose host says memory is off (context.memory: false, agent#11) asks memoryFor nothing; the policy is the same either way (contextPolicy in packages/harness/context/policy.ts). Default: `4000`. |
| `budget.turnMilliseconds` |  | integer (min 1) | One turn's wall clock, from the run's start, on the vercel engine: when it passes, what is in flight is stopped and the run pauses as budget_paused does, with its checkpoint and a reason; a proposal already made is delivered, with a note, and the run ends. Continuing the pause takes a request grant, so at the 100-request cap it cannot be continued; the reason says so. The codex engine has none of its own (the Codex bridge bounds a run at ten minutes). Not in the Desk's table; the harness's default is 900000 (fifteen minutes). Default: `900000`. |
| `budget.toolCallMilliseconds` |  | integer (min 1) | One runtime or host tool call's deadline, on both engines: a call past it is no longer waited for (it may still complete; a host tool is also handed the deadline's abort signal) and is answered to the model as a failed result (failure code timeout, source tool, outcome unknown), and the run goes on. propose_pack, which the adapter reads itself, and a tool that pauses the run for the person or the coordinator have none. Not in the Desk's table; the harness's default is 120000 (two minutes). Default: `120000`. |
| `budget.imagesPerRequest` |  | integer (min 0) | The most images one model request carries, on the vercel engine (agent#5): the newest; an older image past it is replaced in that request by a line saying it is no longer shown, and the result that brought it still describes it. The codex engine's agent wire carries text and shows none. Not in the Desk's table; the harness's default is 8. Default: `8`. |
| `budget.imageBytesPerRequest` |  | integer (min 0) | The most bytes, decoded, of the images one model request carries, counted from the newest with imagesPerRequest; an older image that would pass it is replaced by the same line. Not in the Desk's table; the harness's default is 16777216 (16 MiB, one image at its own bound). Default: `16777216`. |
| `budget.testCallsPerTurn` |  | integer (min 0) | How many experimental_test_cases calls one turn may make, on both engines (context/session.ts, claimTestCall). Each call re-sends the whole pack, so a test, fix and retest loop is held to a few rounds rather than to the request allowance. A call past it is not made: the model reads a failed result (failure code tool-failed, source tool, outcome not-started) naming the limit, and the run goes on. Counted per turn: the count is the checkpoint's testCalls (checkpoint.json), carried as used is through a Retry, a budget continuation and a delegation's parent continuation, and the delegation coordinator counts the parent and its continuation together; a new turn starts at nought. Not in the Desk's table; the harness's default is 3. Default: `3`. |
| `budget.testPackBytes` |  | integer (min 1) | The most UTF-8 bytes experimental_test_cases' pack text may be; the tool gate refuses a larger call on the wire (tool-call.json $defs/testCasesArguments). Not in the Desk's table; the harness's default is 65536. Default: `65536`. |
| `budget.testMatrixBytes` |  | integer (min 1) | The most UTF-8 bytes experimental_test_cases' matrix text may be; the tool gate refuses a larger call on the wire. Not in the Desk's table; the harness's default is 65536. Default: `65536`. |
| `budget.turnIndexEntries` |  | integer (min 0) | How many earlier results the index a chat-scoped run's first message carries lists, the newest; older ones are counted in its omitted member (tool-result.json#/$defs/turnIndex, agent#8). Not in the Desk's table; the harness's default is 20. Default: `20`. |
| `budget.turnIndexBytes` |  | integer (min 0) | The most UTF-8 bytes that index may be, its line and data block together (agent#8). It is also cut to the room the run's first message leaves in the prompt's allowance, and to at most a quarter of that allowance (INDEX_SHARE): rows go, oldest first, until it fits, an index with no room is not sent, and the request is never previewed and a run never fails because of it. Not in the Desk's table; the harness's default is 8000. Default: `8000`. |
| `budget.projectInstructionsBytes` |  | integer (min 0) | The most UTF-8 bytes of the project's own instructions the host's projectInstructions() may answer (ADR-0005): instructions at the lowest rank, placed after the package's guidance and the brief. A longer answer is refused before the run's first request (configuration) and nothing is sent. Not in the Desk's table; the harness's default is 4000. Default: `4000`. |
| `budget.toolDescriptionBytes` |  | integer (min 1) | The most UTF-8 bytes of a tool's description the model is shown, for a tool with a source (ADR-0006, SOURCE_DEFAULTS), and of each description inside its input schema: a longer one is cut there, and it is framed as retrieved text, never instructions. Not in the Desk's table; the harness's default is 1024. Default: `1024`. |
| `budget.offeredSchemas` |  | integer (min 0) | The most tools with a source one run offers with their schemas (ADR-0006): past it, or past offeredSchemaBytes, sources are made catalogues, the largest first, until the rest fit (tool-offer.json#/$defs/catalogueEntry), and load_tool is offered beside them. The Runtime's tools and the host's own are not counted. Not in the Desk's table; the harness's default is 16. Default: `16`. |
| `budget.offeredSchemaBytes` |  | integer (min 0) | The most UTF-8 bytes of the tools with a source one run offers with their schemas, each counted as the JSON text of its name, its framed description and its schema (ADR-0006); also the most bytes of the schemas load_tool may load in one run. Not in the Desk's table; the harness's default is 24000. Default: `24000`. |
| `budget.cataloguedTools` |  | integer (min 0) | The most tools a catalogue holds (ADR-0006): the tools of the sources made catalogues, in the order declared, are offered as catalogue entries up to this many and catalogueBytes bytes in all; the rest are withheld, out of the offer and out of load_tool, and one guardrail event for each source says how many. Not in the Desk's table; the harness's default is 64. Default: `64`. |
| `budget.catalogueBytes` |  | integer (min 0) | The most UTF-8 bytes the catalogue's entries take in all, each counted as the JSON text of its name, its description and its open schema (ADR-0006); the tools past it, or past cataloguedTools, are withheld. Not in the Desk's table; the harness's default is 24000. Default: `24000`. |
| `budget.sourceResultBytes` |  | integer (min 1) | The largest result of a tool with a source, as JSON text, the model is shown whole (ADR-0006): a larger one is retained in the context ledger and shown as the reference envelope (tool-result.json#/$defs/reference), at the lower of this and toolResultBytes. Not in the Desk's table; the harness's default is 8000. Default: `8000`. |
| `budget.sourceCallsPerTurn` |  | integer (min 0) | How many calls of one source's tools one run may make (ADR-0006), counted in the run report (provenance.json#/$defs/sourceFigures). A call past it is not made: the model reads a failed result (tool-failed, source tool, outcome not-started) naming the limit. Not in the Desk's table; the harness's default is 8. Default: `8`. |
| `budget.sourceCallMilliseconds` |  | integer (min 1) | One call's deadline for a tool with a source (ADR-0006), at the lower of this and toolCallMilliseconds: past it the host's execute is handed the abort and the call is answered as a failed result (timeout, source tool, outcome unknown). Not in the Desk's table; the harness's default is 30000. Default: `30000`. |
| `budget.skillBytes` |  | integer (min 0) | The most UTF-8 bytes one skill may take (ADR-0009): its instructions and each resource's name and text together (skill.json#/$defs/bytes). A declared skill past it is withheld at declaration with a guardrail (skill withheld). Not in the Desk's table; the harness's default is 8000. Default: `8000`. |
| `budget.skillsPerRun` |  | integer (min 0) | The most skill loads one response may make (ADR-0009): each load_skill that loads counts, a load after an unload counts again, and the count is carried through a resume in the run report's rows. A load past it is refused (tool-failed, not-started) with a guardrail (skill budget). Not in the Desk's table; the harness's default is 4. Default: `4`. |
| `budget.skillsCatalogueBytes` |  | integer (min 0) | The most UTF-8 bytes the skills catalogue's entries take in all (ADR-0009), each counted as the JSON text of its name and its line (the frame's constant lines not counted); the skills past it, or past cataloguedSkills, are withheld, one guardrail for each source. Not in the Desk's table; the harness's default is 8000. Default: `8000`. |
| `budget.cataloguedSkills` |  | integer (min 0) | The most skills the catalogue lists (ADR-0009), in the order declared; the rest are withheld, out of the catalogue and out of load_skill, one guardrail for each source saying how many. Not in the Desk's table; the harness's default is 32. Default: `32`. |
| `critic` |  | object |  |
| `critic.default` |  | enum `off`, `auto`, `on` | Default: `"auto"`. |
| `critic.reasoningFloor` |  | const `author` | Default: `"author"`. |
| `models` |  | object |  |
| `models.author` |  | `$defs/role` |  |
| `models.critic` |  | `$defs/role` |  |
| `models.taskClasses` |  | object |  |
| `models.taskClasses.routine` |  | enum `off`, `on`, `ultra`, `none`, `minimal`, `low`, `medium`, `high`, `xhigh`, `max` |  |
| `models.taskClasses.material` |  | enum `off`, `on`, `ultra`, `none`, `minimal`, `low`, `medium`, `high`, `xhigh`, `max` |  |
| `models.taskClasses.boundary` |  | enum `off`, `on`, `ultra`, `none`, `minimal`, `low`, `medium`, `high`, `xhigh`, `max` |  |
| `concurrency` |  | object |  |
| `concurrency.children` |  | integer (min 1, max 16) | Fixed at one on the agent capability. Default: `1`. |
| `concurrency.requests` |  | integer (min 1, max 16) | Cannot exceed the host connection capacity. Default: `1`. |
| `pace` |  | object |  |
| `pace.requestsPerDay` |  | integer or null (min 0) | Host project period allowance; null means no period ceiling. Default: `null`. |
| `pace.invocationsPerDay` |  | integer or null (min 0) | Host project period allowance; null means no period ceiling. Default: `null`. |
| `pace.bytesPerDay` |  | integer or null (min 0) | Host project period allowance; null means no period ceiling. Default: `null`. |
| `pace.warnAt` |  | number (min 0, max 1) | Default: `0.8`. |
| `taskClass` |  | enum `routine`, `material`, `boundary` | Default: `"routine"`. |

### `policy.json $defs/member`

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `value` | yes | any |  |
| `layer` | yes | enum `run`, `project`, `table` |  |

### `policy.json $defs/paceUsed`

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `requestsPerDay` |  | integer (min 0) |  |
| `invocationsPerDay` |  | integer (min 0) |  |
| `bytesPerDay` |  | integer (min 0) |  |

### `policy.json $defs/resolved`

Open: a member the schema does not name is accepted.

Type: object of `$defs/member`

### `policy.json $defs/role`

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `model` |  | string (min length 1, max length 128) |  |
| `reasoning` |  | enum `off`, `on`, `ultra`, `none`, `minimal`, `low`, `medium`, `high`, `xhigh`, `max` |  |
| `provider` |  | string (min length 1, max length 128) | same selects the author's connection; other selects a host connection of another family; otherwise a connected ID. Run > project > table. Default: `"same"`. |
