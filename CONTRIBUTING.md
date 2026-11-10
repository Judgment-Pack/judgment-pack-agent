# Contributing

Thank you for helping. This repository is early, with one maintainer; expect
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

Run `scripts/dev/check.sh` once before opening a pull request, and paste its summary block in the
pull request body. It runs what `.github/workflows/ci.yml` runs, from the list in `dev.json`
(`scripts/dev/check.sh --setup` installs the dependencies first). Adapter directories carry their
own checks.

The local runner (`jpdev`) needs Node at exactly the release `dev.json` pins under
`toolchains.node.version`, 22.23.4 today, the one CI sets up and records. It looks for that release
under nvm (`$NVM_DIR`, by default `~/.nvm`), not for whichever Node is first on the `PATH`, and stops
when it is not installed: run `nvm install 22.23.4`. Another 22.x passes most checks, but it is not
the Node the release evidence holds CI to.

## Developer Certificate of Origin

Every commit is signed off (`git commit -s`) under the
[Developer Certificate of Origin](https://developercertificate.org/). The sign-off name and email
must match the commit's author.

## Licence

Contributions are accepted under Apache-2.0, the repository's licence.

## Conduct

See [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md).
