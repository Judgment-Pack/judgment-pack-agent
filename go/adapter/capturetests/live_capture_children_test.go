package capturetests

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/eino"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/relay"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func scopedCaptureRoute(run *adapter.Run) {
	tier := "off"
	run.Policy = runcontext.PolicyLayer{"version": 2, "routing": map[string]any{"mode": "rules", "tiers": []string{"off"}}}
	run.Route = &runcontext.RouteSelection{Route: "agent", Tier: &tier, Skills: []string{}, Tools: []string{}, Operations: "all", Planning: "withheld"}
	run.Context.Objective = ""
	run.Context.Connection = "provider"
	run.Context.ConnectionRevisions = map[string]string{"provider": "one"}
}
func namedCaptureReply(text string) *http.Response {
	content, _ := json.Marshal(text)
	body := fmt.Sprintf("data: {\"id\":\"fixture\",\"object\":\"chat.completion.chunk\",\"model\":\"fixture\",\"choices\":[{\"index\":0,\"delta\":{\"content\":%s},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n", content)
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}
}
func TestOriginalLiveCaptureConcurrentChildren(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
	sink := &originalCaptureSink{}
	root := originalCaptureRun(store, sink)
	scopedCaptureRoute(root)
	prepared, e := runcontext.PrepareDurableInput(ctx, root.Context)
	if e != nil {
		t.Fatal(e)
	}
	root.Context = prepared
	rootEntered, firstEntered, rootGate, firstGate := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	rootDone, firstDone := make(chan error, 1), make(chan error, 1)
	root.Model = func(given context.Context, _ string, _ relay.Request) (*http.Response, error) {
		close(rootEntered)
		select {
		case <-rootGate:
			return namedCaptureReply("Root answer."), nil
		case <-given.Done():
			return nil, given.Err()
		}
	}
	go func() { rootDone <- eino.New().Run(ctx, root) }()
	select {
	case <-rootEntered:
	case <-time.After(10 * time.Second):
		t.Fatal("root physical admission not reached")
	}
	child := func(id string, wait bool) *adapter.Run {
		input := &runcontext.Input{CurrentRequest: &runcontext.CurrentRequest{Message: "Read", Input: "Fixed"}}
		runcontext.ChildPersistence(root.Context, input, id)
		run := &adapter.Run{Wire: "openai-compatible", ModelName: "fixture", Thinking: "off", Prompt: "Read.", AllowConversation: true, Critic: "off", Context: input, Emit: func(contract.RunEvent) {}}
		scopedCaptureRoute(run)
		run.Model = func(given context.Context, _ string, _ relay.Request) (*http.Response, error) {
			if wait {
				close(firstEntered)
				select {
				case <-firstGate:
				case <-given.Done():
					return nil, given.Err()
				}
			}
			return namedCaptureReply(id + " answer."), nil
		}
		return run
	}
	first := child("first-child", true)
	go func() { firstDone <- eino.New().Run(ctx, first) }()
	select {
	case <-firstEntered:
	case <-time.After(10 * time.Second):
		t.Fatal("first child physical admission not reached")
	}
	if e = eino.New().Run(ctx, child("second-child", false)); e != nil {
		t.Fatal(e)
	}
	if sink.calls != 0 {
		t.Fatal("child finalized original root capture")
	}
	head, e := store.ReadHistoryHead(ctx, "original-live")
	if e != nil || head.Metadata != nil {
		t.Fatal("child published root metadata", e)
	}
	close(firstGate)
	if e = <-firstDone; e != nil {
		t.Fatal(e)
	}
	if sink.calls != 0 {
		t.Fatal("first child finalized original root")
	}
	close(rootGate)
	if e = <-rootDone; e != nil {
		t.Fatal(e)
	}
	if sink.calls != 1 {
		t.Fatal("original root did not publish complete child capture")
	}
	view, e := runcontext.ReadRetainedRunHistory(ctx, store, "original-live")
	if e != nil {
		t.Fatal(e)
	}
	capture, e := runcontext.ReadCapturedCorpusView(view)
	if e != nil {
		t.Fatal(e)
	}
	var rows []struct {
		Response struct {
			Body string `json:"body"`
		} `json:"response"`
	}
	_ = json.Unmarshal(capture.Entry.Requests, &rows)
	if len(rows) != 3 || !strings.Contains(rows[0].Response.Body, "Root answer.") || !strings.Contains(rows[1].Response.Body, "first-child answer.") || !strings.Contains(rows[2].Response.Body, "second-child answer.") {
		t.Fatal("capture followed finish order instead of admission order")
	}
}
