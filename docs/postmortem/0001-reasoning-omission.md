# 0001: Reasoning omission and request boundaries

## What happened

The v0.9.0 release audits repeatedly found that a reasoning value correctly
resolved to omission could be restored before request construction. The
[first audit](https://github.com/Judgment-Pack/judgment-pack-agent/pull/146#issuecomment-6353962452)
identified compactor defaults, a compatibility claim contradicted by new refusals,
and missing downgrade guidance. The
[second audit](https://github.com/Judgment-Pack/judgment-pack-agent/pull/146#issuecomment-6354981806)
confirmed those fixes and found two remaining restoration paths: spreading the raw
TypeScript session retained its effort/thinking, and the critic floor in both
languages restored an omitted tier. A Codex-engine fallback also needed removal.

A later [integrated audit](https://github.com/Judgment-Pack/judgment-pack-agent/pull/146#issuecomment-6365215450)
found the same class in delegated children. The child inherited raw parent
thinking through a session spread and resolved it again after discovery had
already selected omission. Its probe used the actual built delegation/session
functions and observed request-boundary restoration; it did not make a remote
provider call. The audit stopped for maintainer direction.

## Why existing checks missed it

Resolved policy and raw configuration remained available together. Correctness at
resolution did not prevent a later spread, fallback or helper from recovering the
old value. The existing delegated fixture began with absent raw thinking and
checked child input policy, so it did not exercise inherited raw thinking after
resolved omission.

The first regression guards enumerated reads in hard-coded files. The
[#148 re-read](https://github.com/Judgment-Pack/judgment-pack-agent/pull/148#issuecomment-6356490602)
showed that spreads, helpers and newly added request-building files escaped them.
The [recovered re-read](https://github.com/Judgment-Pack/judgment-pack-agent/pull/148#issuecomment-6361481000)
then showed ordinary typed constant-key reads in TypeScript and defined-type
conversions in Go escaped the source-wide guards. It also found that a TypeScript
mutation's reported catch came from unrelated failures: the named test still
passed. A green check or mutation result therefore did not establish the claimed
boundary coverage.

## Failure class and structural fix

This is restoration of discarded configuration across a resolution boundary.
[#148](https://github.com/Judgment-Pack/judgment-pack-agent/pull/148) introduced
request-session types that exclude raw reasoning, a conversion site, and typed
source guards. The [targeted final review](https://github.com/Judgment-Pack/judgment-pack-agent/pull/148#issuecomment-6362242048)
verified corrected constant-key and defined-type detection with independent
fixtures and removal of each guard fix. It kept the ineffective TypeScript
mutation open in #149; it did not claim that finding resolved.

[#164](https://github.com/Judgment-Pack/judgment-pack-agent/issues/164) extends the
same boundary to every resolved request value, including model, provider,
capabilities and tool admission. The prevention principle is to give builders only
the resolved request representation, then test actual request bytes and resumed
requests when raw values disagree with resolved omission. Guard probes must
compile, exercise the intended assertion and fail when its specific safeguard is
removed. Deliberate TypeScript `any` and Go reflection/unsafe remain disclosed
limits; these guards are not language sandboxes.

The corresponding [request-boundary defensive pattern](../defensive-patterns.md#request-builders-read-only-the-resolved-boundary-148-164) records the rule, enforcement and remaining language escapes.
