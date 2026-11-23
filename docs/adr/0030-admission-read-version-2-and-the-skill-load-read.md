# ADR-0030: The admission read version "2" and the skill-load read

Status: accepted, with the maintainer's rulings at the end, which answer its open questions. 0030
is a placeholder, assigned at merge. Builds on
[ADR-0006](0006-tool-sources.md), [ADR-0009](0009-skills.md), [ADR-0013](0013-sub-agents.md) and
[ADR-0029](0029-agent-principals.md). It is the library's row of build step 4 of spec RFC 0030's
acceptance plan: version `"2"` admission reads and the skill-load read (RFC §2a, *The admission
reads on the wire*, at spec commit
[`4b51a575`](https://github.com/Judgment-Pack/judgment-pack-spec/blob/4b51a575aae13b4c549c93ea41a73872e62eb890/rfcs/0030-agent-principals-and-delegated-authority.md)).
The RFC is a Draft; where this builds against it, it builds against a Draft.

## Context

ADR-0029 admits every call of a tool with a source immediately before dispatch by handing the
host's admission port a read of version `"1"`: `PrincipalAdmissionRead.Value` writes `readVersion`
`"1"` (`go/context/run_principal.go:178`), the TypeScript `AdmissionRead` type is fixed at `'1'`
(`packages/harness/principal.ts:70`), and the reads are built at `go/context/run_principal.go:1097`
and `:1148` and `packages/harness/principal.ts:699` and `:730`. Skills load with no read of the
issuing host: `load_skill` checks the name, the loaded set and the skill budget, then writes the
checkpoint's `skills` and emits `skill_loaded` (`go/context/skills.go:622`, the budget claim at
`:639`; `packages/harness/skills.ts:449`), and a continued run restores the checkpoint's set
against the host's declarations alone (`go/context/skills.go:522`; `packages/harness/skills.ts:417`).

RFC §2a adds version `"2"`: a call read carries `call.version`, the called tool's descriptor digest
or `null`, and `call.skills`, the skills loaded at the call, and may be answered `tool-version-refused`
or `skill-refused` (step 12). Version `"2"` adds a skill-load read the library alone asks before a
skill loads into a delegated run. An issuing host answers a body it does not read with
`{"refusal": "read-version-unsupported"}` or `{"refusal": "read-malformed"}`. The sender's version
is configured, never negotiated, never retried at another.

The library's approval of a tool records no descriptor digest today: both descriptor-snapshot
readers take only a snapshot with no `snapshotVersion` or with `"2"`
(`go/context/descriptor_snapshot.go:329`; `packages/harness/descriptorSnapshot.ts:149`), and the
per-tool `descriptorDigest` is a member of snapshot versions 3 and 4 (RFC open question 24).

## Decision

**Version "2" is opted into per issuing host.** `RunPrincipal` gains `ReadVersions`
(TypeScript `readVersions`), a map from issuing host to `"1"` or `"2"`, beside `Keys`, which is
keyed the same way. A host it does not name, and an absent map, is `"1"`. Any other value refuses the
principal before any request, as an unreadable principal does (`PrincipalInputInvalid` in
TypeScript; the Go input error), naming the host and the value. A run reads its version once, at
start, from its root's `host`, and every read it makes to that host is at that version. A delegated
child (ADR-0013) inherits its parent's map and reads at the same version, its chain having the same
root. The child-link request (`run-principal.json#/$defs/childLinkRequest`) is not one of §2a's
reads and stays at `"1"` at either setting (ruling 3).

**At "1", nothing changes, byte for byte.** With no map, or `"1"` for the host: the read types,
their bytes, the answers taken, the refusals, the loader, the restore and every record are as today;
`contract/vectors/principal/admission.json` (33 cases) passes unchanged. The new answer forms are
`admission-unknown` at `"1"` (RFC §2a). An undelegated run makes no read at any version.

**The call read at "2".** The read types gain the two members, written only at `"2"`:

- Go: `PrincipalAdmissionRead` gains `ReadVersion string` (empty is `"1"`), and
  `PrincipalAdmissionCall` gains `Version *string` (nil is `null`) and `Skills []PrincipalSkill`
  (`{ID, Digest}`). `Value()` writes them, and `readVersion` `"2"`, only when `ReadVersion` is
  `"2"`. Additive, so a host's port written for `"1"` compiles and receives the same values.
- TypeScript: `AdmissionRead` becomes a union on `readVersion`, `'1'` as today and `'2'` with
  `call.version: string | null` and `call.skills: {id, digest}[]`.

Every call read is at that version, the standing reads of a host tool with no source (source
`local`, `go/context/run_principal.go:1148`) and the pre-approval read with counting unset
(ADR-0029) included, so a version or skill refusal is known before the person is asked to approve.

**How `version` is filled.** From the approval the library holds, never from the model, the
arguments, the tool's answer or the host's current observation:

- a source declared in snapshot mode (ADR-0006, *Gateway snapshot pins*): the `descriptorDigest` of
  the called tool's entry in the approved `descriptorSnapshot`. This needs both readers to take
  snapshot versions `"3"` and `"4"`, by the gateway's reader, which this record builds in the same
  release, first: the readers at `go/context/descriptor_snapshot.go:329` and
  `packages/harness/descriptorSnapshot.ts:149` are part of this build, not a follow-up, so
  `call.version` is never always `null` (ruling 6). The current observation is compared with the approved bytes as today, and a
  changed snapshot latches refusal before any read;
- `null` for a snapshot of a version holding no `descriptorDigest`, a legacy tool-list declaration,
  a `custom` source, a host source, an acting Runtime call (source `runtime`) and source `local`.

For a gateway tool this is the digest the gateway names in its own read and in its tools/list
`_meta.descriptorDigest` (gateway decision record on read `"2"`), when the approved snapshot is the
capture the gateway pins. The library does not read `_meta`; a host can show it when it asks for
approval. A version label is not read and not sent: §2a's `version` is a digest or `null`, and the
label is in the gateway's `_meta` only, beside the digest; the RFC needs no amendment (ruling 1).

**How `skills` is filled.** The run's loaded set as the checkpoint records it (`checkpoint.skills`)
at the instant of the read: `{id, digest}` per skill, ordered by `id` by code point, `[]` when none
is loaded, never `null`. It is taken through `SkillGate.Loads` (`go/context/skills.go:570`) and its
TypeScript peer, under the gate's lock, so a read sees every load and unload before it and none
half done; a call's read waits for a skill-load read in flight (at most five seconds). A child's
read names the child's own loaded set, never its parent's (ruling 5): by ADR-0009's amendment
*Declared subagent skills*, an ad hoc delegated child is handed no skills and a declared subagent
exactly its definition's, loaded under its own `skillsPerRun` count, and neither inherits the
parent's loaded set. A gateway source's call makes the library's read too, uncounted
(ADR-0029), so the issuing host judges a gateway call's skills on the library's read.

**The skill-load read, and where it sits.** At `"2"`, for a delegated run, `load_skill` runs in this
order:

1. the name is catalogued (`invalid-tool-input` otherwise), as today;
2. a skill already loaded loads nothing and reads nothing, as today;
3. the skill budget is claimed (`skillsPerRun`; `skill budget` past it, no read made), as today;
4. step 8 at the library's time (`not-yet-valid`, `expired`), no read made where it refuses;
5. the skill-load read, through the port below: `{"readVersion": "2", "host", "audience", "chain",
   "load": {"skill": {"id", "digest"}, "skills": [...]}, "at"}`, `load.skills` the set already
   loaded in the form above, never holding `load.skill.id`, `audience` the run's agent-host
   audience;
6. on `{"load": "admit"}` alone, the load as today: the checkpoint's `skills` written and
   `skill_loaded` emitted.

The budget is claimed before the read, so a refused or unanswered read keeps its claim: each
attempt that reaches the issuing host counts as a load in `sourceFigures.skills`, and the budget
bounds the reads a model can cause in one run. The read counts nothing at the
host. A refused load counts against the skill budget, so a loop of refusals ends (ruling 2). An
unload makes no read.

**The skill-load port.** `RunPrincipal` gains `SkillLoad` (Go `PrincipalSkillLoadPort`,
`func(ctx, PrincipalSkillLoadRead) ([]byte, error)`; TypeScript `skillLoad`, `(read, signal) =>
answer`), answered `{"load": "admit"}`, `{"load": "refuse", "refusal": R}` with R one of `expired`,
`agent-unregistered`, `software-mismatch`, `not-issued`, `revoked` and `skill-refused`, or §2a's
two `{"refusal": ...}` forms; anything else, a throw, or no answer in five seconds is
`admission-unknown`. A run whose host is at `"2"` and that catalogues any skill, with no `SkillLoad`,
is refused before any request (`configuration`), so no skill loads unread. A separate port keeps the
version `"1"` call-read type unchanged and gives a host a type it cannot confuse with a call read.

**On `skill-refused`, or any refused or unanswered skill-load read.** Nothing loads: `load_skill`
fails `tool-failed`, outcome `not-started`, with a guardrail between the call and its result, reason
`delegation refused`, its `refusal` and the run's `principal` (ADR-0029, *Refusals are named and
recorded*); the model is told the refusal by name and that nothing was loaded. The checkpoint's
`skills` and `skill_loaded` are not written, and the run goes on.

**On `tool-version-refused` and `skill-refused` at a call.** As every host refusal under ADR-0029:
nothing is dispatched (for a gateway source, the gateway is not called), the guardrail names the
refusal, the model is told, and the run goes on. Order: both are step 12, after the library's own
part of step 11 and after `subject-mismatch` and `subject-unauthorized`.
`read-version-unsupported` and `read-malformed` are reported where steps 8 to 10 are, before the
library's own `out-of-scope`, as `admission-unknown` is (`principalBeforeScope`). None is retried,
and a `"2"` read is never resent at `"1"`.

**The restore at "2".** A continued delegated run restores the checkpoint's loaded set as ADR-0009
says, then asks a skill-load read for each skill it would restore, in the checkpoint's load order,
each read's `load.skills` naming those restored before it. A refused or unanswered one is dropped,
with the guardrail above, and left out of the set the restore writes. A restore claims no budget:
the loads counted before the resume stay counted (ADR-0009). The restore runs before the first
request, so its reads are made then.

**The contract.** `run-principal.json` gains the version `"2"` call read beside `admissionRead`,
`skillLoadRead`, `skillLoadAnswer`, the two answer forms of a body the host does not read, and four
names in `$defs/refusal`: `tool-version-refused`, `skill-refused`, `read-version-unsupported` and
`read-malformed`. Record versions do not move (the question still open at the end).

**Go and TypeScript in parity.** Both readers build every part of this record, from the same
contract, against the same vectors and the same spec copy, as ADR-0029's do.

## Considered and not taken

- **One map for the whole library, or one value per run.** The RFC fixes the version per issuing
  host, and `Keys` is already keyed by host; a per-run value would let two runs of one host differ.
- **The skill-load read through the admission port.** It would change the `"1"` call-read type in
  Go (`Call` would have to become optional) and make every port written for `"1"` handle a second
  kind.
- **The read before the budget claim.** It leaves a model free to cause one issuing-host read per
  `load_skill` call, without bound; and checking the budget without claiming it races a child's
  loads on the shared source rows.
- **Taking `version` from the gateway's `_meta`.** That is the source's answer at listing time, not
  the approval the person gave.

## Tests

- The shared conformance set: every case of tables 4 and 5 whose `applies` names the library, in Go
  and TypeScript, against an issuing-host test double, from a byte-for-byte copy
  `contract/testdata/principal/spec-admission-reads.json` listed in `spec-citation.json` with its
  SHA-256, checked before it is read (the copy's form is below): every case whose `applies` holds
  `sender` and whose `senders` names the library. A case whose sender part is "never emits" is
  passed as ruling 4 says.
- `"1"` byte for byte: `admission.json` unchanged; a run with no map and one with `"1"` hand the
  port identical reads; no skill-load read at `"1"` or in an undelegated run.
- The option: values other than `"1"` and `"2"` refused; a host not named is `"1"`; `"2"` with
  skills and no `SkillLoad` refused before any request.
- `version`: snapshot versions 3 and 4 read, by the gateway's vectors; the approved digest sent while
  the observation differs only in what drift already refuses; `null` for each case listed above.
- `skills`: ordered by code point (ids differing only in case), `[]` with none, a load and an unload
  just before a call reflected in its read.
- The loader: the six steps in order, including no read for a name not catalogued, an already
  loaded skill or a spent budget; a refused and an unanswered read leaving the checkpoint, the events
  and the loaded set unchanged and the budget claimed.
- The restore: two restored skills, the second refused (`skill-load-resume`).
- Answers and order: each new answer at `"2"` and at `"1"`; `read-malformed` before the library's
  own `out-of-scope`; `tool-version-refused` after it; exactly one read per refused call.
- Go and TypeScript agree on every vector, through the existing differential.

**Mutants planned** (four, each in a scratch copy): load before the read answers (the answer-lost
case fails); resend at `"1"` after `read-version-unsupported` (the one-read test fails); drop the
code-point sort of `skills` (the body bytes fail); take `version` from the current observation
rather than the approved snapshot (the version test fails).

## The shared conformance set

One file in the specification repository, `conformance/delegation/admission-reads.json`, beside
`es256-statements.json`, listed in that directory's README with its case count and SHA-256, holding
tables 4 and 5, one case per name, and the gateway record's case `read-v2-binding-step-12`. Each
case holds `name`, `table`, `kind`, `applies` (the side it holds, `issuing-host`, `sender` or both,
each with what that side does; ruling 4), `senders` (`["gateway"]`, `["library"]` or both; the
`skills` and skill-load cases are the library's), the sender's
configured version and the versions the host double reads, `inputs` (the chain by the name of a case
in `ed25519-statements.json`, `at`, `audience`, the call or the load, the approved `version`, the
loaded skills, `count`, and the double's policy where a case needs one), `body` (the exact `canon`
bytes as a string, or `null` where no read is made), `answer` (the exact answer bytes, or `null` for
none) and `expect` (the verdict, what is counted, that nothing is resent, and for table 5 what is
loaded). It is hand-written, needs no signer, and is frozen once both implementations pin it. The
library copies it unchanged and adds it to `spec-citation.json`, moving the citation's commit to the
one that adds it (the two files cited today keep their SHA-256); the gateway pins it by SHA-256 in
its test. Passed by both, it is the RFC's acceptance evidence for spec `v0.6.1`: two separately
written implementations, same vendor, not a cross-vendor claim.

## Not built here

- A version label in the read: §2a defines no member for it.
- The issuing host's side: its policy on `version`, `skills` and `null` is its own (RFC open
  question 25).
- Reads of a version after `"2"`, and any statement of the versions a host reads (RFC open question
  26).

## Rulings on the open questions

The maintainer's rulings, at acceptance:

1. **A version label.** It goes in the gateway's tools/list `_meta` only, never in `call.version`,
   and the RFC needs no amendment. The plan's earlier wording "label in `call.version`" meant the
   label beside the digest in `_meta`.
2. **The budget and refused loads.** A refused skill load counts against the skill budget, so a
   loop of refusals ends: each attempt that reaches the issuing host counts in
   `sourceFigures.skills`, refused or not, and no second budget is added.
3. **The child-link request's version.** It stays `"1"`, at either setting.
4. **Table 4's reader-side cases.** The malformed-body cases (`read-version-not-a-string`,
   `read-v1-with-skills`, `read-v2-skills-missing`, `read-v2-skills-unsorted`,
   `read-v2-skills-repeated`, `read-v2-call-and-load`, `read-body-over-cap`, and table 5's
   `skill-load-with-count`, `skill-load-names-itself` and `skill-load-at-v1`) are passed by both
   implementations as senders: they never emit such a body, and they refuse correctly when answered
   `read-malformed`. The conformance file's `applies` says so per case
   (`"sender": "never emits; refuses on read-malformed"`).
5. **A child's set.** A child names its own loaded skills on its own reads, as ADR-0009's amendment
   *Declared subagent skills* says: neither an ad hoc child nor a declared subagent inherits the
   parent's loaded set.
6. **The snapshot readers.** Accepting snapshot versions `"3"` and `"4"`
   (`go/context/descriptor_snapshot.go:329`, `packages/harness/descriptorSnapshot.ts:149`) is part of
   this build, so `call.version` is never always `null`; it is not a follow-up.

**Still open: record versions** (the former open question 4). No ruling was given. The four new
refusal names reach guardrail records only in runs at `"2"`. The record keeps its recommendation:
no record version moves, if the build confirms that a reader that predates the names refuses such a
record rather than misreading it; otherwise the event version moves, by ADR-0024.
