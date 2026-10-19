# The wire contract

Draft, 2026-10-18. This page will state what crosses between a host and an adapter. It is drawn
from the Desk's engine contract and will be held as schemas and vectors under `contract/`.

## What crosses

- **Run events**, from the adapter to the host, in order: message text and reasoning deltas,
  tool calls and results, questions to the person, delegated-task state, execution checkpoints,
  proposals, budget pauses, failures, end.
- **Tool offers and calls**, from the host: the tools offered this run with their schemas and their
  effect class; each call's name and arguments; each result, bounded, with a reference the adapter
  may read back in sections.
- **Approvals**: a call that needs the person's decision pauses the run; the decision resumes it.
  The adapter never decides.
- **Failures**: one catalogue, with the class, the provider's code where there is one, and whether
  the request was refused before anything was delivered.
- **Budgets**: requests, tool calls, time, and what remains; the adapter stops when told.
- **Provenance**: the model, the adapter and contract versions, the prompt digest, the usage the
  provider reported, and the timings.

## Open questions

- Which side assembles the prompt: the host's server adds the project brief and memory today,
  behind the adapter's back. The contract must put that on one side.
- How a context ledger is shared across turns and bounded.
- How a delegated task's budget and result are carried for an adapter whose framework has its own
  subagents.
