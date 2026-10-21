# ADR-0020: Provider failover and the usage ledger

Status: accepted, 2026-10-21. Issue agent#104, milestone v0.8.0. The maintainer's
accepted decision builds on [ADR-0011](0011-the-agents-policy-table.md) and
[ADR-0012](0012-cross-provider-modelling.md).

## Context

The policy table binds each role to a host connection, and the cross-provider
contract records its family. A transient provider failure still ended the run,
and the existing figures did not form one closed, per-run ledger. The package
needs bounded recovery without moving a cached prefix across provider families
or manufacturing figures a wire omitted.

## Decision

`models.<role>.fallback` is an ordered list of connected IDs. Every fallback must
have the selected connection's family; a cross-family entry is refused at
admission with `policy refused` and a `failover refused` guardrail. The default
`failover.classes` is exactly `unavailable`, `rate-limited`, and `timeout`.

For one logical request, the ordinary refusal retry rule runs first. If the final
failure has a qualifying class, the adapter sends the complete request to the
next fallback, emits `connection_switched {role, from, to, reason}` before that
dispatch, and rebuilds the stable prefix through that connection. The fallback's
retry count starts at one. A request switches at most once. A refusal, content
filter, invalid request, or any class outside the policy never switches.

A provider-managed agent account is exclusive during delegation. A qualifying
failure there does not move a child or continuation to another connection: the
switch is refused and its `failover refused` guardrail records the reason.

Every run emits one `usage {type, usage}` event immediately before `end` and
stores the same closed `usage.json` ledger in its checkpoint. Each row identifies
a source, connection and role and counts model requests, provider-managed
invocations, observed milliseconds and bytes. Token members are present only
where the provider reported them; the package never estimates them. Completion
rows identify the connection and role that answered. Prices and project totals
remain the host's.

## Certification

Scenario 20 runs on every TypeScript engine and both Go adapters. Scripted wires
hold a qualifying failure and same-family switch, the switch event and rebuilt
prefix, a refusal that does not switch, cross-family admission refusal,
mid-delegation refusal on the agent capability, and a usage event with token
members absent where the wire reported none. Handwritten vectors under
`contract/vectors/failover/` cover those five decisions.

Four mutants must be caught in both implementations: treating a refusal as a
qualifying failure, omitting the switch event, inventing token counts, and
removing the cross-family admission check.

## Consequences

Availability can improve within one provider family without weakening policy.
The switch creates another request and another cache write, both visible in the
ledger. Hosts can price and aggregate the exact reported counts without putting
credentials, prices or project accounting into the package.

## What stays the host's

Connections, credentials, capacity, prices, project totals and the report's
presentation remain host-owned. The package owns admission, switching, events,
checkpoint continuity and the per-run count ledger.
