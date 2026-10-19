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

## Layout

- `contract/`: the wire contract, schemas and vectors. Empty until the first extraction lands.
- `docs/adr/`: decisions. `docs/design/`: designs, starting with the
  [wire contract](docs/design/wire-contract.md).
- Adapters live beside the contract once extracted, one directory per framework, each with the
  certification suite run against it.

## Status and plan

Nothing is extracted yet. The harness code lives in the Desk's `web/src/assistant` and the Go
server. The order of work, from the audit recorded on the Desk's `assistant-harness` issues:

1. Extract the contract from the Desk's engine slot and certify the existing Vercel AI SDK loop
   against it, moved onto the SDK's own agent layer.
2. Add a second adapter, LangChain Deep Agents for JavaScript, as the first real swap.
3. Add a Go adapter when the loop moves into the Desk's server.

Releases follow the gateway's recipe, signed tags, attested archives and a reviewed `release`
gate, once there is something to ship. Until then, `main` is the only line.

## Licence

Apache-2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
