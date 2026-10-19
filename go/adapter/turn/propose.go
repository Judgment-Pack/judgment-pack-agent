package turn

import (
	"encoding/json"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
)

// ProposePackTool is PROPOSE_PACK_TOOL: the proposal as a host tool call. The host registers
// propose_pack in its host tools with ProposePackSchema, and the adapter handles the call itself —
// the host's execute is never reached. The fence stays the fallback.
const ProposePackTool = "propose_pack"

// ProposePackSchema is PROPOSE_PACK_SCHEMA, as JSON.stringify writes the TypeScript literal: the
// schema is in the subset every family's wire carries, so a Gemini session earns no narrowing
// notice for it. contract/vectors/tool-offer/propose-pack.json.
var ProposePackSchema = json.RawMessage(`{"type":"object","properties":{"document":{"type":"object","description":"The proposed pack, as a JSON object (not a JSON string)."},"unknowns":{"type":"array","items":{"type":"string"},"description":"What the policy leaves open, one sentence each."}},"required":["document"]}`)

// ProposePackInstructions is PROPOSE_PACK_INSTRUCTIONS.
const ProposePackInstructions = "\nDeliver the pack proposal by calling " + ProposePackTool + " with the document as an object and its unknowns, then end with a short summary. Use the fenced JSON block only if that call is unavailable."

// ProposalReceived is PROPOSAL_RECEIVED: what the model is told when its call delivered the
// proposal.
const ProposalReceived = "The proposal was received and will be shown to the person for review; nothing was written. Do not call propose_pack again unless the proposal changes. End with a short summary."

// OffersProposeTool is offersProposeTool: whether the host declared propose_pack.
func OffersProposeTool(hostTools []contract.ToolOfferHostTool) bool {
	for _, tool := range hostTools {
		if tool.Name == ProposePackTool {
			return true
		}
	}
	return false
}

// ReadProposedPack is readProposedPack: the tool's arguments as a proposal, or a failure the model
// can read and correct.
func ReadProposedPack(args json.RawMessage) (Proposal, error) {
	parsed := value(args)
	if !parsed.isObject() {
		return Proposal{}, failure.ToolInput("Call propose_pack with an object holding document and unknowns.")
	}
	if !parsed.get("document").isObject() {
		return Proposal{}, failure.ToolInput("Pass the pack as the document object itself, not as a JSON string, an array or null.")
	}
	// The fence's rule, held on the tool path too: only a new pack is proposed.
	if kind := parsed.get("kind"); kind != nil && !(kind.kind == jstring && kind.s == "create") {
		return Proposal{}, failure.ToolInput("Only a new pack can be proposed: leave kind out, or pass \"create\".")
	}
	unknowns := parsed.get("unknowns")
	if unknowns != nil {
		if !unknowns.isArray() {
			return Proposal{}, failure.ToolInput("Pass unknowns as an array of strings.")
		}
		for _, item := range unknowns.a {
			if item.kind != jstring {
				return Proposal{}, failure.ToolInput("Pass unknowns as an array of strings.")
			}
		}
	}
	out := Proposal{Document: raw(parsed.get("document")), Unknowns: []string{}}
	if unknowns != nil {
		for _, item := range unknowns.a {
			out.Unknowns = append(out.Unknowns, item.s)
		}
	}
	return out, nil
}

// TwoProposals is twoProposals: a proposal made through propose_pack beside another in a fenced
// block of the same answer is ambiguous, refused as two fenced proposals are.
func TwoProposals() error {
	return failure.Authored("invalid-response",
		"the answer carried a proposal through propose_pack and another in a fenced JSON block; it must carry exactly one", "")
}

// RecoverableProposal is recoverableProposal: a recovery action for old replies, never an
// automatic import of chat prose. Nil where the answer carries no pack proposal.
func RecoverableProposal(text string) *Proposal {
	proposal, err := ExtractProposal(text)
	if err != nil {
		return nil
	}
	document := value(proposal.Document)
	for _, member := range []string{"specVersion", "id", "title"} {
		held := document.get(member)
		if held == nil || held.kind != jstring {
			return nil
		}
	}
	return &proposal
}
