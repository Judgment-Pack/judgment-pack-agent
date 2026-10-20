# ADR-0008: The approval pause

Status: accepted, 2026-10-20. Issue agent#74. Amends [ADR-0006](0006-tool-sources.md) §2 (act
tools); builds on [ADR-0007](0007-provider-managed-delegation-in-go.md) §2 (the handoff and the
continuation) and the existing pause `budget_paused`. The Desk's side (the event in the task view,
the decision in the continue call, the policy in place of its approval reader) is a Desk follow-up.

## Context

Until this decision an `act` call of a tool with a source waited inside the tool call for the
person: the host's approval reader (`HostTool.approve`) was asked `{name, tool, source, args}` and
could hold the call open until the person answered. A held call holds everything under it: the
model engine's step, the agent engine's invocation and the provider account it runs on, and the
browser tab or process the run lives in. A person who decides an hour later, or after a restart,
has nothing to decide on.

The run already has a pause that survives all of that: `budget_paused`. The checkpoint's status is
`paused`, the run ends with `end`, and only the person's grant, recorded in the checkpoint,
continues it. ADR-0007 gives a delegation the continuation shape a paused call needs: the
invocation ends, the checkpoint carries the pending step, and a new request carries the exact
result as one data block.

| Shape | Before this decision | Reused for |
| --- | --- | --- |
| `budget_paused` | the checkpoint's status is `paused`, the run ends with `end`, the person's grant continues it | the pause and its continuation mechanics |
| `user_question` | the turn ends, the answer comes with the next run | nothing: a question is a new user turn, an approval is mid-call |
| ADR-0007 handoff | the invocation ends, the checkpoint carries the pending step, a continuation request carries the exact result as a data block | the continuation after the decision, on every engine |
| the operations log | a committed operation is never repeated after a restart | the one-execution guarantee |
| `HostTool.approve` (the in-call reader, ADR-0006) | asked `{name, tool, source, args}`, may wait | replaced by the policy answer `allow`, `refuse` or `ask` |

## Decision

**An `act` call no longer waits inside the tool call for the person. The host's approval policy
answers at once with one of three words: `allow` (the call runs), `refuse` (the model reads a
refused result, a guardrail says why) or `ask`. On `ask` the run pauses the way a budget pause
does: the call is never run, `approval_requested` takes the place of its `tool_call`, the
checkpoint carries the call under `approval` with status `paused`, and the run ends with `end` and
no error. Nothing is held open: no promise, no invocation, no provider account.** The person
decides in the host's own time; the host continues the restored checkpoint with the decision, as
it continues a budget pause with a grant. On `approved` the call runs once, its result is recorded
in the checkpoint before anything else happens, and the run continues in a new request that carries
the result as one data block under a drawn marker. On `refused` the continuation carries the refused
result instead. A restart between the pause and the continuation changes nothing: the checkpoint
says what was asked, whether it ran, and what it answered.

### 1. The policy

`HostTool.approve` (Go `HostTool.Approve`, of type `ApprovalPolicy`) becomes the policy function
`(call) → 'allow' | 'refuse' | 'ask'` (`tool-offer.json#/$defs/approvalPolicy`). It is handed the
call as before (`ApprovalRequest`: the wire name the model called, the tool's own name, its source
record and a copy of the arguments) and answers at once; it never waits for the person. An answer
that is none of the three, and a policy that throws (Go: panics), is `refuse`. A declared `required`
tool without a policy answers `ask`. `approval: none` on an act stays the host's declared decision:
the policy is not asked. The package holds no grant and mints none.

The policy is asked once per call, where the engine hands the call to the host, before the call's
`tool_call` (`SessionContext.approval`, Go `SessionContext.Approval`, over `SourceGate.decide`, Go
`SourceGate.Decide`, in `toolSources.ts` and `go/context/sources.go`), and after what the gate and the
engine refuse on their own: a call the engine refuses itself (repeated work, the request reserved
for a final answer, a name the phase did not offer) and a call the gate's own checks refuse (the
source's changed list, the list it serves now, a catalogued tool not loaded or its arguments, the
calls per turn) never reach the policy, so the person is never asked about a call that could not
run.

### 2. The pause

On `ask` the call is written into the execution state as the checkpoint's `approval`
(`checkpoint.json#/$defs/approval`, closed): `callId` (the call's id, drawn where the run's events
carry none), `name`, `tool`, `source`, `args`, `state: asked`. The engine delivers
`approval_requested {type, callId, name, tool, source, args}` (closed) in place of the call's
`tool_call`, and never hands the call to the host. The host's stream carries the event, unlike
`delegation_requested`: the person has to see it.

The run then ends at the shared boundary (`context/managed.ts`, Go `runcontext.Boundary`), on every
engine with the same events: the checkpoint written with status `paused`, emitted `durable`, then
`end`, and no error.

- A **model engine** ends once its step's other calls are answered: its next request is refused
  before it is claimed or sent (`ApprovalPaused`, Go `ErrApprovalPaused`), and the boundary turns
  what the engine reports of that refusal into the pause. The engine's loop is answered for the held
  call with a line it never sends (`APPROVAL_HELD`).
- An **agent engine** ends at the call, as at a handoff: the invocation is stopped, the provider's
  run returns, and the account is released.

**At most one approval is pending.** An act call made while one waits (in the same answer, or by an
agent calling tools at once) is refused: the boundary says so with the call, a `guardrail` `refused`
with `reason: approval pending` and the refused result (`permission`, `not-started`), whatever the
engine does with the call after; the policy is not asked and the call is never run. A model that
wants two acts asks twice.

### 3. The continuation

The host continues the paused checkpoint with the person's decision
(`checkpoint.json#/$defs/approvalDecision`, closed: `callId`, `approved`, an optional `note` of at
most 2,000 characters), handed to the run beside the checkpoint (`context.approval`, Go
`Input.Approval`), the continuation input beside a request grant. Before anything runs:

- a decision whose `callId` is not the pending approval's is refused: the run ends with
  `stale-state` (source `context`), no request is sent and nothing runs;
- a paused approval without a decision is refused the same way;
- a decision that is not one the package reads is `configuration`.

Then, in order, each write through the host's durable callback (`context.checkpoint`, Go
`Input.Checkpoint`) and emitted `durable` before the next step:

- **refused**: the approval is written `refused`, with `decidedAt`, the `note` and the refused
  result (`permission`, `not-started`); the call is never run.
- **approved**: the approval is written `approved`, with `decidedAt` and the `note`, and
  acknowledged; the call runs once through the host's own function (`SourceGate.approved`, Go
  `SourceGate.Approved`), the policy not asked again, under the gate's bounds: its tool still
  offered as the person saw it (the same wire name and source record), its source's list unchanged
  and still the one it serves, a catalogued tool's arguments held to its declared schema, a slot
  under `sourceCallsPerTurn`, and `sourceCallMilliseconds`. The approval is written `executed`
  with the result, and acknowledged, before anything else. A refusal by the gate at this point is
  the call's result (the call not run) and is written `executed` the same way.

The run's first request then carries, after the request text, one line (`APPROVAL_CONTINUATION`,
Go `runcontext.ApprovalContinuation`) and one data block under a marker drawn for it, from the tool
the model called, whose data is `{callId, name, tool, approved, result, note}`
(`checkpoint.json#/$defs/approvalContinuation`), and then a chat's bounded index of earlier results
where the run has one: the shape ADR-0007 gives a delegation's continuation. The person's note is
the person's words and reaches the model only inside the block, as data. Once that request is sent
(the digest of its body recorded), the approval is written `answered`.

A restored checkpoint continues from where it stood:

- `executed` or `refused`: the continuation with the recorded result; the call is never run again.
- `approved`, whose result was never written: the call may have run, so it is not run again; it is
  written `executed` with a result saying its outcome is unknown (`unknown`, outcome `unknown`), and
  the continuation carries that, as a restored operation that was running is read, not repeated.
- `answered`: nothing more; the result is in the run's history.

```
model calls act tool ──▶ gate: offered? effect act? bounds? ──▶ host policy(call)
                                                                  │
            ┌─────────────────────────────────────────────────────┼──────────────┐
          allow                                                 refuse           ask
            │                                                     │               │
   execute under bounds                               tool_result refused   approval_requested event
   tool_result                                        + guardrail           checkpoint.approval = asked
   (as today)                                         (as today)            status = paused, run ends (end)
                                                                                  │
                                                                     the person decides, any time later
                                                                                  │
                                                      host continues the checkpoint with {callId, approved}
                                                                  ┌───────────────┴──────────────┐
                                                              approved                        refused
                                                                  │                               │
                                              approval = approved, written, acknowledged    approval = refused
                                              execute ONCE; approval = executed + result    result = refused
                                              (a restart here never executes again)             │
                                                                  └───────────────┬──────────────┘
                                                      continuation request: CONTINUATION line + the result
                                                      as one data block under a drawn marker + the context
                                                      index; approval = answered; the run goes on
```

### 4. Per engine

- **vercel, deepagents**: the pause ends the loop after the checkpoint is emitted `durable`; the
  continuation is a new run from the restored checkpoint whose first request carries the
  continuation block (no provider-side history is relied on, as ADR-0007 already accepts for a
  TypeScript response that stops between a handoff and its continuation). The continuation's
  request is one model request, counted against the restored allowance.
- **codex** and the **Go agent capability** (`go/adapter/agent`): the invocation ends at the pause
  exactly as it ends at a handoff (the account released, the host's function returned); the
  continuation is the response's next run, whose invocation carries the continuation block in its
  prompt. On a delegation coordinator (`withDelegation`, Go `go/adapter/delegate`) that invocation is
  counted against `MAX_AGENT_RUNS` (Go `turn.MaxAgentRuns`) from the restored figures, and an act is
  offered to a parent invocation only while it and the continuation's invocation both remain, as
  `delegate_tasks` is offered only while its batch and its continuation remain. No model-request or
  token figure is invented.
- **eino**: the Go model session pauses as vercel does; it runs a turn's calls one at a time, so a
  second act in one answer is decided after the first. Its continuation goes through the same
  `go/delegate` path as any run.
- **Children, Plan mode, the critic and the reformat run** admit no act call. A delegated child and
  Plan mode offer reads only (ADR-0006) and the critic and the reformat run no host tool; the gate
  asserts it besides: an act call that reaches it there is refused with a guardrail, and the policy
  is not asked.

### 5. The contract

1. `run-event.json`: a new event, `approval_requested {type, callId, name, tool, source, args}`
   (closed), the harness's addition to the Desk's eighteen kinds; `guardrail` gains `reason`,
   `approval refused` or `approval pending`.
2. `checkpoint.json`: `approval` (`$defs/approval`, closed), at most one per checkpoint, `status`
   `paused` while it is `asked`; `$defs/approvalDecision` (closed), the continuation input beside the
   grant; `$defs/approvalContinuation`, the continuation's text.
3. `tool-offer.json`: `approval`'s description, and `$defs/approvalPolicy`, the policy's three
   answers; `tool-call.json`'s `tool_call` names `approval_requested` as what an asked act call gets
   in its place.
4. The vectors `approval/pause.json`, `approval/second-act-while-pending.json`,
   `approval/continue-refused.json`, `approval/continue-approved.json` and
   `approval/wrong-call-id.json`, written by hand.

## Certification

Scenario 8 (`certification/scenarios/approval-pause.json`), on `vercel` and `deepagents` with both
answer shapes, on `codex`, and in the Go `harness-check` on `eino` and on the agent capability
(through the delegation coordinator, `8 agent` checks): scenario 7's scripted MCP server, its policy
asking about `create_issue` and allowing every other act. The pausing run loads `create_issue`'s
schema and makes two `create_issue` calls in one answer: the first pauses the run
(`approval_requested`, the checkpoint paused and durable with the call, `end`, no error, nothing
run), the second is refused with the `approval pending` guardrail between its call and its result.
The paused checkpoint is then read back from its JSON text in fresh sessions, sharing nothing but
the scripted server: continued with a decision for another call (refused before anything runs);
refused with a note (the refused result and the note inside one data block, nothing run, the
approval `answered`); approved, and broken off once the call's result was written and acknowledged
(written `approved`, acknowledged, run once, written `executed`, acknowledged, in that order); and
continued once more from that checkpoint (the continuation runs, the call does not: the server's
count stays one). The continuation is one line and one block after the request text, its marker
drawn afresh; it counts as one model request on a model engine, and on the agent capability as one
counted invocation with no model-request figure. Mutants, each caught (`mutation-rows.json`): a
restored `executed` approval running its call again; the pause skipped under `ask`; the
wrong-`callId` check removed; the pending check removed; the note outside the block.

## Consequences

- An act call costs the person a decision only where the host's policy asks; Auto within limits
  can answer `allow` for the tools the person has set.
- The run-event set is the Desk's eighteen kinds and `approval_requested`; a consumer that does not
  know the kind still sees the paused checkpoint and `end`.
- A paused act is one more durable write, and its continuation two more (approved, executed) before
  the call's first request; the checkpoint carries one approval, within its 4 MiB.
- The person's words reach the model as data; an instruction in a note is a line in a data block.

## What stays the host's

The policy and whatever it reads (the person's settings, the tools set to Auto), showing the
pending call and taking the decision, when a paused run is abandoned, the durable store behind the
checkpoint, and executing the call. In the Desk: the event in the task view (the call's name,
source and arguments, a note field, approve and refuse), the decision in the continue call as the
budget grant is carried, the checkpoint reader reading `approval`, and its approval reader (#529)
becoming the policy.

## Not decided here

- Several pending approvals at once (one is enough: a model that wants two acts asks twice).
- An approval that outlives its call (a grant across calls): the host's grants in `approval.json`
  stay the host's.
- Timing out a pause: the person's host decides when a paused run is abandoned.
- `modelRequests` on the agent capability's figures (agent#71) rides in the same release, in its own
  change.
