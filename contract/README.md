# contract/

The wire contract: JSON schemas for what crosses between a host and an adapter, and vectors that
every adapter must answer alike. This first statement is a **descriptive extraction**: it states the
contract the Desk's two engines (`vercel` and `codex`) keep today, read from the Desk's
`integration/v0.13` line at `d9251ecc272b9ac4f182fdfe652af54a0b2ead23`, with every member name and
value spelled as the Desk spells it. Where the Desk departs from a clean contract, the departure is
written down as an open question in [`docs/design/wire-contract.md`](../docs/design/wire-contract.md),
not fixed here. See also [ADR-0001](../docs/adr/0001-one-contract-many-adapters.md).

ADR-0021 is covered by the handwritten `vectors/steer/` set: one boundary
delivery, two deliveries in order, an approval-pause refusal, and the checkpoint
member that carries pending and delivered steers through resume.

Raw protocol evidence belongs in `fixtures/`, separate from the schema-named records in
`vectors/`. The handwritten `fixtures/storage/model-terminals.json` cases include invalid and
truncated provider responses; Go and TypeScript terminal-verification tests consume them directly.
They are parser acceptance fixtures, not serialized Agent records or schema vectors.

Shared scenario inputs and expected refusals also live in `fixtures/`: `feedback/` covers ratings,
candidates and relation evidence; `history/` covers archive, capture, checkpoint display, task
identity and report versions; `report/repeat-tools.json` covers repeat summaries; and
`turn-route/decision.json` covers route decisions. TypeScript and Go scenario readers consume
these files directly. The corresponding `vectors/feedback/*-wire.json`,
`vectors/history/*-wire.json` and `vectors/turn-route/*-wire.json` carry positive schema-named
records as `items` arrays for schema validation and generated-type round trips. The shared
`fixtures/record-versions/report-discriminator.json` cases pin exact integer version spellings
for the Go report union and the TypeScript report byte reader, including refusals before rounding.

## Reference

[`docs/reference/`](../docs/reference/) lists the contract's components page by page (tools, skills, delegation, the critic, events, budget, reasons, the checkpoint), generated from these schemas and the package's constants by `scripts/gen-reference.mjs` and checked in CI.

## Schemas

`corpus-entry.json` is the closed manifest for an ADR-0022 evaluation-corpus entry. It names the
recorded requests, tool results, events, checkpoint and expectations, plus the timing or drawn-marker
tolerances replay may ignore. `recorded` in `run-event.json` announces a successful write; `record
refused` says the output guard could not make the entry safe and nothing was written.

JSON Schema, draft 2020-12, one file per thing that crosses, in `schemas/`. Each `$id` is the file's
place in this repository, and files refer to each other by relative `$ref`.

A schema is **closed** (`additionalProperties: false`) where the Desk's reader refuses a member it
does not name, and **open** where the Desk's reader accepts one (keeping or dropping it) or where no
reader checks at all, which is the case for everything that only travels in process. The *Root*
column is the root object; *Closed parts* names the definitions inside that are closed although the
root is not.

| Schema | What it describes | Root | Closed parts | Read from (Desk file, symbol) |
| --- | --- | --- | --- | --- |
| `tool-taxonomy.json` | Owner-declared operation and effect (ADR-0027), with derived capability profiles | closed | `$defs/profile` | `packages/harness/toolTaxonomy.ts`, `go/adapter/wire/tooltaxonomy.go` |
| `chat-context-snapshot.json` | Complete portable chat state, ordered JSON sources, aliases and active turns (#160) | closed | | `context/chat.ts`, `go/context/chat_snapshot.go` |
| `history-chunk.json` | Resource-scoped content-addressed retained object chunks | closed | | `history/codec.ts`, `go/context/history_codec.go` |
| `history-entry.json` | Storage-observed commit history with sequence/time and exact context binding | closed | | `storage/history.ts`, `go/context/history_store.go` |
| `dialect-authoring.json` | Exact read and complete replacement review-only tool inputs (#171); the paged read of original declarations and the read port's page request and page, bound to the collection revision and inventory digest (#243); the read port's revision field index request and page, checked against the top index entry (#252) | open | `$defs/conversion`, `$defs/read`, `$defs/fact`, `$defs/evidence`, `$defs/propose`, `$defs/pageRequest`, `$defs/declaration`, `$defs/page`, `$defs/indexField`, `$defs/fieldIndex`, `$defs/fieldIndexRequest`, `$defs/fieldIndexPage` | `packages/harness/dialects.ts`, `go/context/dialects.go` |
| `dialect-draft.json` | Bounded editable draft checkpoints, exact stage receipts, and final review lineage (#171); version 2 carries the complete original inventory by digest (`researchIndex`, a bounded top index of revision field index digests since #252), version 1 keeps v0.11.0's inline `researchDeclarations` (#243) | open | `$defs/conversion`, `$defs/fact`, `$defs/evidence`, `$defs/sample`, `$defs/research`, `$defs/column`, `$defs/candidate`, `$defs/result`, `$defs/failure`, `$defs/check`, `$defs/checkpoint`, `$defs/ack`, `$defs/lineage`, `$defs/originalField`, `$defs/originalRevision`, `$defs/conversionContract`, `$defs/retainedRead`, `$defs/indexRevision`, `$defs/researchIndex` | `packages/harness/dialects.ts`, `go/context/dialects.go` |
| `run-report.json` | Pure projection of recorded execution state and optional validated event prefix (#157) | closed | the root and all report sections | `packages/harness/runReport.ts`; `go/context/run_report.go` |
| `run-report-v2.json` | Feature checkpoint3 projection with attributable usage subtotals and completeness (#170) | closed | `$defs/total`, `$defs/totals` | `packages/harness/runReport.ts`; `go/context/run_report.go` |
| `usage-budget.json` | Root-owned bounded shared-budget reservations, refusals and reported facts (#170); receipts grant no dispatch authority | closed | `$defs/amounts`, `$defs/scope`, `$defs/reservation`, `$defs/receipt`, `$defs/entry`, `$defs/refusal` | `packages/harness/storage/usageJournal.ts`; `go/context/usage_budget_journal.go` |
| `run-event.json` | Every event a run emits, 38 kinds discriminated by `type`, including ADR-0016's `compacted` and ADR-0017's `output_guarded`; the proposal's `note` where something after it did not finish and ADR-0022's digest of its canonical JSON; the critic's comparison of declared outcomes with the runtime's, and the cases a host declares for it (agent#10); a delegated child's prose so far, `message_progress` with its `taskId` (agent#16); the approval pause's event and the guardrail's reasons; ADR-0018's `export_written`, self-describing critiques and `records expired`; ADR-0019's source identity; ADR-0021's `steered` and `steer refused`; ADR-0020's `connection_switched`, terminal `usage` and `failover refused`; and ADR-0022's `recorded` and `record refused`; task plan revision, departure, completion refusal and plan decision events | open | `$defs/steered`, `$defs/compacted`, `$defs/execution_checkpoint`, `$defs/budget_paused`, `$defs/approval_requested`, `$defs/skill_loaded`, `$defs/skill_unloaded`, `$defs/retry`, `$defs/response_recovery`, `$defs/delegation_requested`, `$defs/delegated_task`, `$defs/user_question`, `$defs/reasoning`, `$defs/message`, `$defs/message_progress`, `$defs/guardrail`, `$defs/thinking_unavailable`, `$defs/critic_decided`, `$defs/critique`, `$defs/proposal`, `$defs/error`, `$defs/end`, `$defs/caseComparison`, `$defs/criticCase`, `$defs/policy_resolved`, `$defs/memory_proposed`, `$defs/memory_recalled`, `$defs/edit_applied`, `$defs/edit_refused`, `$defs/output_guarded`, `$defs/export_written`, `$defs/connection_switched`, `$defs/usage`, `$defs/recorded`, `$defs/plan_revised`, `$defs/plan_departed`, `$defs/plan_completion_refused`, `$defs/plan_drift_decided`, `$defs/route_decided`, `$defs/route_widened`, `$defs/repeat_detected` | `web/src/assistant/engine.ts`, `AssistantEvent`; `packages/harness/records.ts` |
| `tool-offer.json` | The tools offered to a run, as the session's two members: `tools` (runtime tools as served, each named within the ceiling) and `hostTools` (host tools' declarations); the agent-wire alias form; the runtime ceiling (the Desk's five and `experimental_test_cases`) and reserved names; `propose_pack`, the proposal as a host tool call; a host tool's context policy, `runtime` among it (agent#8); the critic's allow-list (agent#10); tool sources (ADR-0006): the source record on a host tool, its effect class and approval, the tool-list digest, the wire name and the mapping, a declaration, a catalogue entry, `load_tool` and its answer; the source identity and identity digest (ADR-0019); the host's approval policy's three answers (ADR-0008); skills (ADR-0009): the session's `skills`, a skill as a host declares it, a catalogue entry, `load_skill`, `unload_skill` and a loaded skill as a load names it, and the source kind `exchange`; the critic's switch (ADR-0010): the session's `critic`, `criticProject`, `criticReasoning`, `existingPack`, `reviewerAssigned` , the modes, the tiers of each capability, and `request_review`, a reserved name | open | `$defs/agentWireTool`, `$defs/identity`, `$defs/sourceRecord`, `$defs/nameMapping`, `$defs/loadedSchema`, `$defs/skillCatalogueEntry`, `$defs/loadedSkill`, `$defs/subagentCatalogueEntry`, `$defs/remember`, `$defs/propose_edit`, `$defs/propose_delete`, `$defs/propose_insert`, `$defs/undo_edit` | `web/src/assistant/engine.ts`, `McpTool`, `HostTool`; `context/session.ts`, `contextualSession`; `internal/codexbridge/contract.go`, `Tool` |
| `tool-call.json` | The `tool_call` event, with the source record of a tool with a source (ADR-0006); the `tools/call` params the gate inspects; the arguments `experimental_test_cases` may leave with; the agent-wire call; a call made in text, the JSON inside its fence | closed | the root, `$defs/testCasesArguments`, `$defs/textToolCall` | `web/src/assistant/engine.ts`, `AssistantEvent`; `toolGate.ts`, `inspectOutboundFrame`; `agentTransport.ts`, `bindAgentRun` |
| `tool-result.json` | The `tool_result` event, with the source record (and its receipt) of a tool with a source and `proposed` for a held proposal (ADR-0006); the MCP result shape, image parts included; the bound, the read-back reference and its envelopes; `read_context` arguments; the reader's arguments and result (`read_window`, and every reading tool's window, its lines numbered); an image item read whole, or the line saying why it cannot be shown; an attachment; `search`'s arguments and result; the agent-wire answer; a reading tool's answer as the model is shown it, framed; the message that carries a text call's result back; the index of earlier results a chat-scoped run is shown, and the key a chat's read cache keeps a read under (agent#8) | closed | the root, `$defs/readContextArguments`, `$defs/readArguments`, `$defs/imagePart`, `$defs/searchArguments`, `$defs/agentWireAnswer`, `$defs/turnIndex`, `$defs/readCacheKey` | `web/src/assistant/engine.ts`, `McpToolResult`; `context/session.ts`, `SessionContext.result`; `context/ledger.ts`, `ContextLedger.reference`, `ContextLedger.tools`, `ContextDocument`; the reader, the search and images are this repository's, `packages/harness/context/reader.ts`, `context/search.ts`, `context/pattern.ts`, `images.ts` and `context/policy.ts`, `readWindowBytes` (agent#1, agent#5); the chat-scoped context is this repository's, `context/chat.ts`, `ChatTurn.index`, `canonicalJSON`, `readCacheKey` (agent#8) |
| `approval.json` | The host's approval state for a chat and the one grant a user decision mints, for one of the server's four actions; the grant request | open | `$defs/request` | `web/src/assistant/approvals/client.ts`, `ActionApproval`, `ActionGrant`; `internal/desk/assistant_approval.go`, `assistantActionGrant`, `assistantApprovalRequest` |
| `question.json` | One question to the person (`ask_user`); the answer draft; a saved question; the format-failure codes | closed | the root, `$defs/option`, `$defs/draft`, `$defs/saved` | `web/src/assistant/questions.ts`, `readUserQuestion`, `validQuestionDraft`, `readSavedQuestions`; `questionFailure.ts`, `QuestionFailureCode` |
| `delegated-task.json` | One child task's state, with its budget, model, references, the task it resumes and, for long findings, the cursor into the whole; its report in its profile's shape, partial, reused and retried, and its tokens, time to first byte and duration (agent#16, agent#17, agent#18); the delegation request, material passed by reference; a task profile with its model, budget, Runtime tools and report shape; a child's budget; the four report shapes, their findings, citations and open questions; the delegate call's result for a batch; the brief a child's prompt ends with, and the text a codex parent's next run is given after a batch (agent#13, agent#14, agent#15); what a host passes with a response about reuse (agent#17) | closed | the root, `$defs/request`, `$defs/taskRequest`, `$defs/budget`, `$defs/resultCursor`, `$defs/answer`, `$defs/answerTask`, `$defs/brief`, `$defs/briefReference`, `$defs/earlierWork`, `$defs/citation`, `$defs/analyzeFinding`, `$defs/reviewFinding`, `$defs/researchFinding`, `$defs/authorTestFinding`, `$defs/openQuestion`, `$defs/analyzeReport`, `$defs/reviewReport`, `$defs/researchReport`, `$defs/authorTestReport` | `web/src/assistant/delegationContract.ts`, `DelegatedTask`, `validDelegatedTask`, `readDelegationRequest`, `TaskProfile`; the profiles' budgets, references, resume, the answer, the reports, partial, reuse, the retry and the figures are this repository's, `packages/harness/delegationContract.ts`, `TASK_PROFILES`, `TaskBudget`, `readReport`, `reportInstructions`, and `delegation.ts`, `withDelegation`, `findingsHead`, `fitReport`, `childRetry` |
| `checkpoint.json` | The execution checkpoint: allowance, grants, plan, operations, completions, retained context (for a chat-scoped run, the entries it retained or read, agent#8); the turn's test-call count; delegation's figures (agent#18; `modelRequests` required on them where `unit` is `model-requests` and optional, absent on an agent capability, where it is `provider-managed`: the schema's `allOf` condition, agent#71); the digest of every request body the adapter sent (ADR-0005); the delegation handoff on an agent capability, the call, its state, the batch's records and the call's result (ADR-0007); the run report's rows by source (ADR-0006); the per-source identity pins, withholding and service-approval history (`sourceIdentities`, ADR-0019); ADR-0021's queued and delivered steering (`steers`), followed by ADR-0020's usage ledger, both carried through interruption and resume; the act call paused for the person's decision and what became of it (`approval`, with the run's framing `marker`, which the continued run reuses so the tool list is the same bytes across the pause), the person's decision, and the continuation's text (ADR-0008); the skills loaded, in load order (`skills`, ADR-0009); the critic's decision, or the model's ask before it, closed (`critic`, ADR-0010); stable task ids, bounded content revision history, acknowledged completion evidence, accepted-plan lineage and purpose-bound departure decisions | closed | the root, `$defs/grant`, `$defs/taskStep`, `$defs/operation`, `$defs/retainedEntry`, `$defs/handoff`, `$defs/approval`, `$defs/approvalDecision`, `$defs/draftDocument`, `$defs/draft`, `$defs/sourceIdentity`, `$defs/durableBook`, `$defs/durableScope`, `$defs/durableDispatch`, `$defs/durableObservation`, `$defs/artifactObservation`, `$defs/planChange`, `$defs/planRevision`, `$defs/planState`, `$defs/stepEvidence`, `$defs/planOrigin`, `$defs/acceptedPlan`, `$defs/planDeparture`, `$defs/planDepartureRecord`, `$defs/planRefusal`, `$defs/pendingDeparture`, `$defs/planDecision`, `$defs/nativePlanPause`, `$defs/nativePlanInvocation`, `$defs/taskCoordinatorBinding`, `$defs/runtimeExecutionOwner`, `$defs/runtimeApprovalBinding`, `$defs/taskStepShare`, `$defs/taskScheduleBinding`, `$defs/taskStepClaim`, `$defs/taskChildResult`, `$defs/taskChildBinding`, `$defs/taskStepRow`, `$defs/taskSchedule`, `$defs/taskExecutionEnvelope`, `$defs/taskEnvelopeBinding`, `$defs/taskPendingDelegation`, `$defs/taskCompletionEvidence`, `$defs/repeatObservation`, `$defs/repeatExecution`, `$defs/repeatTools` | `web/src/assistant/execution/state.ts`, `ExecutionCheckpoint`, `readExecution`; the handoff is the Go module's, `go/adapter/delegate/agent.go` and `go/context/state.go`, `ValidHandoff` |
| `corpus-checkpoint-view.json` | Read-only exact or derived corpus checkpoint display; frozen version 1/2 field graphs and current checkpoint 3 fields. Actual source and derived byte identities are distinct; the semantic reader never grants resume authority. | closed | the root, `$defs/v1.failure.json`, `$defs/v1.subagent.json`, `$defs/v1.policy.json`, `$defs/v1.skill.json`, `$defs/v1.tool-offer.json`, `$defs/v1.delegated-task.json`, `$defs/v1.usage.json`, `$defs/v1.memory.json`, `$defs/v1.provenance.json`, `$defs/v1.tool-result.json`, `$defs/v1.checkpoint.json`, `$defs/v2.failure.json`, `$defs/v2.subagent.json`, `$defs/v2.policy.json`, `$defs/v2.skill.json`, `$defs/v2.tool-offer.json`, `$defs/v2.delegated-task.json`, `$defs/v2.usage.json`, `$defs/v2.memory.json`, `$defs/v2.provenance.json`, `$defs/v2.tool-result.json`, `$defs/v2.checkpoint.json` | `packages/harness/history/corpusCheckpointView.ts`; `go/context/corpus_checkpoint_view.go` |
| `failure.json` | One failure: the catalogue's 23 classes, source, the `not-started`/`refused`/`unknown` outcome, HTTP metadata; a catalogue entry; a failed tool's content | closed | the root | `web/src/assistant/failures.ts`, `AIFailure`, `readFailure`; `internal/aifailure/failure.go`, `Failure`; `internal/aifailure/catalog.json` |
| `policy.json` | Host-only sections plus `compaction`, `outputGuard`, `outputPatterns`, `outputAllow`, `records` and `failover`; run over project over table; resolved members retain optional values, layers and sources (ADR-0011). `models.<role>.discovered` carries authenticated supported reasoning values and an optional default. An unsupported table value falls back to that default or omission; unsupported run and project values are refused. `models.compactor` selects ADR-0016's bounded summary request. ADR-0018 adds `records`, declaring retention, redaction on store and JSON export. ADR-0020 adds each role's ordered same-family `fallback` chain and the three default `failover.classes`. | closed | the root, `$defs/role`, `$defs/member`, `$defs/discovered`, `$defs/paceUsed`, `$defs/outputPattern`, `$defs/routeSelection` | `packages/harness/policy.ts`; `go/context/policy_table.go` |
| `budget.json` | The context budget table, with the critic's switch, `criticDefault` and `criticReasoningFloor`, with adversarialReview retired in v0.9.1; the per-model override and profile configuration; the request allowance the model is told, and beside it in a conversation the delegation coordinator wraps the tool-call cap (agent#13); the budget-increase answer; the fixed limits by their Desk names; the harness's turn clock and tool-call deadline; where the execution state sits on each wire (the two capability-record members that place it are `capability.json`'s); its bounds on the test tool, on the images one request carries and on a chat's index of earlier results; the host's three parts of every request, each with its layer, header, end line and bound, the project's own instructions bounded by `projectInstructionsBytes` (ADR-0005); no reserve for the brief or the memory (agent#69); the six bounds on tools with a source, their descriptions, the schema budget, their results, calls and deadline (ADR-0006); the skills budget, `skillBytes`, `skillsPerRun`, `skillsCatalogueBytes` and `cataloguedSkills` (ADR-0009) | open | `$defs/modelBudget`, `$defs/configuration`, `$defs/hostPart` | `internal/aicontext/policy.json`; `web/src/assistant/context/policy.ts`, `ContextPolicy`, `contextConfiguration`; `internal/aicontext/policy.go`, `Policy`, `Decode`; `execution/controller.ts`, `guidance`, `budgetTool`; the tool-call cap is this repository's, `execution/controller.ts`, `requestState`, `toolAllowance` (agent#13); the placement is this repository's, `packages/harness/engines/contract.ts`, `STATE_HEADER`, and `packages/harness/engines/vercel/loop.ts`, `statePlacement` (agent#9); so are the host's parts, `packages/harness/prompt.ts`, `HOST_PARTS`, `systemText`, `readHostParts` (ADR-0005), and the memory reserve, `context/policy.ts`, `contextPolicy` |
| `provenance.json` | One model request's record, including its record figures and the connection and role that answered, and the reports carried with a checkpoint, including ADR-0016's compaction summaries and report, ADR-0019's per-identity source call figures, ADR-0021's queued and delivered steering and ADR-0020's ledger row | closed | the root, `$defs/completion`, `$defs/promptDigest`, `$defs/compactionSummary`, `$defs/compactionReport`, `$defs/queuedSteer`, `$defs/deliveredSteer`, `$defs/steerReport`, `$defs/criticFigures`, `$defs/tokenFigures`, `$defs/childFigures`, `$defs/figuresTotal`, `$defs/batchFigures`, `$defs/parentFigures`, `$defs/delegationReport`, `$defs/sourceFigures`, `$defs/identityFigures`, `$defs/skillLoad`, `$defs/criticDecision`, `$defs/provider`, `$defs/crossProvider`, `$defs/memoryRecall`, `$defs/memories`, `$defs/edit`, `$defs/recordFigures` | `web/src/assistant/execution/state.ts`, `ExecutionCheckpoint.completions`; `completion.ts`, `ModelCompletion`, `readCompletion` |
| `export.json` | The closed `export.json` manifest: record schemas, versions, digests, lifetimes and byte counts, the records policy and package version. The exported records sit beside it and are supplied by the host; the package has no store (ADR records, #101). | closed | the root, `$defs/record` | `packages/harness/records.ts`, `exportRecords`; `go/context/records.go`, `ExportRecords` |
| `usage.json` | ADR-0020's closed run ledger: requests, reported tokens, invocations, milliseconds and bytes per source, connection and role; token members stay absent where the wire reports none | closed | the root, `$defs/tokens`, `$defs/row` | `packages/harness/execution/state.ts`, `UsageLedger`; `go/context/controller.go`, `ExecutionController.Usage` |
| `corpus-entry.json` | ADR-0022's evaluation-corpus manifest: id, package version, resolved policy, the five recorded files and timing/drawn-marker tolerances | closed | the root | `packages/harness/corpus.ts`; `go/context/corpus.go` |
| `capability.json` | What one endpoint and model can do, as the `vercel` adapter reads it wherever it decides: window and output cap, native and parallel tool calls, vision, the reasoning kind (and whether the endpoint ignores it), streaming quirks, the tool-schema subset, and where the execution state goes; what a host answers for an endpoint (any members of the record) | closed | the root, `$defs/reasoning`, `$defs/streamingQuirks`, `$defs/geminiModelPath`, `$defs/endpointCapabilities` | none at the pinned commit (the Desk decides by its three families: `web/src/assistant/thinking.ts`, `firstDialect`; `geminiSchema.ts`; `context/policy.ts`); the record is this repository's, `packages/harness/capabilities.ts`, `ModelCapabilities`, `FAMILY_CAPABILITIES`, `readCapabilities`, `capabilitiesFor` (agent#4, agent#9) |
| `memory.json` | Closed typed memory records and metadata-only lifecycle inputs; bare text retired in v0.9.1 (ADR-0014) | closed | the root, `$defs/lifecycleUpdate` | `packages/harness/memories.ts`; `go/context/memories.go` |
| `skill.json` | A skill (ADR-0009): its id, its name, its description, its source record (`exchange` among the kinds, `runtime` refused), its instructions, the host tools it names, its resources, the digest the person approved (`pinnedDigest`, on the declaration) and its own (`digest`, on admission); the digest's rule; a skill's size against `skillBytes` | closed | the root, `$defs/resource` | none at the pinned commit; the skill is this repository's, `packages/harness/skills.ts`, `SkillDeclaration`, `declareSkills`, `skillDigest`, `catalogueSkills`, `SkillGate` |
| `admitted-skill-v1.json` | Current in-process skill admission result: declaration fields, approved and computed digests, computed dependency capability profile, and optional measured bytes. The schema name versions this result; declarations gain no version member and historical admitted records remain under `skill.json`. The profile grants no tool authority. | closed | the root | `packages/harness/skills.ts`, `Skill`, `declareSkills` |

| `subagent.json` | Pinned reads-only host definition and its approved record (ADR-0013) | closed | the root, `$defs/record` | `packages/harness/subagents.ts`, `go/context/subagents.go` |
| `corpus-candidate.json` | Pending review candidate tied to a run, feedback digest, archive digest and source view, with feedback, review and replay data | closed | the root | `packages/harness/corpusCandidate.ts`, `go/context/corpus_candidate.go`, `ReadCorpusCandidate` |
| `feedback.json` | A run rating and its sequence, predecessor, target, actor and timestamp, with the report view used in summaries | closed | the root, `$defs/view` | `packages/harness/history/metadata.ts`, `readRunFeedback`; `go/context/history_metadata.go`, `ReadRunFeedback` |
| `history-metadata.json` | Capture metadata or feedback metadata, with optional candidate reference on feedback | open | | `packages/harness/history/metadata.ts`, `readHistoryMetadata`; `go/context/history_metadata.go`, `ReadHistoryMetadata` |
| `history-relation-evidence.json` | Observed relation evidence scoped to a dispatch and checkpoint, with observation and request/result digests and a target | closed | the root | `packages/harness/history/relations.ts`, `readHistoryRelationEvidence`; `go/context/history_relations.go`, `ReadHistoryRelationEvidence` |
| `run-archive-view.json` | A redacted view of an archive source, recording its source kind and version, redactions and displayed document | closed | the root | `packages/harness/history/archive.ts`, `ArchiveRepresentation`; `go/context/history_archive.go`, `archiveView` |
| `run-archive.json` | An archive manifest with typed mappings and members plus unique byte objects | closed | the root, `$defs/ref`, `$defs/head` | `packages/harness/history/archive.ts`, `exportRunArchive`, `readRunArchive`; `go/context/history_archive.go`, `ReadRunArchive` |
| `run-capture.json` | A capture record with run and execution revision, checkpoint, requests, published files and redaction marker | closed | the root | `packages/harness/history/metadata.ts`, `readRunCapture`; `go/context/history_metadata.go`, `ReadRunCapture` |
| `turn-route.json` | One acknowledged route selection with its decision, binding and catalogue digests, refusals and optional classifier or widening record | closed | the root, `$defs/selection`, `$defs/policy`, `$defs/classifier` | `packages/harness/turnRoute.ts`, `TurnRoute`; `go/context/turn_route.go`, `TurnRoute` |
| `run-lease.json` | One store resource's lease (ADR-0025, #223): owner, authoritative expiry by the store clock and exact bounded epoch; the fence presented at every checkpoint commit and effect admission; the epoch-free effect idempotency identity; named lease and capability refusals | closed | `$defs/fence`, `$defs/operation`, `$defs/effectAdmission`, `$defs/refusal`, `$defs/capabilityRefusal` | `packages/harness/storage/lease.ts`; `go/context/run_lease.go` |
| `run-wait.json` | One run's durable wait (ADR-0025 decisions 2, 3, 5 and 6, #224, #225, #227 and #228): its reason (approval, budget or question) and the pending decision read from the retained checkpoint or event log, its wake condition, timeout, deadline policy and stamped deadline, an approval wait's binding, its pins, continuation, parked-head binding, wake-key digest and provider release evidence, who parked and resumed it and when, the winning wake, a deadline that ended it, the revalidation its resume recorded, a question's answered or unanswered resolution; the host's wake evidence; the one `needs_attention` event a park records and its delivery attempts; the park/wake report and the export it is read from; named wait and capability refusals | closed | the root, `$defs/pendingApproval`, `$defs/pendingBudget`, `$defs/pendingQuestion`, `$defs/eventCondition`, `$defs/condition`, `$defs/checkpointBinding`, `$defs/chatBinding`, `$defs/release`, `$defs/actor`, `$defs/disposition`, `$defs/resolution`, `$defs/wakeEvidence`, `$defs/attention`, `$defs/delivery`, `$defs/content`, `$defs/reportEntry`, `$defs/report`, `$defs/export`, `$defs/refusal`, `$defs/capabilityRefusal` | `packages/harness/storage/wait.ts`; `go/context/run_wait.go` |
| `run-resume.json` | Deadlines, approval expiry and resume revalidation (ADR-0025 decisions 3 and 5, #225 and #227): the revalidation a resume records (pins and evidence digests, every change with its admission or trusted layer); the host's deadline policy and a run's deadline; the deadline a park stamps; an approval wait's binding; the pins a park reads from the retained checkpoint; the host's current evidence and explicit admissions; named revalidation refusals | closed | the root, `$defs/limits`, `$defs/runLimit`, `$defs/deadline`, `$defs/approval`, `$defs/model`, `$defs/sourcePin`, `$defs/artifactPin`, `$defs/planPin`, `$defs/pins`, `$defs/currentModel`, `$defs/currentPolicy`, `$defs/admission`, `$defs/evidence`, `$defs/change`, `$defs/refusal` | `packages/harness/storage/resume.ts`; `go/context/run_resume.go` |

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

## Provider failover and usage (ADR-0020)

`policy.json` adds ordered `models.<role>.fallback` connection IDs and the
three-class `failover.classes` default. Admission refuses an unavailable or
cross-family fallback. `run-event.json` adds `connection_switched`, terminal
`usage`, and guardrail reason `failover refused`; completion provenance records
the answering connection and role. Closed `usage.json` rows count requests,
provider-managed invocations, milliseconds, bytes and only provider-reported
tokens per source, connection and role. `checkpoint.usage` survives resume.
The five hand-authored vectors in `vectors/failover/` carry every decided event,
refusal and token-presence behavior.

## Context compaction (ADR-0016)

`policy.json` adds `compaction`, `budget.summaryBytes` and `models.compactor`.
`run-event.json` adds the `compacted` marker and `compaction refused`; the
checkpoint and provenance schemas carry stored summary rows and replaced-turn
digests for byte-identical resume. The four handwritten vectors in
`vectors/compaction/` cover the unchanged index, a framed bounded summary, failure
fallback and the checkpoint member. Tool results remain in the retained index.

The output boundary (ADR-0017, agent#100) defaults to `outputGuard: redact`.
Answers, proposals, tool arguments, memory proposals and draft edits receive
labelled holes for the versioned base patterns and host `outputPatterns`.
`outputAllow` disables stable names; `refuse` stops the outbound step with an
`output refused` guardrail. `output_guarded` events and `outputGuard` counts make
the decision visible and carry it through checkpoint resume. Redacted proposals
are validated with their holes. Model input remains unchanged, and incomplete
fragments wait for the complete guarded answer.

Memory's closed `$defs/lifecycleUpdate` and array `$defs/lifecycleAnswer` describe the optional
metadata-only host input in v0.9.1. They do not extend checkpoint or provenance representation.
The canonical session validates transition/identity consistency and the whole resulting book;
no-answer callbacks do not revoke. Bare text and the old critic/flat-policy inputs are refused.

Versioned checkpoint, event and policy readers follow [ADR-0024](../docs/adr/0024-versioned-record-readers.md). Current writers use checkpoint 2 and event/policy 1; supported historical formats have frozen structural inventories.

## Storage recovery records

Checkpoint version 2 adds `durable`: scoped pre-dispatch/result/unknown claims, exact safe
prepared request bytes and their digests, acknowledged operation/artifact provenance, child
checkpoints and pending handoff intent. Handoff may retain exact `continuation` bytes.
`vectors/storage/checkpoints.json` covers the added members and enum values in both readers.
The stored event envelope is version 1 `{version,scope,event}`; cursors/revisions remain host
metadata outside signed records. See [storage integration](../docs/design/storage.md).

Taxonomy consumer fixtures live in `testdata/taxonomy/`: classification and preset expectations,
profiles, Gateway snapshots and receipts. Their handwritten malformed inputs are exercised by
both language readers. They are harness inputs and expected behavior, not serialized schema records.
The `rehearsal` rows of `tools.json` hold the ADR-0027 amendment's (#241) host declarations of a
rehearse check, admitted with its hints and approval or refused for its description.
`vectors/taxonomy/declarations.json` and `profiles.json` carry actual closed taxonomy records;
`declarations.json` `refused` carries four host declarations both validators refuse, each with
its reason (a host `evaluate`, and `rehearse` under read, with read-external and with act).

The #171 dialect draft schema additionally admits `originalField`, `originalRevision`,
`conversionContract` and `retainedRead` definitions. New factory candidates retain the
complete original revision inventory (null means unavailable), exact source-template
text and selected occurrence, and the explicit package-pinned private Draft identity.
These members are additive for inspection of existing version-1 checkpoints; they grant
no save, acquisition, preparation or execution authority. The provisional private
Runtime document role must be repinned with reviewed immutable Spec artifacts before
full #171 release qualification. See [dialect authoring](../docs/design/dialect-authoring.md).
