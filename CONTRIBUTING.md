# Contributing

Thank you for helping. This repository is an early research preview with one maintainer; expect
the contract to change while the first adapters are extracted.

## Working on an issue

Say on the issue that you are taking it. Keep a pull request to one issue, one adapter or one
contract change. A change to `contract/` is a public-surface change: say so in the pull request,
and add or change the vectors with it.

## Adding an adapter

An adapter is one framework behind the contract. It lives in its own directory, implements the
events, tool calls, approvals, failures and budgets the contract states, and passes the
certification suite unchanged. An adapter never holds a model key, never offers a tool the host
did not offer, and never acts without the approval the host requires.

## Review

Every material change gets one clean-room review round, recorded on the pull request with the
prompt, the findings and the maintainer's disposition of each. A change to the contract or to what
a model may reach is material. Cosmetic and documentation changes need no round.

## Local checks

Run what `.github/workflows/ci.yml` runs: every JSON document under `contract/` parses, and
`git diff --check` is clean. Adapter directories carry their own checks.

## Developer Certificate of Origin

Every commit is signed off (`git commit -s`) under the
[Developer Certificate of Origin](https://developercertificate.org/). The sign-off name and email
must match the commit's author.

## Licence

Contributions are accepted under Apache-2.0, the repository's licence.

## Conduct

See [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md).
