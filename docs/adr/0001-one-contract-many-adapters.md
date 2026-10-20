# ADR-0001: one wire contract, many adapters, one certification suite

Status: proposed, 2026-10-18.

## Context

The Desk's assistant loop was built on the Vercel AI SDK behind an engine slot
(the Desk's ADR-0001), with a second engine for a ChatGPT subscription. An audit on 2026-10-18
found the harness around the loop, the context ledger, delegation, memory, the tool gate and the
failure catalogue, worth about fourteen thousand lines, coupled to Desk features in both directions,
and found that the parts of the prompt the Go server assembles sit outside the slot. The audit also
found that newer frameworks ship, out of the box, what the Desk builds by hand: chunked file reads,
search, subagents, context compaction, approvals.

The Desk ships as one Go binary with the web bundle embedded and no other runtime. Any framework in
another runtime is a second process to ship, or a service.

## Decision

- The harness is defined by a **wire contract**, language-neutral, held as JSON schemas and vectors
  in `contract/`: run events, tool calls and results, approvals, failures, budgets, provenance.
- A framework is an **adapter** behind that contract, in its own directory, in whatever language its
  runtime needs. Adapters are swappable because the host sees only the contract.
- One **certification suite**, run against every adapter, decides conformance. An adapter that
  fails it is not an adapter.
- The host keeps the relay and key custody, the tool gate and approvals, and the Runtime tools'
  rehearsal rule. An adapter's own guardrails are never the boundary.
- The first adapters are TypeScript and run in the browser, as today. A Go adapter follows when the
  loop moves into the Desk's server, where the relay, capacity and approvals already live.

## Order of work

1. Extract the contract from the Desk's engine slot and the Go server's share of the prompt; decide
   which side assembles the prompt and record it here. Decided by
   [ADR-0005](0005-the-prompt-is-the-adapters.md): the adapter assembles the whole prompt, and the
   host supplies its parts through `brief()`, `memoryFor(query)` and `projectInstructions()`.
2. The **Vercel AI SDK adapter**, in two parts.
   (a) The existing loop extracted as it is, with the Codex engine as a second adapter of the same
   package, certified by the suite, and swapped into the Desk in place of its in-tree assistant.
   It is the baseline the certification suite already covers.
   (b) After the swap, the loop moved onto the SDK's agent layer (`ToolLoopAgent`, subagents as
   tools, policy approvals).
3. The **Deep Agents adapter** (LangChain, JavaScript, MIT, browser entrypoint): the first real swap,
   and the one that brings file and search tools, subagents, compaction, skills and interrupts
   without building them.
4. A **Go adapter** (Eino or ADK for Go) when the loop moves server-side.

The first release is the swap-ready one: step 1 and step 2a, packaged and attested, with the Desk
swapped onto it.

## Consequences

- The Desk pins this repository like the Runtime, Runner and Gateway, and consumes it through the
  contract only.
- The audit's issues move here where they are the harness's, and stay on the Desk where they are the
  host's.
- Until step 1 lands, nothing here is released and `main` is the only line.
