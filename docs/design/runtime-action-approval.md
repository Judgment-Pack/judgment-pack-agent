# Runtime action approval

Agent issue #214 opens one additional admission path under ADR-0027: an acting
Runtime evaluation can ask for an exact person decision through the original
run store. The actual connector remains the execution owner. A tool name,
owner taxonomy, source record or serialized approval never constitutes a live
dispatch grant.

## Trusted host port

TypeScript hosts supply `runtimeActions` to `openAssistantConnection`. Go hosts
supply `gate.Options.RuntimeActions` before `Gate.Open`. The port identifies the
verified immutable artifact, unpredictable process launch, project, actual server
session, effective principal and effective resource scope. Each observation must
identify the execution reached by that connection. The host must prevent or signal
retargeting between observation and transport; a cached configuration, binary
version or browser connection id is insufficient. Project, principal and scope
labels explain the request to the person and confer no authority.

Absent host policy means ask. A configured policy may return allow, refuse or ask;
throwing or any other answer refuses. A policy receives detached canonical argument
and owner copies. An arbitrary call callback, a direct contextual acting call, or a
connector without the trusted port cannot dispatch an action. Read and pure tools
retain their existing execution path.

## Exact durable continuation

The actual connector reads every page of the served tool list, including unselected
siblings, and applies the ordinary wire inspection and rehearsal rewrite before
approval. Its list projection preserves order and contains each name, optional
description and input schema, and owner taxonomy. Repeated names, repeated cursors
and an excessive bounded list refuse. Acting evaluation retains `rehearsal: true`;
this feature does not disable the Runtime's audit rules or grant a nonrehearsal path.

One in-progress approval preparation is reserved synchronously across host and
Runtime action decisions. An ask also claims the native invocation's pause before
writing it. Current checkpoint 3 approval and event 2 approval requests carry the
closed `runtime: {owner, admissionDigest}` member. The original writer saves the
exact asked call with paused status and acknowledges it before an actionable
request is exposed. A lost pause acknowledgement exposes no actionable request
and sends no Runtime call.

A separate person decision resumes only the stored exact pending call. The
original writer acknowledges approved state before constructing a fresh private
attempt. Admission pins the actual execution and chat turn, role and task, phase,
effective policy and child permissions, final admitted Runtime offer, full source
record, exact owner and canonical rewritten arguments. The source identity digest
keeps its existing meaning: the digest of its source identity only.

Immediately before the physical call, the package repeats live authority,
cancellation, owner, complete list, exact canonical arguments and current admission
checks. It consumes the private attempt synchronously, then sends only the retained
exact name and argument bytes. The original writer records kind `runtime` with
`read: false`; its canonical request includes version, call id, name, arguments,
source and Runtime binding. An uncertain claim or unacknowledged result never
replays automatically. This is at most one admitted client dispatch, not a promise
of exactly-once external effects.

The admission preimage is canonical JSON of version 1, owner, source, execution,
turn, role, task (null when absent), phase, effectivePermissions and
finalRuntimeOffer. Objects sort keys by UTF-16 code units; arrays retain order;
omitted properties remain omitted; no case, whitespace or Unicode normalization
is applied. Finite numbers use the existing JavaScript-compatible JSON writer;
integer values must be exact safe integers. Shared handwritten fixtures cover
non-ASCII keys, decimals, maximum safe integers, negative zero and omission.

Published checkpoint 2, event 1, policy 1 and report 1 graphs remain frozen.
Current Runtime-only checkpoints project report 2 without usage totals unless an
actual usage journal is present. Recording and history observations are descriptive
and cannot recreate a private acting attempt.

## Validation scope

The paired acceptance tests enter the exported TypeScript model/native adapters
and Go Eino/native adapters with the original MemoryRunStore and a bounded actual
MCP protocol transport. They cover default asks, acknowledged pause and decision,
exact dispatch, stale owner/list/phase/enrollment, lost acknowledgements, uncertain
outcomes, policy isolation and portable record readers. A deterministic provider
fixture is not a live model measurement. The selected [Runtime v0.39.0-alpha.1](https://github.com/Judgment-Pack/judgment-pack-runtime/releases/tag/v0.39.0-alpha.1) producer is published and verified, with bounded consumer evidence. Final Agent artifact pairing remains required; protocol fixtures alone do not establish that artifact's identity or audit behavior. This alpha does not include the later #171 dialect changes.
