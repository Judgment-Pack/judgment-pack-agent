# @judgment-pack/harness

Provider selection is policy (ADR-0012): `models.<role>.provider` accepts `same`,
`other`, or a host-supplied connection ID, with run > project > table precedence.
`other` refuses when no other family is connected. Critic and child records identify
the answering connection and family; the checkpoint report carries `crossProvider`.
Capability records hold the tier map; hosts can override it per connection.
See the generated provider matrix in `docs/reference/providers.md` at the repository
root. Scenario 12 exercises two families, and certification prints its measurements.


Budgets are the `budget` section of the [agent's policy table](../../docs/adr/0011-the-agents-policy-table.md).
The host's run layer wins over its project layer, then table defaults. The same
resolver covers critic, models, concurrency, pace, and task class, with each member's
value and layer in `policy_resolved` and `checkpoint.policy`. `budget.json` is a
read alias for one release. Model-visible text cannot set policy.

The assistant harness of [Judgment Pack Desk](https://github.com/Judgment-Pack/judgment-pack-desk),
extracted from the Desk's `web/src/assistant` as it is at `integration/v0.13`, `d9251ec`, with no
behaviour change, behind the wire contract in [`contract/`](../../contract/README.md). Research
preview; the version is the package's own (`package.json`), and the releases are on GitHub.

It holds the engine slot and its three adapters, the **Vercel AI SDK adapter** (`engines/vercel`),
the **Codex adapter** (`engines/codex`) and, added here, the **Deep Agents adapter**
(`engines/deepagents`, [ADR-0003](../../docs/adr/0003-the-deep-agents-adapter.md)), and what they
share: the context budget and ledger
(`context/`), execution and planning (`execution/`, `planning/`), delegation, the memory client and
its tools (`memory/`; the store stays in the host), questions, refutation, completion, the failure
catalogue (`failures.ts`), the tool gate (`toolGate.ts`), the framing of retrieved text
(`framing.ts`, agent#12), thinking tiers, the Gemini schema rules,
the agent capability (`agent.ts`, `agentTransport.ts`) and the page side of a session
(`session.ts`: the model call bound to the host's relay and the gated MCP connection; no key is
ever in it). And it holds the **certification suite** (`certification/`).

Every file keeps its Desk name, place and exports (`conformance/` is `certification/` here, and two
test files lose their `.tsx`), so a host that ran the Desk's copy swaps to this one by changing
import paths. What the moved code read from a Desk feature it now reads from one module, `host.ts`.
Beyond that, one type is exported so declarations can be emitted (`Entry` in `context/ledger.ts`),
and tests that exercise a Desk feature stay in the Desk (see Tests). `scripts/parity.sh` at the
repository root prints every difference from the Desk's files and names its reason, including the
changes made here since the extraction, each by its issue (agent#1, the read window and the reader;
agent#9, the stable prefix and the state's placement; agent#4, the capability record and the text
tool path; agent#5, search, numbered reads and images; agent#8, the chat-scoped context; agent#11,
the request text on every request and the memory reserve (retired in agent#69); ADR-0005, the prompt assembled whole by
the adapter, with the host's parts and the prompt's digest; ADR-0006, tools with a source held at
the boundary (`toolSources.ts`, new); ADR-0009, skills held at the boundary (`skills.ts`, new); agent#10, the critic; agent#25, the
agent-engine certification leg; agent#29, the Codex adapter's reformat run; agent#14, agent#15 and
agent#13, delegation as one parent run with context by reference and per-profile budgets).

## Entry points

| Import | What it is |
| --- | --- |
| `@judgment-pack/harness/contract` | The engine slot's types and events (`engine.ts`), what every adapter shares (`engines/contract.ts`), the registry (`engines/index.ts`), the host's side (`host.ts`), the capability record (`capabilities.ts`), the image part (`images.ts`), and the chat-scoped context a host makes once per chat (`ChatContext`, `context/chat.ts`) with the run's context inputs (`ContextInput`). |
| `@judgment-pack/harness/vercel` | The Vercel AI SDK adapter, `engines/vercel/index.ts`. |
| `@judgment-pack/harness/codex` | The Codex adapter, `engines/codex/index.ts`. |
| `@judgment-pack/harness/deepagents` | The Deep Agents adapter, `engines/deepagents/index.ts` (see The Deep Agents adapter). |
| `@judgment-pack/harness/certification` | The scripted model and runtime server, and the recorded scenario and runtime the suite runs on. |
| `@judgment-pack/harness/<path>` | Any module at its Desk path, for example `context/session` or `failures`. |

`npm run build` typechecks every file, tests included, and emits ES modules with declarations to
`dist/`. The modules keep the Desk's extensionless relative imports and import JSON directly
(`contract/catalog.json`, `contract/policy.json`, `certification/*.json`), so they are consumed
through a bundler, as the Desk's Vite consumes them (`moduleResolution: bundler`).

## Host injection points

`host.ts` holds two kinds of thing.

**Contract values and types**, spelled as the contract spells them and held to it by `host.test.ts`
and `tests/test_harness_copies.py`: the runtime tool ceiling `ASSISTANT_TOOLS` (the six reads the
tool gate intersects every allow-list with: the Desk's five and, since agent#7,
`experimental_test_cases`), the engine ids `ASSISTANT_ENGINES` (the Desk's two and `deepagents`), and the types
`EndpointKind`, `ThinkingTier`, `Language`, `AITarget`, and, for Plan mode, `Chat`, `ChatStore`,
`FileRevision` and `PreparationReply`, each reduced to the members the harness touches. A host's
own types for these are structurally the same. `sourceMessage` fills the harness's own messages from
their values; it is the Desk's function without the Desk's plural table, which no harness message
reaches (none passes a `count`).

**Injection points**, what only a host can do. Each one throws, naming itself, until the host
supplies it with `configureHarness`. (`NO_SESSION_MESSAGE` is a sentence and cannot throw; it is
read only to build a `NoSession`, which does. `endpointCapabilities` has an answer of its own,
nothing known, so a host that knows nothing about its endpoints may leave it out; so does
`modelsFor`, none, every run's subordinate work on its own model; and so do the three parts of the
prompt, `brief`, `memoryFor` and `projectInstructions`, nothing.)

| Injection point | What the host supplies | The Desk feature it replaces | Read by |
| --- | --- | --- | --- |
| `assistantLanguageInstructions(locale)` | The reply-language section a run's instructions end with | `i18n/assistantLanguage` | `engines/vercel/loop.ts`, `engines/codex/index.ts`, `engines/deepagents/loop.ts` |
| `currentLanguage()` | The page's language, where a session names none | `i18n/locales` | `session.ts` |
| `sessionBearer()` | The session id a request carries, or a throw of `NoSession` | `mcp/session` | `session.ts`, `agentTransport.ts` |
| `sessionEnded()` | The sentence the session ended with, or null | `mcp/session` | `session.ts` |
| `whenSessionEnds(listener)` | A subscription to the session's end | `mcp/session` | `session.ts`, `agentTransport.ts` |
| `forgetSession(reason)` | Forget the session the host refused | `mcp/session` | `session.ts` |
| `refusalCode(answer)` | The code the host marked its own refusal with | `mcp/session` | `session.ts` |
| `discardBody(answer)` | Let go of an answer nothing will read | `mcp/session` | `session.ts` |
| `NoSession`, `NO_SESSION_MESSAGE` | The error a call without a session throws, and its sentence | `mcp/session` | `session.ts` |
| `chassisUrl(path, params)` | One host URL from a path and its own query | `files/client` | `session.ts`, `agentTransport.ts` |
| `deskHeaders()` | The headers that scope a request to the open desk | `desks/scope` | `session.ts` |
| `deskSocketURL(url)` | A socket address scoped to the open desk | `desks/scope` | `agentTransport.ts` |
| `aiHeaders(id, revision)` | The headers that bind a call to a configured AI connection | `assistant/aiConnections` | `session.ts` |
| `socketURL()`, `socketProtocols(id)` | The host's MCP socket address and the subprotocols it offers | `mcp/McpProvider` | `session.ts`, `agentTransport.ts` |
| `DeskWebSocketTransport` | The host's MCP socket transport | `mcp/transport` | `session.ts` |
| `deskFetch(input, init)`, `answer(response)` | An authenticated request to the host, and its answer read | `files/client` | `memory/client.ts` |
| `readFile(path, signal)` | A saved file's current revision | `files/client` | `planning/tools.ts` |
| `dialectAPI(path, …)` | A request to the host's input-preparation API | `dialects/client` | `planning/tools.ts` |
| `endpointCapabilities({ family, model })` | What the host knows about an endpoint and model: any members of its capability record (see The capability record), its probe result. Answers `{}`, nothing known, until supplied | none (new, agent#9, agent#4: the Desk decides by its three families) | `capabilities.ts`, read once per run by `context/session.ts` |
| `renderImageToText(image, signal)` | An image as text, for a model whose record says no vision: the host's render adapter | none (new, agent#5) | `images.ts`, from `engines/vercel/loop.ts`, `engines/codex/index.ts` and `engines/deepagents/loop.ts` |
| `brief()` | The project brief: the text of the project file the host's profile names, or nothing; at most `briefBytes`. Asked once per run, before its first request, and placed in the third layer of every request (see The prompt) | the Desk server's addition behind the relay (`internal/desk/ai_context.go`, `aiContext`) | `prompt.ts`, `readHostParts`, read by `context/session.ts`, `SessionContext.askHost` |
| `memoryFor(query)` | Closed memory records for `query` (bare text is an alias for v0.8.0), the person's message (a delegated child's objective; the host's prompt where neither is given), or nothing; at most `memoryBytes`. Asked once per run, not where `context.memory` is `false`, and placed in the fifth layer, the end of the stable prefix | the Desk server's recall behind the relay (`ai_context.go`, `memoryBackground`) | as `brief` |
| `projectInstructions()` | The project's own instructions, the per-project block the person wrote, or nothing; at most `projectInstructionsBytes`. Instructions at the lowest rank, placed in the fourth layer after the package's guidance and the brief; they cannot grant a tool or change an effect | none (new, ADR-0005) | as `brief` |
| `modelsFor({ connection, engine, family, model })` | The models a run's connection names for its subordinate work: `child`, delegated children's, and `critic`, the critic's; each absent or null for the run's own model. Asked once per run with the run's connection (`context.connection`), engine, family and model. Answers none until supplied. It replaces agent#10's `criticModel` (see The critic) | none (new, agent#10; one seam since the review of agent#35) | `host.ts`, `runModels`, read by `context/session.ts`, `SessionContext.models`, for `engines/vercel/loop.ts`, `engines/codex/index.ts`, `engines/deepagents/loop.ts` and `delegation.ts` |

The failure catalogue and the context budget table are the package's own copies of the Desk's
Go-side files (`internal/aifailure/catalog.json`, `internal/aicontext/policy.json`), byte for byte,
in `contract/`, held equal to the contract's copies and schemas by `tests/test_harness_copies.py`.

### Wiring a host

Supply every injection point once, before the first run, from the module that starts the host:

```ts
import { configureHarness } from '@judgment-pack/harness/contract'

configureHarness({
  assistantLanguageInstructions, currentLanguage,
  sessionBearer, sessionEnded, whenSessionEnds, forgetSession, refusalCode, discardBody,
  NoSession, NO_SESSION_MESSAGE,
  chassisUrl, deskHeaders, deskSocketURL, aiHeaders, socketURL, socketProtocols, DeskWebSocketTransport,
  deskFetch, answer, readFile, dialectAPI, endpointCapabilities, renderImageToText, modelsFor,
  brief, memoryFor, projectInstructions
})
```

For the Desk each value is the one its own module exports under the same name (for example
`sessionBearer` from `web/src/mcp/session.ts`). A host may wire them in parts; members it leaves out
keep what they had. The host keeps what the contract says it keeps: the relay and its keys, the tool
gate's decision of what a session may call, and approvals.

### Reading tools

A host's reading tools (a page it fetched, a document a person attached) hold each window to
`readWindowBytes(policy)` from `context/policy`: the result limit (`toolResultBytes`) less a
1,024-byte envelope for the reader's own header and members, counted in the window's JSON-encoded
text. `encodedPrefix(text, bytes)` cuts a text to that. A read so held reaches the model whole, as
one result; a result that still exceeds the limit is retained in the ledger and the model is shown
a reference and a preview, as before. The policy is the session's, `contextPolicy(configuration,
model)`.

The ledger offers its own reader, `read_window` (`context/reader.ts`), over every item it retains:
by line, or by page for an item whose `pages` are strings, with `offset`, `limit` and a `more` cursor
in each result. The reference envelope of a result over the limit names it (`readWith`). A host's readers keep the same argument and result shape (`ReadArguments`, `ReadResult`;
the contract's `tool-result.json`), and a host that retains an attached document in the ledger as
`{ pages: [...] }` gives the model a reader over it at no prompt cost until a part is read. A read
by line numbers its lines, each `<number><tab><line>` counted from 1 (line n is at offset n − 1), and
shows two hundred lines where the model names no limit (agent#5).

Beside it the ledger offers `search` (`context/search.ts`, agent#5): a regular expression over one
retained item, every item, or the attachments the host retained for the run (`context.documents`),
line by line; each match with its item's id and label, its line, offset and column, the page of a
paged item, and the line with two either side, numbered as `read_window` numbers them; as many as fit
in one result, at most `limit`, with a `more` cursor. The pattern is the subset RE2 accepts
(backreferences, lookaround, Unicode property classes and inline flags refused; at most 256
characters), compiled to a small program and run as a Pike VM (`context/pattern.ts`): every position
of a line is read once, so no pattern backtracks, and one search's work is bounded, stopping with a
cursor rather than holding the page. Compiling is bounded too (every node built and every repetition
counted), and each line is charged the program's size, so millions of short lines stop at the bound
as one long line does. `$` also matches just before a line's final carriage return, and
`ignoreCase` folds a letter through every case mapping it reaches, as `RegExp`'s `iu` does. An image
item is not searched; one that cannot be shown is named in the result's `notShown`.

**Images** (`images.ts`, agent#5). A tool result's `content` may carry an image part, `{ kind:
'image', mediaType, bytes }` (base64, PNG, JPEG or WebP, at most 16 MiB) or `{ kind: 'image',
mediaType, reference }` (an image the execution retains, by its attachment or ledger id), and an
attachment's value may be one; MCP's own image item is read as one too. A model whose capability
record says `vision` is shown it as an image beside its result; any other is shown the host's
rendering (`renderImageToText`) in its place. An image is never dropped in silence: one that cannot
be shown is a line in the result saying why. Its bytes are never part of the text the result limit
counts, so a result with an image stays within `toolResultBytes`. `read_window` reads an image item
whole, as the image (a reference followed to the image it names), or answers the line saying why it
cannot be shown. One request carries at most `imagesPerRequest` images and `imageBytesPerRequest`
bytes of them (8 and 16 MiB by default; `context/policy.ts`, `IMAGE_DEFAULTS`), the newest; an older
one is a line saying it is no longer shown. An endpoint that keeps a strict turn order off Anthropic
is shown the rendering where it takes native tool calls, and the image on the text path, where the
image rides in the result's own message. The `codex` engine's agent wire carries text, so it always
gets the rendering.

### The capability record

What one endpoint and model can do (`capabilities.ts`, the contract's `capability.json`; agent#4),
and the whole of what the `vercel` adapter reads where it decides, in place of the three families:
the window and output cap (`contextPolicy`), native tool calls, one call per turn, vision, the
reasoning kind and whether the endpoint ignores it (`thinking.ts`), the reasoning member a stream
uses and whether the endpoint answers whole (`engines/vercel/relay.ts`), the Gemini schema subset,
and where the execution state goes. The family stays the wire. A run's record is the family's
default (`FAMILY_CAPABILITIES`, the Desk's three families as records, which change nothing), then
the host's `endpointCapabilities` answer member by member, then `session.model.capabilities` where
the host puts its endpoint's own on the session; read closed, and refused where the reasoning kind
is not one the wire speaks.

An endpoint without native tool calls is sent none: its tools are described at the end of the
system text, the model calls one per answer as one fenced JSON block, `{"tool": name, "arguments":
{…}}`, read by the proposal fence's own closed reader (`extractToolCall` in `engines/contract.ts`),
and the call runs through the same dispatch and gate as a native one; a name the phase did not offer
is refused, never looked up. Its result goes back as the next user message, headed `TOOL RESULT from
<name>` or `TOOL ERROR from <name>`, a reading tool's answer framed as data there as it is natively
(`framing.ts`). On the request reserved for a final answer a call made in text is refused and never
run.

### The critic

The refutation pass (`refutation.ts`, agent#10) runs after a proposal exists and before it is
delivered, on every engine, where the critic's switch says so ([ADR-0010](../../docs/adr/0010-the-critics-switch.md),
`criticSwitch.ts`). It needs the runtime's `test_pack` prompt as `testPrompt`.

- **The switch.** The session's `critic` (`off`, `auto` or `on`, the person's word for the run), else
  `criticProject`, else the policy table's `criticDefault` (`contract/policy.json`: `auto`). Under
  `on` the critic runs on every proposal, under `off` never; under `auto` it runs on a proposal where
  the model called `request_review` this run, the host declared `criticCases`, or the host says the run
  works on an existing pack (`existingPack`). A reviewer assigned (`reviewerAssigned`), a
  graph-purpose run, Plan mode and a delegated child turn it off in every mode. `adversarialReview` is
  read for one release as an alias of `critic` (`true` is `on`, `false` is `off`). One decision for
  every engine (`criticDecision`, through the boundary's `decideCritic`); nothing is read from prose.
- **Its tier** is `criticReasoning` (a thinking tier on a model session, an effort on an agent
  session), else the author's (`criticReasoningFloor`: `author`); a lower one the host names is
  honoured. On the model engines it is the thinking members of the critic's requests; on `codex` the
  critic run's `effort`.
- **The record.** Runs reaching the critic decision point emit `critic_decided`; tracked runs checkpoint that
  decision. Earlier failures or pauses may omit it; a checkpointed review request survives a resume.
  A critic with no room left is recorded with the reason `budget`.
- **`request_review`** is offered to the author under `auto` only, after the context tools; the first
  call is recorded and answered, a second, or a call anywhere else, is refused with a `guardrail`
  whose reason is `review refused`. On `codex` with delegation one agent run is held back for the
  critic under `auto` and `on`, so a batch that would take it is refused.

- **Its tools** are an allow-list: `CRITIC_TOOLS` (`validate`, `experimental_evaluate`, a rehearsal on
  the wire, `get_schema` and `experimental_test_cases`, within its bounds and the turn's test-call
  cap, shared with the loop) and `CRITIC_READERS` (`read_window` and `search`, the harness's own
  context readers). It is offered those the session offers and no other host tool. While it runs,
  the `admit` on the shared boundary's wrapped `callTool` and host tools (`SessionContext.admit`)
  refuses any other name before the tool gate and before a host tool runs, with a `guardrail`
  `refused` event and a `permission` failure the model reads; each engine's own call of it is a
  redundant first check. On `codex` a name the session serves but the run was not offered is
  refused before any call, and the tool-free reformat run is held to nothing.
- **Its first message**, where it does not fit, is retained, and the critic reads it with
  `read_window` or `search`; where it does not fit even as that preview, the pass is refused with a
  `critic-unfinished` note.
- **Its comparison.** A host passes the cases whose outcomes it knows as `criticCases`: the pack's
  saved tests (`source: 'test'`) and cases the request declares (`source: 'declared'`), each a row of
  the runtime's matrix (`CriticCase`; read closed by `readCriticCases`, and a case it refuses ends the
  run before any request). The critic is shown the cases that state a whole §8.3 disposition or an
  error class as that matrix (`testable`), and the cases that state part of one apart, for rehearsal
  only. Each rehearsal of the proposed document with a case's inputs (the sets `reasons` and
  `handoff.triggeredBy` in any order), and each test-run row of a whole case sent as declared, is
  compared with the declared outcome. The cases the critic's own calls left uncompared the harness
  runs once each after its turns, through the same gate, marked `origin: 'harness'` and counted in
  the figures (one test call over the whole ones where the critic is offered the test tool, then up
  to `CRITIC_REHEARSALS` rehearsals; no model request; on `codex` within the pass's ten minutes,
  `CRITIC_PASS_MILLISECONDS`). A disagreeing case refutes the proposal, and so does a declared case
  nothing compared: the critique's `cases` and `incomplete` say which, and the proposal's `critique`
  names the incomplete ones.
- **Its provider and model** come from `models.critic.provider` and the selected host binding.
  The provider defaults to `same`; `other` requires a connected different family. On the author's
  connection, the `critic` member of `modelsFor` still names a subordinate model, asked once per
  run, or the run's model. The critique records the answering connection and family.
- **Its cost** is on the critique, and on a `critic-unfinished` note: `figures`, the model and the
  requests (model requests and their token counts on `vercel`, agent runs on `codex`), and the
  harness's own calls (`harnessCalls`).

### What changes between requests

The `vercel` adapter sends a stable prefix: its system text with the execution instructions, the
tools, the host's prompt and the history. What changes between requests is one text headed
`STATE_HEADER` (`engines/contract.ts`): the request counters, the plan and the operations index,
the step's own instruction, and the host's own changing text, which a host passes as
`context.currentState` (a chat's draft snapshot, the latest check) rather than inside `prompt`. On
the first request it is a second text part of the person's message, on every wire. After that it
goes where the endpoint's placement says: `trailing-user`, one user message after the history (the
default), or `system-tail`, the end of the system text (for an endpoint marked `strictAlternation`,
and by default for Anthropic when a thinking tier is on). A host sets both through
`endpointCapabilities`; they are two members of the capability record. On the Anthropic wire the adapter
marks cache breakpoints after the system head and after the tools and, under `trailing-user` only, after
the request text (the first text part of the first user turn): three under `trailing-user`, two under
`system-tail`, where the state sits before the messages and the bytes through the request text grow on
every request. The tier is held for the run, changed only where the endpoint refused it
or a thinking signature came back truncated. The provider's cache reads and writes are recorded with
each completion (`cacheReadTokens`, `cacheWriteTokens`). The contract states the header and where it
sits on each wire (`budget.json`, `contract/vectors/budget/state-placement.json`).

On the agent wire the `codex` engine makes one request with one prompt string, and the same header
heads what it appends (agent#36): the person's message, then after a blank line `STATE_HEADER`, the
host's `context.currentState` and, in a conversation, the execution guidance, then a chat's index of
earlier results. A run that appends neither state nor guidance has no header. `STATE_HEADER` is one
string for both adapters, defined in `engines/contract.ts`.

### Across the turns of a chat

When history reaches its allowance, `context/session.ts` follows ADR-0016. The
policy's default `compaction: index` preserves the existing deterministic turn
index byte for byte. `summary` keeps `recentMessages` raw and asks the
`models.compactor` connection at its resolved tier (`low` by default) for prose
bounded by `summaryBytes`. Tool results stay in the index; the prose, index and
replaced-turn digests are framed as data. `compacted` exposes the replacement,
and a failed compactor emits `compaction refused` and continues with the index.
The checkpoint stores the exact framed replacement for byte-identical resume.
Neither mode changes the stable prefix.

A run's context ledger and read cache are its own and end with it, unless the host hands it a
chat-scoped context (`context/chat.ts`, agent#8). The host makes one per chat and passes it to every
run of that chat:

```ts
import { ChatContext } from '@judgment-pack/harness/contract'

const chat = new ChatContext()                 // once, when the chat opens
session.context = { ...session.context, chat, runtime: { identity: runtimeDigest, version: runtimeVersion } }
// …every run of the chat…
chat.dispose()                                 // when the chat is deleted or closed, after its run has ended
```

The chat's ledger keeps what each run retained, and every answer of a session tool that reads, so a
later run's `read_window`, `read_context` and `search` find it. A read the tool declares immutable is
served again from the chat's read cache without a call, keyed by the tool, its arguments and, for a
Runtime-served read (every runtime tool, and a host tool that declares `contextPolicy.runtime`), the
`context.runtime` the host names; without that, such a read is cached for the run only. A mutable
read never is, and the cache serves only a name the run's session offers: a delegated child is never
served a read its profile was not offered. `retainedBytes` bounds the chat: the oldest entries no running run references are
evicted first. Each run's first message carries, after the request text, an index of the results
earlier runs kept (`read_window` id, tool, arguments digest, size, first line), bounded by
`turnIndexEntries` and `turnIndexBytes` and cut to the room the message leaves in the prompt's
allowance, at most a quarter of it, so the request is never previewed and a run never fails because
of the index; results evicted since the last index was sent are listed there. A run's checkpoint carries the entries it retained or read, not the
chat's. Nothing is global: a run reaches only the chat context it was handed, and a delegated child
gets a view of its parent's that shows it its references and what it reads itself (see Delegation).
`wire-contract.md` has the rules; `tool-result.json` the index and the cache key.

### Tool sources (ADR-0006)

A tool from a gateway connection, a public MCP server or the person is a host tool whose declaration
carries `source` (`ToolSource`: `kind` `gateway`, `mcp` or `custom`, `id`, `trust`, and `version` or,
for an MCP server, the `toolListDigest` the person approved, with `currentDigest()` to check the
list it serves now). ADR-0017 adds `identity` (`person`, `service` or `connection`, with a
recognisable label that is never a credential) and `currentIdentity()`; absent identity defaults to
the connection labelled by the source id. The offer says `runs as: <label>`, every call and result
carries the pinned identity, and source figures count calls per identity. A changed identity is
withheld until re-approval. The first `act` under a service identity is asked once per run even when
the policy says `allow`, with an `approval downgraded` guardrail; model arguments never choose the
identity. A declaration optionally carries `effectClass` (`read`, `propose`, `act`; `act` where absent),
`approval` (`none`, `required`; required for `act` where absent) and `approve({name, tool, source, args})`, the
host's approval policy (ADR-0008), handed the wire name, the tool's own name, its source record and a copy of the
arguments and answering at once `'allow'`, `'refuse'` or `'ask'` (no policy asks); on `'ask'` the run pauses
(`approval_requested`, the call in the checkpoint's `approval`, `end`) and the host continues the paused checkpoint
with the person's decision, `context.approval` (`{callId, approved, note?}`), the continued run's first request
carrying the result as one data block;
`execute(args, signal, callId)` is handed the id of the call's events, where the run's events carry one. The host executes every call; `toolSources.ts` holds them at the boundary
(`contextualSession`): each is offered under its wire name (`wireName`: the source's id, `__`, the
name made wire-safe, a hash suffix where a character was replaced or the name cut), its description
cut at `toolDescriptionBytes` and framed with the run's marker, as is each description inside its
input schema (only the structural keywords are offered, every other is not, and a name over 64 bytes
or with white space and an `enum` or `const` with a string over 64 bytes are dropped, with a guardrail); past `offeredSchemas` or
`offeredSchemaBytes` the largest sources become catalogues that `load_tool` loads from, the loaded
schemas riding in the execution state after the history, the catalogue itself held to `cataloguedTools`
and `catalogueBytes` (the tools past them are withheld, with a guardrail); and each call is refused where the
source's list changed, the tool is catalogued and not loaded or its arguments do not match the schema it
loaded, the source has made
`sourceCallsPerTurn` calls (the slot is claimed at the check, so concurrent calls cannot pass it, and a
delegated child's calls count against the same turn), or an act has no approval, held for the review card where it is a
proposal (`tool_result` `proposed`), and otherwise run under `sourceCallMilliseconds`, its result
framed and shown whole up to `sourceResultBytes`. The events carry `source`; the checkpoint's
`sources` counts each source. `toolListDigest` is exported for a host pinning a server's list.

### Skills (ADR-0009)

A host declares skills on `session.skills` (`SkillDeclaration`: `id`, `name`, `description`, `source`
with `kind` `gateway`, `mcp`, `custom` or `exchange`, `instructions`, the host tools it names in `tools`,
`resources`, and `pinnedDigest`, the digest of the bundle the person approved, which `skillDigest`
computes). `skills.ts` holds them at the boundary (`contextualSession`): a skill naming a tool the host
did not declare, over `skillBytes`, or whose bundle does not hash to its pinned digest is withheld with
a guardrail (`skill withheld`); the admitted ones are listed at the end of the system text, each name
with its description's first line framed as data, held to `cataloguedSkills` and
`skillsCatalogueBytes`; `load_skill` and `unload_skill` are offered beside the context tools. A load is
counted against `skillsPerRun` (a load past it refused, `skill budget`) and the loaded skills ride
after the history in the execution state as one block headed at the rank of the project's own
instructions, so the tools and the system text never change; on the agent wire a load's answer
carries the block. `skill_loaded` and `skill_unloaded` are on the stream, the checkpoint's `skills`
keeps the loaded set (a continued run restores it, saying what it did not restore) and its `sources`
count each source's loads. A skill grants no tool; a delegated child is handed none.

### The prompt (ADR-0005)

The adapter assembles the whole prompt, and the host's relay adds nothing to it
([ADR-0005](../../docs/adr/0005-the-prompt-is-the-adapters.md)). Every engine builds the same
layered request with one assembler (`prompt.ts`, `systemText`): the system text (the agent wire's
`instructions`) is the package's guidance, then the project brief, the project's own instructions
and the memory, each asked of the host once per run before its first request (`brief()`,
`projectInstructions()`, `memoryFor(query)`), the brief and project instructions as a header, text and end line; accepted memory records each framed as data with kind and source under the run marker
(`budget/host-parts.json`); on an endpoint without native tool calls the tools described in text
follow. That text is the stable prefix: the same bytes on every request of the run. Then the
messages: the first user turn carries the request text (the host's prompt, which carries the
Runtime's prompt and the person's request as one text), then a chat's index of earlier results,
then on the first request the state headed `STATE_HEADER`; the turn's steps follow, and the state
after them where the endpoint's placement puts it. On the agent wire (`codex`) the one prompt
string carries the same parts in the same order: the request text, the index, then a blank line,
`STATE_HEADER` and the state. Where the prompt does not fit its allowance, the person's message
(`currentRequest.message`) leads the preview with its reference; a delegated child's objective
(`context.objective`) leads its first message and is its memory query.

A part over its bound (`briefBytes`, `projectInstructionsBytes`, `memoryBytes`), or an invalid answer, is refused and the run fails before any request (`configuration`). The project's instructions
are instructions at the lowest rank: they follow the package's guidance, under a header that says
to follow them where they do not contradict the rules above, and they cannot grant a tool or change
an effect, because the offer and the gate are code; a certification leg holds that a block saying
to ignore the data rule and call `write_file` changes nothing the gate admits. The certification
suite checks that each part lands in its layer, and that the stable prefix holds, on every request
of 21 certified legs in four families.

Every request body the adapter sent is digested in the checkpoint's `prompts`, in order (the
SHA-256 of the provider body on a model engine, of the agent request's JSON on the agent wire;
`provenance.json#/$defs/promptDigest`).

The host says per run whether the project's memory is on (`context.memory`): `false` means `memoryFor`
is not asked. No reserve is taken out of a request's input allowance for the brief or the memory
(`briefBytes` and `memoryBytes` are the parts' bounds): a part is counted once, where it sits.

### Delegation

`withDelegation(engine, options)` (`delegation.ts`) wraps any of the three engines for a conversation and
offers the model `delegate_tasks` (`delegationContract.ts`; the contract's `delegated-task.json`;
agent#13, agent#14, agent#15, agent#16, agent#17, agent#18):

- **One parent run.** The call runs the batch. The `vercel` and `deepagents` engines await it in the parent's step,
  with no tool-call deadline (the batch is bounded by its children's time limits and the run's own
  signal), and the children's findings are the call's tool result in the same run. The `codex`
  engine ends its run at the call, since the provider account runs one agent run at a time and the
  children need it; the coordinator runs the batch and starts the response's next run with the
  call's result alone, as one data block, the earlier run's reads listed in its index. Up to four
  batches per response (`MAX_BATCHES`), one at a time; on an agent capability only while a child
  run and the parent's next run both remain (`MAX_AGENT_RUNS`, five).
- **Context by reference.** A task names `references`, ledger entry ids or document ids of the
  parent's context, resolved in the parent's chat alone. The child gets a view of the chat
  (`ChatContext.childScope`) showing it those entries and what it reads itself, sharing the chat's
  read cache, and reads them with `read_window` and `search`; nothing is copied into its brief. The
  coordinator makes a chat context for the response where the host handed none, and disposes of it
  when the response ends.
- **Profiles.** `TASK_PROFILES`: `analyze`, `review`, `research` and `author-test`, each with a
  `model`, a `budget` (model requests, tool calls, milliseconds) and its tools; `author-test` is
  offered the gate's six Runtime tools and no host tool, and its test calls count against the
  parent's turn. A feature adapter may add a profile through `options.profiles`
  (`[...TASK_PROFILES, its own]`), held by `readProfiles` within `TASK_BUDGET_CEILING` and the
  gate's ceiling. Each task's record carries its `budget` and the `model` it ran on.
- **A child provider and model.** `models.child.<profile>.provider` (or a declared
  `models.subagent.<id>.provider`) selects the host binding, defaulting to `same`. Another family
  is allowed; its findings return as data and every task event records its connection and family.
  On the same connection, a host names a cheaper model for children of a connection with `modelsFor`
  (see Host injection points), asked once per run with the run's connection (`context.connection`);
  a child whose profile asks for the parent's model (`author-test`) stays on it. On `vercel` the
  host answers `endpointCapabilities` for the child model as for any other, and its relay must admit
  it; on `codex` the child's agent run carries it (`AgentRequest.model`), and the bridge must admit
  it on a child's run as it does on a critic run.
- **Shared limits.** The parent's execution owns the response's model requests once its run starts
  (`ContextInput.link`), a child's are charged to it, and the last is kept for the parent. Children
  running at once share what is left by grants, so each finishes within its own; a finished child's
  rest goes to the next. A child's call of a name it was not offered is refused before it counts. Twenty
  host and Runtime tool calls per response, the parent's and its children's together, stated in
  the instructions and in the execution state as `toolCalls`.
- **Long findings and resume.** Findings past a task's share of the answer are retained whole in the
  chat's ledger and the parent gets their head and a `read_window` cursor (`more`); a task may ask
  to `summarize` them in one request of its budget. A complete task's context is kept in the chat
  under its task id, and a later batch resumes it with `resume`.
- **Reports.** Each profile names its report shape (`profile.report`): a summary, findings,
  citations, open questions, a confidence and what was not done, with a review finding's severity,
  a research finding's sources and an author-test finding's check and status. The child ends its
  answer with the report as one fenced `json` block (`reportInstructions`); the coordinator reads it
  closed (`readReport`), or writes one itself (`confidence` `unstated`) where the block is missing
  or breaks the shape. The parent's answer gives the report first, held to the task's share by
  whole items (`fitReport`), then the text.
- **Open questions, not failures.** A child's `user_question`, a proposal it wrote (its document
  kept in the chat's ledger, never applied or shown for review), a delegation it asked for, or a
  research source call that needs the parent's research choice comes back as an open question in
  its report, and the task is complete (the research hand-back as a partial result). A run that
  failed fails its task, its open questions kept on the record. A batch that returned one earns the
  response one batch past `MAX_BATCHES`, once.
- **Streamed progress.** A running child's prose reaches the host as `message_progress` with the
  task's `taskId`, cleared when the child ends. A host shows it under the task, never as the answer.
- **Partial results and one retry.** A child stopped at its time limit returns what it had written,
  `partial`. On a model capability, a child's request refused with a 5xx other than 504 carrying
  the catalogue's retryable class or overload code (a 503 `overloaded_error`) is sent once more,
  only before the first byte of the child's first answer's body (`childRetry`; the record's
  `retried`).
- **Capacity.** A batch runs `concurrentRequests` children at once: the host's `modelsFor` answer
  for the run's connection (`context.connection`) carries it beside the critic's and the children's
  models, two where it names none; an agent capability runs one. A connection setting, so not on
  the capability record, which is keyed on the family and model.
- **Reuse.** A complete result of a child that made no counted tool call is kept in the chat under
  its profile, its model, its objective's text and its references, once its batch's answer is
  delivered; an identical task of the same chat, or later in the same batch, gets it again with no
  model request, `reused` naming the earlier task. A host starts a response with
  `context.delegationReuse: false` to reuse nothing earlier (a Retry asked to run afresh).
- **Cost and time.** Each child's record carries its tokens (from the provider's usage figures),
  `firstByteMs` and `durationMs` beside its model, requests and tool calls; the parent's checkpoint
  carries each batch's figures (the running one too), its total and the parent's own, counted where
  the parent makes each request or run (`delegation`, `validDelegationFigures`), kept through a
  resume.
- **Kept:** `options.checkpoint` before each task transition, the durable checkpoint of the shared
  count before each child request, and cancellation with the parent.

The Desk's side: the task view (a task's report, its open questions, `partial`, `reused`, its
streamed prose by `taskId`), the run report's lines for each child and batch (desk#499), the
connection's capacity setting, answered as `concurrentRequests` in `modelsFor`, and
`delegationReuse: false` on a Retry the person asks to run afresh.

## Certification

```sh
npm ci
npm run certify -- --adapter vercel
npm run certify -- --adapter codex
npm run certify -- --adapter deepagents
```

The suite is the Desk's conformance session and its certification checks, moved whole: the recorded
bake-off scenario on a scripted model and a recorded runtime, the relay-only rule (K1), the
scenario's five-tool offer (K2), the rehearsal rewrite (K3a), the refused write (K3b), the proposal
(K3c), event order, and the network and timer seal shown to fail on the four fixtures in
`certification/certification/`. One check is added (agent#7), and every adapter runs it: the
ceiling is exactly six names, and `test_conformance` is refused at the wire even where a runtime
serves it and a file grants it.

The checks, by adapter. `deepagents` is a model engine and is held to every check in the `vercel`
column, on the same legs:

| Check | `vercel` (model engine) | `codex` (agent engine) |
| --- | --- | --- |
| The ceiling is six names; `test_conformance` refused at the wire | `certification/conformance.test.ts` | the same |
| The seal: no network global touched, at load, during or after (K1a) | each wire leg | the scenario leg |
| No credential, nothing but the relay (K1) | each wire leg | not applicable: no model capability |
| K2: the offer is `tool-offer/scenario-offer.json`, both members | each wire leg, every request | since agent#25: the agent-wire offer read back by real name; the critic's is `tool-offer/critic-offer.json` |
| K3a: the evaluate reaches the runtime as a rehearsal, reported | each wire leg | the scenario leg |
| K3b: `write_file` reaches the gate, is refused there and never reaches the runtime, and the model is told | each wire leg (T7) | since agent#25: T7, a call of a tool the agent was never offered |
| K3c: the proposal is DRAFT_V2 with T8's unknowns | each wire leg | the scenario leg |
| The event order and one `end` | each wire leg, exactly | a subset |
| The refutation pass: the verdict is the runtime's, a refuted draft, a critic refused at the wire | each wire leg, asked for (agent#10) | the verdict, on the scenario leg, asked for |
| A seeded wrong outcome is refuted and the case named (agent#10) | each wire leg | the scenario leg |
| No critic where nothing asks for one: the table's `auto` with no trigger, at any tier (agent#10; ADR-0010) | each wire leg | `critic.test.ts`, outside certification |
| Thinking, no-thinking, strict alternation, text tool calls | each wire leg | not applicable |

So a gate that admits a name the session did not grant (the sixth-tool mutant) fails both
certifications. It reads the contract's vectors where the contract states what it
asserts (the scenario's event sequence and K2's offer, from `contract/vectors/`) and the recorded
scenario otherwise. Each adapter runs the suite's own checks of the scenario and the seal, then the
checks the Desk runs over every engine of its kind: a model engine (`vercel`) the conformance legs,
six wire legs plus the thinking and refutation legs, and, added here (agent#9), the strict-alternation
leg: an endpoint that answers 400 to two user turns in a row or a user text after a tool result,
which the loop passes with the state in the system tail; and (agent#4) the text tool calls legs: the
whole scenario, on each of the six wire legs, at an endpoint whose record says it takes no native
tool calls and which refuses any request that carries tools, with the same calls, gate events and
proposal, and once more where it also keeps a strict turn order; and (ADR-0005) on every request
of 21 certified legs in four families (plain, text-path, strict-alternation and thinking), each of
the host's parts in its layer and the stable prefix the same bytes, and on the six plain legs a
project block that asks for `write_file` refused at the gate as before; an agent engine (`codex`)
the recorded scenario behind the real tool gate and the agent-engine checks in
`engines/codex/codex.test.ts`, among them (ADR-0005) the host's parts at the end of the
instructions and the request text before the state in the prompt.

### The recorded runtime and its recorder (agent#37)

`certification/runtime.json` is the Runtime's own answers to the scenario's scripted calls, and
its `recorder` member names `scripts/record-conformance.mjs`, which is in this repository. It
drives a `jpack mcp` over stdio, asks no model anything, and writes the file; `verify` records to
memory and compares byte for byte:

```sh
npm run conformance:verify --workspace @judgment-pack/harness -- <jpack binary> <project dir>
npm run conformance:record --workspace @judgment-pack/harness -- <jpack binary> <project dir>
```

The project directory must declare an audit trail, as the fixture's `runtime.project` says. The
fixture names the binary it was recorded from by SHA-256, so a binary with another digest records a
file that differs in that member, and `verify` says so. The recorded binary is not kept in this
repository: `certification/recorder.test.ts` runs the recorder over a stand-in that answers from
the fixture (what it writes is the committed file in every member but the binary's digest, `verify`
of a file it wrote is a match, and a hand edit of one answer is not), and `conformance.test.ts`
holds that the file the fixture names exists and is the one the package's scripts run. Re-recording
against the recorded runtime is a command a person runs with that binary.

### The mutation check (agent#37)

`scripts/mutation-rows.json` is the table of rows, one deliberate break of one safeguard each (the
file, the text found once and the text put in its place, the test files that catch it, where the
row came from, and `caught` or `retired`). `scripts/mutation-check.mjs` applies one row at a time to
a scratch copy of the package, runs the row's tests, and fails on a row no test fails under:

```sh
npm run mutation --workspace @judgment-pack/harness -- --list
npm run mutation --workspace @judgment-pack/harness -- --check            # every row still applies; no test run
npm run mutation --workspace @judgment-pack/harness -- --row "any relay 401"
npm run mutation --workspace @judgment-pack/harness -- --changed origin/main --sample 15
npm run mutation --workspace @judgment-pack/harness -- --shard 1/4         # the full table, in four parts
```

The first 111 rows are the ones the Desk's `mutation-check.sh` held over the files that moved here,
carried by the swap (judgment-pack-desk#516); the rest are this repository's own (agent#36 and
agent#38 each add one, ADR-0003 five for the Deep Agents adapter: its boundary, its built-ins
off, its question pause, its tracing refusal and its critic dispatch; the review of #44 and the
release review of v0.3.0 add more, for the chat's cache offer, the codex prompt's message head, the
critic's allow-list, the tracing variables, and a call on a request with its tools closed; ADR-0005
four, for a host part dropped, the project block above the package's guidance, the prompt's digest
omitted and memory recalled per request, and re-anchors the codex prompt's message head on the
preview every engine builds). Nine of the Desk's needles no longer matched the moved code: eight were
re-anchored on the same safeguard (a row's `note` says why), and one row is retired, with its
reason in the table, because the code it broke is gone. A row's `tests` are the files that
import the mutated file or a module that does, and those that name it in a string; a run in which
nothing fails is run again against the whole suite, which decides. `.github/workflows/mutation.yml`
runs the rows over the files a pull request changes (at most fifteen) and five more by the pull
request's number, and the whole table weekly, in four shards, and on demand.

### The certified scenarios (agent#13)

Seven more scripted scenarios run beside the bake-off's, on every adapter, by the same command. Each
is written by hand in `certification/scenarios/<name>.json` as `scenario.json` is: the model's
turns are listed in order, and nothing in them was recorded from a model. The model engine runs
each on the OpenAI-compatible wire, once with each answer shape (a stream, a whole object), through
the scripted endpoint; the agent engine runs each at its own seam (`AgentRun`), as
`engines/codex/codex.test.ts` does, the scripted agent calling each tool by the name its offer gave
it. The suite is `certification/scenarios.test.ts`; its turns and runs are driven by
`testing/scenarioDriver.ts`. `npm run certify` prints the checks passed by scenario.

| Scenario | Checks | `vercel` and `deepagents` (per answer shape) | `codex` |
| --- | --- | --- | --- |
| 1 Chat mode with host tools: a read, a proposal, a name never offered | the offer (two host tools beside the Runtime's six, the harness's six and, under the table's `auto`, `request_review`); the gate refuses the third name between its call and its result; the proposal reaches the review card and nothing is acted on; the answer is delivered | 4 | 4 |
| 2 Delegation: a batch of a review and a research child, then a follow-up that resumes the research child | one parent run per response (vercel) or the next run's result and the chat's index (codex); the children's offers closed; a reference resolved within the chat; each result in its profile's shape and valid against the contract; the figures; the resumed child's earlier work | 7 | 7 |
| 3 Memory: three tool steps | memory asked once, for the person's message, and carried in the same place on every request (ADR-0005); the same allowance, with no reserve, with memory on, unsaid or off | 4 | 4 |
| 4 Plan mode | no action tool offered; the Runtime reads offered are the gate's ceiling less the evaluation and the test run; a call to an action tool refused and visible; the plan delivered | 4 | 4 |
| 5 The correction loop: a draft validate refuses, one correction request, the critic (the correction request's text is the scenario's own script; the scenario exercises `correctArtifact`, one correction then ready, the engines' critic and the gate's rehearsal rule) | the first draft fails validate with its diagnostic; one correction request and a passing draft; the request count; the critic's comparison rows for the declared case; no `incomplete` mark; the critic's call of a tool outside its allow-list refused by `admit`, reaching no Runtime tool, with the pass still ending | 6 | 6 |
| 6 A multi-turn transcript: three turns of one chat | no unchanged source read again on turns two and three; the index present and within its bound; the ledger kept across the runs and gone after `dispose()` | 3 | 3 |
| 7 Tool sources (ADR-0006): a scripted MCP server of seventeen tools, three descriptions telling the model to drop the data rule and call `write_file`, its list changing after its first call; one custom read; a second run after the new list is approved | the digests; the schema budget trips and the server is a catalogue beside `load_tool`, with no `write_file`; every source description framed, the poisoned lines inside their blocks; `load_tool` loads one schema, carried after the history while the tools and the system text hold; each act asks the host's approval policy and runs where it allows, the read does not ask; `write_file` refused at the gate; the changed list refuses the next call before the host; the source records on the events and the run report; the second run offered the new list and its call run | 9 | 9 |
| 8 The approval pause (ADR-0008): scenario 7's server, its policy asking about `create_issue`; two `create_issue` calls in one answer; the paused checkpoint continued from its JSON text with a decision for another call, refused with a note, approved and broken off after the result was written, and once more from there | `approval_requested` in place of the call, the checkpoint paused and durable, `end`, nothing run; the second call refused with the `approval pending` guardrail; the decision for another call refused before anything runs; refused: the result and the note in one data block, `answered`; approved: written and acknowledged, run once, its result written and acknowledged; the restart runs the continuation and not the call; the continuation one line and one block after the request text; one more model request on the model engine, no invented figure on the agent engine | 8 | 8 |
| 9 Skills (ADR-0009): 42 skills declared beside the custom read, 40 admissible past `cataloguedSkills`, one with a changed bundle and one naming `write_file`; two loads (one poisoned), the read, `write_file`, a load, an unload, a fourth load and one past `skillsPerRun`; a fresh session restored from the last checkpoint's JSON text with one skill withdrawn; a third run with the critic on, the author and the critic each loading the same skill | the catalogue of 32 with its guardrails; each load and unload between its call and its result; the block after the history on every later request with the tool list and the system text the same bytes (the agent engine: in `load_skill`'s answer); the load past the budget refused; the poisoned skill held off; the loaded set and each source's loads; the restore and the withdrawn skill's guardrail; the critic sees the loaded skill (its step note on the model engines, the block in its `load_skill` answer on the agent engine) | 8 | 8 |
| 10 The critic's switch (ADR-0010): the runs of `critic-switch.json`, in order: `auto` with declared cases, a conversation turn, an existing pack and a new one, the model's ask (twice), an ask under `off`, `on`, a reviewer under `on`, the critic at the author's tier and lowered, a run broken off at the ask and resumed, and the measurement's four runs; on the agent engine three delegating responses | `cases` before the critique with the durable checkpoint carrying it; `no-proposal`; `changes-existing`, and `table` with no critic; `model-asked`, the second ask refused with `review refused`, the critic never offered it; the ask under `off` refused, no critic; `person`; `reviewer-assigned`; the tier in the decision and on the critic's requests or run; the resume's `model-asked` and its own ask refused; the measurement's table; on the agent engine a batch of three refused under `auto`, two proceeding with the critic as the fifth run, three proceeding under `off` | 10 | 11 |
| 19 Identity per tool call (ADR-0017): a gateway runs as a service, changes identity, and offers an act whose policy says allow | the framed identity line; the host identity on every event despite a model argument; per-identity figures; changed identity withheld; first service act asked with the downgrade recorded | 5 | 5 |

The checks the adapters make differently are recorded here, with the reason, and not skipped:

- **1** The agent transport refuses a name outside the agent's offer before the gate sees it (the run ends), so the agent engine is driven at `AgentRun`, where the gate refuses it and the agent's answer is the refusal (agent#25). On the `vercel` model engine the SDK answers the unoffered call first, with its own no-such-tool error naming the tool, and the model reads that; on the `deepagents` model engine the adapter's boundary answers every call before LangChain's tool node, so the model reads the gate's refusal, as the agent's does (ADR-0003); the stream shows the gate's refusal on all three.
- **2** The agent's run ends at the delegate call, so a response is the call's run, the children's runs and the next run: four runs for a batch of two, three for the resumed child. The agent wire reports no usage and no model request, so a child's record carries no `tokens` and no model requests, and the batch's figures count `agentRuns` instead; its `firstByteMs` and `durationMs` are present.
- **3** The agent engine makes one request, so "every request" is its one run's: the memory at the end of its instructions, the person's message opening its prompt. The allowance checks are the same on both.
- **5** The model engine's loop is five model requests (two authoring requests, the critic's three); the agent engine's is three agent runs (author, author, critic). The critic's boundary check runs the loop once more with a call of `write_file` first in the critic's script, on both adapters; the guardrail's detail, "is not one of the tools the critic may call", is what shows `admit` refused it.
- **4** and **6** make the same checks on both. The index is a text part of the first message on the model engine and the end of the prompt on the agent engine.
- **7** The agent wire sends the execution state once per run, so on the agent engine a loaded schema reaches the agent in `load_tool`'s answer only; the model engines carry it after the history on every later request.
- **10** The model engines carry the critic's tier as its requests' thinking members (on the OpenAI-compatible wire `reasoning_effort`, none at `off`); the agent engine as the critic run's `effort`. The invocation held back for the critic is the agent engine's alone, so its eleventh check is the delegating responses. `npm run certify` prints the measurement's rows as a table after the counts.
- **9** The same holds for a loaded skill: on the agent engine its block reaches the agent in `load_skill`'s answer, and the restored run's state carries the loaded set. The critic's agent run is sent no loaded set, so its `load_skill` of a skill the author loaded is answered with the block; the model engines' critic step note carries the loaded set. An unload drops the block from the later requests on the model engines; on the agent engine a later run's state drops it, and the current run keeps the answer it was given.

Not covered by these scenarios: the network seal (the recorded scenario's legs hold it) and the other two wires (the recorded scenario's legs cover them). A model engine is added by registering it, as `deepagents` was, and then runs every scenario.

### The Deep Agents adapter (ADR-0003)

`engines/deepagents` runs the turn on LangChain's Deep Agents (`deepagents@1.14.1`, through its
browser entry, `deepagents/browser`), with the same session, the same seams and the same
certification as `vercel`:

- **Its model is the relay's.** `model.ts` is a LangChain chat model whose only reach is
  `session.model.call`, wrapped in the shared retry rule, and a run whose environment turns
  LangChain's LangSmith tracer on (its other way out) is refused before it starts; `wire.ts` writes and reads the three wires
  itself, because LangChain's provider classes cannot carry what the contract holds a model engine
  to (the Gemini one cannot route through the relay at all; the ADR lists what was measured).
- **The gate is its only tool boundary.** Deep Agents' own middleware (its file tools, the `task`
  subagent tool, summarization, the tool-call patcher) is replaced by name with middleware that does
  nothing. The tools it is given are declarations; every call is answered by its boundary middleware
  (`wrapToolCall`), through the gate, with the contract's events, a name outside the phase's offer
  refused there. Its `wrapModelCall` hook asks the package's context for each request's budget and
  compaction, places the execution state and records the completion.
- **The package keeps the rest.** Delegation is the package's (`withDelegation`), the critic, the
  reformat request and the turn clock are the contract's, and `interruptOn`, `checkpointer`,
  `permissions` and Deep Agents' subagents, memory and skills are not used; the ADR says why for
  each.
- **It builds for the browser**; the chunks only it reaches add 1,308.0 kB minified, 367.6 kB gzipped,
  beside the 212.8 kB it shares with `vercel` (`npm run bundle:size` builds the two engines as two
  entries and prints these figures; ADR-0003, decision 6, gives the settings).

Its own checks are `engines/deepagents/deepagents.test.ts`: the offer is the gate's alone, a
built-in name is refused at the boundary on both tool-call paths, the question pause, a relay that
refuses the bearer fails closed, unreadable arguments are never run, and the browser entry.

Tests and certification run on `testing/host.ts`, a host that supplies each injection point the way
the Desk's own test ground does: a session already in hand, a relay addressed by path, no socket.

## Tests

`npm test` runs every moved test and the suite (vitest, jsdom, as the Desk runs them), including
`session.test.ts`, whose session store and language come from the test host. The cases that
exercise a Desk feature stay in the Desk and run there against this package after its swap: the
chat checkpoint round trip (`execution/execution.test.ts`), the plan store and plan card
(`planning/planning.test.tsx`, three cases), the memory settings screen (`memory/memory.test.tsx`,
two cases), the response history (`delegation.test.ts`, one case) and the dialect chat tools
(`engines/codex/codex.test.ts`, one case).

Two whole files stay in the Desk and **cannot run against this package as written**: the
architecture tripwires `enforcement.test.ts` and `context/coverage.test.ts` read the harness by
source path (`assistant/engine.ts`, `session.ts`, `engines/**`, `failures.ts`), and one expects the
catalogue import string `internal/aifailure/catalog.json`, which is `./contract/catalog.json` here.
The package publishes no TypeScript source, so they need porting in the swap (#22).

### Declared sub-agents

Hosts may declare pinned `subagents[]` beside tools and skills (ADR-0013). The
`delegate_tasks` catalogue lists their ids and framed purpose lines beside the
unchanged `analyze`, `review`, `research` and `author-test` profiles. A definition
admits only declared read tools and skills, narrowing bounds and an approved digest.
The child uses `models.subagent.<id>`, its own skill-load budget and the common
prompt assembler; it has no actions, delegation, critic or review request.
Events, checkpoint tasks and report figures identify the definition's id, digest
and source. Withdrawn or changed definitions in restored batches are reported
without dispatch. The host owns storage and re-approval.

Memory proposals ([ADR-0014](../../docs/adr/0014-memory-the-write-side.md)) use the reserved
`remember {kind, text}` tool. Only the author may propose, including Plan mode. The defaults are
1,000 UTF-8 text bytes and four proposals per run; prior proposals count after resume. The package
emits `memory_proposed`, records `checkpoint.memories.proposed`, and stores nothing. The host
accepts, declines, revises, revokes and stores. `memoryFor` returns `Memory[]`; the text alias lasts
for v0.8.0. Only accepted, unexpired records are framed. `memory_recalled` and the checkpoint's
recalled rows record ids/digests and kinds; a revoked record returned after resume updates the
report but never enters the prompt. Scenario 14 certifies the write side on every engine.

### Proposal edits

[ADR-0015](../../docs/adr/0015-proposal-edits.md) (#110): the package owns drafts addressed as
`draft:<document>` by `search` and `read_window`. The author uses `propose_edit`,
`propose_delete`, `propose_insert` and `undo_edit`; each unique-anchor change is
validated before replacement and bounded by the policy table. The proposal remains
a whole document with its edit provenance. Drafts and undo history survive
checkpoint resume, and ride in execution state so the stable prefix is unchanged.
Children and the critic never edit. Hosts supply accepted documents or the first
`propose_pack` opens the draft; no host file is written.

Proposal edit refusals omit `document` when the identifier is missing, non-string
or empty; nonempty identifiers are truncated to 512 Unicode characters. The
`reason` names an invalid identifier. This makes malformed calls schema-valid
refusal events. Interrupted and waiting turns deliver every draft with its edits.
Both languages execute the shared calls under `contract/vectors/edits/`, including
successful deletion/insertion and malformed-identifier cases.
