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

This milestone does not add completion validation, accepted-plan drift approval or
the report projector. Those dependent contracts are the next authorized phase.
