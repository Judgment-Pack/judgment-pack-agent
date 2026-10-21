# contract/

The wire contract: JSON schemas for what crosses between a host and an adapter, and vectors that
every adapter must answer alike. This first statement is a **descriptive extraction**: it states the
contract the Desk's two engines (`vercel` and `codex`) keep today, read from the Desk's
`integration/v0.13` line at `d9251ecc272b9ac4f182fdfe652af54a0b2ead23`, with every member name and
value spelled as the Desk spells it. Where the Desk departs from a clean contract, the departure is
written down as an open question in [`docs/design/wire-contract.md`](../docs/design/wire-contract.md),
not fixed here. See also [ADR-0001](../docs/adr/0001-one-contract-many-adapters.md).

## Reference

[`docs/reference/`](../docs/reference/) lists the contract's components page by page (tools, skills, delegation, the critic, events, budget, reasons, the checkpoint), generated from these schemas and the package's constants by `scripts/gen-reference.mjs` and checked in CI.

## Schemas

JSON Schema, draft 2020-12, one file per thing that crosses, in `schemas/`. Each `$id` is the file's
place in this repository, and files refer to each other by relative `$ref`.

A schema is **closed** (`additionalProperties: false`) where the Desk's reader refuses a member it
does not name, and **open** where the Desk's reader accepts one (keeping or dropping it) or where no
reader checks at all, which is the case for everything that only travels in process. The *Root*
column is the root object; *Closed parts* names the definitions inside that are closed although the
root is not.

| Schema | What it describes | Root | Closed parts | Read from (Desk file, symbol) |
| --- | --- | --- | --- | --- |
| `run-event.json` | Every event a run emits, 28 kinds discriminated by `type`, including ADR-0016's `compacted`; the proposal's `note` where something after it did not finish; the critic's comparison of declared outcomes with the runtime's, and the cases a host declares for it (agent#10); a delegated child's prose so far, `message_progress` with its `taskId` (agent#16); the approval pause's event and the guardrail's reasons | open | `$defs/compacted`, `$defs/edit_applied`, `$defs/edit_refused`, `$defs/criticCase`, `$defs/approval_requested`, `$defs/policy_resolved`, `$defs/memory_proposed`, `$defs/memory_recalled` | `web/src/assistant/engine.ts`, `AssistantEvent` |
| `tool-offer.json` | The tools offered to a run, as the session's two members: `tools` (runtime tools as served, each named within the ceiling) and `hostTools` (host tools' declarations); the agent-wire alias form; the runtime ceiling (the Desk's five and `experimental_test_cases`) and reserved names; `propose_pack`, the proposal as a host tool call; a host tool's context policy, `runtime` among it (agent#8); the critic's allow-list (agent#10); tool sources (ADR-0006): the source record on a host tool, its effect class and approval, the tool-list digest, the wire name and the mapping, a declaration, a catalogue entry, `load_tool` and its answer; the source identity and identity digest (ADR-0019); the host's approval policy's three answers (ADR-0008); skills (ADR-0009): the session's `skills`, a skill as a host declares it, a catalogue entry, `load_skill`, `unload_skill` and a loaded skill as a load names it, and the source kind `exchange`; the critic's switch (ADR-0010): the session's `critic`, `criticProject`, `criticReasoning`, `existingPack`, `reviewerAssigned` and the alias `adversarialReview`, the modes, the tiers of each capability, and `request_review`, a reserved name | open | `$defs/agentWireTool`, `$defs/sourceRecord`, `$defs/identity`, `$defs/nameMapping`, `$defs/loadedSchema`, `$defs/skillCatalogueEntry`, `$defs/subagentCatalogueEntry`, `$defs/loadedSkill`, `$defs/remember`, `$defs/propose_edit`, `$defs/propose_delete`, `$defs/propose_insert`, `$defs/undo_edit` | `web/src/assistant/engine.ts`, `McpTool`, `HostTool`; `context/session.ts`, `contextualSession`; `internal/codexbridge/contract.go`, `Tool` |
| `tool-call.json` | The `tool_call` event, with the source record of a tool with a source (ADR-0006); the `tools/call` params the gate inspects; the arguments `experimental_test_cases` may leave with; the agent-wire call; a call made in text, the JSON inside its fence | open | `$defs/testCasesArguments`, `$defs/textToolCall` | `web/src/assistant/engine.ts`, `AssistantEvent`; `toolGate.ts`, `inspectOutboundFrame`; `agentTransport.ts`, `bindAgentRun` |
| `tool-result.json` | The `tool_result` event, with the source record (and its receipt) of a tool with a source and `proposed` for a held proposal (ADR-0006); the MCP result shape, image parts included; the bound, the read-back reference and its envelopes; `read_context` arguments; the reader's arguments and result (`read_window`, and every reading tool's window, its lines numbered); an image item read whole, or the line saying why it cannot be shown; an attachment; `search`'s arguments and result; the agent-wire answer; a reading tool's answer as the model is shown it, framed; the message that carries a text call's result back; the index of earlier results a chat-scoped run is shown, and the key a chat's read cache keeps a read under (agent#8) | open | `$defs/readContextArguments`, `$defs/readArguments`, `$defs/imagePart`, `$defs/searchArguments`, `$defs/agentWireAnswer`, `$defs/turnIndex`, `$defs/readCacheKey` | `web/src/assistant/engine.ts`, `McpToolResult`; `context/session.ts`, `SessionContext.result`; `context/ledger.ts`, `ContextLedger.reference`, `ContextLedger.tools`, `ContextDocument`; the reader, the search and images are this repository's, `packages/harness/context/reader.ts`, `context/search.ts`, `context/pattern.ts`, `images.ts` and `context/policy.ts`, `readWindowBytes` (agent#1, agent#5); the chat-scoped context is this repository's, `context/chat.ts`, `ChatTurn.index`, `canonicalJSON`, `readCacheKey` (agent#8) |
| `approval.json` | The host's approval state for a chat and the one grant a user decision mints, for one of the server's four actions; the grant request | open | `$defs/request` | `web/src/assistant/approvals/client.ts`, `ActionApproval`, `ActionGrant`; `internal/desk/assistant_approval.go`, `assistantActionGrant`, `assistantApprovalRequest` |
| `question.json` | One question to the person (`ask_user`); the answer draft; a saved question; the format-failure codes | closed | the root, `$defs/option`, `$defs/draft`, `$defs/saved` | `web/src/assistant/questions.ts`, `readUserQuestion`, `validQuestionDraft`, `readSavedQuestions`; `questionFailure.ts`, `QuestionFailureCode` |
| `delegated-task.json` | One child task's state, with its budget, model, references, the task it resumes and, for long findings, the cursor into the whole; its report in its profile's shape, partial, reused and retried, and its tokens, time to first byte and duration (agent#16, agent#17, agent#18); the delegation request, material passed by reference; a task profile with its model, budget, Runtime tools and report shape; a child's budget; the four report shapes, their findings, citations and open questions; the delegate call's result for a batch; the brief a child's prompt ends with, and the text a codex parent's next run is given after a batch (agent#13, agent#14, agent#15); what a host passes with a response about reuse (agent#17) | open | `$defs/request`, `$defs/taskRequest`, `$defs/budget`, `$defs/resultCursor`, `$defs/answer`, `$defs/answerTask`, `$defs/brief`, `$defs/briefReference`, `$defs/earlierWork`, `$defs/citation`, `$defs/analyzeFinding`, `$defs/reviewFinding`, `$defs/researchFinding`, `$defs/authorTestFinding`, `$defs/openQuestion`, `$defs/analyzeReport`, `$defs/reviewReport`, `$defs/researchReport`, `$defs/authorTestReport` | `web/src/assistant/delegationContract.ts`, `DelegatedTask`, `validDelegatedTask`, `readDelegationRequest`, `TaskProfile`; the profiles' budgets, references, resume, the answer, the reports, partial, reuse, the retry and the figures are this repository's, `packages/harness/delegationContract.ts`, `TASK_PROFILES`, `TaskBudget`, `readReport`, `reportInstructions`, and `delegation.ts`, `withDelegation`, `findingsHead`, `fitReport`, `childRetry` |
| `checkpoint.json` | The execution checkpoint: allowance, grants, plan, operations, completions, retained context (for a chat-scoped run, the entries it retained or read, agent#8); the turn's test-call count; delegation's figures (agent#18; `modelRequests` required on them where `unit` is `model-requests` and optional, absent on an agent capability, where it is `provider-managed`: the schema's `allOf` condition, agent#71); the digest of every request body the adapter sent (ADR-0005); the delegation handoff on an agent capability, the call, its state, the batch's records and the call's result (ADR-0007); the run report's rows by source (ADR-0006); the per-source identity pins, withholding and service-approval history (`sourceIdentities`, ADR-0019); the act call paused for the person's decision and what became of it (`approval`, with the run's framing `marker`, which the continued run reuses so the tool list is the same bytes across the pause), the person's decision, and the continuation's text (ADR-0008); the skills loaded, in load order (`skills`, ADR-0009); the critic's decision, or the model's ask before it, closed (`critic`, ADR-0010) | open | `$defs/draft`, `$defs/draftDocument`, `$defs/handoff`, `$defs/approval`, `$defs/approvalDecision`, `$defs/sourceIdentity` | `web/src/assistant/execution/state.ts`, `ExecutionCheckpoint`, `readExecution`; the handoff is the Go module's, `go/adapter/delegate/agent.go` and `go/context/state.go`, `ValidHandoff` |
| `failure.json` | One failure: the catalogue's 23 classes, source, the `not-started`/`refused`/`unknown` outcome, HTTP metadata; a catalogue entry; a failed tool's content | open | none | `web/src/assistant/failures.ts`, `AIFailure`, `readFailure`; `internal/aifailure/failure.go`, `Failure`; `internal/aifailure/catalog.json` |
| `policy.json` | Host-only sections plus `compaction`; run over project over table; resolved members retain values and layers (ADR-0011). `models.compactor` selects ADR-0016's bounded summary request. | closed | `$defs/role`, `$defs/member`, `$defs/paceUsed` | `packages/harness/policy.ts`; `go/context/policy_table.go` |
| `budget.json` | The context budget table, with the critic's switch, `criticDefault` and `criticReasoningFloor`, and `adversarialReview` read as an alias for one release (ADR-0010; the default was agent#10's); the per-model override and profile configuration; the request allowance the model is told, and beside it in a conversation the delegation coordinator wraps the tool-call cap (agent#13); the budget-increase answer; the fixed limits by their Desk names; the harness's turn clock and tool-call deadline; where the execution state sits on each wire (the two capability-record members that place it are `capability.json`'s); its bounds on the test tool, on the images one request carries and on a chat's index of earlier results; the host's three parts of every request, each with its layer, header, end line and bound, the project's own instructions bounded by `projectInstructionsBytes` (ADR-0005); no reserve for the brief or the memory (agent#69); the six bounds on tools with a source, their descriptions, the schema budget, their results, calls and deadline (ADR-0006); the skills budget, `skillBytes`, `skillsPerRun`, `skillsCatalogueBytes` and `cataloguedSkills` (ADR-0009) | open | `$defs/modelBudget`, `$defs/configuration`, `$defs/hostPart` | `internal/aicontext/policy.json`; `web/src/assistant/context/policy.ts`, `ContextPolicy`, `contextConfiguration`; `internal/aicontext/policy.go`, `Policy`, `Decode`; `execution/controller.ts`, `guidance`, `budgetTool`; the tool-call cap is this repository's, `execution/controller.ts`, `requestState`, `toolAllowance` (agent#13); the placement is this repository's, `packages/harness/engines/contract.ts`, `STATE_HEADER`, and `packages/harness/engines/vercel/loop.ts`, `statePlacement` (agent#9); so are the host's parts, `packages/harness/prompt.ts`, `HOST_PARTS`, `systemText`, `readHostParts` (ADR-0005), and the memory reserve, `context/policy.ts`, `contextPolicy` |
| `provenance.json` | One model request's record and the reports carried with a checkpoint, including ADR-0016's compaction summaries and ADR-0019's per-identity source call figures | open | `$defs/compactionSummary`, `$defs/compactionReport`, `$defs/edit`, `$defs/promptDigest`, `$defs/criticFigures`, `$defs/tokenFigures`, `$defs/childFigures`, `$defs/figuresTotal`, `$defs/batchFigures`, `$defs/parentFigures`, `$defs/delegationReport`, `$defs/sourceFigures`, `$defs/identityFigures`, `$defs/skillLoad`, `$defs/criticDecision`, `$defs/provider`, `$defs/crossProvider`, `$defs/memories`, `$defs/memoryRecall` | `web/src/assistant/execution/state.ts`, `ExecutionCheckpoint.completions`; `completion.ts`, `ModelCompletion`, `readCompletion` |
| `capability.json` | What one endpoint and model can do, as the `vercel` adapter reads it wherever it decides: window and output cap, native and parallel tool calls, vision, the reasoning kind (and whether the endpoint ignores it), streaming quirks, the tool-schema subset, and where the execution state goes; what a host answers for an endpoint (any members of the record) | closed | the root, `$defs/reasoning`, `$defs/streamingQuirks`, `$defs/endpointCapabilities` | none at the pinned commit (the Desk decides by its three families: `web/src/assistant/thinking.ts`, `firstDialect`; `geminiSchema.ts`; `context/policy.ts`); the record is this repository's, `packages/harness/capabilities.ts`, `ModelCapabilities`, `FAMILY_CAPABILITIES`, `readCapabilities`, `capabilitiesFor` (agent#4, agent#9) |
| `memory.json` | Closed memory records, recall answer with the v0.8.0 text alias (ADR-0014) | closed | the root | `packages/harness/memories.ts`; `go/context/memories.go` |
| `skill.json` | A skill (ADR-0009): its id, its name, its description, its source record (`exchange` among the kinds, `runtime` refused), its instructions, the host tools it names, its resources, the digest the person approved (`pinnedDigest`, on the declaration) and its own (`digest`, on admission); the digest's rule; a skill's size against `skillBytes` | closed | the root, `$defs/resource` | none at the pinned commit; the skill is this repository's, `packages/harness/skills.ts`, `SkillDeclaration`, `declareSkills`, `skillDigest`, `catalogueSkills`, `SkillGate` |

| `subagent.json` | Pinned reads-only host definition and its approved record (ADR-0013) | closed | the root, `bounds`, `source`, `$defs/record` | `packages/harness/subagents.ts`, `go/context/subagents.go` |

## Vectors

Data the Desk's engines produce today, in `vectors/`, one JSON file per vector, and data the harness
package produces where it has changed or added behaviour since the extraction:

```json
{ "description": "…", "schema": "run-event.json", "source": { "desk": {…}, "files": ["…"] }, "items": [ … ] }
```

`schema` names the schema each item must validate against (a file, or a file and a `$defs`
pointer); `items` are the instances, in order where order matters; `source` names the Desk files
and symbols they were read or rendered from, or, as `{"harness": {"repository":
"Judgment-Pack/judgment-pack-agent", "package": "packages/harness"}, "files": […]}`, the package's
files they were rendered from.

- `scenario/expense-reimbursement.json`: the Desk's recorded conformance scenario, **one file for
  the whole run**, because what the conformance session asserts is the run's order (every call
  before its result, the gate's events between them, one `end`). `steps` maps each event to the
  scenario step that produced it. It is the sequence the model-engine leg asserts (non-interactive
  session, reasoning tier off, the five runtime tools allowed), with the recorded runtime answers
  and the texts the host writes rendered from the code that writes them. Since ADR-0010 the run's
  `critic_decided` comes before the proposal: the table's `auto`, and no trigger, so no critic.
- `failures/<class>.json`: one per catalogue class, with the terminal events an adapter emits for it
  on one path the Desk (or, where `source` names the package, the harness) actually takes, named in
  `source`: `error` then `end` for most; a failed `tool_result` for `tool-failed` and
  `invalid-tool-input`, which are working data and not a failed run; `retry` events before
  `retries-exhausted` and before `rate-limited`; and nothing at all for `cancelled`, which is never
  an error event.
- `tool-offer/`: what the scenario's session offers, whole (`scenario-offer.json`, its `tools` and
  `hostTools`, the context tools and, under the table's `auto`, `request_review`, ADR-0010), then its runtime tools and its host tools one by one (`runtime-tools.json`,
  `host-tools.json`), and the same offer as the `codex` engine puts it on the agent wire
  (`agent-wire-offer.json`); and the runtime ceiling the gate applies (`runtime-ceiling.json`): the
  Desk's five and `experimental_test_cases`.
- `budget/desk-defaults.json` and `budget/desk-limits.json`: the budget table verbatim with the two
  members the harness adds (`criticDefault` and `criticReasoningFloor`, ADR-0010), and the fixed limits by their constant
  names; `budget/harness-defaults.json`: the policy a harness session works under, the table, the two
  time bounds, the two image bounds, the three test-tool bounds, the two bounds on a chat's index
  of earlier results and the bound on the project's own instructions.
- `budget/host-parts.json`: the host's three parts of every request (ADR-0005), in their layers'
  order: the brief, the project's own instructions and the memory, each with its injection point,
  the first and last lines of its block and the member that bounds it. Written by hand;
  `prompt.test.ts` holds `HOST_PARTS` to it and `go/context` holds its table to it.
- `provenance/prompt-digest.json`: the digest of each request body an adapter sent (ADR-0005), with
  the bodies (`bodies`) it was computed from, written by hand; `prompt.test.ts` holds the harness's
  digest to each and a run's checkpoint to the bodies it sent, and the contract tests compute each
  digest again.
- `proposal/`: the proposal through `propose_pack` (`propose-pack.json`), a proposal whose critic
  failed, delivered with its `note` and the critic's figures (`critic-unfinished.json`), a
  `propose_pack` proposal whose closing request failed, delivered with its `note`
  (`turn-unfinished.json`), and an answer whose proposal could not be read, kept whole before the
  failure after the one reformat, on both engines since agent#29 (`unreadable.json`); and
  `tool-offer/propose-pack.json`, the tool as a host declares it.
- `critic/` (agent#10), written by hand from the schemas: cases a host declares for the critic,
  partial and whole (`declared-cases.json`); a critique that refutes a seeded wrong outcome, naming
  the case and the one left incomplete (`outcome-refuted.json`), and one that reads a test run's rows
  of the saved tests stating a whole disposition (`test-run-compared.json`); and what a critic pass
  cost on each engine, with the harness's own calls (`figures.json`). `critic.test.ts` holds the
  critiques and the cases to what `refutation.ts` writes, and the figures, `proposal/unreadable.json`
  and `proposal/critic-unfinished.json` to what the engines emit. `tool-offer/critic-tools.json` is
  the critic's allow-list and `tool-offer/critic-offer.json` what the critic is offered in the
  scenario's session (its runtime reads and the harness's two readers), which the conformance
  session and the agent-engine leg assert.
- `critic/` for the critic's switch (ADR-0010), written by hand: the decision for each case beside it
  (`cases`, by position: the session's critic members, the table's, the forced-off facts, the run's
  facts and the author's tier), each mode and layer (`switch-modes.json`), each `auto` trigger
  (`switch-triggers.json`), each forced-off reason (`switch-forced-off.json`), the skip for budget
  (`switch-budget.json`), the tier floor and an explicit lower tier (`switch-tier.json`), and the
  alias (`switch-alias.json`); sessions carrying the switch's members (`switch-sessions.json`);
  `request_review` as the boundary declares it (`request-review.json`); the ask taken
  (`review-asked.json`) and refused, a second ask and one under off (`review-refused.json`); one
  `critic_decided` for each reason (`decided-events.json`); and the checkpoint's `critic`, the ask
  alone and decided (`checkpoint.json`). `criticSwitch.test.ts` and `go/context/critic_test.go` hold
  each language to them, and the contract tests compute each decision again.
- `provenance/engine-ids.json`: the engine ids of `provenance.json#/$defs/engine`, in order: the Desk's
  two, `deepagents` (ADR-0003) and `eino`, the Go adapter (ADR-0004), which the TypeScript registry does not
  load. `host.test.ts` and `tests/test_contract.py` hold it to the schema.
- `bounds/`: a 429 retried once, with the attempts in the completion record
  (`rate-limited-retried.json`), the turn's wall clock pausing the run (`turn-clock.json`), a tool
  past its deadline answered as a failed result (`tool-timeout.json`), and a turn whose test calls
  pass `testCallsPerTurn` (`test-call-cap.json`).
- `budget/state-placement.json`: where the execution state, headed `STATE_HEADER`, sits in the
  first and second requests of the recorded scenario on each wire, for each placement the endpoint
  capabilities resolve to. This harness's (agent#9); `engines/vercel/prefix.test.ts` checks every
  item against a run.
- `chat/`: the index of earlier results a chat-scoped run is shown (`turn-index.json`: the index
  after a first run kept three results, and after a second run's result evicted the two oldest), and
  the objects a chat's read cache keys a read by, with each one's canonical JSON and SHA-256
  (`cache-key.json`). Written by hand from the schema; `context/chat.test.ts` builds the same chat
  and holds the code to both, and the contract tests derive each row and digest again. This
  harness's (agent#8).
- Vectors added for the members the v0.2.0 release review found without one (#43), written by hand
  from the schemas: `delegation/record-resumed.json`, `delegation/answer-resumed.json` (a resumed,
  summarized task), `delegation/brief-partial.json` (an earlier exchange that stopped at its time
  limit), `delegation/report-analyze-proposal.json` (an open question with a `ref`),
  `delegation/figures-agent-runs.json` (one execution on each capability: a codex child's
  `agentRuns` with no `modelRequests` member, then a model capability's figures with reasoning and
  cache-write tokens; agent#71), `critic/figures-cache-write.json`, `critic/declared-cases-members.json`,
  `critic/error-outcome-compared.json` (an error class and phase as a case outcome; the harness's own
  calls, `origin`), `tool-offer/host-declarations.json` (the interaction, delegation and
  presentation members and `contextPolicy.runtime`), `tool-call/host-presentation.json` and
  `delegation/connection-models.json` (the model a task ran on under two connections,
  `context.connection`). The TypeScript tests that read them are named in each file's `source`.
- `delegation/handoff.json`: one delegation handoff on an agent capability (ADR-0007) in its four
  states, as the Go coordinator writes it into the parent's checkpoint: released, the records with
  one child finished and one dispatched, answered after a restart left the dispatched child's
  outcome unknown, and continued. Written by hand from the schema; its `source` names the Go module
  (`{"go": {"repository": "Judgment-Pack/judgment-pack-agent", "module": "go"}, "files": […]}`), the
  only vector rendered from it, and `go/context` (`ValidHandoff`) and `go/adapter/delegate` hold
  their reader and writer to every item.
- `tool-call/test-cases-arguments.json`: the arguments `experimental_test_cases` leaves the page with.
- The approval pause (ADR-0008), written by hand from the schemas: `approval/pause.json` (the
  `approval_requested` event, the checkpoint paused with the call's `approval` asked, carrying the
  run's framing `marker`, which the continued run reuses, `end`),
  `approval/second-act-while-pending.json` (a second act call refused with the `approval pending`
  guardrail between its call and its result), `approval/continue-refused.json` and
  `approval/continue-approved.json` (the person's decision, `checkpoint.json#/$defs/approvalDecision`;
  the checkpoints the continued run writes, `refused`, or `approved` then `executed`, then `answered`,
  and after a refusal's write the one `guardrail` with `reason: approval refused`;
  and the continuation's text, one line and the result as one data block with the note inside it) and
  `approval/wrong-call-id.json` (a decision for another call, refused before anything runs) and
  `approval/policy.json` (the host's approval policy's three answers, `tool-offer.json#/$defs/approvalPolicy`) and
  `approval/stale-decision.json` (the gate's own refusal of an act call whose decision no longer
  holds for it, the `guardrail` with `reason: approval stale` between the call and its failed
  result). The contract tests hold each continuation's data to its decision and result;
  `toolSources.test.ts` and `go/context` hold the package's words to them, and certified scenario 8
  runs the same on every engine and compares the pausing run and the run with a decision for another
  call with `pause.json` and `wrong-call-id.json`, the call id and the marker being each run's own, and
  holds the tool list and the system text of each continued run to the pausing run's bytes.
- Tool sources (ADR-0006), written by hand from the schemas: `tool-offer/source-declarations.json`
  (five host tools with a source as a host declares them, and six declarations the package refuses,
  each failing the schema), `tool-offer/source-offer.json` (the offer made of them, under their wire
  names, descriptions framed with a fixed marker, effect classes and approvals filled in),
  `tool-offer/catalogue.json` (the same as catalogue entries), `tool-offer/source-schema.json` (a tool
  whose input schema carries text its source wrote: the property descriptions framed, every keyword
  but the structural ones dropped, a long name and a long enum dropped, and the guardrails they earn) and `tool-offer/load-tool.json`;
  `tool-offer/source-names.json` (wire names, with the suffix where a character was replaced or the
  name was cut, a collision and the names a source may not bear) and `tool-offer/tool-list-digest.json`
  (two tool lists, their canonical JSON and the digest a host pins), each computed again by the
  contract tests; `tool-result/loaded-schema.json` (`load_tool`'s answer); `tool-call/source-calls.json`
  and `tool-result/source-results.json` (events with the source record, a receipt, a held proposal, a
  refusal after a tool list changed); and `provenance/source-report.json` (the run report's rows of
  certified scenario 7). `toolSources.test.ts` holds the package to each; `go/context` holds the Go
  port to the names, the digests, the declarations and the offer.
- Identity per tool call (ADR-0019), written by hand under `identity/`: `offer.json` carries the
  service identity, its digest and framed `runs as` line; `events.json` carries it on the call and
  result while a model argument cannot replace it; `figures.json` counts calls by identity;
  `changed.json` withholds a changed identity; and `service-act.json` records the first service act's
  downgrade from allow to ask. `toolSources.test.ts`, `go/context`, and certified scenario 19 hold
  both implementations to them.
- Skills (ADR-0009), written by hand from the schemas, in `skills/`: `declarations.json` (three skills
  as a host declares them, a custom one that names a declared tool and carries a resource, an
  Exchange entry's and an MCP server's prompt, and six declarations the package refuses, each failing
  the schema), `admitted.json` (the same, admitted with their digests, each computed again by the
  contract tests), `catalogue.json` (the catalogue the system text carries of them under a fixed
  marker), `load.json` (a load: the call, `skill_loaded`, the answer, and the block every later request
  carries after the history), `unload.json`, `withheld-digest.json` (a bundle changed after the person
  approved it), `withheld-tool.json` (a skill naming a tool the host did not declare), `budget.json` (a
  load past `skillsPerRun` refused), `checkpoint.json` (the loaded set and each source's loads, a reload
  counted again), `load-skill.json` and `unload-skill.json` (the loaders as the boundary offers
  them), and `offer.json` (a tool offer whose declaration carries scenario 9's skills beside its
  tools, the root member `skills`). `skills.test.ts` holds the package to each; `go/context` holds the Go port to the same.
- The gate's own vectors (agent#53), written by hand: `tool-call/gate-frames.json`, what the host's
  gate decides about outbound frames (each item the params of a `tools/call` frame, each of its
  `frames` a whole frame, with the verdict, the guardrail notice and what left, under the vector's
  allow-list and bounds or a case's own); `tool-offer/phase-offers.json`, what each phase of a run is
  offered of one session (the loop's, the critic's, Plan mode's, the reformat run's and a delegated
  child's of each closed profile); and `approval/grant-states.json`, a chat's approval state and
  what it admits (the mode shown, whether a commit is admitted, the grant's state after). The
  TypeScript gate (`toolGate.test.ts`) and the Go gate (`go/gate`) are held to the first two; the
  harness carries no approval reader, so the third holds the Go gate and the schema.
- `delegation/`: the four closed task profiles with their models, budgets and tools
  (`profiles.json`; `delegation.test.ts` holds `TASK_PROFILES` equal to it); a delegate call with
  material by reference, a summary asked for and a resumed task (`request-by-reference.json`); one
  task's records, queued to complete, whose long findings reach the parent as their head and a
  `read_window` cursor (`long-result.json`; the contract tests derive the head, cursor, length and
  ledger id from its fixture); the delegate call's result for a batch of two (`answer.json`); a
  delegating `vercel` turn as one run, the batch's records between the call and its result
  (`one-run.json`) and the event an engine that cannot wait for the call sends instead, with the call's
  id (`requested-run.json`); two children's briefs, one with references and one resumed (`brief.json`); and
  the text a codex parent's next run is given after a batch, the result as one data block
  (`continuation.json`; the contract tests hold its data to the answer's schema). `answer.json`'s
  second item is a batch with a report, a partial task and a reused one. One report per profile, in
  its shape (`report-analyze.json`, `report-review.json`, `report-research.json`,
  `report-author-test.json`; `delegationResults.test.ts` reads each with `readReport`); a child's
  prose streamed under its task (`child-progress.json`); what delegation cost in one execution,
  two batches and the parent's own figures (`figures.json`; the contract tests hold each total to its
  children's sums); task records with a report, `partial`, `reused`, `retried` and the figures
  (`record.json`), a checkpoint saved during a batch with its `delegation`, and a provider-managed run's with its
  `delegation` (agent runs, no `modelRequests`), `handoff`, `prompts` and `sources` (`checkpoint.json`), and
  what a host passes to reuse nothing earlier (`host-request.json`); `delegationResults.test.ts`
  reads the first two with `validDelegatedTask` and `readExecution`. Written by hand from the schemas. This harness's (agent#13, agent#14, agent#15,
  agent#16, agent#17, agent#18).
- `budget/tool-call-cap.json`: the tool-call cap as the execution state states it, a parent's for
  the response and a child's for its task. Written by hand from the schema. This harness's
  (agent#13).
- `framing/`: a reading tool's answer as the model is shown it, one data block closed by the run's
  marker (`framed-result.json`), and a page that asks for a crafted call, refused at the gate
  (`injection-refused.json`). On the text path the same block is the body of the result message
  (`tool-result/text-result.json`).
- `question/interview.json`: the Desk's interview-question fixture.
- `capability/`: the capability record of each of the Desk's three families as the harness defaults
  it (`openai-compatible.json`, `anthropic.json`, `gemini.json`: what the loop did for each before it
  read a record), and an illustrative record for an open-weight model on a local
  OpenAI-compatible endpoint (`open-weight-local.json`: no native tool calls, an ignored
  reasoning parameter, a pinned reasoning member, a 32,768-token window), written as the Desk's
  probe would record one and not measured. Each names its `family`. This harness's (agent#4);
  `packages/harness/capabilities.test.ts` holds the three to `FAMILY_CAPABILITIES` and runs the
  recorded scenario on the fourth.
- `tool-result/`: results that carry images (`image-result.json`), `read_window`'s answer for an
  image item and for one it cannot show (`image-read.json`), the documents a host hands a run to
  retain (`attachments.json`), and `search`'s arguments and results (`search-arguments.json`,
  `search.json`, rendered from the code the tests run, with a fixed ledger scope, and a search that
  names an image it cannot show, written by hand). This harness's (agent#5).
- `tool-call/text-call.json` and `tool-result/text-result.json`: on an endpoint that takes no native
  tool calls, the calls a model makes in a fenced JSON block, the answers that make them and two
  that make none, and the messages that carry the results back, headed `TOOL RESULT` or `TOOL
  ERROR`, a reading tool's answer framed as data with a fixed marker. Written by hand from the schemas; `engines/vercel/capabilities.test.ts` holds the reader
  and a run to them. This harness's (agent#4).

Values the Desk generates afresh on every run (UUIDs, timestamps, digests) appear only where the
recorded data carries them, or, in the harness's own vectors, as rendered with a fixed ledger scope;
no vector carries an event the Desk or the harness does not emit.

## The Desk's own lists

`desk/catalog.json` and `desk/policy.json` are byte-for-byte copies of the Desk's failure catalogue
(`internal/aifailure/catalog.json`) and budget table (`internal/aicontext/policy.json`), the table with
two members the harness adds, `criticDefault` (`auto`) and `criticReasoningFloor` (`author`) (ADR-0010:
the critic's switch, which every engine reads; they replaced agent#10's `adversarialReview`; the Desk's
table takes them in its swap, and its Go reader ignores a member it does not name), and
`desk/inventory.json` lists, by source and symbol, the event kinds, failure sources and outcomes,
completion reasons, engine ids, runtime tool ceiling and approval actions at the pinned commit. The
tests hold the schemas and the budget vector to them. When the Desk changes one of them, copy it again
and the tests say which schema has to follow.

## Checks

`python -m unittest discover -s tests -v` (Python 3.12, `pip install -r requirements-dev.txt`):
every schema is well formed and every `$ref` resolves; every vector validates against its schema;
every member a vector carries is stated by its schema (the schemas re-checked with every object
definition closed, so an open schema still has to name what the Desk emits); the event kinds,
failure classes and approval actions equal the Desk's lists; the budget vector is the Desk's table;
one vector per catalogue class, a failed tool's failure validated as one; a run that ends or fails
ends once, last; every capability record names a family whose wire speaks its reasoning kind, with
one default record per family, and a host's answer may carry any members of the record and no
other; the host's parts are in layer order, each bounded by a member of the harness's table; each prompt digest is the
digest of its body; each read cache key is the
digest of its canonical JSON; each row of the chat index follows from the vector's fixture; the critic's allow-list is four of
the ceiling's reads and two reserved readers, the table's default is `auto` with the author's tier as the floor and no
`adversarialReview`, and its offer in the scenario is the scenario's runtime tools on the list; each critic decision of the
`critic/switch-*.json` vectors follows from the case beside it, by the rule computed once more in the test, and `request_review`
is reserved, offered after the context tools as `critic/request-review.json` declares it and never to the critic (ADR-0010); a long delegated finding's head, cursor, length and ledger id follow from its vector's fixture; the
codex continuation's block carries a batch's answer under one marker; each finding of a report vector cites only its report's citations; each batch total of every delegation-report vector is its children's sums, `agentRuns` included, and `modelRequests` counts model requests only (an agent capability's figures omit it, the checkpoint requires it where its `unit` is `model-requests`, agent#71); every member of the `$defs` added or changed between v0.1.0 and the v0.2.0 release review, and the root members of `delegated-task.json`, `tool-call.json` and `tool-result.json`, is carried by at least one vector (a probe sets the member's schema to `false` and validates every vector again), as is each value of the guardrail's `reason` and of `approvalPolicy` (a probe takes the value out of the enum), and
each value of the critic's switch's enums: the modes, the tiers, the thirteen reasons, the triggers and the reserved name
`request_review` (ADR-0010); the scenario's order is the one the conformance session asserts; and this table's
*Root* and *Closed parts* columns match each schema (*Closed parts* lists the closed top-level
`$defs`).

## Policy (ADR-0011)

`schemas/policy.json` is the closed six-section host table. The session's `policy`
and `policyProject` are partial layers, with `taskClass` and host-counted `paceUsed`.
The run wins over project over table per member. `policy_resolved`,
`checkpoint.policy`, and the provenance policy row retain values and layers.
`budget.json` remains a read alias for v0.8.0. Hand-authored vectors are in
`vectors/policy/`; model text cannot supply any member.

## Cross-provider modelling (ADR-0012)

`policy.json` admits `models.<role>.provider`: `same`, `other`, or a connected ID.
The resolved member is the selected connection ID and its layer. `other` with no
other family refuses before any dispatch, with `policy refused` naming the role.
`capability.json` adds `family`, `tiers` (three non-empty strings), and
`cacheBreakpoints`; endpoint overrides admit the same members. Defaults and the
wire matrix are generated from capability records into `docs/reference/providers.md`.

`critique` and `delegated_task` carry `provider: {connection, family}`. The report's
`crossProvider` rows carry role, relation, author and provider; checkpoint roundtrips
retain them. Historical records can omit these members. The hand-authored vectors
in `vectors/providers/` cover selection, precedence, refusal, child/subagent policy,
provider records, tier overrides, checkpoint reports and cross-family failover refusal.

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

## Memory (ADR-0014)

`memory.json` is closed: id, kind, text, source (run and proposedBy), scope, digest, createdAt,
optional expiresAt, and state. `memory.json#/$defs/answer` is the `memoryFor` answer: records,
with bare text retained as an alias for v0.8.0. The digest is SHA-256 of UTF-8 text with `sha256:`.
`tool-offer.json#/$defs/remember` is the reserved proposal tool. Policy adds memoryProposalBytes
(1,000) and memoryProposalsPerRun (4); memoryBytes still bounds recall. Events add memory_proposed,
memory_recalled and guardrail reason `memory refused`. Checkpoints carry `memories` through resume;
provenance defines full proposal and recalled id/digest/kind/state rows. The handwritten
`vectors/memory/` cover proposals, refusal, records, revoked recall, alias, events and checkpoint.

Proposal edits (ADR-0015, #110): the package owns drafts addressed as
`draft:<document>` by `search` and `read_window`. The author uses `propose_edit`,
`propose_delete`, `propose_insert` and `undo_edit`; each unique-anchor change is
validated before replacement and bounded by the policy table. The proposal remains
a whole document with its edit provenance. Drafts and undo history survive
checkpoint resume, and ride in execution state so the stable prefix is unchanged.
Children and the critic never edit. Hosts supply accepted documents or the first
`propose_pack` opens the draft; no host file is written.

Proposal edit refusals omit `document` when the identifier is missing, non-string
or empty; nonempty identifiers are truncated to 512 Unicode characters. The
`reason` names an invalid identifier. This makes malformed calls schema-valid
refusal events. Interrupted and waiting turns deliver every draft with its edits.
Both languages execute the shared calls under `contract/vectors/edits/`, including
successful deletion/insertion and malformed-identifier cases.

## Context compaction (ADR-0016)

`policy.json` adds `compaction`, `budget.summaryBytes` and `models.compactor`.
`run-event.json` adds the `compacted` marker and `compaction refused`; the
checkpoint and provenance schemas carry stored summary rows and replaced-turn
digests for byte-identical resume. The four handwritten vectors in
`vectors/compaction/` cover the unchanged index, a framed bounded summary, failure
fallback and the checkpoint member. Tool results remain in the retained index.
