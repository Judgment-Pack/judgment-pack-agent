# ADR-0010: The critic's switch

Status: accepted, 2026-10-20. Issue agent#96. In the shape of [ADR-0005](0005-the-prompt-is-the-adapters.md),
[ADR-0006](0006-tool-sources.md) and [ADR-0009](0009-skills.md); builds on the Desk's ADR-0001 (the
refutation pass and its three rules), agent#10 (declared cases), agent#25 and agent#35 (the critic's
model per connection), [ADR-0007](0007-provider-managed-delegation-in-go.md) §3 (invocation accounting)
and ADR-0009 (the critic sees the author's loaded skills). The Desk's side (the composer's three-state
control, the project form's default, the admin's table default, the decision in the task view) is a
Desk follow-up.

## Context

The package holds the critic: a second, adversarial pass over the proposed document (`refutation.ts`,
Go `go/adapter/turn/refutation.go`, `go/adapter/eino/critic.go`, the agent capability's critic
invocation). It runs under three rules from the Desk's ADR-0001: a non-empty list of checks before
"not refuted" is shown; the verdict comes from the Runtime's results, never from the model's prose; the
critic runs inside the same gate as the author (reads only, `experimental_evaluate` rewritten to a
rehearsal). It compares the outcomes the host declares (`criticCases`) with the Runtime's, has its own
model per connection, and emits `critique`, `criticCase` and `caseComparison` before the proposal.

Until this decision the switch was one boolean the host passed per run, `session.adversarialReview`,
else the policy table's `adversarialReview` (`false`). Nothing tied it to the run's facts or to the
author's reasoning tier, the decision was not recorded (the checkpoint and the report said whether a
`critique` happened, not why), and on the agent capability the critic's invocation competed with
delegation for the response's five (`MAX_AGENT_RUNS`).

## Decision

**The critic's switch is a three-valued policy, `critic: off | auto | on`, set in three layers with one
precedence: the run's word over the project's over the policy table's default (`auto`).** Under `on`
the critic runs on every proposal; under `off` never; under `auto` the package decides from facts it
already holds, never from the person's or the model's prose: the run produced a proposal, and declared
cases exist, or the proposal changes an existing pack, or the model asked through the reserved tool
`request_review`. A human reviewer assigned, a graph-purpose run, Plan mode and a delegated child turn
the critic off whatever the mode, each with its named reason. The critic runs at its own tier, at
least the author's unless the host lowers it explicitly.
Runs reaching the critic decision point emit `critic_decided`; tracked runs checkpoint that
decision. Earlier failures or pauses may omit it; a checkpointed review request survives a resume.
The three rules of the refutation pass do not change.

```
 HOST (the Desk)                                        PACKAGE (the adapter)
 ───────────────────────────────────────                ────────────────────────────────────────────────────────
 policy table: criticDefault = auto ──────────────────▶  mode = critic ?? criticProject ?? table.criticDefault
 project form: criticProject = off|auto|on               (one precedence, here, whatever the host's forms)
 composer:     critic = off|auto|on (per run)
               criticReasoning (optional, per run)
 facts:        existingPack, reviewerAssigned            forced off (any mode), each with its reason:
                                                           a child · Plan mode (phase) · purpose graph · reviewer assigned
                                                         off  ──▶ no critic; request_review refused (guardrail)
                                                         on   ──▶ the critic on every proposal
                                                         auto ──▶ the critic when the run proposed a pack AND
                                                                  (the model called request_review this run
                                                                   OR declared cases exist
                                                                   OR the proposal changes an existing pack);
                                                                  no proposal ──▶ no critic (nothing to refute)
                                                         budget: on the agent capability one invocation HELD BACK
                                                                 for the critic before a delegation batch may take
                                                                 it; a critic with no room left is recorded (budget)
                                                         the critic runs at criticReasoning, else the author's
                                                                 tier (criticReasoningFloor: author), on the
                                                                 connection's critic model, inside the same gate,
                                                                 reads only, the loaded skills visible (ADR-0009)
                                                         events: critic_decided {mode, ran, reason, reasoning, trigger}
                                                                 + critique / criticCase / caseComparison (as before)
                                                         checkpoint.critic = {mode, ran, reason, reasoning, trigger}
 the person sees: the control's state, "a review ran because the pack has saved tests" (or why not)
```

### What exists already, and what this reuses

| Shape | Before | Reused for |
|---|---|---|
| `session.adversarialReview` and the table's `adversarialReview` (`false`) | the switch | `session.critic` and the table's `criticDefault` (`auto`); the boolean read for one release as an alias of its layer (`true` = `on`, `false` = `off`), the new member winning where both are set |
| `criticRuns(session, policy)` (`refutation.ts`; Go `turn.CriticRuns`) | one line | removed; the decision is `criticDecision` (`criticSwitch.ts`; Go `runcontext.DecideCritic`), read through the boundary's `decideCritic` |
| the forced-off cases (a reviewer, a graph purpose, Plan mode, a child) | the Desk's and each engine's | one place in the package, each with a named reason |
| `criticCases` (agent#10) | the critic's comparisons | the `auto` trigger `cases` |
| the critic's model per connection (agent#25, agent#35) | `modelsFor` | `criticReasoning` beside it |
| ADR-0007 §3 invocation accounting | the critic's invocation among the parent's | the invocation held back before a delegation batch |
| the reserved harness tools (`load_tool`, `load_skill`) | catalogue loaders | `request_review`: one reserved name, one optional `reason`, offered in the author's phase only |

## The contract

1. **The session** (`tool-offer.json`, beside `tools`, `hostTools` and `skills`; all optional):
   `critic` and `criticProject` (`$defs/criticMode`: `off`, `auto`, `on`), the run's and the
   project's words; `criticReasoning` (`$defs/criticReasoning`), the critic's tier in the enum of the
   run's capability: a thinking tier (`off`, `on`, `ultra`) on the model capability, an effort (`none`
   to `max`) on the agent capability, a value of the other capability's enum refused before any
   request (`configuration`); `existingPack` and `reviewerAssigned`, the host's two facts; and
   `adversarialReview`, the alias. The design's `critic.reasoning` is spelled `criticReasoning`, as the
   table's `critic.default` is spelled `criticDefault`: the session's `critic` is the mode itself, a
   string, so it carries no member. The project's word is a session member because the package takes
   it from the host as it takes the run's; the host reads its own forms.
2. **`request_review`** (`$defs/requestReview`, `$defs/reservedName`): the model's ask, effect `plan`,
   one optional `reason` (a string; it is shown on the call's event and recorded nowhere else).
3. **`budget.json`**: `criticDefault` (`auto`) and `criticReasoningFloor` (`author`, the one value this
   release defines), which the harness adds to the Desk's table (`contract/desk/policy.json`);
   `adversarialReview` stays in the schema as a read alias of `criticDefault` for one release and
   leaves the harness's table. The flat members are chosen over a nested `critic` object so the table
   stays one level of members, as every other bound in it is.
4. **`run-event.json`**: `critic_decided {type, mode, ran, reason, reasoning, trigger}`, once per run
   that reaches the decision; the guardrail reason `review refused`.
5. **`checkpoint.json`**: `critic` (closed: `mode`, `ran`, `reason`, `reasoning`, `trigger`; `ran` and
   `reason` together), written durably after `critic_decided`, and before that, under `auto`, when the
   model's ask is taken (`mode` and `trigger` alone); carried through a resume.
6. **`provenance.json`**: `$defs/criticDecision`, the report's row, and `$defs/criticReason`, the one
   enum: `person`, `project`, `table`, `model-asked`, `cases`, `changes-existing`, `no-proposal`,
   `reviewer-assigned`, `purpose`, `phase`, `child`, `budget`, `off`.

### The decision, in order

`criticDecision(session, table, forced, run)` in both languages answers the mode, whether the critic
runs, the reason and the trigger:

1. **The mode and its layer**: the run's word (`critic`, else the alias `adversarialReview`) as
   `person`; else `criticProject` as `project`; else the table's `criticDefault` (else the table's
   alias, else `auto`) as `table`.
2. **Forced off**, whatever the mode, read in the order `child`, `phase` (Plan mode), `purpose` (a
   graph-purpose run), `reviewer-assigned`: `ran` false, that reason, no trigger.
3. **`off`**: `ran` false, reason `off`.
4. **No proposal** (a conversation turn): `ran` false, reason `no-proposal`, in any mode.
5. **`on`**: reason the layer that set it.
6. **`auto`**: the trigger, read in the order `model-asked`, `cases`, `changes-existing`, is the
   reason and the `trigger`; with none, `ran` false and the reason is the layer that set `auto`.
7. **Room**: a critic that would run but has no room (the request allowance spent, or on the agent
   capability the response's five invocations taken) does not run: reason `budget`, the trigger kept.

`reasoning` is the critic's tier (`criticTier`): the host's `criticReasoning` where it names one,
honoured as it is, lower than the author's included; otherwise the floor, the author's own tier (the
session's thinking tier on the model capability, its effort on the agent capability). It is recorded
whether or not the critic runs, and absent only where the run states no tier (an agent capability with
no effort). On the model engines a tier other than the author's opens a thinking slot of its own for
the critic's requests (a decision with no tier sends the critic none, `off`); on the agent capability
the critic's invocation carries it as its `effort`.

The decision is taken where the critic would start (after the proposal exists, before anything the
critic does, before its `critique` and the proposal event) or where the run ends with no proposal
(before its last message). A run that fails or pauses before it reaches either point emits none; a
proposal delivered with a `turn-unfinished` note says no critic ran. The shared boundary writes the
checkpoint carrying the decision, durable, straight after the event, on a tracked run.

`ran` means the switch allowed the critic pass. The pass may still report that it could not run,
with zero checks, when the run supplies no test prompt.

## The ownership boundary, stated

- **The person** sets the run's word and the project's default, and sees the decision and its reason.
- **The host** holds the forms and the table; passes `critic`, `criticProject`, `criticReasoning`,
  `existingPack` and `reviewerAssigned` on the session; declares the cases. A Go host that opens the
  gate with its offer adds `request_review` beside the context tools where the switch offers it
  (`runcontext.OfferedReviewTool`, as it adds `OfferedSkillTools`).
- **The package** decides from its own facts, holds the invocation back, runs the critic at its tier
  inside the gate, and records the decision. It never reads intent from prose.
- **The model** may ask for a review (`request_review`) under `auto`; it can never decide the verdict,
  never turn a review off, and its ask anywhere else is refused and recorded as a guardrail.

## The five points, decided

1. **The table's default is `auto`**, so a pack with saved tests is reviewed by default and a
   conversation turn is not. A Desk release may keep `off` as its project default while people learn
   the control: the project's word is above the table's.
2. **The critic's tier is at least the author's**; the host may set it lower only explicitly
   (`criticReasoning`), and `critic_decided` and the checkpoint show it. No automatic coupling of the
   switch to the author's tier here; the measurement (below) informs the Desk's defaults later.
3. **The model's ask counts as a trigger under `auto` only, once per run, in the author's phase.**
   `request_review` is offered only there and only on a run nothing forces the critic off. The first
   call is answered with a note and recorded (the checkpoint's `critic`, so a resume keeps it); a second
   call is refused (`tool-failed`, `not-started`) with the guardrail `review refused`. A call under
   `off` or `on`, from the critic, from the reformat run, in Plan mode or in a child is refused by the
   boundary before anything runs, as any tool the run was not offered is, with the same guardrail and
   reason; nothing is recorded. The switch refuses the ask under `off` and `on` even where it reaches
   it in the author's phase. In Go the adapters refuse it before the gate, since it reaches no host
   function and no Runtime tool.
4. **On the agent capability one invocation is reserved for the critic under `auto` and `on`**:
   the reserve applies to `delegate_tasks`, batches and children. A parent is offered
   `delegate_tasks` only while it, a child, the continuation and the critic's invocation remain; a
   batch of `n` tasks is admitted only while `n` children, the continuation and the critic's
   invocation remain (otherwise it is refused whole, `invalid-tool-input`, no task started); a child
   never takes the continuation's or the critic's invocation. A parent's own continuation may still
   take the invocation; a critic squeezed out that way is recorded as `budget`. Under `off` nothing
   is reserved and the rules are ADR-0007's. On the model capability the critic's requests are
   claimed against the request allowance as before, and a critic with no request left is skipped for
   `budget`.
5. **A human reviewer assigned turns the critic off in every mode**, with the reason
   `reviewer-assigned`, in the package, so every host behaves the same.

## Certification

Scenario 10 (`certification/scenarios/critic-switch.json`) runs on every engine (`vercel` and
`deepagents` on both answer shapes, `codex`) and on both Go adapters (`eino` on both shapes, the agent
capability on the stand-in), the runs played in the file's order: `auto` with a proposal and declared
cases runs the critic (`cases`, the decision before the critique, the durable checkpoint carrying it);
`auto` with a conversation turn runs none (`no-proposal`); `auto` with a proposal on an existing pack
runs it (`changes-existing`) and one on a new pack with no cases runs none (`table`); `request_review`
under `auto` runs it (`model-asked`), the first ask answered and the second refused with `review
refused`, and is never offered to the critic; under `off` it is refused with the guardrail and no critic
runs; `on` runs on every proposal (`person`); a reviewer assigned under `on` runs none
(`reviewer-assigned`); the critic's tier is the author's unless lowered explicitly, shown in the
decision and sent on the critic's requests or invocation; a run broken off once the model's ask is
written and continued from its checkpoint's JSON text runs the critic with `model-asked` and refuses
its own ask. On the agent capability, a batch of three under `auto` is refused and the critic runs on
its held invocation, a batch of two proceeds with the critic as the fifth invocation, and under `off`
a batch of three proceeds.

**The measurement**: the same proposal at two author tiers (`off` and `on` on the model capability,
`low` and `high` on the agent capability), with and without the critic, each run's outcome written as a
table in the certification output (`npm run certify`, `go run ./cmd/harness-check`): the author's
tier, the switch, whether the critic ran and at which tier, its verdict, its checks and its requests.
No verdict is drawn from it in the package; the Desk reads the numbers to set its defaults.

The vectors (`contract/vectors/critic/switch-*.json`, `request-review.json`, `review-asked.json`,
`review-refused.json`, `decided-events.json`, `checkpoint.json`) are written by hand; each language's
decision is held to each case beside its row, and the contract tests compute every row again from the
rule as written here. Six mutation rows are caught, each with its Go twin: the decision read from the
model's prose, `request_review` honoured under `off`, the held-back invocation removed, the reason not
recorded, the tier floor removed, the table's default over the run's word.

## Consequences

- Under the table's default every author run is offered `request_review`, so the offer of a session
  that states nothing carries one more host tool after the context tools, and runs reaching the critic
  decision point carry one more event.
- A pack with saved tests is reviewed by default, which costs the critic's requests (or one
  invocation) where before it cost nothing unless the host asked; a new pack in a conversation is not.
- On the agent capability a response under `auto` or `on` can delegate one task fewer than under `off`.
- `criticRuns` is no longer exported from the contract entry; `criticDecision`, `criticMode`,
  `criticTier`, `CRITIC_MODES`, `CRITIC_REASONS` and `REQUEST_REVIEW` are. Go's `turn.CriticRuns` is
  removed; `runcontext.DecideCritic` and `SessionContext.DecideCritic` replace it.
- `adversarialReview` is read for one release and then retired, on the session and in the table.

## What stays the host's

The forms (the composer's control beside the thinking tier, the project form's default, the admin's
table default), the line under `auto` that says when a review runs, the task view's and the report's
display of the decision and its reason, the reviewer assignment, the declared cases and the fact that
a run works on an existing pack.

## Not decided here

Inferring intent from the person's words (a model judgment, kept out of the proof path); several
critics, or a critic of the critic; the critic's verdict changing the proposal (it reports; the person
decides); a narrowing of the `auto` triggers by the host; a tier rule derived from the measurement
(the Desk's default to set once the numbers exist); a floor other than `author`.

v0.9.1 amendment (#155): adversarialReview (native Run.Review) is retired and refused on presence, including false; use critic / Critic, or policy.critic.default.
