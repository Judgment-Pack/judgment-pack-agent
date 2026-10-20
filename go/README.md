# The Go module

`github.com/Judgment-Pack/judgment-pack-agent/go` holds the Go side of the agent harness
(ADR-0004): the contract's generated types, the tool gate, the Eino adapter on the model capability,
the agent adapter on the agent capability (ADR-0007), the delegation coordinator for both, and the
Go runner of the certification suite. It is a module in a subdirectory of this repository, so Go resolves it by a tag
named `go/vX.Y.Z`, not by the release tag `vX.Y.Z`.

## Using it

```sh
go get github.com/Judgment-Pack/judgment-pack-agent/go@vX.Y.Z
```

`vX.Y.Z` is a release of the package. **The module's version is the package's:** every release
`vX.Y.Z` of `@judgment-pack/harness` has a module version `vX.Y.Z` at the same commit, and the two
carry the same contract. Use the versions together.

`go get` records the version in `go.mod` and the module's checksum in `go.sum`. Keep `go.sum` in
version control: Go checks the downloaded module against it, and against the public checksum
database, so the bytes are the ones the tag named. Do not use `@latest` or a branch name in a
host's build.

The Desk pins the module by version, the version of the package release it pins (the same release,
by digest, in its component lock).

## The host's side of the prompt

A Go host hands an adapter its parts of the prompt on `adapter.Run`'s `Host`, a `runcontext.Host`
(`Brief`, `ProjectInstructions` and `MemoryFor(query)`; `runcontext.HostFuncs` makes one of
functions). The adapter asks each once per run, before its first request, holds each to its bound
in the policy table, and places it in its layer of every request
([ADR-0005](../docs/adr/0005-the-prompt-is-the-adapters.md)); the relay adds nothing. The digest of
every request body is in the run's checkpoint (`prompts`).

## The agent capability

A run is on one of two capabilities: the model capability (`adapter.Run.Model`, the relay) or the
agent capability (`adapter.Run.Agent`, [ADR-0007](../docs/adr/0007-provider-managed-delegation-in-go.md)),
a host-owned provider-run function for a provider that runs its own model loop. The host's
`adapter.Agent` carries the bound model, the function (`Run`, one invocation per call, returning
only once its account is released, and an error wrapping `adapter.ErrBusy` for a busy refusal at
admission) and its enabled-model admission (`Enabled`). The adapter is `go/adapter/agent`; wrap it
in `delegate.With` for delegation: a `delegate_tasks` call ends the parent's invocation, the children
run one at a time on the account, and a new invocation continues with the call's result. Each step
is written through the host's durable callback (`runcontext.Input.Checkpoint`) and acknowledged
before the next invocation; the checkpoint's `handoff` member says where a restarted response
continues. `examples/agent-host` binds a stand-in bridge, a run store and a gate.

To certify a binding, call `certification.CertifyAgent` with an `AgentHost` whose `Bind` plays each
check's script through the host's own bridge, account and tool handler. `go run ./cmd/harness-check`
runs the same checks against the package's stand-in, a fake exclusive account, and says that no
host's bridge was certified.

## How the tag is made

The release workflow creates `go/vX.Y.Z` on the release commit after the release is published, and
fails the run if it cannot. A person does not tag the module for a normal release. `go/v0.3.0` was
pushed by hand; [`docs/releasing.md`](../docs/releasing.md) says how to do that again if the
workflow's step fails.
