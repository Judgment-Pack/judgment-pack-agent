# ADR-0002: who assembles the prompt

Status: superseded by [ADR-0005](0005-the-prompt-is-the-adapters.md), 2026-10-19, which decides it: the adapter
assembles the whole prompt, and the host supplies its parts (option 2, with the parts asked through
injection points). Proposed 2026-10-18; the text below is as it was proposed, its Decision aside.

## Context

ADR-0001's first step asks that the contract put the assembly of the prompt on one side. Today it
is on both. The adapter builds the request it sends: the Desk's short system text for the purpose,
the runtime's prompt, the execution guidance with the request counters and the plan, the person's
request (compacted to references when it is too large), the tool schemas and the history of the
turn. Then the Desk server, which every model request passes through, adds two things the adapter
never sees (`internal/desk/ai_context.go`):

- the **project brief**, a project file named by the assistant profile, read afresh on every
  dispatch, at most `briefBytes` (8,000), headed with its path and SHA-256 and marked as background,
  not approval or authority;
- **recalled project memory**, at most `memoryBytes` (4,000), recalled for each request by the
  tail of the latest user message, marked as historical background.

On the model relay the server places them in the provider body by family: a leading system message
for OpenAI-compatible endpoints, appended to `system` for Anthropic, a part appended to
`systemInstruction` for Gemini. On the agent wire it appends them to the run's instructions. It
then runs the final guard over the whole request, messages, instructions, tool schemas and output
reserve together, and refuses a request over the input budget before dispatch
(`context-limit`, `not-started`); it never clips one. The browser, for its part, subtracts
`briefBytes` and `memoryBytes` from every request's budget whether or not either is configured.

The research worker the server runs on its own uses the same policy table and the same additions.

## The evidence for assembly on the host

- **The relay already holds what is added.** The brief is a project file and memory is a project
  store; both are read where the project's files and storage are, under the server's own path and
  symlink rules. Moving them to the adapter means handing an adapter project content it does not
  hold today.
- **One place every request passes.** Every model dispatch passes through the Desk server: the
  `vercel` engine's requests, its critic's and its delegated children's through the model relay,
  `codex` runs through `/api/agent/run` (`prepareAgentContext`), and the server's own research
  through the same preparation; each path adds the same background. Adding it there makes it
  impossible for an adapter to forget it, and the final byte guard there sees the request whole.
- **Custody and capacity live there.** The relay holds the keys and the admission slots. A guard
  that refuses before dispatch is cheapest where the dispatch is.
- **An adapter cannot be trusted with the boundary.** ADR-0001 keeps the relay, the gate and the
  approvals on the host because an adapter's own guardrails are never the boundary. Background
  that is marked as not-approval is part of how the host frames authority to the model.

## The evidence for assembly in the adapter

- **Prompt caching.** A cached prefix must be byte-identical across requests (agent#9). The server
  inserts the brief at a position that differs by family and the recalled memory changes with each
  request, so the prefix the adapter builds is not the prefix the provider sees, and an adapter
  cannot place a cache breakpoint after content it does not know is there.
- **One place.** A framework that assembles its own prompt (Deep Agents, the AI SDK's agent layer)
  expects to own the whole of it; two assemblers mean two places to read when a prompt is wrong.
- **Accounting.** The adapter reserves 12,000 bytes on every request for additions it cannot see,
  even when both are off (agent#11), and cannot tell the person what the model was shown.
- **Correctness across steps.** Recall reads the latest user message; after a tool step on the
  Anthropic and Gemini wires that message is a tool result, so memory drops out of later steps
  (agent#11). An assembler that knows the run would recall once per run.
- **Provenance.** No record digests the prompt actually sent (see the wire contract's open
  questions); an adapter that assembles all of it can digest all of it.

## Options

1. **The host assembles the background, as today, stated in the contract.** The contract names
   the additions, their size and their position per family, and gives the adapter their sizes (or
   a flag that each is absent) so it reserves only what is there. Consequences: no change to
   custody; caching stays limited to what precedes the insertion point, and the position becomes a
   contract term; the recall-per-request defect is fixed on the server.
2. **The adapter assembles everything; the host supplies the background as data.** The session
   carries the brief and the recalled memory as context inputs (with their digests and bytes),
   recalled once per run; the adapter places them, with cache breakpoints; the server keeps only
   the final guard and refuses, never adds. Consequences: one assembler and a stable prefix; project
   content crosses into the adapter, which for a browser adapter means into the page; the host
   must check that what it supplied was used, or accept that it may not be.
3. **The host supplies, the adapter places, the relay verifies.** As option 2, and the relay checks
   by digest that the supplied background is present in the outgoing request, refusing one without
   it. Consequences: the adapter owns position and caching while the host keeps the guarantee;
   the relay must find the background in three wire formats, which is the per-family knowledge
   option 2 meant to move out of it.

## Decision

Decided by [ADR-0005](0005-the-prompt-is-the-adapters.md).

## Consequences

Whichever option is chosen is recorded here and in ADR-0001's first step, and the session schema
and the wire contract's prompt section follow it in the same pull request.
