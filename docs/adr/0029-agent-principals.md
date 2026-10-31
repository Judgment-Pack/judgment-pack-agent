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
(`configuration`). The model never sees, chooses or changes the chain.

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

**The count is made at the dispatch.** RFC 0030 §2's rule: counting happens after the action-key
lookup, an answer from retained evidence counts nothing, and of the admissions made for one call only
the final one counts. So the gate admits every call with counting unset, which refuses a read the
caches could answer after a revocation too, and the admission that counts is the last await before
the host's execute, below the read caches and the durable writer's lookup. A dispatched call makes at
least two reads, and a served one makes one, uncounted. A counted read that does not admit the call
refuses it there, and nothing is dispatched.

**An acting Runtime call is admitted like any other act.** A Runtime tool the Runtime serves with
effect `act` is read under the agent-host audience as source `runtime`, the name its source record
carries, effect `act`: refused outside the scope, past expiry and after revocation. It is checked with
counting unset before it is asked and before an approved call prepares, and counted immediately before
the physical call. A refused approved call answers with its refusal, as an approved call of a source
does, and the run continues.

**A host tool with no source is held to a delegation that stands.** A scope names sources, so it does
not match such a tool, whose reach is local by its declaration (`act` and `read-external` need a
source). Validity at the host's time and the port's answer still apply, under a read of `source: local`
that is never counted. A question or a delegation (`interaction`) is the package's own interaction with
the person or its children, not the host's dispatch; a delegation is held at the child's start and by
the child's calls.

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
agent, issuing host and software as the actor, the root link's agent, the chain's subject, the
leaf by id and digest, and every link's digest) and are event 3; its checkpoint carries `principal` and is version 4,
whatever a feature writer stamps; its run report is version 3. ADR-0019's identity keeps its form
and meaning beside it. The record never carries a credential, a token, statement bytes or a
label. A run with no delegation writes the earlier versions and members byte for byte. Readers
accept checkpoint 4 and event 3 against the current schemas; checkpoint 3 and event 2 are read
against their inventory frozen at v0.12.0 (`contract/records/published-v012-shapes.json`, pinned
by digest), so neither can carry a principal, and readers that predate these versions refuse
them by ADR-0024 before reading the body.

**A resume keeps its principals.** A delegated checkpoint resumes only with a delegation of the
same subject, root actor and issuing host; a chain re-issued or narrowed for them replaces the record.
Another of the three, a delegated run resumed without its delegation, and an undelegated run resumed
with one are refused with failure code `stale-state`, naming `principal-changed`. In both languages
this holds on a durable store too: TypeScript's durable root makes the comparison before anything of
the run is bound, as its opening of the chain is before the store is touched.

**A run resumed at its handoff opens its parent first.** A restored batch runs its children before the
next provider invocation, so a delegated run, or one restored from a delegated checkpoint, opens its
parent context first, with or without task steps: the resume check runs, and the children start under
the opened principal. On every path a child's start refuses when its run holds a delegation it has not
opened, so a child never runs with no admission (`RequireOpenedPrincipal`). In TypeScript this hold's
call site is unreachable once the parent opens; its helper is pinned, and Go pins the call too.

**A delegated child gets the narrowed link the issuing host signs, never its parent's reach.**
Under a delegation a child starts only with its own chain, and only from a delegation that stands: the
coordinator first reads the parent's standing (`source: local`, tool `delegate_tasks`), so a revoked or
expired parent starts no child and is told why, on a resumed batch as on a first. It then asks the host's child port
(`RunPrincipal.child`, Go `RunPrincipal.Child`) for it at the child's start, naming the host, the
parent's chain digests, the task, the profile and the tools the child is offered, once per task. The
chain answered must be the parent's chain plus exactly one link, verified to the same rules
as any chain, so a link wider than its parent is refused `widened` at the child's start, whatever the
host answered; a chain of another root or with other links above it is `chain-broken`. A parent whose
link sets `mayDelegate` false, or a host with no child port, has no child: `delegation-not-permitted`,
without asking. The host may refuse by name (`delegation-not-permitted`, `not-parent-actor`); a port
that fails, answers another form or answers past its five seconds refuses `admission-unknown`. A
refused child starts nothing: the run records the guardrail, its refusal and the parent's `principal`,
and the task fails `permission` with outcome `not-started`, stating the refusal to the parent. A child
that starts carries its own chain: its calls are admitted under that chain, and `delegated_task` carries
its `principal` as event 3. ADR-0013's rule holds: a child never delegates, so the harness asks for one
level; a chain of more links reaches the child only through its parent's chain.

**A lease holds the principals it hosts.** `run-lease.json` version 2 is a lease and fence with
`principal` `{sub, rootActor, host}` (`$defs/binding` of `run-principal.json`, the form of the
gateway's reservation scope). A delegated run's first lease binds it; a later acquisition by the same
principals is a new epoch as ever, and any other, or a version 1 lease taken over for a delegated run,
or a version 2 row taken without principals, refuses `principal-changed` before the epoch moves, also
after a release, since the row is kept. A fence checks against the row (`principal-changed`), so the
reference effect host refuses a dispatch under other principals and performs nothing, and a session
opening on a fenced store refuses a fence that does not hold the run's principals, in both directions,
before anything is committed. A store kind must keep and return the member: it declares it with the
property `leasePrincipals = true` (Go, the method of `LeasePrincipalStore`), `fencedRunStore` refuses a delegated fence on one that
does not, and the store conformance kit proves it (`lease.principal.*`, including the history and snapshot commits, on any store that has them), and names a store holding the member in another form. The idempotency key names
neither the principals nor the epoch, so it is unchanged. A wait's pins hold the binding, its claim
lease holds it, and a resume's evidence names it: missing evidence for a delegated wait is
`evidence-missing` and another binding is `principal-changed`, never a refresh an admission could
accept. The wait conformance kit's `wait.principal.*` and `wait.restart.principal*` hold it across a
restart.

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
- **The read-only Runtime tools** are not admitted against the scope. A tool is admitted when the
  Runtime serves it with effect `act`. The others on the ceiling are `get_schema`, `list_examples`,
  `get_example`, `validate` and `experimental_test_cases`: they read the Runtime's own published schemas
  and examples, or check the arguments they are given, reach no source of the subject's and change
  nothing, and they carry no source record a scope could name.
- **A host tool with no source is not matched against the scope**, though validity and the port's
  answer always apply (above). The read's `source: local` is the harness's: a port that applies scope
  itself must not refuse it. The package's own tools (retained context, the plan, the budget request,
  memory and draft proposals, skills) reach no source and are not admitted; a child's are the same.
- **A mapping is a gateway source's.** `gatewayScope` is refused at declaration on any other kind,
  since the host dispatches those itself and must count them, and when it names another authority than
  the receipts the source pins.
- **The record adds `rootActor`** beside the RFC's members: the checkpoint needs it to hold a resume
  to RFC §7 rule 2's subject and root actor, which the chain's digests do not name.
- **Expiry or revocation while a run holds its lease refuses the call** and the run continues with
  the refusal; the re-authorization park naming the delegation (ADR-0025 decision 4) is not built.
- **The lease's binding adds the issuing `host`** beside RFC §7 rule 2's subject and root actor: it is
  the form of the gateway's reservation scope, and two hosts' root actors are different principals.
- **The issuing host signs the child's link, not the harness.** The harness holds no signing key, so
  RFC §3's narrowing is the host's act; the harness refuses by name any chain that does not satisfy it.
  With no child port there is no child under a delegation: a delegated run never falls back to running
  a child under its parent's chain.
- **`delegated_task` carries `principal`** as event 3, beside the RFC's tool events: it is the record
  of the chain a child ran under, and the only event that names the link before the child's first call.

## Not built here

Key rotation for an issuing host, revocation statements and the offline `delegation-unresolved`
check are the RFC's open questions and are not built. The certification scenarios do not yet run a
delegated child on every engine; the coordinator's cases are package tests in both languages.

## Evidence

Both languages read the reference gateway's 39 statement vectors (cited by repository, commit and
digest in `contract/testdata/principal/`, copied unchanged) to the gateway's expectations, and the
handwritten agent vectors in `contract/vectors/principal/`: agent-host chains signed by an
independent signer, 33 admission cases with the exact admission read each makes, the record
member, every refusal, the port's answer forms, event 3 records and a report 3. Gate and session
tests in both languages cover a call out of scope, an expired and a revoked delegation, a
revocation between the check and the dispatch, an approval's continuation, an effect in flight, the
undelegated run unchanged, startup refusal and resume. The child cases (`contract/vectors/principal/child.json`)
drive the same narrowing in both languages, and through the real coordinator a narrowed child runs and
is recorded, a widened one and one with no port or permission start nothing, and an undelegated parent
delegates as before. The lease vectors hold version 2 and `principal-changed`; the store and wait
conformance kits hold the binding across takeover, effect admission, claim and restart. Mutation rows
`principal P1` onward hold the safeguards, each with its Go twin.
