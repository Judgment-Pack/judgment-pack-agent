# Trusted CI evidence at release

A release takes the evidence main CI and the full mutation table already produced at the exact
release commit, instead of running them again, and refuses when that evidence is missing, partial,
stale, unreadable or different. The gate is the devkit's
[release evidence gate](https://github.com/Judgment-Pack/judgment-pack-devkit/blob/v0.14.0/docs/ci-evidence.md),
vendored unchanged from devkit v0.14.0 as `scripts/dev/release_evidence.py` (line 2 names the kit
file and release), with this repository's configuration in
[`.github/ci-evidence.json`](../.github/ci-evidence.json), schema 2. The kit's document is the
contract; this one records what the agent configures, why, and what changed.

History: agent #213 adopted an early form of the gate as `scripts/dev/ci_evidence.py` (schema 1),
which the agent kept as its own verifier. Agent #239 and #240 move the agent to the shared gate at
v0.13.0, adopting its reported runner identity
([devkit #59](https://github.com/Judgment-Pack/judgment-pack-devkit/issues/59)) and its
pull-request-to-branch reuse ([devkit #60](https://github.com/Judgment-Pack/judgment-pack-devkit/issues/60)).
Devkit v0.14.0 follows: at v0.13.0 every live plan on `main` decided
`fresh: reuse-unavailable: KeyError`, because the kit's pinned API version no longer returns
`merge_commit_sha`, so every check ran. That failed closed. v0.14.0 binds the merge to the pull
request timeline's `merged` event
([devkit #70](https://github.com/Judgment-Pack/judgment-pack-devkit/issues/70)).
`scripts/dev/ci_evidence.py` is removed.

## Why the shared gate, not a port

The alternative was to port the reported identity and the reuse rules into the agent's own
verifier. That would be a second copy of about six hundred lines of binding logic (the identity
manifest, the actions reader, the merged pull request's binding, the plan, the readmission), to be
reviewed again here and kept in step with every kit release by hand: the duplication #240 is about.
The shared gate is a superset of what the agent's verifier checked, and every guarantee of the
agent's gate is held either by the gate's own contract or by this repository's configuration:

| Guarantee of the agent's gate | Held by |
| --- | --- |
| Exact source: this repository, the release commit, no tree-equivalent commit | the gate's contract (`run-sha`, `evidence-identity`, `input-identity`); inputs are git object ids at the commit |
| Latest attempt, never an older green one | the gate's selection (`run-newer-failed`, `run-unfinished`, `run-race`, `attempt-changed`, `latest-changed`, `final-latest-changed`) |
| Coverage: every job and matrix leg, every check, the PR-only job skipped | `workflows.*.jobs`, `checks`, `setup`, `skipped_jobs` |
| Freshness: 48 hours from the selected attempt's start | `max_age_hours: 48`; the reused pull request run is held to it too |
| Advisory: only `dependency-audit-live` may be skipped, which proves nothing about advisories | `optional_skips`; the live query stays fresh (below) |
| Mutation: every full shard of a main run at the commit | the `mutation` workflow's jobs, one per shard |
| Approval, provenance, the paired Go tag | unchanged jobs of `release.yml`, outside the gate; named in `fresh_at_release` |
| The runner and tools, resolved again on the release runner | every profile `compare: release-runner`, now with the reported identity |

Where the shared gate differs, it is stricter, with one exception named here. Stricter: the
workflow `name:` and the jobs' workflow name are bound; every job's labels and hosted-runner group;
the default branch is read from the API; every run at the commit on main is scanned whatever its
trigger (a newer failed scheduled mutation run refuses); every `uses:` must be pinned. The exception
is the selection rule: the agent's verifier selected the run created last, the gate selects the run
whose latest attempt started last. A re-run of an older run is therefore the newest attempt, and an
earlier failed run does not block it; any run whose attempt started after the selected one and did
not succeed still refuses. This is the shared contract's definition of the newest attempt, and it
is what makes its fresh route (re-run all jobs of the main run) work.

The v0.11.0 runner image exception is not carried over: the configuration had already dropped it,
the gate has no such member, and the
[approval record](security/v011-runner-image-exception.md) stays as the history of v0.11.0.

## Runner identity (#239)

The organisation uses standard GitHub-hosted runners (the maintainer's decision on devkit #59).
A label such as `ubuntu-latest` is an address that GitHub moves to newer images; a standard runner
cannot be pinned to an image version. Every job therefore records the image identity its runner
**reports**, and the gate compares those reports. This is a reported image identity, **not a
pinned VM image**: nothing here chooses, holds or attests the machine a job runs on.

- `.github/runner-identity.json` is the kit's manifest, revision 1, vendored unchanged and pinned
  by SHA-256 in the configuration's `identity` member.
- Every profile (`base` for `contract`, `check`, `mutation` for the shards) is variant `linux-x64`,
  pins `ImageOS` `ubuntu26` and records its tools. Every profile compares with the release runner:
  the release's `CI at the tag` job sets up Node, Go and Python from `dev.json` with the same pinned
  actions the CI jobs use, makes the Python environment, and must resolve exactly what each job
  reported (image version, OS-release digest and every tool).
- Every `uses:` of `ci.yml`, `mutation.yml` and `release.yml` is pinned to a full commit, and the
  producers and the consumer use the same pin of each action.
- What the identity does not cover, as the kit states: the inner `uses:` of remote composite actions
  and reusable workflows, and container or service images. The agent's workflows use none of the
  latter.

When the hosted image rolls between the CI run and the release, the release refuses
(`runtime-identity`): the agent does not opt in to revalidation, because its `check` job does work
outside the check list (the Desk and Gateway checkouts) and its mutation shards are not the check
list. The fresh route below applies. When `ubuntu-latest` moves to another OS release, capture
refuses on the next main run (`runner-platform`) and the `ImageOS` pin moves in a reviewed change.
GitHub patches and retires hosted images; nothing here freezes one.

## Removed repetition and pull-request reuse (#240)

The inventory of #240, and what this change does with each item. Timings are job and check
durations reported by the API and by `check.sh`'s summary on main run 41088263343 (CI) and
41088263379 (mutation table), at `86ce27ab82fd05fcacc4252c84f07c88dd599285`, before this change.
No timing after it has been measured.

| Repeated work | Before | After |
| --- | --- | --- |
| `contract`, `local-paths`, `agent-docs`, `diff-check` | run by the `contract` job and again by `check` (under 1 s each in `check`) | `contract` runs them on every event; `check` skips them (`--skip`), and its configured checks no longer list them |
| the rows-applicability check | `dev.json`'s `mutation-rows` in `check` (under 1 s) and the mutation workflow's `every row applies` job (7 s) | `mutation-rows` in `check` only, which also covers the row that names `docs/reference/providers.md`; the `every row applies` job is removed |
| the offline `dependency-audit` test | in `check` and again in the audit workflow | in `check` only; the audit workflow runs the live query alone, fresh |

The status names `contract`, `check` and `dependency-audit` are unchanged. The shards' are
`every row (shard N of 10)` since #281. Branch protection requires `DCO` and `contract`.

**Pull-request-to-branch reuse.** The `check` job (45 minutes on the run above, `go-race` 1,341 s
and `unit-tests` 484 s of it) may take from the merged pull request's newest run the checks
`pull_request_reuse.checks` lists, where that run passed them. Tree equality alone never qualifies:
the gate binds the run to the one pull request of its head branch, merged into `main` as this
commit with no base change, checked out as a merge commit on this commit's first parent with this
commit's tree, with the same inputs, runtime, identity and bound environment. Every refusal is a
fresh run of the whole rest of the list. On a pull request into `main` from this repository, the
`check` job records what its scoped selection passed (`Record the pull request's checks`); that
record is never evidence for a release by itself. A fork's pull request records nothing, since the
gate never reuses one. On a push to `main`, `Plan reuse from the merged pull request`
decides before the checks, and the capture records each covered check as `REUSED`. At the release
the gate admits that pull request evidence again, under the same rules and the same 48 hours.

Listed as reusable, because each result depends only on the tree and the recorded inputs:
`record-shapes`, `dependency-audit`, `release-quality`, `mutation-rows`,
`release-quality-engine`, `reference`, `unit-tests`, the three certification suites,
`differential`, `gateway-receipt-process`, `gateway-receipts` (the Gateway checkout is pinned by
commit in `tests/fixtures/gateway-receipts/source.json`), `go-module-path`, `gofmt`, `go-vet`,
`go-generate`, `go-test`, `go-race`, `harness-check` and `parity` (the Desk checkout is pinned by
commit in `scripts/parity.sh`).

Not reusable, run on every branch push:

| Check | Why |
| --- | --- |
| `python-tests` | reads the checkout's release tags (`scripts/check-upgrade-guides.py`) |
| `generated-docs` | a generator that reads Go module metadata |
| `build` | its output is read by later checks of the job |
| `packed-smoke` | packs and installs the package: a packaged-consumer check |
| `dependency-audit-live` | a live advisory query, kept fresh (and optional on main) |

The four `contract` steps run in their own job on every event and plan no reuse. The mutation
table plans none: a pull request's mutation sample is not shard coverage, and the full shards
run on every push to `main`.

`tests/test_release_evidence.py` holds every check of the `check` job to one of the two lists, so a
new `dev.json` check must be classified before CI passes. It also scans every tracked source under
`scripts/`, `tests/`, `packages/` and `go/`, and `dev.json`'s commands, for the event, the ref, the
commit and the run: the `GITHUB_*` names (`GITHUB_EVENT_NAME`, `GITHUB_REF`, `GITHUB_HEAD_REF`,
`GITHUB_BASE_REF`, `GITHUB_SHA`, `GITHUB_WORKFLOW_REF`, `GITHUB_RUN_ID` and the like) as bare words
in any spelling, `github.event` and the other expression contexts, and reads of the Checks step's
`EVENT`, `BASE_REF` and `COVERED` (`$EVENT`, `${EVENT:-}`, `process.env.X`, `process.env['X']`,
`os.getenv`, `os.environ`, `os.Getenv`, `os.LookupEnv`). Only the gate and the test itself are
exempt. A reusable check must not branch on the event or on which ref or commit it runs at. What a
static scan cannot see (a name assembled at run time, an environment read through a library) is
outside it.

**The bound environment.** `JPDEV_BIND_ENV`, set once for `ci.yml`, must equal
`pull_request_reuse.environment`. The test derives that list: it is every variable the checks'
sources read (the same spellings as above, in every tracked source but the kit's runner scripts and
the Python test modules of the fresh `python-tests` check), plus the toolchain flags `GOFLAGS`,
`GOEXPERIMENT`, `GORACE`, `GODEBUG`, `CGO_ENABLED`, `GOTOOLCHAIN`, `GOPROXY`, `GOSUMDB`,
`NODE_OPTIONS`, `NODE_ENV` and `TZ`, minus named exemptions. Today that binds `CI`, `JPDEV_DESK`,
`JP_AGENT_GATEWAY_SOURCE`, `NODE`, `DIFF_SEED`, `DIFF_CASE`, the `JPACK_DIALECT_*` variables,
`JP_RUNTIME_BINARY`, `JP_RUNTIME_BINARY_SHA256`, `JP_AGENT_GATEWAY_FIXTURE`, `JP_CORPUS_ROOT`,
`JPS_CHAT_CONTEXT_STORE`, `JPS_CHAT_CONTEXT_ID`, `JP_CHECKPOINT_GUARD_ROOT`, `JP_REASONING_GUARD_ROOT`,
`BODY_PARSER_PROBE_ROOT` and `UNDICI_PROBE_ROOT`, with the toolchain flags. The exemptions, each with
its reason in the test: `TMPDIR` and `JPDEV_SCRATCH` (per-run scratch directories), `PATH` (where
the recorded tools are), `GITHUB_STEP_SUMMARY` (the live audit only), `LANGSMITH_API_KEY` (a secret,
never bound), and the variables a script or test sets for its own child process. `check.sh` prints
the SHA-256 of each bound value (`unset` when absent), and of the manifest it read, on its
`environment:` line; the capture records them and the gate compares the pull request's with the
branch's. The workflow sets no bound variable except the two checkout paths, which are the same on
every hosted run (also a test).

**Main-to-release reuse** (#213) is unchanged in kind: the release takes the exact commit's main
evidence, now under the gate's contract.

## The mutation table's shards (#281)

The full table runs on every push to `main` in ten shards. Two pushes in a row lost shard 4 of four
to its 120-minute limit, so no commit had the evidence the release requires. A shard's job limit stays
120 minutes and every row still runs.

What decides a shard's time, over the rows of `2107a63` (209 rows measured, and most others estimated
from a measured row with the same test files):

- **The rows' cost is concentrated, and every-n-th spreads it evenly enough.** A row costs from
  seconds to many minutes: 18 percent of the rows carry 72 percent of the cost. Yet the four old
  shards were within 11 percent of their mean by cost. So the difference between shards on hosted runs
  (shard 2 took 47 to 92 minutes across runs) is mostly runner speed, which no division of the rows can
  balance. A table balanced by measured cost would need a costs file kept up to date for a gain of
  about 10 percent, so the shards stay every n-th row.
- **The total crossed the limit.** The 48 rows of #272 added about 20 minutes to a shard already at
  about 100 on a slow runner. The number of shards is the remedy.

The number is this repository's, not the kit's: the gate reads the jobs from `.github/ci-evidence.json`
and `release.yml` does not name them. To change it, change the matrix, the job's name and `--shard` in
`.github/workflows/mutation.yml`, the `full-N` jobs in the configuration and `SHARDS` in
`tests/test_release_evidence.py`, together: a run and its evidence are of one commit, so an older run
at another number is never read for a newer commit. A shard warns past three quarters of its limit, and
a run prints each row's seconds as it ends (`mutation-row`), so a shard stopped at its limit still says
what it ran.

## The release

The `CI at the tag` job runs `scripts/dev/release_evidence.py verify`, writes the decision record to
its log and keeps it as the run's `release-evidence-<attempt>` artifact, and sets the `decision`
output. The `Package` job, which every publishing job needs, begins by requiring that decision to
be `reuse`. Tag admission, release notes, upgrade guidance, the environment reviewer, the package
version, packing, checksums, attestation, the draft and published identities and the paired
`go/<tag>` tag stay fresh in `release.yml`, as before; the configuration names them in
`fresh_at_release`. So does the live dependency advisory review of `docs/releasing.md`.

## The fresh route

A refusal stops that release run. The operator reads the refusal and chooses; no workflow
dispatches or re-runs anything automatically.

1. Make fresh evidence at the same commit, once: re-run **all** jobs of the main CI run at the
   commit, and of the main mutation run when its evidence refused
   (`gh run rerun <run id> --repo Judgment-Pack/judgment-pack-agent`; a failed-jobs-only re-run
   leaves the other jobs' evidence in an earlier attempt and refuses). This works after main moved
   on, and is the route for a rolled image (`runtime-identity`) and for `run-age`. When main still
   names the commit, a dispatch of `ci.yml` or `mutation.yml` on `main` is another. A re-run or a
   dispatch never reuses a pull request's checks: it runs the whole list.
2. When that run has completed, re-run the release run's failed jobs once.
3. If it refuses again, or the evidence cannot be made at that commit, stop: fix the cause on main
   and release a new version from a validated commit under the tag policy.

`run-unfinished` usually means the tag was pushed while main CI, the mutation table or its weekly
scheduled run at the commit was still running: wait for it, then step 2.

## Limits

- **Not live-tested.** The pull request capture first runs on the pull request that made this
  change; the main capture of the identity and the bound environment, and the first reuse plan,
  on the next push to `main` after it merges (only a pull request run that recorded evidence with
  this version of `ci.yml` can be reused). The GitHub API reads behind the reuse binding were
  exercised only against fixtures, here and in the kit. The first live reuse decisions should be
  watched.
- **No savings claimed.** The removed repetition was measured above as seconds; the possible saving
  is reuse, and it is measured on this repository's own runs after the change, not inferred.
- **Event names.** A future base-change event whose name contains neither `base_change` nor
  `base_ref` would not be refused (the kit's stated limit).
- **Fixtures.** `tests/test_release_evidence.py` runs the vendored gate over this repository's own
  configuration and workflow files at a fixture commit, with handwritten Actions API answers. It is
  not evidence that a remote workflow ran. The kit's own suite tests the gate's contract.
