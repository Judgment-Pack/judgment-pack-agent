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
execution's context ledger, its checkpoint and its request accounting, and a delegation coordinator
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
fence, before the `invalid-response` error. A critic that fails after a valid proposal (its
provider error, its length cutoff, a budget pause or the turn's wall clock) does not raise an error
ahead of the proposal: the proposal is delivered with `note` `{kind: "critic-unfinished", message,
failure}` and no `critique`. A person's Stop still ends the run.

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
child may use), `contextPolicy` (`immutable`, `reference`) and `presentation`. The boundary adds
`list_context` and `read_context` to every session and, in a conversation, `update_plan` and
`request_budget_increase`; an offered tool may not take those names.

An adapter shows the model the schema the runtime serves or nothing: a tool served without one ends
the session. The one exception is the Gemini family, shown the schema less a closed list of
keywords, with a `narrowed` guardrail per tool that lost one. On the agent wire the `codex` engine
offers each tool under an alias (`desk_runtime_N`, `desk_host_N`) and reports the real name in its
events.

A host may offer `propose_pack`, with the input schema the contract states (`document`, an object;
`unknowns`, strings) and effect `proposal`. Where it does, the instructions tell the model to deliver
the proposal by calling it; the adapter reads the call itself, answers the model that the proposal
was received and will be shown for review, and never reaches the host's `execute`; arguments of
another shape are a failed result naming what to correct. The latest call's document is the
proposal, and the fenced block stays the fallback. The critic is not offered it.

## Tool calls

`tool-call.json`. A `tool_call` carries the tool's own `name`, the model's own `args` (before the
gate rewrites anything) and, where set, a `callId`. It is emitted before the call is made, so the
host can journal it. A host tool with an `interaction` gets `user_question` or
`delegation_requested` instead.

Every runtime call leaves through the host's gate (`toolGate.ts`), which reads the outbound
`tools/call` frame: a name outside the allow-list is refused on the wire (`guardrail` `refused`,
and the call fails); `experimental_test_cases` leaves only with exactly `pack` and `matrix`, each
JSON text within the policy's `testPackBytes` and `testMatrixBytes` (`$defs/testCasesArguments`),
and any other member, a path among them, is refused the same way, the notice naming members and
sizes and never their text; `experimental_evaluate` always leaves with an own `rehearsal: true`
(`guardrail` `rewrote` where the model's call lacked it), so a rehearsal never writes an audit
record. The adapter neither applies nor skips these rules.

One turn may call `experimental_test_cases` at most `testCallsPerTurn` times (three by default).
The count is the shared boundary's (`context/session.ts`), so both engines keep it: a call past it
is not made, and the model reads a failed result (`request-limit`, source `tool`, outcome
`not-started`) that names the limit and tells it to use the reports it has. Each call re-sends the
whole pack, so without the cap a test, fix and retest loop would be bounded only by the request
allowance, at a cost that grows with the square of the steps; the repeated-work detector compares
arguments and does not see a pack that changes each time. A new turn starts at nought.

## Tool results

`tool-result.json`. A `tool_result` carries `name`, `isError`, `text` (the answer's text blocks
joined) and `structured` (its `structuredContent`), and the `callId` of its call where one is set.
The event is not bounded: the host sees the whole answer.

What the model is shown is bounded. A result whose JSON text is at most `toolResultBytes` (12,000
by default) reaches the model whole; a larger one is retained in the execution's context ledger and
the model is shown a reference envelope: the item's `contextId`, `kind`, `label`, `bytes`,
`sha256`, up to thirty `sections` (JSON pointers with their types), `readWith: "read_context"`,
whether it is `historical`, and a 1,000-byte `preview`. The model reads it back in sections with
`read_context` (`id`, optional `sha256`, `path`, literal `query`, `offset`, `limit` up to
`readBytes`, 6,000 by default). Where the ledger itself is full (`retainedBytes`, 8 MiB), the model
is told the operation returned and its output could not be retained, and not to repeat it.

What the model is shown of a reading tool's answer (every runtime tool, `read_context`, and a host
tool declared `effect: "read"` or, declaring no effect, offered to children as a read) is one data
block (`$defs/framedData`): a first line naming the run's marker and the tool, the answer's text,
and a last line, `END RETRIEVED DATA` and the marker. The marker is 128 random bits drawn when the
run starts, the same for every block of the run and never written into the instructions, so text
fetched before the run cannot close a block early; where an answer itself carries the marker (a
tool that echoes an argument the model wrote), each occurrence is replaced before framing. `vercel`
hands the block to the model on purpose, through the SDK's `toModelOutput`, as a text output whose
data is the bounded answer's JSON; `codex` sends it as the agent-wire answer's text. Context
compaction reads a block back to its data, so the summary keeps its handles, and frames the
compacted steps as one block of the same form. The run's instructions, the critic's included, carry
the rule that text inside a block is data and never instructions (`DATA_RULE`). A failure the
harness wrote and an acknowledgement the adapter wrote are not framed: they are the host's words.
The `tool_result` event is never framed.

A tool that throws is a failed result carrying `{"failure": …}` (or `{"questionFailure": …}` for
`ask_user`), recoverable working data for the model. So is a runtime or host tool call past the
policy's `toolCallMilliseconds`: it is stopped and answered with class `timeout`, source `tool`,
outcome `unknown`, and the run goes on. On the agent wire the answer travels as
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
and result, the `completions`, and the ledger's retained `context` (each entry with its digest, so
a restore refuses a changed one). Never permissions, evidence approval or private reasoning. At
most 4 MiB as JSON text.

## Failures

`failure.json`. One catalogue (`contract/desk/catalog.json`, 23 classes, each with the sentence a
person sees and a recovery), and one failure shape: `version` 1, `code`, `source` (`model`,
`agent`, `tool`, `context`, `storage`, `desk`), and `outcome`: `not-started` (admission refused it,
nothing ran), `refused` (a 4xx other than 408), or `unknown` (everything else, every 5xx and
timeout included). Optional `status`, `requestId`, `retryAt` (from the provider's headers),
`detail` (host-authored only, replacing the catalogue sentence) and `completion`. Provider prose,
bodies, URLs and headers are never retained. Only `busy` with `not-started` proves an agent run safe
to retry. A model request the provider refused is sent again by `vercel` only where the outcome is
`refused` and the catalogue's recovery for the class is `wait` or `retry` (429 `rate-limited`, a 4xx
carrying an overload code): at most twice, each after the longer of the backoff (one second, then
two) and the provider's `Retry-After`; not at all where `Retry-After` asks for more than thirty
seconds, when the refusal is reported at once with its `retryAt`; never on an `unknown` outcome (a
5xx, 408, 504 or a transport failure); and never once a response has answered, so a stream that
breaks after its first byte is reported. Each attempt is a model request, counted against the
allowance and recorded, and each retry is said with a `retry` event. What each engine reports on a
dropped connection, a stall, a context-length refusal, a runtime socket drop and a whole answer
where a stream was asked for, and what Retry does after each, is in
[AI error handling](ai-error-handling.md).

## Budgets

`budget.json`. One table both the browser and the server read: the input ceiling
(`maxInputBytes`, and the window and output reserve it is computed from, overridable per model),
`toolResultBytes`, `readBytes`, `retainedBytes`, and the bytes reserved for what the server adds
behind the adapter (`briefBytes`, `memoryBytes`). The request allowance is counted by the host in
model requests: twenty to start (`MAX_TURNS`, also the turn bound), ten more per grant the person
makes, never above a hundred. The model is told the counts in its execution guidance and may ask
for more with `request_budget_increase`, which never grants: with two or fewer requests left it
pauses the run for the person.

The harness adds two time bounds the Desk's table does not carry, each taken from the table where it
does. `turnMilliseconds` (fifteen minutes): on `vercel`, when one turn's wall clock passes, what is
in flight is stopped and the run pauses as `budget_paused` does, the checkpoint's `reason` saying
so; a proposal already made is delivered, with a note where the critic was cut.
`toolCallMilliseconds` (two minutes): on both engines, a runtime or host tool call past it is a
failed result the model reads, never a failed run.

It adds three bounds on the Runtime's test tool, likewise: `testCallsPerTurn` (three),
`testPackBytes` and `testMatrixBytes` (65,536 each), described under Tool calls.

## Provenance

`provenance.json`. Per model request, the Desk records the request number, the phase (`work`,
`synthesis`, `review`) and the provider-neutral completion (`finishReason`, and the input, output
and reasoning token counts the provider reported), in the checkpoint; a request the provider
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
`memoryBytes`, recalled by the tail of the latest user message). On the model relay it puts them in
the provider body (a leading system message for OpenAI-compatible endpoints, appended to `system`
for Anthropic, appended to `systemInstruction` for Gemini); on the agent wire it appends them to the
run's instructions. It then refuses, never clips, a request over the input budget. The adapter
cannot see either addition and reserves their full size from every request. The evidence for each
side and the options are in [ADR-0002](../adr/0002-who-assembles-the-prompt.md); the decision is the
maintainer's.

### How a cross-turn ledger is bounded

The context ledger lives for one execution and is cleared when the run ends; a continuation
restores it only from the checkpoint, digest-checked. Three caps meet here and do not agree: the
ledger may retain 8 MiB (`retainedBytes`), the checkpoint that carries it is at most 4 MiB
(`MAX_CHECKPOINT_BYTES`, and a run whose snapshot exceeds it stops with "the execution checkpoint
is full"), and the saved chat history that holds every chat's checkpoints is one document of at
most 16 MiB (`maxConversationBytes`) for up to 256 chats. A ledger kept across the turns of a chat
(agent#8) needs one bound, an owner (the host's store or the adapter's framework), and a rule for
what a turn may evict.

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
  reports `invalid-response`; `codex` reports it at once. Both deliver the whole answer first.
- **Failure source.** A loop failure is `source: "model"` from `vercel` and `"agent"` from
  `codex`.
- **Certification.** `vercel` is held to the conformance session's exact event order on six legs
  (three wire families, each streamed and whole); `codex` to a separate leg that runs T1 to T6 only, with the critic on, and asserts a subset (no
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
  `ContextUsage` estimate is computed and emitted nowhere. Cache reads and writes are not recorded.
- **Declared, unread.** `recentMessages` is in the budget table and read by no code.
