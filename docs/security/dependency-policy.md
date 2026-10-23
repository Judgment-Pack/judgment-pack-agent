# Dependency advisories

The maintainer decides advisory dispositions and records the evidence on the issue or release
review. A dispute or the absence of a fixed version is evidence to investigate, never an automatic
exception. Review every severity, including development dependencies and transitive paths.

## Audit evidence

The `dependency-audit` check runs on every pull request, weekly on Monday, and on manual dispatch.
Its job summary reports the current npm registry audit for the root `package-lock.json`, all
workspaces, and production, development, optional and peer dependencies. No package installation
or lifecycle script runs. It reports the full current set, rather than only a baseline delta, so a
new advisory cannot be hidden by an old finding. Compare the advisory identifiers and counts with
the last successful run to identify changes; follow registry evidence for package paths and details.

This job does not audit Go modules, Python development requirements, OS packages, vendored source,
or advisories absent from the npm registry. A clean result proves only this stated coverage at the
time of the query. Release review must assess the other dependency ecosystems separately.

Commands live in `dev.json`: `dependency-audit` is the offline fixture check;
`dependency-audit-live` queries the registry when `DEPENDENCY_AUDIT_LIVE=1`. The local checklist
skips the live query unless explicitly enabled. Its fixture check always runs. The dedicated CI job
enables the live query and has a stable `dependency-audit` result visible on PRs and scheduled runs.

| Result | Exit | Meaning |
| --- | --- | --- |
| `clean` | 0 | Available, structurally valid npm audit v2 evidence with no findings |
| `findings` | 1 | Available evidence contains affected packages, at any severity |
| `unavailable` | 2 | Failed query, timeout, oversized output, unsupported/malformed report, or inconsistent exit/count evidence; no clean audit is established |

The job fails for findings and unavailable evidence. It has no ignore list, severity suppression,
or exception switch. A maintainer-approved disposition remains a recorded release-review decision;
it does not turn this audit green. An unavailable result needs a fresh successful query, not an
exception based on missing evidence. Only validated counts and GitHub advisory identifiers enter
logs and job summaries; raw registry responses, errors, credentials and configuration are never
uploaded as artifacts or printed. Use npm audit locally for detailed evidence and redact it before
sharing. The job uses read-only repository permission and no repository secret.

## Remedies, in order

1. Take an upstream fix and verify the resolved dependency path, the relevant regression test and
   a fresh audit. A claimed fix without a fixed version is not a fix.
2. Remove the dependency path, including indirect and development paths where relevant. Verify it
   is absent from the resolved graph and run the affected checks.
3. Only if neither remedy is feasible, request a bounded reachability exception from the maintainer.
   Record why the first two remedies are unavailable. Approval must be explicit and linked in the
   advisory issue and release review; an agent cannot grant it.

Each proposed exception must name:

- the advisory identifier and primary source, affected version, dependency chain, vulnerable code
  and exact affected harness path;
- reachability evidence explaining why no harness entry point reaches that code with the relevant
  input, including build, test and development paths within the decision's scope;
- a regression test that fails if that path becomes reachable, its command and passing evidence;
- the approving maintainer and approval link, the affected releases, and an expiry date no later
  than 30 days after approval;
- reassessment at expiry, before each release, and immediately on a dependency/path/input change,
  a new upstream fix, or changed advisory evidence, whichever comes first.

Expired or invalidated approval cannot support a release. Reassess with fresh evidence or take one
of the first two remedies. Preserve the original record and its replacement; do not silently delete
or suppress an advisory. A source's withdrawal is its own disposition: record the source and date,
recheck the live audit, and distinguish it from code remediation or a reachability exception.

## Historical decision: #151

The maintainer allowed v0.9.0 to proceed while deferring the remaining braces remediation to
[#161](https://github.com/Judgment-Pack/judgment-pack-agent/issues/161).
[#151](https://github.com/Judgment-Pack/judgment-pack-agent/issues/151) is the historical assessment.
This policy does not grant an exception or revive its stopped validation task.
[GHSA-vfj7-8cjw-p6xm](https://github.com/advisories/GHSA-vfj7-8cjw-p6xm) was withdrawn by GitHub on
2026-10-23; the primary advisory lists no patched version. Record this as **upstream withdrawal**,
not a code fix or a finding of library safety. No dependency pin is changed by this decision.
If the npm registry still reports it, the audit remains a findings result until its evidence changes;
release review records the withdrawal separately. A future advisory or changed evidence requires
fresh assessment. [#161](https://github.com/Judgment-Pack/judgment-pack-agent/issues/161) remains in
undated Upstream follow-up, unscheduled and outside v0.9.1. No reachability exception is granted here.
