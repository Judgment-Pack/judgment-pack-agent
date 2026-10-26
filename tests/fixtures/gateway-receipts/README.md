# Actual Gateway receipt consumer proof

The required `gateway-receipts` check in `dev.json` runs
`scripts/gateway-receipt-check.mjs`. Set `JP_AGENT_GATEWAY_SOURCE` to the explicit
Gateway checkout. The check requires a clean checkout at the exact commit in
`source.json`: `55e2d150b7e96d1dcc3f36479ae5fa447c532709`, the reviewed producer
with green exact-head CI. A missing pin, another commit or a dirty checkout fails
closed. The CI check job reads this pin, checks out that exact Gateway commit with
credential persistence disabled, and supplies its path in `JP_AGENT_GATEWAY_SOURCE`.
The evidence input inventory includes the pin, runner, process helper and producer
fixture; both receipt checks require passing evidence and cannot be skipped.

The runner compiles the actual Gateway producer through a Go overlay, the Agent
consumer tests and the adapter dependencies, then typechecks before starting a
fresh fixture. It exercises the persisted V2 connect/capture, actual adapter,
signer and public MCP frontend, followed by both Agent SourceGate consumers.
Remote HTTP and stdio-backed acquisitions verify signature, call/result binding
and independently pinned identity; replay, missing session mapping, swaps,
cancellation, accounting and approval/snapshot guards remain covered. The
upstream servers/container runtime are handwritten stand-ins; no external
service, Docker installation or credentials are required. The stdio proof is
real adapter-to-server pipes, not a synthetic signed receipt.

The fixture retains its ten-minute hard lifetime. The foreground runner's live
phase is bounded to four minutes and always requests the explicit stop handshake
in `finally`. An independent work cutoff reserves time for consumer cleanup,
stop acknowledgement, complete output delivery and producer teardown inside that
budget. Consumers use one cancellable command capability; closing it refuses late
starts and joins every admitted child cleanup, including unawaited commands. A
stalled async callback cannot extend that capability. JavaScript that blocks the
event loop is outside this bound. It runs only receipt and V2 tests while the
fixture is alive.

The Linux helper records the detached leader's PID, start identity and session ID.
Gateway's anchor/adapter groups remain in that dedicated session even when they
leave the leader's group; cleanup enumerates the session and rechecks each exact
PID/start/group/session before an individual signal. Process events, observed
identities and final outcomes are retained in the producer log. This accounts for
the inspected fixture topology, not arbitrary descendants that create another
session (`setsid`). Inaccessible process evidence refuses cleanup verification.
Child exit alone never establishes complete output: bounded pipe closure is
required, and truncated handshake or source identity output is refused. Scratch and TMPDIR must
be configured, and heavy invocations must be serialized by the repository lock.

For explicitly authorized local development integration, an explicit
`--development-manifest FILE` verifies the frozen manifest's HEAD, every changed
file digest, exact changed-file boundary and tracked diff. This mode is refused
in CI and is labelled development evidence. It does not satisfy the final
immutable source pin. Tests run alone can skip live cases without the fixture;
those skips are never evidence of producer coverage. The required runner starts
and supplies the fixture rather than relying on an external live service.
