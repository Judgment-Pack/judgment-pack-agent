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

## 2026-11-09: Undici 7.30.2 and 8.12.4

On 2026-11-05 Undici published 7.30.2 and 8.12.4, which patch six advisories. The previously
approved 7.30.1 and 8.12.2 are inside every one of their affected ranges:

- [GHSA-mqg3-3v4r-cm32](https://github.com/advisories/GHSA-mqg3-3v4r-cm32) (high): cache poisoning
  and cross-user disclosure via intermediate redirect hops (affects 7.30.1 and 8.12.2–8.12.3 only;
  it bypasses the earlier GHSA-4w7w-5xrc-9h26 fix);
- [GHSA-7xg3-fphp-28hg](https://github.com/advisories/GHSA-7xg3-fphp-28hg) (medium): cross-user
  cache disclosure via a same-origin redirect;
- [GHSA-5rm8-gv32-r954](https://github.com/advisories/GHSA-5rm8-gv32-r954) (medium): cross-user
  disclosure via cross-origin redirect revalidation;
- [GHSA-3g9m-rv3r-w5w2](https://github.com/advisories/GHSA-3g9m-rv3r-w5w2) (medium): denial of
  service via an unhandled exception in cache-interceptor response replay;
- [GHSA-9v6h-8rgp-m69f](https://github.com/advisories/GHSA-9v6h-8rgp-m69f) (medium): cross-origin
  credential disclosure when array-of-tuples headers bypass the redirect interceptor's stripping;
- [GHSA-r4pc-4jcp-7pqc](https://github.com/advisories/GHSA-r4pc-4jcp-7pqc) (medium): Subresource
  Integrity bypass via whitespace in integrity metadata.

Remedy 1 (upstream fix): the Undici control (`packages/harness/scripts/undici-controls.mjs`) now
approves exactly 7.30.2 and 8.12.4 and refuses 7.30.1 and 8.12.2. The lock moves only its two
Undici records: `@ai-sdk/provider-utils/node_modules/undici` (runtime, `^7.29.0`) to 7.30.2 and the
development-only root `undici` (jsdom, `^8.10.2`) to 8.12.4. No manifest, range or override changes.

Reachability, recorded for the decision, not as an exception: the four cache advisories and
GHSA-9v6h-8rgp-m69f need Undici's `cache()` or `redirect()` interceptors. No harness module outside
the Undici test controls imports Undici. `@ai-sdk/provider-utils` uses an `Agent` with a
DNS-validating `connect.lookup` and no interceptors, and calls Undici `fetch` with
`redirect: 'error'`, or `redirect: 'manual'` in its own per-hop validated loop that rebuilds
`Headers` and drops caller headers on a cross-origin hop. No harness or provider path passes an
`integrity` option, so GHSA-r4pc-4jcp-7pqc's SRI check is not reached. jsdom, the development-only
test environment (vitest `environment: 'jsdom'`, no options), follows redirects in its own
dispatcher and composes only its decompression interceptor; the harness adds none.

The registry audit (`dependency-audit-live`) on 2026-11-09 reports clean for both the previous and
the updated lock: it did not yet list these six identifiers, so a clean audit is not the evidence
for this change. The control's installed-copy inventory and behavior tests are.
