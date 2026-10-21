# Evaluation corpus

Each directory is one reviewed, redacted run recorded with `record: true`. It contains
`manifest.json`, `requests.json`, `tool-results.json`, `events.json`, `checkpoint.json`, and
`expected.json`. Add or intentionally update an entry in the same pull request as the behaviour
change, and state the reason just as for a parity row. Never add an unreviewed recording.

Replay uses only the recorded scripted-model requests and tool results. It compares the gate's
decisions, proposal digest, critic verdict and reasons, refusals, and outcomes. It never compares
model prose and never reaches a live provider. A manifest may tolerate only timing values and
randomly drawn markers. Run `node packages/harness/scripts/corpus-check.mjs corpus` and
`go run ./cmd/harness-check corpus` from `go/` before proposing an entry.
