# Tools

## Sources

Schemas:

- `contract/schemas/tool-offer.json`
- `contract/schemas/provenance.json`

Package files (`packages/harness`), with the symbols read:

- `host.ts`: `ASSISTANT_TOOLS`
- `toolGate.ts`: `REFUSED_RUNTIME_TOOLS`, `TEST_TOOL`, `REHEARSAL_TOOL`, `REHEARSAL_MEMBER`, `TEST_ARGUMENTS`
- `toolSources.ts`: `HELD_NAMES`, `LOAD_TOOL`, `DECLARED_SOURCE_KINDS`, `TRUST_CLASSES`, `EFFECT_CLASSES`, `APPROVAL_DEFAULT`, `SOURCE_ID`, `NAME_BYTES`
- `engines/contract.ts`: `PROPOSE_PACK_TOOL`
- `delegationContract.ts`: `DELEGATE_TOOL`, `TASK_PROFILES`
- `context/reader.ts`: `READ_TOOL`
- `context/search.ts`: `SEARCH_TOOL`
- `skills.ts`: `LOAD_SKILL`, `UNLOAD_SKILL`
- `refutation.ts`: `CRITIC_TOOLS`, `CRITIC_READERS`
- `planning/policy.ts`: `PLAN_RUNTIME_READS`

> generated; edit the sources, then run `node scripts/gen-reference.mjs`. `--check` fails where this page differs from them.

The Runtime tools a run may be offered, per profile; the names the harness reserves; a host tool’s declaration; effect classes, approval and tool sources.

See also: [skills.md](skills.md), [delegation.md](delegation.md), [critic.md](critic.md), [budget.md](budget.md), [reasons.md](reasons.md).

## Runtime tools

### The ceiling

The runtime tools any session may be allowed (ASSISTANT_TOOLS in packages/harness/host.ts). A session's allow-list is a subset; the gate refuses a call outside it on the wire. The Desk's five (runtimeToolCeiling in contract/desk/inventory.json) and, from the harness (agent#7), experimental_test_cases, which takes pack text and a matrix and reads no project file; it leaves the page only with the arguments tool-call.json $defs/testCasesArguments states, and one turn may call it at most testCallsPerTurn times (budget.json). The Runtime's other test and project tools stay off it: test_conformance (its suite argument is a local path the runtime reads), experimental_test_packs and experimental_test_graphs (they test the saved tree, not the draft, outside the rehearsal rule), get_pack and list_packs (they read the saved tree); toolGate.ts, REFUSED_RUNTIME_TOOLS.

| Runtime tool | Gate rule named in the package |
| --- | --- |
| `get_schema` |  |
| `list_examples` |  |
| `get_example` |  |
| `validate` |  |
| `experimental_evaluate` | held to the rehearsal (`rehearsal` member) |
| `experimental_test_cases` | arguments limited to `pack`, `matrix` |

The Runtime tools that stay off the ceiling, with the reason the gate’s source states for each:

| Runtime tool | Why it stays off the ceiling |
| --- | --- |
| `test_conformance` | its suite argument is "a local suite directory or manifest.json", a path the runtime reads on the machine that serves it: admitting it would give the model a file read, and the gate checks names, not paths |
| `experimental_test_packs` | it tests the saved project tree, not the draft the model is proposing, and saved suites are not held by the rehearsal rule that holds experimental_evaluate |
| `experimental_test_graphs` | it tests the saved graphs, not the draft, under the same rule as experimental_test_packs |
| `get_pack` | it reads the saved project tree; the draft travels in the arguments of the tools that test it |
| `list_packs` | it lists the saved project tree, for the same reason as get_pack |

### Runtime tools per profile

| Profile | Rule | Runtime tools offered |
| --- | --- | --- |
| session | a subset of the ceiling, the host’s allow-list | `get_schema`, `list_examples`, `get_example`, `validate`, `experimental_evaluate`, `experimental_test_cases` |
| plan mode | the ceiling without `experimental_evaluate`, `experimental_test_cases` | `get_schema`, `list_examples`, `get_example`, `validate` |
| critic | its own allow-list | `validate`, `experimental_evaluate`, `get_schema`, `experimental_test_cases` |
| delegated child: analyze | the profile’s runtimeTools | none |
| delegated child: review | the profile’s runtimeTools | none |
| delegated child: research | the profile’s runtimeTools | none |
| delegated child: author-test | the profile’s runtimeTools | `get_schema`, `list_examples`, `get_example`, `validate`, `experimental_evaluate`, `experimental_test_cases` |

### `tool-offer.json $defs/propose_edit`

ADR-0015: change only the package draft. Author only. Anchors must occur exactly once; every candidate, including undo, is validated before replacement.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `document` | yes | string (min length 1, max length 512) |  |
| `old_text` | yes | string (min length 1) |  |
| `new_text` | yes | string |  |

### `tool-offer.json $defs/propose_delete`

ADR-0015: change only the package draft. Author only. Anchors must occur exactly once; every candidate, including undo, is validated before replacement.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `document` | yes | string (min length 1, max length 512) |  |
| `old_text` | yes | string (min length 1) |  |

### `tool-offer.json $defs/propose_insert`

ADR-0015: change only the package draft. Author only. Anchors must occur exactly once; every candidate, including undo, is validated before replacement.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `document` | yes | string (min length 1, max length 512) |  |
| `after_anchor` | yes | string (min length 1) |  |
| `text` | yes | string |  |

### `tool-offer.json $defs/undo_edit`

ADR-0015: change only the package draft. Author only. Anchors must occur exactly once; every candidate, including undo, is validated before replacement.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `document` | yes | string (min length 1, max length 512) |  |

### `tool-offer.json $defs/draftId`

The current package-owned document in the search/read_window id space; the suffix is its document id, never a file path.

Type: string (pattern `^draft:.+$`)

### `provenance.json $defs/edit`

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `type` | yes | enum `propose_edit`, `propose_delete`, `propose_insert`, `undo_edit` |  |
| `document` | yes | string (min length 1, max length 512) |  |
| `line` | yes | integer (min 1) |  |
| `bytes` | yes | integer (min 0) |  |
| `valid` | yes | const `true` |  |

### `provenance.json $defs/edits`

Ordered provenance of successful draft changes, including undo; the person and critic review the whole proposal.

Type: array of `$defs/edit`

## Reserved names and harness tools

### Context tool names

Names the host keeps for its own context tools; an offered tool that takes one ends the session (context/session.ts, contextualSession). read_window (agent#1) and search (agent#5) are the harness's own readers. load_tool (ADR-0006) is the harness's catalogue loader, offered only where the schema budget trips. load_skill and unload_skill (ADR-0009) are the harness's skill loaders, offered only where the run catalogues a skill. request_review (ADR-0010) is the harness's ask for a review, offered only under auto in the author's phase.

The package also holds a wider set, `HELD_NAMES`, that a tool with a source may not bear: the ceiling, the Runtime tools that stay off it, the context names above and `propose_pack`.

| Held name | Origin |
| --- | --- |
| `get_schema` | Runtime tool on the ceiling |
| `list_examples` | Runtime tool on the ceiling |
| `get_example` | Runtime tool on the ceiling |
| `validate` | Runtime tool on the ceiling |
| `experimental_evaluate` | Runtime tool on the ceiling |
| `experimental_test_cases` | Runtime tool on the ceiling |
| `test_conformance` | Runtime tool off the ceiling |
| `experimental_test_packs` | Runtime tool off the ceiling |
| `experimental_test_graphs` | Runtime tool off the ceiling |
| `get_pack` | Runtime tool off the ceiling |
| `list_packs` | Runtime tool off the ceiling |
| `list_context` | context tool name |
| `read_context` | context tool name |
| `read_window` | context tool name |
| `search` | context tool name |
| `update_plan` | context tool name |
| `request_budget_increase` | context tool name |
| `load_tool` | context tool name |
| `remember` | context tool name |
| `propose_edit` | context tool name |
| `propose_delete` | context tool name |
| `propose_insert` | context tool name |
| `undo_edit` | context tool name |
| `load_skill` | context tool name |
| `unload_skill` | context tool name |
| `request_review` | context tool name |
| `propose_pack` | harness host tool |

The tools the harness itself serves:

| Tool | Constant | File | Schema |
| --- | --- | --- | --- |
| `propose_pack` | `PROPOSE_PACK_TOOL` | `engines/contract.ts` | `tool-offer.json $defs/proposePack` |
| `load_tool` | `LOAD_TOOL` | `toolSources.ts` | `tool-offer.json $defs/loadTool` |
| `load_skill` | `LOAD_SKILL` | `skills.ts` | `tool-offer.json $defs/loadSkill` |
| `unload_skill` | `UNLOAD_SKILL` | `skills.ts` | `tool-offer.json $defs/unloadSkill` |
| `delegate_tasks` | `DELEGATE_TOOL` | `delegationContract.ts` | `delegated-task.json $defs/request` |
| `read_window` | `READ_TOOL` | `context/reader.ts` | `tool-result.json $defs/readArguments` |
| `search` | `SEARCH_TOOL` | `context/search.ts` | `tool-result.json $defs/searchArguments` |

## Host tools

### A host tool’s declaration

A host tool's declaration (HostTool, without its execute function, which never crosses). Its schema is the host's, written in the subset every family's wire carries.

Open: a member the schema does not name is accepted.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `name` | yes | string |  |
| `description` | yes | string |  |
| `inputSchema` | yes | any |  |
| `effect` |  | enum `read`, `proposal`, `workflow`, `memory`, `plan` | Host-declared effect, never supplied by model arguments. |
| `interaction` |  | enum `question`, `delegation` | A tool the adapter handles itself. question (ask_user): the adapter emits user_question, pauses, and returns no answer of its own. delegation (delegate_tasks): the host's coordinator runs the batch; an engine that awaits a tool call (vercel) executes it in the run's step with no tool-call deadline and reports a tool_call and a tool_result, the children's findings its result (agent#14); an engine whose run cannot wait for one (codex) emits delegation_requested and ends its run. |
| `delegation` |  | enum `read`, `research` | The read-only capability this tool offers to a delegated child; absent means parent-only. |
| `contextPolicy` |  | object | Host-declared context retention (ContextToolPolicy), independent of topic, provider and action permission. immutable: the same arguments return the same value, so a read may be served from the read cache: the run's own, or a chat-scoped context's across the chat's runs (agent#8, tool-result.json#/$defs/readCacheKey); declare it only for a value that does not change for the same arguments while the chat lasts. reference: prefer this material in the bounded working summary. runtime: the result depends on the Runtime that serves it (its identity and version), so a chat's read cache keys it by the Runtime the host names for the run; every runtime tool is. |
| `contextPolicy.immutable` |  | boolean |  |
| `contextPolicy.reference` |  | boolean |  |
| `contextPolicy.runtime` |  | boolean |  |
| `presentation` |  | object | Host-owned presentation, frozen when the tool is offered. |
| `presentation.provider` | yes | string |  |
| `source` |  | `$defs/sourceRecord` | Where the tool comes from (ADR-0006), on a tool a source outside the host serves; absent on the host's own tools. On the offer it carries tool, the tool's own name at its source. |
| `effectClass` |  | `$defs/effectClass` |  |
| `approval` |  | enum `none`, `required` | Whether a call needs the person's approval before the host runs it (ADR-0006, ADR-0008): the host's approval policy (HostTool.approve, Go ApprovalPolicy) answers each call at once, $defs/approvalPolicy: allow runs it, refuse refuses it (the model reads a refused result and a guardrail says why), ask pauses the run (run-event.json#/$defs/approval_requested) until the person decides. A required tool without a policy answers ask. none: the call runs without asking, the host's declared decision. Default by effect class: none for read and propose (a proposal's approval is its review card), required for act. Read on a tool with a source; the host's own tools keep their own rules. |

## Effect classes and approval

### Effect class of a tool with a source

What a call of a tool with a source may do (ADR-0006): read (it only reads; its result is data), propose (it is never run during the run: the call is held as a proposal for the person's review card, as propose_pack's is, and the host runs it on acceptance, after the run, under its grants) or act (it changes something outside the run, and by default needs the person's approval before each call). A tool with a source is act, with approval required, unless the host declares it a read or a proposal; the model's arguments never change it. On the offer the package sets the host tool's effect from it: read is effect read, propose is effect proposal, act is no effect, so Plan mode and a delegated child, which admit reads only, never offer an act.

| Effect class | Default approval |
| --- | --- |
| `read` | `none` |
| `propose` | `none` |
| `act` | `required` |

### The approval policy’s answers

What the host's approval policy answers for one act call (ADR-0008; HostTool.approve, Go ApprovalPolicy), asked with the call (the wire name, the tool's own name, its source record, a copy of the arguments) and answered at once, never waiting for the person: allow, the call runs under its bounds; refuse, it is refused with a guardrail (reason approval refused) and never run; ask, the run pauses (approval_requested). An answer that is none of the three, or a policy that fails, is refuse.

## Tool sources

Kinds a host declares: `gateway`, `mcp`, `custom`; the record also carries `runtime` (the Runtime's own row) and `exchange` (a skill from a Judgment Exchange entry, see [skills.md](skills.md)). Trust classes: `first-party`, `connected`, `public`, `person`. A source's id matches `^[A-Za-z][A-Za-z0-9-]{0,23}$`; a wire name is at most 64 characters.

### The source record

Where a host tool comes from (ADR-0006). Declared by the host on each host tool a source outside the host serves; the package adds tool (the tool's own name at its source) on the offer, carries the record on the tool's tool_call and tool_result events (with receipt on a result where the host's answer gave one), and counts the source in the run report (provenance.json#/$defs/sourceFigures). gateway: a gateway connection the person configured (Databricks, Azure DevOps, Drive), which may give a receipt for each call; mcp: a public MCP server the host connected, whose tool list is pinned by digest at connection time; custom: a tool the person created; exchange (ADR-0009): a skill whose bytes the host fetched from a Judgment Exchange entry, the entry's id and version the record's id and version, which a host tool may not declare. runtime is the Runtime's own row in the run report: its tools come through tools/list behind the gate, carry no declaration, and a host tool declared with kind runtime is refused. Either version or toolListDigest is present; mcp carries toolListDigest. Closed: the package refuses another member before any request (configuration).

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `kind` | yes | enum `runtime`, `gateway`, `mcp`, `custom`, `exchange` |  |
| `id` | yes | string (pattern `^[A-Za-z][A-Za-z0-9-]{0,23}$`) | The source's name as the host names it (a connection, a server, the person's own tools): a letter, then letters, digits and hyphens, at most 24 characters. Every wire name of the source's tools begins with it. |
| `version` |  | string (min length 1, max length 64) | The source's version as the host knows it: a connection's revision, a server's version, a custom tool's revision. |
| `toolListDigest` |  | `$defs/toolListDigest` |  |
| `trust` | yes | enum `first-party`, `connected`, `public`, `person` | The trust class the host gives the source: first-party (the Runtime, and a source the host itself runs), connected (a connection the person configured with their own grants), public (a public server), person (a tool the person wrote). Reported, never a grant: whatever the class, the source's descriptions are untrusted text and its results data, and its tools are held to their effect class. |
| `identity` |  | `$defs/identity` |  |
| `identityDigest` |  | string (pattern `^sha256:[0-9a-f]{64}$`) | The package's pin of identity: sha256: then the SHA-256 of the canonical JSON of identity. It is set with the source's tool-list pin and compared with the identity the host reports before a call; a change withholds the source until the person re-approves it. |
| `tool` |  | string (min length 1, max length 128) | The tool's own name at its source, set by the package on the offer and the events from the declaration's name (the wire name is the offered tool's name). A declaration leaves it out. |
| `receipt` |  | string (min length 1, max length 128) | On a tool_result only: the receipt id the host's answer gave for this call (HostToolResult.receipt; a gateway connection's), at most 128 characters. |

Conditions beyond the members (if, anyOf) are in the schema file.
