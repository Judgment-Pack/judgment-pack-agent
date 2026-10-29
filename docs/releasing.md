# Releasing the agent harness

Only maintainers release this repository. A release is a tag a person pushes on a commit already on
`main`. [`release.yml`](../.github/workflows/release.yml) builds the package from that commit and
publishes no release until a maintainer approves it.

There is no registry. A host such as the Desk installs the attested asset and pins it by digest in
its component lock.

## What a release is

- `judgment-pack-harness-<version>.tgz`: the npm package `@judgment-pack/harness` as `npm pack`
  writes it from the tagged commit (`dist/`, `contract/`, `README.md`), where `<version>` is the tag
  without its `v`.
- `checksums.txt`: the SHA-256 of the tarball, in the form `sha256sum` writes.
- A build-provenance attestation whose subjects are both files, each by its own digest, signed by
  GitHub for this workflow at the tagged commit.
- The notes a person wrote, from `docs/releases/<tag>.md`.
- The Go module's tag, `go/<tag>`, on the same commit (see below). It is not an asset of the
  release: Go resolves the module `github.com/Judgment-Pack/judgment-pack-agent/go` by that tag, and
  by no other. The module's version equals the package's. See [`go/README.md`](../go/README.md).

## Once, before the first release

Both are done by a maintainer and neither is read by the workflow except the first one's reviewer
rule.

1. The `release` environment exists with a required reviewer. The workflow reads the rule before it
   writes anything and refuses a repository without it, because GitHub creates a missing
   environment on first use with no protection. It reads that a reviewer rule exists, not who the
   reviewers are, and it cannot stop an administrator from changing the rule later. With one
   maintainer, the person who pushed the tag is the person who approves: the gate is a pause to read
   the draft, not a second person.

   ```sh
   gh api -X PUT repos/Judgment-Pack/judgment-pack-agent/environments/release \
     -F 'reviewers[][type]=User' -F "reviewers[][id]=$(gh api user --jq .id)"
   ```

2. **Immutable releases must be turned on by the maintainer** in the repository settings
   (Settings, General, Releases). It locks the tag and the assets once a release is published, not
   before. This is not done yet: until it is, the workflow compares the tag with the commit the run
   was started for, and a published release can still be changed. The workflow does not read this
   setting. Turn it on before the first tag is pushed.

## Prepare the release

1. Open a pull request that:
   - sets `version` to the release's in `packages/harness/package.json` and in the
     `packages/harness` entry of `package-lock.json` (`npm install --package-lock-only` writes it);
     the repository carries `<next>-dev` between releases, and the workflow refuses a tag whose
     version the package does not hold;
   - finalises `docs/releases/<tag>.md`: what the package is, what the release holds and what it
     does not, and what a host must review before it pins the new version. It is the text the
     release page will carry. It names the Go module, `github.com/Judgment-Pack/judgment-pack-agent/go`,
     and the tag `go/<tag>` that the workflow makes (a test holds every notes file from v0.3.0 on to
     this). Remove the coordinator placeholder comment from it before tagging.
   - writes `docs/upgrade/<tag>.md`, headed with the exact tag. State renamed or
     retired inputs, new required inputs, record-version changes and new refusals,
     and the host migration for each. Link it from the notes with
     `[host upgrade guide](../upgrade/<tag>.md)`. Initial releases describe initial
     adoption; a candidate guide must not assert publication.
2. Run the check list once locally, as `ci.yml` does:

   ```sh
   scripts/dev/check.sh --setup
   scripts/dev/check.sh
   ```

   `scripts/dev/check.sh` runs the steps of [`dev.json`](../dev.json) in order and prints one summary
   block: the three certification suites, the Go steps, `harness-check`, the Python tests, the
   mutation table's `--check`, the parity run against the Desk, and the whitespace and path checks.
   The parity step needs a checkout of the Desk at the commit `scripts/parity.sh` names (`desk_pin`),
   which it reads from `JPDEV_DESK`. The runner is the devkit's, vendored (see the README).

   The `go-race` step sets a 40-minute timeout for each package test binary (#255). The race
   suite can exhaust Go's default 10-minute aggregate package timeout while an individual test is
   still making progress, and the adapter package needs about twice its usual time on slower
   hosted runners. The selected packages, assertions and race instrumentation remain the same.
   The whole step retains its 3,000-second limit.
3. Include the latest `dependency-audit` run for the release candidate in release review, following
   the [dependency advisory policy](security/dependency-policy.md). Record the candidate commit,
   query time, coverage, result and every finding's disposition. Run the live check for that commit
   (`DEPENDENCY_AUDIT_LIVE=1 scripts/dev/check.sh --only dependency-audit-live`) if there is no current
   result. Unavailable evidence is not clean evidence. Review Go and Python dependencies separately,
   since the npm audit does not cover them. Link explicit, unexpired maintainer approvals for any
   reachability exceptions and reassess each before release; withdrawal is separate from remediation.
4. Merge the pull request.

## Tag

Tag the merge commit on `main`, signed, and push only that tag:

```sh
git fetch origin
git tag -s <tag> -m "agent harness <tag>" "$(git rev-parse origin/main)"
git push origin <tag>
```

If no signing key is configured, stop and settle the signing policy. Do not replace a signed tag
with an unsigned one. Signing is the maintainer's practice; the workflow does not check it.

The tag must be annotated (`-s` makes it so): the workflow refuses a lightweight tag, reading the
tag's object type. The notes file `docs/releases/<tag>.md` must name the tag, for example `v0.1.0`,
on its first line (an optional heading marker is allowed), or the workflow refuses it.
The matching nonempty `docs/upgrade/<tag>.md` must name that same tag on its first
line, contain guidance beyond headings and be linked from those notes. The offline
`scripts/check-upgrade-guides.py --tag <tag>` admission check uses the actual tag,
even when it has no entry in the tracked notes inventory.

A tag is `vMAJOR.MINOR.PATCH`, in ASCII digits with no leading zeroes, and nothing after: no
prerelease and no build metadata. A tag is never moved or reused. A fix is a new release.

## What the workflow does

Every job works on the commit the run was started for, by its digest, not on the tag's name.

1. **Admits the tag.** It is in the grammar; it is an annotated tag; it names the commit the run was started for; that
   commit is on `main`; `docs/releases/<tag>.md` exists, is not empty and names the tag on its first line; and the `release`
   environment has a reviewer rule. Matching host upgrade guidance and its notes
   link are also required before draft/publication. If a `go/<tag>` tag already exists it must name that same commit, or the
   run is refused: a `go/` tag never points anywhere but at a `v` release commit.
2. **Verifies trusted exact-source CI evidence.** The Agent-owned
   [evidence gate](ci-evidence.md) requires complete successful `contract`/`check` and full
   mutation-table coverage on the exact release commit from trusted main workflows, with
   independently resolved current inputs. Tag-sensitive checks and fresh advisory review
   remain separate. PRs and automatic main CI keep their full checklist and status names.
3. **Builds the package.** It refuses unless the package's version, in `package.json` and in
   `package-lock.json`, equals the tag without `v`. Then `npm ci`, `npm run build`, `npm pack`
   into `judgment-pack-harness-<version>.tgz`, and `checksums.txt` written over it and checked.
4. **Attests and drafts.** Only if the tag still names the commit and no release exists under the
   tag, both files are attested, each by its own path, and a draft release is created with the
   notes from the commit. The job records the draft's release id for the publish job.
5. **Waits at the `release` gate.** Review the draft on the Releases page: the notes and the two
   assets. Approve the pending deployment on the run.
6. **Compares the draft with the run, and publishes it by its id.** A draft can be changed while it
   waits, by anyone who can write to the repository. After the gate, the tag is compared with the
   commit once more, and the run's own files are held to their checksums. The release the run
   drafted is then read through the releases API by its id, without downloading anything: it must
   still be a draft under the tag, with the title the workflow gave it and the notes the commit
   holds (compared without the newlines either ends in), and the digest the API reports for each of
   its assets must be the digest of the run's file of that name, no asset more or fewer. Any
   difference refuses. The publish call follows that last read at once, addressed to the same
   release id, not to the tag, and marks the release latest.
7. **Reads the published release again.** Immediately after publication the job reads the
   published release's asset digests through the API once more, and fails the step `Require the
   published release to hold what this run wrote`, printing both digest sets, if they differ from
   the run's files. A read that fails is a separate failure, "could not verify the published release", and also fails the step.

8. **Tags the Go module.** Once the release is published, the `Tag the Go module` job creates the
   annotated tag `go/<tag>` (for example `go/v0.4.0`) on the commit the run was started for, through
   the API with the workflow's token (`contents: write`), and reads it back. A `go/` tag that already
   names that commit is left as it is; one that names another commit fails the job, and nothing moves
   it. The tag starts no workflow: the trigger at the top of `release.yml` matches `v` tags alone.
   A failure here fails the run loudly, after the release is already published.

An approval given more than thirty days after the run finds the run's files gone, and the job
fails: release a new version.

### The mutation table and exact-source CI evidence

The full mutation table still runs on every main push, weekly and by dispatch;
PRs run a sample. Release verifies all four full shards and `every row applies`
through the [trusted CI evidence gate](ci-evidence.md), together with
`contract` and `check`. It selects the latest applicable main run and its latest
attempt without filtering for completion; a newer failed, canceled or running
attempt blocks reuse. A PR sample and a partial rerun cannot qualify.

The source-controlled initial age limit is 48 hours. Changed runner/tool/input
identity, stale/missing/inaccessible evidence or tag movement refuses reuse. One
explicit full main dispatch (CI and mutation) followed by one release-verification
retry is the bounded fresh route when main still names the target. There is no
older-green fallback. Existing fresh advisory evidence/review and approved scoped
exception decisions remain required; the optional audit's CI skip proves nothing
about current advisories. Actual-tag upgrade, packaging, approval, publication and
paired Go-tag gates stay in force. See the gate document for identities, API
limits, observed baseline durations and candidate measurement limits.

For v0.11.0 only, the [approved two-image exception](security/v011-runner-image-exception.md)
permits the two recorded ImageVersions until 2026-11-07 00:00:00 UTC. Both actual
producer and verifier must satisfy its exact scope/OS tuple and all configured
tools must still match. Verification checks expiry again after collection; later
versions and verification at/after expiry refuse. This accepts unknown host-image
differences and does not establish image equivalence. New validated main evidence
and independent review are required; every other gate above remains in force.

### The window that remains

The API cannot publish a release on condition that its assets are the ones just read, so one
window remains: between the last read of the draft's asset digests and the publish call, a few
moments of the same step. Whoever can write to the repository's releases can act in it: a
repository writer, which in this organisation is a maintainer, the same people the `release` gate
asks to approve. Nothing in the workflow closes that window. A file replaced in it is published,
and the read after publication then fails the run by name; with release immutability on, the
published release can no longer be changed, so the replacement stays visible as a failed release
and is not quietly corrected.

No maintainer token and no repository secret is used.

### What exists before the approval

The gate is on the release. Three things exist before it and are not secret:

- the files, as an artifact of the workflow run, downloadable for thirty days by anyone who can read
  the repository's runs;
- the attestation of the tarball and `checksums.txt`, recorded in a public transparency log when it
  is made. It says which workflow built the files, from which commit. It does not say a maintainer
  approved them;
- the draft, visible to those who can write to the repository.

### If a run fails

Fix the cause on `main` and release a new version. Re-running is for a failure that was the hosted
runner's: re-run the failed jobs, not all jobs.

What to do with a draft depends on which job failed:

| The job that failed | The draft | What to do |
| --- | --- | --- |
| Any job before `Attest and draft release` | none was made | re-run the failed jobs; where the package version did not match the tag, fix it on `main` and release a new version |
| `Attest and draft release` | may exist, and may lack assets | read it, delete it by hand, then re-run the failed jobs. The job refuses to run while a release under the tag exists, draft or published, and refuses when it cannot find out |
| `Publish release`, before publication | needs looking at: it may be as it was made, changed, incomplete or gone | read why the job failed, and look at the Releases page. **Keep** a draft that is as it was made, and re-run the failed jobs: that job only publishes the release id the run drafted. Do not publish a draft that was changed or is incomplete. Where the draft cannot be published, release a new version |
| `Tag the Go module` | the release is published | the release is fine; only the module's tag is missing. Re-run the failed job, or tag by hand (below). If the job said the tag names another commit, do not move it: release a new version |
| `Publish release`, at `Require the published release to hold what this run wrote` | published, holding files this run did not write, or not published after the call | someone who can write to the repository changed the release in the window above. Treat it as an incident: tell the maintainers, point no one at the release, and release a new version from a new tag. Do not re-run |

### Tagging the Go module by hand

If the `Tag the Go module` job cannot be re-run, make the tag on the release commit, annotated and
signed as the release tag is, and push only that tag. It triggers no workflow.

```sh
git fetch origin --tags
git tag -s go/<tag> -m "Go module go/<tag>" "$(git rev-parse '<tag>^{commit}')"
git push origin go/<tag>
```

Then check that Go resolves it: `go list -m github.com/Judgment-Pack/judgment-pack-agent/go@<tag>`.
`go/v0.3.0` was made this way. A `go/` tag is never moved or reused, and never made on a commit
that is not a `v` release commit.

Delete a draft with `gh release delete <tag> --repo Judgment-Pack/judgment-pack-agent`, which
leaves the tag. A published release is never deleted to make room for another.

## Verifying a release by hand

Both files are attestation subjects, and `checksums.txt` also names the tarball's digest. With the
release's files downloaded into a directory of their own:

```sh
git clone https://github.com/Judgment-Pack/judgment-pack-agent.git agent
commit="$(git -C agent rev-parse '<tag>^{commit}')"
gh release download <tag> --repo Judgment-Pack/judgment-pack-agent --dir release-<tag>
cd release-<tag>
for file in judgment-pack-harness-<version>.tgz checksums.txt; do
  gh attestation verify "${file}" \
    --repo Judgment-Pack/judgment-pack-agent \
    --signer-workflow Judgment-Pack/judgment-pack-agent/.github/workflows/release.yml \
    --source-ref refs/tags/<tag> \
    --source-digest "${commit}" \
    --format json
done
sha256sum --check --strict checksums.txt
```

The loop checks each file against the attestation made in this repository, by this workflow, in a
run started for that tag, from that commit; with `--repo` alone it would check the repository and
nothing more. The checksum command checks the tarball against `checksums.txt`, itself an attested
file. `--format json` is for scripts: `gh attestation verify` prints nothing when its output is not
a terminal. On macOS use `shasum -a 256 --check checksums.txt`.

To check that the tarball is what the tagged source builds, build it from the tag and compare. The
packing is reproducible, so the digests are equal:

```sh
git -C ../agent checkout --detach <tag>
mkdir rebuilt
(cd ../agent && npm ci && npm run build && npm pack --workspace @judgment-pack/harness --pack-destination "$(cd ../release-<tag>/rebuilt && pwd)")
sha256sum rebuilt/judgment-pack-harness-<version>.tgz judgment-pack-harness-<version>.tgz
```

and, for the package's integrity string as `npm` states it, compare
`npm pack --dry-run --json --workspace @judgment-pack/harness` run in the checkout (its `integrity`
field) with `openssl dgst -sha512 -binary judgment-pack-harness-<version>.tgz | base64 -w0`, which
carries the prefix `sha512-`. None of these says a maintainer approved the release, which its being
published says.

## Live answer-quality evidence

For v0.10.0, follow the [scoped maintainer deferral](release-quality.md#v0100-scope-decision)
and explicitly record that live answer quality was not evaluated. Issue #169 remains
open for the Desk handoff; this deferral changes no other release requirement.

For v0.11.0 only, follow the [scoped maintainer deferral](release-quality.md#v0110-scope-decision).
Issue #169 and the dependent live routed/unrouted comparison report are outstanding follow-ups,
not blocking release gates. Release review and notes must disclose the deferral; no live
answer-quality, token-saving or monetary-cost result is claimed. Desk's accepted comparison
for its v0.14.0 release and all remaining Agent gates are unchanged. Routing remains off by default.

For releases without a scoped maintainer deferral, before accepting a release candidate,
follow [the live answer comparison](release-quality.md).
Attach its validated candidate-versus-previous-release report and any threshold decision
to release review, and state actual model, tokens and rate-derived USD cost in release notes.
The separate ADR-0022 offline replay and fixture tests do not meet this live prerequisite.

## Upgrade-guide coverage

The existing Python CI step validates every tracked release-notes version and
locally available `v` / `go/v` tag using `scripts/check-upgrade-guides.py`. Missing,
empty, heading-only or mismatched guides and missing/wrong notes links fail.
No network is needed, and missing refs in a shallow checkout are not treated as
complete historical tag evidence. The release workflow separately passes its
actual `RELEASE_TAG` to the same validator before any draft is created, including
a newly named version outside the notes inventory. Historical guides are later
backfills over their cited release notes/tagged source; they do not modify prior
release artifacts or imply these new checks ran on old tags.

## Generated dependency inventory from v0.11.0

Before release, regenerate `docs/dependencies.md` with `python3 scripts/gen-dependencies.py` using
the configuration in `scripts/dependencies-config.json` and the check runner's selected Go toolchain.
When dependencies or their evidence change, first run
`python3 scripts/gen-dependencies.py --refresh-metadata`, inspect the version-bound local package,
origin and license evidence in `scripts/dependencies-metadata.json`, then regenerate. Missing owner
or license evidence stays explicitly unknown; this inventory does not replace fresh advisory review.
The ordinary `generated-docs` checklist check regenerates in a disposable source copy and fails on
stale dependencies, metadata, build configuration or package closure. Other reference checks remain.

Every release note from v0.11.0 onward links the inventory at its actual tag, for example
`https://github.com/Judgment-Pack/judgment-pack-agent/blob/v0.11.0/docs/dependencies.md` for v0.11.0.
The existing actual-tag `check-upgrade-guides.py --tag` admission also requires this link and the
inventory file, preserving the historical notes contract before v0.11.0. Do not link `main` or a
previous version. The published Go library has no single host final binary; native closure in this
document is the configured Linux/amd64 CGO-enabled production package union, not every host's link.
