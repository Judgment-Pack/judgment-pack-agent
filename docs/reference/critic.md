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

> generated; edit the sources, then run `node scripts/gen-reference.mjs`. `--check` fails where this page differs from them.

The critic reviews a proposal before it is delivered. This page lists its switch, the tools it may call, its bounds and the records it produces.

See also: [tools.md](tools.md), [events.md](events.md), [budget.md](budget.md), [reasons.md](reasons.md).

## The switch

This page documents what is on `main`. The switch today is the boolean `adversarialReview`: a session sets it, and where a session leaves it unset the context budget’s member of the same name is read. ADR-0010 will replace the boolean; this page is then regenerated from what replaces it.

| Where | Member | Type | Default | Description |
| --- | --- | --- | --- | --- |
| context budget (`budget.json`) | `adversarialReview` | boolean | `false` | Whether the critic runs where a session leaves adversarialReview unset, on both engines (packages/harness/refutation.ts, criticRuns): false, so a critic runs only where the host asks for one, at any reasoning tier. The harness adds it to the Desk's table (contract/desk/policy.json). The host chooses per run by passing adversarialReview on the session, from its own settings; this member is the default the harness reads where a session leaves it unset. |

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
| `refuted` | yes | boolean |  |
| `checks` | yes | array of object |  |
| `checks[].tool` | yes | string |  |
| `checks[].status` | yes | string |  |
| `text` | yes | string |  |
| `cases` |  | array of `$defs/caseComparison` |  |
| `incomplete` |  | array of string | The declared cases nothing compared, by id. Any makes refuted true and is named on the proposal's critique. |
| `figures` |  | `provenance.json $defs/criticFigures` |  |

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

Listed here once ADR-0010 lands.
