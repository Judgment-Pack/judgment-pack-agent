// Package adapter is what a Go host hands a Go adapter for one run (the Go form of the TypeScript
// AssistantSession, engine.ts), and the shape of an adapter: capabilities, never an address or a
// credential.
//
// The host builds a Run on one of two capabilities (ENGINE_CAPABILITIES in the TypeScript package):
// the model capability, the relay (go/relay), whose requests the adapter makes and the host relays;
// or the agent capability (Agent, ADR-0007), a host-owned function that runs one invocation of a
// provider-managed agent, whose model loop is the provider's. The runtime is reached only through
// the gate's narrow handle (go/gate, gate.Calls), host tools are the host's own functions beside
// their declarations, and the events go to Emit in order. The certification runner
// (go/certification) is one host; the Desk's server is another.
package adapter

import (
	"context"
	"encoding/json"

	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
	"github.com/Judgment-Pack/judgment-pack-agent/go/relay"
)

// Run is one run's session.
//
// Gate carries the gated call, never the runtime's transport (ADR-0004, as amended): an adapter
// calls a Runtime tool (CallTool), holds a host tool to the phase (AdmitHost) and narrows the run to
// the critic's or the reformat run's phase and back; it cannot set the offer, enter Plan mode or a
// child's phase, or reach the transport. The gate writes its guardrail events to Emit while the call
// is in flight. Tools are the Runtime tools tools/list served through the gate, those on the
// allow-list; HostTools are the host's tools, each with its declaration and its function.
type Run struct {
	Policy            runcontext.PolicyLayer
	PolicyProject     runcontext.PolicyLayer
	TaskClass         string
	PaceUsed          map[string]int64
	PolicyConnections map[string]PolicyConnection
	PolicyRequests    int

	// Wire is the endpoint's family: openai-compatible, anthropic or gemini.
	Wire string
	// ModelName is the model the requests name.
	ModelName string
	// Model is the relay capability: a path suffix and a request. Nil on a run on the agent
	// capability.
	Model relay.Call
	// Agent is the agent capability (ADR-0007): set instead of Model, never beside it. ModelName is
	// then Agent.Model, and Wire, Capabilities and Thinking are not read.
	Agent *Agent
	// Capabilities are the members of the endpoint's capability record the host knows (agent#4),
	// over the family's default record; nil where it knows none.
	Capabilities *contract.CapabilityEndpointCapabilities
	// Thinking is the tier the host's file asked for: off, on or ultra.
	Thinking string
	Gate     *gate.Calls
	Tools    []contract.ToolOfferRuntimeTool
	// HostTools are the host's tools; the boundary adds its own context tools beside them.
	HostTools []runcontext.HostTool
	// Skills are the skills the host declares for the run (ADR-0009), each with the digest the person
	// approved; the boundary admits, catalogues and loads them. A delegated child's run is handed none.
	Skills []runcontext.Skill
	// Prompt is the runtime's prompt (prompts/get), TestPrompt its testing prompt for the critic, or
	// empty.
	Prompt     string
	TestPrompt string
	// Purpose is test-design, brief or graph, or empty for authoring.
	Purpose string
	// AllowConversation permits a prose-only answer; Interactive asks for the presentation events.
	AllowConversation bool
	Interactive       bool
	// Critic is the run's word on the critic (ADR-0010): off, auto or on, the person's per-run
	// control, over CriticProject and the policy table's criticDefault (auto). "" states none.
	Critic string
	// CriticProject is the project's word on the critic, read where the run states none.
	CriticProject string
	// CriticReasoning is the critic's tier in the enum of the run's capability (a thinking tier on
	// the model capability, an effort on the agent capability); "" is the author's tier. A tier lower
	// than the author's is the host's explicit choice, honoured and recorded.
	CriticReasoning string
	// ExistingPack is the host's fact that the run works on a pack that already exists: under auto,
	// its proposal is reviewed.
	ExistingPack bool
	// ReviewerAssigned is the host's fact that a human reviewer is assigned: no critic in any mode.
	ReviewerAssigned bool
	// Review is the switch before ADR-0010, read for one release as an alias of Critic (true on,
	// false off); Critic wins where both are set. Nil states none.
	Review *bool
	// Cases are the cases whose outcomes the host declares for the critic (agent#10), each as
	// run-event.json's $defs/criticCase.
	Cases []json.RawMessage
	// Context is the run's context input (ContextInput): the chat, the current request, the
	// phase, memory, documents and the rest; nil is none.
	Context *runcontext.Input
	// LanguageInstructions is the host's instruction on the language the person reads
	// (assistantLanguageInstructions in host.ts), appended to the system text; empty is none.
	LanguageInstructions string
	// Models is the host's answer for the run's connection (runModels in host.ts, read by
	// runcontext.ReadRunModels): the models it gives the critic and the delegated children, an empty
	// name being the run's own, and how many model requests the connection serves at once
	// (concurrentRequests), by which a delegation batch runs that many children at once. Nil is the
	// run's own model for both and no stated capacity.
	Models func() (runcontext.RunModels, error)
	// Children is the host's capability to start a delegated child's run behind the gate, in the
	// child's phase within this run's (agent#15); nil where the host offers no delegation.
	Children Children
	// BeforeRuntimeCall, where set, is asked before each Runtime call the run's phase offered, and
	// an error it answers is the call's failure the model reads: the delegation coordinator's count
	// of the response's tool calls (agent#13). The gate still decides the call.
	BeforeRuntimeCall func(ctx context.Context, name string) error
	// Emit receives the run's events, in order.
	Emit func(contract.RunEvent)
	// Host is the host's side of the prompt (ADR-0005; brief, memoryFor and projectInstructions in
	// host.ts): the project brief, the project's own instructions and the memory recalled for the
	// person's message, each asked once per run before its first request and placed by the adapter
	// in its layer of every request. A delegated child's run is handed the same Host and asks its
	// own, memory for its objective. Nil supplies none.
	Host runcontext.Host
}

// Children starts a delegated child's gated session: the host enters the child's phase (its
// profile's tools within the parent's phase) on a gate of its own over the same runtime, and hands
// back the child's narrow handle and the Runtime tools that phase offers.
type Children func(ctx context.Context, profile contract.DelegatedTaskProfile, emit func(contract.RunEvent)) (*gate.Calls, []contract.ToolOfferRuntimeTool, error)

// Adapter is one Go adapter, as a host drives it.
type Adapter interface {
	Run(ctx context.Context, run *Run) error
}

// Func lets a function be an Adapter.
type Func func(ctx context.Context, run *Run) error

// Run calls f.
func (f Func) Run(ctx context.Context, run *Run) error { return f(ctx, run) }
