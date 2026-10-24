package runcontext_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/eino"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
	"github.com/Judgment-Pack/judgment-pack-agent/go/relay"
)

const durableToolResponse = "data: {\"id\":\"fixture\",\"object\":\"chat.completion.chunk\",\"model\":\"fixture\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call-fixture\",\"type\":\"function\",\"function\":{\"name\":\"read_fixture\",\"arguments\":\"{}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n"
const durableFinalResponse = "data: {\"id\":\"fixture\",\"object\":\"chat.completion.chunk\",\"model\":\"fixture\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Done.\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"

type engineModelStore struct {
	runcontext.RunStore
	commit func(context.Context, runcontext.RunCommit) (runcontext.RunReceipt, error)
}

func (s engineModelStore) Commit(ctx context.Context, in runcontext.RunCommit) (runcontext.RunReceipt, error) {
	return s.commit(ctx, in)
}

type observedModelBody struct {
	io.ReadCloser
	read chan struct{}
	once sync.Once
}

func (b *observedModelBody) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.read) })
	return b.ReadCloser.Read(p)
}

// The full race lane runs packages concurrently. Await real channel milestones with
// a bounded budget that leaves room for instrumentation and scheduler contention.
const modelFixtureBarrierTimeout = 30 * time.Second

func waitModelBarrier(t *testing.T, ch, done, dependent <-chan struct{}, phase string) {
	t.Helper()
	if ch == done {
		done = nil
	}
	timer := time.NewTimer(modelFixtureBarrierTimeout)
	defer timer.Stop()
	unexpectedDependent := func() { t.Fatalf("unacknowledged result allowed dependent execution before %s", phase) }
	select {
	case <-ch:
	case <-dependent:
		unexpectedDependent()
	case <-done:
		// A lost acknowledgement can end the engine before this waiter is
		// scheduled, after the fixture has already signalled its barrier.
		select {
		case <-ch:
			return
		default:
		}
		select {
		case <-dependent:
			unexpectedDependent()
		default:
		}
		t.Fatalf("model fixture ended before %s", phase)
	case <-timer.C:
		select {
		case <-ch:
			return
		default:
		}
		t.Fatalf("model fixture did not reach %s within %s", phase, modelFixtureBarrierTimeout)
	}
}
func TestDurableModelActualEinoWaitsForTerminalAndAcknowledgement(t *testing.T) {
	for _, mode := range []string{"acknowledged", "lost-ack", "missing-terminal", "cancel-body"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			memory := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
			headers, reading, ack, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
			var unblock sync.Once
			defer unblock.Do(func() { close(release) })
			var calls, tools atomic.Int32
			dependent := make(chan struct{})
			var dependentOnce sync.Once
			markDependent := func() { dependentOnce.Do(func() { close(dependent) }) }
			var events []contract.RunEvent
			store := engineModelStore{RunStore: memory}
			armed := true
			store.commit = func(given context.Context, in runcontext.RunCommit) (runcontext.RunReceipt, error) {
				receipt, err := memory.Commit(given, in)
				if err != nil {
					return receipt, err
				}
				var cp struct{ Durable runcontext.DurableBook }
				if err = json.Unmarshal(in.Checkpoint, &cp); err != nil {
					return receipt, err
				}
				// Only the model-only outcome commit is this acknowledgement. A later
				// event or tool-claim commit cannot substitute for the model barrier.
				if armed && (mode == "acknowledged" || mode == "lost-ack") && len(in.Events) == 0 && len(cp.Durable.Dispatches) == 1 && cp.Durable.Dispatches[0].Kind == "model" && cp.Durable.Dispatches[0].State == "result" {
					armed = false
					close(ack)
					if mode == "lost-ack" {
						return runcontext.RunReceipt{}, errors.New("private lost acknowledgement fixture")
					}
					select {
					case <-release:
					case <-given.Done():
						return runcontext.RunReceipt{}, given.Err()
					}
				}
				return receipt, nil
			}
			reader, bodyWriter := io.Pipe()
			defer bodyWriter.Close()
			effect := "read"
			g := gate.New(gate.Options{})
			g.SetOffer(contract.ToolOffer{Tools: []contract.ToolOfferRuntimeTool{}, HostTools: []contract.ToolOfferHostTool{{Name: "read_fixture", Description: "Read fixture", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`), Effect: &effect}}})
			run := &adapter.Run{Wire: "openai-compatible", ModelName: "fixture", Thinking: "off", Prompt: "Read the fixture then answer.", AllowConversation: true, Critic: "off", Gate: g.Calls(), Tools: []contract.ToolOfferRuntimeTool{},
				Context: &runcontext.Input{CurrentRequest: &runcontext.CurrentRequest{Message: "Read fixture", Input: "Read fixture"}, RunStore: &runcontext.DurableRunInput{Store: store, ID: "model-engine"}},
				HostTools: []runcontext.HostTool{{Name: "read_fixture", Description: "Read fixture", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`), Effect: "read", Execute: func(context.Context, json.RawMessage) (runcontext.ToolResult, error) {
					markDependent()
					tools.Add(1)
					return runcontext.ResultOf([]runcontext.Content{runcontext.TextContent("fixture result")}, nil), nil
				}}},
				Emit: func(e contract.RunEvent) { events = append(events, e) }, Model: func(context.Context, string, relay.Request) (*http.Response, error) {
					if calls.Add(1) == 1 {
						close(headers)
						return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: &observedModelBody{ReadCloser: reader, read: reading}}, nil
					}
					markDependent()
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(durableFinalResponse))}, nil
				}}
			var runErr error
			go func() {
				defer close(done)
				runErr = eino.New().Run(ctx, run)
			}()
			t.Cleanup(func() {
				cancel()
				bodyWriter.CloseWithError(context.Canceled)
				reader.CloseWithError(context.Canceled)
				unblock.Do(func() { close(release) })
				timer := time.NewTimer(modelFixtureBarrierTimeout)
				defer timer.Stop()
				select {
				case <-done:
				case <-timer.C:
					t.Error("model fixture did not drain after cancellation")
				}
			})
			waitModelBarrier(t, headers, done, nil, "response headers")
			select {
			case <-reading:
			case <-ack:
				t.Fatal("headers promoted a claim before the original body was read")
			case <-done:
				t.Fatal("model fixture ended before the original body was read")
			case <-time.After(modelFixtureBarrierTimeout):
				t.Fatal("neither body read nor acknowledgement reached")
			}
			head, err := memory.Load(context.Background(), "model-engine")
			if err != nil || head == nil {
				t.Fatal("claim missing before body")
			}
			var cp struct{ Durable runcontext.DurableBook }
			if err = json.Unmarshal(head.Checkpoint, &cp); err != nil {
				t.Fatal(err)
			}
			if len(cp.Durable.Dispatches) != 1 || cp.Durable.Dispatches[0].State != "claimed" || tools.Load() != 0 || calls.Load() != 1 {
				t.Fatal("headers promoted claim or allowed dependent execution")
			}
			if mode == "cancel-body" {
				cancel()
			} else {
				body := durableToolResponse
				if mode == "missing-terminal" {
					body = strings.Replace(body, "data: [DONE]\n\n", "", 1)
				}
				if _, err = io.WriteString(bodyWriter, body); err != nil {
					t.Fatal(err)
				}
				bodyWriter.Close()
				if mode == "acknowledged" || mode == "lost-ack" {
					waitModelBarrier(t, ack, done, dependent, "result acknowledgement")
					if tools.Load() != 0 || calls.Load() != 1 {
						t.Fatal("unacknowledged result allowed dependent execution")
					}
					unblock.Do(func() { close(release) })
				}
			}
			waitModelBarrier(t, done, done, nil, "engine completion after result release")
			if runErr != nil {
				t.Error(runErr)
			}
			if mode == "acknowledged" {
				if tools.Load() != 1 || calls.Load() != 2 {
					t.Fatalf("valid terminal/ack: tools=%d models=%d", tools.Load(), calls.Load())
				}
				for _, e := range events {
					if e.Error != nil {
						t.Errorf("valid fixture failed: %s", e.Error.Message)
					}
				}
			} else {
				if tools.Load() != 0 || calls.Load() != 1 {
					t.Fatal("unknown model outcome dispatched dependent work")
				}
				if mode == "lost-ack" {
					found := false
					for _, e := range events {
						if e.Error != nil && strings.Contains(e.Error.Message, "StorageOutcomeUnknown:") {
							found = true
						}
					}
					if !found {
						t.Fatal("lost result acknowledgement lost its named uncertainty")
					}
				}
			}
		})
	}
}
