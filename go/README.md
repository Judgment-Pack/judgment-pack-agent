# The Go module

Provider selection is policy (ADR-0012): `models.<role>.provider` accepts `same`,
`other`, or a host-supplied connection ID, with run > project > table precedence.
`other` refuses when no other family is connected. Critic and child records identify
the answering connection and family; the checkpoint report carries `crossProvider`.
Capability records hold the tier map; hosts can override it per connection.
See the generated provider matrix in `docs/reference/providers.md` at the repository
root. Scenario 12 exercises two families, and certification prints its measurements.

ADR-0020 adds ordered, same-family `models.<role>.fallback` connections. The
model adapter switches once on `unavailable`, `rate-limited` after its retry
rule, or `timeout`, emits `connection_switched`, and resends the rebuilt request
with its retry count reset. The agent adapter refuses a switch during delegated
work because its provider account is exclusive. Every adapter emits one terminal
`usage` ledger and checkpoints it; tokens are absent when the wire supplied none.
The host owns prices and project totals.


Budgets are the `budget` section of the [agent's policy table](../docs/adr/0011-the-agents-policy-table.md).
The host's run layer wins over its project layer, then table defaults. The same
resolver covers critic, models, concurrency, pace, and task class, with each member's
value and layer in `policy_resolved` and `checkpoint.policy`. `budget.json` is a
read alias for one release. Model-visible text cannot set policy.

`github.com/Judgment-Pack/judgment-pack-agent/go` holds the Go side of the agent harness
(ADR-0004): the contract's generated types, the tool gate, the Eino adapter on the model capability,
the agent adapter on the agent capability (ADR-0007), the delegation coordinator for both, and the
Go runner of the certification suite. It is a module in a subdirectory of this repository, so Go resolves it by a tag
named `go/vX.Y.Z`, not by the release tag `vX.Y.Z`.

## Using it

```sh
go get github.com/Judgment-Pack/judgment-pack-agent/go@vX.Y.Z
```

`vX.Y.Z` is a release of the package. **The module's version is the package's:** every release
`vX.Y.Z` of `@judgment-pack/harness` has a module version `vX.Y.Z` at the same commit, and the two
carry the same contract. Use the versions together.

`go get` records the version in `go.mod` and the module's checksum in `go.sum`. Keep `go.sum` in
version control: Go checks the downloaded module against it, and against the public checksum
database, so the bytes are the ones the tag named. Do not use `@latest` or a branch name in a
host's build.

The Desk pins the module by version, the version of the package release it pins (the same release,
by digest, in its component lock).

## The host's side of the prompt

A Go host hands an adapter its parts of the prompt on `adapter.Run`'s `Host`, a `runcontext.Host`
(`Brief`, `ProjectInstructions` and `MemoryFor(query)`; `runcontext.HostFuncs` makes one of
functions). `MemoryRecordsForFunc` answers `[]contract.Memory`; a host may also implement a
record-returning `MemoryFor`. `MemoryForFunc` and legacy string-returning hosts remain supported
for v0.8.0. Accepted records are framed as data under the run marker, with kind and source.
The adapter asks each once per run, before its first request, holds each to its bound
in the policy table, and places it in its layer of every request
([ADR-0005](../docs/adr/0005-the-prompt-is-the-adapters.md)); the relay adds nothing. The digest of
every request body is in the run's checkpoint (`prompts`).

No reserve is taken out of a request's input allowance for the brief or the memory: `BriefBytes` and
`MemoryBytes` are the parts' bounds, and a part is counted once, where it sits in the system text.
`runcontext.ContextPolicy` takes no word on memory; `Input.Memory` only decides whether `MemoryFor`
is asked. On the Anthropic wire the `eino` adapter marks cache breakpoints after the system head and
after the tools and, under `trailing-user` placement only, after the request text (the first text part
of the first user turn): three under `trailing-user`, two under `system-tail`, where the state sits
before the messages and the bytes through the request text grow on every request.

## Context compaction

`runcontext.SessionContext` implements ADR-0016 for both adapters. The policy
defaults to the byte-identical deterministic `index`; `summary` keeps
`RecentMessages` raw and calls `Input.Compactor` with the policy-selected
`models.compactor` binding, `low` by default, and the `SummaryBytes` bound. The
callback sees prose and replaced-turn digests but no tool calls or results. Those
remain referenced through the retained index, framed with the summary as data.
`compacted` reports the marker; failure emits `compaction refused` and continues
with the index. `Checkpoint.Compaction` makes resume reuse exactly the same bytes,
after the unchanged stable prefix.

## Tool sources

A tool from a gateway connection, a public MCP server or the person is a `runcontext.HostTool` with
`Source` (`runcontext.ToolSource`, with `CurrentDigest` for an MCP server's served list),
`EffectClass`, `Approval` and `Approve`, the host's approval policy (`runcontext.ApprovalPolicy`), which
is handed the call (`runcontext.ApprovalRequest`: the wire name, the tool's own name, the source record
and a copy of the arguments) and answers at once `allow`, `refuse` or `ask`
([ADR-0006](../docs/adr/0006-tool-sources.md), [ADR-0008](../docs/adr/0008-the-approval-pause.md)). On
`ask` the run pauses (`approval_requested`, the call in the checkpoint's `approval`, `end`); the host
continues the paused checkpoint with the person's decision in `runcontext.Input.Approval`, its durable
callback (required for an approved continuation: without it nothing runs) acknowledging the approved
write before the call runs once and the executed write before the
continued run's first request, which carries the result as one data block. `ContextualSession` names each for the wire,
frames its description, holds the schema budget with `load_tool`, and wraps each call in the source
gate (`go/context/sources.go`); the boundary carries the source record on its events and the
checkpoint's `sources` counts each source. The gate holds a host tool to its phase by the offered
name, so a host opens its gate with `runcontext.OfferedHostTools(...)`, beside the boundary's
context tools. `runcontext.ToolListDigest` is the digest a host pins when the person approves a
server's list.

A host declares skills ([ADR-0009](../docs/adr/0009-skills.md)) on `adapter.Run.Skills`
(`runcontext.Skill`: id, name, description, source, instructions, the host tools it names, resources
and `PinnedDigest`, the digest the person approved, `runcontext.SkillDigest`). `ContextualSession`
admits them (`go/context/skills.go`), lists the admitted ones at the end of the system text, offers
`load_skill` and `unload_skill`, and carries a loaded skill's block after the history; the gate holds
the loaders by name, so a host adds `runcontext.OfferedSkillTools(...)` to the names it opens the gate
with. Built-in children receive no skills. A declared sub-agent receives only its definition's skills, under its own load budget (ADR-0013).

## The critic's switch

`Run.Policy` and `Run.PolicyProject` select `models.critic.provider` from
`Run.PolicyConnections`. The default is the author's connection; `other` requires
a different connected family. The selected binding supplies the critic's model or
agent, capabilities and tier override. Critique events record its connection and
family. Children use the same rule through `models.child.<profile>.provider` or
`models.subagent.<id>.provider`, and return their findings as data with a provider
on every task event. The host's subordinate model names remain available on the
author's connection.

A host passes the critic's switch ([ADR-0010](../docs/adr/0010-the-critics-switch.md)) on `adapter.Run`:
`Critic` (the run's word, `off`, `auto` or `on`), `CriticProject` (the project's), `CriticReasoning`
(the critic's tier: a thinking tier on the model capability, an effort on the agent capability; empty
is the author's) and the facts `ExistingPack` and `ReviewerAssigned`; `Review` is read for one release
as an alias of `Critic`. The table's default is `runcontext.Defaults.CriticDefault`, `auto`. Each
adapter decides through `SessionContext.DecideCritic` (`go/context/critic.go`).
Runs reaching the critic decision point emit `critic_decided`; tracked runs checkpoint that
decision. Earlier failures or pauses may omit it; a checkpointed review request survives a resume.
Under `auto` the boundary offers `request_review`, so a host that
opens the gate with its offer adds `runcontext.OfferedReviewTool(...)` beside the context tools. On
the agent capability `delegate.With` holds one invocation back for the critic under `auto` and `on`.

## The agent capability

A run is on one of two capabilities: the model capability (`adapter.Run.Model`, the relay) or the
agent capability (`adapter.Run.Agent`, [ADR-0007](../docs/adr/0007-provider-managed-delegation-in-go.md)),
a host-owned provider-run function for a provider that runs its own model loop. The host's
`adapter.Agent` carries the bound model, the function (`Run`, one invocation per call, returning
only once its account is released, and an error wrapping `adapter.ErrBusy` for a busy refusal at
admission) and its enabled-model admission (`Enabled`). The adapter is `go/adapter/agent`; wrap it
in `delegate.With` for delegation: a `delegate_tasks` call ends the parent's invocation, the children
run one at a time on the account, and a new invocation continues with the call's result. Each step
is written through the host's durable callback (`runcontext.Input.Checkpoint`) and acknowledged
before the next invocation; the checkpoint's `handoff` member says where a restarted response
continues. `examples/agent-host` binds a stand-in bridge, a run store and a gate.

To certify a binding, call `certification.CertifyAgent` with an `AgentHost` whose `Bind` plays each
check's script through the host's own bridge, account and tool handler. `go run ./cmd/harness-check`
runs the same checks against the package's stand-in, a fake exclusive account, and says that no
host's bridge was certified.

## How the tag is made

The release workflow creates `go/vX.Y.Z` on the release commit after the release is published, and
fails the run if it cannot. A person does not tag the module for a normal release. `go/v0.3.0` was
pushed by hand; [`docs/releasing.md`](../docs/releasing.md) says how to do that again if the
workflow's step fails.

### Declared sub-agents

Hosts may declare pinned `subagents[]` beside tools and skills (ADR-0013). The
`delegate_tasks` catalogue lists their ids and framed purpose lines beside the
unchanged `analyze`, `review`, `research` and `author-test` profiles. A definition
admits only declared read tools and skills, narrowing bounds and an approved digest.
The child uses `models.subagent.<id>`, its own skill-load budget and the common
prompt assembler; it has no actions, delegation, critic or review request.
Events, checkpoint tasks and report figures identify the definition's id, digest
and source. Withdrawn or changed definitions in restored batches are reported
without dispatch. The host owns storage and re-approval.

The reserved `remember {kind, text}` tool proposes memory for host review (ADR-0014). Its
1,000-byte and four-proposal defaults are policy members. Child and critic proposals are refused;
Plan-mode author proposals are allowed. `memory_proposed` and `checkpoint.memories.proposed`
record the proposal; the package stores nothing. Recall records ids/digests with kinds and states,
preserves both lists through resume, and withholds a revoked record while updating its restored
report row. Scenario 14 runs on both adapters.

### Proposal edits

[ADR-0015](../docs/adr/0015-proposal-edits.md) (#110): the package owns drafts addressed as
`draft:<document>` by `search` and `read_window`. The author uses `propose_edit`,
`propose_delete`, `propose_insert` and `undo_edit`; each unique-anchor change is
validated before replacement and bounded by the policy table. The proposal remains
a whole document with its edit provenance. Drafts and undo history survive
checkpoint resume, and ride in execution state so the stable prefix is unchanged.
Children and the critic never edit. Hosts supply accepted documents or the first
`propose_pack` opens the draft; no host file is written.

Hosts add `runcontext.OfferedEditTools(session)` beside `OfferedSkillTools` and
`OfferedReviewTool` when opening the gate. Only names in the phase's offer are
admitted; an empty offer refuses every edit name. On interrupted or waiting turns,
every draft is delivered with its own edit provenance. A malformed identifier is
refused: the event omits `document` if missing, non-string or empty, otherwise
truncates it to 512 Unicode characters; `reason` names an invalid identifier.
