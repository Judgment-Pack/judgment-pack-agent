# Checkpoint mutation

The checkpoint wire schema and lifecycle rules are unchanged. All live checkpoint writes now
pass through one mutation implementation in each language. A patch replaces explicitly named
members together. The implementation copies incoming values, checks a detached candidate and
publishes it only after validation succeeds. One changed patch advances the live revision once;
an equal patch or a rejected candidate leaves it unchanged. Object member order alone is
not a change; array order and omission remain significant. The revision is local bookkeeping,
never a checkpoint member.

In TypeScript, `ExecutionController.state` is a getter returning a detached checkpoint. Replace
`controller.state.reason = reason` with `controller.mutate({reason})`. For a nested edit, read the
member, edit that copy, then pass it in a patch. `undefined` removes an optional member; an empty
string, zero, empty array and nested JSON null retain their existing meanings. The shared
`mutateCheckpoint` implementation also serves reader normalization and detached projections.
`CheckpointMutation` holds the private live value and exposes its revision. Runtime patches
reject unknown schema members and invalid counter operations before publication. Initialization
accepts the validated reader result, preserving existing opaque members of the open checkpoint;
unrelated patches carry those members unchanged.

In Go, the exported `ExecutionController.State` field becomes `State()`, which returns a detached
`*Execution`. Replace `controller.State.Reason = &reason` with
`controller.MutateCheckpoint(runcontext.CheckpointPatch{"Reason": &reason})`, and handle the error
at source API boundaries. `CheckpointRevision()` reports the live revision. The shared generic
`runcontext.MutateCheckpoint` applies the same operation to detached `Execution` and
`contract.Checkpoint` projections. A patch names Go fields and rejects unknown fields and wrong
types atomically; nil clears an optional field. These are source API migrations, including code
that previously assigned the whole controller state. They do not change serialized records.

For counters shared by producers, use `new CheckpointIncrement(1)` in a TypeScript patch or
`runcontext.CheckpointIncrement(1)` in a Go patch. The increment reads the current member inside
the mutation boundary. Reading a counter and later replacing it could lose another producer's
update. Go serializes the live boundary and usage accounting; source, skill, approval and
delegation owners retain their existing locks. Detached reads alone do not provide an atomic
read-modify-write operation for a shared book.

Sources, identity pins, skills, memory proposals, drafts and output counts remain owned by their
existing gates. Those gates edit their own copies under their existing ownership rules and
publish explicit member patches. Helper APIs that receive a detached state getter must also
receive their write/publish callback: for example, `SkillRun.write` in TypeScript and the optional
writer passed to `NewSkillGate` or `NewCriticSwitch` in Go. The session supplies those callbacks;
standalone helpers can continue to own their own raw checkpoint. Their mutable working books are never aliases of the canonical
checkpoint. Controller journal entries, prompts, usage and steers follow the same copy-and-commit
rule. Managed completion, approval continuation and adapter accounting use that boundary too.
Future lifecycle changes should validate a candidate book completely before submitting its patch;
a validation callback receives a copy, cannot make committed edits and must not call the live
Go store recursively. This change adds no memory lifecycle or approval behavior.

Initialization, reader normalization, allowance extensions and snapshot/delegation projections
use the shared mutation implementation. Reader materialization constructs a fresh object before
it enters the live store. Snapshot status and retained ledger context are projections. The record
writer still regenerates schema/version, digest and storage redaction figures, retaining the
existing expiry when one was carried. The Go delegation coordinator still owns handoff and
projects it after restoring its own state. Those ownership rules are deliberate and unchanged.

The source-wide guards discover production files and use the language type checker to reject
direct aggregate writes, including aliases, computed TypeScript keys and direct nested writes.
Nested member and array aliases retain their originating book. An explicit later patch of that
same book under its own member by its originating controller or raw aggregate permits a
working-copy edit; a different owner or member, a fresh read, an uncalled closure or an earlier
publication does not. Stable receiver and aggregate aliases retain that owner identity;
reassigned or unresolved aliases receive no publication credit from an earlier value. A generic
mutator names the raw aggregate it publishes; Go's validated initialization projection is
attached to its controller only inside the owning Initialize implementation.
Inline synchronous map/forEach/filter/reduce callback element and array parameters retain their
collection's origin. Async callbacks keep their own publication scope. The scanner also
recognizes the owning source-book publisher and Go deferred publication closures.
Implementation and reader-materialization exemptions name their exact owning paths. These typed
structural guards cover the documented forms; they do not prove arbitrary control flow.
They also reject a new writer file without adding it to a filename inventory. Go's private live
store is accessible only in the mutation implementation. Both languages test nested alias
isolation, unchanged/rejected revisions and concurrent counter producers.

`contract/vectors/checkpoint/members.json` explicitly assigns every top-level schema member to
its owning probe. Its values are hand authored. The tests derive the member inventory from the
unchanged schema, so a new member without a fixture and an owning probe fails. Controller members
are checked as identical canonical JSON bytes across an interrupted snapshot, restored controller
and continued snapshot. Omission is checked separately from allowed empty and zero defaults;
nested JSON null is retained. Envelope tests exercise the actual snapshot writer's existing
regeneration policy. The Go handoff probe restores the interrupted coordinator and uses its real
checkpoint projection; TypeScript retains that opaque member through controller resume.
