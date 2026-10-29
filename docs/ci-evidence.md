# Trusted CI evidence at release

Agent issue #213 originally adopted the historical Devkit issue #37
`lib/ci_evidence.py` helper and handwritten collector/CLI fixtures. Agent now owns
`scripts/dev/ci_evidence.py`, its configuration and workflows. Current Devkit has
removed that legacy helper and uses a different release-evidence implementation;
this document does not claim current shared code or migrate Agent to that contract.
The Agent release-admission CLI and JSON evidence contract use schema 1.

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
refuses reuse, except for the explicit v0.11.0 image policy below. Agent's
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

## Approved v0.11.0 image exception

The [maintainer approval](security/v011-runner-image-exception.md) permits only
ImageVersion 20261023.163.3 and 20261026.180.1 for Agent repository ID 1424951648,
exact tag `v0.11.0`. Both producer and independently resolved verifier must be in
that pair and exactly match ubuntu26/Linux/X64 and the approved os_release hash.
Configured profile tools remain exact. This accepts unknown host library, kernel
and preinstalled-tool differences; it does not prove cross-image equivalence or
claim a product behavior change or live validation.

The source config binds the concrete approval record's SHA-256; independent
release-source inputs must include that same hash. Scope fields cannot be widened
through config. Missing/extra/malformed scope, wrong repository/tag, either runner
outside the approved tuple/pair, any tool mismatch or other payload difference
refuses. Raw artifacts are not normalized. The verifier reports raw producer and
verifier runtimes per job, per-job `image_version_exception_used`, and top-level
`runner_image_exception` scope/approval plus `used` (whether any image differed).
Capture continues to record actual runtime unchanged.

The verifier reads its real UTC clock at entry and again after collection and all
run/tag rechecks. It must finish before 2026-11-07 00:00:00 UTC; equality and a
collection crossing that instant refuse, regardless of an earlier run-age clock.
The configured exception refuses after expiry and on any other release, even if
images match. Configurations without the exception retain exact legacy matching
and output. Removing or changing this policy requires a reviewed source change;
there is no generic ignore-image flag, environment override or local-evidence path.

The exception was not extended to v0.12.0 ([maintainer decision](https://github.com/Judgment-Pack/judgment-pack-agent/issues/169#issuecomment-6506875748)).
`.github/ci-evidence.json` no longer configures it, so verification uses exact runner and
tool matching. The approval record stays as the history of v0.11.0.

A new validated main commit and independent review are required. Prior artifacts
at a different source do not qualify. Latest-attempt, age, complete coverage,
archive/API integrity and every tag/publication/advisory gate remain in force.
The one fresh recovery pair exposed the same mismatch; further arbitrary reruns
or no-op commits are not the remedy. This approval does not waive other gates.

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

Run `python3 -m unittest discover -s tests -p test_ci_evidence.py` in Agent for the
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
