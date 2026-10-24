# Live release answer comparison

Issue #169 adds a release-review tool separate from ADR-0022's deterministic offline
replay. It compares public final answer text from two real release builds through the
Vercel engine, using the same explicitly named OpenAI-compatible model and a fixed
slice of two model corpus entries. The agent-capability corpus entry is outside this
slice. The corpus's original request bodies are empty placeholders: the reviewed
`quality/release-slice.json` supplies public prompts and pins every corpus file by digest.
This is a small answer-change signal, not a semantic quality score or full adapter suite.

`quality/release-config.proposed.json` is an **unapproved proposal**. It deliberately
fails validation. “Use our policy” supplies no numeric budget or change threshold.
A maintainer must authorize a concrete local configuration with the exact model,
HTTPS provider endpoint, provider API-key environment variable name, confirmed rates,
previous release tag/commit and final candidate commit. Never use Codex session
credentials or infer provider access from a subscription. The previous release's
package version must match its tag. Both checkouts must be clean at their exact commits.
The runner configures the public harness host with fixed English instructions, no project
brief or memory, an in-memory corpus runtime and a synthetic relay session. That session
is never a provider credential; only the separately bounded provider transport receives
the explicitly configured API key. File, image and external runtime capabilities refuse.
Build each with its own lockfile before running; the report identifies the complete
built `dist` tree by digest. This records local build identity, not an attestation that
an untrusted build is authentic.

The proposed limits are four calls per case per version, 32,768 input tokens and 512
output tokens per call, 532,480 tokens across both versions (16 calls maximum), USD 1
maximum, and changed-answer share threshold zero. Input bounds use complete serialized
text/tool request UTF-8 bytes plus 1,024 framing tokens, deliberately conservative for
byte-based text tokenizers. Use only a provider/model whose text tokenizer and billing
accept this upper bound; no image/audio/file inputs are accepted. Models with a different
bound require a reviewed change before use. Each send reserves the full per-call token
and USD allowance from the remaining **global** budget. A lower authorized budget may
stop the replay incomplete before the maximum number of calls; no partial pass is possible.
No automatic retries occur. Responses have byte and time limits. Unexpected model,
non-success/non-tool finish, missing/invalid usage, out-of-scope tools, missing fixture
reads or engine errors refuse evidence. Provider errors expose no raw response or credentials.

Rates are integer nanodollars per token: a published price of USD 1 per million tokens
is 1,000 nanodollars per token. Record separate input, cached input and output rates,
including all billable completion/reasoning tokens in the provider's completion total.
Unsupported billing classes are refused. Cost is exact rate-derived USD for provider
reported tokens; invoice credits, discounts, taxes and unrelated provider charges are
outside this figure. The report states each call's usage and cost, total tokens, total USD,
model, bounds and threshold. A failed request may have incurred unknown
charges: its incomplete artifact preserves known usage and marks uncertain attempts. An abruptly
terminated process can leave only the initial incomplete marker; recover billing from the
provider before any rerun.
Never describe it as a complete run or zero-cost evidence.

From the repository root, with Node 22 and explicit credentials in the configured
variable, run:

```sh
node scripts/release-quality.mjs run CONFIG SLICE PRIOR_CHECKOUT CANDIDATE_CHECKOUT REPORT
node scripts/release-quality.mjs validate CONFIG REPORT
```

The report path must be new. Both commands return 0 for a complete comparison within the
threshold, 2 when a complete comparison requires a maintainer decision, and 1 for invalid
or incomplete evidence. Validation needs the intended concrete configuration; it recomputes
scope, changes, token totals and cost, and rejects offline fixtures. It does not independently
attest that a provider was contacted; retain the trusted runner's original artifact.

Above the threshold, review every recorded prior/candidate answer pair and record the
maintainer's decision in the release review. Exact text changes include harmless wording
variation. The report retains only final host-visible messages after the engine's output
guard, never raw provider responses, credentials, private reasoning or tool transcripts.
The fixed inputs/tool fixtures are public and synthetic; never substitute private prompts.
Review the report for publication before attaching it.

Attach the validated live report and configuration, exact release identities, and the
threshold decision to the release review. State its **actual** total USD and tokens in the
release notes, with the model, slice digest and rate assumptions. Tooling tests and
`provenance: offline-test` reports do not meet this release prerequisite. If provider
configuration/spend approval is absent, record the missing prerequisite and leave #169
acceptance incomplete. No per-run live cost has been established by the proposal itself.
