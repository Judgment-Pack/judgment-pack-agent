# Working in this repository

This file is read by Codex as `AGENTS.md` and by Claude as `CLAUDE.md`; the two are the same text.
Edit `AGENTS.md` and run `scripts/sync-agent-docs.sh` to regenerate `CLAUDE.md`.

## Before you open a pull request

- Define the related issues, dependency order and acceptance criteria, then implement a coherent
  batch with handwritten acceptance and regression fixtures alongside the code. Do not run a
  full local suite or CI cycle merely per issue, intermediate commit or handoff.
- During development, run useful focused checks for changed paths, affected contracts and
  relevant interactions, including both readers of shared language vectors. Use
  `scripts/dev/check.sh --only NAME[,NAME]` (or `jpdev check --only NAME[,NAME]`).
- Run the required full checklist once for the stable integrated candidate before CI:
  `scripts/dev/check.sh` (or `jpdev check`). This gate belongs to the coherent batch, not each
  issue or intermediate handoff. Every merge candidate still requires its own exact-head checks.
- Paste the actual summary in the pull request body under "Checks". Record the exact candidate
  identity (head and any uncommitted diff), scope, commands, results and remaining CI ownership.
  A skipped or unrun check is not a pass.
- After failures, group related fixes and rerun affected checks with `--only NAME`. Repeat broad
  validation only when actual repository requirements, substantive changes or unresolved evidence
  warrant it, including unknown affected coverage or changed runner/check-selection semantics.
- Keep automatic CI enabled. Full deterministic validation remains required in CI for the
  integrated candidate, default branch and release under the repository's actual gates. Review
  begins only after green CI on the exact reviewed head. Start no CI watch; report once and stop
  after pushing.
- Push with `jpdev push [BRANCH]`, or where the kit is not installed with
  `git -c credential.helper= -c 'credential.helper=!gh auth git-credential' push -u origin BRANCH`.
  The machine's stored git credential can be another account's, and a plain `git push` is then
  refused; the helper form uses the `gh` login. Never store a credential, never change global git
  configuration, never push with force (`jpdev push` refuses `--force` and `-f`).

## Before you hand over

Run these self-checks on the builder's own head before writing the commit message:

- (a) The boundary paths in `dev.json` (`handover.boundary_paths`) show no admission or verdict
  change unless the issue names one: `git diff origin/main...HEAD -- <paths>` contains additions to
  offer tables or new paths only.
- (b) Every schema member and enum value the change adds is carried by at least one vector: remove
  it from a scratch copy of the schema and a vector fails.
- (c) Where two languages read the same vectors, both readers pass over everything added.
- (d) Every mutation row added is caught, and its twin in the other language is caught by hand.
- (e) Every refusal path added emits an event that validates with a malformed input: empty,
  missing or oversized.
- (f) An interrupted and resumed run carries every new checkpoint member.
- (g) The design's words and the code agree sentence by sentence.

Record applicable self-checks and the stable batch checklist result, or identify the coordinator
who owns that gate when handing off an incomplete batch. Keep heavy checks
serialized under the repository lock and temporary state beneath the session scratch/TMPDIR.
Change the paths in `dev.json` `handover.shared_paths` by appending only, never by reordering or
rewriting, because a sibling build may be open. Never fetch, rebase or merge; the coordinator
carries `main` into the branch after the push.

A HIGH (a path that executes twice, runs without approval, or crosses a boundary the issue did not
open) is fixed in the pull request before review. MEDIUMs named by the review of record are fixed in
the same fix round; a MEDIUM found only on a re-read goes to the pre-release batch. LOW rides.

## Testing

- Verify claims against artifacts: inspect the actual files, candidate identity, working directory,
  command output and assertions. A report alone does not prove files were included or tests ran.
- Prefer real implementations and consumer entry paths (commands, endpoints or exported APIs).
  Use mocks only for dependencies that cannot run in CI, and state that limitation.
- Resolve the product under test to the candidate's explicit source or build path; never let an
  installed product of the same name silently run instead. This does not prohibit model CLIs or
  toolchains used as dependencies; record their identity when relevant.
- Write acceptance/regression vectors and fixtures by hand, not from the implementation's output.
- Remove each new fix or safeguard once in scratch and show its regression test fails at the
  intended assertion. Setup, compilation errors and timeouts do not demonstrate that assertion.

## Rules

- Write test vectors and fixtures by hand. Never record them "from a run" of the code under test.
- Keep machine-local paths and personal names out of tracked files, commit messages and pull
  request bodies. Refer to people by role or handle where a name is needed.
- Sign off every commit (`git commit -s`). Never force-push; never rewrite published history.
- End a process you started by its process id. Never use a pattern kill (`pkill`, `killall`).
- One pull request per issue or batch. State what changed and why in plain, factual wording.
- A change to a public surface (a schema, a contract, a command's output) says so in the pull
  request and changes its vectors and documentation in the same pull request.
- If a safeguard stops you (a permission prompt, a refused command, a failed gate), stop and say
  so in your report. Do not work around it.

## Reports

Your final report is short: the pull request and its head commit, what changed, the check
summary block, and anything you stopped on or chose not to do.

## This repository

- The checks are `dev.json`, run by `scripts/dev/check.sh`; CI runs the same list (scoped on a pull
  request, see below). Edit `dev.json` when a check is added, not the workflow.
- A pull request's `check` job runs `scripts/dev/check.sh --all --changed origin/<base>`: only the
  steps that `jpdev.json` maps its changed paths to, and the whole list when a changed path matches
  no rule (the workflows, `.gitignore`, `.gitattributes`, the root `package.json` and
  `package-lock.json`, a file in a new place) or when `jpdev.json`, `dev.json` or the runner
  changed. A push to `main` and the release's call run the whole list, and only that run is what a
  release is held to; the `contract` job keeps its fixed `--only` list. Run the same selection
  locally with `scripts/dev/check.sh --changed origin/main` (or `jpdev check --changed`). The rules
  follow what each step reads, not its name: the Go certification steps (`go-test`, `go-race`,
  `harness-check`) start the TypeScript scripted server, so a harness change selects them; the
  generated reference reads the Go sources and `packages/harness/dist`, so a Go change selects
  `reference` and `unit-tests`, and every rule with `reference`, `unit-tests`, `packed-smoke` or
  `release-quality-engine` also selects `build`. A new step, a new input of a step or a file in a
  new place is mapped in `jpdev.json`; when unsure, map it broader.
- `scripts/parity.sh` needs the Desk checkout at the commit it pins (`desk_pin`); set `JPDEV_DESK`
  to it. After an intended change to a moved file, put the digest the failure prints in that file's row.
- The full mutation table runs on every push to `main` and the release job requires it green on the
  tagged commit (`docs/releasing.md`).
