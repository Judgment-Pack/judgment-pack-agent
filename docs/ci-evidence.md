# Trusted CI evidence at release

Devkit issue #37 defines the shared `lib/ci_evidence.py` command and handwritten
collector/CLI fixtures. Repository adoption owns its configuration and workflows;
Agent issue #213 vendors the identical helper as `scripts/dev/ci_evidence.py`.
This is a new release-admission CLI and JSON evidence contract (schema 1).

## Identity and collection

`capture --workflow KEY --job KEY [--check-log FILE] [--setup-log FILE] --output FILE`
runs after successful checks on trusted main. The CI workflow uploads that one
`evidence.json` with a pinned immutable artifact action and a name containing the
workflow key, producer job key (including matrix leg) and run attempt. Capture is
not an admission command. A local file or a caller's successful assertion cannot
establish reusable evidence.

`verify` reads only this repository's Actions API, through the supported `gh api`
route with `actions: read` and `contents: read`. There is no input for a local
evidence file, an alternate API host, a green flag or an age override. Expected
repository name and numeric ID come from the release's GitHub context. Source is
its exact `GITHUB_SHA`; HEAD must match and tracked files must be unchanged.
Configuration and input hashes come independently from `git show` at that commit,
never from an artifact. This includes the workflow definitions, helper, check
runner, manifest and configured dependency/parity inputs. The workflow's git blob
identity is also compared. Tree equivalence and a different commit do not qualify.

The numeric workflow ID is resolved independently by the expected workflow path.
Run metadata must bind that ID/path, this repository and head repository (names
and numeric IDs), exact source, `main`, an allowed main event, and no PR association.
Only a complete successful attempt qualifies. GitHub job display names are not
immutable workflow identities: the source-controlled map binds each unique name
to a producer job key, and the verified workflow bytes bind that map to the actual
commands. Producer artifacts carry the key and attempt. Duplicate or unexpected
API jobs fail, as do missing jobs or matrix legs. Only explicitly configured
PR-only jobs may be skipped. Check-runner summaries must cover every configured
check, with no failures/not-run rows or unexpected skips. Agent permits only its
existing optional `dependency-audit-live` skip; this never establishes advisory
clearance. Parity and setup checks are required.

The collector reads all pages (100 entries per page, at most 10 pages) and requires
stable exact totals and unique IDs. It refuses counts of 1,000 or more because
GitHub caps filtered workflow-run searches at 1,000. It does not filter by status.
Among applicable main events, the greatest `(created_at, run_id)` selects the run;
that run's current `run_attempt` selects its attempt. A later failed, canceled,
queued or running applicable run blocks reuse. An older run's rerun does not move
it past a newer-created applicable run. No latest-green search occurs.

Jobs are fetched from the attempt-specific endpoint. A failed-jobs-only rerun
with incomplete attempt coverage is refused; jobs/artifacts from different
attempts are never combined. An artifact must have exactly the expected name,
producer interval, run/source/ref metadata, nonexpired state, bounded size, and
API-reported SHA-256 of the downloaded ZIP. The ZIP must contain exactly one
bounded `evidence.json` entry. Archive files are never extracted or executed.
Malformed, missing, inaccessible, expired and partial evidence refuse admission.

## Execution inputs and mutable state

The same source alone does not establish equivalent execution. Each artifact
records the actual GitHub-hosted image (`ImageOS`, `ImageVersion`), OS release
hash, platform/architecture and configured tool-version commands. Release resolves
these independently on its current runner; a changed image or tool resolution
refuses reuse. Devkit records bash, git, Python, Go, shellcheck and rsync. Agent's
baseline contract job is bound to its hosted image; its check job additionally
records Node/npm/Go, the actual Python virtual-environment interpreter and installed
Python distribution versions. The release runs the existing Python dependency
setup to resolve that inventory independently. Mutation records its Node/npm
resolution, all four full shards and `every row applies`; the PR sample must be
skipped. Agent's manifest and mutation workflow currently both resolve Node 22;
a future intentional difference needs an explicit separate release resolution.

Dependencies remain bound to source lockfiles/requirements and their existing
installer behavior; this is not a new package provenance system. Python inventory
compares resolved distribution versions, not every installed file's bytes. No
local cache is accepted as proof. Existing setup/download integrity and trusted
hosted-runner assumptions remain.

The initial age cap is 48 hours, measured from the selected attempt's start and
held in source configuration. Future-started runs refuse. This conservative cap
and image/tool equality can require another full main run, so reuse is not a
promise of lower duration. The collector rechecks the selected attempt and latest
run after artifacts, then every workflow again after all collection. The annotated
release tag must still peel to the release commit before/after, and its tag object
must be unchanged across collection. API reads are not an atomic transaction: a
run/tag can change after the final read. Existing tag checks before draft and
publish remain, and protected approval is still required.

Fresh advisory review, current tag/main/protection decisions, actual-tag upgrade
admission, version/template-copy checks, package execution/build, archive/checksum
and attestation checks, protected approval, exact draft/published asset identity,
and Agent's paired Go tag remain under their existing gates. The evidence gate
neither grants an advisory exception nor changes live-quality requirements.

## Bounded recovery and operation

If reuse is refused, stop that release attempt. A maintainer may explicitly
trigger **one full fresh CI run on main** using its `workflow_dispatch` entry, and
Agent's full mutation workflow on main, then wait outside the release workflow
and retry verification once. Main must still name the exact intended release
commit. A newer failure/running attempt must be resolved; do not select an older
green. If main has advanced, or inputs remain incompatible, prepare a new release
from a validated main commit under the existing tag policy. A dispatch on a tag,
PR or another repository is ineligible. Unavailable evidence never becomes success.
No workflow automatically dispatches/retries, and no automatic full CI is disabled.

The unchanged `lint`/`tests` and `contract`/`check` job names retain branch-protection
statuses. Main/PR CI still runs full deterministic checks. Release now verifies
that the required full deterministic work was completed at the exact source and
compatible inputs, rather than executing those equivalent checks again. Review,
exact-head CI and publication approval requirements remain.

Logs include the selected workflow ID/path, run/attempt/state, source/input hashes,
job IDs/results/coverage/runtime identities and the reuse/refusal decision. Only
repository-owned JSON metadata and tool versions are logged; credentials and raw
command stderr are not printed. Each external command has a 45-second timeout and
an output-size admission bound, spooled under `TMPDIR`; the release job also has
a 15-minute bound. API request failures do not fall back to a cache.

## Verification and measurement limits

Run `python3 tests/ci_evidence_test.py` in Devkit or
`python3 -m unittest discover -s tests -p test_ci_evidence.py` in Agent for the
focused handwritten fixtures. They exercise the collector and public capture/
verify commands, including pagination, failed/running/canceled latest runs,
partial reruns, matrix coverage, artifact/input identities and tag movement.
The API fixture replaces a live dependency; these tests are not evidence that a
remote candidate workflow ran. Intended safeguard removals belong in isolated
scratch, never the candidate.

Observed historical Agent runs at `e00d2b11d11af4dabb0a4de6c1a5e7322bd8527e`:
main run `40147506913` executed `contract` in 5 seconds and `check` in 1,291 seconds;
release run `40152129767` repeated them in 6 and 1,687 seconds. Devkit main run
`39962337298` at `d0c0a0152b8a601bde80b0b5a4f9d3bd481edc1f` executed `lint` in
9 seconds and `tests` in 72 seconds. These are actual API job start/end intervals,
not summed elapsed release time or a prediction of candidate savings. Before,
release called the full CI workflow; after, it executes the collector and current
input resolution while main still executes every original job. Candidate remote
job inventory/duration and release behavior must be observed after exact-head CI
and a future authorized release. No in-flight release is changed by this work.

GitHub's primary API contracts:
[workflow runs](https://docs.github.com/en/rest/actions/workflow-runs),
[attempt-specific jobs](https://docs.github.com/en/rest/actions/workflow-jobs),
[artifact metadata/downloads](https://docs.github.com/en/rest/actions/artifacts).
