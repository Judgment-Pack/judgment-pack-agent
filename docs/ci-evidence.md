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
| Mutation: every full shard of a dispatch run on main at the commit | the `mutation` workflow's jobs, one per shard, and its `events` |
| Approval, provenance, the paired Go tag | unchanged jobs of `release.yml`, outside the gate; named in `fresh_at_release` |
| The runner and tools, resolved again on the release runner | every profile `compare: release-runner`, now with the reported identity |

Where the shared gate differs, it is stricter, with one exception named here. Stricter: the
workflow `name:` and the jobs' workflow name are bound; every job's labels and hosted-runner group;
the default branch is read from the API; every run at the commit on main is scanned whatever its
trigger (a newer failed nightly mutation run refuses); every `uses:` must be pinned. The exception
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
- Every profile (`base` for `contract`, `check` for the `check`, `go-race-a` and `go-race-b` jobs, `mutation` for
  the shards) is variant `linux-x64`,
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
`every row (shard N of 10)` since #281. Branch protection requires `DCO` and `contract`. Since #292
`go-race-a` and `go-race-b` are statuses as well, in place of the one `go-race` of #254.

**Pull-request-to-branch reuse.** The `check`, `go-race-a` and `go-race-b` jobs (one job of 45 minutes
on the run above, the race step 1,341 s and `unit-tests` 484 s of it, before #254) may each take from the same job
of the merged pull request's newest run the checks `pull_request_reuse.checks` lists, where that run
passed them. Tree equality alone never qualifies:
the gate binds the run to the one pull request of its head branch, merged into `main` as this
commit with no base change, checked out as a merge commit on this commit's first parent with this
commit's tree, with the same inputs, runtime, identity and bound environment. Every refusal is a
fresh run of that job's whole part of the list. On a pull request into `main` from this repository,
each job records what its scoped selection passed (`Record the pull request's checks`); that
record is never evidence for a release by itself. A fork's pull request records nothing, since the
gate never reuses one. On a push to `main`, `Plan reuse from the merged pull request`
decides before the checks, and the capture records each covered check as `REUSED`. At the release
the gate admits that pull request evidence again, under the same rules and the same 48 hours.

Listed as reusable, because each result depends only on the tree and the recorded inputs:
`record-shapes`, `dependency-audit`, `release-quality`, `mutation-rows`,
`release-quality-engine`, `reference`, `unit-tests`, the three certification suites,
`differential`, `gateway-receipt-process`, `gateway-receipts` (the Gateway checkout is pinned by
commit in `tests/fixtures/gateway-receipts/source.json`), `go-module-path`, `gofmt`, `go-vet`,
`go-generate`, `go-test`, `go-race-a`, `go-race-b`, `harness-check` and `parity` (the Desk checkout is pinned by
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
run by dispatch for a release.

`tests/test_release_evidence.py` holds every check of the two jobs to one of the two lists, so a
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

The full table runs in ten shards. When it ran on every push to `main`, two pushes in a row lost shard 4 of four
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

## The full table nightly and by dispatch (#299)

Ten shards on every push to `main` held most of the plan's runners for about an hour, so pull request
runs and main's CI queued behind them. On 2026-11-02 main's CI waited 30 minutes until two superseded
tables were cancelled by hand. So a push to `main` no longer runs the table. It runs nightly, at whatever
commit `main` names, and by dispatch. A pull request still runs its own rows and its sample, and every
push still checks that every row applies (`mutation-rows`).

**The trade-off.** A row that stops being caught and that no pull request's selection named is found that
night, not at the merge.

**How a release gets its table.** By dispatch at the exact commit ([the step](releasing.md#prepare-the-release)).
`workflows.mutation.events` in the configuration is `workflow_dispatch` alone, so only a dispatch run is
evidence. Without one the gate refuses with `run-missing`, naming the workflow and the commit, and the
`CI at the tag` job's next step prints the command to run. A dispatch cannot name an older commit, so a
candidate left behind by a newer merge cannot get a first table: that is why the step holds merges until
the run has started. A dispatch that has started keeps its commit, and can be re-run after `main` moves.

**What a nightly run at a candidate's commit does.** It lands there only if `main` has not moved. It
never replaces the candidate's evidence: a schedule is not a trusted event, the gate does not read its
jobs, and its jobs record nothing (the gate refuses a capture on it, too). It cannot cancel the candidate's
run: each trigger has its own concurrency group. It can hold or refuse the release, and it says so, under
the gate's unchanged rule:

| The nightly run, at the candidate's commit | The gate | What to do |
| --- | --- | --- |
| not finished, whenever it started | `run-unfinished`, naming it | wait, or cancel it if it started before the dispatch; then re-run the release's failed jobs |
| started before the dispatch, and failed or was cancelled | no effect: the dispatch run is newer | none |
| started after the dispatch, and passed | no effect: the evidence is still the dispatch run's | none |
| started after the dispatch, and failed or was cancelled | `run-newer-failed`, naming it and its event | read it. A real fault is fixed on `main`, in a new version. Otherwise re-run its failed jobs, or re-run all jobs of the dispatch run, which makes that the newest attempt; then re-run the release's failed jobs |

So do not cancel a nightly run that started after the candidate's dispatch: that refuses the release as a
failure would. A hosted schedule starts late, too: the last two runs of this cron's hour and minute started
6 and 8 hours after it, so its hour does not say when it will land.

**What a nightly failure does.** It does not stop anything: no merge waits on it, and it reaches a release
only as the table above says. The `report a failed nightly table` job opens one issue titled
`The nightly mutation table did not pass`, or comments on it if it is open, naming the commit, the run and
the shards. That notifies whoever watches the repository. GitHub also tells the person who last changed
the cron, if their settings allow. A shard stopped at its limit is reported, although the API says
`cancelled` for it: the job tells it from a run cancelled by hand by its time, and a run cancelled by hand
reports nothing. The job is skipped on every other event, and the gate requires that (`skipped_jobs`).

**Other commits.** A nightly or a dispatch at another commit is in another concurrency group and does not
touch a candidate's table. Nothing re-runs a run someone cancelled.

## The race step on its own runner (#254)

This section is the record of #254, when there was one step and one job; [the next](#the-two-race-jobs-292)
divides them.

The `go-race` step was about half of one job's time: 771 to 2,272 s of a `check` job of 1,553 to
3,883 s, on the main and pull request runs from 2026-10-30 to 2026-11-01. The same work varies by a
factor of about 2.7 between hosted runners. Its time is its largest binary's: a local run on four CPUs
took 1,738 s, and `./adapter` alone 1,691 s of it, with the other packages done by about 700 s. On
2026-11-01 a pull request's run reached the binary's own limit: `go-race` 2,477 s, `./adapter`
stopped by `panic: test timed out after 40m0s` while it was making progress. A longer job could not
have helped, and a longer limit would hide this. A CPU profile of one of the slowest tests shows race
instrumentation and the writer's checkpoint save per event, spread over every run of a real adapter:
no fixture setup repeats that could be shared without giving up the separate runs.

Two changes, neither of which changes a command or a test:

- **The job.** `dev.json`'s `go-race` step runs in a `go-race` job. The job selects by skipping every
  other step, a list it derives from the manifest, because `check.sh` refuses `--changed` with
  `--only`; the `check` job skips `go-race`. Together they run each selected check once. The job uses
  the `check` profile, with its setup steps, and needs neither the Desk nor the Gateway checkout, so
  `JPDEV_DESK` and `JP_AGENT_GATEWAY_SOURCE` are unset in its bound environment on both sides of a
  reuse. No Go test reads either. It needs no build: the scripted server loads the TypeScript through
  Vite's module runner. Its evidence is its own artifact, `ci-evidence-ci-go-race-<attempt>`; its plan,
  its capture and the release's readmission are the job's own, under the same rules.
- **The test binaries.** The heaviest clusters are test packages of their own, so that each has its
  own process and limit and `go test` runs them side by side. `./adapter/...` already selects them:
  `plantests` (the prepared-plan, enrollment and native Plan tests), `capturetests` (live capture,
  usage budgets and the native routes that use their fixtures), `reauthtests` (re-authorization, waits
  and runtime actions), `principaltests` (the delegated runs, and the heaviest re-authorization test),
  `durableapprovaltests`, `durablewritetests` and `durablewiretests` (durable writes), and
  `enrollmenttests` (runs under a host that enrolls its offers, #289; its rows run in parallel), and
  `framingtests` (what a model is shown of a source's text, #302). A test
  binary cannot import another's test files, so where a cluster divides a family its helpers are
  copied, and say so.

So that a binary's time is seen before it reaches its limit, the job prints what each binary took,
on a failure too (`check.sh`'s summary keeps one line of the step's output), and warns past three
quarters of the 40-minute limit. That warning is the sign that the next split is due.

A required check does not change: branch protection requires `DCO` and `contract`. The release
required `go-race` by its job name in `.github/ci-evidence.json`, as it required the step inside
`check`. Hosted times per binary are printed by the job, and are in the pull requests of #254.

## The two race jobs (#292)

After #254 each test binary has room on a slow runner, but the step did not. On the hosted run of #291
it took 1,657 s, and the same work on the slowest runner seen comes to 91 to 96 percent of the 3,000 s
limit in `dev.json`, with more tests still to land. Moving a test to another binary does not reduce the
total, and the step takes about a quarter of the total on four CPUs. A stop at that limit fails closed
and no evidence is false, but it costs an hour on a release candidate. So the one step is two, each on a
runner of its own. No limit was raised and no test was dropped: both steps are the same `go test` flags,
under the same 40-minute limit per binary and the same 3,000 s per step.

- **The two steps.** `go-race-a` names its packages, `./gate` among them. `go-race-b` asks `go list ./context/... ./adapter/...`
  and takes every package `go-race-a` does not name, so a new package runs, in `go-race-b`, before anyone
  has placed it. `tests/test_release_evidence.py` runs both steps' own text with a stand-in `go`, over this
  tree and over this tree with a package added: together they are the packages of the one pattern, each
  once. A package `go-race-a` names that has moved stops that step, and a `go list` that fails part of the
  way stops `go-race-b` rather than letting it test what it got.
- **The division.** By each binary's seconds on the hosted run of #291:

  | `go-race-a` | s | `go-race-b` | s |
  | --- | ---: | --- | ---: |
  | `./adapter/delegate` | 824 | `./adapter/durablewiretests` | 646 |
  | `./adapter/reauthtests` | 579 | `./adapter/eino` | 631 |
  | `./adapter/durableapprovaltests` | 566 | `./adapter/durablewritetests` | 488 |
  | `./adapter/principaltests` | 540 | `./context` | 486 |
  | `./adapter/plantests` | 394 | `./adapter/capturetests` | 371 |
  | `./adapter/agent` | 112 | `./adapter`, `./adapter/wire`, `./adapter/turn` | 96 |
  | total | 3,015 | total | 2,718 |

  Two more went to `go-race-a` after that, as the leg with more room. `./adapter/enrollmenttests` (#298) came out
  of `./adapter/delegate`, whose race run had grown past the binary's 40-minute limit on a slow runner; each of its
  rows is whole runs through the writer, and none was dropped. Under race it takes 186 s on 16 CPUs and 342 s at
  `GOMAXPROCS=4`, the hosted runner's size. `./gate` (2 s, #302) was in neither leg: it is outside the pattern
  `go-race-b` lists, so it is named here and the exclusion does not need it.

  `go-race-b` is the lighter on purpose: a new package lands there and `./context` is there, so the
  tests still to come add to that side first. To move a binary to `go-race-a`, name it in that step and in
  `go-race-b`'s exclusion; a name in only one of the two fails the test. The job's warning below says when
  that is due.
- **The jobs.** `go-race` in `ci.yml` is one matrix job with two legs, `go-race-a` and `go-race-b`, as the
  mutation table is one job with ten shards. In `.github/ci-evidence.json` each leg is a job of its own,
  with one check; each has its own status, its own artifact (`ci-evidence-ci-go-race-a-<attempt>`,
  `ci-evidence-ci-go-race-b-<attempt>`), its own plan, capture and readmission, under the same rules as
  #254's one job. Neither cancels the other on a failure. A leg skips every other step of the manifest, the
  other leg's among them, and the `check` job skips both.
- **What a release requires.** Both. The gate holds a candidate's job names to the configuration's, so a
  run without either leg refuses (`job-coverage`), as does a run with the one old `go-race` job; without
  either artifact, `artifact-coverage`; without either result, `check-coverage` or `check-result`. A leg's
  evidence holding the other's step refuses too. A merged pull request's run from before the division has
  neither leg, so on main both steps run fresh (`reuse-job`), and the `check` job's reuse is unaffected.
- **The warning.** As well as the warning for a binary, each job warns when its step passes three quarters
  of the 3,000 s, 2,250 s: the sign that a binary should move to the other step, or that a third is due.

The cost is one more job: its setup, and a second race compile, which the two steps no longer share. The
hosted times of both jobs and the margin they leave are in the pull request of #292.

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
   commit, and of the mutation table's dispatch run when its evidence refused
   (`gh run rerun <run id> --repo Judgment-Pack/judgment-pack-agent`; a failed-jobs-only re-run
   leaves the other jobs' evidence in an earlier attempt and refuses). This works after main moved
   on, and is the route for a rolled image (`runtime-identity`) and for `run-age`. When main still
   names the commit, a dispatch of `ci.yml` or `mutation.yml` on `main` is another. A re-run of a nightly
   run is not: it stays a schedule run. A re-run or a dispatch never reuses a pull request's checks: it
   runs the whole list.
2. When that run has completed, re-run the release run's failed jobs once.
3. If it refuses again, or the evidence cannot be made at that commit, stop: fix the cause on main
   and release a new version from a validated commit under the tag policy.

`run-unfinished` usually means the tag was pushed while main CI, the mutation table or its nightly
run at the commit was still running: wait for it, then step 2. `run-missing` for `mutation.yml` means no
table was dispatched at the commit: [the table's section](#the-full-table-nightly-and-by-dispatch-299).

## Limits

- **Not live-tested.** The pull request capture first runs on the pull request that made this
  change; the main capture of the identity and the bound environment, and the first reuse plan,
  on the next push to `main` after it merges (only a pull request run that recorded evidence with
  this version of `ci.yml` can be reused). The GitHub API reads behind the reuse binding were
  exercised only against fixtures, here and in the kit. The first live reuse decisions should be
  watched.
- **No savings claimed.** The removed repetition was measured above as seconds; the possible saving
  is reuse, and it is measured on this repository's own runs after the change, not inferred.
- **The `go-race-a` and `go-race-b` jobs.** Their evidence first runs on the pull request of #292, and
  their first plan and capture on main on the next push after it merges. The one job's were live on main
  from #254; the two legs' were exercised only against fixtures until then. The division's seconds are one
  hosted run's, and what a step takes on a slow runner is an estimate from the speed of a reference binary
  on other runs, not a run on that runner.
- **The nightly table.** `report a failed nightly table` first runs on the first failed nightly after
  #299 merges; its issue writes were exercised only against a stand-in `gh`. How the API reports a shard at
  its limit and a hand cancel is from live runs. How a concurrency group treats a pending run, and GitHub's
  own notice of a failed schedule, are from GitHub's documentation, not tried here. When a nightly starts is
  from two readings of the weekly cron.
- **Event names.** A future base-change event whose name contains neither `base_change` nor
  `base_ref` would not be refused (the kit's stated limit).
- **Fixtures.** `tests/test_release_evidence.py` runs the vendored gate over this repository's own
  configuration and workflow files at a fixture commit, with handwritten Actions API answers. It is
  not evidence that a remote workflow ran. The kit's own suite tests the gate's contract.
