# Skills

## Sources

Schemas:

- `contract/schemas/skill.json`
- `contract/schemas/tool-offer.json`
- `contract/schemas/tool-taxonomy.json`
- `contract/schemas/provenance.json`

Package files (`packages/harness`), with the symbols read:

- `skills.ts`: `SKILL_SOURCE_KINDS`, `LOAD_SKILL`, `UNLOAD_SKILL`, `SKILL_NAME`, `SKILL_LINE_BYTES`, `SKILLS_HEADER`, `SKILLS_END`, `SKILL_INSTRUCTIONS_HEADER`, `SKILL_INSTRUCTIONS_END`, `SKILL_OPENING`, `SKILL_CLOSING`
- `context/policy.ts`: `SKILL_DEFAULTS`

> generated; edit the sources, then run `node scripts/gen-reference.mjs`. `--check` fails where this page differs from them.

The skill record, how a host declares one and how the run offers it, the skill bounds and the names of the headers and markers.

See also: [tools.md](tools.md), [budget.md](budget.md), [reasons.md](reasons.md), [events.md](events.md), [checkpoint.md](checkpoint.md), [critic.md](critic.md).

## A skill

A skill (ADR-0009): a named, versioned bundle the host declares and supplies beside its host tools (tool-offer.json#/$defs/skillDeclaration): instructions, optionally the names of host tools it uses, optionally small text resources. It comes from a source as a tool does and carries the same source record; the package never fetches one. The package admits it (the record readable, every tools[] name a host tool the host declared, its bytes within skillBytes, its digest the one the person approved), catalogues it in the system text (its name and one framed line), loads it on the model's load_skill call under the skills budget, places its instructions and resources after the history as one block at the rank of the project's own instructions, and records every load on the events, in the checkpoint and in the run report. A skill grants nothing: the tools it names are the host's declared tools under their own effect classes and approvals, offered or not by phase as before, and its text changes nothing the gate admits. Closed: a declaration carrying any other member is refused before any request (configuration).

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `id` | yes | string (pattern `^[A-Za-z][A-Za-z0-9-]{0,23}$`) | The skill's id as the host names it: a letter, then letters, digits and hyphens, at most 24 characters. The checkpoint's loaded set and the skill_unloaded event name a skill by it. |
| `name` | yes | `$defs/name` |  |
| `description` | yes | string (min length 1, max length 1024) | What the skill is for, as its author wrote it: untrusted text. The catalogue shows its first line, cut to 160 bytes and framed as retrieved data under the run's marker. |
| `source` | yes | all of: `tool-offer.json $defs/sourceRecord` / any | Where the skill comes from: tool-offer.json#/$defs/sourceRecord, the record a tool with a source carries, with its kind one a host declares (gateway, mcp, custom or exchange; runtime is refused, as for a tool) and without the members the package sets on a tool's offer (tool, receipt). exchange: a skill whose bytes the host fetched from a Judgment Exchange entry, the entry's id and version the record's id and version. Reported, never a grant. |
| `instructions` | yes | string (min length 1) | The skill's instructions: text, placed only when the skill is loaded, after the history, under the heading that ranks them with the project's own instructions and after them. Counted with the resources against skillBytes. |
| `tools` |  | array of string (min length 1, max length 128) (max items 16) | At most 16 unique references to tools admitted for this run: host tools by offered wire name, and Runtime tools only from validated offered _meta. An unknown or withdrawn reference withholds the skill; tools grant no authority and derive the profile and approval pin dependencies. |
| `resources` |  | array of `$defs/resource` (max items 8) | Small text resources placed with the instructions when the skill is loaded, at most 8, each its name and text. Counted with the instructions against skillBytes. |
| `pinnedDigest` |  | `$defs/digest` | On the declaration: the digest of the bundle the person approved, pinned by the host when the person approved the skill ($defs/digest). A skill whose bundle does not hash to it is withheld at declaration with a guardrail, until the person re-approves the skill and the host declares the new digest. |
| `digest` |  | `$defs/digest` | On admission: the digest of the bundle as declared ($defs/digest), which is the pinned one. The skill_loaded event and the checkpoint's loaded set carry it. |
| `kind` |  | enum `method`, `knowledge`, `procedure`, `review` | Optional author/host classification, untrusted and digest-bound. Absent is unclassified. |
| `tags` |  | array of string (max length 32) (max items 8) | Optional untrusted search tags, never authority; part of the pin. |

### `skill.json $defs/name`

The name the model loads a skill by (load_skill's name) and the catalogue lists: 1 to 64 of the characters a tool name on every provider wire takes. Unique among the skills one run declares.

Type: string (pattern `^[A-Za-z0-9_-]{1,64}$`)

### `skill.json $defs/resource`

One text resource of a skill: its name (1 to 64 characters) and its text.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `name` | yes | string (min length 1, max length 64) |  |
| `text` | yes | string |  |

### `skill.json $defs/digest`

Bundle digest of canonical {name,description,instructions,tools,resources}, plus kind/tags exactly when supplied, plus dependencies for nonempty tools: ordered {name,taxonomy} for each admitted tool. Unknown dependencies cannot be pinned. Classification changes require person re-approval. Existing historical record bytes and stored digest strings are never rewritten.

Type: string (pattern `^sha256:[0-9a-f]{64}$`)

### `skill.json $defs/bytes`

The size a skill is counted at against skillBytes, and that skill_loaded and the checkpoint's loaded set carry: the UTF-8 bytes of its instructions, and of each resource's name and text.

Type: integer (min 0)

## Derived capability profile

### `tool-taxonomy.json $defs/profile`

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `effect` | yes | enum `pure`, `read`, `read-external`, `propose`, `act` |  |
| `operations` | yes | array of enum `discover`, `read`, `answer`, `check`, `transform`, `evaluate`, `propose`, `act`, `run`, `own-state`, `ask` |  |

The package derives the strongest effect and ordered operation union from admitted tool references, including validated offered Runtime tools. Unknown or withdrawn tools withhold the skill; an empty skill is pure. Kind/tags and dependency taxonomies bind the approval digest. Catalogue lines show the profile, and preset filtering consumes it; author/model profiles are refused.

## The offer

### A skill as a host declares it

A skill as the host declares it (ADR-0009, skill.json): the bundle, its source record and the digest the person approved (pinnedDigest), and no digest of its own (the package computes it). A declaration the package cannot read (skill.json's members and bounds, a source of kind runtime, two skills of one name or one id, a source declared twice with different records) ends the run before any request (configuration). A readable one is withheld from the catalogue, with a guardrail refused event whose reason is skill withheld, where a tools[] name is not a host tool the host declared for the run, where its bytes pass skillBytes, or where its bundle does not hash to pinnedDigest (until the person re-approves it).

Type: all of: `skill.json` / any

### A catalogue entry

One admitted skill as the skills catalogue in the system text lists it (ADR-0009): its name, then its description's first line, cut to 160 bytes and framed as retrieved data under the run's marker. The catalogue sits after the tools described in text, in the stable prefix: it never changes during the run, and a load or an unload changes neither it nor the tool list. Its entries, in the order declared, are held to cataloguedSkills and skillsCatalogueBytes (each counted as the JSON text of its name and its line, the frame's constant lines not counted); the skills past either are withheld, one guardrail for each source saying how many.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `name` | yes | `skill.json $defs/name` |  |
| `description` | yes | string |  |

### The tool `load_skill`

The harness tool that loads one catalogued skill (ADR-0009): a reserved name, offered beside load_tool where the run catalogues any skill, in Plan mode and to the critic too (a skill grants no tool), never to a built-in child; declared children receive only the skills their definition names. Its effect is plan, so its answer is not framed as data. It loads only a catalogued, admitted skill (any other name is invalid-tool-input); each load counts against skillsPerRun, the response's loads carried through a resume in the run report's rows (provenance.json#/$defs/sourceFigures, skills), and a load past it is refused (tool-failed, not-started) with a guardrail whose reason is skill budget. Loading a skill already loaded loads nothing and counts nothing. A load emits skill_loaded, writes the checkpoint's skills, and the skill's instructions and resources ride after the history on every later request as one block, under the heading that ranks them with the project's own instructions; on the agent wire, which sends the execution state once per run, the answer carries the block itself. Its answer: the loaded skill ($defs/loadedSkill), its bytes, and a note.

Type: all of: `$defs/hostTool` / any

### The tool `unload_skill`

The harness tool that drops one loaded skill (ADR-0009): a reserved name, offered with load_skill. The skill's block leaves every later request, skill_unloaded is emitted and the checkpoint's skills is written; the skill stays catalogued, and loading it again counts again. A name not loaded is invalid-tool-input.

Type: all of: `$defs/hostTool` / any

### A loaded skill as a load names it

A skill as a load names it (ADR-0009): its id, its name, its source record and its digest. load_skill's answer and the skill_loaded event carry it; the instructions are never on the event.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `id` | yes | string (pattern `^[A-Za-z][A-Za-z0-9-]{0,23}$`) |  |
| `name` | yes | `skill.json $defs/name` |  |
| `source` | yes | `$defs/sourceRecord` |  |
| `digest` | yes | `skill.json $defs/digest` |  |

### A skill in the run report

One loaded skill as the checkpoint keeps it (ADR-0009; checkpoint.json skills): its id, the digest it was loaded at, its bytes (skill.json#/$defs/bytes) and when it was loaded. A continued run restores it only where the host still declares the skill with that digest.

Closed: a member the schema does not name is refused.

| Member | Required | Type | Description |
| --- | --- | --- | --- |
| `id` | yes | string (pattern `^[A-Za-z][A-Za-z0-9-]{0,23}$`) |  |
| `digest` | yes | `skill.json $defs/digest` |  |
| `bytes` | yes | `skill.json $defs/bytes` |  |
| `loadedAt` | yes | string (format date-time) |  |

## Constants

| Constant | Value |
| --- | --- |
| `SKILL_SOURCE_KINDS` | `gateway`, `mcp`, `custom`, `exchange` |
| `SKILL_NAME` | `^[A-Za-z0-9_-]{1,64}$` |
| `SKILL_LINE_BYTES` | 160 |

### Skill bounds and their defaults

These are members of the context budget; see [budget.md](budget.md).

| Member | Default |
| --- | --- |
| `skillBytes` | 8000 |
| `skillsPerRun` | 4 |
| `skillsCatalogueBytes` | 8000 |
| `cataloguedSkills` | 32 |

### Headers and markers

Named here, never their text. Each is a constant of `skills.ts`.

| Constant |
| --- |
| `SKILLS_HEADER` |
| `SKILLS_END` |
| `SKILL_INSTRUCTIONS_HEADER` |
| `SKILL_INSTRUCTIONS_END` |
| `SKILL_OPENING` |
| `SKILL_CLOSING` |

## Asking well

When the retained tool offer includes `ask_user` as a question with taxonomy `ask` / `pure`, Agent includes the version 1 `asking-well` method. Its only dependency is `ask_user`; its profile is derived as `ask` / `pure`. `askingWellSkill()` (TypeScript) and `AskingWellSkill()` (Go) return the package bundle with the published current pin. Its existing `custom` source `agent-methods`, trust `first-party`, identifies immutable package-owned bytes. This does not introduce a source kind or confer that provenance on host declarations.

The skill tells the model to read available context and tools first, ask only necessary missing information, avoid answered questions, ask one decision at a time, explain what changes, present 2–4 mutually exclusive options with a reasoned recommendation first, and preserve free text and the person's actual choice. Loading remains the model's choice. A recommendation never supplies a user answer. Instructions do not enforce arbitrary model behavior; scripted certification checks delivered instructions and harness behavior, not general live-model quality. The live release comparison gate remains required.

The name `asking-well` is reserved for a host replacement when this question tool is actually offered. Supply exactly one normal declaration with that name, kind `method`, and exactly `tools: ["ask_user"]`, with the host's own provenance and approved digest. The existing digest covers instructions, kind, tools, resources, tags and dependency taxonomy. No profile or digest override is accepted. Wrong kind, dependencies or duplicate replacement declarations refuse configuration before a provider request. A stale pin withholds the replacement without falling back to the package method and refuses startup.

Host declaration order is preserved and the default is appended. Ordinary skill admission, 32-skill catalogue maximum and byte bounds still apply; no host entry is evicted. If the selected method is withheld by admission, pin, dependency, profile or catalogue capacity, a run offering the question tool refuses configuration before a provider request. Other host skills retain their ordinary admission behavior. Neither `allowConversation` nor a child or Plan phase creates a question tool; the method follows the retained actual offer. Children without `ask_user` receive no automatic method. No tool or action authority is granted by a skill.
