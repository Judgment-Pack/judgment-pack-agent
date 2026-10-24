# Judgment Pack Agent

Provider selection is policy (ADR-0012): `models.<role>.provider` accepts `same`,
`other`, or a host-supplied connection ID, with run > project > table precedence.
`other` refuses when no other family is connected. Critic and child records identify
the answering connection and family; the checkpoint report carries `crossProvider`.
Capability records hold the tier map; hosts can override it per connection.
See the generated provider matrix in `docs/reference/providers.md` at the repository
root. Scenario 12 exercises two families, and certification prints its measurements.

ADR-0021 adds in-flight steering: the host queues `steer {text}`, and every
engine delivers accepted text only at the next request boundary, framed under
the run marker at the person's request rank. Agent invocations are not
interrupted. `checkpoint.steers` preserves pending and delivered entries across
resume; steering is refused while an approval decision is pending.


Budgets are the `budget` section of the [agent's policy table](docs/adr/0011-the-agents-policy-table.md).
The host's run layer wins over its project layer, then table defaults. The same
resolver covers critic, models, concurrency, pace, and task class, with each member's
value and layer in `policy_resolved` and `checkpoint.policy`. `budget.json` is a
read alias for one release. Model-visible text cannot set policy.

Scaffolded 2026-10-18. This repository holds the agent harness behind
[Judgment Pack Desk](https://github.com/Judgment-Pack/judgment-pack-desk): the loop that drives a
model through tools, the context budget and ledger, delegation to subagents, the tool gate and the
policy-selected context compaction (the deterministic index by default, or a framed bounded summary),
host-tool contract, the failure catalogue, the provider capability record, and the certification
suite that proves an adapter keeps the contract.

It is built on one decision, recorded in [ADR-0001](docs/adr/0001-one-contract-many-adapters.md):
**one wire contract, many adapters, one certification suite.** The contract is language-neutral:
JSON schemas and vectors for the events a run emits, the tool calls and results that cross it, the
approvals, the failures, the budgets and the provenance. An adapter is one framework behind that
contract. The first adapters are TypeScript and run in the browser, as the Desk's loop does today;
Go adapters run in the Desk's server when the loop moves there. A host such as the Desk consumes
the harness through the contract and pins it like any other component.

The Go module requires **Go 1.26.0 or newer** for consumers. Repository builds and CI
use the separately pinned Go 1.26.10 toolchain in `dev.json`. The minimum rises from
Go 1.25.0 for the draft v0.10.0 dependency remediation; see
[the compatibility assessment](docs/security/v0.10.0-go.md).

## What stays the host's

Three things never move into an adapter: the keyless model relay and its custody of keys, the tool
gate and the approvals that decide what a model may do, and the Runtime tools' rehearsal rule. A
framework's own guardrails are a convenience inside an adapter, never the boundary.

The prompt, on the other hand, is the adapter's ([ADR-0005](docs/adr/0005-the-prompt-is-the-adapters.md)):
every engine assembles the whole request in the same layered order, and the relay adds nothing to
it. The host supplies the parts only it holds through three injection points, `brief()`,
`memoryFor(query)` and `projectInstructions()` (`configureHarness` in TypeScript, `adapter.Run`'s
`Host` in Go), each asked once per run and placed by the adapter in its layer; the project's own
instructions rank below the package's rules and cannot grant a tool. Every request body an adapter
sends is digested in the run's checkpoint. `memoryFor` returns closed memory records; accepted
records are framed as data, with kind and source, never instructions. The text answer remains an
alias for v0.8.0. `remember {kind, text}` records a bounded proposal for host review and stores
nothing ([ADR-0014](docs/adr/0014-memory-the-write-side.md)).

Tools from outside the Runtime ([ADR-0006](docs/adr/0006-tool-sources.md)) are host tools: a
gateway connection's, a public MCP server's or one the person created is declared by the host with
its source record and executed by the host under the person's grants; the package keeps no MCP
client. It offers each under a wire-safe name mapped to its source, frames its description as
retrieved text, holds a pinned tool list, a schema budget with `load_tool`, an effect class with its
approval default and per-source bounds, and reports each source's calls, bytes and time.

Skills ([ADR-0009](docs/adr/0009-skills.md)) are bundles of instructions the host declares beside its
host tools, each from a source and pinned to the digest the person approved. The package admits them
(a skill naming a tool the host did not declare, or whose bundle changed, is withheld), lists them in
the system text with one framed line each, loads one on the model's `load_skill` call under a
budget, and places its instructions after the history at the rank of the project's own
instructions. A skill grants no tool.

The critic ([ADR-0010](docs/adr/0010-the-critics-switch.md)) is switched `off`, `auto` or `on`, the
run's word over the project's over the policy table's default, `auto`. Under `auto` it runs where the
run proposes a pack and the host declared cases (saved tests), or the proposal changes an existing
pack, or the model asked through `request_review`; a reviewer assigned, a graph-purpose run, Plan mode
and a delegated child turn it off. It runs at least at the author's reasoning tier unless the host
lowers it; on the agent capability one invocation is held back for it.
Runs reaching the critic decision point emit `critic_decided`; tracked runs checkpoint that
decision. Earlier failures or pauses may omit it; a checkpointed review request survives a resume.

## Layout

- `contract/`: the wire contract, schemas and vectors, extracted from the Desk's engine slot; see
  [`contract/README.md`](contract/README.md).
- `packages/harness/`: the harness as an npm workspace package, `@judgment-pack/harness`, extracted
  from the Desk's `web/src/assistant` with no behaviour change: the Vercel AI SDK adapter
  (`engines/vercel`) and the Codex adapter (`engines/codex`) behind the engine slot, the Deep Agents
  adapter added here (`engines/deepagents`, [ADR-0003](docs/adr/0003-the-deep-agents-adapter.md)), the context
  budget and ledger, execution and planning, delegation, the memory client, the tool gate, the
  failure catalogue, and the certification suite (`certification/`). What it needs from a host
  comes through one module, `host.ts`; see [its README](packages/harness/README.md).
- `go/`: the Go module (ADR-0004): the contract's types, generated from `contract/schemas`, and the
  Go runner of the certification suite, which drives a Go adapter against the TypeScript suite's
  own scripted server (`packages/harness/scripts/scripted-server.mjs`). `go/spike/` holds the spikes
  ADR-0004 measured, a separate module, not shipped.
- `corpus/`: ADR-0022's reviewed, output-guarded recordings. The TypeScript and Go replay commands
  compare package-owned decisions and outcomes without reaching a live provider or comparing prose.
- `scripts/parity.sh`: diffs the package against a Desk checkout and names every difference.
- `tests/`: the contract's checks, and the package's copies of the Desk's JSON held to them.
- `docs/adr/`: decisions. `docs/design/`: designs, starting with the
  [wire contract](docs/design/wire-contract.md).
- An adapter is one directory of a package, certified by the suite:
  `npm ci && npm run certify -- --adapter <id>`.

## Status and plan

The contract is extracted (`contract/`, #19), and the harness is extracted as a package
(`packages/harness`, #20, #21); the Desk runs its in-tree copy until its swap
(judgment-pack-desk#515), and the Go server's share stays in the Desk. The order of work, from the audit recorded on the Desk's
`assistant-harness` issues:

1. The contract, extracted from the Desk's engine slot (#19).
2. (a) The Vercel AI SDK adapter, extracted as it is today, with the Codex adapter beside it, both
   certified (#20, #21). (b) After the swap, the loop moved onto the SDK's own agent layer.
3. v0.1.0: the package, an attested release, and the Desk swap (#22, judgment-pack-desk#515).
4. The Deep Agents adapter (LangChain, JavaScript), as the first real swap: `engines/deepagents`,
   certified by the same suite ([ADR-0003](docs/adr/0003-the-deep-agents-adapter.md), proposed).
5. A Go adapter when the loop moves into the Desk's server.

v0.1.0 means parity with the Desk's harness, plus the improvements of #1 to #18 that land before
it; the rest follow in later releases.

## Checks and agent instructions

`scripts/dev/check.sh` runs every check CI runs, in order, and prints one summary block of passes,
failures and skips; `--setup` runs the install steps first, `--only NAME` re-runs one check. The
checks are listed in [`dev.json`](dev.json), the manifest of the Judgment Pack devkit
(v0.1.0), whose runner is vendored here unchanged. `AGENTS.md` and `CLAUDE.md` carry the same
instructions for agent sessions, shared rules first and this repository's own after them; CI checks
the two are identical (`scripts/sync-agent-docs.sh` writes `CLAUDE.md` from `AGENTS.md`). The parity
step needs a checkout of the Desk named by `JPDEV_DESK`, at the commit `scripts/parity.sh` pins.

## Release

A release is a signed tag on `main`. The workflow builds `@judgment-pack/harness` into a tarball
with its checksums, attests both files, and publishes them by release id only after a maintainer
approves the `release` gate. There is no registry; a host pins the attested asset by digest. See
[`docs/releasing.md`](docs/releasing.md) for preparing, tagging, what the workflow does and
verifying a release by hand. Until the first release, `main` is the only line.

## Licence

Apache-2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).

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

### Proposal edits

[ADR-0015](docs/adr/0015-proposal-edits.md) (#110): the package owns drafts addressed as
`draft:<document>` by `search` and `read_window`. The author uses `propose_edit`,
`propose_delete`, `propose_insert` and `undo_edit`; each unique-anchor change is
validated before replacement and bounded by the policy table. The proposal remains
a whole document with its edit provenance. Drafts and undo history survive
checkpoint resume, and ride in execution state so the stable prefix is unchanged.
Children and the critic never edit. Hosts supply accepted documents or the first
`propose_pack` opens the draft; no host file is written.

The output boundary (ADR-0017, agent#100) defaults to `outputGuard: redact`.
Answers, proposals, tool arguments, memory proposals and draft edits receive
labelled holes for the versioned base patterns and host `outputPatterns`.
`outputAllow` disables stable names; `refuse` stops the outbound step with an
`output refused` guardrail. `output_guarded` events and `outputGuard` counts make
the decision visible and carry it through checkpoint resume. Redacted proposals
are validated with their holes. Model input remains unchanged, and incomplete
fragments wait for the complete guarded answer.

### Records

[ADR-0018](docs/adr/0018-records.md) (#101): the policy table declares record
retention, redaction on store and JSON export. New checkpoints, provenance rows and
critiques carry their schema, version, digest and `retainUntil`; an expired checkpoint
is refused on resume with `records expired`. The package builds `export.json` only
from records the host supplies. The host stores and deletes; the package has no store.
