# The wire contract

Status: first statement, 2026-10-18. A descriptive extraction of the contract the Desk's engine slot
keeps today, read from the Desk's `integration/v0.13` line at `d9251ec`. Nothing here is a redesign:
where the Desk departs from a clean contract, the departure is listed under
[Open questions](#open-questions) and left for the decisions that follow. The schemas and vectors
are in [`contract/`](../../contract/README.md); this page says the same thing in words.

Amended for agent#6 and the harness side of agent#2: nothing a model finished is lost (the whole
answer kept, the proposal's `note`, the `propose_pack` tool), the model-request retry rule, the
turn's wall clock and the tool-call deadline. Amended again for agent#7 (the Runtime's test tool on
the ceiling, under an argument allow-list and a per-turn cap), agent#3 (the failure paths, recorded
in [AI error handling](ai-error-handling.md)) and the harness side of agent#12 (retrieved text
framed as data). These are the harness package's behaviour, not yet the Desk's; their vectors name
the package's files as their source.

## Host, adapter, and the slot between them

In the Desk, an adapter is an **engine**: a module the page loads for one session, which receives a
session and returns an ordered stream of events (`Engine.start(session)`,
`web/src/assistant/engine.ts`). Two engines ship: `vercel`, a loop on the Vercel AI SDK that drives a
model over the relay, and `codex`, which drives a ChatGPT subscription agent that the Desk server
runs. Everything an engine needs is decided by the host before the session starts and handed to it:

- the runtime's prompt (`prompts/get`) and, for the critic, its testing prompt;
- the runtime's tools exactly as `tools/list` served them, and `callTool`, the only way to reach
  them, bound through the host's tool gate;
- the host's own tools (`hostTools`), executed by the host on the engine's behalf;
- a model capability (`model.call(suffix, request)`: a path suffix and a body, no address, no
  credential) or, for `codex`, an agent capability (`agent.run`);
- the reasoning tier, normalized by the host's table; an abort signal; and the session's options
  (purpose, reply language, conversation, interactive presentation, critic, context inputs).

Both engines run inside one shared boundary (`context/managed.ts`, `managedContext`) that owns the
execution's context ledger (or its turn on a chat's, [The chat-scoped context](#the-chat-scoped-context)),
its checkpoint and its request accounting, and a delegation coordinator
(`delegation.ts`, `withDelegation`) wraps either engine for a conversation. Some events on the
stream are therefore the host's, interleaved with the loop's: the gate's guardrails, the
checkpoints and the delegated tasks. The contract below is the stream that leaves the slot, with
those included, because that is what the Desk's consumer reads.

The keyless relay and its custody of keys, the tool gate and its approvals, and the Runtime tools'
rehearsal rule stay the host's (ADR-0001). Nothing below moves them.

## Run events

`run-event.json`. Eighteen kinds, discriminated by `type`, from the adapter to the host, in order:

- **Work:** `tool_call` and `tool_result` (each call before its result, paired by `callId` where
  one is set); `reasoning` (deltas, then the whole passage with `done`); `message_progress` (the
  prose so far, whole each time, proposal fence hidden; empty clears it) and `message` (the
  model's prose beside the proposal, as written; where the proposal could not be read, the whole
  answer, prose and raw fence, before the error).
- **Pauses for someone else:** `user_question` (the model asked the person; the turn ends),
  `delegation_requested` (the model asked for a batch of child tasks; the coordinator runs them),
  `budget_paused` (the request allowance ran out, the model asked for more or, on `vercel`, the
  turn's wall clock passed; the person decides).
- **Host reports:** `guardrail` (`rewrote` and `refused` from the tool gate, between the call and
  its result; `narrowed` before the model is asked anything, where a family's wire cannot carry a
  served schema keyword; `refused` with tool `proposal` where Plan mode or a conversation withholds
  proposals), `execution_checkpoint` (state to save, `durable` where it must be saved before the
  next operation), `delegated_task` (a child's state, whole, at each transition).
- **Provider recovery:** `retry` (attempt one or two: on `codex` relayed from the subscription
  agent, on `vercel` before a refused model request is sent again), `response_recovery` (one
  tool-free request to finish a response cut short, or to ask once for a proposal that could not be
  read),
  `thinking_unavailable` (the tier could not be expressed here, said once).
- **Outcome:** `critique` (the critic's checks, decided from the runtime's results and never its
  prose, before the proposal), `proposal` (one document out of exactly one fenced JSON envelope or
  the model's `propose_pack` call, with its unknowns, the critique's verdict where the critic
  reached a check, and a `note` where the critic started and did not finish), `error` (the
  catalogue's sentence and the classified failure), `end` (exactly once, last).

A consumer that leaves gets no `end`; nothing is delivered after the run closes. A cancelled run
emits no `error`. A failed tool is a `tool_result` with `isError`, not an `error`. The proposal is
the adapter's last word on the document, and accepting or rejecting it is the host's action.

Nothing the model finished is lost. A proposal that cannot be read (two marked blocks; none,
outside a conversation; a block not marked `json`, one not closed, or one that is not valid JSON,
the last classed `invalid-response` like the rest) earns `vercel` one tool-free reformat request
within the allowance; whatever that request returns, the first answer is delivered whole, prose and raw
fence, before the `invalid-response` error. In a conversation the reformat may come back as prose
with no proposal: the first answer is still delivered whole, that prose after it, and then the first
answer's failure. A critic that fails after a valid proposal (its provider error, its length cutoff,
the allowance or the turn's wall clock) does not raise an error ahead of the proposal: the proposal
is delivered with `note` `{kind: "critic-unfinished", message, failure}` and no `critique`. A
proposal made through `propose_pack` is kept the same way: where a later request of the turn fails
(the closing summary refused or cut off, the allowance, the turn's wall clock, or on `codex` the
run's own failure), the proposal is delivered with `note` `{kind: "turn-unfinished", …}`, nothing
after it runs, the critic included, and the run ends. A note's `failure` carries the engine's
source (`model` or `agent`), and for the allowance its sentence says the allowance was used up,
not that work paused. A person's Stop still ends the run.

The stream is open: it is an in-process union with no reader at run time that refuses a member.

## Tool offers

`tool-offer.json`, the session's two members, `tools` and `hostTools`. The runtime's tools arrive
as served (`name`, `description`, `inputSchema`), already filtered to the session's allow-list,
which is a subset of a six-tool ceiling, so each name is one of the six: the Desk's five
(`get_schema`, `list_examples`, `get_example`, `validate`, `experimental_evaluate`) and
`experimental_test_cases`, which takes pack text and a matrix and reads no project file, so a model
can test, fix and retest a draft it has not saved. The Runtime's other test and project tools stay
off it, each for a reason the gate's documentation keeps (`REFUSED_RUNTIME_TOOLS` in `toolGate.ts`):
`test_conformance` takes `suite`, a local path the runtime reads on the machine that serves it, and
the gate checks names, not paths; `experimental_test_packs` and `experimental_test_graphs` test the
saved tree rather than the draft, and their saved suites are not held by the rule that holds a
rehearsal; `get_pack` and `list_packs` read the saved tree. A host's own configuration may grant
fewer (the Desk's decoder admits its five today). The host's
tools arrive as declarations (`name`, `description`, `inputSchema`) with host-declared members the
model never supplies: `effect` (`read`, `proposal`, `workflow`, `memory`, `plan`), `interaction`
(`question` or `delegation`, the two that pause the run), `delegation` (the read-only capability a
child may use), `contextPolicy` (`immutable`, `reference`, and, since agent#8, `runtime`: the
result depends on the Runtime serving it) and `presentation`. The boundary adds
`list_context`, `read_context`, `read_window` and `search` to every session and, in a conversation,
`update_plan` and `request_budget_increase`; an offered tool may not take those names.
(`read_window`, the reader below, is this harness's, agent#1, and so is `search`, agent#5; the Desk
takes both in its swap.)

An adapter shows the model the schema the runtime serves or nothing: a tool served without one ends
the session. The one exception is an endpoint whose capability record says it takes the Gemini
subset (`toolSchemas`, by default the Gemini family), shown the schema less a closed list of
keywords, with a `narrowed` guardrail per tool that lost one on the Gemini wire.

An endpoint whose capability record says it takes no native tool calls (`nativeToolCalls` false;
see [The capability record](#the-capability-record)) is offered the same tools in text: each one's
name, description and shown schema at the end of the system text, and no tool on the wire. The
model calls one per answer as one fenced JSON block, `{"tool": name, "arguments": {…}}`, read by
the proposal fence's own closed reader: one block whose root is exactly `tool` and `arguments`, or
the call is not made and the model is told why (`tool-call.json#/$defs/textToolCall`; the vector
`tool-call/text-call.json`). On the agent wire the `codex` engine
offers each tool under an alias (`desk_runtime_N`, `desk_host_N`) and reports the real name in its
events.

A host may offer `propose_pack`, with the input schema the contract states (`document`, an object;
`unknowns`, strings) and effect `proposal`. Where it does, the instructions tell the model to deliver
the proposal by calling it; the adapter reads the call itself, answers the model that the proposal
was received and will be shown for review, and never reaches the host's `execute`; arguments of
another shape are a failed result naming what to correct, under the fence's rules (`kind`, where
given, must be `create`). The latest call's document is the proposal, and the fenced block stays the
fallback; a fenced proposal in the same answer as a tool proposal is ambiguous and refused, as two
fenced proposals are. The critic is not offered it. For `propose_pack` and the host's other
`propose_*` tools the boundary is the review card, not the gate: an offered proposal tool is one
the model may call, retrieved text that asks for it included, and what holds is that a proposal is
shown for the person's review and nothing is written or applied until the person acts. The gate
holds the tools that are not offered.

## Tool calls

`tool-call.json`. A `tool_call` carries the tool's own `name`, the model's own `args` (before the
gate rewrites anything) and, where set, a `callId`. It is emitted before the call is made, so the
host can journal it. A host tool with an `interaction` gets `user_question` or
`delegation_requested` instead.

Every runtime call leaves through the host's gate (`toolGate.ts`), which reads the outbound
`tools/call` frame: a name outside the allow-list is refused on the wire (`guardrail` `refused`,
and the call fails); `experimental_test_cases` leaves only with exactly `pack` and `matrix`, each a
string within its bound, the policy's `testPackBytes` and `testMatrixBytes`
(`$defs/testCasesArguments`; the Runtime refuses text that is not JSON),
and any other member, a path among them, is refused the same way, the notice naming members and
sizes and never their text; `experimental_evaluate` always leaves with an own `rehearsal: true`
(`guardrail` `rewrote` where the model's call lacked it), so a rehearsal never writes an audit
record. The adapter neither applies nor skips these rules.

One turn may call `experimental_test_cases` at most `testCallsPerTurn` times (three by default),
counted per turn. The count is the shared boundary's (`context/session.ts`), so both engines keep
it, and it is the checkpoint's `testCalls`, carried as `used` is: a Retry, a budget continuation and
a delegation's parent continuation go on counting, and the delegation coordinator counts the parent
and its continuation together. A call past it is not made, and the model reads a failed result
(`tool-failed`, source `tool`, outcome `not-started`) that names `testCallsPerTurn` and tells it to
use the reports it has. Each call re-sends the whole pack, so without the cap a test, fix and
retest loop would be bounded only by the request allowance, at a cost that grows with the square of
the steps; the repeated-work detector compares arguments and does not see a pack that changes each
time. A new turn starts at nought.

A call made in text goes through the same dispatch, and so the same gate, as a native one, and
emits the same `tool_call`, `guardrail` and `tool_result` events. A name the phase did not offer
(the critic is offered fewer host tools than the loop) is put through the gate as the model wrote
it and reported refused to the model whatever the gate answered, never looked up among the host's
tools. The result goes back as the next user message, headed `TOOL RESULT from <name>` or `TOOL
ERROR from <name>`, then what a native call's result would show: a reading tool's answer as its
data block (see Tool results), any other the bounded answer as JSON
(`tool-result.json#/$defs/textToolResultMessage`; the vector `tool-result/text-result.json`). On the request reserved for a final answer a call made in
text is refused (`reserved for a final answer`) and never run, as a native one is.

## Tool results

`tool-result.json`. A `tool_result` carries `name`, `isError`, `text` (the answer's text blocks
joined) and `structured` (its `structuredContent`), and the `callId` of its call where one is set.
The event is not bounded: the host sees the whole answer.

What the model is shown is bounded. A result whose JSON text is at most `toolResultBytes` (12,000
by default) reaches the model whole; a larger one is retained in the execution's context ledger and
the model is shown a reference envelope: the item's `contextId`, `kind`, `label`, `bytes`,
`sha256`, up to thirty `sections` (JSON pointers with their types), `readWith: "read_window"`,
whether it is `historical`, and a 1,000-byte `preview`. The model reads it back with the reader
the envelope names, `read_window` (below), or in sections with `read_context` (`id`, optional
`sha256`, `path`, literal `query`, `offset`, `limit` up to `readBytes`, 6,000 by default). Where the
ledger itself is full (`retainedBytes`, 8 MiB), the model
is told the operation returned and its output could not be retained, and not to repeat it.

A reading tool keeps under that bound by construction (agent#1): its window is `readWindowBytes`,
the result limit less a 1,024-byte envelope, counted in the window's JSON-encoded text, so one read
is one result and the reference path is left for results that still exceed it. The boundary's own
reader, `read_window`, reads a retained item in such windows by line, or a paged document (an item
whose `pages` are strings) by page: arguments `id`, `unit`, `offset` (from 0) and `limit`, or the
`cursor` a previous read returned; a result with the `text` (lines separated by a newline, pages by
a form feed), the units shown, the item's `total` and `more`, the cursor that reads on, or null. A
host's own readers keep the same shape. Since agent#5 a read by line numbers its lines,
`<number><tab><line>` counted from 1 (line n is at offset n − 1), and shows 200 lines where the
model names no limit; an item whose value is an image is read whole, as the image, a `reference`
followed to the image the execution retains (`$defs/imageReadResult`). An item meant as an image that
cannot be shown (a GIF, bytes that are not base64, a member an image part does not carry, a
reference to nothing retained) is answered with the line a tool result gives in its place, `[image 1
(…): not shown, because …]`, never an empty window.

`search` (`$defs/searchArguments`, `$defs/searchResult`; agent#5) finds a regular expression in
retained items, line by line: one item by `id`, every item (`scope` `all`, the default) or the
documents the host handed the run (`attachments`, `$defs/attachment`). The pattern is the subset
RE2 accepts (no backreferences, lookaround, Unicode property classes or inline flags; at most 256
characters; compiling bounded at 2,000 steps, every node built and every repetition counted, so a
nested repetition of an empty group is refused at once), run in time linear in each line with a
bound on one search's work, each line charged the program's size before it is read. Two readings
differ from ECMAScript's `RegExp` with `u`: `$` also matches just before a line's final carriage
return (a line of a CRLF text ends in one), and with `ignoreCase` a letter matches every letter its
case mappings reach, as `iu` does (`σ`, `ς` and `Σ`; `k`, `K` and the Kelvin sign; `s`, `S` and
`ſ`), literals and classes alike. Each match gives
the item's `id` and `label`, its `line` (counted from 1), `offset` (the read offset), `column`, the
`page` of a paged item, and `text`, the line with two either side, numbered. As many matches as fit
in `readWindowBytes`, at most `limit` (20 by default, 100 at most), and `more`, a cursor bound to
the same pattern and scope; a search that reaches its work bound stops with a cursor and a `note`.
An image item is not searched as text; one in scope that cannot be shown is named on the first page
in `notShown`, with the same line, so none is passed over in silence.

A result's `content` may carry image parts (`$defs/imagePart`: `kind` `image`, a PNG, JPEG or
WebP `mediaType`, and either `bytes`, base64 up to 16 MiB decoded, or a `reference` to an image the
execution retains), and so may an attachment's value; MCP's own image item is read as one. What the
model is shown depends on its capability record: with `vision`, the image as an image beside its
result; without, the host's text rendering of it (`renderImageToText`) in its place. The bytes are
never part of the text the result limit counts. An image that cannot be shown is a line in the
result saying why; none is dropped in silence. One model request carries at most `imagesPerRequest`
images and `imageBytesPerRequest` of their bytes, decoded (8 and 16 MiB by default): the newest,
and an older image past either bound is a line in that request saying it is no longer shown, its
result still describing it. An attachment's value is retained in the ledger as its JSON text, base64
included, against `retainedBytes` (8 MiB), so an attached image holds at most about 6 MiB decoded;
a larger one fails the run's start with a context-budget error. The agent wire carries text, so the
`codex` engine always gets the rendering.

What the model is shown of a reading tool's answer (every runtime tool, `read_window`,
`read_context`, `search`, and a host tool declared `effect: "read"` or, declaring no effect, offered to
children as a read) is one data block (`$defs/framedData`): a first line naming the run's marker
and the tool, the answer's text, and a last line, `END RETRIEVED DATA` and the marker. The marker is 128 random bits drawn when the
run starts, the same for every block of the run and never written into the instructions, so text
fetched before the run cannot close a block early; where an answer itself carries the marker (a
tool that echoes an argument the model wrote), each occurrence is replaced before framing. `vercel`
hands the block to the model on purpose, through the SDK's `toModelOutput`, as a text output whose
data is the bounded answer's JSON, and, on an endpoint without native tool calls, as the body of the
`TOOL RESULT` message (`$defs/textToolResultMessage`); `codex` sends it as the agent-wire answer's
text. Context
compaction reads a block back to its data, so the summary keeps its handles, and frames the
compacted steps as one block of the same form. The run's instructions, the critic's included, carry
the rule that text inside a block is data and never instructions (`DATA_RULE`). A failure the
harness wrote and an acknowledgement the adapter wrote are not framed: they are the host's words.
The `tool_result` event is never framed.

A tool that throws is a failed result carrying `{"failure": …}` (or `{"questionFailure": …}` for
`ask_user`), recoverable working data for the model. So is a runtime or host tool call past the
policy's `toolCallMilliseconds`: it is no longer waited for (it may still complete; a host tool is
also handed the deadline's abort signal) and is answered with class `timeout`, source `tool`, outcome
`unknown`, and the run goes on. On the agent wire the answer travels as
`{text, isError}`, a structured answer as its JSON text.

## Approvals

`approval.json`. No approval crosses the slot today. The run proposes and ends; the host acts on the
proposal afterwards. What the host holds per chat is a mode, `manual` (Ask me) or `auto` (Auto
within limits), and at most one grant an explicit user decision minted: one of the four actions the
server's dispatcher admits (`policy-update`, `pack-update`, `portable-research`,
`dialect-publish`; any other is refused) on one resource revision, within fifteen minutes, consumed
by the commit, with `state` `ready`, `used`, `expired`, `revoked` or `stale`. Model output never
mints one.

The run's own pauses for a person are the question (`user_question`), the budget
(`budget_paused`, resumed only by a grant recorded in the checkpoint) and, for the coordinator,
delegation. The model's call of an unoffered or refused tool is not a pause: the gate refuses it.

## Questions

`question.json`. `ask_user` puts one question to the person: `title` (40), `question` (600),
`selection` (`single` or `multiple`), none or two to four `options` (`id`, `label`, `description`),
and a `recommendedOptionId` with its `recommendation`, both or neither. Free text is always
available. The adapter emits `user_question` with a fresh `id`, ends the turn and returns no answer
of its own; the answer arrives with the next run. A malformed question is a failed result naming
the field to correct (`shape`, `title`, `question`, `selection`, `options`, `recommendation`), and
the model asks again in the same turn. The reader is closed: no member it does not name.

## Delegated tasks

`delegated-task.json`. The model asks for one batch of one to three tasks (`title`, `objective`,
the only `context` the child gets, and an offered `profile`: `analyze`, `review` or `research`).
The adapter emits `delegation_requested` and pauses. The host's coordinator runs each child on the
same engine with a fresh session, the profile's instructions and only the host tools the profile's
capabilities admit, and publishes each transition as `delegated_task`: `state` (`queued`,
`running`, `complete`, `failed`, `interrupted`), its `steps`, its counts (`modelRequests`,
`toolCalls`, `attempts`), and its `result`, `problem` or `failure`. Then it continues the parent
with the findings as retained, unverified working material. Children cannot ask, propose, approve
or delegate again.

The shared limits are the coordinator's: twenty tool calls across parent and children, six model
requests and six tool calls per child, one request held back for the parent, two minutes and
12,000 characters of result per child. On an agent capability the coordinator admits at most five
agent runs in all, shared by the parent and its children, and retries every agent run under it,
the parent's included, after a `busy`, `not-started` refusal and nothing else, three attempts in
all.

## Checkpoints

`checkpoint.json`. The execution's record, carried whole on `execution_checkpoint` and
`budget_paused` and handed back to resume: `requestKey` (binding it to the request it was made
for), `phase` (`plan` or `work`), `status`, the allowance (`unit`, `used`, `limit`, and `grants`
`{from, to, at}`), the task `plan` and its `explanation`, every `operation` with its exact arguments
and result, the `completions`, the ledger's retained `context` (each entry with its digest, so a
restore refuses a changed one; for a run handed a chat-scoped context, the entries that run retained
or read), and, once the turn has called the test tool, `testCalls`, carried
as `used` is. Never permissions, evidence approval or private reasoning. At most 4 MiB as JSON
text.

## Failures

`failure.json`. One catalogue (`contract/desk/catalog.json`, 23 classes, each with the sentence a
person sees and a recovery), and one failure shape: `version` 1, `code`, `source` (`model`,
`agent`, `tool`, `context`, `storage`, `desk`), and `outcome`: `not-started` (admission refused it,
nothing ran), `refused` (a 4xx other than 408), or `unknown` (everything else, every 5xx and
timeout included). Optional `status`, `requestId`, `retryAt` (from the provider's headers),
`detail` (host-authored only, replacing the catalogue sentence) and `completion`. Provider prose,
bodies, URLs and headers are never retained. Only `busy` with `not-started` proves an agent run safe
to retry. A model request the provider refused is sent again by `vercel` only where the outcome is
`refused` and the refusal is one `failures.ts` names (the `rate-limited` class: a 429 or a
rate-limit alias; and a 4xx carrying the overload code `overloaded_error`): at most twice, each after the longer of the backoff (one second, then
two) and the provider's `Retry-After`; not at all where `Retry-After` asks for more than thirty
seconds, when the refusal is reported at once with its `retryAt`; never on an `unknown` outcome (a
5xx, 408, 504 or a transport failure); and never once a response has answered, so a stream that
breaks after its first byte is reported; and never with the allowance's last request, which is the
turn's tool-free final answer (tools close whenever one request is left, counted afresh on every
step). Each attempt is a model request, counted against the allowance and recorded, each retry is
said with a `retry` event, and the attempt number starts again at 1 on every new call. What each
engine reports on a dropped connection, a stall, a context-length refusal, a runtime socket drop and
a whole answer where a stream was asked for, and what Retry does after each, is in
[AI error handling](ai-error-handling.md).

## Budgets

`budget.json`. One table both the browser and the server read: the input ceiling
(`maxInputBytes`, and the window and output reserve it is computed from, overridable per model),
`toolResultBytes`, `readBytes`, `retainedBytes`, and the bytes reserved for what the server adds
behind the adapter (`briefBytes`, `memoryBytes`). Since agent#11 `memoryBytes` is reserved only while
memory may be added: the host says per run whether the project's memory is on (`context.memory`),
`false` frees the reserve and the run has the full input allowance, and a run whose host says
nothing keeps it, so a server that adds memory never receives a request the harness left no room
for. The request allowance is counted by the host in
model requests: twenty to start (`MAX_TURNS`, also the turn bound), ten more per grant the person
makes, never above a hundred. The model is told the counts in its execution guidance and may ask
for more with `request_budget_increase`, which never grants: with two or fewer requests left it
pauses the run for the person.

The harness adds two time bounds the Desk's table does not carry, each taken from the table where it
does. `turnMilliseconds` (fifteen minutes): on `vercel`, when one turn's wall clock passes, what is
in flight is stopped and the run pauses as `budget_paused` does, the checkpoint's `reason` saying
so; a proposal already made is delivered, with a note, and the run ends. Continuing a clock pause
takes a request grant, as any pause does, so a run already at the hundred-request cap cannot be
continued and needs a new response; the reason says so. Continuing without a grant stays open under
agent#2. `toolCallMilliseconds` (two minutes): on both engines, a runtime or host tool call past it
is no longer waited for and is a failed result the model reads, never a failed run. It adds two
image bounds the same way (agent#5): `imagesPerRequest` (8) and `imageBytesPerRequest` (16 MiB), the
images one model request carries on `vercel`, newest first. And two bounds on the index of earlier
results a chat-scoped run is shown (agent#8): `turnIndexEntries` (20) and `turnIndexBytes` (8,000).

## The capability record

`capability.json`; the vectors `capability/`. Since agent#4 the `vercel` adapter reads one record per
endpoint and model wherever it decided by the three families, which stay only the wire (the provider
that speaks the request, where signatures ride, where a cache breakpoint goes):

- `contextWindowTokens` and `outputTokens`: with both present, the run's window and output reserve,
  in place of the budget table's; an assistant profile's own override for the model stays the last
  word.
- `nativeToolCalls`: false offers the tools in text (see Tool offers and Tool calls).
- `parallelToolCalls`: false asks for one call per turn on a request that offers tools, where the
  wire has a member for it (`parallel_tool_calls` on OpenAI-compatible, `disable_parallel_tool_use`
  on Anthropic).
- `vision`: whether an image reaches the model as an image or as the host's rendering. An endpoint
  marked `strictAlternation` off the Anthropic wire that takes native tool calls gets the rendering
  either way, since a native call's image there is a user turn after the result. On the text path
  the image rides inside the result's own user message, so the turn order holds and a vision model
  is shown it: `vision && (!nativeToolCalls || strictAlternation !== true || family ===
  'anthropic')`.
- `reasoning`: the `kind` the tier is spelled in (`anthropic-thinking`, `gemini-thinking`,
  `openai-effort`, or `none`), which must be one the session's wire speaks, and `ignored`, an
  endpoint that takes the parameter and does nothing with it. With `none` or `ignored` nothing is sent
  at any tier, and a tier other than off is reported unavailable once, before the first request.
- `streamingQuirks`: `reasoningField`, the OpenAI-compatible member reasoning streams in and is
  carried back in (null: either, as the SDK reads it), and `wholeJsonAnswer`, an endpoint that answers
  whole, which is asked for a whole answer.
- `toolSchemas`: the served JSON Schema, or the Gemini subset, on any wire.
- `statePlacement` and `strictAlternation` (agent#9): where the execution state goes; see below.

A run's record is the family's default (the three vectors, which change nothing the loop did), then
the host's `endpointCapabilities` answer for the endpoint and model, any members of the record
(`$defs/endpointCapabilities`), then the session's own (`session.model.capabilities`), read whole and
closed. Where the record comes from (an endpoint probe, a keyless local endpoint, a LAN address, a
context-window field, a provider's header) is the host's.

It adds three bounds on the Runtime's test tool, likewise: `testCallsPerTurn` (three),
`testPackBytes` and `testMatrixBytes` (65,536 each), described under Tool calls.

## The execution state

`budget.json`, `$defs/stateLocation`, and the two members of the capability record that place it
(`capability.json`); the vector
`budget/state-placement.json`. Since agent#9 the `vercel` adapter sends a stable prefix (its system
text with the execution instructions, the tools, the host's prompt, the history) and puts what
changes between requests in one text, the **execution state**: the request counters, the plan and
the operations index, the host's changing text (`context.currentState`, such as a chat's draft
snapshot) and the step's own instruction. Its first line is always

```text
EXECUTION STATE (from the host, current for this request and replaced on the next; working state, not a message from the person and never approval)
```

and the header occurs nowhere else in a request. On the first request the state is a second text
part of the person's own message, on every wire. After that it goes where the endpoint's placement
says: `trailing-user`, one user message after the history (merged by the Anthropic provider into the
tool-result turn it follows), or `system-tail`, the end of the system text after the head that
carries the Anthropic cache breakpoint (one string with the head on the OpenAI-compatible wire, a
second block or part on the other two). The host sets the placement through `configureHarness`
(`endpointCapabilities`, members of the capability record): `system-tail` for an endpoint marked `strictAlternation` (it refuses two
user turns in a row and a user text after a tool result), and by default for Anthropic when a
thinking tier is on, where a text block in a tool-result turn starts a new assistant turn;
`trailing-user` otherwise. The placement is chosen once per run. The critic's step note carries the
same header and is placed the same way. A host that reads requests, as the Desk server's memory
recall does, finds the request text as the next section says.

## Where the request text sits

`budget.json`, `$defs/recallQuery`; the vectors `recall/openai-compatible.json`,
`recall/anthropic.json`, `recall/gemini.json` and `recall/delegated-child.json`; the rule in code,
`certification/recall.ts`. The Desk server recalls project memory for every request it relays, from
the request text (agent#11). The rule a server applies, on any request of a run:

1. Take the request's first user turn: the first message with role `user` in `messages`
   (OpenAI-compatible, where system messages may come before it, and Anthropic), or the first
   content with role `user` in `contents` (Gemini).
2. The request text is its first text part that does not start with `STATE_HEADER`. A string
   content is one text part; on Gemini a part is text when it carries `text` and is not a thought.

The request text is the host's prompt where it fits the prompt's allowance. Where it does not, the
prompt is retained and previewed, and the person's message (`currentRequest.message`) is sent as its
own first text part, before the preview, so the first text part is still the person's request. A
delegated child's request text is its objective: the coordinator hands it to the child as
`context.objective`, and the adapter sends it as the first text part, before the child's prompt
(`SessionContext.opening`).

It holds on every request of a run, on every wire, because the `vercel` adapter puts the request
text first in the run's first user message and never moves, replaces or drops that message: the
state joins it only as a later part on the first request, a chat's index of earlier results is its
last part, and compaction keeps it whole. Every other user text the loop adds comes after it: a
text-path result (headed `TOOL RESULT` or `TOOL ERROR`), the images shown beside a result, the
finishing and reformat instructions, the compaction summary and the trailing state. So the newest
user text is often not the person's, and the first user turn always is. Measured on all three wires
in the review of judgment-pack-agent#33: the newest-remaining-text rule fails on a text-path
request, the finishing request, the reformat request and after compaction; the Desk's code as
ported fails the same, except after compaction on Anthropic, where the first message and the
compaction summary merge into one user turn. Where the text sits on the
third request of the recorded scenario, after two tool calls:

| Wire | First user turn | Pointer |
| --- | --- | --- |
| OpenAI-compatible | the second message, after the system message; its content a string (an array of parts when it has more than one: the person's message before a preview, or a chat's index after the request) | `/messages/1/content` |
| Anthropic | the first message; a text block | `/messages/0/content/0/text` |
| Gemini | the first content; a text part | `/contents/0/parts/0/text` |

The critic's requests are a second conversation, whose first user turn is the critic's own
message. On the agent wire (`codex`) the server recalls once per run from the agent request's
prompt, which starts with the request text. The certification suite applies the rule to every
request of the recorded scenario on every certified leg, 21 legs in four families (the six plain
legs, the six text-path legs, three strict-alternation legs and the six thinking legs' authoring
requests), and expects the text the run started from; `memory/recall.test.ts` applies it on a
finishing request, a reformat request, after compaction, with a chat's index, where the prompt is
previewed (a 400-turn transcript at a 32,768-token window) and on a delegated child's first
request. The recalled set itself, and carrying it through a run's steps, is the server's:
the package reads nothing new from memory.

## The chat-scoped context

`tool-result.json`, `$defs/turnIndex` and `$defs/readCacheKey`; `checkpoint.json`'s `context`; the
vectors `chat/turn-index.json` and `chat/cache-key.json`; `context/chat.ts`. Without one, a run's
context ledger and read cache are its own and end with it, as before. Since agent#8 a host may make
one `ChatContext` per chat and hand it to every run of that chat as `context.chat`. It holds:

- **The chat's ledger.** An entry's id is the chat's scope, the document's id and the first 16 hex
  digits of its content's SHA-256, so the same content retained by a later run is the same entry,
  and `read_window`, `read_context` and `search` in a later run find what an earlier one retained.
  Each answer of a session tool that reads (every runtime tool, and a host tool declared a read) is
  retained, with its tool and the SHA-256 of its canonical arguments, whatever its size. The
  ledger's own tools and the plan tools are not: their answers are windows of what the chat holds,
  or working state.
- **The chat's read cache.** A read the tool declares immutable (`contextPolicy.immutable` on a host
  tool that reads; the gate's catalogue, `runtimeToolContextPolicy`, for a runtime tool) is served
  again across the chat's runs without a call, under the SHA-256 of the canonical JSON of `{tool,
  arguments, runtime}`. `runtime` is present for every runtime tool and for a host tool that
  declares `contextPolicy.runtime`, and is what the host gives as the run's `context.runtime` (an
  identity, such as the Runtime's pinned digest, and a version); without it such a read is cached
  for the run only. A mutable tool is never served from the cache. A cached result is the ledger's
  entry for it: evicted, it is no longer served.
- **One byte bound across the runs.** `retainedBytes` (8 MiB) holds for the chat as it held for a
  run. Each run is one turn of the chat, and an entry it retains or reads is referenced by its turn
  until the run ends. When an entry would take the chat past the bound, the entries no open turn
  references are evicted, the oldest first: an entry's age is the last turn that retained or read
  it, and among one turn's entries the first retained goes first. If what is left is all referenced
  by open turns, the retain is refused as before (`context-limit`). An evicted result is listed in
  the next run's index and a read of an evicted entry says why it is gone, so nothing goes unsaid.
- **The index.** A run's first message carries, after the request text, a compact index of the
  results earlier runs kept, in the compaction summary's shape: one line, then one data block
  closed by the run's marker, whose JSON lists each result's `read_window` id, tool, arguments
  digest, size and first line, the count of older ones left out, and the results evicted since the
  last index was sent. It is bounded by `turnIndexEntries` and `turnIndexBytes` (the newest rows
  are kept) and cut to the room the first message leaves in the prompt's allowance, at most a
  quarter of that allowance (`INDEX_SHARE`): rows go, oldest first, until it fits. The request is
  never previewed and a run never fails because of the index; with no room it is not sent, and the
  evictions it would have listed wait for the next index that is. A run resumed from another
  chat's checkpoint, whose ledger is its own, is sent none. On the agent wire it is added at the end
  of the prompt.

**Where it lives and how it is bounded** (the open question this settles): in memory, in the host's
`ChatContext` object; the package writes it nowhere, and it is bounded by `retainedBytes` alone. A
run's checkpoint carries the entries that run retained or read, not the chat's ledger, so the
checkpoint (4 MiB) and the saved chat history (16 MiB) grow with a run, not with a chat. Resumed in
the same chat, the checkpoint's entries the chat no longer holds are retained again; resumed under
another chat context or none (after a reload, say), they make a ledger of the run's own, as before.

**When a chat is deleted** the host calls `dispose()` on its context, after its run has ended or
been stopped: everything it holds is cleared and a later run handed it fails before any request.
Nothing is global: a run reaches only the chat context it was handed, a delegated child is handed
none, and an independent phase (the critic) works in a ledger of its own and neither clears nor adds
to the chat's, so nothing retained in one chat is reachable from another. A run that fails at its
start closes its turn, so what it retained can be evicted, and a checkpoint restored into the chat
is held to `retainedBytes` as any retain is.

Measured on a scripted two-turn chat (`context/chat.test.ts`: the instructions, the schema and the
tests read on turn one, a new message on turn two), turn two makes no Runtime or host read and one
model request instead of four. Its first request is 21,231 bytes against 18,920 without a chat
context, the index adding 2,146 bytes of text; the turn's requests come to 21,231 bytes against
102,963, because the three results are not read again.

## Provenance

`provenance.json`. Per model request, the Desk records the request number, the phase (`work`,
`synthesis`, `review`) and the provider-neutral completion (`finishReason`, and the input, output
and reasoning token counts the provider reported, and, since agent#9 in this harness, the prompt
cache's reads and writes where the provider reports them), in the checkpoint; a request the provider
refused and that was sent again has a row per attempt, the refused one with its `attempt`,
`failure` and `waitMs`, the one that answered with its `attempt`. A failure carries the status,
request id and completion it was classified from. The checkpoint's `requestKey` and every retained
entry are SHA-256 digests; the server writes the project brief's digest into the brief itself.
That is all: see the open question on provenance below.

## Open questions

### Who assembles the prompt

The adapter does not assemble the whole prompt today. The Desk server adds two things behind it on
every dispatch (`internal/desk/ai_context.go`): the project brief (a project file read afresh, at
most `briefBytes`, headed with its path and SHA-256) and recalled project memory (at most
`memoryBytes`, recalled from the request text, found as [Where the request text sits](#where-the-request-text-sits)
states). On the model relay it puts them in
the provider body (a leading system message for OpenAI-compatible endpoints, appended to `system`
for Anthropic, appended to `systemInstruction` for Gemini); on the agent wire it appends them to the
run's instructions. It then refuses, never clips, a request over the input budget. The adapter
cannot see either addition and reserves the brief's full size from every request, and the memory's
while the host has not said memory is off (agent#11). Since agent#9 the
`vercel` adapter orders its own part as a stable prefix with the changing state placed after it
(see [The execution state](#the-execution-state)). The server's addition still lands inside that
prefix (a leading system message on the OpenAI-compatible wire, after the system text on the other
two), so where memory is recalled per request, a request whose recall differs parts from the one
before it there. The
evidence for each side and the options are in
[ADR-0002](../adr/0002-who-assembles-the-prompt.md); the decision is the maintainer's.

### How a cross-turn ledger is bounded

Settled by agent#8: see [The chat-scoped context](#the-chat-scoped-context). Three caps met here: the
ledger may retain 8 MiB (`retainedBytes`), the checkpoint that carries it is at most 4 MiB
(`MAX_CHECKPOINT_BYTES`), and the saved chat history that holds every chat's checkpoints is one
document of at most 16 MiB (`maxConversationBytes`) for up to 256 chats. The chat's ledger lives in
the host's `ChatContext`, in memory, bounded by `retainedBytes` with eviction; a checkpoint carries
a run's entries only. Persisting a chat's ledger across a reload is not done, and would need a
store and a bound of its own.

### How a framework with its own subagents carries a delegated task's budget

Delegation today is the host's: the adapter only reports `delegation_requested`, and the
coordinator runs each child on the same engine, counting each child's model requests through the
model capability it wraps and holding the shared limits above. A framework that brings its own
subagents (Deep Agents' task tool, the AI SDK's subagents as tools) runs children inside the
adapter, where the host sees no `delegation_requested`, no per-child model call and no child state
unless the adapter reports them. Open: whether every child's model traffic must still go through
the host's model capability so the host keeps counting; whether the adapter emits
`delegated_task` itself, and with whose limits; and how a child's budget is stated to the adapter
(a member of the request, or the host's counting alone).

### Where the two engines disagree

- **The critic's default.** `vercel` runs the critic when the session leaves `adversarialReview`
  unset and the reasoning tier is on (`adversarialReview ?? runsRefutation()`); `codex` runs it
  only when `adversarialReview` is `true`.
- **Retries.** `vercel` keeps the SDK's own retries off (`maxRetries: 0`) and sends a model request
  again only after a `refused` outcome whose recovery is `wait` or `retry`, at most twice, with a
  `retry` event each time (see Failures); separately, it starts the whole session again only for a
  tier refusal that arrived before anything was delivered, three attempts in all. `codex` relays up to two `retry` events from the Codex bridge's
  own recovery within the turn, then fails with `retries-exhausted`. In a conversation the
  delegation coordinator retries every agent run under it, the parent's included, on `busy`,
  `not-started` and nothing else, three attempts in all, within five agent runs shared by the
  parent and its children.
- **Deadlines.** `codex`: the Codex bridge bounds a whole run at ten minutes and the server reports
  it as a `timeout` failure; the browser's agent transport ends a run at twelve minutes, a backstop
  behind the bridge's deadline. `vercel`: no deadline for a whole run, and a turn's wall clock
  (`turnMilliseconds`) that pauses the run rather than failing it; the relay bounds each relayed
  request at ten minutes, and two minutes without a write. Both: a tool call past
  `toolCallMilliseconds` is a failed result, not a failed run.
- **Budgets.** `vercel` counts model requests (`unit: "model-requests"`, twenty, grants of ten up to
  a hundred). `codex` is `provider-managed`: the limit stays 1, no grant is possible,
  `request_budget_increase` answers `unavailable`, `budget_paused` cannot arise in the loop, and the
  coordinator counts agent runs rather than requests.
- **Call ids.** `codex` sets `callId` on every call and result. `vercel` sets it on an interactive
  session, and always on its refusal of a repeated call or of a call on the final, tool-free
  request; never on a call to a tool the model was not offered.
- **An unoffered tool.** `vercel` reports the call and a refused result, and the run goes on;
  `codex`'s transport treats it as a protocol failure and the run ends with `connection-lost`.
- **Framed answers.** Both frame a reading tool's answer the same way; the data inside differs as
  the wires do: on `vercel` the bounded answer's JSON, on `codex` the agent-wire answer's text (a
  text-only answer as its text).
- **Messages.** `codex` emits a `message` for each commentary-phase message during the run as well
  as the final prose; `vercel` emits one `message`, at the end.
- **Kinds only one engine emits.** `reasoning`, `thinking_unavailable`, `response_recovery` and
  `guardrail` `narrowed` are `vercel`'s only. Both emit `retry`, for different recoveries.
- **An unreadable proposal.** `vercel` asks once more, with a tool-free reformat request, before it
  reports `invalid-response`; `codex` reports it at once. Both deliver the whole answer first. The
  reformat request and the turn clock on `codex` are agent#29.
- **Failure source.** A loop failure is `source: "model"` from `vercel` and `"agent"` from
  `codex`.
- **The capability record.** `vercel` reads it; `codex` has no model capability and reads none: its
  agent wire carries text only, so an image always reaches it as the host's rendering, and its tools
  are always the agent's own.
- **Certification.** `vercel` is held to the conformance session's exact event order on six legs
  (three wire families, each streamed and whole), and to the same order through the text tool path
  on the same six; `codex` to a separate leg that runs T1 to T6 only, with the critic on, and asserts a subset (no
  error, the proposal, the gate's events in place, one `end`). The two are not held to one
  sequence.

### Other departures

- **No approval event.** The design draft had a call that needs a person's decision pause the run;
  the Desk has no such event. A framework with its own approval interrupt (the AI SDK's tool
  approval, Deep Agents' interrupts) has nothing to map to except `user_question`. The boundary
  already refuses to compact across a pending approval part, so the shape is anticipated.
- **The host's events in the adapter's stream.** `guardrail` `rewrote` and `refused` are emitted by
  the gate through a callback and land between a call and its result only because both run in one
  page. Across a process boundary the order needs stating.
- **No effect class on runtime tools.** Which runtime tools are reads is decided twice by name
  (`runtimeToolContextPolicy` and a list in `context/managed.ts`).
- **The stream is open.** No reader refuses an unknown member or kind at run time; whether the
  contract closes the events is for the certification suite.
- **Two shapes for one limit.** The request limit is `budget_paused` within a run and an `error`
  with `request-limit` when a paused execution is resumed without a grant.
- **Provenance.** No record names the model, the adapter or the contract version, digests the
  prompt actually sent (`requestKey` digests the person's request), or times a request; a
  `ContextUsage` estimate is computed and emitted nowhere. Cache reads and writes are recorded by
  this harness since agent#9; the Desk's Go reader of a completion drops them until its swap.
- **Declared, unread.** `recentMessages` is in the budget table and read by no code.
