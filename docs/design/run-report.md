# Canonical recorded run report

Agent #157 adds one presentation contract for facts already recorded in a terminal execution checkpoint. TypeScript and Go project the same hand-written vectors into identical canonical UTF-8 JSON. The projection reads no clock, provider, store or model and dispatches no work.

## Sources and omissions

The report has format `version: 1` and records the input format separately as `checkpointVersion`. It accepts validated checkpoint versions 1 and 2 with status `complete` or `stopped`. Running and paused checkpoints are outside this terminal report contract.

The projector copies these checkpoint members whole when present:

`id`, `phase`, `status`, `unit`, `used`, `limit`, `requestCap`, `grants`, `reason`, `explanation`, `prompts`, `sources`, `sourceIdentities`, `skills`, `critic`, `delegation`, `usage`, `childUsage`, `failover`, `completions`, `plan`, `planState`.

Prompt entries retain request order. Source rows retain figures, source skill-load counts and reported identities. Skill loads retain load order, digest, bytes and recorded load time. The critic retains its recorded switch, trigger, decision and reason. Delegation retains child rows, batch figures and parent figures; the usage ledger and child usage remain distinct recorded ledgers. Failover retains each recorded connection and history. The projector does not recompute totals or infer a missing model, token count, cost or timing. An absent optional member stays absent; a present empty array or ledger stays present; zero stays zero. Arrays are never sorted.

Plan and planState use the existing checkpoint types whole, including stable step identities, origins, evidence references, revision history, accepted plan provenance, departures, pending drift state, recorded drift decisions and refusal history. A report never decides that a step has evidence or is complete.

These checkpoint members are intentionally excluded: `schema`, `digest`, `retainUntil`, `records`, `requestKey`, `operations`, `context`, `testCalls`, `durable`, `handoff`, `approval`, `outputGuard`, `memories`, `draft`, `policy`, `crossProvider`, `compaction`, `steers`. They carry checkpoint envelope metadata, internal operation/request/context payloads, recovery or admission state, or unrelated retained state. The report does not embed a checkpoint or expose raw event payloads. Source-inventory tests partition every checkpoint property into carried, excluded, or the separately renamed checkpoint version; a newly introduced property requires an explicit decision.

## Supplied event pages

`buildRunReport` / `BuildRunReport` adds corroboration using the actual RunHead, RunEventPage and stored-envelope contracts. The caller reads one host-bound root resource from the beginning (`null` in TypeScript, empty cursor in Go), then requests each successive page using the returned cursor. The API performs no store I/O.

Validation requires nonempty bounded pages, consistent hasMore flags, unique valid opaque cursors, and a nextCursor equal to each page's last cursor. Every envelope is decoded by the existing stored-event reader; checkpoint events also pass the semantic checkpoint reader and their execution id matches their scope. Root envelopes name the supplied execution. Child envelopes follow the writer's execution-equals-task convention; no membership is inferred from potentially pruned child histories. The final retained root checkpoint must canonically equal the supplied terminal checkpoint. The head's run id and canonical checkpoint must match it, and the last supplied cursor must equal the head cursor. Formatting and object member order do not change checkpoint equality. Cursor text is never compared lexically.

RunEventPage carries no requested predecessor, first-page marker or cryptographic chain. These checks cannot independently prove that the caller started at the beginning, omitted no row, preserved all interior rows, or contacted an authentic host. The host port and caller own retrieval and resource binding. Tests explicitly preserve this limitation. An observed head/receipt proves no current authority or permission to resume. Events add no authority, totals or facts to the projected report. All required report sources already have checkpoint members; absent checkpoint evidence remains absent even if a related event was supplied.

The two APIs return the same report for the same checkpoint. The checkpoint-only API makes no journal claim. The supplied-page API corroborates that checkpoint within the supplied observations. Neither report is a journal export or an independent proof of event-log completeness.

## Bounds and refusals

The projector validates the checkpoint before copying. Canonical report text is limited to 4 MiB. Page validation retains at most 400 events and at most 4 MiB of supplied event bytes, with the store's existing limit of 100 events per page. It refuses overflow rather than truncating evidence or returning a partial report. There are no implicit limits on time or costs beyond facts recorded by the original run.

`RunReportInputInvalid` refuses an invalid or nonterminal input. `RunReportPrefixInvalid` refuses mismatched or malformed supplied observations. `RunReportOverflow` refuses report, event-count or event-byte overflow. The errors use bounded fixed text. These pure readers return/throw errors; they do not emit execution events or mutate checkpoints, pages or store state.

## Verification

The hand-written shared vectors cover every report property, both checkpoint versions, both terminal statuses, both phases and both request units. They exercise absent and present empty values, zero usage, prompt order, source skill counts, skill loads, critic reasons, delegation, child accounting, failover and plan state. Both languages assert exact canonical text and audit source omission inventories. Shared plan vectors test whole-state preservation as plan contracts evolve. Matched prefix probes cover malformed input, wrong heads, differing terminal checkpoint facts, repeated cursors, page flags, tail mismatches and exact event bounds. A prefix with missing predecessor evidence is explicitly accepted as a supplied observation rather than claimed complete.

## Strict retained reads (#215)

`readRunReport` and Go `ReadRunReport` / `ParseRunReport` add a retained-input boundary separate from canonical serialization. They validate report facts directly, without inventing request keys, operations, context ledgers or other checkpoint state. The self-contained frozen v1 graph retains the published v0.10.0 schema's complete reachable constraints. Existing Plan, source, skill, critic, delegation, usage, failure and provenance validators supply the same nested semantic rules; report-only relationships check grants, request order, completion request bounds, unique child ledgers and failover consumption. No totals, authority or absent provider facts are inferred.

The reader makes an owned copy before semantic inspection. TypeScript copies only own enumerable data descriptors and dense array elements; byte parsing rejects duplicate names and checks exact decimal values before conversion. Go reads JSON tokens under the same limits and checks Unicode escapes before its general decoder could replace an unpaired surrogate. Existing object/Value input cannot recover numeric precision already lost upstream. Both languages preserve accepted canonical bytes, array order, absence and explicit empty/zero values. See the reference for fixed failures and portable Unicode/numeric constraints.

Depth 64 exceeds the deepest supported frozen graph (13 levels after the existing prohibition on nested pending Plan candidates). A node ceiling equal to the 4 MiB byte ceiling cannot exclude a byte-bounded report: each JSON value consumes at least one byte. The test with 32,000 child-usage rows protects this derivation against a smaller arbitrary node ceiling. Raw encoded input and canonical output are both byte-bounded, and malformed input never returns partial facts.

Shared handwritten retained vectors cover historical absence, the safe-integer boundary, Unicode, grants and matched malformed nested members. Existing handwritten terminal report and Plan/evidence vectors exercise every intended optional report member in both exported readers. The packed consumer checks both public TypeScript exports, source graph byte identity and emitted graph equivalence in the actual npm artifact. The original serializer remains deliberately nonvalidating. These readers establish no archive completeness, host authenticity or resume authority; #207's complete history and #208's feedback require their own versioned contracts.
