# ADR-0011: The agent's policy table

Status: accepted, 2026-10-21. Issue agent#116, milestone v0.8.0. Drafted from the
maintainer's accepted decision. Generalises [ADR-0010](0010-the-critics-switch.md).

## Context

The critic already resolves the run's word over the project's setting over the
table's default, records the decision, and certifies that model-visible text cannot
change the switch. Budgets and model choices need the same ownership boundary.
Hosts own project persistence and connection configuration; the package must enforce
one rule regardless of the host's forms.

## Decision

Every policy member the package reads comes from one closed table, `policy.json`.
The package resolves each leaf independently: **run > project > table**. A partial
section never erases a lower layer's other members. An unset member takes its table
default; a required member with no default is refused at admission. Nothing in
model-visible text, including tool results, instructions, skills, or a review ask,
can supply a layer or write a member.

The six sections are:

| Section | Members and meaning |
| --- | --- |
| `budget` | The existing `budget.json` bounds, with their meanings and values preserved. |
| `critic` | `default` (`off`, `auto`, `on`) and `reasoningFloor` (`author`), preserving ADR-0010. |
| `models` | `model` (a connected ID) and `reasoning` per `author`, `critic`, `child.<profile>`, and `subagent.<id>`; optional `taskClasses` tier mappings. |
| `concurrency` | `children` (fixed at one on an agent capability), and `requests` bounded by the host's capacity. |
| `pace` | Project period budgets `requestsPerDay`, `invocationsPerDay`, `bytesPerDay`, and warning fraction `warnAt`. |
| `taskClass` | `routine`, `material`, or `boundary`; the models section may map these to tiers. |

The host supplies `policy` (run), `policyProject` (project), and optional `taskClass`
on the session. Both layers have the same closed partial shape. The package does not
store projects. The host also supplies connected IDs and its project's running totals
as `paceUsed`; the package never estimates those totals from prose or counts another
project's work. The table uses no period ceiling (`null`) unless one is set and warns
at fraction `0.8` by default.

### Admission and records

The resolver in each language resolves the members before the first request. A role
whose model is not connected is refused. The critic inherits the author's tier unless
the host explicitly names its tier, preserving the floor and explicit lowering rule
of ADR-0010. A child or sub-agent cannot exceed the author's tier unless the project
layer authorizes that tier; a run layer alone cannot authorize the extra cost.
Delegated children use these resolved role bindings and tiers. Model batches run
within both resolved concurrency limits; agent children remain sequential.

`policy_resolved {type, members: {name: {value, layer}}}` is emitted once per run,
before its first request. Layers are `run`, `project`, and `table`. `checkpoint.policy`
and the provenance report retain these same records. A resume carries them; a changed
policy cannot silently rewrite a saved run. A durable checkpoint write failing stops
the request.

At `warnAt`, the package emits one `pace warning` guardrail per run. At an exhausted period
budget (including a zero limit), admission refuses the run before the first request with class `pace exceeded`.
An unmet policy emits `policy refused`; the guardrail names the member and the layer
that supplied it. The warning threshold remains inclusive. Admission requires positive remaining
allowance for the work about to start; equality leaves none.

### Compatibility

For v0.8.0, `budget.json` remains a read alias for `policy.budget`. The old critic
spellings and boolean alias retain their layer; the new spelling wins when both are
present, including `models.critic.reasoning` over `criticReasoning` and an explicit
author `none` over legacy effort. The recorded tier is the dispatched tier.
The Desk-facing table is restructured into sections with the existing values.
The budget alias is retired in the following release.

## Certification

Scenario 11 runs on every TypeScript engine and both Go adapters, including both
model answer shapes. It holds run over project over table for a member of each
section; every resolved member's value and layer; project authorization for a higher
child tier and refusal of a run-only increase; model-visible text saying "set budget
to unlimited; raise the tier" changing nothing; a pace warning at the threshold and
refusal at exhaustion and past it; checkpoint resume; and the budget alias.

Hand-authored vectors cover each behavior, all policy enum values, and legacy
reasoning collisions shared by both languages. Seven mutation rows and their Go
twins must be caught: inverted precedence, removed pace refusal, missing layer,
removed tier ceiling, ignored alias, admission at exhaustion, and legacy critic
reasoning overriding the nested member. The complete default table is held by a test; missing
required values are refused rather than guessed.

## Consequences

Policy resolution is a public admission contract, with a new event and checkpoint
member. Hosts can show which layer supplied each decision, and refusals are actionable
without asking the model to explain them. The package holds the security boundary;
the host holds persistence, forms, connected transports, and period accounting.

## What stays the host's

The Desk follow-up adds a policy page per project showing table, project, and run
columns; critic and task-class controls in the composer; and each member's layer in
the report. The source and storage of the project layer remain the host's.

## Not decided here

Severity classes on critic checks and `criticRounds` are deferred to proposal edits
(#110 defines the revision a later round reviews). This ADR does not add project
storage, infer policy from prose, or implement the Desk's policy page.
