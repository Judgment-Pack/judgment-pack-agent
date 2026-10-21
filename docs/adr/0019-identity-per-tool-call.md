# ADR-0019: Identity per tool call

Status: accepted, 2026-10-21. Issue agent#102, milestone v0.8.0. The maintainer's
accepted decision builds on [ADR-0006](0006-tool-sources.md) and
[ADR-0008](0008-the-approval-pause.md).

## Context

A source record says where a host tool comes from, but not whose authority the
connection is using for a particular call. A gateway may hold a person's token,
a service identity, or only the connection's own identity. The person needs that
fact before an action, on the event record, and in the run figures. It cannot be
left to model arguments because identity is a host fact, not a model choice.

## Decision

The source record gains `identity: {kind, label}`. `kind` is `person`, `service`
or `connection`; `label` is a name the person recognises and is never a credential
or token. The host declares identity per source. Where it says nothing, the
package uses `{kind: "connection", label: source.id}`. The package derives and
carries `identityDigest` as the identity pin beside a tool-list digest.

A declaration whose label matches a recognisable credential is withheld before
any offer, event, figure or checkpoint can carry the label. Its refused guardrail
uses generic text. The credential patterns are ADR-0017's version 1 set, copied
in the identity code until the shared set is carried into this branch, plus
credential assignments such as `api_key=...`.

The offer's framed description begins with `runs as: <label>`. Every
`tool_call` and `tool_result` of the source carries the source record with the
identity and its digest. The source figures count calls under each identity. An
`identity` member in model-supplied tool arguments remains an ordinary argument
and cannot replace the host's record.

Where the host can report the current identity, the package checks it before
each call. A changed identity withholds the source for the rest of the run until
the person re-approves it, and emits a refused guardrail with reason
`identity changed`, just as a changed MCP tool list is withheld.
The checkpoint's `sourceIdentities` stores each source's `source`,
`identityDigest`, `withheld` latch and `serviceAsked` history. Restoring the
checkpoint keeps the original pin and withholding even if the original identity
returns or the host changes the declaration. Re-approval means the host supplies
fresh approved declarations in a new run with fresh identity state.

The first `act` call of each source under a `service` identity is always asked
once per run, including a declaration with `approval: none` (implicit `allow`).
If the host approval policy answers `allow`, the package changes
the answer to `ask` and emits a rewritten guardrail with reason
`approval downgraded`. Later act calls follow the host policy. Reads do not
satisfy this first-act rule. The source is marked as asked only after the approval
book records the request; without a book, repeated calls are refused. A failed
recording does not consume the safeguard. A policy refusal still refuses the call.
The per-source history is separate from the latest approval, so resuming after
another source's approval does not ask an already-asked source again under `allow`.
Identity is reported and counted; beyond this
first-service-action safeguard, the label grants nothing.

## Certification

Scenario 19 runs on every TypeScript engine and both Go adapters. It checks the
identity on the framed offer, every source event and the figures; ignores an
identity supplied in model arguments; withholds a changed identity; and asks the
first service act where the policy said `allow`, recording the downgrade.

Handwritten vectors under `contract/vectors/identity/` cover the offer, call and
result, figures, changed identity, first service act, credential labels and saved
identity state. Both languages test `approval: none`, repeated calls without an
approval book, credential refusal before publication, and restoration of the
withholding latch, identity pin and two sources' approval history. Three mutants must be
caught: identity dropped from a result, an identity change not withheld, and the
service downgrade removed. Three fix-round mutants also bypass service approval
with `none`, consume the first ask without a book, and publish a credential label.
Their Go twins run on disposable copies.

## Consequences

Hosts must supply only a recognisable label, never secret material, and may
supply a current-identity reader where identity can change. The Desk's connection
form is follow-up work. The model receives identity as evidence but receives no
identity selector and no grant from the label.
