# ADR-0021: Steering a run in flight

Status: accepted, 2026-10-21. Issue agent#105, milestone v0.8.0. The maintainer's
accepted decision builds on [ADR-0005](0005-the-prompt-is-the-adapters.md),
[ADR-0007](0007-provider-managed-delegation-in-go.md),
[ADR-0008](0008-the-approval-pause.md), and
[ADR-0011](0011-the-agents-policy-table.md).

## Context

A person's instruction can change while a response is running. Interrupting an
active provider request would make its outcome ambiguous, while putting the new
words in system text would give them the wrong rank and invalidate the stable
prefix. An approval pause is narrower still: the person's decision on the
pending call is the only input it accepts.

## Decision

The host may enqueue `steer {text}` while a run is in flight. One steer is at
most `budget.steerBytes` (2,000 UTF-8 bytes), and one run accepts at most
`budget.steersPerRun` (4). The package drains the queue only at a request
boundary. Each accepted text is one data block under the run's random marker,
headed as the person's new instruction and placed at the request text's rank.
Several pending steers are placed at the same boundary in input order, one
frame each. They do not change policy, offer, phase, or the stable prefix. The
model has no tool with which to request a steer.

Each delivery emits `steered {type, bytes, atRequest}`. On a model capability
`atRequest` is the model-request number; on an agent capability it is the next
invocation number. An invocation already in progress is never interrupted.
`checkpoint.steers {queued, delivered}` carries pending text and delivery rows
through interruption and resume.

A steer received while an approval is asked is refused with a guardrail whose
reason is `steer refused`: the ADR-0008 decision input is the only accepted input
during that pause. Empty, missing, oversized, and over-count inputs are likewise
refused and never enter the prompt.

## Certification

Scenario 21 runs on every TypeScript engine and both Go adapters. It checks one
steer between requests, the request-rank frame and unchanged prefix; two steers
in order at one boundary; refusal during an approval pause; resume with a queued
steer; and next-invocation delivery on the agent capability. Handwritten vectors
under `contract/vectors/steer/` cover those contract rows. Four mutants are
caught: moving a steer to system text, removing the pause refusal, reversing the
queue, and removing the byte bound; their Go twins are run on disposable copies.

## Consequences

Steering is deterministic at boundaries and does not pretend that an active
request can be recalled. A late instruction may therefore wait for the current
request or invocation to finish. The checkpoint retains pending text because a
resume must not silently lose the person's newest instruction.
