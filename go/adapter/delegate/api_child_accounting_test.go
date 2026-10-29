package delegate_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/delegate"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/eino"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/turn"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
	"github.com/Judgment-Pack/judgment-pack-agent/go/relay"
)

// monotoneStore is a host run store that keeps the root's request count monotone, as a host's
// canonical store may: a commit whose checkpoint counts fewer root requests than the one it saved
// last is refused. Handwritten; no provider or captured execution supplies it.
type monotoneStore struct {
	runcontext.RunStore
	mu       sync.Mutex
	saved    int64
	counts   []int64
	refusals []string
}

func (s *monotoneStore) Commit(ctx context.Context, in runcontext.RunCommit) (runcontext.RunReceipt, error) {
	var cp struct {
		Used *int64 `json:"used"`
	}
	if err := json.Unmarshal(in.Checkpoint, &cp); err != nil || cp.Used == nil {
		return runcontext.RunReceipt{}, errors.New("fixture: checkpoint without a root request count")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if *cp.Used < s.saved {
		s.refusals = append(s.refusals, "root request count regressed")
		return runcontext.RunReceipt{}, errors.New("fixture: root request count regressed")
	}
	receipt, err := s.RunStore.Commit(ctx, in)
	if err == nil {
		s.saved = *cp.Used
		s.counts = append(s.counts, *cp.Used)
	}
	return receipt, err
}

func apiStream(delta map[string]any, finish string) *http.Response {
	body, _ := json.Marshal(map[string]any{"id": "fixture", "object": "chat.completion.chunk", "model": "fixture",
		"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}, "usage": map[string]any{"prompt_tokens": 4, "completion_tokens": 2}})
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: " + string(body) + "\n\ndata: [DONE]\n\n"))}
}

// agent#249: on the API path (delegate.With over eino.New) each child's model request is charged
// to the root and checkpointed before it leaves. Two children whose scopes overlap both reach their
// provider, and every root count the host saves is monotone.
func TestAPIChildAccountingKeepsTheRootCountMonotone(t *testing.T) {
	store := &monotoneStore{RunStore: runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})}
	g := gate.New(gate.Options{})
	g.SetOffer(contract.ToolOffer{Tools: []contract.ToolOfferRuntimeTool{}, HostTools: []contract.ToolOfferHostTool{turn.DelegationTool(turn.TaskProfiles)}})
	var events []contract.RunEvent
	var eventsMu sync.Mutex
	run := &adapter.Run{Wire: "openai-compatible", ModelName: "fixture", Thinking: "off", Prompt: "Answer.", AllowConversation: true, Critic: "off", Interactive: true,
		Gate: g.Calls(),
		Context: &runcontext.Input{CurrentRequest: &runcontext.CurrentRequest{Message: "Answer", Input: "Answer"},
			RunStore: &runcontext.DurableRunInput{Store: store, ID: "api-children"}},
		Children: func(context.Context, contract.DelegatedTaskProfile, func(contract.RunEvent)) (*gate.Calls, []contract.ToolOfferRuntimeTool, error) {
			return nil, nil, nil
		},
		Emit: func(e contract.RunEvent) { eventsMu.Lock(); events = append(events, e); eventsMu.Unlock() }}
	var mu sync.Mutex
	parentCalls, childCalls := 0, 0
	arrived := make(chan struct{}, 2)
	overlapped := make(chan struct{})
	var once sync.Once
	run.Model = func(ctx context.Context, _ string, request relay.Request) (*http.Response, error) {
		if !bytes.Contains(request.Body, []byte(`"delegate_tasks"`)) {
			mu.Lock()
			childCalls++
			mu.Unlock()
			// Hold each child at its provider until its sibling arrives, so both scopes are open at once.
			arrived <- struct{}{}
			if len(arrived) == 2 {
				once.Do(func() { close(overlapped) })
			}
			select {
			case <-overlapped:
			case <-time.After(lifecycleBarrierTimeout):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return apiStream(map[string]any{"content": "Checked."}, "stop"), nil
		}
		mu.Lock()
		parentCalls++
		first := parentCalls == 1
		mu.Unlock()
		if first {
			args := `{"tasks":[{"title":"First","objective":"Check the first claim.","profile":"review"},{"title":"Second","objective":"Check the second claim.","profile":"review"}]}`
			return apiStream(map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "delegate-1", "type": "function", "function": map[string]any{"name": "delegate_tasks", "arguments": args}}}}, "tool_calls"), nil
		}
		return apiStream(map[string]any{"content": "Done."}, "stop"), nil
	}
	if err := delegate.With(eino.New(), delegate.Options{}).Run(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.refusals) != 0 {
		t.Fatalf("the host refused a root checkpoint: %v (saved counts %v)", store.refusals, store.counts)
	}
	mu.Lock()
	defer mu.Unlock()
	if childCalls != 2 || parentCalls != 2 {
		t.Fatalf("child provider calls=%d parent provider calls=%d, want 2 and 2", childCalls, parentCalls)
	}
	select {
	case <-overlapped:
	default:
		t.Fatal("the two child scopes did not overlap")
	}
	for i := 1; i < len(store.counts); i++ {
		if store.counts[i] < store.counts[i-1] {
			t.Fatalf("saved root counts regress: %v", store.counts)
		}
	}
	if last := store.counts[len(store.counts)-1]; last != 4 {
		t.Fatalf("final saved root count %d, want 4 (saved counts %v)", last, store.counts)
	}
	eventsMu.Lock()
	defer eventsMu.Unlock()
	for _, e := range events {
		if e.Error != nil {
			t.Fatalf("run failed: %s", e.Error.Message)
		}
	}
}
