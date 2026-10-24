package delegate_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/agent"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/delegate"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/turn"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
)

// These fixtures are handwritten. No provider, socket or captured execution supplies authority.
type lifecycleStore struct {
	runcontext.RunStore
	commit func(context.Context, runcontext.RunCommit) (runcontext.RunReceipt, error)
}

func (s lifecycleStore) Commit(ctx context.Context, in runcontext.RunCommit) (runcontext.RunReceipt, error) {
	return s.commit(ctx, in)
}
func lifecycleBook(t *testing.T, raw []byte) runcontext.DurableBook {
	t.Helper()
	var cp struct {
		Durable runcontext.DurableBook `json:"durable"`
	}
	if err := json.Unmarshal(raw, &cp); err != nil {
		t.Fatal(err)
	}
	return cp.Durable
}
func lifecycleRun(store runcontext.RunStore, id string, resume bool, provider adapter.AgentRun, emit func(contract.RunEvent)) *adapter.Run {
	g := gate.New(gate.Options{})
	g.SetOffer(contract.ToolOffer{Tools: []contract.ToolOfferRuntimeTool{}, HostTools: []contract.ToolOfferHostTool{turn.DelegationTool(turn.TaskProfiles)}})
	return &adapter.Run{Gate: g.Calls(), ModelName: "fixture", Prompt: "Review", Critic: "off", AllowConversation: true, Interactive: true,
		Context: &runcontext.Input{CurrentRequest: &runcontext.CurrentRequest{Message: "Review", Input: "Review"}, RunStore: &runcontext.DurableRunInput{Store: store, ID: id, Resume: resume}}, Emit: emit,
		Children: func(context.Context, contract.DelegatedTaskProfile, func(contract.RunEvent)) (*gate.Calls, []contract.ToolOfferRuntimeTool, error) {
			return nil, nil, nil
		},
		Agent: &adapter.Agent{Model: "fixture", Run: provider}}
}
func lifecycleDelegate(ctx context.Context, request adapter.AgentRequest, cb adapter.AgentCallbacks) error {
	for _, tool := range request.Tools {
		if strings.HasPrefix(tool.Description, "delegate_tasks:") {
			_, err := cb.Tool(ctx, adapter.AgentToolCall{ID: "delegate-wire", Name: tool.Name, Arguments: json.RawMessage(`{"tasks":[{"title":"Review","profile":"review","objective":"Check claims."}]}`)})
			return err
		}
	}
	return errors.New("fixture did not receive delegation tool")
}
func lifecycleExecute(t *testing.T, ctx context.Context, run *adapter.Run) {
	t.Helper()
	if err := delegate.With(agent.New(), delegate.Options{}).Run(ctx, run); err != nil {
		t.Fatal(err)
	}
}

// Race checks run packages concurrently; fixture scheduling is not a lifecycle
// deadline. Keep bounded channel waits and report the exact missing milestone.
const lifecycleBarrierTimeout = 30 * time.Second

func lifecycleWait(t *testing.T, ch, done <-chan struct{}, phase string) {
	t.Helper()
	timer := time.NewTimer(lifecycleBarrierTimeout)
	defer timer.Stop()
	select {
	case <-ch:
	case <-done:
		// The expected milestone may already have closed before engine completion.
		select {
		case <-ch:
			return
		default:
		}
		t.Fatalf("invocation ended before %s", phase)
	case <-timer.C:
		select {
		case <-ch:
			return
		default:
		}
		t.Fatalf("fixture did not reach %s within %s", phase, lifecycleBarrierTimeout)
	}
}

func TestStorageRootCancellationStillStopsProviderDispatch(t *testing.T) {
	for _, at := range []string{"before-claim-ack", "late-claim-receipt", "after-authorize"} {
		t.Run(at, func(t *testing.T) {
			ctx, stop := context.WithCancel(context.Background())
			defer stop()
			memory := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
			injected := false
			claimAcknowledged := false
			store := lifecycleStore{RunStore: memory}
			store.commit = func(given context.Context, in runcontext.RunCommit) (runcontext.RunReceipt, error) {
				book := lifecycleBook(t, in.Checkpoint)
				claim := len(book.Dispatches) == 1 && book.Dispatches[0].Kind == "agent" && book.Dispatches[0].State == "claimed"
				if claim && !injected && at != "after-authorize" {
					injected = true
					if at == "before-claim-ack" {
						stop()
						return memory.Commit(given, in)
					}
					receipt, err := memory.Commit(given, in)
					stop()
					return receipt, err
				}
				receipt, err := memory.Commit(given, in)
				if err == nil && claim {
					claimAcknowledged = true
				}
				return receipt, err
			}
			calls := 0
			run := lifecycleRun(store, "cancel-claim", false, func(context.Context, adapter.AgentRequest, adapter.AgentCallbacks) error { calls++; return nil }, func(contract.RunEvent) {})
			if at == "after-authorize" {
				run.Context.Authorize = func(context.Context) error {
					if claimAcknowledged {
						injected = true
						stop()
					}
					return nil
				}
			}
			lifecycleExecute(t, ctx, run)
			if !injected || calls != 0 {
				t.Fatalf("cancellation injected=%v provider calls=%d", injected, calls)
			}
		})
	}
}

func TestStorageInvalidProviderReleaseNeverAuthorizesResume(t *testing.T) {
	for _, mode := range []string{"wrong", "duplicate", "late"} {
		t.Run(mode, func(t *testing.T) {
			memory := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
			calls := 0
			var release *adapter.ProviderRelease
			provider := func(ctx context.Context, request adapter.AgentRequest, cb adapter.AgentCallbacks) error {
				calls++
				release = cb.ProviderRelease
				err := lifecycleDelegate(ctx, request, cb)
				switch mode {
				case "wrong":
					other, _ := adapter.ProviderReleaseAttempt()
					if release.Acknowledge(other.Invocation) {
						t.Error("wrong token acknowledged")
					}
				case "duplicate":
					if !release.Acknowledge(release.Invocation) || release.Acknowledge(release.Invocation) {
						t.Error("duplicate token accepted")
					}
				}
				return err
			}
			events := []contract.RunEvent{}
			lifecycleExecute(t, context.Background(), lifecycleRun(memory, "invalid-release", false, provider, func(e contract.RunEvent) { events = append(events, e) }))
			if release == nil || release.Acknowledge(release.Invocation) {
				t.Fatal("late release accepted")
			}
			found := false
			for _, e := range events {
				if e.Error != nil && strings.Contains(e.Error.Message, "ProviderReleaseUnknown:") {
					found = true
				}
			}
			if !found || calls != 1 {
				t.Fatalf("named refusal=%v provider calls=%d", found, calls)
			}
			head, err := memory.Load(context.Background(), "invalid-release")
			if err != nil || head == nil {
				t.Fatal("refusal checkpoint missing")
			}
			if !strings.Contains(string(head.Checkpoint), "ProviderReleaseUnknown:") {
				t.Fatal("release refusal not durable")
			}
			lifecycleExecute(t, context.Background(), lifecycleRun(memory, "invalid-release", true, provider, func(contract.RunEvent) {}))
			if calls != 1 {
				t.Fatal("resume minted release authority")
			}
		})
	}
}

func TestStorageLostPauseOutcomeAcknowledgementNeverReplaysIntent(t *testing.T) {
	memory := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
	lost := errors.New("fixture lost outcome acknowledgement")
	injected := false
	store := lifecycleStore{RunStore: memory}
	store.commit = func(ctx context.Context, in runcontext.RunCommit) (runcontext.RunReceipt, error) {
		receipt, err := memory.Commit(ctx, in)
		if err != nil {
			return receipt, err
		}
		book := lifecycleBook(t, in.Checkpoint)
		if !injected && book.PendingHandoff != nil && len(book.Dispatches) == 1 && book.Dispatches[0].State == "unknown" {
			injected = true
			return runcontext.RunReceipt{}, lost
		}
		return receipt, nil
	}
	calls := 0
	provider := func(ctx context.Context, request adapter.AgentRequest, cb adapter.AgentCallbacks) error {
		calls++
		defer cb.ProviderRelease.Acknowledge(cb.ProviderRelease.Invocation)
		return lifecycleDelegate(ctx, request, cb)
	}
	events := []contract.RunEvent{}
	lifecycleExecute(t, context.Background(), lifecycleRun(store, "lost-pause", false, provider, func(e contract.RunEvent) { events = append(events, e) }))
	if !injected || calls != 1 {
		t.Fatalf("lost acknowledgement injected=%v calls=%d", injected, calls)
	}
	found := false
	for _, e := range events {
		if e.Error != nil && strings.Contains(e.Error.Message, "StorageOutcomeUnknown:") {
			found = true
		}
	}
	if !found {
		details := []string{}
		for _, e := range events {
			if e.Error != nil {
				details = append(details, e.Error.Message)
			}
		}
		t.Fatalf("missing storage outcome uncertainty; errors=%v", details)
	}
	head, _ := memory.Load(context.Background(), "lost-pause")
	if head == nil || lifecycleBook(t, head.Checkpoint).PendingHandoff == nil {
		t.Fatal("lost acknowledgement erased intent")
	}
	lifecycleExecute(t, context.Background(), lifecycleRun(store, "lost-pause", true, provider, func(contract.RunEvent) {}))
	if calls != 1 {
		t.Fatal("intent replayed after lost outcome acknowledgement")
	}
}

func TestStorageRootCancellationAfterReleaseStartsNoChild(t *testing.T) {
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	memory := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
	calls := 0
	provider := func(ctx context.Context, request adapter.AgentRequest, cb adapter.AgentCallbacks) error {
		calls++
		err := lifecycleDelegate(ctx, request, cb)
		if !cb.ProviderRelease.Acknowledge(cb.ProviderRelease.Invocation) {
			t.Error("valid release refused")
		}
		stop()
		return err
	}
	lifecycleExecute(t, ctx, lifecycleRun(memory, "cancel-release", false, provider, func(contract.RunEvent) {}))
	if calls != 1 {
		t.Fatalf("root cancellation dispatched %d providers", calls)
	}
	lifecycleExecute(t, context.Background(), lifecycleRun(memory, "cancel-release", true, provider, func(contract.RunEvent) {}))
	if calls != 1 {
		t.Fatal("cancelled intent resumed as released")
	}
}

func TestStorageAttemptCallbacksCloseBeforeRetry(t *testing.T) {
	memory := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
	calls := 0
	var old adapter.AgentCallbacks
	var oldTool string
	provider := func(ctx context.Context, request adapter.AgentRequest, cb adapter.AgentCallbacks) error {
		calls++
		if calls == 1 {
			old = cb
			for _, tool := range request.Tools {
				if strings.HasPrefix(tool.Description, "delegate_tasks:") {
					oldTool = tool.Name
				}
			}
			return adapter.ErrBusy
		}
		if err := old.Event(ctx, adapter.AgentEvent{Type: "message", ID: "old", Text: "Stale answer.", Phase: "final"}); err == nil {
			t.Error("old event callback reached new attempt")
		}
		if _, err := old.Tool(ctx, adapter.AgentToolCall{ID: "old-call", Name: oldTool, Arguments: json.RawMessage(`{"tasks":[{"title":"Review","profile":"review","objective":"Check claims."}]}`)}); err == nil {
			t.Error("old tool callback reached new attempt")
		}
		if ctx.Err() != nil {
			t.Error("old callback cancelled current attempt")
		}
		cb.ProviderRelease.Acknowledge(cb.ProviderRelease.Invocation)
		return cb.Event(ctx, adapter.AgentEvent{Type: "message", ID: "current", Text: "Claims agree.", Phase: "final"})
	}
	events := []contract.RunEvent{}
	lifecycleExecute(t, context.Background(), lifecycleRun(memory, "retry-window", false, provider, func(e contract.RunEvent) { events = append(events, e) }))
	if calls != 2 {
		t.Fatalf("provider calls=%d", calls)
	}
	for _, e := range events {
		if e.Error != nil {
			t.Errorf("retry failed: %s", e.Error.Message)
		}
	}
}

func TestStorageProviderOutcomeWaitsForAdmittedCallback(t *testing.T) {
	for _, cancelRoot := range []bool{false, true} {
		name := "drain"
		if cancelRoot {
			name = "cancel-root"
		}
		t.Run(name, func(t *testing.T) {
			ctx, stop := context.WithCancel(context.Background())
			defer stop()
			memory := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
			entered, returned, release, callbackDone, done, outcome := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
			var releaseOnce, outcomeOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			defer unblock()
			var callbackStarted, callbackActive atomic.Bool
			var early atomic.Bool
			store := lifecycleStore{RunStore: memory}
			store.commit = func(given context.Context, in runcontext.RunCommit) (runcontext.RunReceipt, error) {
				book := lifecycleBook(t, in.Checkpoint)
				if len(book.Dispatches) > 0 && book.Dispatches[0].Kind == "agent" && book.Dispatches[0].State != "claimed" {
					if callbackActive.Load() {
						early.Store(true)
					}
					outcomeOnce.Do(func() { close(outcome) })
				}
				return memory.Commit(given, in)
			}
			var cbSaved adapter.AgentCallbacks
			provider := func(given context.Context, request adapter.AgentRequest, cb adapter.AgentCallbacks) error {
				cbSaved = cb
				if err := cb.Event(given, adapter.AgentEvent{Type: "message", ID: "final", Text: "Claims agree.", Phase: "final"}); err != nil {
					return err
				}
				alias := ""
				for _, tool := range request.Tools {
					if strings.HasPrefix(tool.Description, "read_fixture:") {
						alias = tool.Name
					}
				}
				if alias == "" {
					return errors.New("fixture read not offered")
				}
				callbackStarted.Store(true)
				go func() {
					defer close(callbackDone)
					_, _ = cb.Tool(given, adapter.AgentToolCall{ID: "read-wire", Name: alias, Arguments: json.RawMessage(`{}`)})
				}()
				select {
				case <-entered:
				case <-callbackDone:
					return errors.New("fixture callback refused before host execution")
				}
				if !cb.ProviderRelease.Acknowledge(cb.ProviderRelease.Invocation) {
					return errors.New("fixture provider release was refused")
				}
				close(returned)
				return nil
			}
			run := lifecycleRun(store, "callback-drain", false, provider, func(contract.RunEvent) {})
			run.HostTools = []runcontext.HostTool{{Name: "read_fixture", Description: "Read a fixture", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`), Effect: "read", Execute: func(given context.Context, _ json.RawMessage) (runcontext.ToolResult, error) {
				callbackActive.Store(true)
				defer callbackActive.Store(false)
				close(entered)
				select {
				case <-release:
					return runcontext.ResultOf([]runcontext.Content{runcontext.TextContent("fixture")}, nil), nil
				case <-given.Done():
					return runcontext.ToolResult{}, given.Err()
				}
			}}}
			g := gate.New(gate.Options{})
			effect := "read"
			g.SetOffer(contract.ToolOffer{Tools: []contract.ToolOfferRuntimeTool{}, HostTools: []contract.ToolOfferHostTool{turn.DelegationTool(turn.TaskProfiles), {Name: "read_fixture", Description: "Read a fixture", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`), Effect: &effect}}})
			run.Gate = g.Calls()
			var runErr error
			go func() {
				defer close(done)
				runErr = delegate.With(agent.New(), delegate.Options{}).Run(ctx, run)
			}()
			t.Cleanup(func() {
				stop()
				unblock()
				lifecycleWait(t, done, nil, "cancelled invocation shutdown")
				if callbackStarted.Load() {
					lifecycleWait(t, callbackDone, nil, "admitted callback shutdown")
				}
			})
			lifecycleWait(t, returned, done, "provider return after callback admission")
			if cancelRoot {
				stop()
			} else {
				// The host has returned with a live admitted callback. Keep it blocked
				// for the same bounded phase budget: absence of an outcome during an
				// arbitrary short scheduler interval is not evidence of callback drain.
				observation := time.NewTimer(lifecycleBarrierTimeout)
				select {
				case <-outcome:
					t.Error("provider outcome committed while callback was blocked")
				case <-observation.C:
				}
				observation.Stop()
			}
			unblock()
			// The callback wrapper can release its drain bookkeeping just before
			// this fixture goroutine closes callbackDone. Neither close orders the other.
			lifecycleWait(t, callbackDone, nil, "admitted callback completion")
			lifecycleWait(t, done, nil, "provider outcome completion after callback drain")
			if runErr != nil {
				t.Fatal(runErr)
			}
			if early.Load() {
				t.Error("provider outcome preceded callback drain")
			}
			if err := cbSaved.Event(context.Background(), adapter.AgentEvent{Type: "message", Text: "Late"}); err == nil {
				t.Error("callback accepted after invocation ended")
			}
		})
	}
}
