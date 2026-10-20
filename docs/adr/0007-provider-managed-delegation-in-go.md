# ADR-0007: Provider-managed delegation in Go (the agent capability)

Status: proposed, 2026-10-19. Issue agent#67; the consumer is judgment-pack-desk#537. Builds on
[ADR-0004](0004-the-go-adapter.md) (the Go adapter) and [ADR-0005](0005-the-prompt-is-the-adapters.md)
(the prompt is the adapter's).

## Context

The Go module of v0.3.0 is a model adapter. `adapter.Run` carries the relay (`Model relay.Call`), and
the delegation coordinator (`go/adapter/delegate`) runs a batch inside the parent's awaited
`delegate_tasks` call, metering children by model requests. Its own comment said the agent
capability's half was not ported. The TypeScript `codex` engine and `withDelegation` have that half:
the parent ends its run at `delegation_requested`, the children run one at a time, and a new run of
the parent continues with the call's result.

The Desk's server-side bridge (`internal/codexbridge/run.go`) owns one active invocation per
subscription account. A synchronous wrapper that starts a child inside the parent's tool callback
asks for the account while the parent still holds it: the child is refused as busy, or waits until
the parent's run times out. Raising the account's concurrency would change the approved
subscription contract. The parent must yield and release first; the coordinator then runs the
children and resumes the parent in a new invocation. A relay-shaped shim would miss that lifecycle
and suggest model-request counts the provider never reports.

## Decision

**The Go adapter interface has two capabilities, and one coordinator drives both.**

### 1. The agent capability beside the model capability

`adapter.Run` gains `Agent *adapter.Agent`, set instead of `Model`, never beside it
(`ENGINE_CAPABILITIES`' `agent`). An `Agent` is:

- `Model`, the model the host bound the capability to, and `Effort`;
- `Run adapter.AgentRun`, the host's provider-run function:
  `func(ctx, AgentRequest, AgentCallbacks) error`. An `AgentRequest` is the published shape
  (`agent.ts`): `prompt`, `instructions`, the tools offered by name (`name`, `description`,
  `inputSchema`), `effort`, `phase` (`author` or `critic`) and `model`. The callbacks are
  `Event(ctx, AgentEvent)` (`text`, `message` with its phase, `retry`) and
  `Tool(ctx, AgentToolCall) (AgentToolAnswer, error)`. No endpoint address, key, credential or
  network access crosses: the host's function holds its own account, process and transport;
- `Enabled`, the host's enabled-model admission, asked before every invocation.

The host's function returns only once the invocation has released its account, stops the provider's
run and returns once `ctx` is done, and answers an error wrapping `adapter.ErrBusy` where the
account refused the invocation at admission because another was active (failure class `busy`,
outcome `not-started`). Any other error is an admitted invocation's outcome.

The adapter on the capability is `go/adapter/agent`, which follows the TypeScript `codex` engine's
lifecycle as the behavioural reference, not a copy: the author's invocation; one tool-free
reformat invocation where a proposal could not be read; the critic's invocation and the harness's
own comparisons where the session asks for the critic; every tool offered under an alias
(`desk_runtime_N`, `desk_host_N`) and answered through the gate's narrow handle, a host tool held to
the phase by `AdmitHost` first. **No second assembler:** every invocation's `instructions` are
`runcontext.SessionContext.System` (the package's guidance, the host's three parts, the data rule)
and its `prompt` is `Opening` (the request text, a chat's index), then `STATE_HEADER` and the state,
the assembly ADR-0005 made canonical. The capability adds no prompt path of its own, and no
authority registry beyond the gate.

The model capability keeps its awaited call: on `Model`, `delegate.With` runs as before.

### 2. The handoff and the continuation

On the agent capability a `delegate_tasks` call is a handoff, not an awaited call. As events and
saves, in order:

1. The parent's invocation calls `delegate_tasks`. The adapter reads the request, emits
   `delegation_requested` with the call's id (`callId`, a new optional member), stops the invocation
   and waits for the host's function to return. The run ends with `end` and no error. The
   coordinator consumes `delegation_requested`; the host's stream does not carry it. The parent's
   closing checkpoint says `running`, not `complete`.
2. The coordinator writes the handoff into the parent's checkpoint (`handoff.state` `released`) and
   waits for the host's acknowledgement.
3. It plans the batch, holds it to the continuation's reserve, publishes each task `queued`, writes
   the records (`children`), acknowledged.
4. For each child, one at a time: the task `running`, published; the child's invocation counted and
   the checkpoint written, acknowledged, after every earlier invocation has returned; the child's
   invocation dispatched; its record (result, report, failure) published and written, acknowledged.
5. The call's exact result (`delegated-task.json#/$defs/answer`) written (`answered`), acknowledged.
6. The parent's next invocation counted, the handoff written `continued`, acknowledged, and the
   invocation dispatched with one line (`CONTINUATION`) and the result as one data block under a
   marker drawn for it, and the bounded context index of earlier results.

A failed or cancelled child keeps its successful siblings' results. A handoff is distinct from a
stop and from a failure: a handoff emits `delegation_requested` (consumed), writes `released` and
continues; a stop emits no error and dispatches nothing more; a provider failure is an `error`
event with source `agent` and its class.

### 3. Invocation accounting

`turn.MaxAgentRuns` (five) is the response's ceiling, the parent's invocations (its critic's among
them) and every child's together. Each admitted invocation is counted once, a busy refusal's retry
included in it. A parent invocation is offered `delegate_tasks` only while it, one child and the
next parent invocation all remain; a batch is admitted only while one child and the continuation
remain; a child never takes the last invocation. Nothing is given back through a child or a
continuation, and a restored checkpoint's counts are where the count resumes. The checkpoint's
`delegation` figures carry the parent's `agentRuns` from its first invocation, each child's
`agentRuns`, and each batch's total. No model-request or token figure is invented: token members
are absent, the execution's unit is `provider-managed`, and `modelRequests`, which the schema
requires, is 0 as its description defines it for an agent capability. The response's tool calls
(`MAX_TOOLS`), the turn's test calls (`testCallsPerTurn`, one count through the parent's gate) and
each profile's narrower bounds hold across the parent and its children.

### 4. Exclusive-account scheduling

Children on the agent capability run one at a time, whatever the connection's capacity says. The
adapter calls the host's function synchronously and returns only after it has returned, so a
handoff's release is confirmed before the coordinator moves; the coordinator also holds every
child and every continuation until no invocation of the response is running on the account. A
cancellation stops the running invocation and returns only after it has returned. A busy refusal at
admission is sent again at most twice, after 250 ms and 500 ms (`canRetryAdmission`); an admitted
invocation's error is never sent again.

### 5. The host's security stays the host's

The host keeps `*gate.Gate`; the adapter and each child get `*gate.Calls`. A child is started by the
host's `Children` capability in its profile's phase within the parent's (`gate.ChildPhase`), so a
child started in Plan mode holds Plan's reads. A child gets no transcript of its parent, no grant
mutation, no approvals, no writes, no further delegation and no native shell, web or filesystem
tool: it is offered its profile's Runtime tools, the parent's host tools whose `delegation` member
is one of its profile's capabilities, and the context tools. The host's `Enabled` is asked for the
parent's, the critic's and each child's invocation, with the model it would run on.

### 6. Durable boundaries

The checkpoint is written through the host's durable callback (`runcontext.Input.Checkpoint`), and
its acknowledgement awaited, before every invocation is dispatched, at each handoff transition
above, and before a host tool that is not a read. A checkpoint the host does not acknowledge
dispatches nothing more: the response ends with `storage` (source `storage`, outcome `not-started`).

The record is `checkpoint.json`'s new `handoff` member (`$defs/handoff`, closed): `callId`, `batch`,
`state` (`released`, `children`, `answered`, `continued`), the call's `request`, the batch's task
records (each child's result, report and failure) and, once answered, the call's `answer`. With
`delegation` (the figures), `operations` (completed operations) and the rest of the checkpoint, it is
what a continuation needs. The Go reader (`ReadExecution`, `ValidHandoff`) reads it closed; the
TypeScript reader keeps it as it keeps any member it does not name.

A restart hands the last acknowledged checkpoint back as `Input.Execution`, which is the person's
explicit continuation; nothing is continued without it. The coordinator continues from where the
checkpoint stood: `released` plans the batch; `children` runs the tasks still `queued`, keeps the
finished ones, and marks a task still `running` `interrupted` with failure `unknown` and the problem
"its outcome is unknown; it was not run again", never dispatching it again; `answered` and
`continued` start the parent's next invocation with the recorded result, as any restored parent
run starts again, its committed operations not repeated. A lost acknowledgement is the same case:
the last checkpoint the host holds says which child was dispatched, and that child is never
dispatched again. Saved model text and a restored checkpoint authorise nothing: every action is
still held to the gate and the host's approvals.

### 7. Questions, Plan and recovery

A question to the person ends the run, unanswered, as on the model capability; a proposal made
through `propose_pack` before it is delivered. Plan mode offers Plan's reads and planning tools to
the parent and Plan's reads within a child's profile. An explicit compute extension is the
execution's (`request_budget_increase`, the person's grant). The restored-context restrictions are
the context package's: a restored operation that was running is `interrupted`, and its result is
read, not repeated.

### 8. Certification

The checks are in `go/certification` (`AgentChecks`), each run against an `AgentHost`: the
package's stand-in (`StandIn`, a fake exclusive account) or a host's own bridge, account and tool
handler bound to each check's script (`AgentHost.Bind`). Every binding is held inside a monitor
that records each invocation and any invocation started while another still runs on the account.
`TestTheFakeAccountCatchesANaiveSynchronousWrapper` runs the handoff checks on a wrapper that runs
each child inside the parent's callback: the exclusive, serial and handoff checks fail. The parity
table maps the agent checks the TypeScript suite also makes (scenario 3's two, scenario 7's eight, run
at the provider-run function's seam, and "one delegated child at a time") and lists the package's own
under `agentCapability`, among them the project's instructions on the agent wire and the critic's
allow-list (an author's tool called by the critic is refused as a name it was not offered); the Eino
model leg certifies none of them. `go/cmd/harness-check` runs the agent checks on the stand-in and says so; a
host certifies its own binding with `certification.CertifyAgent` and its `AgentHost`.

## Consequences

- A Go host can run provider-managed delegation without copying the TypeScript coordinator or
  forking the Go one: it supplies its provider-run function, its enabled models, its durable
  callback, its gate and its `Children` capability (`go/examples/agent-host`).
- The exclusive account is never asked for while it is held, so the account's concurrency stays as
  the subscription contract approves.
- Every handoff transition costs one durable write, acknowledged, before the next invocation.
- The checkpoint carries the handoff, up to three task records and one answer: within its 4 MiB.

## What stays the host's

The provider bridge and its account lifecycle (admission, the busy refusal, process start and reap,
the run's deadline), authentication and session, policy admission and the enabled models, the
durable run store behind the checkpoint callback, host tools and approvals, and the UI and its event
client. In the Desk: `internal/codexbridge`, its run store and its assistant handler.

## Departures from the TypeScript coordinator

- The Go coordinator writes `handoff` and acknowledges each transition; the TypeScript coordinator
  keeps the handoff in memory, so a TypeScript response that stops between a handoff and its
  continuation starts again from the parent.
- The Go adapter waits for the host's function to return at a handoff before its run ends; the
  TypeScript `codex` engine ends the run when the call's signal is aborted, and the bridge's own
  close follows.
- On the agent capability the Go checkpoint carries the `delegation` figures from the parent's
  first invocation, so the count survives a restart before any batch; the TypeScript coordinator
  writes them once a batch has started.
- The Go adapter writes the checkpoint, acknowledged, before a host tool that is not a read; the
  TypeScript `codex` engine relies on the boundary's durable `execution_checkpoint` events.

## Not decided here

ADR-0006's tool sources; a provider-specific transport in this repository (the Desk's bridge stays
the Desk's); the critic's and a child's model on the agent wire beyond the host's admission; a
partial child result streamed into the parent's checkpoint.
