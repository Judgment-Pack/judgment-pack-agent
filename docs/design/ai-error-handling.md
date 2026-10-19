# AI error handling in the harness

Status: 2026-10-18, for agent#3. The Desk's shared design (`docs/design/ai-error-handling.md` in the
Desk) owns the failure catalogue, its recovery categories and how a host renders a failure; the
[wire contract](wire-contract.md) states the failure shape and the retry rule. This page records what
the harness's two engines report on the failure paths the audit found untested, what a host's
Retry does after each, and one decision: what happens when a provider refuses the context as too
long.

## What every failure path keeps

A failure is reported once, as an `error` event followed by one `end`, with a catalogue class, a
source and an outcome (`failure.json`). The catalogue's `recovery` for the class is the hint a host
reads. The run's checkpoint survives: its last `execution_checkpoint` keeps every completed
operation with its arguments and result, and is one a host can hand back. A failed tool call is not
a failed run: the model reads a failed result and goes on.

A host offers **Retry** unless the recovery is `narrow`, and not before the failure's `retryAt`.
Retry starts the same request again from the failed run's checkpoint (`context.execution`, status
`running`), as the Desk's `retryResponse` does: completed operations are kept and the harness does
not make them again, an operation that was in flight becomes `interrupted`, and the request count
carries on from where it stopped, within the same allowance.

## The paths

Each row is a case in `packages/harness/engines/failurePaths.test.ts`, run on one turn that has
already completed a runtime call when the failure comes.

| What happens | `vercel` reports | `codex` reports | Recovery | Retry |
| --- | --- | --- | --- | --- |
| The connection drops before the first byte (the page's request to the relay fails, or the relay answers 502 `assistant-relay-upstream`) | `connection-lost`, outcome `unknown`, one request | `connection-lost` (the agent socket closes before `started`) | review | offered; resumes |
| The connection drops after the first byte (a stream ends with an error after its first chunk) | the stream's error: `unknown`, outcome `unknown`, never sent again | `connection-lost` (the socket closes mid-run) | review | offered; resumes |
| The same, on a whole answer the relay is reading | `connection-lost`, never sent again | (no whole answers on the agent wire) | review | offered; resumes |
| A stream stalls past the relay's idle bound (two minutes without a write in the Desk's relay, which then cuts the stream) | the cut stream's error, as above | the Codex bridge's run deadline passes and the server ends the run: `timeout` | review | offered; resumes |
| The provider refuses the context as too long (413, or a 4xx carrying `context_length_exceeded`; on the agent wire the server's own input-budget refusal) | `context-limit`, outcome `refused`, one request | `context-limit`, source `context`, outcome `not-started` | narrow | not offered |
| The runtime's socket drops during a tool call | the model reads a failed result, `tool-failed`, outcome `unknown`; the run goes on | the same | review (of the result) | no failed run |
| The endpoint answers whole JSON where a stream was asked for | read whole and re-framed (`relay.ts`); nothing fails | (no counterpart) | none | none |
| The same, cut short so it is not JSON | `invalid-response` | (no counterpart) | retry | offered; resumes |

No path is retried automatically: only a `refused` outcome of a class or code `failures.ts` names
is (`RETRYABLE_REFUSAL_CLASSES`, `RETRYABLE_OVERLOAD_CODES`; the wire contract's retry rule), and
none of these is one. `invalid-response`'s recovery is `retry`, which is the person's Retry, not
the engine's. A whole answer that
broke while the relay read it, or is not JSON, used to be handed to the SDK as a spent body and
reported as `unknown`; the relay now reports it by class (`connection-lost`, `invalid-response`).
A stream broken after its first byte is reported as the SDK reports it, `unknown`: the harness
cannot tell the stream's error from any other at that point, and the recovery is `review` either
way.

## The decision: no shrink-and-resend in this release

When the provider refuses a request as longer than its context window, the run reports
`context-limit` with its checkpoint and stops. The harness does not shrink the request and send it
again on its own, and a host does not offer Retry for it.

Why:

- The harness already sizes every request to the configured window before it is sent: the input
  budget, the bounded tool results, the read-back references and the compaction of earlier steps.
  A provider refusal therefore means the provider's window is smaller than the one configured for
  that model, or that the person's own request is too large to fit. Neither is fixed by sending the
  same request again, and the first is a configuration the person can correct (the per-model
  `contextWindowTokens` override).
- Shrinking would decide, without the person, which of their material the model does not see. The
  compaction that exists drops only completed steps, kept in the ledger with their handles; a
  resend that cut the current request or its supplied input would answer a question the person did
  not ask.
- A 4xx refusal is not charged and did nothing, so stopping costs nothing but the turn; the
  checkpoint keeps every completed operation for the next one.

What the person sees is the catalogue's sentence and the `narrow` recovery: send a smaller request,
or read smaller context sections. A later release may add one bounded, visible resend after the
harness compacts what it can, with the person's consent recorded in the checkpoint; it would be a
change to this page and to the wire contract's retry rule.
