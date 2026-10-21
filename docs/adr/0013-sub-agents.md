# ADR-0013: Sub-agents

Status: accepted, 2026-10-21. Issue agent#97, milestone v0.8.0. The maintainer's
accepted decision and its seven decided points. Builds on [ADR-0007](0007-provider-managed-delegation-in-go.md),
[ADR-0009](0009-skills.md), [ADR-0006](0006-tool-sources.md),
[ADR-0011](0011-the-agents-policy-table.md) and [ADR-0012](0012-cross-provider-modelling.md).

## Context

The built-in child profiles describe fixed kinds of work. A host also needs to declare
a named specialist with approved instructions, tools, skills, model and bounds.
Such a declaration must neither broaden the gate nor turn model text into authority.

## Decision

A host declares `subagents[]` beside its tools and skills. `subagent.json` is closed:
`id`, `name`, `purpose`, `instructions`, `source`, `tools`, `skills`, `model`,
`reasoning`, `bounds` and `pinnedDigest`. The package adds `digest` after admission.
The digest is SHA-256 over the canonical JSON bundle, including source and bounds,
excluding `pinnedDigest` and `digest`; the person pins it when approving the bundle.
A changed bundle is withheld until re-approval. Sources use the ADR-0006 record,
with kind `custom`, `gateway`, `mcp` or `exchange`.

Every tool name must be an already declared host wire name and a read. Every skill
id must already be declared and admitted. A declaration grants no tool and raises
no effect. Bounds name `modelRequests`, `agentRuns`, `toolCalls`, `bytes`,
`milliseconds` and `resultBytes`; each only narrows the turn and the existing child
ceiling. Task arguments carry no bounds and cannot raise them. The model and tier
resolve through `models.subagent.<id>` under the policy table and its tier ceiling;
provider selection follows ADR-0012.

`delegate_tasks` lists each admitted id and its purpose's first line beside the fixed
built-in profiles. Purpose lines are framed as data, cut to 160 UTF-8 bytes, with
at most `cataloguedSubagents` (16) entries and `subagentCatalogueBytes` (4,000)
bytes of JSON id and purpose payloads. Definitions outside either bound are withheld.
The task selects a declared id in `profile`. The package attaches the `subagent`
record `{id, digest, source}`; model-supplied records are refused.

The adapter assembles the child's prompt through ADR-0005's assembler, with the
definition's instructions at the project block's rank. The child receives only the
definition's tools and skills, with its own `skillsPerRun` count. It inherits none
of the parent's loaded skills. Empty `skills` loads nothing. This closes ADR-0009's
child-skills deferral.

The seven decided points are:

1. No child `act`. The child returns a proposal as data for the parent to act on
   through the person's approval pause.
2. Skills are available through the definition's `skills[]` only.
3. No nesting: definitions name no sub-agents, and children never delegate.
4. No child critic or `request_review`; the parent's critic reviews the parent's proposal.
5. Source kind `exchange` is supported now. RFC 0019's `subagent` entry kind is a
   separate Exchange amendment when this ADR lands.
6. Built-in profiles remain fixed, declared nowhere and listed beside declarations.
   A host declaring nothing retains the existing behavior and profile bytes.
7. Definition bounds only narrow the turn; the parent cannot raise them in a task.

`delegation_requested` and `delegated_task` carry the approved record, as do the
checkpoint's task records and delegation figures. Reports distinguish built-in
profiles from declared definitions and group definition figures by id and digest.
A queued task restored with a withdrawn or changed definition is not dispatched
again; its interrupted record states why. A running task retains ADR-0007's unknown
outcome rule. Guardrail reasons are `subagent withheld` for admission and restore,
and `subagent refused` for an unknown selection or a child's attempted delegation.

## Certification

Scenario 13 uses scripted declarations on every engine and both Go adapters. It
checks the shared offer, scoped tools and skills, resolved tier and bounds, results
as data, source records, admission failures, changed digest and re-approval, refused
task widening, no nesting, and restored withdrawal. Existing delegation scenarios
hold the built-ins unchanged. Mutation checks remove the tool admission check,
bounds ceiling, digest comparison, record propagation and built-in exclusion;
each must be caught in both languages.

## Consequences

Definitions are versioned, auditable scopes of work. Their purpose lines cost the
parent bounded context; their instructions and loaded skills cost only the child.
The package enforces admission and execution, while hosts keep declaration storage,
approval and provider capabilities.

## What stays the host's

The Desk follow-up supplies the form, project storage with source kind `custom`,
the delegation view distinguishing declared specialists from built-ins, and
re-approval on digest changes. The host fetches Exchange entries; the package does not.

## Not decided here

Act-capable children, nested delegation, child critics, Exchange publication,
Desk forms, and new provider transports are outside this change.
