# The critic

## Sources

Schemas:

- `contract/schemas/budget.json`
- `contract/schemas/tool-offer.json`
- `contract/schemas/run-event.json`
- `contract/schemas/provenance.json`

Package files (`packages/harness`), with the symbols read:

- `refutation.ts`: `CRITIC_TOOLS`, `CRITIC_READERS`, `MAX_CRITIC_TURNS`, `CRITIC_REHEARSALS`, `SETTLED_STATUS`, `REFUTATION_MARKER`
- `engines/codex/index.ts`: `CRITIC_PASS_MILLISECONDS`
- `context/policy.ts`: `TEST_DEFAULTS`, `CONTEXT_DEFAULTS`
- `criticSwitch.ts`: `CRITIC_MODES`, `CRITIC_REASONS`, `CRITIC_TRIGGERS`, `MODEL_TIERS`, `AGENT_TIERS`, `REQUEST_REVIEW`

> generated; edit the sources, then run `node scripts/gen-reference.mjs`. `--check` fails where this page differs from them.

The critic reviews a proposal before it is delivered. This page lists its switch, the tools it may call, its bounds and the records it produces.

See also: [tools.md](tools.md), [events.md](events.md), [budget.md](budget.md), [reasons.md](reasons.md).

## The switch

The modes: `off`, `auto`, `on`. The tiers: `off`, `low`, `on`, `medium`, `ultra`, `high` on the model capability, `none`, `minimal`, `low`, `medium`, `high`, `xhigh`, `max` on the agent capability. The run's word, then the project's, then the context budget's default decides the mode (ADR-0010).

| Where | Member | Type | Default | Description |
| --- | --- | --- | --- | --- |
| session (`tool-offer.json`) | `critic` | `$defs/criticMode` |  | The run's word on the critic (ADR-0010; the person's, from the host's per-run control): off, auto or on. It wins over the project's word (criticProject) and the policy table's (budget.json criticDefault). Absent, the run states none and the alias adversarialReview is read where it is set. |
| session (`tool-offer.json`) | `criticProject` | `$defs/criticMode` |  | The project's word on the critic (ADR-0010), as the host passes it from the project's settings: off, auto or on. Read where the run states none; absent, the policy table's default (budget.json criticDefault) decides. |
| session (`tool-offer.json`) | `criticReasoning` | `$defs/criticReasoning` |  | The critic's reasoning tier (ADR-0010), in the enum of the run's capability: a thinking tier (off, on, ultra) on the model capability, an effort (none to max) on the agent capability; a value of the other capability's enum is refused before any request (configuration). Absent, the critic runs at the author's tier (budget.json criticReasoningFloor, author). A value lower than the author's is the host's explicit choice: it is honoured and recorded in critic_decided and the checkpoint's critic. |
| session (`tool-offer.json`) | `existingPack` | boolean |  | The host's fact that the run works on a pack that already exists rather than drafting a new one (ADR-0010): under auto, a proposal of such a run changes an existing pack, one of the three triggers. Absent is false. |
| session (`tool-offer.json`) | `reviewerAssigned` | boolean |  | The host's fact that a human reviewer is assigned to the run's work (ADR-0010): the critic does not run in any mode, with the reason reviewer-assigned. Absent is false. |
| session (`tool-offer.json`) | `adversarialReview` | boolean |  | The switch before ADR-0010, kept for one release as a read alias of the run's word: true reads as on, false as off. Where critic is also set, critic wins. Retired in the release after. |
| context budget (`budget.json`) | `criticDefault` | `tool-offer.json $defs/criticMode` | `"auto"` | The critic's mode where neither the run (session critic) nor the project (session criticProject) states one (ADR-0010; tool-offer.json#/$defs/criticMode): auto, so a proposal with declared cases, a proposal that changes an existing pack, or one the model asked a review of is reviewed, and a conversation turn is not. The harness adds it to the Desk's table (contract/desk/policy.json). |
| context budget (`budget.json`) | `criticReasoningFloor` | enum `author` | `"author"` | The critic's reasoning tier where the host names none (ADR-0010): author, the author's own tier on the run's capability, so a critic is never run at a lower tier than the author unless the host says so explicitly (session criticReasoning). The one value this release defines. The harness adds it to the Desk's table. |
| context budget (`budget.json`) | `adversarialReview` | boolean |  | The critic's default before ADR-0010, kept for one release as a read alias of criticDefault: true reads as on, false as off. Where the table states criticDefault too, criticDefault wins. The harness's table (contract/desk/policy.json) no longer carries it. |

### The modes

The critic's switch (ADR-0010): off, the critic never runs; on, it runs on every proposal; auto, it runs on a proposal where the package's own facts say so (declared cases exist, the proposal changes an existing pack, or the model asked through request_review). The mode is the run's word, else the project's, else the policy table's (run > project > table).

Type: enum `off`, `auto`, `on`

### The tiers

A reasoning tier in the enum of a run's capability (ADR-0010): the thinking tiers off, on and ultra of the model capability (ThinkingTier), or the efforts none, minimal, low, medium, high, xhigh and max of the agent capability (CodexEffort), each enum in its own order, lowest first.

Type: enum `off`, `on`, `ultra`, `none`, `minimal`, `low`, `medium`, `high`, `xhigh`, `max`

## What the critic may call

### The critic’s allow-list

The critic's tools (agent#10; packages/harness/refutation.ts, CRITIC_TOOLS and CRITIC_READERS): an allow-list, and the whole of what the critic may call. Four of the runtime's reads about the document: validate, experimental_evaluate (only as a rehearsal, the gate's rewrite) and get_schema, and experimental_test_cases under its own bounds (tool-call.json $defs/testCasesArguments, and budget.json testCallsPerTurn, which the critic shares with the loop). Then two of the harness's own context readers, read_window and search (review row 6): the context ledger's tools, whose names a host tool may not take ($defs/reservedName), which reach no Runtime tool and no action; with them the critic reads its own first message where that message was retained to fit its request. The critic is offered these, of the tools the session offers, in the session's order. Any other name it calls (the runtime's other reads, every host tool, propose_* and request_* among them, and the other context tools) is refused by the shared boundary while the critic runs (context/session.ts, admit on the wrapped callTool and host tools), before the tool gate and before a host tool runs: the call is not made, the model reads a permission failure, and a guardrail refused event names the tool. Where the run catalogues skills (ADR-0009), the critic is also offered load_skill and unload_skill: a skill grants no tool, so a load leaves this list as it is.

| Group | Names |
| --- | --- |
| Runtime reads (`CRITIC_TOOLS`) | `validate`, `experimental_evaluate`, `get_schema`, `experimental_test_cases` |
| Harness context readers (`CRITIC_READERS`) | `read_window`, `search` |

## Bounds

| Bound | Value | Source comment |
| --- | --- | --- |
| `MAX_CRITIC_TURNS` | 4 | How many turns a critic gets. |
| `CRITIC_REHEARSALS` | 8 | How many of the declared cases the critic's own calls left uncompared the harness rehearses itself, one call each, after the critic's turns (agent#10). The rest stay `incomplete`. Bounded so that a large saved matrix cannot spend the turn's shared tool calls on one pass. |
| `CRITIC_PASS_MILLISECONDS` (the codex engine) | 600000 | How long one critic pass may take on this engine (agent#10 review row 8): the Codex bridge's own deadline for an agent run, ten minutes, held from the critic run's start over the harness's own calls after it. A harness call is not made once it has passed, and its case stays incomplete; one in flight is bounded by what is left of it. |
| `testCallsPerTurn` (shared with the loop) | 3 | The cap on `experimental_test_cases` calls in one turn; see [budget.md](budget.md). |

## The Runtime’s settled words

A check counts only when the Runtime answers with its settled word for the tool.

| Tool | Settled status |
| --- | --- |
| `validate` | `valid` |
| `experimental_evaluate` | `evaluated` |

## Reports and records

### The `critique` event

The critic pass's report, before the proposal it reviewed: refuted is decided from the runtime's own results and never from the critic's prose, true where a check came back as something other than the runtime's settled word (validate valid, experimental_evaluate evaluated) or, since agent#10, where a compared case's outcome is not the declared one, or a declared case was not compared at all (review row 1: an uncompared case is unsettled, so a critique that did not reach every declared case is never not refuted); checks lists each runtime check it ran with the runtime's status; text is the host's quote of them and of the comparisons. cases and incomplete are present where the host declared cases (session criticCases, $defs/criticCase): each comparison of a declared outcome with the runtime's ($defs/caseComparison), and the ids of the declared cases nothing compared, the harness's own calls for the uncompared included. figures: what the critic cost (provenance.json $defs/criticFigures).

Open: a member the schema does not name is accepted.

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

### A declared case

One case whose outcome the host declares for the critic (agent#10; the session's criticCases; packages/harness/refutation.ts, CriticCase, readCriticCases): one of the pack's saved tests (source test) or a case the request declares (source declared), as a row of the runtime's matrix (matrixVersion 3) with its source. The critic is shown the cases stating a whole §8.3 disposition (kind, reasons and handoff) or an error class as that matrix, without source, so experimental_test_cases can run them as they are, and the cases stating part of a disposition apart, for rehearsal only (review row 2). Closed: the reader refuses another member, a repeated id, a case with no facts object, and a case that does not expect exactly one of a disposition (an object with a kind) and an error class; a session carries at most 200, and one the reader refuses ends the run before any request (configuration).

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `source` | yes | enum `declared`, `test` |  |
| `id` | yes | string (min length 1, max length 200) |  |
| `facts` | yes | object |  |
| `evidenceAvailability` |  | object |  |
| `supportedExtensions` |  | array of string |  |
| `expectedDisposition` |  | object | The §8.3 disposition the case expects. Each member it states must equal the outcome's: a matrix row states the whole disposition, a case a request declares may state kind and outcomeId alone. |
| `expectedDisposition.kind` | yes | string |  |
| `expectedDisposition.outcomeId` |  | string |  |
| `expectedDisposition.reasons` |  | array of any |  |
| `expectedDisposition.handoff` |  | object |  |
| `expectedErrorClass` |  | string (min length 1) |  |
| `expectedErrorPhase` |  | string |  |
| `expectedHandoffTarget` |  | any |  |
| `origin` |  | string |  |
| `focus` |  | string |  |
| `specSection` |  | string |  |
| `cites` |  | any |  |

### An outcome

An outcome, as a comparison reports it: a §8.3 disposition as the runtime gave it, or a §8.4 evaluation error by its class and, where known, its phase.

Type: one of: object / object

### A comparison

One comparison of a declared case's outcome with the runtime's, the critic's structured note (agent#10; packages/harness/refutation.ts, CaseComparison). tool experimental_evaluate: a rehearsal the critic ran over the proposed document (its pack argument, read as JSON, is the proposal's document; a pack_id never is) with the case's facts, evidence (an omitted document and an empty one both leave every requirement unknown) and supported extensions; agrees where each member the declared disposition states equals the rehearsal's, the §8.3 sets reasons and handoff.triggeredBy compared in any order (review row 3), or the error class (and phase, where stated) is the declared one. tool experimental_test_cases: a row of a test run over the proposed document whose matrix carried the case exactly as declared, for a case stating the whole §8.3 disposition (kind, reasons and handoff) or an error class (review row 2: the runtime compares a disposition byte for byte, so a case stating part of one is compared by rehearsal only); the runtime compared the row, and agrees is its status passed. The call may be the critic's own or one the harness makes after the critic's turns for a declared case nothing compared yet (refutation.ts, compareTheRest: one experimental_test_cases call over those stating a whole disposition or an error class where the critic is offered it, then one rehearsal each of the rest, at most CRITIC_REHEARSALS, 8, where it is offered experimental_evaluate); both go through the same gate and appear as tool_call and tool_result events, the harness's marked origin harness. expected is the declared outcome; actual what the runtime gave, absent where it reported none.

Open: a member the schema does not name is accepted.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `case` | yes | string | The declared case's id. |
| `source` | yes | enum `declared`, `test` |  |
| `tool` | yes | enum `experimental_evaluate`, `experimental_test_cases` |  |
| `agrees` | yes | boolean |  |
| `expected` | yes | `$defs/caseOutcome` |  |
| `actual` |  | `$defs/caseOutcome` |  |

### What one critic pass cost

What one critic pass cost (agent#10; packages/harness/refutation.ts, CriticFigures), carried on the critique event and on a critic-unfinished note (run-event.json). model: the model the critic ran on, the host's critic model for the connection (the critic member of modelsFor, asked once per run) or the run's. unit model-requests (the vercel engine): requests counts the model requests the critic claimed against the allowance, refused attempts included, and the token members sum what its completion rows (phase review) reported, each absent where no row reported it. unit agent-runs (the codex engine): requests counts the critic's agent runs, and the bridge reports no tokens. harnessCalls counts the harness's own runtime calls in the pass.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `model` | yes | string (min length 1, max length 128) |  |
| `unit` | yes | enum `model-requests`, `agent-runs` |  |
| `requests` | yes | integer (min 0) |  |
| `inputTokens` |  | `$defs/tokens` |  |
| `outputTokens` |  | `$defs/tokens` |  |
| `reasoningTokens` |  | `$defs/tokens` |  |
| `cacheReadTokens` |  | `$defs/tokens` |  |
| `cacheWriteTokens` |  | `$defs/tokens` |  |
| `harnessCalls` |  | integer (min 1) | The runtime calls the harness made itself in the pass for declared cases the critic left uncompared (review row 8), each marked origin harness on its events; absent where it made none. Not model requests. |

### The marker

The critic's request is marked by the constant `REFUTATION_MARKER` in `refutation.ts` (named here, not its text).

## Triggers

Under `auto`, in the order read: `model-asked`, `cases`, `changes-existing`.

## The decision

### The decision, the report's row

The critic's decision for one run, the report's row (ADR-0010): the mode (run > project > table), whether the critic ran, the reason, the tier the critic ran or would run at, and under auto the trigger. reason: a forced-off reason (child, phase, purpose, reviewer-assigned, in that order) whatever the mode; else off under off; else no-proposal where the run proposed nothing; else under on the layer that set it; else under auto the trigger, in the order model-asked, cases, changes-existing, or, with none, the layer that set auto and ran false; a critic that would run but has no room left is not run, with budget. reasoning: the critic's tier, the host's criticReasoning, else the author's (criticReasoningFloor); absent where the run states no tier (an agent capability with no effort). trigger: under auto, the trigger that held, kept on a budget skip; absent otherwise. Emitted once per run as critic_decided and written in the checkpoint's critic.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `mode` | yes | `tool-offer.json $defs/criticMode` |  |
| `ran` | yes | boolean |  |
| `reason` | yes | `$defs/criticReason` |  |
| `reasoning` |  | `tool-offer.json $defs/criticReasoning` |  |
| `trigger` |  | enum `model-asked`, `cases`, `changes-existing` |  |

### The reasons

Why the critic ran or did not (ADR-0010), one enum. The layer that set the mode: person (the run's word), project, table. The trigger under auto: cases (declared cases exist), changes-existing (the proposal changes an existing pack), model-asked (the model called request_review). Not run: no-proposal (nothing to refute), off (the mode is off), budget (the request allowance, or on the agent capability the invocations, left none for the critic). Forced off whatever the mode: reviewer-assigned, purpose (a graph-purpose run), phase (Plan mode), child (a delegated child).

Type: enum `person`, `project`, `table`, `model-asked`, `cases`, `changes-existing`, `no-proposal`, `reviewer-assigned`, `purpose`, `phase`, `child`, `budget`, `off`

### The `critic_decided` event

The critic's decision for the run (ADR-0010; provenance.json#/$defs/criticDecision): emitted once per run, after the proposal exists and before the critic would start (and before its critique), or where the critic is skipped, a run with no proposal included, before the run's last message. mode, ran and reason always; ran means the switch allowed the critic pass (the pass may still report that it could not run, with zero checks); reasoning, the critic's tier, where the run states one; trigger under auto. The checkpoint's critic carries the same members, written durably after it.

Open: a member the schema does not name is accepted.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | const `critic_decided` |  |
| `mode` | yes | `tool-offer.json $defs/criticMode` |  |
| `ran` | yes | boolean |  |
| `reason` | yes | `provenance.json $defs/criticReason` |  |
| `reasoning` |  | `tool-offer.json $defs/criticReasoning` |  |
| `trigger` |  | enum `model-asked`, `cases`, `changes-existing` |  |

### The tool `request_review`

The harness tool by which the model asks for a review (ADR-0010): a reserved name, offered only where the critic's mode is auto, in the author's phase, on a run the critic is not forced off for; never offered to the critic, to a delegated child, in Plan mode or to the reformat run. It takes one optional reason. The first call of a run is recorded as the trigger model-asked (the checkpoint's critic, carried through a resume) and answered with a note; the package then runs the critic on the run's proposal. A second call of the run, a call under off or on, and a call outside the author's phase are refused (tool-failed, not-started) with a guardrail whose reason is review refused, and nothing is recorded. The model's ask is a trigger, never a verdict: it cannot turn a review off or decide what the critic finds. Its effect is plan, so its answer is not framed as data.

Type: all of: `$defs/hostTool` / any
