# Run events

## Sources

Schemas:

- `contract/schemas/run-event.json`
- `contract/schemas/tool-call.json`
- `contract/schemas/tool-result.json`

> generated; edit the sources, then run `node scripts/gen-reference.mjs`. `--check` fails where this page differs from them.

One event of a run, from the adapter to the host, in order, discriminated by type. Every kind of the Desk's AssistantEvent union (web/src/assistant/engine.ts) at the pinned commit, with its members spelled as the Desk spells them. A run ends with exactly one end and nothing follows it; a run the consumer left gets no end. Some events are the host's own, interleaved into the same stream rather than emitted by a loop: the tool gate's guardrail rewrote and refused, the shared context boundary's execution_checkpoint, budget_paused (which also replaces a loop's own budget_paused or request-limit error) and approval_requested (ADR-0008: emitted in place of an act call's tool_call where the host's approval policy answers ask), and the delegation coordinator's delegated_task. critic_decided (ADR-0010) is emitted by the adapter once per run, where the critic would start or where it is skipped. The versioned record reader refuses newer versions and unknown structural members. Historical events omit version. Feature-free writers emit version 1. Writers select version 2 for repeat detection, route decisions/widening, runtime approval requests and events carrying checkpoint 3; task-aware plan lifecycle and approval events also explicitly select version 2. An explicitly selected version 2 is preserved.

See also: [tools.md](tools.md), [delegation.md](delegation.md), [critic.md](critic.md), [skills.md](skills.md), [budget.md](budget.md), [reasons.md](reasons.md), [checkpoint.md](checkpoint.md).

## The 41 kinds

| Kind | Defined in | Required beyond type |
| --- | --- | --- |
| `execution_checkpoint` | `run-event.json $defs/execution_checkpoint` | `checkpoint` |
| `budget_paused` | `run-event.json $defs/budget_paused` | `checkpoint` |
| `approval_requested` | `run-event.json $defs/approval_requested` | `callId` |
| `steered` | `run-event.json $defs/steered` | `bytes`, `atRequest` |
| `compacted` | `run-event.json $defs/compacted` | `turns`, `bytes`, `digest`, `mode` |
| `skill_loaded` | `run-event.json $defs/skill_loaded` | `skill`, `bytes` |
| `skill_unloaded` | `run-event.json $defs/skill_unloaded` | `id` |
| `retry` | `run-event.json $defs/retry` | `attempt` |
| `response_recovery` | `run-event.json $defs/response_recovery` |  |
| `delegation_requested` | `run-event.json $defs/delegation_requested` | `request` |
| `delegated_task` | `run-event.json $defs/delegated_task` | `task` |
| `user_question` | `run-event.json $defs/user_question` | `id`, `question` |
| `reasoning` | `run-event.json $defs/reasoning` | `text`, `done` |
| `message` | `run-event.json $defs/message` | `text` |
| `message_progress` | `run-event.json $defs/message_progress` | `text` |
| `tool_call` | `tool-call.json` | `name`, `args` |
| `tool_result` | `tool-result.json` | `name`, `isError`, `text` |
| `guardrail` | `run-event.json $defs/guardrail` | `tool`, `action`, `detail` |
| `thinking_unavailable` | `run-event.json $defs/thinking_unavailable` | `detail` |
| `critic_decided` | `run-event.json $defs/critic_decided` | `mode`, `ran`, `reason` |
| `critique` | `run-event.json $defs/critique` | `refuted`, `checks`, `text` |
| `proposal` | `run-event.json $defs/proposal` | `document`, `unknowns`, `digest` |
| `error` | `run-event.json $defs/error` | `message` |
| `end` | `run-event.json $defs/end` |  |
| `policy_resolved` | `run-event.json $defs/policy_resolved` | `members` |
| `memory_proposed` | `run-event.json $defs/memory_proposed` | `memory` |
| `memory_recalled` | `run-event.json $defs/memory_recalled` | `ids`, `digests`, `bytes` |
| `edit_applied` | `run-event.json $defs/edit_applied` | `document`, `line`, `bytes`, `valid` |
| `edit_refused` | `run-event.json $defs/edit_refused` | `reason` |
| `output_guarded` | `run-event.json $defs/output_guarded` | `where`, `pattern`, `action` |
| `export_written` | `run-event.json $defs/export_written` | `records`, `bytes`, `digest` |
| `connection_switched` | `run-event.json $defs/connection_switched` | `role`, `from`, `to`, `reason` |
| `usage` | `run-event.json $defs/usage` | `usage` |
| `recorded` | `run-event.json $defs/recorded` | `id`, `bytes` |
| `plan_revised` | `run-event.json $defs/plan_revised` | `execution`, `sequence`, `previous`, `revision`, `explanation`, `diff` |
| `plan_departed` | `run-event.json $defs/plan_departed` | `execution`, `revision`, `acceptedRevision`, `explanation`, `departures` |
| `plan_completion_refused` | `run-event.json $defs/plan_completion_refused` | `execution`, `revision`, `reason` |
| `plan_drift_decided` | `run-event.json $defs/plan_drift_decided` | `execution`, `callId`, `revision`, `candidateRevision`, `approved` |
| `route_decided` | `run-event.json $defs/route_decided` | `route` |
| `route_widened` | `run-event.json $defs/route_widened` | `route` |
| `repeat_detected` | `run-event.json $defs/repeat_detected` | `execution`, `dispatchId`, `keyDigest`, `count`, `reminder`, `stopped` |

## Emitters and package consumers

Source-derived publication/construction sites and explicit event discriminant reads in production package code. TypeScript follows compiler-resolved event arguments, yields, local bindings and returned factories; Go parses event constructors, event-return paths and the JSON-backed event factories. Tests, fixtures, examples, generated declarations and framework message parts do not establish emission. Every schema kind must have an emitter in both languages.

Consumer entries identify explicit kind reads, not delivery guarantees. Generic event forwarding and storage apply to all kinds and are not repeated in every row. Checkpoint/controller and run-report readers appear only for kinds they inspect; reports also project checkpoint facts and do not consume every kind individually.

| Kind | TypeScript emitters | Go emitters | TypeScript consumers | Go consumers |
| --- | --- | --- | --- | --- |
| `execution_checkpoint` | [`packages/harness/context/managed.ts:126`](../../packages/harness/context/managed.ts#L126), [`packages/harness/context/managed.ts:141`](../../packages/harness/context/managed.ts#L141), [`packages/harness/context/managed.ts:255`](../../packages/harness/context/managed.ts#L255), [`packages/harness/context/managed.ts:262`](../../packages/harness/context/managed.ts#L262), [`packages/harness/delegation.ts:498`](../../packages/harness/delegation.ts#L498), [`packages/harness/storage/writer.ts:248`](../../packages/harness/storage/writer.ts#L248), [`packages/harness/storage/writer.ts:277`](../../packages/harness/storage/writer.ts#L277), [`packages/harness/storage/writer.ts:465`](../../packages/harness/storage/writer.ts#L465), [`packages/harness/storage/writer.ts:748`](../../packages/harness/storage/writer.ts#L748), [`packages/harness/storage/writer.ts:777`](../../packages/harness/storage/writer.ts#L777) | [`go/adapter/delegate/agent.go:371`](../../go/adapter/delegate/agent.go#L371), [`go/adapter/delegate/calls.go:118`](../../go/adapter/delegate/calls.go#L118), [`go/context/managed.go:138`](../../go/context/managed.go#L138), [`go/context/managed.go:240`](../../go/context/managed.go#L240), [`go/context/managed.go:426`](../../go/context/managed.go#L426), [`go/context/native_plan_pause.go:128`](../../go/context/native_plan_pause.go#L128), [`go/context/native_plan_pause.go:159`](../../go/context/native_plan_pause.go#L159), [`go/context/run_writer.go:2496`](../../go/context/run_writer.go#L2496), [`go/context/run_writer.go:2608`](../../go/context/run_writer.go#L2608), [`go/context/run_writer.go:464`](../../go/context/run_writer.go#L464), [`go/context/session.go:2019`](../../go/context/session.go#L2019) | [`packages/harness/corpus.ts:167`](../../packages/harness/corpus.ts#L167), [`packages/harness/delegation.ts:1033`](../../packages/harness/delegation.ts#L1033), [`packages/harness/delegation.ts:1034`](../../packages/harness/delegation.ts#L1034), [`packages/harness/delegation.ts:721`](../../packages/harness/delegation.ts#L721), [`packages/harness/history/capture.ts:74`](../../packages/harness/history/capture.ts#L74), [`packages/harness/history/view.ts:93`](../../packages/harness/history/view.ts#L93), [`packages/harness/recordReader.ts:102`](../../packages/harness/recordReader.ts#L102), [`packages/harness/runReport.ts:79`](../../packages/harness/runReport.ts#L79), [`packages/harness/runReport.ts:84`](../../packages/harness/runReport.ts#L84), [`packages/harness/storage/nativePlanPause.ts:64`](../../packages/harness/storage/nativePlanPause.ts#L64), [`packages/harness/storage/writer.ts:530`](../../packages/harness/storage/writer.ts#L530), [`packages/harness/storage/writer.ts:532`](../../packages/harness/storage/writer.ts#L532), [`packages/harness/storage/writer.ts:666`](../../packages/harness/storage/writer.ts#L666) | [`go/adapter/delegate/batch.go:314`](../../go/adapter/delegate/batch.go#L314), [`go/adapter/delegate/batch.go:318`](../../go/adapter/delegate/batch.go#L318), [`go/adapter/delegate/batch.go:319`](../../go/adapter/delegate/batch.go#L319), [`go/adapter/delegate/coordinator.go:545`](../../go/adapter/delegate/coordinator.go#L545), [`go/adapter/delegate/coordinator.go:546`](../../go/adapter/delegate/coordinator.go#L546), [`go/context/history_view.go:313`](../../go/context/history_view.go#L313), [`go/context/native_plan_pause.go:434`](../../go/context/native_plan_pause.go#L434), [`go/context/run_report.go:197`](../../go/context/run_report.go#L197), [`go/context/run_report.go:198`](../../go/context/run_report.go#L198), [`go/context/run_writer.go:1077`](../../go/context/run_writer.go#L1077), [`go/context/run_writer.go:1078`](../../go/context/run_writer.go#L1078), [`go/context/run_writer.go:711`](../../go/context/run_writer.go#L711), [`go/context/run_writer.go:712`](../../go/context/run_writer.go#L712), [`go/context/run_writer.go:726`](../../go/context/run_writer.go#L726), [`go/contract/records.go:583`](../../go/contract/records.go#L583) |
| `budget_paused` | [`packages/harness/context/managed.ts:126`](../../packages/harness/context/managed.ts#L126), [`packages/harness/engines/deepagents/loop.ts:1122`](../../packages/harness/engines/deepagents/loop.ts#L1122), [`packages/harness/engines/vercel/loop.ts:1840`](../../packages/harness/engines/vercel/loop.ts#L1840) | [`go/adapter/agent/agent.go:185`](../../go/adapter/agent/agent.go#L185), [`go/adapter/eino/run.go:209`](../../go/adapter/eino/run.go#L209), [`go/context/managed.go:287`](../../go/context/managed.go#L287), [`go/context/managed.go:297`](../../go/context/managed.go#L297) | [`packages/harness/context/managed.ts:184`](../../packages/harness/context/managed.ts#L184), [`packages/harness/context/managed.ts:209`](../../packages/harness/context/managed.ts#L209), [`packages/harness/context/managed.ts:223`](../../packages/harness/context/managed.ts#L223), [`packages/harness/delegation.ts:1033`](../../packages/harness/delegation.ts#L1033), [`packages/harness/delegation.ts:728`](../../packages/harness/delegation.ts#L728), [`packages/harness/history/view.ts:93`](../../packages/harness/history/view.ts#L93), [`packages/harness/recordReader.ts:102`](../../packages/harness/recordReader.ts#L102), [`packages/harness/runReport.ts:79`](../../packages/harness/runReport.ts#L79), [`packages/harness/runReport.ts:84`](../../packages/harness/runReport.ts#L84), [`packages/harness/storage/writer.ts:530`](../../packages/harness/storage/writer.ts#L530), [`packages/harness/storage/writer.ts:532`](../../packages/harness/storage/writer.ts#L532), [`packages/harness/storage/writer.ts:666`](../../packages/harness/storage/writer.ts#L666) | [`go/adapter/delegate/batch.go:352`](../../go/adapter/delegate/batch.go#L352), [`go/adapter/delegate/coordinator.go:562`](../../go/adapter/delegate/coordinator.go#L562), [`go/adapter/delegate/coordinator.go:563`](../../go/adapter/delegate/coordinator.go#L563), [`go/context/history_view.go:313`](../../go/context/history_view.go#L313), [`go/context/managed.go:183`](../../go/context/managed.go#L183), [`go/context/managed.go:230`](../../go/context/managed.go#L230), [`go/context/managed.go:272`](../../go/context/managed.go#L272), [`go/context/run_report.go:199`](../../go/context/run_report.go#L199), [`go/context/run_report.go:200`](../../go/context/run_report.go#L200), [`go/context/run_writer.go:1080`](../../go/context/run_writer.go#L1080), [`go/context/run_writer.go:1081`](../../go/context/run_writer.go#L1081), [`go/context/run_writer.go:720`](../../go/context/run_writer.go#L720), [`go/context/run_writer.go:721`](../../go/context/run_writer.go#L721), [`go/context/run_writer.go:726`](../../go/context/run_writer.go#L726), [`go/contract/records.go:583`](../../go/contract/records.go#L583) |
| `approval_requested` | [`packages/harness/context/session.ts:531`](../../packages/harness/context/session.ts#L531), [`packages/harness/execution/controller.ts:368`](../../packages/harness/execution/controller.ts#L368), [`packages/harness/execution/controller.ts:370`](../../packages/harness/execution/controller.ts#L370) | [`go/context/plan_actions.go:377`](../../go/context/plan_actions.go#L377), [`go/context/session.go:1344`](../../go/context/session.go#L1344) | [`packages/harness/context/managed.ts:127`](../../packages/harness/context/managed.ts#L127), [`packages/harness/context/managed.ts:148`](../../packages/harness/context/managed.ts#L148), [`packages/harness/context/managed.ts:183`](../../packages/harness/context/managed.ts#L183), [`packages/harness/context/managed.ts:191`](../../packages/harness/context/managed.ts#L191), [`packages/harness/context/managed.ts:220`](../../packages/harness/context/managed.ts#L220), [`packages/harness/context/managed.ts:255`](../../packages/harness/context/managed.ts#L255), [`packages/harness/recordReader.ts:102`](../../packages/harness/recordReader.ts#L102), [`packages/harness/storage/writer.ts:529`](../../packages/harness/storage/writer.ts#L529), [`packages/harness/storage/writer.ts:653`](../../packages/harness/storage/writer.ts#L653) | [`go/context/managed.go:192`](../../go/context/managed.go#L192), [`go/context/managed.go:193`](../../go/context/managed.go#L193), [`go/context/managed.go:232`](../../go/context/managed.go#L232), [`go/context/run_writer.go:1989`](../../go/context/run_writer.go#L1989), [`go/context/run_writer.go:1990`](../../go/context/run_writer.go#L1990), [`go/context/run_writer.go:2007`](../../go/context/run_writer.go#L2007), [`go/context/run_writer.go:2008`](../../go/context/run_writer.go#L2008), [`go/contract/records.go:583`](../../go/contract/records.go#L583) |
| `compacted` | [`packages/harness/context/session.ts:808`](../../packages/harness/context/session.ts#L808) | [`go/context/session.go:1150`](../../go/context/session.go#L1150) | — | — |
| `skill_loaded` | [`packages/harness/skills.ts:493`](../../packages/harness/skills.ts#L493) | [`go/context/skills.go:651`](../../go/context/skills.go#L651) | — | — |
| `skill_unloaded` | [`packages/harness/skills.ts:505`](../../packages/harness/skills.ts#L505) | [`go/context/skills.go:686`](../../go/context/skills.go#L686) | — | — |
| `retry` | [`packages/harness/engines/codex/index.ts:126`](../../packages/harness/engines/codex/index.ts#L126), [`packages/harness/engines/deepagents/loop.ts:597`](../../packages/harness/engines/deepagents/loop.ts#L597), [`packages/harness/engines/vercel/loop.ts:1086`](../../packages/harness/engines/vercel/loop.ts#L1086) | [`go/adapter/agent/invoke.go:515`](../../go/adapter/agent/invoke.go#L515), [`go/adapter/eino/run.go:400`](../../go/adapter/eino/run.go#L400) | — | — |
| `response_recovery` | [`packages/harness/engines/codex/index.ts:321`](../../packages/harness/engines/codex/index.ts#L321), [`packages/harness/engines/deepagents/loop.ts:711`](../../packages/harness/engines/deepagents/loop.ts#L711), [`packages/harness/engines/deepagents/loop.ts:714`](../../packages/harness/engines/deepagents/loop.ts#L714), [`packages/harness/engines/vercel/loop.ts:1192`](../../packages/harness/engines/vercel/loop.ts#L1192), [`packages/harness/engines/vercel/loop.ts:1195`](../../packages/harness/engines/vercel/loop.ts#L1195) | [`go/adapter/agent/agent.go:410`](../../go/adapter/agent/agent.go#L410), [`go/adapter/eino/loopphase.go:43`](../../go/adapter/eino/loopphase.go#L43), [`go/adapter/eino/loopphase.go:55`](../../go/adapter/eino/loopphase.go#L55) | — | — |
| `delegation_requested` | [`packages/harness/engines/codex/index.ts:183`](../../packages/harness/engines/codex/index.ts#L183) | [`go/adapter/agent/invoke.go:766`](../../go/adapter/agent/invoke.go#L766) | [`packages/harness/context/managed.ts:167`](../../packages/harness/context/managed.ts#L167), [`packages/harness/delegation.ts:1035`](../../packages/harness/delegation.ts#L1035), [`packages/harness/delegation.ts:734`](../../packages/harness/delegation.ts#L734), [`packages/harness/storage/writer.ts:666`](../../packages/harness/storage/writer.ts#L666) | [`go/adapter/delegate/coordinator.go:534`](../../go/adapter/delegate/coordinator.go#L534), [`go/adapter/delegate/coordinator.go:536`](../../go/adapter/delegate/coordinator.go#L536), [`go/context/output_boundary.go:75`](../../go/context/output_boundary.go#L75), [`go/context/output_boundary.go:76`](../../go/context/output_boundary.go#L76), [`go/context/output_boundary.go:80`](../../go/context/output_boundary.go#L80), [`go/context/run_writer.go:1069`](../../go/context/run_writer.go#L1069), [`go/context/run_writer.go:1070`](../../go/context/run_writer.go#L1070) |
| `delegated_task` | [`packages/harness/delegation.ts:464`](../../packages/harness/delegation.ts#L464) | [`go/adapter/delegate/coordinator.go:368`](../../go/adapter/delegate/coordinator.go#L368) | [`packages/harness/storage/writer.ts:320`](../../packages/harness/storage/writer.ts#L320), [`packages/harness/storage/writer.ts:536`](../../packages/harness/storage/writer.ts#L536) | [`go/context/run_writer.go:2664`](../../go/context/run_writer.go#L2664), [`go/context/run_writer.go:2669`](../../go/context/run_writer.go#L2669), [`go/context/run_writer.go:2680`](../../go/context/run_writer.go#L2680), [`go/context/run_writer.go:2840`](../../go/context/run_writer.go#L2840), [`go/context/run_writer.go:2843`](../../go/context/run_writer.go#L2843), [`go/context/run_writer.go:2844`](../../go/context/run_writer.go#L2844), [`go/context/run_writer.go:730`](../../go/context/run_writer.go#L730), [`go/context/run_writer.go:733`](../../go/context/run_writer.go#L733) |
| `user_question` | [`packages/harness/engines/codex/index.ts:191`](../../packages/harness/engines/codex/index.ts#L191), [`packages/harness/engines/deepagents/loop.ts:516`](../../packages/harness/engines/deepagents/loop.ts#L516), [`packages/harness/engines/vercel/loop.ts:976`](../../packages/harness/engines/vercel/loop.ts#L976) | [`go/adapter/agent/invoke.go:777`](../../go/adapter/agent/invoke.go#L777), [`go/adapter/eino/calls.go:318`](../../go/adapter/eino/calls.go#L318) | [`packages/harness/context/managed.ts:166`](../../packages/harness/context/managed.ts#L166), [`packages/harness/context/managed.ts:184`](../../packages/harness/context/managed.ts#L184), [`packages/harness/context/managed.ts:229`](../../packages/harness/context/managed.ts#L229), [`packages/harness/context/managed.ts:230`](../../packages/harness/context/managed.ts#L230), [`packages/harness/context/managed.ts:231`](../../packages/harness/context/managed.ts#L231), [`packages/harness/delegation.ts:732`](../../packages/harness/delegation.ts#L732) | [`go/adapter/delegate/batch.go:358`](../../go/adapter/delegate/batch.go#L358), [`go/adapter/delegate/batch.go:359`](../../go/adapter/delegate/batch.go#L359), [`go/context/managed.go:183`](../../go/context/managed.go#L183), [`go/context/managed.go:305`](../../go/context/managed.go#L305), [`go/context/managed.go:313`](../../go/context/managed.go#L313), [`go/context/output_boundary.go:67`](../../go/context/output_boundary.go#L67), [`go/context/output_boundary.go:68`](../../go/context/output_boundary.go#L68), [`go/context/output_boundary.go:72`](../../go/context/output_boundary.go#L72) |
| `reasoning` | [`packages/harness/engines/deepagents/loop.ts:616`](../../packages/harness/engines/deepagents/loop.ts#L616), [`packages/harness/engines/deepagents/loop.ts:619`](../../packages/harness/engines/deepagents/loop.ts#L619), [`packages/harness/engines/vercel/loop.ts:1304`](../../packages/harness/engines/vercel/loop.ts#L1304), [`packages/harness/engines/vercel/loop.ts:1310`](../../packages/harness/engines/vercel/loop.ts#L1310), [`packages/harness/engines/vercel/loop.ts:1618`](../../packages/harness/engines/vercel/loop.ts#L1618), [`packages/harness/engines/vercel/loop.ts:1622`](../../packages/harness/engines/vercel/loop.ts#L1622) | [`go/adapter/eino/step.go:36`](../../go/adapter/eino/step.go#L36), [`go/adapter/eino/step.go:42`](../../go/adapter/eino/step.go#L42) | [`packages/harness/context/managed.ts:161`](../../packages/harness/context/managed.ts#L161), [`packages/harness/context/managed.ts:162`](../../packages/harness/context/managed.ts#L162) | [`go/context/managed.go:154`](../../go/context/managed.go#L154), [`go/context/output_boundary.go:49`](../../go/context/output_boundary.go#L49), [`go/context/output_boundary.go:50`](../../go/context/output_boundary.go#L50) |
| `message` | [`packages/harness/engines/codex/index.ts:137`](../../packages/harness/engines/codex/index.ts#L137), [`packages/harness/engines/codex/index.ts:297`](../../packages/harness/engines/codex/index.ts#L297), [`packages/harness/engines/codex/index.ts:318`](../../packages/harness/engines/codex/index.ts#L318), [`packages/harness/engines/codex/index.ts:336`](../../packages/harness/engines/codex/index.ts#L336), [`packages/harness/engines/codex/index.ts:347`](../../packages/harness/engines/codex/index.ts#L347), [`packages/harness/engines/deepagents/loop.ts:680`](../../packages/harness/engines/deepagents/loop.ts#L680), [`packages/harness/engines/deepagents/loop.ts:889`](../../packages/harness/engines/deepagents/loop.ts#L889), [`packages/harness/engines/deepagents/loop.ts:895`](../../packages/harness/engines/deepagents/loop.ts#L895), [`packages/harness/engines/deepagents/loop.ts:900`](../../packages/harness/engines/deepagents/loop.ts#L900), [`packages/harness/engines/vercel/loop.ts:1163`](../../packages/harness/engines/vercel/loop.ts#L1163), [`packages/harness/engines/vercel/loop.ts:1445`](../../packages/harness/engines/vercel/loop.ts#L1445), [`packages/harness/engines/vercel/loop.ts:1451`](../../packages/harness/engines/vercel/loop.ts#L1451), [`packages/harness/engines/vercel/loop.ts:1458`](../../packages/harness/engines/vercel/loop.ts#L1458) | [`go/adapter/agent/agent.go:627`](../../go/adapter/agent/agent.go#L627), [`go/adapter/eino/loop.go:174`](../../go/adapter/eino/loop.go#L174), [`go/adapter/eino/loop.go:350`](../../go/adapter/eino/loop.go#L350) | [`packages/harness/context/managed.ts:163`](../../packages/harness/context/managed.ts#L163), [`packages/harness/delegation.ts:724`](../../packages/harness/delegation.ts#L724), [`packages/harness/history/feedback.ts:29`](../../packages/harness/history/feedback.ts#L29), [`packages/harness/storage/history.ts:129`](../../packages/harness/storage/history.ts#L129) | [`go/adapter/delegate/batch.go:332`](../../go/adapter/delegate/batch.go#L332), [`go/adapter/delegate/batch.go:336`](../../go/adapter/delegate/batch.go#L336), [`go/context/history_metadata_store.go:130`](../../go/context/history_metadata_store.go#L130), [`go/context/output_boundary.go:52`](../../go/context/output_boundary.go#L52), [`go/context/output_boundary.go:53`](../../go/context/output_boundary.go#L53) |
| `message_progress` | [`packages/harness/delegation.ts:726`](../../packages/harness/delegation.ts#L726), [`packages/harness/delegation.ts:885`](../../packages/harness/delegation.ts#L885), [`packages/harness/delegation.ts:902`](../../packages/harness/delegation.ts#L902), [`packages/harness/engines/codex/index.ts:132`](../../packages/harness/engines/codex/index.ts#L132), [`packages/harness/engines/deepagents/loop.ts:613`](../../packages/harness/engines/deepagents/loop.ts#L613), [`packages/harness/engines/deepagents/loop.ts:710`](../../packages/harness/engines/deepagents/loop.ts#L710), [`packages/harness/engines/vercel/loop.ts:1174`](../../packages/harness/engines/vercel/loop.ts#L1174), [`packages/harness/engines/vercel/loop.ts:1191`](../../packages/harness/engines/vercel/loop.ts#L1191) | [`go/adapter/agent/invoke.go:522`](../../go/adapter/agent/invoke.go#L522), [`go/adapter/delegate/batch.go:342`](../../go/adapter/delegate/batch.go#L342), [`go/adapter/delegate/run.go:121`](../../go/adapter/delegate/run.go#L121), [`go/adapter/delegate/run.go:301`](../../go/adapter/delegate/run.go#L301), [`go/adapter/eino/loopphase.go:40`](../../go/adapter/eino/loopphase.go#L40), [`go/adapter/eino/step.go:30`](../../go/adapter/eino/step.go#L30) | [`packages/harness/context/managed.ts:161`](../../packages/harness/context/managed.ts#L161), [`packages/harness/delegation.ts:726`](../../packages/harness/delegation.ts#L726), [`packages/harness/engines/deepagents/loop.ts:409`](../../packages/harness/engines/deepagents/loop.ts#L409), [`packages/harness/engines/vercel/loop.ts:825`](../../packages/harness/engines/vercel/loop.ts#L825) | [`go/adapter/delegate/batch.go:338`](../../go/adapter/delegate/batch.go#L338), [`go/adapter/delegate/batch.go:339`](../../go/adapter/delegate/batch.go#L339), [`go/adapter/delegate/batch.go:340`](../../go/adapter/delegate/batch.go#L340), [`go/adapter/delegate/batch.go:342`](../../go/adapter/delegate/batch.go#L342), [`go/adapter/eino/run.go:259`](../../go/adapter/eino/run.go#L259), [`go/context/managed.go:154`](../../go/context/managed.go#L154) |
| `tool_call` | [`packages/harness/engines/codex/index.ts:153`](../../packages/harness/engines/codex/index.ts#L153), [`packages/harness/engines/codex/index.ts:171`](../../packages/harness/engines/codex/index.ts#L171), [`packages/harness/engines/codex/index.ts:247`](../../packages/harness/engines/codex/index.ts#L247), [`packages/harness/engines/deepagents/loop.ts:436`](../../packages/harness/engines/deepagents/loop.ts#L436), [`packages/harness/engines/deepagents/loop.ts:462`](../../packages/harness/engines/deepagents/loop.ts#L462), [`packages/harness/engines/deepagents/loop.ts:500`](../../packages/harness/engines/deepagents/loop.ts#L500), [`packages/harness/engines/deepagents/loop.ts:551`](../../packages/harness/engines/deepagents/loop.ts#L551), [`packages/harness/engines/deepagents/loop.ts:563`](../../packages/harness/engines/deepagents/loop.ts#L563), [`packages/harness/engines/vercel/loop.ts:1049`](../../packages/harness/engines/vercel/loop.ts#L1049), [`packages/harness/engines/vercel/loop.ts:848`](../../packages/harness/engines/vercel/loop.ts#L848), [`packages/harness/engines/vercel/loop.ts:899`](../../packages/harness/engines/vercel/loop.ts#L899), [`packages/harness/engines/vercel/loop.ts:959`](../../packages/harness/engines/vercel/loop.ts#L959) | [`go/adapter/agent/invoke.go:888`](../../go/adapter/agent/invoke.go#L888), [`go/adapter/eino/calls.go:103`](../../go/adapter/eino/calls.go#L103) | [`packages/harness/context/managed.ts:164`](../../packages/harness/context/managed.ts#L164), [`packages/harness/context/managed.ts:184`](../../packages/harness/context/managed.ts#L184), [`packages/harness/context/managed.ts:192`](../../packages/harness/context/managed.ts#L192), [`packages/harness/context/managed.ts:193`](../../packages/harness/context/managed.ts#L193), [`packages/harness/context/managed.ts:229`](../../packages/harness/context/managed.ts#L229), [`packages/harness/corpus.ts:168`](../../packages/harness/corpus.ts#L168), [`packages/harness/execution/controller.ts:269`](../../packages/harness/execution/controller.ts#L269), [`packages/harness/history/capture.ts:119`](../../packages/harness/history/capture.ts#L119), [`packages/harness/history/capture.ts:77`](../../packages/harness/history/capture.ts#L77), [`packages/harness/toolSources.ts:1295`](../../packages/harness/toolSources.ts#L1295), [`packages/harness/toolSources.ts:1298`](../../packages/harness/toolSources.ts#L1298) | [`go/context/managed.go:183`](../../go/context/managed.go#L183), [`go/context/managed.go:195`](../../go/context/managed.go#L195), [`go/context/managed.go:202`](../../go/context/managed.go#L202), [`go/context/managed.go:267`](../../go/context/managed.go#L267), [`go/context/managed.go:268`](../../go/context/managed.go#L268), [`go/context/managed.go:305`](../../go/context/managed.go#L305), [`go/context/managed.go:307`](../../go/context/managed.go#L307), [`go/context/managed.go:308`](../../go/context/managed.go#L308), [`go/context/output_boundary.go:55`](../../go/context/output_boundary.go#L55), [`go/context/output_boundary.go:57`](../../go/context/output_boundary.go#L57), [`go/context/output_boundary.go:59`](../../go/context/output_boundary.go#L59), [`go/context/output_boundary.go:60`](../../go/context/output_boundary.go#L60), [`go/context/sources.go:2112`](../../go/context/sources.go#L2112), [`go/context/sources.go:2113`](../../go/context/sources.go#L2113), [`go/context/sources.go:2117`](../../go/context/sources.go#L2117), [`go/context/sources.go:2120`](../../go/context/sources.go#L2120) |
| `tool_result` | [`packages/harness/context/managed.ts:199`](../../packages/harness/context/managed.ts#L199), [`packages/harness/engines/codex/index.ts:158`](../../packages/harness/engines/codex/index.ts#L158), [`packages/harness/engines/codex/index.ts:215`](../../packages/harness/engines/codex/index.ts#L215), [`packages/harness/engines/codex/index.ts:259`](../../packages/harness/engines/codex/index.ts#L259), [`packages/harness/engines/deepagents/loop.ts:437`](../../packages/harness/engines/deepagents/loop.ts#L437), [`packages/harness/engines/deepagents/loop.ts:470`](../../packages/harness/engines/deepagents/loop.ts#L470), [`packages/harness/engines/deepagents/loop.ts:474`](../../packages/harness/engines/deepagents/loop.ts#L474), [`packages/harness/engines/deepagents/loop.ts:534`](../../packages/harness/engines/deepagents/loop.ts#L534), [`packages/harness/engines/deepagents/loop.ts:538`](../../packages/harness/engines/deepagents/loop.ts#L538), [`packages/harness/engines/deepagents/loop.ts:553`](../../packages/harness/engines/deepagents/loop.ts#L553), [`packages/harness/engines/deepagents/loop.ts:564`](../../packages/harness/engines/deepagents/loop.ts#L564), [`packages/harness/engines/vercel/loop.ts:1000`](../../packages/harness/engines/vercel/loop.ts#L1000), [`packages/harness/engines/vercel/loop.ts:1005`](../../packages/harness/engines/vercel/loop.ts#L1005), [`packages/harness/engines/vercel/loop.ts:1051`](../../packages/harness/engines/vercel/loop.ts#L1051), [`packages/harness/engines/vercel/loop.ts:849`](../../packages/harness/engines/vercel/loop.ts#L849), [`packages/harness/engines/vercel/loop.ts:915`](../../packages/harness/engines/vercel/loop.ts#L915), [`packages/harness/engines/vercel/loop.ts:920`](../../packages/harness/engines/vercel/loop.ts#L920), [`packages/harness/engines/vercel/loop.ts:996`](../../packages/harness/engines/vercel/loop.ts#L996) | [`go/adapter/agent/invoke.go:899`](../../go/adapter/agent/invoke.go#L899), [`go/adapter/eino/calls.go:114`](../../go/adapter/eino/calls.go#L114), [`go/context/managed.go:206`](../../go/context/managed.go#L206), [`go/gate/session.go:355`](../../go/gate/session.go#L355) | [`packages/harness/context/managed.ts:165`](../../packages/harness/context/managed.ts#L165), [`packages/harness/context/managed.ts:184`](../../packages/harness/context/managed.ts#L184), [`packages/harness/context/managed.ts:205`](../../packages/harness/context/managed.ts#L205), [`packages/harness/context/managed.ts:228`](../../packages/harness/context/managed.ts#L228), [`packages/harness/context/managed.ts:229`](../../packages/harness/context/managed.ts#L229), [`packages/harness/corpus.ts:173`](../../packages/harness/corpus.ts#L173), [`packages/harness/corpus.ts:32`](../../packages/harness/corpus.ts#L32), [`packages/harness/execution/controller.ts:274`](../../packages/harness/execution/controller.ts#L274), [`packages/harness/history/capture.ts:119`](../../packages/harness/history/capture.ts#L119), [`packages/harness/history/capture.ts:77`](../../packages/harness/history/capture.ts#L77), [`packages/harness/toolSources.ts:1295`](../../packages/harness/toolSources.ts#L1295) | [`go/context/managed.go:183`](../../go/context/managed.go#L183), [`go/context/managed.go:208`](../../go/context/managed.go#L208), [`go/context/managed.go:217`](../../go/context/managed.go#L217), [`go/context/managed.go:269`](../../go/context/managed.go#L269), [`go/context/managed.go:270`](../../go/context/managed.go#L270), [`go/context/managed.go:291`](../../go/context/managed.go#L291), [`go/context/managed.go:305`](../../go/context/managed.go#L305), [`go/context/managed.go:310`](../../go/context/managed.go#L310), [`go/context/managed.go:311`](../../go/context/managed.go#L311), [`go/context/output_boundary.go:62`](../../go/context/output_boundary.go#L62), [`go/context/output_boundary.go:63`](../../go/context/output_boundary.go#L63), [`go/context/output_boundary.go:65`](../../go/context/output_boundary.go#L65), [`go/context/sources.go:2121`](../../go/context/sources.go#L2121), [`go/context/sources.go:2122`](../../go/context/sources.go#L2122), [`go/context/sources.go:2128`](../../go/context/sources.go#L2128), [`go/context/sources.go:2129`](../../go/context/sources.go#L2129), [`go/context/sources.go:2130`](../../go/context/sources.go#L2130), [`go/context/sources.go:2131`](../../go/context/sources.go#L2131), [`go/context/sources.go:2133`](../../go/context/sources.go#L2133), [`go/context/sources.go:2137`](../../go/context/sources.go#L2137), [`go/context/sources.go:2148`](../../go/context/sources.go#L2148) |
| `guardrail` | [`packages/harness/context/managed.ts:129`](../../packages/harness/context/managed.ts#L129), [`packages/harness/context/managed.ts:155`](../../packages/harness/context/managed.ts#L155), [`packages/harness/context/managed.ts:197`](../../packages/harness/context/managed.ts#L197), [`packages/harness/context/managed.ts:238`](../../packages/harness/context/managed.ts#L238), [`packages/harness/context/managed.ts:239`](../../packages/harness/context/managed.ts#L239), [`packages/harness/context/managed.ts:252`](../../packages/harness/context/managed.ts#L252), [`packages/harness/context/session.ts:1005`](../../packages/harness/context/session.ts#L1005), [`packages/harness/context/session.ts:275`](../../packages/harness/context/session.ts#L275), [`packages/harness/context/session.ts:298`](../../packages/harness/context/session.ts#L298), [`packages/harness/context/session.ts:454`](../../packages/harness/context/session.ts#L454), [`packages/harness/context/session.ts:506`](../../packages/harness/context/session.ts#L506), [`packages/harness/context/session.ts:798`](../../packages/harness/context/session.ts#L798), [`packages/harness/corpus.ts:64`](../../packages/harness/corpus.ts#L64), [`packages/harness/edits.ts:98`](../../packages/harness/edits.ts#L98), [`packages/harness/engines/contract.ts:727`](../../packages/harness/engines/contract.ts#L727), [`packages/harness/execution/controller.ts:214`](../../packages/harness/execution/controller.ts#L214), [`packages/harness/outputGuard.ts:46`](../../packages/harness/outputGuard.ts#L46), [`packages/harness/policy.ts:53`](../../packages/harness/policy.ts#L53), [`packages/harness/recordReader.ts:16`](../../packages/harness/recordReader.ts#L16), [`packages/harness/session.ts:472`](../../packages/harness/session.ts#L472), [`packages/harness/skills.ts:485`](../../packages/harness/skills.ts#L485), [`packages/harness/toolSources.ts:986`](../../packages/harness/toolSources.ts#L986) | [`go/adapter/agent/invoke.go:328`](../../go/adapter/agent/invoke.go#L328), [`go/adapter/eino/run.go:423`](../../go/adapter/eino/run.go#L423), [`go/adapter/policy.go:151`](../../go/adapter/policy.go#L151), [`go/adapter/policy.go:247`](../../go/adapter/policy.go#L247), [`go/adapter/wire/geminischema.go:329`](../../go/adapter/wire/geminischema.go#L329), [`go/context/controller.go:653`](../../go/context/controller.go#L653), [`go/context/corpus.go:243`](../../go/context/corpus.go#L243), [`go/context/critic.go:337`](../../go/context/critic.go#L337), [`go/context/edits.go:320`](../../go/context/edits.go#L320), [`go/context/live_capture.go:198`](../../go/context/live_capture.go#L198), [`go/context/managed.go:169`](../../go/context/managed.go#L169), [`go/context/managed.go:204`](../../go/context/managed.go#L204), [`go/context/managed.go:356`](../../go/context/managed.go#L356), [`go/context/managed.go:361`](../../go/context/managed.go#L361), [`go/context/managed.go:82`](../../go/context/managed.go#L82), [`go/context/memories.go:327`](../../go/context/memories.go#L327), [`go/context/output_guard.go:178`](../../go/context/output_guard.go#L178), [`go/context/plan_actions.go:512`](../../go/context/plan_actions.go#L512), [`go/context/policy_table.go:60`](../../go/context/policy_table.go#L60), [`go/context/records.go:230`](../../go/context/records.go#L230), [`go/context/runtime_actions.go:76`](../../go/context/runtime_actions.go#L76), [`go/context/session.go:1130`](../../go/context/session.go#L1130), [`go/context/session.go:1761`](../../go/context/session.go#L1761), [`go/context/session.go:2075`](../../go/context/session.go#L2075), [`go/context/skills.go:279`](../../go/context/skills.go#L279), [`go/context/skills.go:646`](../../go/context/skills.go#L646), [`go/context/sources.go:1061`](../../go/context/sources.go#L1061), [`go/context/sources.go:1165`](../../go/context/sources.go#L1165), [`go/context/sources.go:1291`](../../go/context/sources.go#L1291), [`go/context/subagents.go:41`](../../go/context/subagents.go#L41), [`go/contract/records.go:72`](../../go/contract/records.go#L72), [`go/gate/gate.go:125`](../../go/gate/gate.go#L125) | [`packages/harness/context/managed.ts:252`](../../packages/harness/context/managed.ts#L252), [`packages/harness/context/managed.ts:36`](../../packages/harness/context/managed.ts#L36), [`packages/harness/corpus.ts:28`](../../packages/harness/corpus.ts#L28), [`packages/harness/delegation.ts:723`](../../packages/harness/delegation.ts#L723), [`packages/harness/delegation.ts:731`](../../packages/harness/delegation.ts#L731), [`packages/harness/delegation.ts:733`](../../packages/harness/delegation.ts#L733) | [`go/adapter/delegate/batch.go:330`](../../go/adapter/delegate/batch.go#L330), [`go/adapter/delegate/batch.go:343`](../../go/adapter/delegate/batch.go#L343), [`go/adapter/delegate/batch.go:356`](../../go/adapter/delegate/batch.go#L356), [`go/context/output_boundary.go:125`](../../go/context/output_boundary.go#L125), [`go/context/output_boundary.go:131`](../../go/context/output_boundary.go#L131), [`go/context/session.go:1550`](../../go/context/session.go#L1550) |
| `thinking_unavailable` | [`packages/harness/thinking.ts:574`](../../packages/harness/thinking.ts#L574) | [`go/adapter/wire/thinking.go:470`](../../go/adapter/wire/thinking.go#L470) | [`packages/harness/thinking.ts:431`](../../packages/harness/thinking.ts#L431) | — |
| `critic_decided` | [`packages/harness/criticSwitch.ts:263`](../../packages/harness/criticSwitch.ts#L263) | [`go/context/critic.go:381`](../../go/context/critic.go#L381) | [`packages/harness/context/managed.ts:235`](../../packages/harness/context/managed.ts#L235) | [`go/context/managed.go:326`](../../go/context/managed.go#L326) |
| `critique` | [`packages/harness/refutation.ts:728`](../../packages/harness/refutation.ts#L728) | [`go/adapter/turn/refutation.go:975`](../../go/adapter/turn/refutation.go#L975) | [`packages/harness/context/managed.ts:220`](../../packages/harness/context/managed.ts#L220), [`packages/harness/corpus.ts:31`](../../packages/harness/corpus.ts#L31) | [`go/context/managed.go:251`](../../go/context/managed.go#L251), [`go/context/managed.go:252`](../../go/context/managed.go#L252), [`go/context/managed.go:261`](../../go/context/managed.go#L261) |
| `proposal` | [`packages/harness/engines/codex/index.ts:287`](../../packages/harness/engines/codex/index.ts#L287), [`packages/harness/engines/codex/index.ts:291`](../../packages/harness/engines/codex/index.ts#L291), [`packages/harness/engines/codex/index.ts:395`](../../packages/harness/engines/codex/index.ts#L395), [`packages/harness/engines/deepagents/loop.ts:1053`](../../packages/harness/engines/deepagents/loop.ts#L1053), [`packages/harness/engines/deepagents/loop.ts:877`](../../packages/harness/engines/deepagents/loop.ts#L877), [`packages/harness/engines/deepagents/loop.ts:880`](../../packages/harness/engines/deepagents/loop.ts#L880), [`packages/harness/engines/vercel/loop.ts:1428`](../../packages/harness/engines/vercel/loop.ts#L1428), [`packages/harness/engines/vercel/loop.ts:1431`](../../packages/harness/engines/vercel/loop.ts#L1431), [`packages/harness/engines/vercel/loop.ts:1696`](../../packages/harness/engines/vercel/loop.ts#L1696) | [`go/adapter/agent/agent.go:623`](../../go/adapter/agent/agent.go#L623), [`go/adapter/eino/loop.go:345`](../../go/adapter/eino/loop.go#L345) | [`packages/harness/context/managed.ts:168`](../../packages/harness/context/managed.ts#L168), [`packages/harness/context/managed.ts:238`](../../packages/harness/context/managed.ts#L238), [`packages/harness/context/managed.ts:239`](../../packages/harness/context/managed.ts#L239), [`packages/harness/corpus.ts:34`](../../packages/harness/corpus.ts#L34), [`packages/harness/delegation.ts:730`](../../packages/harness/delegation.ts#L730) | [`go/adapter/agent/agent.go:370`](../../go/adapter/agent/agent.go#L370), [`go/adapter/agent/agent.go:469`](../../go/adapter/agent/agent.go#L469), [`go/adapter/agent/agent.go:470`](../../go/adapter/agent/agent.go#L470), [`go/adapter/delegate/batch.go:354`](../../go/adapter/delegate/batch.go#L354), [`go/adapter/delegate/batch.go:355`](../../go/adapter/delegate/batch.go#L355), [`go/adapter/eino/critic.go:70`](../../go/adapter/eino/critic.go#L70), [`go/adapter/eino/critic.go:71`](../../go/adapter/eino/critic.go#L71), [`go/adapter/eino/loop.go:308`](../../go/adapter/eino/loop.go#L308), [`go/context/managed.go:353`](../../go/context/managed.go#L353), [`go/context/managed.go:365`](../../go/context/managed.go#L365), [`go/context/managed.go:382`](../../go/context/managed.go#L382), [`go/context/output_boundary.go:83`](../../go/context/output_boundary.go#L83), [`go/context/output_boundary.go:84`](../../go/context/output_boundary.go#L84), [`go/context/output_boundary.go:88`](../../go/context/output_boundary.go#L88), [`go/context/output_boundary.go:90`](../../go/context/output_boundary.go#L90) |
| `error` | [`packages/harness/failures.ts:61`](../../packages/harness/failures.ts#L61) | [`go/failure/failure.go:356`](../../go/failure/failure.go#L356) | [`packages/harness/context/managed.ts:184`](../../packages/harness/context/managed.ts#L184), [`packages/harness/context/managed.ts:209`](../../packages/harness/context/managed.ts#L209), [`packages/harness/context/managed.ts:221`](../../packages/harness/context/managed.ts#L221), [`packages/harness/context/managed.ts:223`](../../packages/harness/context/managed.ts#L223), [`packages/harness/corpus.ts:33`](../../packages/harness/corpus.ts#L33), [`packages/harness/delegation.ts:1037`](../../packages/harness/delegation.ts#L1037), [`packages/harness/delegation.ts:727`](../../packages/harness/delegation.ts#L727) | [`go/adapter/delegate/batch.go:345`](../../go/adapter/delegate/batch.go#L345), [`go/adapter/delegate/batch.go:346`](../../go/adapter/delegate/batch.go#L346), [`go/adapter/delegate/batch.go:347`](../../go/adapter/delegate/batch.go#L347), [`go/adapter/delegate/batch.go:348`](../../go/adapter/delegate/batch.go#L348), [`go/adapter/delegate/coordinator.go:541`](../../go/adapter/delegate/coordinator.go#L541), [`go/adapter/eino/observation.go:61`](../../go/adapter/eino/observation.go#L61), [`go/adapter/eino/observation.go:62`](../../go/adapter/eino/observation.go#L62), [`go/context/managed.go:183`](../../go/context/managed.go#L183), [`go/context/managed.go:230`](../../go/context/managed.go#L230), [`go/context/managed.go:263`](../../go/context/managed.go#L263), [`go/context/managed.go:273`](../../go/context/managed.go#L273), [`go/context/managed.go:274`](../../go/context/managed.go#L274), [`go/context/managed.go:277`](../../go/context/managed.go#L277) |
| `end` | [`packages/harness/context/managed.ts:216`](../../packages/harness/context/managed.ts#L216), [`packages/harness/context/managed.ts:228`](../../packages/harness/context/managed.ts#L228), [`packages/harness/context/managed.ts:247`](../../packages/harness/context/managed.ts#L247), [`packages/harness/context/managed.ts:258`](../../packages/harness/context/managed.ts#L258), [`packages/harness/context/managed.ts:269`](../../packages/harness/context/managed.ts#L269), [`packages/harness/context/managed.ts:64`](../../packages/harness/context/managed.ts#L64), [`packages/harness/delegation.ts:1062`](../../packages/harness/delegation.ts#L1062), [`packages/harness/delegation.ts:345`](../../packages/harness/delegation.ts#L345), [`packages/harness/engines/codex/index.ts:401`](../../packages/harness/engines/codex/index.ts#L401), [`packages/harness/engines/deepagents/loop.ts:1113`](../../packages/harness/engines/deepagents/loop.ts#L1113), [`packages/harness/engines/vercel/loop.ts:1826`](../../packages/harness/engines/vercel/loop.ts#L1826), [`packages/harness/storage/writer.ts:462`](../../packages/harness/storage/writer.ts#L462) | [`go/adapter/agent/agent.go:190`](../../go/adapter/agent/agent.go#L190), [`go/adapter/delegate/agent.go:520`](../../go/adapter/delegate/agent.go#L520), [`go/adapter/delegate/coordinator.go:516`](../../go/adapter/delegate/coordinator.go#L516), [`go/adapter/delegate/coordinator.go:525`](../../go/adapter/delegate/coordinator.go#L525), [`go/adapter/eino/run.go:215`](../../go/adapter/eino/run.go#L215), [`go/context/managed.go:176`](../../go/context/managed.go#L176), [`go/context/managed.go:181`](../../go/context/managed.go#L181), [`go/context/managed.go:245`](../../go/context/managed.go#L245), [`go/context/managed.go:259`](../../go/context/managed.go#L259), [`go/context/managed.go:301`](../../go/context/managed.go#L301), [`go/context/managed.go:367`](../../go/context/managed.go#L367), [`go/context/managed.go:371`](../../go/context/managed.go#L371), [`go/context/managed.go:376`](../../go/context/managed.go#L376), [`go/context/managed.go:380`](../../go/context/managed.go#L380), [`go/context/managed.go:400`](../../go/context/managed.go#L400), [`go/context/managed.go:402`](../../go/context/managed.go#L402), [`go/context/managed.go:413`](../../go/context/managed.go#L413), [`go/context/managed.go:416`](../../go/context/managed.go#L416), [`go/context/managed.go:420`](../../go/context/managed.go#L420), [`go/context/managed.go:428`](../../go/context/managed.go#L428), [`go/context/managed.go:432`](../../go/context/managed.go#L432), [`go/context/managed.go:434`](../../go/context/managed.go#L434), [`go/context/run_writer.go:465`](../../go/context/run_writer.go#L465) | [`packages/harness/context/managed.ts:209`](../../packages/harness/context/managed.ts#L209), [`packages/harness/context/managed.ts:236`](../../packages/harness/context/managed.ts#L236), [`packages/harness/context/managed.ts:237`](../../packages/harness/context/managed.ts#L237), [`packages/harness/context/managed.ts:240`](../../packages/harness/context/managed.ts#L240), [`packages/harness/context/managed.ts:61`](../../packages/harness/context/managed.ts#L61), [`packages/harness/corpus.ts:162`](../../packages/harness/corpus.ts#L162), [`packages/harness/delegation.ts:1036`](../../packages/harness/delegation.ts#L1036) | [`go/adapter/delegate/coordinator.go:532`](../../go/adapter/delegate/coordinator.go#L532), [`go/context/managed.go:230`](../../go/context/managed.go#L230), [`go/context/managed.go:334`](../../go/context/managed.go#L334), [`go/context/managed.go:344`](../../go/context/managed.go#L344), [`go/context/managed.go:384`](../../go/context/managed.go#L384), [`go/context/managed.go:388`](../../go/context/managed.go#L388) |
| `policy_resolved` | [`packages/harness/context/managed.ts:124`](../../packages/harness/context/managed.ts#L124) | [`go/context/managed.go:79`](../../go/context/managed.go#L79) | [`packages/harness/corpus.ts:163`](../../packages/harness/corpus.ts#L163) | — |
| `memory_proposed` | [`packages/harness/memories.ts:152`](../../packages/harness/memories.ts#L152) | [`go/context/memories.go:354`](../../go/context/memories.go#L354) | — | — |
| `memory_recalled` | [`packages/harness/context/session.ts:316`](../../packages/harness/context/session.ts#L316) | [`go/context/memories.go:310`](../../go/context/memories.go#L310) | — | — |
| `edit_applied` | [`packages/harness/edits.ts:147`](../../packages/harness/edits.ts#L147) | [`go/context/edits.go:500`](../../go/context/edits.go#L500) | [`packages/harness/context/managed.ts:35`](../../packages/harness/context/managed.ts#L35) | [`go/context/output_boundary.go:120`](../../go/context/output_boundary.go#L120), [`go/context/output_boundary.go:123`](../../go/context/output_boundary.go#L123), [`go/context/output_boundary.go:133`](../../go/context/output_boundary.go#L133) |
| `edit_refused` | [`packages/harness/edits.ts:37`](../../packages/harness/edits.ts#L37) | [`go/context/edits.go:320`](../../go/context/edits.go#L320) | [`packages/harness/context/managed.ts:34`](../../packages/harness/context/managed.ts#L34) | [`go/context/output_boundary.go:108`](../../go/context/output_boundary.go#L108), [`go/context/output_boundary.go:118`](../../go/context/output_boundary.go#L118), [`go/context/output_boundary.go:133`](../../go/context/output_boundary.go#L133) |
| `output_guarded` | [`packages/harness/outputGuard.ts:41`](../../packages/harness/outputGuard.ts#L41) | [`go/context/output_guard.go:167`](../../go/context/output_guard.go#L167) | [`packages/harness/delegation.ts:722`](../../packages/harness/delegation.ts#L722) | [`go/adapter/delegate/batch.go:322`](../../go/adapter/delegate/batch.go#L322), [`go/adapter/delegate/batch.go:327`](../../go/adapter/delegate/batch.go#L327) |
| `export_written` | [`packages/harness/records.ts:122`](../../packages/harness/records.ts#L122) | [`go/context/records.go:286`](../../go/context/records.go#L286) | — | — |
| `steered` | [`packages/harness/execution/controller.ts:231`](../../packages/harness/execution/controller.ts#L231) | [`go/context/controller.go:693`](../../go/context/controller.go#L693) | — | — |
| `connection_switched` | [`packages/harness/context/session.ts:1122`](../../packages/harness/context/session.ts#L1122), [`packages/harness/context/session.ts:380`](../../packages/harness/context/session.ts#L380), [`packages/harness/engines/deepagents/loop.ts:604`](../../packages/harness/engines/deepagents/loop.ts#L604), [`packages/harness/engines/vercel/loop.ts:1094`](../../packages/harness/engines/vercel/loop.ts#L1094) | [`go/adapter/agent/invoke.go:299`](../../go/adapter/agent/invoke.go#L299), [`go/adapter/eino/run.go:440`](../../go/adapter/eino/run.go#L440) | — | — |
| `usage` | [`packages/harness/context/managed.ts:215`](../../packages/harness/context/managed.ts#L215), [`packages/harness/context/managed.ts:228`](../../packages/harness/context/managed.ts#L228), [`packages/harness/context/managed.ts:240`](../../packages/harness/context/managed.ts#L240), [`packages/harness/context/managed.ts:246`](../../packages/harness/context/managed.ts#L246), [`packages/harness/context/managed.ts:257`](../../packages/harness/context/managed.ts#L257), [`packages/harness/context/managed.ts:268`](../../packages/harness/context/managed.ts#L268) | [`go/context/managed.go:144`](../../go/context/managed.go#L144), [`go/context/managed.go:242`](../../go/context/managed.go#L242), [`go/context/managed.go:299`](../../go/context/managed.go#L299), [`go/context/managed.go:409`](../../go/context/managed.go#L409) | [`packages/harness/delegation.ts:720`](../../packages/harness/delegation.ts#L720) | [`go/adapter/delegate/batch.go:305`](../../go/adapter/delegate/batch.go#L305), [`go/adapter/delegate/batch.go:311`](../../go/adapter/delegate/batch.go#L311), [`go/context/managed.go:173`](../../go/context/managed.go#L173) |
| `recorded` | [`packages/harness/corpus.ts:62`](../../packages/harness/corpus.ts#L62) | [`go/context/corpus.go:236`](../../go/context/corpus.go#L236) | — | — |
| `plan_revised` | [`packages/harness/execution/controller.ts:361`](../../packages/harness/execution/controller.ts#L361), [`packages/harness/execution/controller.ts:402`](../../packages/harness/execution/controller.ts#L402) | [`go/context/controller.go:1363`](../../go/context/controller.go#L1363), [`go/context/plan_actions.go:432`](../../go/context/plan_actions.go#L432) | [`packages/harness/history/query.ts:22`](../../packages/harness/history/query.ts#L22), [`packages/harness/storage/writer.ts:529`](../../packages/harness/storage/writer.ts#L529) | [`go/context/run_writer.go:1977`](../../go/context/run_writer.go#L1977), [`go/context/run_writer.go:1978`](../../go/context/run_writer.go#L1978), [`go/context/run_writer.go:1995`](../../go/context/run_writer.go#L1995), [`go/context/run_writer.go:1996`](../../go/context/run_writer.go#L1996) |
| `plan_departed` | [`packages/harness/execution/controller.ts:387`](../../packages/harness/execution/controller.ts#L387) | [`go/context/plan_actions.go:365`](../../go/context/plan_actions.go#L365) | [`packages/harness/history/query.ts:23`](../../packages/harness/history/query.ts#L23), [`packages/harness/storage/writer.ts:529`](../../packages/harness/storage/writer.ts#L529) | [`go/context/run_writer.go:1980`](../../go/context/run_writer.go#L1980), [`go/context/run_writer.go:1981`](../../go/context/run_writer.go#L1981), [`go/context/run_writer.go:1998`](../../go/context/run_writer.go#L1998), [`go/context/run_writer.go:1999`](../../go/context/run_writer.go#L1999) |
| `plan_completion_refused` | [`packages/harness/execution/controller.ts:397`](../../packages/harness/execution/controller.ts#L397) | [`go/context/plan_actions.go:418`](../../go/context/plan_actions.go#L418) | [`packages/harness/history/query.ts:24`](../../packages/harness/history/query.ts#L24), [`packages/harness/storage/writer.ts:529`](../../packages/harness/storage/writer.ts#L529) | [`go/context/run_writer.go:1983`](../../go/context/run_writer.go#L1983), [`go/context/run_writer.go:1984`](../../go/context/run_writer.go#L1984), [`go/context/run_writer.go:2001`](../../go/context/run_writer.go#L2001), [`go/context/run_writer.go:2002`](../../go/context/run_writer.go#L2002) |
| `plan_drift_decided` | [`packages/harness/execution/controller.ts:104`](../../packages/harness/execution/controller.ts#L104) | [`go/context/plan_validation.go:274`](../../go/context/plan_validation.go#L274) | [`packages/harness/context/session.ts:1025`](../../packages/harness/context/session.ts#L1025), [`packages/harness/recordReader.ts:102`](../../packages/harness/recordReader.ts#L102), [`packages/harness/storage/nativePlanPause.ts:78`](../../packages/harness/storage/nativePlanPause.ts#L78), [`packages/harness/storage/writer.ts:528`](../../packages/harness/storage/writer.ts#L528), [`packages/harness/storage/writer.ts:529`](../../packages/harness/storage/writer.ts#L529), [`packages/harness/storage/writer.ts:655`](../../packages/harness/storage/writer.ts#L655) | [`go/context/native_plan_pause.go:188`](../../go/context/native_plan_pause.go#L188), [`go/context/native_plan_pause.go:237`](../../go/context/native_plan_pause.go#L237), [`go/context/native_plan_pause.go:464`](../../go/context/native_plan_pause.go#L464), [`go/context/native_plan_pause.go:473`](../../go/context/native_plan_pause.go#L473), [`go/context/run_writer.go:1986`](../../go/context/run_writer.go#L1986), [`go/context/run_writer.go:1987`](../../go/context/run_writer.go#L1987), [`go/context/run_writer.go:2004`](../../go/context/run_writer.go#L2004), [`go/context/run_writer.go:2005`](../../go/context/run_writer.go#L2005), [`go/contract/records.go:583`](../../go/contract/records.go#L583) |
| `route_decided` | [`packages/harness/storage/turnRouteOwner.ts:71`](../../packages/harness/storage/turnRouteOwner.ts#L71) | [`go/context/turn_route_owner.go:230`](../../go/context/turn_route_owner.go#L230) | [`packages/harness/recordReader.ts:102`](../../packages/harness/recordReader.ts#L102), [`packages/harness/storage/turnRouteOwner.ts:71`](../../packages/harness/storage/turnRouteOwner.ts#L71) | [`go/context/run_writer.go:1971`](../../go/context/run_writer.go#L1971), [`go/context/run_writer.go:1972`](../../go/context/run_writer.go#L1972), [`go/context/run_writer.go:942`](../../go/context/run_writer.go#L942), [`go/context/run_writer.go:943`](../../go/context/run_writer.go#L943), [`go/context/turn_route_owner.go:230`](../../go/context/turn_route_owner.go#L230), [`go/contract/records.go:583`](../../go/contract/records.go#L583) |
| `route_widened` | [`packages/harness/storage/turnRouteOwner.ts:71`](../../packages/harness/storage/turnRouteOwner.ts#L71) | [`go/context/turn_route_owner.go:232`](../../go/context/turn_route_owner.go#L232) | [`packages/harness/recordReader.ts:102`](../../packages/harness/recordReader.ts#L102), [`packages/harness/storage/turnRouteOwner.ts:71`](../../packages/harness/storage/turnRouteOwner.ts#L71), [`packages/harness/storage/writer.ts:600`](../../packages/harness/storage/writer.ts#L600), [`packages/harness/storage/writer.ts:617`](../../packages/harness/storage/writer.ts#L617) | [`go/context/run_writer.go:1974`](../../go/context/run_writer.go#L1974), [`go/context/run_writer.go:1975`](../../go/context/run_writer.go#L1975), [`go/context/run_writer.go:909`](../../go/context/run_writer.go#L909), [`go/context/run_writer.go:910`](../../go/context/run_writer.go#L910), [`go/context/run_writer.go:915`](../../go/context/run_writer.go#L915), [`go/context/run_writer.go:928`](../../go/context/run_writer.go#L928), [`go/context/run_writer.go:945`](../../go/context/run_writer.go#L945), [`go/context/run_writer.go:946`](../../go/context/run_writer.go#L946), [`go/context/turn_route_owner.go:232`](../../go/context/turn_route_owner.go#L232), [`go/context/turn_route_owner.go:246`](../../go/context/turn_route_owner.go#L246), [`go/context/turn_route_owner.go:247`](../../go/context/turn_route_owner.go#L247), [`go/context/turn_route_owner.go:251`](../../go/context/turn_route_owner.go#L251), [`go/context/turn_route_owner.go:753`](../../go/context/turn_route_owner.go#L753), [`go/contract/records.go:583`](../../go/contract/records.go#L583) |
| `repeat_detected` | [`packages/harness/repeatTools.ts:36`](../../packages/harness/repeatTools.ts#L36) | [`go/context/repeat_tools.go:99`](../../go/context/repeat_tools.go#L99) | [`packages/harness/recordReader.ts:102`](../../packages/harness/recordReader.ts#L102) | [`go/context/run_writer.go:1968`](../../go/context/run_writer.go#L1968), [`go/context/run_writer.go:1969`](../../go/context/run_writer.go#L1969), [`go/contract/records.go:583`](../../go/contract/records.go#L583) |

## Every kind with its members

### `execution_checkpoint`

The execution's state, for the host to save: at the start of a tracked run, after each tool call, tool result and question, and at the end. durable true asks the host to save before the next operation (anything but a read); absent or false means the save may be lazy. Emitted by the shared context boundary (context/managed.ts) and by the delegation coordinator, only where the session carries a current request or a saved execution.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `execution_checkpoint` |  |
| `durable` |  | boolean |  |
| `checkpoint` | yes | `checkpoint.json` |  |
| `version` |  | enum `1`, `2` | Feature-bearing checkpoint3 selects event2; default producers remain event1. |

### `budget_paused`

Work paused at the request allowance: the allowance ran out, or the model asked for more with request_budget_increase. The checkpoint's status is paused; the run ends with end. Only the person's grant (a new checkpoint grant) continues it. An adapter's error with code request-limit is converted to this by the shared context boundary. On the vercel engine it is also how the turn's wall clock ends a turn (budget.json, turnMilliseconds): what is in flight is stopped, completed work is kept, and the checkpoint's reason says the turn reached its limit. Continuing it takes a request grant like any pause, so a run already at the 100-request cap cannot be continued, which the reason also says. A clock that passes while the critic runs leaves the proposal delivered with a note instead, and the run ends.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `budget_paused` |  |
| `checkpoint` | yes | `checkpoint.json` |  |
| `version` |  | enum `1`, `2` | Feature-bearing checkpoint3 selects event2; default producers remain event1. |

### `approval_requested`

An act call paused for the person (ADR-0008): the model called a tool with a source whose effect class is act and whose approval is required, and the host's approval policy answered ask. The shared context boundary emits this in place of the call's tool_call, never runs the call, writes the call into the checkpoint's approval (state asked) with status paused, emits the checkpoint durable, and ends the run with end and no error: a model engine once the step's other calls are answered (its next request is never sent), an agent engine at the call (the invocation is stopped, as at a delegation handoff). The host's stream carries it, unlike delegation_requested: the person has to see the call. The host continues the paused checkpoint with the person's decision (checkpoint.json#/$defs/approvalDecision). callId is the call's id, drawn by the boundary where the run's events carry none; name the wire name the model called; tool the tool's own name at its source; source its record; args the arguments the model sent. Closed.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `approval_requested` |  |
| `callId` | yes | string (min length 1, max length 256) |  |
| `name` |  | `tool-offer.json $defs/wireName` |  |
| `tool` |  | string (min length 1, max length 128) |  |
| `source` |  | `tool-offer.json $defs/sourceRecord` |  |
| `args` |  | object |  |
| `version` |  | enum `1`, `2` | Event wire format version. Absent only on historical records; current writers emit 1. |
| `purpose` |  | enum `tool-call`, `plan-drift` |  |
| `plan` |  | object |  |
| `plan.execution` | yes | string (min length 1, max length 256) |  |
| `plan.revision` | yes | string (pattern `^sha256:[a-f0-9]{64}$`) |  |
| `plan.candidateRevision` | yes | string (pattern `^sha256:[a-f0-9]{64}$`) |  |
| `plan.explanation` | yes | string (max length 2000) |  |
| `plan.departures` | yes | array of `checkpoint.json $defs/planDeparture` (max items 40) |  |
| `runtime` |  | `checkpoint.json $defs/runtimeApprovalBinding` |  |

Conditions beyond the members (allOf) are in the schema file.

### `steered`

ADR-0021: one steer was placed at a request boundary. On an agent capability atRequest is the invocation number.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `steered` |  |
| `bytes` | yes | integer (min 1) |  |
| `atRequest` | yes | integer (min 1) |  |
| `version` |  | const `1` | Event wire format version. Absent only on historical records; current writers emit 1. |

### `compacted`

ADR-0016: the visible marker that one context compaction replaced complete turns. digest is the digest of the framed replacement; mode says whether the deterministic index or a model-authored summary was used.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `compacted` |  |
| `turns` | yes | integer (min 1) |  |
| `bytes` | yes | integer (min 1) |  |
| `digest` | yes | `provenance.json $defs/sha256` |  |
| `mode` | yes | enum `index`, `summary` |  |
| `version` |  | const `1` | Event wire format version. Absent only on historical records; current writers emit 1. |

### `skill_loaded`

A skill loaded (ADR-0009): the model's load_skill call loaded it, between that call and its result. skill names it (tool-offer.json#/$defs/loadedSkill: id, name, source record, digest; never its instructions); bytes is what it counts against skillBytes (skill.json#/$defs/bytes). The checkpoint's skills carries it from here.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `skill_loaded` |  |
| `skill` | yes | `tool-offer.json $defs/loadedSkill` |  |
| `bytes` | yes | `skill.json $defs/bytes` |  |
| `version` |  | const `1` | Event wire format version. Absent only on historical records; current writers emit 1. |

### `skill_unloaded`

A skill unloaded (ADR-0009): the model's unload_skill call dropped it, between that call and its result; id is the skill's id. Its block leaves every later request.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `skill_unloaded` |  |
| `id` | yes | string (pattern `^[A-Za-z][A-Za-z0-9-]{0,23}$`) |  |
| `version` |  | const `1` | Event wire format version. Absent only on historical records; current writers emit 1. |

### `retry`

The adapter is sending again within this turn, attempt n of at most two. The codex engine relays the Codex bridge's recovery notices. The vercel engine emits it before it sends again a model request the provider refused (packages/harness/engines/vercel/retry.ts, retryingCall): only a failure whose outcome is refused and that failures.ts names (canRetryRefusal: the rate-limited class, a 429 or a rate-limit alias, and a 4xx carrying the overload code overloaded_error), never an unknown outcome (a 5xx, 408, 504 or a transport failure) and never a response that answered: a stream that breaks after its first byte is reported, not sent again; at most two retries, each after the longer of the backoff (1 s, then 2 s) and the provider's Retry-After, and none where Retry-After asks for more than 30 s, when the refusal is reported at once with its retryAt, nor where the retry would take the allowance's last request, which is the turn's tool-free final answer. Each attempt is a model request: counted against the allowance and recorded in the checkpoint's completions (provenance.json: attempt, failure, waitMs).

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `retry` |  |
| `attempt` | yes | integer (min 1, max 2) |  |
| `version` |  | const `1` | Event wire format version. Absent only on historical records; current writers emit 1. |

### `response_recovery`

The adapter is spending one tool-free request to finish a response cut short, to stop repeated work, or to ask once, within the allowance, for a proposal that could not be read, in the shape the contract reads (the reformat request). Only the vercel engine emits it.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `response_recovery` |  |
| `version` |  | const `1` | Event wire format version. Absent only on historical records; current writers emit 1. |

### `delegation_requested`

The model called delegate_tasks on an engine whose run cannot wait for a tool call (codex: the provider account runs one agent run at a time, and the children need it). The adapter emits this instead of a tool_call and ends its run; the host's coordinator runs the batch and starts the response's next run with the call's result (delegated-task.json#/$defs/answer). An engine that awaits the call (vercel, agent#14) emits a tool_call and a tool_result for delegate_tasks instead, in the same run. The coordinator consumes this event: a host reading the coordinator's stream does not see it.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `delegation_requested` |  |
| `request` | yes | `delegated-task.json $defs/request` |  |
| `callId` |  | string (min length 1, max length 256) | The agent wire's id of the delegate_tasks call (ADR-0007), where the engine has one: the Go agent engine sends it, and the coordinator keeps it in the checkpoint's handoff. The TypeScript codex engine sends none. |
| `subagent` |  | `subagent.json $defs/record` |  |
| `version` |  | const `1` | Event wire format version. Absent only on historical records; current writers emit 1. |

### `delegated_task`

A child task's state, whole, at each transition. Emitted by the host's delegation coordinator; on an engine that awaits the delegate call, between that call's tool_call and its tool_result.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `delegated_task` |  |
| `task` | yes | `delegated-task.json` |  |
| `provider` |  | `provenance.json $defs/provider` |  |
| `subagent` |  | `subagent.json $defs/record` |  |
| `version` |  | const `1` | Event wire format version. Absent only on historical records; current writers emit 1. |

### `user_question`

The model called ask_user. The adapter emits this instead of a tool_call, ends the turn and waits; the answer comes with the next run. id is fresh per question.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `user_question` |  |
| `id` | yes | string |  |
| `question` | yes | `question.json` |  |
| `version` |  | const `1` | Event wire format version. Absent only on historical records; current writers emit 1. |

### `reasoning`

The model's reasoning, as the provider streams it: deltas with done false, then the whole passage once with done true. Shown to the person, never sent to the runtime. Only the vercel engine emits it.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `reasoning` |  |
| `text` | yes | string |  |
| `done` | yes | boolean |  |
| `version` |  | const `1` | Event wire format version. Absent only on historical records; current writers emit 1. |

### `message`

The model's own prose to the person, outside the proposal fence, as it wrote it. Never written by the host; absent where the model wrote nothing but the fence. Where the run's proposal could not be read, the whole final answer, prose and raw fence as written, is one message before the error: nothing the model finished is lost. On the vercel engine that is the answer before its reformat request; where the reformat request comes back as prose with no proposal, which a conversation allows, that prose is a second message, and the first answer's failure follows.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `message` |  |
| `text` | yes | string |  |
| `version` |  | const `1` | Event wire format version. Absent only on historical records; current writers emit 1. |

### `message_progress`

The prose so far of the answer being written, whole each time (not a delta), with any proposal fence hidden; an empty text clears it. Only on an interactive session. taskId (agent#16): a delegated child's prose so far, under its task, forwarded by the delegation coordinator as the child writes it, between that task's delegated_task events; an empty text with its taskId clears it when the child ends. An event without taskId is the run's own answer: a host routes one with taskId to the task's view, never to the answer.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `message_progress` |  |
| `text` | yes | string |  |
| `taskId` |  | `delegated-task.json $defs/uuid` | The delegated task (delegated-task.json id) whose child is writing this prose. |
| `version` |  | const `1` | Event wire format version. Absent only on historical records; current writers emit 1. |

### `tool_call`

The tool_call event: the model asked for one tool, with these arguments, and the adapter is about to hand the call to the host (callTool for a runtime tool, execute for a host tool). Emitted before the call is made, so the host can journal it; its arguments are the model's own, before the host's gate rewrites anything. A host tool with an interaction (ask_user, delegate_tasks) gets no tool_call: it gets user_question or delegation_requested instead. An act call of a tool with a source whose approval policy answers ask gets approval_requested instead (ADR-0008). Read from the AssistantEvent union (web/src/assistant/engine.ts). The versioned record reader refuses unknown structural members.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `tool_call` |  |
| `callId` |  | string | Pairs the call with its tool_result. The codex engine always sets it (the server's 48-hex call id); the vercel engine sets it on an interactive session, always on an act call of a tool with a source (ADR-0008: its approval decision is tied to the call by it), and always on its refusal of a repeated call or of a call made on the final, tool-free request; never on a call to a tool the model was not offered. |
| `name` | yes | string | The tool's declared name, or the output-guarded display name for an undeclared model name (ADR-0017); dispatch still uses the original name. The codex engine offers its tools on the agent wire under aliases (desk_runtime_N, desk_host_N) and reports the real name here. A tool with a source is reported by its wire name, its own name in source.tool (ADR-0006). |
| `args` | yes | any | The arguments exactly as the model wrote them. Usually an object; a call to a tool the model was not offered carries whatever the model sent, or {}. |
| `presentation` |  | object | Host-owned presentation frozen when the tool was offered. Added by a host consumer (the research run) from the offered host tool, never by an engine and never from model arguments. |
| `presentation.provider` | yes | string |  |
| `origin` |  | const `harness` | harness: the harness made this call itself, not the model: the critic pass's own run of a declared case nothing compared yet (agent#10 review row 8; refutation.ts, compareTheRest). Absent on every call a model made. |
| `source` |  | `tool-offer.json $defs/sourceRecord` | The tool's source record (ADR-0006), on the call of a tool with a source, with tool, its own name there; name is then the wire name the model called. Absent on the Runtime's tools and the host's own. |
| `version` |  | const `1` | Event wire format version. Absent only on historical records; current writers emit 1. |

### `tool_result`

The tool_result event: what a tool answered, as the adapter reports it to the host after a tool_call. The model is shown the same answer, bounded (see $defs/reference). Read from the AssistantEvent union (web/src/assistant/engine.ts) as both engines emit it (engines/vercel/loop.ts, engines/codex/index.ts). The versioned record reader refuses unknown structural members. A failed tool is a result with isError true and the failure in structured, never an error event (toolFailureResult). The bound: a result whose JSON text, after an exact JSON text block is folded into structuredContent, is at most toolResultBytes (budget.json, 12000 by default) reaches the model whole. A larger one is retained in the execution's context ledger and the model is shown a reference envelope instead, which it reads back with the host tool the envelope names (readWith: read_window, by line or page in windows of readWindowBytes), or in sections with read_context (at most readBytes, 6000 by default, per read). A reading tool holds its window to the result limit less the envelope (readWindowBytes: toolResultBytes less 1024 bytes, counted in the window's JSON-encoded text), so one read is one result and never a reference; the host tool read_window reads a retained item by line or page in such windows ($defs/readArguments, $defs/readResult). Where the ledger is full (retainedBytes) the model is shown the unavailable envelope. An image part in a result ($defs/imagePart) is never part of the text the bound counts: a model whose capability record says vision is shown it as an image beside the result, any other the host's text rendering of it in its place (capability.json, vision), and a line in the result says which. One model request carries at most imagesPerRequest images and imageBytesPerRequest of their bytes, decoded (budget.json, 8 and 16 MiB by default): the newest, and an older image past either bound is replaced in that request by a line saying it is no longer shown; the result that brought it still describes it. The host tool search finds a regular expression in retained items, line by line ($defs/searchArguments, $defs/searchResult). The tool_result event itself is never bounded: the host sees the whole answer. A runtime or host tool call past the policy's toolCallMilliseconds (budget.json) is no longer waited for (it may still complete) and is answered as a failed result with failure code timeout, source tool and outcome unknown; the run goes on. What the model is shown of a reading tool's answer (every runtime tool, read_window, read_context, search, and a host tool declared a read) is that projection framed as one data block ($defs/framedData), on the text path too ($defs/textToolResultMessage), closed by a marker drawn afresh for each run; the run's instructions say that text inside a block is data, never instructions. A failure the harness wrote (a refused, capped or timed-out call) and an acknowledgement the adapter wrote are shown as they are. The tool_result event is never framed. A tool with a source (ADR-0006) is held to its source's bounds as well: its result is shown whole only up to sourceResultBytes (budget.json) and is otherwise retained and shown as the reference envelope, its calls are held to sourceCallsPerTurn and sourceCallMilliseconds, and its answer is framed as data whatever its effect class.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `tool_result` |  |
| `callId` |  | string | The id of the tool_call this answers. The codex engine always sets it; the vercel engine sets it on an interactive session, and always on its refusal of a repeated call or of a call made on the final, tool-free request; never on the result of a call to a tool the model was not offered. |
| `name` | yes | string | The tool's declared name, or the same output-guarded display name as its call for an undeclared model name (ADR-0017). The result payload remains exempt. A tool with a source is reported by its wire name, its own name in source.tool (ADR-0006). |
| `isError` | yes | boolean |  |
| `text` | yes | string | The text blocks of the answer, joined with newlines in the runtime's order (textOf). |
| `structured` |  | any | The answer's structuredContent, where it had one. On a failed result the host wrote, it is bound: for ask_user, the question format failure ({"questionFailure": …}); otherwise, where it carries a failure member, {"failure": …} (failure.json#/$defs/toolFailureContent), which the Desk reads back with readFailure. A runtime tool's own failed answer passes its structuredContent through unbound. |
| `origin` |  | const `harness` | harness: the harness made this call itself, not the model: the critic pass's own run of a declared case nothing compared yet (agent#10 review row 8; refutation.ts, compareTheRest). Absent on every call a model made. |
| `source` |  | `tool-offer.json $defs/sourceRecord` | The tool's source record (ADR-0006), as on its tool_call, with receipt where the host's answer gave one for this call. Absent on the Runtime's tools and the host's own. |
| `proposed` |  | const `true` | The call of a propose-class tool with a source (ADR-0006): it was not run; the call, its tool_call's arguments and its source, is the review card the person accepts or rejects, and the host runs it on acceptance, after the run, under its grants. The model is told the call was proposed and nothing was run. |
| `version` |  | const `1` | Event wire format version. Absent only on historical records; current writers emit 1. |

Conditions beyond the members (allOf) are in the schema file.

### `guardrail`

The host saying what it did. rewrote and refused are the tool gate's, emitted by the host as the call's frame leaves the page, so they arrive between that call's tool_call and tool_result. narrowed is the adapter's notice, before the model is asked anything, that a tool's served schema lost a keyword on this family's wire. Also refused, with tool proposal, where the host withholds a proposal (Plan mode, or a conversation without artifact proposals). reason (ADR-0008), on a refusal of an act call of a tool with a source: approval refused, the host's approval policy answered refuse; approval pending, the call came while another act call of the run waits for the person's decision (at most one approval is pending); approval stale, the gate's own refusal of a call whose decision no longer holds for it (its arguments not those its approval was decided for, a call that reaches its run without an allow, or, on the continued run, its tool no longer offered as the person decided). None of these calls is run. skill withheld (ADR-0009), on a skill the package did not catalogue or restore, tool its name (or, for the catalogue's bounds, its source's id): its tools[] names a tool the host did not declare, its bytes pass skillBytes, its bundle does not hash to the digest the person approved, the catalogue's bounds left it out, or a skill the checkpoint had loaded is no longer declared as it was; skill budget, on a load_skill call refused past skillsPerRun, between that call and its failed result. Nothing of a withheld skill reaches the model. review refused (ADR-0010), on a request_review call refused: a second ask of the run, an ask under off or on, or one outside the author's phase; nothing is recorded and no review is asked.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `guardrail` |  |
| `tool` | yes | string |  |
| `action` | yes | enum `rewrote`, `refused`, `narrowed` |  |
| `detail` | yes | string |  |
| `reason` |  | enum `approval refused`, `approval pending`, `approval stale`, `skill withheld`, `skill budget`, `review refused`, `policy refused`, `pace warning`, `subagent withheld`, `subagent refused`, `memory refused`, `edit refused`, `compaction refused`, `output refused`, `records expired`, `identity changed`, `approval downgraded`, `steer refused`, `failover refused`, `record refused`, `record version unsupported`, `record member unsupported`, `plan update refused` |  |
| `version` |  | const `1` | Event wire format version. Absent only on historical records; current writers emit 1. |

### `thinking_unavailable`

The reasoning tier the host asked for could not be expressed on this endpoint, said once. Only the vercel engine emits it; the sentence is the host's thinking table's.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `thinking_unavailable` |  |
| `detail` | yes | string |  |
| `version` |  | const `1` | Event wire format version. Absent only on historical records; current writers emit 1. |

### `critic_decided`

The critic's decision for the run (ADR-0010; provenance.json#/$defs/criticDecision): emitted once per run, after the proposal exists and before the critic would start (and before its critique), or where the critic is skipped, a run with no proposal included, before the run's last message. mode, ran and reason always; ran means the switch allowed the critic pass (the pass may still report that it could not run, with zero checks); reasoning, the critic's tier, where the run states one; trigger under auto. The checkpoint's critic carries the same members, written durably after it.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `critic_decided` |  |
| `mode` | yes | `tool-offer.json $defs/criticMode` |  |
| `ran` | yes | boolean |  |
| `reason` | yes | `provenance.json $defs/criticReason` |  |
| `reasoning` |  | `tool-offer.json $defs/criticReasoning` |  |
| `trigger` |  | enum `model-asked`, `cases`, `changes-existing` |  |
| `version` |  | const `1` | Event wire format version. Absent only on historical records; current writers emit 1. |

### `critique`

The critic pass's report, before the proposal it reviewed: refuted is decided from the runtime's own results and never from the critic's prose, true where a check came back as something other than the runtime's settled word (validate valid, experimental_evaluate evaluated) or, since agent#10, where a compared case's outcome is not the declared one, or a declared case was not compared at all (review row 1: an uncompared case is unsettled, so a critique that did not reach every declared case is never not refuted); checks lists each runtime check it ran with the runtime's status; text is the host's quote of them and of the comparisons. cases and incomplete are present where the host declared cases (session criticCases, $defs/criticCase): each comparison of a declared outcome with the runtime's ($defs/caseComparison), and the ids of the declared cases nothing compared, the harness's own calls for the uncompared included. figures: what the critic cost (provenance.json $defs/criticFigures).

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `critique` |  |
| `schema` |  | const (see schema) |  |
| `version` |  | const `1` |  |
| `digest` |  | `provenance.json $defs/recordDigest` |  |
| `retainUntil` |  | `provenance.json $defs/retainUntil` |  |
| `refuted` | yes | boolean |  |
| `checks` | yes | array of object |  |
| `checks[].tool` | yes | string |  |
| `checks[].status` | yes | string |  |
| `text` | yes | string |  |
| `cases` |  | array of `$defs/caseComparison` |  |
| `incomplete` |  | array of string | The declared cases nothing compared, by id. Any makes refuted true and is named on the proposal's critique. |
| `figures` |  | `provenance.json $defs/criticFigures` |  |
| `provider` |  | `provenance.json $defs/provider` |  |
| `records` |  | `provenance.json $defs/recordFigures` |  |

### `proposal`

The one document the run proposes: from exactly one fenced JSON block shaped {"proposal": {"kind": "create", "document": …, "unknowns": […]}} and never from the prose around it, or from the model's call of the host tool propose_pack (tool-offer.json, $defs/proposePack), which requires documentJson containing the document JSON text (legacy document refused), the fence then being the fallback. Source numeric values must survive canonical JSON serialization unchanged; decimal/exponent spelling and negative zero may normalize. The guarded accepted proposal event remains authoritative. A tool proposal beside a fenced one in the same answer is ambiguous and refused, as two fenced proposals are. critique is present only where a critic ran and reached at least one check or compared at least one declared case; note where something that ran after the proposal existed did not finish. Accept and Reject are the host's actions, not the adapter's.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `proposal` |  |
| `document` | yes | object | A JSON object. |
| `unknowns` | yes | array of string |  |
| `digest` | yes | string (pattern `^sha256:[0-9a-f]{64}$`) | ADR-0022: sha256: and the SHA-256 of the canonical JSON of this proposal with digest omitted. |
| `critique` |  | object |  |
| `critique.refuted` | yes | boolean |  |
| `critique.incomplete` |  | array of string (min items 1) | The declared cases the critic's pass did not compare, by id (review row 1): present where any is, with refuted true, so the proposal's critique is never a plain not-refuted while a declared case is uncompared. |
| `note` |  | object | Something that ran after the proposal existed did not finish, and the proposal, being the finished work, is delivered all the same, with no critique and no error event ahead of it (refutation.ts, proposalNote). kind critic-unfinished: the critic started and did not finish (its provider error, its length cutoff, the allowance or the turn's wall clock). kind turn-unfinished: the proposal was made through propose_pack and a later request of the turn failed (the closing summary refused or cut off, the allowance, the turn's wall clock, or on the codex engine the run's own failure), so nothing after it ran, the critic included, and the run ends with the proposal. message is host-authored: one sentence saying what did not run, then the catalogue's sentence for the failure, or, for the allowance, a sentence saying it was used up (nothing pauses). failure is the failure that stopped it, classified, with source model on the vercel engine and agent on the codex engine, the turn's wall clock included. |
| `note.kind` | yes | enum `critic-unfinished`, `turn-unfinished` |  |
| `note.message` | yes | string |  |
| `note.failure` | yes | `failure.json` |  |
| `note.figures` |  | `provenance.json $defs/criticFigures` | On critic-unfinished: what the critic cost before it stopped (agent#10). |
| `edits` |  | `provenance.json $defs/edits` |  |
| `version` |  | const `1` | Event wire format version. Absent only on historical records; current writers emit 1. |

### `error`

The run failed. message is the catalogue's sentence for the class, or the failure's host-authored detail; failure is the classified failure. A failed tool is not an error event (tool_result with isError), and a cancelled run emits none.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `error` |  |
| `message` | yes | string |  |
| `failure` |  | `failure.json` |  |
| `version` |  | const `1` | Event wire format version. Absent only on historical records; current writers emit 1. |

### `end`

The run is over. Exactly once, last.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `end` |  |
| `version` |  | const `1` | Event wire format version. Absent only on historical records; current writers emit 1. |

### `policy_resolved`

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `policy_resolved` |  |
| `members` | yes | `policy.json $defs/resolved` |  |
| `version` |  | const `1` | Event wire format version. Absent only on historical records; current writers emit 1. |

### `memory_proposed`

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `memory_proposed` |  |
| `memory` | yes | `memory.json` |  |
| `version` |  | const `1` | Event wire format version. Absent only on historical records; current writers emit 1. |

### `memory_recalled`

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `memory_recalled` |  |
| `ids` | yes | array of string |  |
| `digests` | yes | array of string (pattern `^sha256:[0-9a-f]{64}$`) |  |
| `bytes` | yes | integer (min 0) |  |
| `version` |  | const `1` | Event wire format version. Absent only on historical records; current writers emit 1. |

### `edit_applied`

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `edit_applied` |  |
| `document` | yes | string (min length 1, max length 512) |  |
| `line` | yes | integer (min 1) |  |
| `bytes` | yes | integer (min 0) |  |
| `valid` | yes | const `true` |  |
| `version` |  | const `1` | Event wire format version. Absent only on historical records; current writers emit 1. |

### `edit_refused`

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `edit_refused` |  |
| `document` |  | string (min length 1, max length 512) | Offending document identifier, truncated to 512 Unicode characters. Omitted when missing, non-string or empty; reason names the invalid document identifier. |
| `reason` | yes | string (min length 1) |  |
| `version` |  | const `1` | Event wire format version. Absent only on historical records; current writers emit 1. |

### `output_guarded`

ADR-0017: outbound text held by a named pattern, without the matched text.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `output_guarded` |  |
| `where` | yes | enum `message`, `proposal`, `tool argument`, `memory proposal`, `draft edit` |  |
| `pattern` | yes | string (min length 1, max length 64, pattern `^[a-z][a-z0-9-]{0,63}$`) |  |
| `action` | yes | enum `redact`, `refuse` |  |
| `version` |  | const `1` | Event wire format version. Absent only on historical records; current writers emit 1. |

### `export_written`

The host asked the package to produce a JSON export from records it supplied. No package store was read.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `export_written` |  |
| `records` | yes | integer (min 0) |  |
| `bytes` | yes | integer (min 1) |  |
| `digest` | yes | `provenance.json $defs/recordDigest` |  |
| `version` |  | const `1` | Event wire format version. Absent only on historical records; current writers emit 1. |

### `connection_switched`

ADR-0020. A qualifying provider failure moved this request once to the next same-family fallback, after the ordinary retry rule. The stable prefix is rebuilt for the new connection.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `connection_switched` |  |
| `role` | yes | string (min length 1, max length 128) |  |
| `from` | yes | string (min length 1, max length 128) |  |
| `to` | yes | string (min length 1, max length 128) |  |
| `reason` | yes | enum `unavailable`, `rate-limited`, `timeout` |  |
| `version` |  | const `1` | Event wire format version. Absent only on historical records; current writers emit 1. |

### `usage`

ADR-0020. Emitted exactly once for every run, immediately before end.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `usage` |  |
| `usage` | yes | `usage.json` |  |
| `version` |  | const `1` | Event wire format version. Absent only on historical records; current writers emit 1. |

### `recorded`

ADR-0022: a redacted evaluation corpus entry was written. id names its corpus directory and bytes is the sum of the UTF-8 bytes in its six JSON files.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `recorded` |  |
| `id` | yes | string (pattern `^[a-z0-9][a-z0-9-]{0,63}$`) |  |
| `bytes` | yes | integer (min 1) |  |
| `version` |  | const `1` | Event wire format version. Absent only on historical records; current writers emit 1. |

### `plan_revised`

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `plan_revised` |  |
| `execution` | yes | string (min length 1, max length 100) |  |
| `sequence` | yes | integer (min 1, max 9007199254740991) |  |
| `previous` | yes | string (pattern `^sha256:[a-f0-9]{64}$`) |  |
| `revision` | yes | string (pattern `^sha256:[a-f0-9]{64}$`) |  |
| `explanation` | yes | string (max length 2000) |  |
| `diff` | yes | array of `checkpoint.json $defs/planChange` (max items 40) |  |
| `version` |  | enum `1`, `2` |  |

### `plan_departed`

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `plan_departed` |  |
| `version` |  | enum `1`, `2` |  |
| `execution` | yes | string (min length 1, max length 256) |  |
| `revision` | yes | string (pattern `^sha256:[a-f0-9]{64}$`) |  |
| `acceptedRevision` | yes | string (pattern `^sha256:[a-f0-9]{64}$`) |  |
| `explanation` | yes | string (max length 2000) |  |
| `departures` | yes | array of `checkpoint.json $defs/planDeparture` (max items 40) |  |

### `plan_completion_refused`

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `plan_completion_refused` |  |
| `version` |  | enum `1`, `2` |  |
| `execution` | yes | string (min length 1, max length 256) |  |
| `revision` | yes | string (pattern `^sha256:[a-f0-9]{64}$`) |  |
| `reason` | yes | enum `invalid-input`, `active-step-required`, `revision-changed`, `evidence-unavailable`, `missing-evidence`, `unknown-operation`, `unsuccessful-operation`, `unobserved-revision`, `wrong-scope` |  |
| `step` |  | string (min length 1, max length 100) |  |

### `plan_drift_decided`

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `plan_drift_decided` |  |
| `version` |  | enum `1`, `2` |  |
| `execution` | yes | string (min length 1, max length 256) |  |
| `callId` | yes | string (min length 1, max length 256) |  |
| `revision` | yes | string (pattern `^sha256:[a-f0-9]{64}$`) |  |
| `candidateRevision` | yes | string (pattern `^sha256:[a-f0-9]{64}$`) |  |
| `approved` | yes | boolean |  |
| `note` |  | string (max length 2000) |  |
| `nativeInvocation` |  | `checkpoint.json $defs/nativePlanInvocation` |  |

### `route_decided`

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `route_decided` |  |
| `version` |  | const `2` |  |
| `route` | yes | `turn-route.json` |  |

### `route_widened`

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `route_widened` |  |
| `version` |  | const `2` |  |
| `route` | yes | `turn-route.json` |  |

### `repeat_detected`

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `repeat_detected` |  |
| `version` |  | const `2` |  |
| `execution` | yes | string (min length 1, max length 100) |  |
| `dispatchId` | yes | string (min length 1, max length 100) |  |
| `keyDigest` | yes | string (pattern `^sha256:[a-f0-9]{64}$`) |  |
| `count` | yes | integer (min 1, max 399) |  |
| `reminder` | yes | boolean |  |
| `stopped` | yes | boolean |  |
