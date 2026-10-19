# @judgment-pack/harness

The assistant harness of [Judgment Pack Desk](https://github.com/Judgment-Pack/judgment-pack-desk),
extracted from the Desk's `web/src/assistant` as it is at `integration/v0.13`, `d9251ec`, with no
behaviour change, behind the wire contract in [`contract/`](../../contract/README.md). Research
preview; version `0.1.0-dev`, unreleased.

It holds the engine slot and its two adapters, the **Vercel AI SDK adapter** (`engines/vercel`) and
the **Codex adapter** (`engines/codex`), and what they share: the context budget and ledger
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
the request text on every request and the memory reserve).

## Entry points

| Import | What it is |
| --- | --- |
| `@judgment-pack/harness/contract` | The engine slot's types and events (`engine.ts`), what every adapter shares (`engines/contract.ts`), the registry (`engines/index.ts`), the host's side (`host.ts`), the capability record (`capabilities.ts`), the image part (`images.ts`), and the chat-scoped context a host makes once per chat (`ChatContext`, `context/chat.ts`) with the run's context inputs (`ContextInput`). |
| `@judgment-pack/harness/vercel` | The Vercel AI SDK adapter, `engines/vercel/index.ts`. |
| `@judgment-pack/harness/codex` | The Codex adapter, `engines/codex/index.ts`. |
| `@judgment-pack/harness/certification` | The scripted model and runtime server, the recorded scenario and runtime the suite runs on, and the recall rule (`recallQuery`, `certification/recall.ts`). |
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
`experimental_test_cases`), the engine ids `ASSISTANT_ENGINES`, and the types
`EndpointKind`, `ThinkingTier`, `Language`, `AITarget`, and, for Plan mode, `Chat`, `ChatStore`,
`FileRevision` and `PreparationReply`, each reduced to the members the harness touches. A host's
own types for these are structurally the same. `sourceMessage` fills the harness's own messages from
their values; it is the Desk's function without the Desk's plural table, which no harness message
reaches (none passes a `count`).

**Injection points**, what only a host can do. Each one throws, naming itself, until the host
supplies it with `configureHarness`. (`NO_SESSION_MESSAGE` is a sentence and cannot throw; it is
read only to build a `NoSession`, which does. `endpointCapabilities` has an answer of its own,
nothing known, so a host that knows nothing about its endpoints may leave it out.)

| Injection point | What the host supplies | The Desk feature it replaces | Read by |
| --- | --- | --- | --- |
| `assistantLanguageInstructions(locale)` | The reply-language section a run's instructions end with | `i18n/assistantLanguage` | `engines/vercel/loop.ts`, `engines/codex/index.ts` |
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
| `renderImageToText(image, signal)` | An image as text, for a model whose record says no vision: the host's render adapter | none (new, agent#5) | `images.ts`, from `engines/vercel/loop.ts` and `engines/codex/index.ts` |

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
  deskFetch, answer, readFile, dialectAPI, endpointCapabilities, renderImageToText
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

### What changes between requests

The `vercel` adapter sends a stable prefix: its system text with the execution instructions, the
tools, the host's prompt and the history. What changes between requests is one text headed
`STATE_HEADER` (`engines/vercel/loop.ts`): the request counters, the plan and the operations index,
the step's own instruction, and the host's own changing text, which a host passes as
`context.currentState` (a chat's draft snapshot, the latest check) rather than inside `prompt`. On
the first request it is a second text part of the person's message, on every wire. After that it
goes where the endpoint's placement says: `trailing-user`, one user message after the history (the
default), or `system-tail`, the end of the system text (for an endpoint marked `strictAlternation`,
and by default for Anthropic when a thinking tier is on). A host sets both through
`endpointCapabilities`; they are two members of the capability record. On the Anthropic wire the adapter marks cache breakpoints after the system
head and after the tools. The tier is held for the run, changed only where the endpoint refused it
or a thinking signature came back truncated. The provider's cache reads and writes are recorded with
each completion (`cacheReadTokens`, `cacheWriteTokens`). The contract states the header and where it
sits on each wire (`budget.json`, `contract/vectors/budget/state-placement.json`).

### Across the turns of a chat

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
read never is. `retainedBytes` bounds the chat: the oldest entries no running run references are
evicted first. Each run's first message carries, after the request text, an index of the results
earlier runs kept (`read_window` id, tool, arguments digest, size, first line), bounded by
`turnIndexEntries` and `turnIndexBytes` and counted in the request budget; results evicted since the
last index are listed there. A run's checkpoint carries the entries it retained or read, not the
chat's. Nothing is global: a run reaches only the chat context it was handed, and a delegated child
gets none. `wire-contract.md` has the rules; `tool-result.json` the index and the cache key.

### The request text and project memory

The request text is the first text part of a run's first user turn on every request of the run, on
every wire (agent#11): nothing the loop does moves it, and every other user text the loop adds comes
after it. The contract states the rule a server applies to find it (`budget.json#/$defs/recallQuery`,
`certification/recall.ts`), and the certification suite applies it to every request on every leg.
The host says per run whether the project's memory is on (`context.memory`): `false` frees the
`memoryBytes` reserve, and a run whose host says nothing keeps it.

## Certification

```sh
npm ci
npm run certify -- --adapter vercel
npm run certify -- --adapter codex
```

The suite is the Desk's conformance session and its certification checks, moved whole: the recorded
bake-off scenario on a scripted model and a recorded runtime, the relay-only rule (K1), the
scenario's five-tool offer (K2), the rehearsal rewrite (K3a), the refused write (K3b), the proposal
(K3c), event order, and the network and timer seal shown to fail on the four fixtures in
`certification/certification/`. One check is added (agent#7), and every adapter runs it: the
ceiling is exactly six names, and `test_conformance` is refused at the wire even where a runtime
serves it and a file grants it. It reads the contract's vectors where the contract states what it
asserts (the scenario's event sequence and K2's offer, from `contract/vectors/`) and the recorded
scenario otherwise. Each adapter runs the suite's own checks of the scenario and the seal, then the
checks the Desk runs over every engine of its kind: a model engine (`vercel`) the conformance legs,
six wire legs plus the thinking and refutation legs, and, added here (agent#9), the strict-alternation
leg: an endpoint that answers 400 to two user turns in a row or a user text after a tool result,
which the loop passes with the state in the system tail; and (agent#4) the text tool calls legs: the
whole scenario, on each of the six wire legs, at an endpoint whose record says it takes no native
tool calls and which refuses any request that carries tools, with the same calls, gate events and
proposal, and once more where it also keeps a strict turn order; and (agent#11) on every request
of the plain, text-path, strict-alternation and thinking legs, the recall rule finds the request
text the run started from; an agent engine (`codex`) the recorded
scenario behind the real tool gate and the agent-engine checks in `engines/codex/codex.test.ts`.

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
