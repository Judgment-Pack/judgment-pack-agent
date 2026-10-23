# ADR-0012: Cross-provider modelling

Status: accepted, 2026-10-21. Issue agent#117, milestone v0.8.0. Drafted from the
maintainer's accepted decision. Builds on [ADR-0011](0011-the-agents-policy-table.md)
and [ADR-0010](0010-the-critics-switch.md).

## Context

The policy table resolves the model and reasoning tier for each role, but model
selection within one connection does not establish independence between providers.
The host owns connections and credentials. The package needs an enforceable choice
of provider, a common tier comparison, and a record of which family answered.

## Decision

Every role the package runs has a provider in `models.<role>.provider`: `author`,
`critic`, each `child.<profile>`, and each declared `subagent.<id>`. The member uses
the policy table's per-member precedence, **run > project > table**, and defaults
to `same` for every role. The critic's provider is under `models.critic`.

- `same` selects the author's connection.
- `other` selects a host-supplied connection whose family differs from the author's.
- A connection ID selects that host-supplied connection.

An unmet provider policy refuses admission with `policy refused`, naming the role
and the supplying layer. In particular, `other` with only one connected family
never falls back to `same`. The person can add a connection and try again. Model
text, tools, child output, and sub-agent output cannot select a provider.

Children and declared sub-agents may run on another family under this policy.
Their findings return as data, with the answering family recorded. The existing
tool gates and result framing continue to apply.

### Tiers and wires

Capability records carry `family` (`openai`, `anthropic`, `gemini`, or `codex`) and
`tiers`, mapping `low`, `medium`, and `high` to strings naming that family's own
settings. Each wire encoder reads the map. The host may override it per connection;
the package never guesses a setting. The common tiers make the critic's floor,
"at least the author's tier", comparable across families.

The generated reference includes the tier map and a wire-compatibility matrix per
family: cache breakpoints, thinking, tool-schema narrowing, and images. Both tables
are read from capability records and checked for drift by reference generation.

Cross-family failover is refused with a recorded reason: the prefix and cache do
not carry to another family. Failover within a family belongs to agent#104 and is
not implemented by this decision. An explicit provider binding for a role is not
failover.

### Contract and records

1. `policy.json` adds `models.<role>.provider`, default `same`, resolved with its
   supplying layer alongside the other role members.
2. `capability.json` adds `family` and `tiers`.
3. `run-event.json` adds `provider: {connection, family}` to `critique` and every
   `delegated_task`. `policy_resolved` lists the provider per role with its layer.
   An unmet provider uses the existing `policy refused` guardrail reason;
   `connection_switched` remains agent#104's work.
4. `provenance.json` adds the report's `crossProvider` row per role, identifying
   `same` or `other` by comparing the role's family with the author's family.
5. Certification output prints the two-family measurement beside ADR-0010's
   two-tier measurement, for the same proposal. The measurement informs the host;
   it does not choose a provider or change the critic's verdict.

## Certification

Scenario 12 uses scripted wires with two families connected on every TypeScript
engine and both Go adapters. It holds these behaviors:

- `critic: other` runs on the other family and the record says `other`.
- `other` with one connected family refuses with its reason.
- An explicit connection ID is honored.
- Each wire's encoded setting for `high` matches its family's tier map.
- A child on another family returns data with its family recorded.
- The two-family measurement table is printed beside the two-tier table.
- Regenerating the compatibility matrix produces no diff.

Hand-authored provider vectors hold the decided behaviors. Mutation checks must
catch `other` falling back to `same`, an omitted family record, an encoder ignoring
the tier map, and a hand-edited generated matrix. The Go twins are checked too.

## Consequences

Provider selection is an admission and security boundary, shared by both languages.
A host must supply a connection that satisfies policy before a role can run. The
record identifies the provider that answered rather than relying on a model name
or on a model's claim about itself. Wire-specific capabilities remain explicit.

## What stays the host's

Connection discovery, credentials, per-connection tier overrides, and forms remain
with the host. The Desk follow-up places providers on the project's providers page
and the critic's provider beside its switch in the composer.

## Not decided here

Within-family failover (agent#104), cross-family cache or prefix transfer, automatic
provider choice by a model, and the Desk's forms are outside this decision.

## Implementation vocabulary

The resolved provider member records the selected connection ID and its supplying
layer. `other` selects the first host-supplied eligible binding (Go sorts connection
IDs); no request or model text supplies candidates. Model engines accept the common
`low`, `medium`, and `high` tiers alongside the existing `off`, `on`, and `ultra`
spellings; `on` and `medium` share a rank, as do `ultra` and `high`. A connection's
`tiers` overrides its three primary wire settings as a complete map. The generated
[provider reference](../reference/providers.md) states those settings and defaults.

A `models.<role>.model` setting never changes the provider default: selecting
another connection requires provider policy. The host's `modelsFor` model names
on the author's connection continue to select subordinate models on that
connection.

`crossProvider` is carried in the execution checkpoint alongside the existing
report rows. Each row names the role, its `same`/`other` relation, the author's
connection and family, and the role's connection and family. Historical events
without a provider remain readable; newly emitted critique and task records carry
one. Model and agent bindings remain distinct capabilities: a model engine refuses
an agent binding, and an agent engine refuses a model binding before dispatch.
The agent host can supply provider-managed invocations from another family.

### Review round two: continuation and fallback dialects

Resume admission compares the saved provider families with the current bindings,
even when connection IDs are unchanged. A cross-family continuation emits `policy
refused` before dispatch. Its stopped checkpoint retains the saved identities and
marks all saved `crossProvider` rows `continued: false`.

A selected critic binding opens its thinking slot from that connection's capabilities,
including when its family and tier equal the author's. The identical binding and tier
retain learned refusals.

The optional capability member `fallbackTiers` maps `low`, `medium`, and `high`
to the fallback dialect's settings. Anthropic enabled defaults to token budgets
`4000`, `8000`, `16000`; Gemini level defaults to `low`, `medium`, `high`.
Connection overrides replace this map just as they replace `tiers`. Both fallback
encoders read it; there is no fallback exception. Off retains its existing omission
or minimum-setting behavior. Shared vectors specify all three default and overridden
settings for both dialects in both implementations.

## Amendment: discovered reasoning defaults (2026-10-23)

Provider discovery may advertise the selected model's reasoning default and supported
values before policy admission. The common policy contract owns that input; an engine
does not rewrite an omitted preference after resolution. Without discovery, omission
uses the model default by leaving the wire member absent. With discovery, the advertised
default is sent and recorded. An explicit run or project value, including `none`, is
checked against the selected model's advertised set and refused before generation when
unsupported; table values follow ADR-0011's table-default amendment.

The resolved member's `source` distinguishes a policy layer, the undiscovered model
default, and a discovered default. Provider failover and resume retain that member so
the replacement or resumed request is rebuilt from the same value and provenance.

## Amendment: table defaults across providers (2026-10-23)

The table-default rule recorded in ADR-0011 applies before a provider request is built.
Consequently a provider change or discovered capability set cannot turn a table default
into an explicit unsupported preference. Run and project values remain explicit and are
never replaced. The resolved value and `source` continue through failover and resume, so
the same provider request members are rebuilt.
