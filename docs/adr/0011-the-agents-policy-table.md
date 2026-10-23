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

`policy_resolved {type, members: {name: {value?, layer, source}}}` is emitted once per run,
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
section; every resolved member's optional value, layer, and source; project authorization for a higher
child tier and refusal of a run-only increase; model-visible text saying "set budget
to unlimited; raise the tier" changing nothing; a pace warning at the threshold and
refusal at exhaustion and past it; checkpoint resume; and the budget alias.

Hand-authored vectors cover each behavior, all policy enum values, and legacy
reasoning collisions shared by both languages. Nine mutation rows and their Go
twins must be caught: inverted precedence, removed pace refusal, missing layer,
removed tier ceiling, ignored alias, admission at exhaustion, legacy critic
reasoning overriding the nested member, synthesized omitted reasoning, and admitted
unsupported discovery. The complete default table is held by a test; missing
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

## Amendment: model-default reasoning (2026-10-23)

An omitted `models.<role>.reasoning` preference means the selected model's default.
Resolution retains a member with no `value` and `source: model-default`; the provider
request omits its effort member. `none` is an explicit value only and is never
synthesized. A layer-supplied value has `source: layer`.

The host may supply authenticated discovery before admission as
`models.<role>.discovered.reasoning`, with an optional `default` and closed `supported` set.
When reasoning is omitted, that default becomes the resolved value with
`source: discovered`; discovery may omit `default`, in which case reasoning stays
absent. An explicit run or project value still wins by the existing precedence, but
discovery refuses it before generation when `supported` does not advertise it. This
includes explicit `none`; the refusal names the value and selected model and never
substitutes another setting. A table value follows the later table-default amendment.

`policy_resolved` and `checkpoint.policy` carry `source` with the member. A resumed
run therefore rebuilds the same request: an omitted effort remains omitted, and a
discovered default remains the same explicit value. Older members without `source`
are read as layer-supplied for checkpoint compatibility.

## Amendment: table reasoning is a default (2026-10-23)

A reasoning value supplied by the policy table is a default rather than a person or
project preference. When authenticated discovery does not advertise that value,
resolution uses discovery's advertised default with `source: discovered`; if discovery
advertises no default, the resolved member has no value and uses `source: model-default`.
The provider request follows that resolution. A supported table value remains unchanged
with `source: layer`.

Only table-layer values receive this fallback. A run or project value remains explicit,
including `none` or `off`, and is refused when the selected model does not advertise it.
When author reasoning is omitted and discovery is absent, the resolver has no tier from
which to infer a child ceiling; a run-only child or sub-agent tier is therefore admitted
as it was before discovery admission. A known author tier retains the existing project
authorization rule.

## Amendment: delegated role admission (2026-10-23)

A delegated child carries its already admitted role's effective reasoning and actual
discovery evidence into fresh child admission. Raw parent thinking or effort cannot
restore an omitted value. The internal handoff is bound to the child's selected
connection and model; fresh admission must agree with its reasoning and discovery
before the original member provenance is retained. A discovered or table default
therefore remains a default rather than becoming an explicit run preference.

The Go handoff restored from a checkpoint uses the existing closed checkpoint reader
before projecting its saved role. It then applies the same fresh child admission and
consistency checks as a live parent. No wire or checkpoint members are added.

v0.9.1 amendment (#155): the flat budget.json policy-layer reader is retired; use nested policy.budget and policy.critic; any retired root member refuses even beside a canonical member.


### v0.9.1 request value representation (#164)

Request builders receive an explicit operational surface and a private admitted
value view in both languages. The view carries the complete resolved member book,
selected model and provider bindings, normalized endpoint capabilities and phase
tool admission. New host fields stay absent until converted. Critic, failover,
delegation and standalone compaction consume the same admitted role projection.
Role capability lookup stays lazy; omission, request bytes and existing gate
authority are preserved. The compactor's public DTO is unchanged.

Compiler-negative fixtures and declaration-based source guards cover raw reads,
writes, copies, typed keys, helper calls, new fields and new production writers.
Operational exemptions belong to their declaring raw type; a new value with the
same name on another raw type does not inherit that exemption.
Construction of the internal request surfaces is confined to named conversion functions.
These are regression guards, not a language sandbox; deliberate TypeScript any
and Go reflection or unsafe remain outside this contract.

Request-builder source migration: TypeScript internal request sessions read model,
reasoning, capabilities and tool offers through `session.values`; use `forRole`
for a critic and `withModel` for an admitted model name. Raw host EngineSession
inputs keep their existing shape. Go request builders use `ModelID`, `Family`,
`AgentCapability`, `PolicyBinding`, `ResolvedCapabilities`, `RuntimeTools` and
`OfferedHostTools` in place of exported raw fields. `Reasoning(role)` retains its
omission behavior. These internal exported source types change; public host Run
inputs and CompactorRequest DTO fields do not.
