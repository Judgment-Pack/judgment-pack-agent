# Repeated tool calls

A host opts in with current policy version 2:

```json
{"version":2,"repeatTools":{"mode":"on","remindAt":1,"stopAt":5}}
```

The host's run, project and table layers use the existing policy precedence.
The default mode is `off`; resolved off policy, requests, tool results,
checkpoints and events keep their existing bytes. The canonical Desk policy
and its mirrors remain unchanged. Enabling detection requires the original
durable run writer and an acknowledged run store.

| Member | Default | Bound |
| --- | --- | --- |
| `mode` | `off` | `off` or `on` |
| `remindAt` | 1 | Positive integer, at most 400 |
| `stopAt` | 5 | Positive integer, at most 400; at least `remindAt` |

A repeat is the second or later acknowledged occurrence of the same source,
tool, canonical arguments and result digest in the original durable execution.
Counts accumulate across interleaved calls, roles and phases. Separate child
executions have separate counters. Changed results do not count as repeats. Argument object keys use UTF16 order;
decimal numbers compare by exact sign, coefficient and exponent, without
float64 rounding or expanding powers. Equivalent spellings such as `1e3` and
`1000` compare equal; adjacent integers above `2^53` remain distinct.

`durable.dispatches[].repeatResultJson` carries the exact original result JSON
encoding only for enabled, acknowledged host/runtime results. The repeat key
uses its exact byte digest. Different result member order or number spellings
can therefore be different results. The existing result projection and replay
behavior are unchanged. Resume checks that the retained encoding represents
the stored result under that existing projection; this consistency check
cannot itself grant dispatch or completion authority. UTF8 result and complete
record limits still apply; custody is never clipped or reconstructed.
Reused durable results and unknown outcomes do not create new observations.
Detection never dispatches a tool to compare its answer.

The original tool-result transaction retains its dispatch identity, counter,
`repeat_detected` event, reminder decision and named stop together. Losing the
acknowledgement stops that invocation; explicit resume uses the acknowledged
journal and cannot recount a result or clear a stop. State is bounded by the
original journal's 400 physical dispatches and 32 child executions, without
eviction.

At `remindAt`, the prompt assembler inserts one framed package reminder into
the next model request. Tool output is unchanged. The checkpoint retains both
the reminder decision and whether an admitted request carried it. At `stopAt`,
the execution retains status `stopped` and reason `RepeatedToolLimit`; the
original writer refuses further tool, model and native-agent dispatches.
Already admitted concurrent claims retain the existing result/unknown rules.
A reminder or stop grants no permission and bypasses no approval.

Enabled state uses current checkpoint 3, and `repeat_detected` uses current
event 2. Published checkpoint 2, event 1 and policy 1 refuse these new members.
Current run report 2 projects repeat counts, thresholds, reminder/stop decisions
and admitted policy provenance, without raw arguments, result bytes or the
observation index.
