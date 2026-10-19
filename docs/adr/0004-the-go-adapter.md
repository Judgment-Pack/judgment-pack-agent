# ADR-0004: The Go adapter

Status: accepted, 2026-10-19, as amended: the gate is PR 2 before the adapter; `Run` carries the
gated call. The Go train is PR 1 this ADR with the spikes and the module, PR 2 the gate (`go/gate`,
held to `toolGate.ts`'s vectors, with the review findings of PR 1, agent#53), and PR 3 the Eino
adapter.

## Context

ADR-0001's fourth step is a Go adapter, Eino or ADK for Go, for when the loop moves into the Desk's
Go server, where the relay, the admission slots, the approvals and the server's share of the prompt
already live. The Desk ships as one Go binary, whose `go.mod` has six direct requirements today. A Go
adapter runs in that process.

The contract is language-neutral (`contract/`), so a Go adapter needs three things this repository
did not have: Go types for the contract, a Go runner for the certification suite that drives the
same scripted model and recorded runtime the TypeScript suite drives, and a framework. This ADR
decides the framework, from one spike per candidate, and lays out the Go module.

The candidates, pinned to releases published before 2026-10-05 and read in their own source at
those tags rather than in any summary:

| Module | Version | Published |
| --- | --- | --- |
| `github.com/cloudwego/eino` (Eino; its ADK is the `adk` package) | v0.9.21 | 2026-09-23 |
| `google.golang.org/adk` (ADK for Go) | v1.8.0 | 2026-10-02 |

What the contract asks of an adapter, as the other three meet it: the three wires
(OpenAI-compatible, Anthropic, Gemini) through the host's keyless relay, with no provider key in the
adapter; the tool gate as the only boundary, with the offer of each phase (the loop's, the critic's
allow-list, Plan mode's, a delegated child's profile) and an unoffered name put through the gate as
the model wrote it; the events of `run-event.json` in order, each guardrail between its call and
its result; the two pauses for a person (the question and the budget) and no approval across the
slot (`approval.json`); the context budget and the ledger; delegation by profile; the critic; one
retry by the shared rule, the turn's clock and the tool-call deadline; and the checkpoint, which is
the execution controller's.

## The spikes

Each spike is under `go/spike/` (its own Go module, not shipped and not in CI). Each runs the
certification's scenario leg, the leg the TypeScript suite runs as `engine <id>`, on the
OpenAI-compatible wire, answered whole and streamed, against the shared scripted server, through the
Go runner below. In both, the model is the spike's own over the relay capability, every tool call
is answered by one dispatch that writes `tool_call`, puts the call through the gate and writes
`tool_result`, and the final answer's proposal is read as the scenario writes it. Probes in process
(a scripted model type, no wire) measure the dispatch itself. The measurement was made in PR 1
through the certification's stand-in gate, which kept the two rules the leg measures; since PR 2
the spikes call the gate the run carries (decision 3), and they were not run again. A streamed leg
exercises the spike's own SSE reader of the relay's answer, not the framework's stream path: Eino's
`Stream` there wraps `Generate`, and ADK for Go's model ignores its stream flag. What the
framework does with a stream is the adapter's pull request's to measure.

Both spikes passed every check the Go runner made in PR 1, on both legs: no reach for the
process's default transport (K1a), no credential and nothing but the relay (K1), the offer (K2), the
rewritten evaluate (K3a), the refused `write_file` (K3b), the proposal (K3c) and the event sequence
the scenario vector states. So does a plain Go loop with no framework, under a hundred lines, which
is the runner's own test (`go/certification/certification_test.go`), and since PR 2 it passes every
check of the leg through the real gate, the ones PR 2 added among them (decision 5). What separates
the candidates is what each needed to get there.

| What the contract needs | Eino v0.9.21 | ADK for Go v1.8.0 |
| --- | --- | --- |
| A model through the keyless relay | Native: the model is any `model.ToolCallingChatModel`; the spike's is a client of the relay capability. The message type, `schema.Message` (role, content, tool calls, tool-call id), is the OpenAI-compatible wire's shape. | Native: the model is any `model.LLM`; the spike's is a client of the relay capability. The message type is `genai.Content`, the Gemini API's, so the OpenAI-compatible and Anthropic codecs translate from Gemini's shape. |
| Every call through the gate | Wrapper, two entry points. A declared name reaches the middleware's `WrapInvokableToolCall`; an unoffered name reaches only `ToolsNodeConfig.UnknownToolsHandler` (measured: `write_file` bypassed the middleware). Both route to one dispatch. A tool is handed the arguments as the model wrote them, byte for byte (measured). | Wrapper, two entry points and a check before them. A declared name reaches its `functiontool` handler; an unoffered name reaches only `OnToolErrorCallbacks`, with a stand-in tool (measured). `functiontool` converts the arguments to a map and checks them against the declared schema before the handler: a call whose arguments do not fit never reaches the handler, and the tool-error callback gets the framework's error instead (measured). The gate sees the arguments re-encoded from a map. |
| The offer of each phase | What the model is shown is native: `BeforeModelRewriteState` rewrites the tools the next request shows (`state.ToolInfos`). Dispatch is not: it still uses `ToolsNodeConfig.Tools`, so the dispatch checks the phase's offer itself, and the gate is that check (`go/gate`, `Gate.CallTool`). Neither spike measures this row. | What the model is shown is native: a `BeforeModelCallback` rewrites the request's tools; dispatch still uses the agent's table, so the dispatch checks the phase's offer itself, as for Eino (as the TypeScript boundary does on either). |
| Events in order | Native with configuration: one turn's calls run in parallel by default and one after the other, in the model's order, with `ExecuteSequentially` (measured: two at once; one at a time in order). | Wrapper: each call of one turn runs in its own goroutine, with no option to run them in order (`internal/llminternal/base_flow.go`; measured: two at once). The dispatch must serialize them so that each guardrail stays between its own call and result. |
| A tool's answer as the model is shown it | Native: a tool returns the text. | Wrapper: a tool returns a map, which `functiontool` wraps in `{"result": …}` where it is not one; the codec unwraps it on every request (28 times on one leg). |
| The pauses | Native and measured, not used: `tool.Interrupt` ends the run with a checkpoint, and `Runner.ResumeWithParams` resumes it; the checkpoint is a gob of the run's whole message state (22,001 bytes in the probe). | Native and measured, not used: `RequireConfirmation` puts a confirmation request in the session's events, and a later turn's function response runs the tool. |
| Context budget and ledger | Native hook: `GenModelInput` and `BeforeModelRewriteState` replace the messages a request carries. | Native hook: a `BeforeModelCallback` replaces the request's contents; the runner also requires a `session.Service`, which keeps every event of the run, a second record of it. |
| Delegation, the critic | Both available (agent-as-tool, sub-agents, `prebuilt`), not used: a child or the critic is a second agent on the same relay and dispatch. | Both available (`agenttool`, sub-agents, A2A), not used, as for Eino. |
| Retries, clock, deadlines | No retry unless `ModelRetryConfig` is set; deadlines are the context's. | No retry in the core (a retry plugin is not mounted); deadlines are the context's. |
| What it adds to the binary | 124 non-standard packages from 28 modules, among them `bytedance/sonic` (a JSON library with a just-in-time assembler), `gonja` and `logrus`; a stripped binary of the spike's packages is 16.7 MB against 3.8 MB for the standard library alone. | 273 non-standard packages from 33 modules, among them `genai`, Google Cloud authentication, gRPC and OpenTelemetry; 14.7 MB. |
| Release line | Before 1.0: v0.9.17 to v0.9.22 between 2026-08-28 and 2026-10-19, with v0.10 in alpha. | 1.x: v1.5.1 to v1.9.0 between 2026-07-22 and 2026-10-15. |

Measured with go1.26.5 on linux/amd64; the binary sizes are `go build -trimpath -ldflags='-s -w'`
of a program that constructs the agent, runner and tools the spike uses.

## Decision

### 1. Eino's ADK, at v0.9.21

The Go adapter is built on Eino's `adk.ChatModelAgent` under an `adk.Runner`, pinned to v0.9.21
and moved only by a pull request the certification suite passes. The three reasons that decide it:

- **The gate sees the call as the model wrote it.** Eino hands the dispatch the arguments text
  unchanged and takes back the text the model is shown. ADK for Go converts the arguments to a map
  and holds them to the declared schema before any handler runs, so a malformed call is refused by
  the framework rather than reported by the gate (the contract's `invalid-tool-input` result), and
  every result is a map a codec has to unwrap.
- **The contract's order is configuration, not a wrapper.** `ExecuteSequentially` runs one turn's
  calls in the model's order, and one middleware rewrites both the messages and the tools of each
  request. ADK for Go runs one turn's calls at once with no option, and its runner keeps a second
  record of the run in a session service it requires.
- **A provider-neutral message type and fewer packages.** `schema.Message` is the shape two of the
  three wires speak; `genai.Content` is Gemini's, and ADK for Go brings `genai`, Google Cloud
  authentication, gRPC and OpenTelemetry into the Desk's binary (273 packages against 124). Fewer
  packages, not a smaller binary: Eino's stripped build is the larger one (16.7 MB against 14.7 MB,
  the table above).

The costs, stated:

- Eino is before 1.0 and releases often; a minor release may change an interface. The pin is exact
  and the suite is what notices.
- It brings `bytedance/sonic` into the Desk's binary, with its assembler, and about 12.9 MB to a
  stripped build; ADK for Go would bring about 11 MB.
- An unoffered name bypasses the middleware and reaches only the unknown-tools handler, so the
  dispatch has two entry points; both are the adapter's, and the mutation check "an unoffered name
  run" covers the second.
- Its checkpoint and interrupt are not used (decision 4), so the pause it is strongest at buys
  nothing today.
- The three wire codecs are the adapter's, as they are for `deepagents` (ADR-0003). The spikes
  used neither candidate's provider clients (Eino's `eino-ext` model components, ADK for Go's
  Gemini model); whether one could route through the relay capability with no credential of its
  own was not measured.

Not chosen, as well as ADK for Go: no framework at all. The runner's reference loop passes the same
leg, and a plain loop is what the framework is measured against. ADR-0001's line is a framework
behind the contract, and Eino's middleware points, its sequential and parallel dispatch, and its
interrupt are what an adapter on it gets without writing them; the reference loop stays the runner's
own baseline.

### 2. What moves server-side, and what stays the host's

Moves into the Desk's server with the loop: the adapter (the loop, the relay client of each wire,
the dispatch and its events), the context budget and the run's ledger (they shape every request,
and a request is built where the loop is), delegation's coordinator, the critic, the reformat run,
retries, the turn's clock and the tool-call deadline. The relay is called in process there rather
than over HTTP, through the same capability: a path suffix and a request, never an address or a
key. The in-process call runs through the Desk's relay handler's own path (`handleModelRelay`,
`internal/desk/modelrelay.go`), not beside it: its four slots (`maxRelayInFlight`), its body bound
(`maxRelayBody`), its idle and overall deadlines (`relayIdle`, `relayDeadline`), its transport that
follows no redirect, and `withoutReflectedCredentials` on the provider's answer, so a key the
provider reflects in an answer's header does not reach an adapter in the Desk's process.
`withoutReflectedCredentials` reads headers only: a body is not read, and a derived form of the key
(base64, hex, a part of it) is not caught (amended, agent#56). The adapter's pull request names the
seam it calls and a test that a reflected credential does not reach it.

Stays the host's: the gate (ported to Go as the host's package, below, and used by the server; an
adapter's own guardrails are never the boundary, ADR-0001), the Desk's UI, the approvals and review
cards, the person's answers to a question and to a budget increase, and the chat's store, which the
host hands a run and takes back as it does today (`checkpoint.json`, the retained context).

### 3. The gate is the host's, in Go

`packages/harness/toolGate.ts` is ported to `go/gate` in its own pull request, PR 2, before the
adapter (amended; PR 1 placed it beside the adapter), and is what a Go host puts the runtime's
transport behind. It holds the ceiling of six names, the frame inspection (the MCP SDK's JSON-RPC
message shapes, the rehearsal rewrite, the test tool's members and byte bounds), each phase's offer
(the loop's, the critic's allow-list, Plan mode's reads, each delegation profile's tools) with the
refusal of a name the phase did not offer before any call, the turn's test-call cap, and the
approval state's rule (no call mints a grant). It is held to the same vectors as the TypeScript
gate: `go/gate`'s test runs every item of `contract/vectors/tool-offer`, `tool-call`, `tool-result`,
`approval` and `bounds` through it, or names why a file is not a gate decision; the gate's cases
that had no vector are vectors now (`tool-call/gate-frames.json`, `tool-offer/phase-offers.json`,
`approval/grant-states.json`), and `toolGate.test.ts` holds the TypeScript gate to the first two.

`Run` carries the gated call, not the runtime's transport (amended): the runner, as the host, puts
the session's runtime behind the gate, runs the handshake through it and keeps the gate's host
handle (`gate.Gate`: the offer, Plan mode's and a child's phase, the transport). `Run.Gate` is the
adapter's narrow handle (`gate.Calls`): an adapter calls `Run.Gate.CallTool`, holds a host tool to
`Run.Gate.AdmitHost`, and begins and ends the critic's and the reformat run's phases, each only a
narrowing of the phase the host entered; it cannot lift Plan mode, widen a child's offer, reset the
offer or reach the transport past the test-call cap. A child's phase is its profile's tools within
its parent's phase, so a child started in Plan mode holds Plan's reads only. The stand-in gate of
PR 1 is removed, and K3a, K3b and K3c run through the real gate.

Eino's per-phase offer is not native to its dispatch (the table above): the gate is the check its
dispatch makes, on both entry points.

### 4. Pauses, checkpoints and delegation stay the contract's

As for `deepagents` (ADR-0003): no framework interrupt, because no approval crosses the slot and the
question and budget pauses end the turn; no framework checkpoint, because the contract's checkpoint
is the execution controller's and never holds private reasoning, while Eino's would hold the whole
message state; no framework subagents, because the coordinator's profiles, budgets, references,
reports and figures are the package's. The wire contract's open question on a framework with its
own subagents is answered the same way for this adapter.

### 5. The Go module

```
go/                         module github.com/Judgment-Pack/judgment-pack-agent/go, go 1.25.0
  contract/                 the contract's types, generated (contract_gen.go), and the helpers they call
  internal/contractgen/     the generator; internal/cmd/contractgen runs it (go generate ./contract)
  gate/                     the host's tool gate (decision 3)
  certification/            the Go runner: the shared server, a session, the relay capability, the
                            scenario leg and its checks, and the parity table with the TypeScript leg
  internal/depcheck/        the dependency allow-list
  spike/                    a separate module: the two spikes, not shipped
```

PR 2 adds `go/gate`; the adapter's pull request (PR 3) adds `go/adapter/eino/` and, beside it (as
built): `go/relay` (the in-process relay client and the relay handler's path), `go/context` (the
run's context, held to the `budget`, `recall`, `chat` and `framing` vectors), `go/failure` (the
failure catalogue), `go/adapter` (what a host hands an adapter), `go/adapter/wire`,
`go/adapter/turn` and `go/adapter/delegate` (the wires, the turn's rules and the delegation
coordinator, framework-neutral), and `go/cmd/harness-check` (the certification as a command).

**The generator is `contractgen`, in this module**, so the module's commit pins it; `go generate
./contract` writes `contract_gen.go`, and a test holds the committed file to the generator's
output. A third-party generator was tried first: `github.com/atombender/go-jsonschema` v0.24.1
refuses the contract's schemas, on a `$ref` to `#/properties/…` (`capability.json`) and on a
`$ref` to `#` (`failure.json`, `question.json`). `contractgen` writes types, not a validator: an
object with properties is a struct in the schema's member order; an open object keeps the members
it does not name and writes them back; a closed one refuses them; `oneOf` and `anyOf` over typed
branches are a union with one member per branch, chosen by the member every object branch fixes to
a distinct constant where there is one (the run event's `type`); `allOf` over objects is one
struct; a type and `null` is `Nullable`. The schemas stay the validator (`tests/test_contract.py`).
Every vector item, 316 in 102 files, decodes into the type its schema address names and encodes
back to the same value (`go/contract/roundtrip_test.go`), and no decoded value keeps, among an open
object's unnamed members, a member its schema names (read from the schema, its `properties` and
those of its `allOf` branches): an open type that dropped a member fails there.

One union the generator cannot tell apart by its value: `delegated-task.json#/$defs/report`, four
closed branches with the same member names, whose shape is the one the task's profile names
(`profile.report`, analyze where absent). Where more than one branch of a union decodes a value,
the generated `UnmarshalJSON` sets the one closed branch among them and otherwise sets none and
keeps the value whole (`Raw`); a research report, which decodes as an analyze report too, is kept
whole. `contractgen` writes `DecodeDelegatedTaskReport(profile, data)`, keyed by the profile, and
`ReportFor(profile)` on the two types that hold a report (`DelegatedTask`, `DelegatedTaskAnswerTask`);
a test decodes each closed profile's report vector by its profile and each task's report in the
record and answer vectors by its own profile, and the round trip fails where any other union keeps
a value whole. A union decided by a
member of another object is named in `contractgen`'s table of such unions, and the generator fails
where that member's values and the branches do not match one to one.

**The runner speaks to the TypeScript suite's own server.** `packages/harness/scripts/scripted-server.mjs`
serves `certification/scriptedModel.ts` and `certification/scriptedServer.ts`, loaded through
Vite's module runner, over HTTP on the loopback interface: a session is one scripted endpoint and
one recorded runtime, with the relay at the Desk's path and the runtime as one JSON-RPC message per
request; the records the TypeScript legs read come back whole. The Go runner starts it, ends it by
its process id, and holds a Go adapter to the same answers, refusals and records. It is the only
change on the TypeScript side.

**The Go checks are held in step with the TypeScript leg.** The Go leg's checks are a Go reading of
the TypeScript `engine <id>` leg's, and `go/certification/parity.json` maps every check of the
TypeScript suite's engine blocks to the Go checks that make it, or to the reason Go does not run it
yet; a test reads `conformance.test.ts` and fails where it has a check the table lacks. Every check
of the engine leg has its Go counterpart, among them the scenario's call and runtime name lists and
the T4, T5 and T6 results, the recall rule (agent#11), K1b under the tracing variables, the wire's
body members, the eight steps with no repeat, and no error event with one end.

**The dependency graph is held to an allow-list** (amended, agent#56; PR 1 wrote a deny list, and a
prefix deny list is open: langchaingo's model clients, for one, were not on it). K1a in Go seals
only `http.DefaultTransport`, and a dependency with a transport of its own would pass it.
`go/internal/depcheck` lists every package of the module and its tests (`go list -deps -test
./...`) and fails on any package outside the standard library whose module is not on its list of
allowed modules, each pinned at the version `go.mod` requires; the deny list of provider clients
(`eino-ext`'s model components, the `genai` clients, ADK for Go's model package, the provider SDKs)
stays as a second check. A new module is a change to the list, reviewed.

### 6. CI

A `go` job in `ci.yml`, and so in the release's CI at the tag: `gofmt` over `go/` (the spikes
included), `go vet` and `go test` of the module (the round-trip of every vector, the generator's
output, the gate held to the vectors, the parity table, the dependency allow-list, which runs
`go list`, and the runner against the shared server, which needs the repository's Node
dependencies), and `go generate` leaving the tree unchanged. The spikes are not built in CI: they are records of
a measurement, and their modules are not the adapter's.

## Consequences

- The repository holds a Go module beside the npm workspace; a Go host consumes the contract's types
  and the gate, and, after the adapter's pull request, the adapter.
- The certification suite certifies a Go adapter against the same server as the TypeScript ones;
  every leg and scenario the TypeScript suite runs is the adapter's pull request's to run in Go,
  with any disagreement recorded in the wire contract.
- Eino v0.9.21 and its dependencies enter the Desk's binary when the Desk adopts the adapter, not
  before.
