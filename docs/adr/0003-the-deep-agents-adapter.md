# ADR-0003: The Deep Agents adapter

Status: proposed, 2026-10-19.

## Context

ADR-0001's third step is an adapter on LangChain's Deep Agents (JavaScript, MIT), the first that
swaps the framework under the contract rather than moving the Desk's own loop. Deep Agents builds
an agent graph on LangGraph (LangChain's `createAgent`: a model node, a tools node and middleware
hooks around both) and mounts, by default, file tools over a backend, a `task` tool that runs
subagents, conversation summarization and a tool-call patcher. Its browser entry,
`deepagents/browser`, exports `createDeepAgent` and `StateBackend` and no Node-only module.

The adapter is `packages/harness/engines/deepagents/`, registered as `deepagents`, a model engine:
it is handed the same session as `vercel` (a model capability, the runtime's tools through the
gate, the host's tools) and is held to the same certification legs and scenarios.

The pins, each published before 2026-10-05, read in `deepagents`'s own `types.ts` and `agent.ts`
at the tag `deepagents@1.14.1` rather than in any summary:

| Package | Version |
| --- | --- |
| `deepagents` | 1.14.1 |
| `langchain` | 1.5.15 |
| `@langchain/core` | 1.2.14 |
| `@langchain/langgraph` | 1.4.19 |
| `@langchain/langgraph-checkpoint` | 1.1.5 |
| `@langchain/langgraph-sdk` | 1.12.1 |
| `langsmith` | 0.9.0 (the newest release the `deepagents` peer range, below 0.10.0, admits) |

Their own dependencies were resolved as published before the same date (`npm install --before`).

`CreateDeepAgentParams` at that tag has `model`, `tools`, `systemPrompt`, `stateSchema`,
`middleware`, `subagents`, `responseFormat`, `contextSchema`, `checkpointer`, `store`, `backend`,
`interruptOn`, `name`, `memory`, `skills`, `permissions` and `streamTransformers`. Each is placed
below: the adapter passes `model`, `tools`, `middleware` and `backend`, and nothing else.

## Decision

### 1. The model is the host's relay, keyless

The adapter's model is a LangChain chat model (`RelayChatModel`, `model.ts`, a `BaseChatModel`)
whose only reach is `session.model.call`, the host's capability: a path suffix, a body and the
protocol's own headers, no address and no credential. LangChain has one other way out: its
LangSmith tracer, which the environment's tracing variables (`LANGSMITH_TRACING`,
`LANGCHAIN_TRACING_V2` and their kin) turn on wherever a host has a `process.env`, and which sends
each run through the global `fetch` with the environment's key. A run whose environment sets one is
refused, as a `configuration` failure, before an agent is built or a request made; so on every run
this engine starts, the relay capability is the only way out. It is refused rather than forced off
because forcing it off does not hold on every path: LangChain reads the environment wherever a run's
async context carries no LangSmith run tree, and LangGraph starts parts of a run in such contexts
(measured: with a run tree whose tracing is off set around the whole run, the tracer still appeared
in the model node and reached the global `fetch`). A browser page has no `process`, and runs. The
certification holds it with a sealed leg run with every tracing variable, a key and an endpoint
set (K1b). `bindModelCall` (`session.ts`) builds the
address on the host's relay and writes the bearer, as it does for `vercel`; the relay attaches the
provider's key on the far side. Every request goes through the one retry a refusal earns, the rule
`vercel` keeps (`engines/vercel/retry.ts`, imported, as `codex` imports `engines/vercel/channel.ts`).

The body and the answer are `wire.ts`'s, written by the adapter for each of the three wires,
OpenAI-compatible, Anthropic and Gemini, because LangChain's provider classes were measured against
the pinned versions and cannot carry what the contract holds a model engine to:

- `@langchain/google-genai@2.3.2` sends through `@google/generative-ai@0.24.1`, whose request
  options take no `fetch`; it calls the global one, so it cannot route through the relay
  capability at all, and the conformance session's seal refuses it.
- `ChatOpenAI` (`@langchain/openai@1.6.2`) routes through the relay (the `openai` SDK takes a
  `fetch`), but reads reasoning only from `reasoning_content` (an endpoint that streams it as
  `reasoning`, as vLLM 0.18 and later and OpenRouter do, is read as none), never carries reasoning
  back on the next request, and throws (`TypeError`) on a whole answer to a streamed request.
- `ChatAnthropic` (`@langchain/anthropic@1.5.12`) routes through the relay too, and throws ("No
  chunks returned from Anthropic API") on a whole answer to a streamed request.
- Both SDKs write a credential header of their own (a placeholder key) on every request, which only
  the host's header allow-list would remove, and retry on their own unless told not to.

On the Anthropic wire each tool carries `eager_input_streaming: true`, as the `vercel` adapter's
provider sends it. One difference from `vercel`'s bodies stays, recorded in the wire contract: the
`experimental_evaluate` call the history carries back is the model's own, without `rehearsal: true`
(the gate's frame rewrite is the one layer; `vercel` adds a second through the AI SDK's
`experimental_refineToolInput`).

The capability record drives the request as it does on `vercel` (`capabilities.ts`): the tier's
members are the host's table (`thinking.ts`, read from the thinking slot each attempt, a refused
spelling falling back and a refused tier degrading once); the Gemini subset of a schema where the
record says so; the execution state placed by the record (`system-tail` for a strict turn order,
and for Anthropic with a tier on); one call per turn where it takes no parallel calls; the
reasoning member it pins; a whole answer asked for where it answers whole; and images by `vision`.
Signed reasoning goes back exactly as it arrived: Anthropic's content blocks (a signature split
across events is joined, never sent as a fragment) and Gemini's parts (pieces of one thought joined,
two empty signed thoughts kept as two). The Anthropic cache breakpoints are on the system head, the
last tool and the first text part of the first user turn.

Cost: the adapter owns three wire codecs, about nine hundred lines (`wire.ts`). A provider's
change that a LangChain class would absorb is this adapter's to make, and the certification
suite's wire legs are what notices.

### 2. The gate is the only tool boundary

Deep Agents' own middleware is off. `createDeepAgent` replaces a mounted middleware in place with a
custom one of the same name (`mergeMiddlewareStack`), so the adapter passes middleware that does
nothing under the four names it mounts by default: `FilesystemMiddleware` (`ls`, `read_file`,
`write_file`, `edit_file`, `delete`, `glob`, `grep`, `execute`), `subAgentMiddleware` (`task`),
`SummarizationMiddleware` and `patchToolCallsMiddleware`. The prompt-caching middleware mounts
only for a model Deep Agents recognises as Anthropic's by class, which the relay model is not;
skills, memory, async subagents and human-in-the-loop mount only when asked for, and are not.

The tools the agent is given are declarations: each name, description and the schema the record
says the model is shown, and a function that refuses if reached. Every call the model makes is
answered by the adapter's one middleware (`JudgmentPackBoundary`), in its `wrapToolCall` hook,
before LangChain's tool node and its argument validation: a name outside the phase's offer (the
loop's, the critic's allow-list, Plan mode's, a delegated child's profile) is put through the gate
as the model wrote it and refused, with the gate's `guardrail` event between the call and its
result; any other goes through the package's `callTool` (and so `admit` and the gate) or the host
tool's own function, with the contract's events. The text tool path dispatches through the same
function. In `wrapModelCall` the same middleware checks that the request offers exactly the
gate's tools and ends the run before anything is sent where another middleware added one.

`interruptOn` is not used. LangGraph's `interrupt()`, which the human-in-the-loop middleware calls,
reads its run's configuration from `AsyncLocalStorage` and needs a checkpointer; the browser build
has no `AsyncLocalStorage` (`@langchain/langgraph`'s web entry), and there the interrupt throws
"Called interrupt() outside the context of a graph" (measured: the same agent interrupts under
Node's conditions and throws under the browser's). The contract also carries no approval pause
(`approval.json`: no approval crosses the slot; the run proposes and the host acts). The run's two
pauses for a person stay where they are: the question (`ask_user`), which the boundary reads closed
and, well formed, reports as `user_question` and ends the turn on, unanswered; a malformed one is a
failed result naming the field, corrected in the same turn. The question's pause stops only the
calls that come after it in the same model turn, on this adapter and on `vercel`: a call made before
`ask_user` in that turn (a `read_policy`) has run, and a call after it is reported as `tool_call`
with no `tool_result`. The other pause is the budget, whose
`request_budget_increase` runs and whose pause is the shared boundary's (`context/managed.ts`).

`permissions` is not used: its rules govern the file tools, which are off, and do not reach the
gate, which checks the names and arguments of the runtime's calls.

Cost: `createDeepAgent` refuses a tool named like one of its built-ins even with them off, so a
host tool may not be called `ls`, `read_file`, `write_file`, `edit_file`, `delete`, `glob`, `grep`,
`execute`, `task`, `start_async_task`, `check_async_task`, `update_async_task`,
`cancel_async_task` or `list_async_tasks` on this engine; the run ends as a `configuration` error.

### 3. Delegation is the package's, not Deep Agents' subagents

A spike ran Deep Agents' subagent middleware with one subagent and a parent that asked for two
tasks (scripted models, no network). The `task` tool takes a `description` and a `subagent_type`;
the child is handed the description as its only message and, by default, the parent's tools; two
calls run at once with no cap; the parent is answered with the child's last text, a report block
in it unread; the parent sees no event for either child and no usage of theirs. Binding that to
the package's profiles, budgets, references, reports and events would rebuild the coordinator
inside the `task` tool, so the subagent middleware is off (decision 2) and delegation is the
package's (`delegation.ts`, `withDelegation`): `delegate_tasks` is a host tool awaited inside the
step, as on `vercel`, its children run on this engine with fresh sessions, and the coordinator's
events, limits, figures, retry, reuse and resume are unchanged. The coordinator now wraps
`deepagents` as it wraps the other two.

### 4. Context and budget stay the package's

The ledger, the chat context, the read cache, the index and the compaction are
`context/session.ts`'s (the recall rule this listed was removed by ADR-0005, and the host's parts of
the prompt, asked once per run, are its too). The boundary's `wrapModelCall` hook asks for them on every request: the
history is projected into the message shape `SessionContext.messages` counts and archives, and
back (its summary becomes a user message); the state is placed; the step's completion and usage
are recorded. Nothing of the agent's own state is a second ledger.

`backend` is Deep Agents' own default, `StateBackend`, named: with the file tools off it holds
nothing. `checkpointer` is not used: the contract's checkpoint (`checkpoint.json`) is the execution
controller's, which never holds private reasoning and is what a resume reads; a LangGraph
checkpoint would hold the whole message state, signed reasoning included, as a second record of
the run under another rule, and nothing here needs one (no interrupt, decision 2). `store`,
`stateSchema`, `contextSchema`, `responseFormat`, `memory`, `skills`, `subagents` and `name` are
not used.

The run's events are written where they happen, on the run's ordered channel
(`engines/vercel/channel.ts`): the chat model says the prose (`message_progress`), the reasoning
(`reasoning`, deltas then the passage) and the end of each passage as it reads the relay's answer;
the boundary says each `tool_call` and `tool_result`, with the gate's `guardrail` between them;
the loop says the rest (`message`, `proposal`, `critique`, `response_recovery`,
`thinking_unavailable`, `user_question`, `retry`, `error`, `end`), and the shared boundary its
checkpoints and pauses. LangGraph's own stream and `streamTransformers` are not the source of these
events: the classic `messages` mode delivers through a background callback queue and the v3 stream
through a pump task, so a `tool_call` the boundary delivers could overtake the reasoning and prose
before it, and the guardrail lands between a call and its result only because the call has reached
the host when the gate fires. The figures are the provider's usage as each wire reports it, read
per step into the completion record (`completion.ts`).

### 5. The critic, the reformat run, the turn clock, retries and deadlines

They follow `engines/contract.ts` as `vercel` does. The turn around Deep Agents' graph is the
adapter's: one stretch of the graph per request sequence that `vercel` runs as one `streamText`
call (until the model stops calling tools, or the step closes the tools or reaches its limit), with
the text tool path, the request that finishes a response cut short, the one reformat request, the
`propose_pack` proposal and the critic (a second agent on the same relay, boundary and dispatch,
offered its allow-list) around it. The turn's wall clock pauses the run; a tool call past
`toolCallMilliseconds` is a failed result; a refused request is sent again by the shared rule.
What this engine does differently is recorded in the wire contract's list of disagreements.

### 6. Bundle shape

The adapter builds for the browser. The figures below come from one committed script,
`packages/harness/scripts/bundle-size.mjs` (`npm run bundle:size`): the package's Vite (8.2.2), for the
browser, minified by Vite's default, the two engines as two entries so that what they share is split
out, no plugin but an empty module for `ws` (an optional peer of LangSmith's client that no page
loads), no config file; sizes in kilobytes of 1,000 bytes, and gzip at level 6. The chunks only
`deepagents` reaches are 1,308.0 kB minified, 367.6 kB gzipped (two chunks: its own, 816.4 kB and
243.7 kB, and one only it imports, 491.6 kB and 123.9 kB); the chunk only `vercel` reaches is 615.5 kB
and 153.8 kB; and the two chunks both reach (the harness's core and their common dependencies) are
212.8 kB and 66.6 kB, which a host that already ships `vercel` already ships. Vite replaces four Node
built-ins that `deepagents/browser`'s dependencies import with empty modules (`path` and `util`, from
`micromatch`, `picomatch` and `fill-range`; `node:path` and `node:fs/promises`, from LangSmith's
sandbox client): none is reached with the file tools off. The figures this section first gave
(1,044.7 kB and 276.1 kB for `deepagents`, 799.5 kB and 181.7 kB for `vercel`) and its count of five
built-ins did not reproduce from any recorded build; these replace them. The built bundle was run
through a session against a scripted capability with the network globals replaced by throwing
sentinels: a runtime call, a refused name and the question pause, and nothing
touched.

## Consequences

- `deepagents` is the registry's third id, a model engine; `npm run certify -- --adapter
  deepagents` runs every model-engine leg and the six scenarios against it, and CI certifies it
  beside the other two. The contract's engine ids gain it (`provenance.json`), after the Desk's two.
- The package exports `./deepagents` and depends on the pinned packages above.
- A Desk selects it only once its own configuration admits the id; nothing here changes the Desk.
- The open question on a framework with its own subagents (the wire contract) is answered for this
  adapter by keeping the package's delegation; a Go adapter faces it again.
