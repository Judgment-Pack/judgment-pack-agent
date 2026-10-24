# Task plan identities and revisions (#180)

`update_plan` retains its complete ordered list of one to twenty steps and at most one
`in_progress` step. Each current step has an execution-scoped package-issued `id`.
Pass that id to retain a step; omit it for a new step. Text and position never imply
identity. Unknown, repeated and removed ids are refused before mutation. IDs are not
paths, artifact identities, permissions or evidence of success.

`planState.revision` is `sha256:` plus the SHA-256 of UTF-8 canonical JSON of the
complete ordered `plan` array, including ids and every recorded step member. It
excludes explanation, history, sequence and allocator, so there is no digest cycle.
Changing explanation alone preserves the content revision. Each admitted call,
including a no-op, advances `sequence` and appends its explanation and deterministic
diff. The sequence is an ordering counter; storage CAS revisions remain opaque host
tokens and are never substituted for this content digest.

Diff order follows the old list first: removal or changed text/detail, status change,
then reorder for each surviving id. Added rows follow in new-list order. A single id
can have changed, status and reorder rows. Each row carries only relevant recorded
before/after steps or zero-based positions. Forty changes are allowed; overflow is
refused atomically. The newest thirty-two revisions remain, with `discarded` giving
the exact discarded prefix count. `nextId` is an unrecycled monotonic allocator;
eviction cannot permit id reuse. Replacing all twenty steps needs forty diff rows.

Checkpoint reads preserve legacy records and digests. At the controller's next
current write, legacy text steps receive `step-1` through `step-N` in their recorded
order, initial sequence zero, and a content digest. This does not attest historical
completion or grant work authority. Existing callers may omit ids, but that explicitly
creates new identities on each update. Callers retaining steps must carry returned ids.
Historical checkpoint version 1 and unversioned event inventories remain frozen;
these additions belong to current checkpoint 2 and event 1 only.

`plan_revised` carries execution id, sequence, previous/current content revisions,
explanation and the same bounded diff as checkpoint history. The existing root writer
atomically acknowledges the current scoped checkpoint and event before `update_plan`
answers. Its private bounded delivery identity allows exactly one notification without
a second append. It is not serialized or granted by matching event values. Copying a
notice gains no skip authority. Child publication includes the current child checkpoint
in that same root transaction. Guarded tool arguments and an exact event guard check
precede persistence; changing stored plan content through record redaction refuses
rather than publishing a mismatched digest. On recovery the committed log/history
remain authoritative; a process-local pending delivery is not recreated or replayed.


## Active-step completion (#181)

`update_plan` edits the ordered list and may carry unchanged terminal rows. It cannot
introduce `completed` or `not-needed`, reopen a terminal step, or reword its text or
detail. Such a call is refused as `PlanCompletionActionRequired` before any unrelated
list change, identity allocation or history mutation.

`complete_plan_step` closes only the current `in_progress` step. It requires that
step's exact `id` and `expectedRevision` (the content revision exposed in request
state). Its closed input chooses `outcome: completed` with `evidence`, or
`outcome: not-needed` with a nonblank `reason` of at most 1,500 characters. Evidence
and reason are mutually exclusive. Completion evidence holds arrays of operation ids
and artifact references, with at most twenty combined entries and at least one entry.
An artifact reference names its exact kind, id and `sha256:` content digest.

The existing writer's acknowledged observation book supplies evidence. A successful
host or Runtime result needs its matching same-execution, role and child scope
observation. An artifact additionally needs an acknowledged successful read or
production observation for that exact identity and digest. Provider requests,
unknown outcomes, failed results, other roles/children, and matching content under
a different artifact id do not establish that evidence. Request state exposes a
bounded `planEvidence` index of the usable ids and artifact revisions. The callback
profile has no observation owner and reports `available: false`; completion there
refuses as `PlanEvidenceUnavailable`. Closing as not-needed requires no evidence. The completion offer declares these
mutually exclusive branches explicitly.

Evidence records provenance; it does not independently judge whether an operation's
result satisfies the task. Legacy completed rows carry no invented evidence and
remain visibly unverified to report consumers. Repeated exact acknowledged tool
requests retain the storage foundation's replay behavior. A new closure against a
stale revision or a closed/nonactive id refuses. Failed closure leaves the active
step in progress, preserves its identity/history/allocator, and appends a bounded
refusal record plus `plan_completion_refused`. The newest thirty-two refusals survive
resume with their discarded-prefix count.

## Accepted-plan lineage and departures (#182)

A host-selected accepted or started plan seeds pending tasks before the first work
request. Outstanding questions refuse startup. Each reference is verified through
the host's `planResolver` capability; a changed or unresolved pin refuses before
work starts. Agent infers no filesystem or URL authority. The accepted snapshot
retains id, original numeric source revision, objective, steps, references and
acceptance time. `validatedReferences` retains the exact successful resolver results
from initial startup, including mapped historical pins; it remains with the original
snapshot when history rolls over. Pins are rechecked before each resume. Its separate content revision hashes canonical
`{objective, steps, questions: [], references}`. Every seeded task records that
accepted id/content revision and zero-based original index as its origin. Later
list edits preserve those package-owned members.

Accepted-plan text edits, additions, removals and reorders require an explanation
and are recorded for each actual transition of accepted work. A second edit to an
already changed accepted step is another departure and rechecks the policy. Removed
rows retain their execution step ids and original accepted indices. Status and evidence changes
alone are not departures. The newest thirty-two departure records remain with an
explicit discarded-prefix count. The default policy pauses removal of accepted
work; hosts may explicitly select a unique subset of the four departure kinds.
Reordering alone records history under the default policy.

A departure that needs human approval keeps the active plan unchanged and saves a
detached one-revision-ahead candidate in `planState.pending`, with status paused.
`approval_requested` has `purpose: plan-drift` and a closed plan payload naming the
execution, current and candidate content revisions, explanation and departures.
It has no tool/source/args authority. The old tool-call approval payload remains
unchanged when purpose is absent or `tool-call`.

The host resumes with the existing `context.approval` decision for that exact
pending call id. The package verifies current revision, candidate history and
accepted lineage before it applies approval once or discards a rejected candidate.
It persists the decision and `plan_drift_decided` before further dispatch. Request
state carries the latest plan decision so rejection and its bounded human note
remain visible on continuation; that data cannot approve a host action. The
newest thirty-two decisions and discarded-prefix count survive resume. An old,
unrelated or repeated decision refuses; it cannot approve or execute a host act
call. Checkpoint validation rejects nested pending proposals, inconsistent origins,
mutated accepted content, or stale pending bindings.

The new reserved tool name `complete_plan_step` is a public compatibility change:
hosts using that name must rename their tool. Model callers that previously marked
new terminal rows in `update_plan` must migrate to the separate active-step action.
Current checkpoint2/event1 readers and generated types include the new fields;
historical reader inventories remain frozen. The report projector copies these
records and retention markers without synthesizing proof or acceptance.
