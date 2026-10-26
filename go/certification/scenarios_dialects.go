package certification

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
	"github.com/Judgment-Pack/judgment-pack-agent/go/relay"
)

var dialectNames = []string{"23 dialect authoring"}
var AgentDialectNames = []string{"23 agent dialect authoring"}

func dialectFixtureDigest(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return "sha256:" + hex.EncodeToString(sum[:])
}
func dialectAuthoringFindings(ctx context.Context, server *Server, inner Adapter, native bool) ([]Finding, error) {
	name := dialectNames[0]
	if native {
		name = AgentDialectNames[0]
	}
	f := findings{check: name}
	root, err := RepositoryRoot()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(filepath.Join(root, "contract/testdata/dialect-authoring/authoring.json"))
	if err != nil {
		return nil, err
	}
	var file struct {
		Request          runcontext.DialectReadRequest `json:"request"`
		Snapshot         runcontext.DialectSnapshot    `json:"snapshot"`
		Bindings         map[string]json.RawMessage    `json:"bindings"`
		ExpectedDocument string                        `json:"expectedDocument"`
	}
	if err = json.Unmarshal(raw, &file); err != nil {
		return nil, err
	}
	for _, mode := range []string{"complete", "plan", "child", "changed-preview", "lost-ack", "repeat-complete", "failed-validation", "transport-failed", "preview-failed", "draft-candidate-lost", "draft-checked-drift", "final-lineage-drift", "empty", "oversized"} {
		var snapshot runcontext.DialectSnapshot
		encoded, _ := json.Marshal(file.Snapshot)
		_ = json.Unmarshal(encoded, &snapshot)
		retained := []runcontext.DialectReviewProposal{}
		drafts := []runcontext.DialectDraftCheckpoint{}
		semanticCalls := 0
		readCalls, previewCalls, enrolled := 0, 0, 0
		selection := runcontext.DialectRetainedColumnDraft()
		tools := runcontext.NewDialectTools(runcontext.DialectAuthoringPorts{
			ConversionContract: &selection,
			Read: func(context.Context, runcontext.DialectReadRequest) (runcontext.DialectSnapshot, error) {
				readCalls++
				encoded, _ := json.Marshal(snapshot)
				var detached runcontext.DialectSnapshot
				_ = json.Unmarshal(encoded, &detached)
				return detached, nil
			},
			Runtime: func(_ context.Context, name string, args map[string]string) (runcontext.ToolResult, error) {
				semanticCalls++
				limits := map[string]any{"documentBytes": 131072, "frameBytes": 2097152}
				r := map[string]any{"contractVersion": "1", "specification": "https://github.com/Judgment-Pack/judgment-pack-spec/blob/" + selection.Spec.Revision + "/rfcs/0029-retained-column-conversions.md", "specRevision": selection.Spec.Revision, "diagnostics": []any{}, "contract": selection.Contract, "profile": selection.Profile, "spec": selection.Spec, "role": "document-validation", "maturity": "experimental-draft", "limits": limits}
				if name == "get_dialect_contract" {
					r["status"] = "available"
					r["capabilities"] = map[string]any{"conversionContracts": []any{map[string]any{"contract": selection.Contract, "spec": selection.Spec, "profile": selection.Profile, "role": "document-validation", "limits": limits}}, "packCheckScope": "declared-inventory-coverage", "operations": []string{"validate", "check"}, "maxPackBytes": 10485760}
				} else {
					r["document"] = args["document"]
					r["digest"] = dialectFixtureDigest(args["document"])
					r["dialectVersion"] = 2
					if name == "dialect_validate" {
						r["status"] = "document-valid"
						r["validationScope"] = "experimental-retained-column-base-document"
					} else {
						r["status"] = "complete"
						r["validationScope"] = "experimental-retained-column-base-document-and-declared-pack-inventory"
						r["compatibility"] = map[string]any{"status": "complete", "scope": "declared-inventory-coverage", "pack": map[string]any{"sha256": dialectFixtureDigest(args["pack"])[7:]}, "valueTypesChecked": false, "executionChecked": false, "facts": []map[string]string{{"target": "/amount"}}, "evidence": []map[string]string{{"target": "receipt"}}}
					}
				}
				if mode == "transport-failed" && name == "dialect_check" {
					return runcontext.ToolResult{}, fmt.Errorf("Runtime unavailable")
				}
				if mode == "failed-validation" && name == "dialect_validate" {
					r["status"] = "refused"
				}
				encoded, _ := json.Marshal(r)
				return runcontext.ResultOf([]runcontext.Content{runcontext.TextContent(string(encoded))}, nil), nil
			},
			Preview: func(_ context.Context, candidate runcontext.DialectCandidate, fingerprint string) (runcontext.DialectPreview, error) {
				previewCalls++
				if mode == "preview-failed" {
					return runcontext.DialectPreview{}, fmt.Errorf("Preview failed")
				}
				if mode == "changed-preview" {
					snapshot.PackJSON += " "
				}
				return runcontext.DialectPreview{Fingerprint: fingerprint, RecordCount: 1, Issues: []runcontext.DialectPreviewIssue{}}, nil
			},
			RetainDraft: func(_ context.Context, c runcontext.DialectDraftCheckpoint) (runcontext.DialectDraftRetention, error) {
				drafts = append(drafts, c)
				if mode == "draft-candidate-lost" && c.Stage == "candidate" {
					return runcontext.DialectDraftRetention{}, fmt.Errorf("lost candidate acknowledgment")
				}
				previous := c.PreviousReceipt
				if mode == "draft-checked-drift" && c.Stage == "checked" {
					wrong := "wrong"
					previous = &wrong
				}
				return runcontext.DialectDraftRetention{Fingerprint: c.Fingerprint, Stage: c.Stage, Digest: c.Digest, PreviousReceipt: previous, Receipt: "draft:" + c.Stage, Status: "draft"}, nil
			},
			Retain: func(_ context.Context, proposal runcontext.DialectReviewProposal) (runcontext.DialectRetention, error) {
				retained = append(retained, proposal)
				if mode == "lost-ack" {
					return runcontext.DialectRetention{}, fmt.Errorf("lost acknowledgment")
				}
				lineage := proposal.Draft
				if mode == "final-lineage-drift" {
					lineage.CheckedReceipt = "wrong"
				}
				return runcontext.DialectRetention{Draft: lineage, Fingerprint: proposal.Fingerprint, Receipt: "review:one", Status: "review-required"}, nil
			},
		})
		runtime := func(_ context.Context, frame json.RawMessage) (json.RawMessage, error) {
			var request struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			if err := json.Unmarshal(frame, &request); err != nil {
				return nil, err
			}
			if request.Method == "notifications/initialized" {
				return nil, nil
			}
			result := any(map[string]any{})
			if request.Method == "tools/list" {
				result = map[string]any{"tools": []any{}}
			}
			return json.Marshal(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
		}
		dialectGate := gate.New(gate.Options{Runtime: runtime, Granted: gate.Ceiling})
		declarations := []contract.ToolOfferHostTool{}
		for _, tool := range tools {
			declarations = append(declarations, tool.Declaration())
		}
		offer, err := dialectGate.Open(ctx, declarations)
		if err != nil {
			return nil, err
		}

		if mode == "plan" {
			dialectGate.Enter(gate.PlanPhase(offer))
		}
		if mode == "child" {
			dialectGate.Enter(gate.ChildPhase(contract.DelegatedTaskProfile{ID: "read-only", Capabilities: []string{"read"}}, offer, gate.LoopPhase(offer)))
		}
		args := map[string]any{"id": file.Request.ID, "revision": file.Snapshot.Revision, "packKey": file.Request.PackKey, "packDigest": dialectFixtureDigest(file.Snapshot.PackJSON), "dialectDigest": dialectFixtureDigest(*file.Snapshot.DialectJSON)}
		for k, v := range file.Bindings {
			args[k] = v
		}
		if mode == "empty" {
			args = map[string]any{}
		}
		if mode == "oversized" {
			args["summary"] = strings.Repeat("x", 4001)
		}
		propose, _ := json.Marshal(args)
		read, _ := json.Marshal(file.Request)
		steps := []memoryStep{{name: "read_dialect", args: read}, {name: "propose_dialect", args: propose}, {say: "The proposal requires human review."}}
		if slices.Contains([]string{"repeat-complete", "lost-ack", "draft-candidate-lost", "draft-checked-drift", "final-lineage-drift"}, mode) {
			steps = []memoryStep{{name: "read_dialect", args: read}, {name: "propose_dialect", args: propose}, {name: "propose_dialect", args: propose}, {say: "The proposal requires human review."}}
		}
		var mu sync.Mutex
		events := []contract.RunEvent{}
		input := &runcontext.Input{CurrentRequest: &runcontext.CurrentRequest{Message: "Propose a structured dialect for review.", Input: "Propose a structured dialect for review."}}
		if mode == "plan" {
			input.Phase = "plan"
		}
		if mode == "child" {
			input.Objective = "Read structured fields."
		}
		run := Run{ModelName: "fixture", Wire: "openai-compatible", Thinking: "off", Prompt: "Propose a structured dialect for review.", AllowConversation: true, Interactive: true, Critic: "off", Gate: dialectGate.Calls(), Tools: offer.Tools, HostTools: tools, Context: input, Emit: func(e contract.RunEvent) { mu.Lock(); defer mu.Unlock(); events = append(events, e) }, EnrollContext: func(_ context.Context, offer adapter.ContextOffer) error {
			enrolled++
			return dialectGate.EnrollContext(offer.ID, offer.HostTools)
		}}
		next := 0
		run.Model = func(_ context.Context, _ string, _ relay.Request) (*http.Response, error) {
			step := memoryStep{say: "Done."}
			if next < len(steps) {
				step = steps[next]
			}
			next++
			message := map[string]any{"role": "assistant", "content": step.say}
			reason := "stop"
			if step.name != "" {
				message["content"] = nil
				message["tool_calls"] = []any{map[string]any{"id": fmt.Sprint("call", next), "type": "function", "function": map[string]any{"name": step.name, "arguments": string(step.args)}}}
				reason = "tool_calls"
			}
			encoded, _ := json.Marshal(map[string]any{"id": "answer", "object": "chat.completion", "choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": reason}}})
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(encoded)))}, nil
		}
		if native {
			run.Model = nil
			run.Agent = &adapter.Agent{Model: "fixture", Effort: "low", Run: func(ctx context.Context, r adapter.AgentRequest, c adapter.AgentCallbacks) error {
				for i, step := range steps {
					if step.name != "" {
						name := step.name
						for _, tool := range r.Tools {
							if strings.HasPrefix(tool.Description, step.name+":") {
								name = tool.Name
								break
							}
						}
						if _, err := c.Tool(ctx, adapter.AgentToolCall{ID: fmt.Sprint("call", i), Name: name, Arguments: step.args}); err != nil {
							return err
						}
					} else if err := c.Event(ctx, adapter.AgentEvent{Type: "message", ID: "answer", Text: step.say, Phase: "final"}); err != nil {
						return err
					}
				}
				return nil
			}}
		}
		if err = inner.Run(ctx, &run); err != nil {
			return nil, err
		}
		if enrolled != 1 {
			f.fail("%s final contextual offer enrollment count %d", mode, enrolled)
		}
		if readCalls == 0 {
			details := []contract.RunEvent{}
			for _, e := range events {
				if e.Guardrail != nil || e.ToolResult != nil || e.Error != nil {
					details = append(details, e)
				}
			}
			encoded, _ := json.Marshal(details)
			f.fail("%s read did not reach the certified read port: %s", mode, string(encoded))
		}
		if mode == "complete" || mode == "repeat-complete" {
			expected := 1
			if mode == "repeat-complete" {
				expected = 2
			}
			if len(retained) != expected || retained[0].DocumentJSON != file.ExpectedDocument || previewCalls != expected {
				f.fail("complete proposal lost exact document, preview or sole retention")
			}
		} else if mode == "lost-ack" || mode == "final-lineage-drift" {
			if len(retained) != 1 {
				f.fail("lost acknowledgment physical retention count %d", len(retained))
			}
		} else if len(retained) != 0 {
			f.fail("%s retained a forbidden proposal", mode)
		}
		expectedDrafts := 0
		switch mode {
		case "complete", "failed-validation", "preview-failed", "changed-preview", "draft-checked-drift", "final-lineage-drift", "lost-ack":
			expectedDrafts = 2
		case "transport-failed", "draft-candidate-lost":
			expectedDrafts = 1
		case "repeat-complete":
			expectedDrafts = 4
		}
		if len(drafts) != expectedDrafts {
			f.fail("%s draft checkpoint count %d wanted %d", mode, len(drafts), expectedDrafts)
		}
		if mode == "repeat-complete" && len(drafts) == 4 && drafts[0].AttemptID == drafts[2].AttemptID {
			f.fail("explicit repeat reused prior artifact")
		}
		if mode == "draft-candidate-lost" && (semanticCalls != 0 || previewCalls != 0) {
			f.fail("unknown candidate acknowledgment continued effects")
		}
		if mode == "draft-checked-drift" && (semanticCalls != 3 || previewCalls != 0) {
			f.fail("unknown checked acknowledgment retried or previewed")
		}
		success, refused := false, false
		for _, event := range events {
			if event.ToolResult != nil && event.ToolResult.Name == "propose_dialect" {
				success = success || !event.ToolResult.IsError
				refused = refused || event.ToolResult.IsError
			}
			if event.Guardrail != nil && event.Guardrail.Tool == "propose_dialect" {
				refused = true
			}
		}
		if (mode == "complete" || mode == "repeat-complete") && !success {
			f.fail("current acknowledged proposal did not produce a successful tool result")
		}
		if mode != "complete" && mode != "repeat-complete" && !refused {
			f.fail("%s has no refusal event", mode)
		}
	}
	return f.list, nil
}

var DialectAuthoring = ScenarioCheck{Scenario: "dialect-authoring", Names: dialectNames, Run: func(ctx context.Context, s *Server, _ string, a Adapter) ([]Finding, error) {
	return dialectAuthoringFindings(ctx, s, a, false)
}}

func agentDialectChecks(ctx context.Context, s *Server, _ AgentHost, with func(Adapter) Adapter, a Adapter) ([]Finding, error) {
	return dialectAuthoringFindings(ctx, s, with(a), true)
}
