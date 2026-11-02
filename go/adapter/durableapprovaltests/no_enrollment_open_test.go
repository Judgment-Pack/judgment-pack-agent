package durableapprovaltests

// #298's review (M17): a host with no EnrollContext is untouched by the marker the gate holds for one that has it. Two
// failure paths are pinned to the events the release before #289 gives, on both engines, since nothing else notices them:
// a stop during a fresh run's open, after its policy commit, and a host tool whose input schema is not JSON.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	native "github.com/Judgment-Pack/judgment-pack-agent/go/adapter/agent"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/eino"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
	"github.com/Judgment-Pack/judgment-pack-agent/go/relay"
)

// stopAtStoreOperation stops the run once its store's k-th operation has returned.
type stopAtStoreOperation struct {
	runcontext.RunStore
	k, count int
	stop     func()
}

func (s *stopAtStoreOperation) tick() {
	s.count++
	if s.count == s.k {
		s.stop()
	}
}

func (s *stopAtStoreOperation) Load(ctx context.Context, id string) (*runcontext.RunHead, error) {
	head, err := s.RunStore.Load(ctx, id)
	s.tick()
	return head, err
}

func (s *stopAtStoreOperation) Commit(ctx context.Context, in runcontext.RunCommit) (runcontext.RunReceipt, error) {
	receipt, err := s.RunStore.Commit(ctx, in)
	s.tick()
	return receipt, err
}

func unconfiguredRun(engine string, store runcontext.RunStore, tool runcontext.HostTool, providers *int, events *[]contract.RunEvent) (adapter.Adapter, *adapter.Run) {
	g := gate.New(gate.Options{})
	g.SetOffer(contract.ToolOffer{})
	run := &adapter.Run{Wire: "openai-compatible", ModelName: "fixture", Gate: g.Calls(), Thinking: "off", Prompt: "Read.", AllowConversation: true, Interactive: true, Critic: "off", HostTools: []runcontext.HostTool{tool},
		Context: &runcontext.Input{CurrentRequest: &runcontext.CurrentRequest{Message: "Read.", Input: "Read."}, RunStore: &runcontext.DurableRunInput{Store: store, ID: "run-open"}},
		Emit:    func(event contract.RunEvent) { *events = append(*events, event) }}
	if engine == "native" {
		run.Agent = &adapter.Agent{Model: "fixture", Run: func(ctx context.Context, _ adapter.AgentRequest, cb adapter.AgentCallbacks) error {
			*providers++
			return cb.Event(ctx, adapter.AgentEvent{Type: "message", ID: "final", Text: "Done.", Phase: "final"})
		}}
		return native.New(), run
	}
	run.Model = func(context.Context, string, relay.Request) (*http.Response, error) {
		*providers++
		return quarantineFinal(), nil
	}
	return eino.New(), run
}

func openEventLine(events []contract.RunEvent) string {
	out := []string{}
	for _, event := range events {
		raw, _ := json.Marshal(event)
		var head struct {
			Type    string `json:"type"`
			Failure struct{ Code, Source, Outcome string }
		}
		_ = json.Unmarshal(raw, &head)
		text := head.Type
		if head.Failure.Code != "" {
			text += fmt.Sprintf("(%s/%s/%s)", head.Failure.Code, head.Failure.Source, head.Failure.Outcome)
		}
		out = append(out, text)
	}
	return strings.Join(out, " ")
}

func headStatus(t *testing.T, store runcontext.RunStore) string {
	t.Helper()
	head, err := store.Load(context.Background(), "run-open")
	if err != nil {
		t.Fatal(err)
	}
	if head == nil {
		return "none"
	}
	var saved contract.Checkpoint
	if err := json.Unmarshal(head.Checkpoint, &saved); err != nil {
		t.Fatal(err)
	}
	return saved.Status
}

func readableTool() runcontext.HostTool {
	return runcontext.HostTool{Name: "note", Description: "note", InputSchema: json.RawMessage(`{"type":"object"}`), Effect: "read", Taxonomy: &runcontext.ToolTaxonomy{Operation: "read", Effect: "read"}, Execute: func(context.Context, json.RawMessage) (runcontext.ToolResult, error) {
		return runcontext.ResultOf([]runcontext.Content{runcontext.TextContent("note")}, nil), nil
	}}
}

func TestAStopDuringTheOpenOfAHostWithNoEnrollment(t *testing.T) {
	// k is the store operation the stop follows. The second is the policy commit, and the fix rests on it.
	for k, row := range map[int]struct{ line, head string }{
		1: {"error(storage/storage/unknown) usage end", "none"},
		2: {"usage end", "running"},
		3: {"policy_resolved error(cancelled/storage/unknown) end error(cancelled/storage/unknown) end", "running"},
		4: {"policy_resolved execution_checkpoint error(cancelled/storage/unknown) end", "running"},
	} {
		for _, engine := range []string{"api", "native"} {
			t.Run(fmt.Sprintf("%s/operation %d", engine, k), func(t *testing.T) {
				memory := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				store := &stopAtStoreOperation{RunStore: memory, k: k, stop: cancel}
				var events []contract.RunEvent
				providers := 0
				implementation, run := unconfiguredRun(engine, store, readableTool(), &providers, &events)
				if err := implementation.Run(ctx, run); err != nil {
					t.Fatal(err)
				}
				if got := openEventLine(events); got != row.line || providers != 0 || headStatus(t, memory) != row.head {
					t.Fatalf("events %q, providers %d, head %s; the release before gives %q, 0, %s", got, providers, headStatus(t, memory), row.line, row.head)
				}
			})
		}
	}
}

func TestAnInputSchemaThatIsNotJSONOnAHostWithNoEnrollment(t *testing.T) {
	const want = "error(invalid-response/context/unknown) usage end"
	for _, engine := range []string{"api", "native"} {
		t.Run(engine, func(t *testing.T) {
			memory := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
			tool := readableTool()
			tool.InputSchema = json.RawMessage(`{bad`)
			var events []contract.RunEvent
			providers := 0
			implementation, run := unconfiguredRun(engine, memory, tool, &providers, &events)
			if err := implementation.Run(context.Background(), run); err != nil {
				t.Fatal(err)
			}
			if got := openEventLine(events); got != want || providers != 0 || headStatus(t, memory) != "running" {
				t.Fatalf("events %q, providers %d, head %s; the release before gives %q, 0, running", got, providers, headStatus(t, memory), want)
			}
		})
	}
}

// #289: on the model path a run opens its context once, but a host that keeps its gate across runs opens it again on that
// gate, and each open must present the bytes the gate enrolled.
func TestAKeptGateTakesASourceToolOnASecondRun(t *testing.T) {
	tool := runcontext.HostTool{Name: "read_note", Description: "read_note", InputSchema: json.RawMessage(`{"type":"object"}`), EffectClass: "read", Taxonomy: &runcontext.ToolTaxonomy{Operation: "read", Effect: "read"}, Execute: func(context.Context, json.RawMessage) (runcontext.ToolResult, error) {
		return runcontext.ResultOf([]runcontext.Content{runcontext.TextContent("note")}, nil), nil
	}}
	digest := runcontext.ToolListDigest([]runcontext.ListedTool{{Name: tool.Name, Description: tool.Description, InputSchema: tool.InputSchema, Taxonomy: tool.Taxonomy}})
	tool.Source = &runcontext.ToolSource{Kind: "custom", ID: "notes", Trust: "person", Version: "1", ToolListDigest: digest, CurrentDigest: func(context.Context) (string, error) { return digest, nil }}
	g := gate.New(gate.Options{})
	g.SetOffer(contract.ToolOffer{})
	enrolled := 0
	for range 2 {
		var failed []string
		run := &adapter.Run{Prompt: "Read.", Wire: "openai-compatible", ModelName: "fixture", Thinking: "off", Critic: "off", AllowConversation: true, Gate: g.Calls(), HostTools: []runcontext.HostTool{tool}, Context: &runcontext.Input{CurrentRequest: &runcontext.CurrentRequest{Message: "Read.", Input: "note"}}, Emit: func(e contract.RunEvent) {
			if e.Error != nil {
				failed = append(failed, e.Error.Message)
			}
		}, EnrollContext: func(_ context.Context, offer adapter.ContextOffer) error {
			enrolled++
			return g.EnrollContext(offer.ID, offer.HostTools)
		}, Model: func(context.Context, string, relay.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Done.\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))}, nil
		}}
		if err := eino.New().Run(context.Background(), run); err != nil {
			t.Fatal(err)
		}
		if len(failed) != 0 {
			t.Fatalf("refused: %v", failed)
		}
	}
	if enrolled != 2 {
		t.Fatalf("enrollments %d", enrolled)
	}
}
