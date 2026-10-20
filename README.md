# Judgment Pack Agent

Research preview, scaffolded 2026-10-18. This repository holds the agent harness behind
[Judgment Pack Desk](https://github.com/Judgment-Pack/judgment-pack-desk): the loop that drives a
model through tools, the context budget and ledger, delegation to subagents, the tool gate and the
host-tool contract, the failure catalogue, the provider capability record, and the certification
suite that proves an adapter keeps the contract.

It is built on one decision, recorded in [ADR-0001](docs/adr/0001-one-contract-many-adapters.md):
**one wire contract, many adapters, one certification suite.** The contract is language-neutral:
JSON schemas and vectors for the events a run emits, the tool calls and results that cross it, the
approvals, the failures, the budgets and the provenance. An adapter is one framework behind that
contract. The first adapters are TypeScript and run in the browser, as the Desk's loop does today;
Go adapters run in the Desk's server when the loop moves there. A host such as the Desk consumes
the harness through the contract and pins it like any other component.

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
sends is digested in the run's checkpoint.

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

## Release

A release is a signed tag on `main`. The workflow builds `@judgment-pack/harness` into a tarball
with its checksums, attests both files, and publishes them by release id only after a maintainer
approves the `release` gate. There is no registry; a host pins the attested asset by digest. See
[`docs/releasing.md`](docs/releasing.md) for preparing, tagging, what the workflow does and
verifying a release by hand. Until the first release, `main` is the only line.

## Licence

Apache-2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
