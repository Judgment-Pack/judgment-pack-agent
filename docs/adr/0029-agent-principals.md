# ADR-0029: Agent principals and delegated authority

Status: accepted for v0.13.0. Issue #260. Builds on [ADR-0006](0006-tool-sources.md),
[ADR-0008](0008-the-approval-pause.md), [ADR-0013](0013-sub-agents.md),
[ADR-0019](0019-identity-per-tool-call.md), [ADR-0024](0024-versioned-record-readers.md),
[ADR-0025](0025-unattended-runs.md) and [ADR-0027](0027-tool-taxonomy.md). It is the agent
harness's row of spec RFC 0030 (agent principals and delegated authority), which is a Draft:
where this decision builds against it, it builds against a Draft, and the text may change.

## Context

ADR-0019's identity labels whose credential a call runs under; it grants nothing and names no
party using the credential. An agent reaching a source with a person's credential is
indistinguishable from the person, its reach is the credential's whole reach, a delegated
child receives its parent's whole reach, and nothing records on whose behalf it acted. RFC 0030
proposes a registered agent principal acting under a signed, bounded delegation chain from an
issuing host. The reference gateway enforces it for gateway sources (its `SPEC.md` §11). For a
host source the agent harness is the enforcing host.

## Decision

**The run carries its delegation.** A host opts a run in with `ContextInput.principal` (Go
`Input.Principal`): the chain's JSON text as the issuing host issued it, the issuing hosts'
pinned Ed25519 keys, the software the host states the run runs, the agent-host name it declares
for its host sources, an admission port, and the host's clock. The package verifies the chain
before anything is offered or asked: RFC 0030's steps 1 to 7 exactly as the gateway's §11.1
reads them (`canon` with code-point order, keys held to the gateway's §8.4 key rule,
Ed25519 over `judgment-pack/delegation/1:`, the narrowing clauses 0 to 8 including
`mayDelegate`, a lifetime of at most 24 hours, at most five links), and the leaf's software
against the run's (`software-mismatch`). A refused chain ends the run before any request
(`configuration`). The model never sees, chooses or changes the chain; a delegated child acts
under its parent's.

**Every call of a tool with a source is admitted immediately before dispatch.** At the host's
time the package applies step 8 (`not-yet-valid`, `expired`; equal time is expired) and its own
part of step 11 (`out-of-scope` with reason `source`, `tool`, `effect` or `limit-unknown`, the
first failing reason root first), then hands the host's admission port the gateway's admission
read body: the issuing host, the audience, the chain's digests, the call and the time. The port
answers from the issuing host's state, read afresh: inventory, issuance, revocation, the `calls`
counters and the subject's standing. Nothing is cached in the package. An answer of another form,
a throw, or none within five seconds refuses the call (`admission-unknown`). The port's refusals
of steps 8 to 10 are reported before the package's own, and the package's own before the port's
step 11 or 12. A source of kind `gateway` is checked against the entry of the gateway authority
and source name its declaration maps it to (`ToolSource.gatewayScope`), without counting, since
the gateway counts its own dispatches; a `gateway` source with no mapping is refused (`source`).
Any other source is this host's own, under its agent-host audience and its source id, and its
dispatch is counted. The effect is the owner's ADR-0027 declaration, never a model argument.

**Approval carries no authority past the delegation.** Before an act call's approval is asked,
the call is checked with counting unset; one the delegation does not admit is never put to the
person. An approved call is admitted again, counted, immediately before it runs. A revocation
landing between a check and a dispatch therefore refuses the call at its re-check. A call already
dispatched is not undone by a later revocation: its outcome follows the existing uncertain-action
rules, and the next admission refuses.

**Refusals are named and recorded.** A refused call earns a guardrail with reason
`delegation refused`, its `refusal` (`run-principal.json#/$defs/refusal`) and the run's
`principal`; the model is told the refusal by name and that nothing was changed.

**Records carry actor and subject under new versions.** A delegated run's `tool_call`,
`tool_result` and delegation refusals carry `principal` (`run-principal.json`: the leaf's
agent, issuing host and software as the actor, the chain's subject, the leaf by id and digest,
and every link's digest) and are event 3; its checkpoint carries `principal` and is version 4,
whatever a feature writer stamps; its run report is version 3. ADR-0019's identity keeps its form
and meaning beside it. The record never carries a credential, a token, statement bytes or a
label. A run with no delegation writes the earlier versions and members byte for byte. Readers
accept checkpoint 4 and event 3 against the current schemas; checkpoint 3 and event 2 are read
against their inventory frozen at v0.12.0 (`contract/records/published-v012-shapes.json`, pinned
by digest), so neither can carry a principal, and readers that predate these versions refuse
them by ADR-0024 before reading the body.

**A resume keeps its principals.** A delegated checkpoint resumes only with a delegation of the
same subject and agent principal; a re-issued chain for them replaces the record. Another subject
or agent, a delegated run resumed without its delegation, and an undelegated run resumed with
one are refused (`principal-changed`).

## Departures from RFC 0030

- **Signing and admission are the host's, not a credential.** The harness binds no credential
  (RFC §2, *The credential*): for a host source it is the enforcing host, and step 0 is met by
  holding only the chain the host supplied at start. Revocation, inventory, issuance, standing and
  counters are the host's admission port's answers; the package keeps none of them.
- **Steps 9 and 12 locally.** The harness compares only the leaf's software with the run's.
  ADR-0019's `person` identity names a label, not an issuer and subject, so `subject-mismatch`
  is the issuing host's answer.
- **Source-declared limits.** The harness declares no `x-` limit: one is `limit-unknown`, as at the
  reference gateway.
- **Which records carry `principal`.** `tool_call`, `tool_result`, delegation refusals, the
  checkpoint and the report; other run events do not, since the checkpoint names the run's
  principal once.
- **The mapping is the declaration's.** A gateway source's audience and source name are on the
  host's `ToolSource` declaration, not on the ADR-0006 source record, so undelegated source records
  are unchanged.
- **The Runtime's own tools** are not admitted against the scope: a scope names sources, and the
  Runtime's tools have none. Acting Runtime metadata keeps its own approval path.
- **A resume compares subject and agent principal**, not the root actor, since the checkpoint names
  the leaf's actor; the lease's principal binding (the root actor) is the stacked follow-up below.
- **Expiry or revocation while a run holds its lease refuses the call** and the run continues with
  the refusal; the re-authorization park naming the delegation (ADR-0025 decision 4) is not built.

## Not built here

Narrowed child links issued by the host for a delegated child and their widening refusal at the
child's start, the lease's principal binding (`principal-changed` at takeover and effect
admission, binding with the resume revalidation of #266) and the wait conformance kit's delegation
properties are the stacked follow-up pull request. Key rotation for an issuing host, revocation
statements and the offline `delegation-unresolved` check are the RFC's open questions and are not
built.

## Evidence

Both languages read the reference gateway's 39 statement vectors (cited by repository, commit and
digest in `contract/testdata/principal/`, copied unchanged) to the gateway's expectations, and the
handwritten agent vectors in `contract/vectors/principal/`: agent-host chains signed by an
independent signer, 33 admission cases with the exact admission read each makes, the record
member, every refusal, the port's answer forms, event 3 records and a report 3. Gate and session
tests in both languages cover a call out of scope, an expired and a revoked delegation, a
revocation between the check and the dispatch, an approval's continuation, an effect in flight, a
child under its parent's principal, the undelegated run unchanged, startup refusal and resume.
Mutation rows `principal P1` to `P21` hold the safeguards, each with its Go twin.
