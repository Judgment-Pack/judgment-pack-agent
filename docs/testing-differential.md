# Prompt and wire differential properties

Issue #167 uses property tests first; it adds no code generation or production behavior.
`node --test scripts/differential.test.mjs` is the `differential` check in `dev.json`,
so the normal CI check list runs it. Node and Go must both be on PATH.

A hand-authored grammar generates 144 cases: seeds `1`, `167`, `24301`, and
`3735928559`, twelve cases per seed, across OpenAI-compatible, Anthropic and Gemini.
Six handwritten controls independently require omitted empty host inputs, populated
prompt layer order, a literal minimal OpenAI request body and provider suffixes.
The test compiles source directly and invokes the real TypeScript and Go host
readers, prompt assemblers, request encoders and whole/SSE response readers.
Decoded answers are re-encoded as assistant turns, including signed replay, so
response reading affects the compared request bytes. Listener events, usage,
answer text and finish reason are also compared.

The grammar covers absent/null/empty brief and instructions, accepted typed memory,
Unicode (including combining characters and non-BMP characters), control characters,
JSON number notation thresholds, numeric object keys and insertion order, tool
schemas and calls, tool failures, multimodal turns and image eviction, system-tail
and trailing-user state, reasoning fields, provider tiers, and whole/stream replies.
It is a bounded regression property, not an exhaustive proof over all JSON values or
malformed provider replies. Existing refusal and conformance suites retain that scope.

Prompt inputs go through the production host readers. Both readers omit an empty
brief or project-instructions answer. The raw TypeScript `hostSection({brief: ''})`
constructor frames an empty block, while Go `HostSection(HostParts{Brief: ""})`
omits it: this property does not claim those raw constructor inputs are equivalent.
Memory inputs are closed typed records, not retired bare strings. No prompt or wire
body is trimmed, sorted, parsed and reserialized, or otherwise normalized for comparison.
Header and decoded metadata objects are compared structurally; the actual prompt,
request body and replay body are compared as exact strings/UTF-8 bytes.

A failure reports its seed, case, family, changed surfaces and bounded first-byte
hex context. Reproduce one generated case with, for example,
`DIFF_SEED=167 DIFF_CASE=3 node --test scripts/differential.test.mjs`; the six
independent controls still run. Retain a small handwritten regression if a failure
needs a production fix. Do not update an expected fixture from either implementation.
The tests use local subprocesses and scripted responses only: no live model or network.
