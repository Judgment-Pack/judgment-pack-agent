# The Go module

`github.com/Judgment-Pack/judgment-pack-agent/go` holds the Go side of the agent harness
(ADR-0004): the contract's generated types, the tool gate, the Eino adapter and the Go runner of the
certification suite. It is a module in a subdirectory of this repository, so Go resolves it by a tag
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

## How the tag is made

The release workflow creates `go/vX.Y.Z` on the release commit after the release is published, and
fails the run if it cannot. A person does not tag the module for a normal release. `go/v0.3.0` was
pushed by hand; [`docs/releasing.md`](../docs/releasing.md) says how to do that again if the
workflow's step fails.
