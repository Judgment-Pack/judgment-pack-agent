package capturetests

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	native "github.com/Judgment-Pack/judgment-pack-agent/go/adapter/agent"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/eino"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
	"github.com/Judgment-Pack/judgment-pack-agent/go/relay"
)

type captureLifecycleStore struct {
	*runcontext.MemoryRunStore
	point         string
	fired         bool
	metadataCalls int
	staleCAS      bool
}

func (s *captureLifecycleStore) CommitHistory(ctx context.Context, in runcontext.RunHistoryCommit) (runcontext.RunHistoryReceipt, error) {
	var cp struct {
		Durable struct {
			Dispatches []struct {
				Kind  string `json:"kind"`
				State string `json:"state"`
			} `json:"dispatches"`
		} `json:"durable"`
	}
	_ = json.Unmarshal(in.Run.Checkpoint, &cp)
	state := ""
	for _, d := range cp.Durable.Dispatches {
		if d.Kind == "model" {
			state = d.State
			break
		}
	}
	end := false
	for _, raw := range in.Run.Events {
		var row struct {
			Event struct {
				Type string `json:"type"`
			} `json:"event"`
		}
		_ = json.Unmarshal(raw, &row)
		end = end || row.Event.Type == "end"
	}
	hit := !s.fired && (strings.HasPrefix(s.point, "claim") && state == "claimed" || strings.HasPrefix(s.point, "result") && state == "result" || strings.HasPrefix(s.point, "end") && end)
	if hit {
		s.fired = true
		if strings.HasSuffix(s.point, "before") {
			return runcontext.RunHistoryReceipt{}, errors.New("fixture before ACK")
		}
	}
	receipt, err := s.MemoryRunStore.CommitHistory(ctx, in)
	if hit && err == nil {
		return runcontext.RunHistoryReceipt{}, errors.New("fixture lost ACK")
	}
	return receipt, err
}
func (s *captureLifecycleStore) CommitHistoryMetadata(ctx context.Context, in runcontext.HistoryMetadataCommit) (runcontext.RunHistoryHead, error) {
	s.metadataCalls++
	if s.point == "metadata-CAS" {
		s.fired = true
		head, err := s.ReadHistoryHead(ctx, in.RunID)
		if err != nil {
			return runcontext.RunHistoryHead{}, err
		}
		_, err = s.MemoryRunStore.CommitHistoryMetadata(ctx, runcontext.HistoryMetadataCommit{RunID: in.RunID, ExpectedHistoryRevision: head.Revision, CommitID: "fixture-rating-race", Record: runcontext.HistoryMetadata{Version: 1, Kind: "feedback", Feedback: &runcontext.RunFeedback{Version: 1, ID: "rating", RunID: in.RunID, Sequence: 1, Target: runcontext.FeedbackTarget{Kind: "run"}, Rating: "good", Actor: "fixture", RecordedAt: "2026-10-26T00:00:00.000Z"}}})
		if err != nil {
			return runcontext.RunHistoryHead{}, err
		}
		result, err := s.MemoryRunStore.CommitHistoryMetadata(ctx, in)
		var conflict *runcontext.StorageConflict
		s.staleCAS = errors.As(err, &conflict)
		return result, err
	}
	head, e := s.MemoryRunStore.CommitHistoryMetadata(ctx, in)
	if s.point == "metadata-lost" && e == nil {
		s.fired = true
		return runcontext.RunHistoryHead{}, errors.New("fixture metadata lost ACK")
	}
	return head, e
}

type captureErrorSink struct{ calls int }

func (s *captureErrorSink) WriteCorpus(string, map[string][]byte) error {
	s.calls++
	return errors.New("fixture sink unavailable")
}
func TestOriginalCaptureAcknowledgementBoundaries(t *testing.T) {
	for _, point := range []string{"claim-before", "claim-after", "result-before", "result-after", "end-before", "end-after", "metadata-CAS", "metadata-lost", "sink-error"} {
		t.Run(point, func(t *testing.T) {
			store := &captureLifecycleStore{MemoryRunStore: runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{}), point: point}
			sink := &originalCaptureSink{}
			badSink := &captureErrorSink{}
			run := originalCaptureRun(store, sink)
			run.Context.Objective = ""
			tier := "off"
			run.Policy = runcontext.PolicyLayer{"version": 2, "routing": map[string]any{"mode": "rules", "tiers": []string{"off"}}}
			run.Route = &runcontext.RouteSelection{Route: "agent", Tier: &tier, Tools: []string{}, Skills: []string{}, Operations: "all", Planning: "withheld"}
			if point == "sink-error" {
				run.Context.Capture.Sink = badSink
			}
			calls := 0
			run.Model = func(context.Context, string, relay.Request) (*http.Response, error) {
				calls++
				return usageReply(), nil
			}
			recorded := false
			run.Emit = func(e contract.RunEvent) { recorded = recorded || e.Recorded != nil }
			_ = eino.New().Run(context.Background(), run)
			wantCalls := 1
			if strings.HasPrefix(point, "claim") {
				wantCalls = 0
			}
			if calls != wantCalls || recorded || sink.calls != 0 {
				t.Fatalf("physical=%d want=%d recorded=%v sink=%d", calls, wantCalls, recorded, sink.calls)
			}
			wantMeta := 0
			if strings.HasPrefix(point, "metadata") || point == "sink-error" {
				wantMeta = 1
			}
			if store.metadataCalls != wantMeta {
				t.Fatalf("metadata attempts=%d want=%d", store.metadataCalls, wantMeta)
			}
			head, e := store.ReadHistoryHead(context.Background(), "original-live")
			if e != nil {
				t.Fatal(e)
			}
			count := 0
			if head != nil && head.Metadata != nil {
				count = head.Metadata.Count
			}
			wantCount := 0
			if point == "metadata-CAS" || point == "metadata-lost" || point == "sink-error" {
				wantCount = 1
			}
			if point == "metadata-CAS" {
				if !store.staleCAS {
					t.Fatal("capture did not reach original stale CAS")
				}
				page, err := store.ReadHistoryMetadataPage(context.Background(), "original-live", head.Revision, 0, 10)
				if err != nil || len(page.Items) != 1 || page.Items[0].Record.Kind != "feedback" {
					t.Fatal("CAS race published capture instead of preserving feedback", err)
				}
			}
			if count != wantCount {
				t.Fatalf("published metadata=%d want=%d", count, wantCount)
			}
			if point == "sink-error" {
				if badSink.calls != 1 {
					t.Fatal("delivery retried or skipped", badSink.calls)
				}
			} else if !store.fired {
				t.Fatal("target transaction not reached")
			}
		})
	}
}

func TestOriginalCaptureResumedMissingFrames(t *testing.T) {
	ctx := context.Background()
	store := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
	sink := &originalCaptureSink{}
	run := originalCaptureRun(store, sink)
	scopedCaptureRoute(run)
	run.Context.Capture = nil
	calls := 0
	run.Model = func(context.Context, string, relay.Request) (*http.Response, error) {
		calls++
		return usageReply(), nil
	}
	_ = eino.New().Run(ctx, run)
	if calls != 1 {
		t.Fatal("first original request absent")
	}
	resumed := originalCaptureRun(store, sink)
	scopedCaptureRoute(resumed)
	resumed.Context.RunStore.Resume = true
	resumed.Model = run.Model
	_ = eino.New().Run(ctx, resumed)
	head, e := store.ReadHistoryHead(ctx, "original-live")
	if e != nil || sink.calls != 0 || head.Metadata != nil {
		t.Fatal("resumed missing frames published capture", sink.calls, e)
	}
}
func TestOriginalCaptureNativeProviderReleaseIsNotResultAck(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
	sink := &originalCaptureSink{}
	run := originalCaptureRun(store, sink)
	run.Context.Objective = ""
	run.Model = nil
	tier := "low"
	run.Policy = runcontext.PolicyLayer{"version": 2, "routing": map[string]any{"mode": "rules", "tiers": []string{"low"}}}
	run.Route = &runcontext.RouteSelection{Route: "agent", Tier: &tier, Tools: []string{}, Skills: []string{}, Operations: "all", Planning: "withheld"}
	entered, release := make(chan struct{}), make(chan struct{})
	run.Agent = &adapter.Agent{Model: "fixture", Run: func(ctx context.Context, _ adapter.AgentRequest, cb adapter.AgentCallbacks) error {
		if cb.ProviderRelease == nil {
			return errors.New("original release token absent")
		}
		if !cb.ProviderRelease.Acknowledge(cb.ProviderRelease.Invocation) {
			return errors.New("release identity guard failed")
		}
		if e := cb.Event(ctx, adapter.AgentEvent{Type: "message", ID: "final", Text: "Done.", Phase: "final"}); e != nil {
			return e
		}
		close(entered)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	done := make(chan error, 1)
	go func() { done <- native.Run(ctx, run) }()
	select {
	case <-entered:
	case e := <-done:
		t.Fatal("provider did not reach release boundary", e)
	case <-time.After(10 * time.Second):
		t.Fatal("provider boundary timed out")
	}
	head, e := store.ReadHistoryHead(ctx, "original-live")
	if e != nil || sink.calls != 0 || head.Metadata != nil {
		t.Fatal("provider release published capture early", e)
	}
	close(release)
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	if sink.calls != 1 {
		t.Fatal("verified terminal capture absent", sink.calls)
	}
}

type captureNativeRetryStore struct {
	*runcontext.MemoryRunStore
	original string
	armed    bool
}

func (s *captureNativeRetryStore) CommitHistory(ctx context.Context, in runcontext.RunHistoryCommit) (runcontext.RunHistoryReceipt, error) {
	receipt, e := s.MemoryRunStore.CommitHistory(ctx, in)
	if e != nil || !s.armed {
		return receipt, e
	}
	var cp struct {
		Durable struct {
			Dispatches []struct{ Kind, State, Request string } `json:"dispatches"`
		} `json:"durable"`
	}
	_ = json.Unmarshal(in.Run.Checkpoint, &cp)
	for _, row := range cp.Durable.Dispatches {
		if row.Kind == "agent" && row.State == "claimed" {
			s.armed = false
			s.original = row.Request
			return runcontext.RunHistoryReceipt{}, errors.New("fixture lost native claim ACK")
		}
	}
	return receipt, nil
}
func TestOriginalCaptureNativeLostClaimRetryIdentity(t *testing.T) {
	ctx := context.Background()
	store := &captureNativeRetryStore{MemoryRunStore: runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{}), armed: true}
	sink := &originalCaptureSink{}
	requests := []string{}
	provider := func(ctx context.Context, r adapter.AgentRequest, cb adapter.AgentCallbacks) error {
		raw, _ := json.Marshal(r)
		requests = append(requests, string(raw))
		if cb.ProviderRelease != nil {
			defer cb.ProviderRelease.Acknowledge(cb.ProviderRelease.Invocation)
		}
		return cb.Event(ctx, adapter.AgentEvent{Type: "message", ID: "final", Text: "Done.", Phase: "final"})
	}
	newRun := func(resume bool) *adapter.Run {
		run := originalCaptureRun(store, sink)
		run.Context.Objective = ""
		run.Context.RunStore.Resume = resume
		run.Model = nil
		tier := "low"
		run.Policy = runcontext.PolicyLayer{"version": 2, "routing": map[string]any{"mode": "rules", "tiers": []string{"low"}}}
		run.Route = &runcontext.RouteSelection{Route: "agent", Tier: &tier, Tools: []string{}, Skills: []string{}, Operations: "all", Planning: "withheld"}
		run.Agent = &adapter.Agent{Model: "fixture", Run: provider}
		return run
	}
	_ = native.Run(ctx, newRun(false))
	if len(requests) != 0 || store.original == "" {
		t.Fatal("unknown claim admitted provider")
	}
	_ = native.Run(ctx, newRun(true))
	if len(requests) != 1 || requests[0] != store.original {
		t.Fatal("native retry request identity changed")
	}
	head, e := store.ReadHistoryHead(ctx, "original-live")
	if e != nil || sink.calls != 0 || head.Metadata != nil {
		t.Fatal("resumed capture invented original frames", sink.calls, e)
	}
}
func TestOriginalCaptureWaitsAdmittedNativeCallbacks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
	sink := &originalCaptureSink{}
	run := originalCaptureRun(store, sink)
	run.Context.Objective = ""
	run.Model = nil
	tier := "low"
	run.Policy = runcontext.PolicyLayer{"version": 2, "routing": map[string]any{"mode": "rules", "tiers": []string{"low"}}}
	run.Route = &runcontext.RouteSelection{Route: "agent", Tier: &tier, Tools: []string{"lookup"}, Skills: []string{}, Operations: "all", Planning: "withheld"}
	entered, release, providerReturned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	tool := runcontext.HostTool{Name: "lookup", Description: "Read.", Effect: "read", Taxonomy: &runcontext.ToolTaxonomy{Operation: "read", Effect: "read"}, InputSchema: json.RawMessage(`{"type":"object","properties":{}}`), Execute: func(ctx context.Context, _ json.RawMessage) (runcontext.ToolResult, error) {
		close(entered)
		select {
		case <-release:
			return runcontext.ReadToolResult(runcontext.MustParseJSON(`{"content":[{"type":"text","text":"Value."}]}`)), nil
		case <-ctx.Done():
			return runcontext.ToolResult{}, ctx.Err()
		}
	}}
	g := gate.New(gate.Options{})
	g.SetOffer(contract.ToolOffer{Tools: []contract.ToolOfferRuntimeTool{}, HostTools: []contract.ToolOfferHostTool{tool.Declaration()}})
	run.Gate = g.Calls()
	run.HostTools = []runcontext.HostTool{tool}
	run.Agent = &adapter.Agent{Model: "fixture", Run: func(ctx context.Context, r adapter.AgentRequest, cb adapter.AgentCallbacks) error {
		go func() {
			_, _ = cb.Tool(ctx, adapter.AgentToolCall{ID: "lookup-call", Name: r.Tools[0].Name, Arguments: json.RawMessage(`{}`)})
		}()
		select {
		case <-entered:
		case <-ctx.Done():
			return ctx.Err()
		}
		err := cb.Event(ctx, adapter.AgentEvent{Type: "message", ID: "final", Text: "Done.", Phase: "final"})
		close(providerReturned)
		return err
	}}
	done := make(chan error, 1)
	go func() { done <- native.Run(ctx, run) }()
	select {
	case <-providerReturned:
	case e := <-done:
		t.Fatal("callback did not start", e)
	case <-time.After(10 * time.Second):
		t.Fatal("callback start timed out")
	}
	head, e := store.ReadHistoryHead(ctx, "original-live")
	if e != nil || sink.calls != 0 || head.Metadata != nil {
		t.Fatal("capture published before callback closed", e)
	}
	close(release)
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	if sink.calls != 1 {
		t.Fatal("complete native callback capture absent", sink.calls)
	}
}

func TestOriginalCaptureConfigurationBeforeAdmission(t *testing.T) {
	ctx := context.Background()
	store := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
	sink := &originalCaptureSink{}
	run := originalCaptureRun(store, sink)
	run.Context.Capture.Manifest.Policy["invalid"] = math.Inf(1)
	calls := 0
	run.Model = func(context.Context, string, relay.Request) (*http.Response, error) {
		calls++
		return usageReply(), nil
	}
	_ = eino.New().Run(ctx, run)
	if calls != 0 || sink.calls != 0 {
		t.Fatal("invalid capture configuration admitted provider")
	}
}
func TestOriginalCaptureEnrolledManifestDetached(t *testing.T) {
	ctx := context.Background()
	store := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
	sink := &originalCaptureSink{}
	run := originalCaptureRun(store, sink)
	scopedCaptureRoute(run)
	prepared, e := runcontext.PrepareDurableInput(ctx, run.Context)
	if e != nil {
		t.Fatal(e)
	}
	run.Context.Capture.Manifest.ID = "mutated-after-enrollment"
	run.Context = prepared
	run.Model = func(context.Context, string, relay.Request) (*http.Response, error) { return usageReply(), nil }
	if e := eino.New().Run(ctx, run); e != nil {
		t.Fatal(e)
	}
	if sink.calls != 1 {
		t.Fatal("original manifest capture absent", sink.calls)
	}
	var m runcontext.CorpusManifest
	_ = json.Unmarshal(sink.files["manifest.json"], &m)
	if m.ID != "original-live" {
		t.Fatal("enrolled manifest changed", m.ID)
	}
}

func TestOriginalCaptureInterruptedResponse(t *testing.T) {
	for _, point := range []string{"before-response", "after-response"} {
		t.Run(point, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			store := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
			sink := &originalCaptureSink{}
			run := originalCaptureRun(store, sink)
			scopedCaptureRoute(run)
			calls := 0
			run.Model = func(context.Context, string, relay.Request) (*http.Response, error) {
				calls++
				if point == "before-response" {
					cancel()
					return nil, ctx.Err()
				}
				response := usageReply()
				cancel()
				return response, nil
			}
			_ = eino.New().Run(ctx, run)
			head, e := store.ReadHistoryHead(context.Background(), "original-live")
			if e != nil || calls != 1 || sink.calls != 0 || head.Metadata != nil {
				t.Fatal("interrupted original response published capture", calls, sink.calls, e)
			}
		})
	}
}
