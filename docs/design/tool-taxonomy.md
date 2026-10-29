# Tool taxonomy integration

[ADR-0027](../adr/0027-tool-taxonomy.md) defines the public taxonomy and migration. Tool owners supply mandatory operation/effect; hosts only restrict offers. `toolTaxonomy.ts` and `go/adapter/wire/tooltaxonomy.go` define validation; context helpers derive presets, annotations and skill profiles.

Runtime must publish `{operation,effect,checkKind?,experimental?}` directly inside MCP `_meta`. Gateway snapshot-backed registrations require current V2 owner taxonomy and independent full approval context; historical V1 remains readable but cannot authorize current admission. An external read's host verifier must authenticate the receipt and bind its source, descriptor pin, tool, canonical arguments and canonical result. Returning true merely because a receipt string exists violates the host contract.

The verifier is asked before anything of the result is kept: an unverified result is never retained
for the context readers, recorded as a durable result or offered as Plan evidence, and the call
stays inspectable only as a failed operation (ADR-0027's amendment, agent#242).

A host approving a skill computes its pin with `skillDigest` / `SkillDigest` and the admitted tool taxonomy map. Pass that same run's map to skill admission. Unknown dependencies never receive a default profile. Optional kind/tags affect the pin; a computed profile is never accepted from an author or model. Preset helpers always retain a valid pure ask, preserving `ask_user`. Routing #172 must consume these helpers and still apply all existing phase, approval and name gates.


The host can configure `source.gatewayReceipt` / `ToolSource.GatewayReceipt` with independently
approved Ed25519 publicKey (64 lowercase hexadecimal digits), authority and optional adapter
{name,version,digest}. Absence of the optional adapter pin leaves executable identity unverified.
The declaration must be read-external with a complete V2 descriptor approval and original byte
pin. The executor obtains `gatewayExecutionContext(signal)` / `GatewayInvocation(ctx)` and maps
its receiptSession to Gateway's existing tools/call `_meta["io.judgment-pack/session"]`, separately
from arguments. It returns the actual complete MCP envelope. Agent authenticates and unwraps
the signed artifact before publication. No custom verifier runs after Gateway verification fails.
The raw tool key comes from approved owner metadata; a frontend bridge calls the explicit
approved platform plus "." plus that key. No source identity is guessed from model text.

Fresh random session, initial index and previous-signature checks prevent prior acquisition replay
and concurrent same-argument response substitution. Failure after execution withholds the result,
retains consumed source-call accounting and reports tool-failed/unknown. It does not authorize
replay. The exact descriptor byte guard remains separate from signed call/result fields; the
receipt is not a signed descriptor snapshot. The current Gateway adapter checks the full approved
upstream projection before executing the selected tool. An observer merely rereading a pinned
file is not independent evidence of upstream freshness.

Focused paired tests use the handwritten Gateway v2/catalogue/v3 vectors. The optional actual
producer fixture is `tests/fixtures/gateway-receipts/producer_test.go.txt`, overlaid into the frozen
Gateway checkout and compiled as a test binary. Set `JP_AGENT_GATEWAY_OUTPUT` to its disposable
output file, run only TestAgentConsumerReceiptProducer, then set `JP_AGENT_GATEWAY_FIXTURE` to
that file for the TS/Go receipt tests. The fixture uses actual connect/capture, executable adapter,
signer and public MCP frontend with disposable local targets. Its stop file is output + ".stop";
it also has a ten-minute lifetime. This test route is distinct from synthetic cryptographic vectors.
