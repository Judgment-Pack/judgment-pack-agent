# Reasons

## Sources

Schemas:

- `contract/schemas/run-event.json`
- `contract/schemas/failure.json`
- `contract/schemas/question.json`
- `contract/schemas/provenance.json`

Package files (`packages/harness`), with the symbols read:

- `completion.ts`: `COMPLETION_REASONS`
- `failures.ts`: `RETRYABLE_REFUSAL_CLASSES`, `RETRYABLE_OVERLOAD_CODES`
- `questionFailure.ts`: `questionFailureMessage`
- `contract/catalog.json`

> generated; edit the sources, then run `node scripts/gen-reference.mjs`. `--check` fails where this page differs from them.

The guardrail’s actions and reasons, the failure classes with their recovery and message, the completion reasons and the question format failures.

See also: [tools.md](tools.md), [skills.md](skills.md), [events.md](events.md), [budget.md](budget.md), [critic.md](critic.md).

## Guardrail

The host saying what it did. rewrote and refused are the tool gate's, emitted by the host as the call's frame leaves the page, so they arrive between that call's tool_call and tool_result. narrowed is the adapter's notice, before the model is asked anything, that a tool's served schema lost a keyword on this family's wire. Also refused, with tool proposal, where the host withholds a proposal (Plan mode, or a conversation without artifact proposals). reason (ADR-0008), on a refusal of an act call of a tool with a source: approval refused, the host's approval policy answered refuse; approval pending, the call came while another act call of the run waits for the person's decision (at most one approval is pending); approval stale, the gate's own refusal of a call whose decision no longer holds for it (its arguments not those its approval was decided for, a call that reaches its run without an allow, or, on the continued run, its tool no longer offered as the person decided). None of these calls is run. skill withheld (ADR-0009), on a skill the package did not catalogue or restore, tool its name (or, for the catalogue's bounds, its source's id): its tools[] names a tool the host did not declare, its bytes pass skillBytes, its bundle does not hash to the digest the person approved, the catalogue's bounds left it out, or a skill the checkpoint had loaded is no longer declared as it was; skill budget, on a load_skill call refused past skillsPerRun, between that call and its failed result. Nothing of a withheld skill reaches the model. review refused (ADR-0010), on a request_review call refused: a second ask of the run, an ask under off or on, or one outside the author's phase; nothing is recorded and no review is asked.

| Member | Values |
| --- | --- |
| `action` | `rewrote`, `refused`, `narrowed` |
| `reason` | `approval refused`, `approval pending`, `approval stale`, `skill withheld`, `skill budget`, `review refused`, `policy refused`, `pace warning`, `subagent withheld`, `subagent refused`, `memory refused`, `edit refused`, `compaction refused`, `output refused`, `records expired`, `identity changed`, `approval downgraded`, `record refused` |

## Failure classes

The catalogue's classes, the keys of failures in contract/desk/catalog.json, spelled as the Desk spells them.

| Class | Recovery | Message | Retryable refusal |
| --- | --- | --- | --- |
| `cancelled` | `none` | The response was stopped. |  |
| `request-limit` | `increase` | Work paused at the approved request limit. |  |
| `provider-limit` | `review` | The AI provider stopped this run at its limit. |  |
| `context-limit` | `narrow` | The current AI context exceeds its configured budget. Saved work is retained; reduce the current request or read smaller context sections. |  |
| `authentication` | `configure` | The AI connection needs authentication. Check its credentials or sign in again. |  |
| `permission` | `configure` | This AI connection does not allow the requested operation. Check access and enabled models. |  |
| `configuration` | `configure` | The AI connection is unavailable or needs configuration. Check it in Admin. |  |
| `busy` | `wait` | The AI connection is busy. No new operation was admitted. |  |
| `rate-limited` | `wait` | The AI provider is limiting requests. Wait before trying again. | yes |
| `unavailable` | `retry` | The AI service is temporarily unavailable. Completed work is retained. |  |
| `connection-lost` | `review` | The AI connection ended before completion. Review completed work before continuing. |  |
| `timeout` | `review` | The AI request timed out. Its outcome may be uncertain; review completed work before continuing. |  |
| `invalid-request` | `configure` | The AI request was rejected. Check the selected model and request settings before trying again. |  |
| `invalid-response` | `retry` | The AI returned no usable complete response. Completed work is retained. |  |
| `storage` | `storage` | Progress could not be saved. Keep this window open and restore storage before continuing. |  |
| `stale-state` | `reload` | The saved context or configuration changed. Reload the current state before continuing. |  |
| `tool-failed` | `review` | The tool could not complete this operation. Check its result before repeating it. |  |
| `unknown` | `review` | The response could not complete. Review completed work before continuing. |  |
| `retries-exhausted` | `review` | The AI connection could not recover after automatic retries. Review completed work before continuing. |  |
| `invalid-tool-input` | `review` | The tool input needs correction before this operation can continue. |  |
| `output-limit` | `review` | The AI reached its output-token limit before completing the response. Completed work is retained. |  |
| `content-filter` | `review` | The AI provider blocked this response. Review the request before continuing. |  |
| `repeated-work` | `review` | The assistant kept repeating completed work. Desk stopped this response and retained the results. |  |
| `pace exceeded` | `wait` | The project period budget is exhausted. Wait for the next period or change the project policy. |  |

Overload codes a refusal is retried on: `overloaded_error`.

Aliases a provider body may name, read as a class:

| Alias | Class |
| --- | --- |
| `busy` | `busy` |
| `assistant-relay-busy` | `busy` |
| `sign-in-required` | `authentication` |
| `assistant-no-key` | `authentication` |
| `unauthorized` | `authentication` |
| `assistant-key-unbound` | `configuration` |
| `assistant-unconfigured` | `configuration` |
| `model-not-allowed` | `permission` |
| `forbidden` | `permission` |
| `limit` | `provider-limit` |
| `unavailable` | `unavailable` |
| `retries-exhausted` | `retries-exhausted` |
| `run-failed` | `unknown` |
| `assistant-relay-upstream` | `connection-lost` |
| `context_length_exceeded` | `context-limit` |
| `context_window_exceeded` | `context-limit` |
| `rate_limit_exceeded` | `rate-limited` |
| `overloaded_error` | `unavailable` |
| `authentication_error` | `authentication` |
| `permission_error` | `permission` |
| `permission_denied_error` | `permission` |
| `invalid_api_key` | `authentication` |
| `insufficient_quota` | `configuration` |
| `billing_error` | `configuration` |
| `stale` | `stale-state` |
| `credentials-required` | `authentication` |
| `rate-limited` | `rate-limited` |
| `search-temporary` | `unavailable` |
| `search-timeout` | `timeout` |
| `search-invalid-response` | `invalid-response` |
| `search-invalid-request` | `invalid-request` |
| `search-storage-failed` | `storage` |
| `source-changed` | `stale-state` |
| `blocked-by-policy` | `permission` |
| `rate_limit_error` | `rate-limited` |
| `RESOURCE_EXHAUSTED` | `rate-limited` |
| `api_error` | `unavailable` |
| `server_error` | `unavailable` |
| `context-limit` | `context-limit` |

### Failure source

Which side the failure came from (FailureSource).

Type: enum `model`, `agent`, `tool`, `context`, `storage`, `desk`

### Failure outcome

not-started: positively refused at admission, nothing ran (an agent run is retried after it only with code busy: canRetryAdmission). refused: a 4xx other than 408, the request was refused (a model request is sent again after it only for the refusals failures.ts names: the rate-limited class and the overload code overloaded_error, canRetryRefusal, at most twice). unknown: anything else, including every 5xx and timeout; never retried by the engine, replay safety is never inferred. One exception, a delegated child's (agent#17, delegated-task.json retried): a 5xx other than 504 that carries the rate-limited class or the overload code overloaded_error, before the child's first answer byte, is sent once more.

Type: enum `not-started`, `refused`, `unknown`

### Recovery

The recovery a catalogue entry names for the person.

Type: enum `none`, `increase`, `review`, `narrow`, `configure`, `wait`, `retry`, `storage`, `reload`

### A failed tool’s content

The structuredContent of a tool result that failed (toolFailureResult): the failure itself, so a failed tool is recoverable working data rather than a failed turn.

Open: a member the schema does not name is accepted.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `failure` | yes | `failure.json` |  |

## Completion reasons

Provider-neutral completion metadata (ModelCompletion). Never raw provider text or reasoning. Open: readCompletion keeps only these members and drops any other.

Finish reasons: `stop`, `length`, `content-filter`, `tool-calls`, `error`, `other`.

## Question format failures

The structuredContent of an ask_user result the host refused (questionFailureResult): which field to correct, or unavailable. The model corrects the named field and asks again in the same turn.

| Code | Message |
| --- | --- |
| `shape` | The question format was invalid. Use only title, question, selection, options, recommendedOptionId and recommendation. |
| `title` | Use a question title between 1 and 40 characters. |
| `question` | Use one question between 1 and 600 characters. |
| `selection` | Set selection to single or multiple. |
| `options` | Use two to four distinct choices, or none for free text. Each choice needs a unique ID (letters, numbers, hyphens or underscores), a label up to 100 characters, and an optional description up to 240 characters. |
| `recommendation` | A recommendation needs an existing choice ID and a reason up to 240 characters. Otherwise leave both recommendation fields empty. |
| `unavailable` | This chat could not accept the question. You can answer in the message field. |
