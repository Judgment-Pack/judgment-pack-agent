# Task steps

Task steps are an optional execution feature. A host enables them with
`ContextInput.taskSteps: true` in TypeScript or `context.Input.TaskSteps: true`
in Go and supplies its existing durable run store. Opting in exposes plan
configuration and actions; it grants no tool, profile, provider call or approval.
The original run writer still admits every physical dispatch.

Without this opt-in, existing plan actions, provider accounting and feature-free
checkpoint/event versions keep their existing behavior and wire shapes. Published
checkpoint 2, event 1, report 1 and delegated-task formats are unchanged. Task
schedule and envelope records belong to current checkpoint 3; task-aware plan
lifecycle events use event 2.

## Plans and delegation

`update_plan` accepts up to 20 identified steps. The task-enabled additions are:

| Member | Meaning |
| --- | --- |
| `dependsOn` | Distinct existing step IDs, at most 19; cycles and self references are refused. |
| `budget.requests` | Optional positive integer share, at most 100 physical provider calls. |
| `delegation` | Full admitted package profile pin: `id`, `digest` and `source`. |

A pending step is ready only after every dependency is `completed` or `not-needed`.
Local work uses the single `in_progress` step. Independent ready package steps
may run concurrently under the existing route, profile, tool, time, concurrency
and response limits. Sibling shares and the existing synthesis/critic reserves
remain held. A share never enlarges a profile or root allowance.

Delegate ready package work through the source-registered `delegate_plan_steps`
action, using the exact current `planRevision` from request state:

```json
{"planRevision":"sha256:<64 lowercase hex characters>","steps":["step-1","step-2"]}
```

The closed action accepts one to three distinct IDs. It validates the entire
batch before a child starts. The writer derives the existing legacy task request
from the selected step text and exact admitted profile. The caller supplies
neither task IDs nor an available balance. The pending selection, revision and
canonical request digest are saved before handoff; actual child IDs are bound in
the original store transaction before publication or execution. Copying a tool
object, schema or registration-like marker cannot register this action.

## Native host envelope

A standalone native provider has no implicit shared run ceiling. Hosts that want
optional per-step budgets configure `taskStepEnvelope` in TypeScript or
`Input.TaskStepEnvelope` in Go:

```ts
const context = {
  taskSteps: true as const,
  taskStepEnvelope: {
    id: 'research-response',
    revision: 'sha256:' + 'a'.repeat(64),
    unit: 'provider-invocations' as const,
    limit: 8,
  },
  runStore: {store, id: 'research-run'},
}
```

```go
input := &context.Input{
    TaskSteps: true,
    TaskStepEnvelope: &context.TaskStepEnvelope{
        ID: "research-response",
        Revision: "sha256:" + strings.Repeat("a", 64),
        Unit: "provider-invocations",
        Limit: 8,
    },
    RunStore: &context.DurableRunInput{Store: store, ID: "research-run"},
}
```

The host chooses a stable nonempty ID and a `sha256:` revision with 64 lowercase
hex characters. The closed configuration accepts an integer limit from 1 through
100. This is a new host-configured feature bound; 100 is not an inherited provider
or execution policy. It is host configuration, never a model tool argument or
caller checkpoint grant. There are no host `available` or `spent` fields.

The writer binds the immutable configuration to the exact execution/root scope
and persists it before the first dispatch, including pre-plan work. Original
physical dispatch identities consume once. Held and unknown identities still
consume capacity after resume. Removing or changing the configured envelope, or
copying it into a foreign run, refuses continuation rather than resetting usage.

Coordinator-backed native paths also keep the actual existing coordinator's run
ceiling, counters and reserves. The host envelope and coordinator allowance
intersect; the same physical dispatch is counted once in each independent
constraint. Usage-budget reservations remain an additional independent constraint.
The provider-managed checkpoint `limit: 1` is not a shared native balance.

Without a host envelope, a standalone native run may use dependencies and local
completion without gaining a new ceiling. Configuring a per-step budget in that
case is refused before dispatch with `native-envelope-required`.

## Completion and resume

Bulk plan updates cannot claim terminal completion of new work. Use the separate
`complete_plan_step` action with the current `expectedRevision`. Local completion
requires acknowledged successful operation or observed artifact evidence from
that exact run scope. `not-needed` requires a reason and releases the unused
share through the acknowledged plan transition.

Delegated completion requires the exact successful child/task result, its complete
child checkpoint, successful physical provider results and the original
acknowledged terminal event transaction. Request state's `planEvidence.children`
exposes verified evidence. Copy its matching child's tuple into this closed input:

```json
{
  "id":"step-1",
  "expectedRevision":"sha256:<current plan revision>",
  "outcome":"completed",
  "evidence":{
    "operations":[],
    "artifacts":[],
    "child":{
      "task":"<original child task ID>",
      "checkpointDigest":"sha256:<verified child checkpoint digest>",
      "resultDigest":"sha256:<verified task result digest>",
      "eventDigest":"sha256:<verified stored event digest>",
      "commitId":"<original acknowledged terminal commit ID>"
    }
  }
}
```

Caller strings, a complete checkpoint alone, failed/interrupted/reused children,
foreign tasks and stale revisions cannot establish this proof. After resume the
writer corroborates retained child tuples against the original store's actual
history transactions. A run store without the history port can restore accounting
facts but cannot turn a retained tuple into new completion authority.

Explicit host resume sets the existing run-store `resume`/`Resume` option and
reopens the same run ID. A durably bound queued child with verified absence of a
physical claim may run once under its original task ID. Losing the binding
acknowledgement does not trigger an automatic retry in the original invocation.
Any existing or uncertain physical claim prevents redispatch; fresh task IDs cannot
bypass it. Root and step shares are reserved together in the original writer
queue/store CAS, so a lost physical-claim acknowledgement retains both charges.
Resume preserves graph identity, sibling shares and unknown claims.

A fully acknowledged authoritative refusal, such as HTTP 429 rate limiting, may
use the existing retry policy; each physical attempt consumes its own root and
step claim. Successful child evidence requires at least one successful physical
result, with any other results limited to acknowledged authoritative refusals.
HTTP 503 `overloaded_error` retains its existing `unknown` outcome classification.
Task-enabled durable execution therefore stops rather than using the older
child overload retry path. Complete error JSON alone does not prove that a
provider refused execution.
