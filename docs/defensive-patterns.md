# Defensive patterns

These rules record bug classes encountered here and the concrete enforcement that
holds each rule. A new [postmortem](postmortem/README.md) must add an entry or amend
the relevant one, including its test/guard and any remaining limits. Documentation
alone does not establish enforcement; keep the references accurate when code moves.

## Request builders read only the resolved boundary (#148, #164)

**Class:** raw configuration restores a default that admission resolved to omission,
or supplies another request value after the corresponding policy decision.

**Rule:** convert admitted policy into the request representation once; builders
read only that representation. Preserve omitted values through critic, fallback,
delegation and resume. Do not spread raw sessions back into requests. Every newly
resolved request value belongs to the same boundary.

**Enforcement:** branded TypeScript request sessions and Go's private request-session
fields/accessors, plus typed source guards over production builders. The
[TypeScript request guard tests](../packages/harness/policySession.test.ts) cover
raw reads, declaration-owned conversion exemptions, copy/helper forms and a future
field/new writer. [Go request-session tests](../go/adapter/request_session_test.go)
include `TestRequestSessionRejectsRawResolvedValues` and
`TestRequestValuesAddedFieldAndNewWriter`. Request/omission vectors and actual
request/resume checks corroborate behavior. Deliberate TypeScript `any` and Go
reflection/unsafe remain disclosed escapes; source guards are not language sandboxes.
See [postmortem 0001](postmortem/0001-reasoning-omission.md).

## Checkpoint writes use the mutation API (#154)

**Class:** an aliased/nested checkpoint write bypasses validation, revision,
ownership or resume accounting; a detached read looks writable but commits nothing.

**Rule:** read detached snapshots. Publish each changed member through its owning
`controller.mutate` / `mutateCheckpoint` (TypeScript) or `MutateCheckpoint` (Go).
Validate and commit one changed aggregate atomically under its owner; do not write
through `State()` or move a member between unrelated owners.

**Enforcement:** [TypeScript canonical mutation tests](../packages/harness/execution/mutation.test.ts)
check detached reads, atomic revisions, production writer discovery, aliases,
computed/nested members and correct owner publication, plus every schema member
through interrupted initialization. [Go aggregate tests](../go/context/checkpoint_mutation_test.go)
cover atomicity, detachment, concurrent counters and resume; the
[Go source guard](../go/adapter/checkpoint_mutation_test.go) includes
`TestCheckpointWritersUseMutationBoundary` and exact declaration-owner exemptions.
Guard fixtures exercise actual source writes; arbitrary payloads retain their
schema-declared semantics.

## Composed permission policy only narrows

**Class:** a later phase/profile layer accidentally re-allows a tool an earlier
permission boundary refused, for example a critic ending inside Plan mode.

**Rule:** compose permission/offer layers by intersection. Each added layer may
remove authority; it cannot supply a previously unoffered name or lift the outer
phase. Ending an inner phase restores only the still-applicable outer ceiling.
This is the permission-composition rule; scalar run/project/table settings retain
their separately documented precedence.

**Enforcement:** the [TypeScript phase-offer/gate vector tests](../packages/harness/toolGate.test.ts)
compare loop, Plan, critic, reformat and child offers against
[shared phase vectors](../contract/vectors/tool-offer/phase-offers.json).
[Go gate tests](../go/gate/gate_test.go) include
`TestPlanModeCannotBeLiftedFromTheAdapterHandle`,
`TestAGrantedNameTheLoopWasNotOfferedIsRefusedBeforeAnyCall` and the narrow-handle
method inventory. The [Go phase implementation](../go/gate/session.go) retains the
host-entered phase while applying the adapter's narrower phase. These tests hold
package composition; hosts must keep their own authorization checks in force.

## An approval nobody can answer is a refusal

**Class:** an act reaches execution without a recordable decision, or an approval
pause is offered from a phase that cannot own it.

**Rule:** `ask` never means allow. Without a place to record/continue approval,
refuse the act without execution. Children, Plan, critic and reformat phases
cannot create an act approval. Execute an approved continuation only after the
host acknowledges its durable writes; an unknown outcome is not permission to retry.

**Enforcement:** [TypeScript source-gate tests](../packages/harness/toolSources.test.ts)
cover missing approval books (including a second service act), invalid/throwing
policy answers, closed phases and once-only decisions. The
[Go source-gate tests](../go/context/sources_test.go) check missing/unavailable
approval books and `TestUnrecordedServiceApprovalDoesNotConsumeSafeguard`.
[Go approval lifecycle tests](../go/context/approval_test.go) include
`TestAnApprovedCallRunsOnlyWhereTheHostAcknowledgesItsWrites` and
`TestAnActCallThatLosesThePauseLeavesNoAskedApproval`.
The [TypeScript managed boundary](../packages/harness/context/managed.ts) refuses
approved execution without acknowledged persistence. These guards do not certify
that a host has presented a usable human approval interface; the host owns that seam.
