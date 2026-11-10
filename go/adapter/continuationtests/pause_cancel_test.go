package continuationtests

// agent#342: a host that answers the package's own pause with its context's error, not the Tool error, still ends the run paused
// with the ask saved, and the ask is saved before the person is shown it. A storage failure and the caller's own cancellation end
// as they did.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

// stoppedByContext calls the tool and, where the Tool answers an error, returns its context's error once the context is done, as
// a provider loop does that stops on its context rather than on the Tool error.
func stoppedByContext(prefix, args string) step {
	return func(ctx context.Context, request adapter.AgentRequest, cb adapter.AgentCallbacks, _ int) error {
		for _, tool := range request.Tools {
			if strings.HasPrefix(tool.Description, prefix+":") {
				if _, err := cb.Tool(ctx, adapter.AgentToolCall{ID: "wire-" + prefix, Name: tool.Name, Arguments: json.RawMessage(args)}); err == nil {
					return nil
				}
				<-ctx.Done()
				return ctx.Err()
			}
		}
		return nil
	}
}

// hosts are the two answers a host gives once the Tool paused the run.
var hosts = []struct {
	name string
	host func(prefix, args string) step
}{{"the Tool error", call}, {"its context's error", stoppedByContext}}

const question = `{"title":"Note","question":"Which note should be written?","selection":"single","options":[{"id":"a","label":"The first","description":"One."},{"id":"b","label":"The second","description":"Two."}]}`

func askPerson() runcontext.HostTool {
	return runcontext.HostTool{Name: "ask_person", Description: "ask_person", Interaction: "question", InputSchema: json.RawMessage(`{"type":"object"}`), Taxonomy: &runcontext.ToolTaxonomy{Operation: "ask", Effect: "pure"}}
}

// stored is the run's head: its checkpoint, whatever the run reported.
func stored(t *testing.T, store runcontext.RunStore) contract.Checkpoint {
	t.Helper()
	head, err := store.Load(context.Background(), "run-c")
	if err != nil || head == nil {
		t.Fatal(head, err)
	}
	var saved contract.Checkpoint
	if err := json.Unmarshal(head.Checkpoint, &saved); err != nil {
		t.Fatal(err)
	}
	return saved
}

func held(saved contract.Checkpoint) string {
	if saved.Approval == nil {
		return saved.Status + "/none"
	}
	return saved.Status + "/" + saved.Approval.State
}

func (s *scene) count(kind func(contract.RunEvent) bool) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, event := range s.events {
		if kind(event) {
			n++
		}
	}
	return n
}

func asked(e contract.RunEvent) bool { return e.ApprovalRequested != nil }

func TestAnAskStoppedByItsOwnPauseIsSavedAndResumed(t *testing.T) {
	for _, h := range hosts {
		t.Run(h.name, func(t *testing.T) {
			s := &scene{store: runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})}
			shown := ""
			s.seen = func(e contract.RunEvent) {
				if e.ApprovalRequested != nil {
					shown = held(stored(t, s.store))
				}
			}
			if err := s.run(t, context.Background(), false, nil, false, h.host("writes__write_note", `{}`)); err != nil {
				t.Fatal(err)
			}
			saved := stored(t, s.store)
			// Saved paused before the person was shown it, and the run ended paused with no error.
			if shown != "paused/asked" || held(saved) != "paused/asked" || s.count(asked) != 1 || len(s.errors()) != 0 || s.ran != 0 {
				t.Fatalf("shown at %q, ended %q, asked %d, errors %v, ran %d", shown, held(saved), s.count(asked), s.errors(), s.ran)
			}
			for _, row := range saved.Durable.Dispatches {
				if row.Kind == "agent" && row.Failure != nil && row.Failure.Code == "cancelled" {
					t.Fatalf("the pause was recorded as a cancelled agent: %+v", row.Failure)
				}
			}
			proof := runcontext.ApprovalShownDigest(*saved.Approval)
			decision := &contract.CheckpointApprovalDecision{CallID: saved.Approval.CallID, Approved: true, ShownDigest: &proof}
			s.events = nil
			if err := s.run(t, context.Background(), true, decision, false); err != nil {
				t.Fatal(err)
			}
			if s.ran != 1 || len(s.errors()) != 0 || !s.finished() {
				t.Fatalf("the decision: ran %d, errors %v, finished %v", s.ran, s.errors(), s.finished())
			}
			s.events = nil
			if err := s.run(t, context.Background(), true, decision, false); err != nil {
				t.Fatal(err)
			}
			if s.ran != 1 {
				t.Fatalf("the decision given again ran the call %d times", s.ran)
			}
		})
	}
}

func TestAQuestionStoppedByItsOwnPauseEndsAsked(t *testing.T) {
	for _, h := range hosts {
		t.Run(h.name, func(t *testing.T) {
			s := &scene{store: runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{}), extra: []runcontext.HostTool{askPerson()}}
			if err := s.run(t, context.Background(), false, nil, false, h.host("ask_person", question)); err != nil {
				t.Fatal(err)
			}
			saved := stored(t, s.store)
			questions := s.count(func(e contract.RunEvent) bool { return e.UserQuestion != nil })
			// A question ends the run unanswered (ADR-0007): the end is stored, with no error.
			if saved.Status != "complete" || questions != 1 || len(s.errors()) != 0 || s.ran != 0 {
				t.Fatalf("ended %q, questions %d, errors %v, ran %d", saved.Status, questions, s.errors(), s.ran)
			}
			for _, row := range saved.Durable.Dispatches {
				if row.Kind == "agent" && row.Failure != nil && row.Failure.Code == "cancelled" {
					t.Fatalf("the question was recorded as a cancelled agent: %+v", row.Failure)
				}
			}
		})
	}
}

// attached holds the run's context, to read what the run kept.
type attached struct{ context *runcontext.SessionContext }

func (a *attached) Attach(c *runcontext.SessionContext) { a.context = c }

// faultStore fails the commit of a checkpoint holding an asked approval: refused, nothing stored, or stored with its
// acknowledgement lost.
type faultStore struct {
	runcontext.RunStore
	lost bool
}

func (s *faultStore) Commit(ctx context.Context, in runcontext.RunCommit) (runcontext.RunReceipt, error) {
	var cp contract.Checkpoint
	_ = json.Unmarshal(in.Checkpoint, &cp)
	ask := cp.Approval != nil && cp.Approval.State == "asked"
	if ask && !s.lost {
		return runcontext.RunReceipt{}, errors.New("refused asked commit")
	}
	receipt, err := s.RunStore.Commit(ctx, in)
	if err == nil && ask {
		return runcontext.RunReceipt{}, errors.New("lost pause acknowledgement")
	}
	return receipt, err
}

func TestAnAskThatCouldNotBeSavedIsNotShown(t *testing.T) {
	for _, h := range hosts {
		for _, lost := range []bool{false, true} {
			name := h.name + "/refused"
			if lost {
				name = h.name + "/acknowledgement lost"
			}
			t.Run(name, func(t *testing.T) {
				memory := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
				link := &attached{}
				s := &scene{store: &faultStore{RunStore: memory, lost: lost}, link: link}
				if err := s.run(t, context.Background(), false, nil, false, h.host("writes__write_note", `{}`)); err != nil {
					t.Fatal(err)
				}
				var failed *contract.RunEventError
				for _, event := range s.events {
					if event.Error != nil {
						failed = event.Error
					}
				}
				// The run ends with the storage failure, as before and as a Runtime action's unsaved ask ends, in both
				// languages: unknown/storage/unknown. Nothing shown, nothing run.
				if s.count(asked) != 0 || s.ran != 0 || failed == nil || failed.Failure == nil || failed.Failure.Code != "unknown" || failed.Failure.Source != "storage" || failed.Failure.Outcome != "unknown" {
					t.Fatalf("asked %d, ran %d, error %+v", s.count(asked), s.ran, failed)
				}
				if saved := stored(t, memory); !lost && saved.Approval != nil {
					t.Fatalf("a refused save left the ask %q", held(saved))
				}
				// The ask is withdrawn, so no later write of the run can carry it.
				if kept := link.context.Execution.State().Approval; kept != nil {
					t.Fatalf("a failed save kept the ask %q", kept.State)
				}
			})
		}
	}
}

// cancelOnAsk is a store that ends the caller's context once it has stored the ask.
type cancelOnAsk struct {
	runcontext.RunStore
	cancel context.CancelFunc
}

func (s *cancelOnAsk) Commit(ctx context.Context, in runcontext.RunCommit) (runcontext.RunReceipt, error) {
	receipt, err := s.RunStore.Commit(ctx, in)
	var cp contract.Checkpoint
	if json.Unmarshal(in.Checkpoint, &cp) == nil && cp.Approval != nil && cp.Approval.State == "asked" {
		s.cancel()
	}
	return receipt, err
}

func TestTheCallersOwnCancellationStillStops(t *testing.T) {
	for _, h := range hosts {
		for _, at := range []string{"before the call", "once its stop closed dispatch", "while the ask is saved", "once the person is shown it"} {
			t.Run(h.name+"/"+at, func(t *testing.T) {
				memory := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
				link := &attached{}
				s := &scene{store: memory, link: link}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				host := h.host("writes__write_note", `{}`)
				switch at {
				case "before the call":
					given := host
					host = func(c context.Context, request adapter.AgentRequest, cb adapter.AgentCallbacks, n int) error {
						cancel()
						return given(c, request, cb, n)
					}
				case "once its stop closed dispatch":
					// A host's stop closes dispatch before it cancels the provider's work (RunWriter.CloseDispatch): the ask
					// comes in that window, the caller's context still live, and cannot be saved.
					s.approving = func() {
						binding := link.context.Persistence("author")
						if err := binding.Writer.CloseDispatch(*binding); err != nil {
							t.Errorf("closing dispatch: %v", err)
						}
					}
				case "while the ask is saved":
					s.store = &cancelOnAsk{RunStore: memory, cancel: cancel}
				default:
					s.seen = func(e contract.RunEvent) {
						if e.ApprovalRequested != nil {
							cancel()
						}
					}
				}
				if err := s.run(t, ctx, false, nil, false, host); err != nil {
					t.Fatal(err)
				}
				stopped := s.count(func(e contract.RunEvent) bool {
					return e.Error != nil && e.Error.Failure != nil && e.Error.Failure.Code == "cancelled"
				})
				// Stopped, as before, never read as the pause: nothing ran, and an ask the caller stopped before it was
				// shown is not shown.
				if stopped != 1 || s.ran != 0 || at != "once the person is shown it" && s.count(asked) != 0 {
					t.Fatalf("stopped %d, ran %d, asked %d, errors %v", stopped, s.ran, s.count(asked), s.errors())
				}
				// Nor is a stop read as a finished run: the run is stopped, or paused where its ask was saved first.
				want := "stopped"
				if at == "while the ask is saved" || at == "once the person is shown it" {
					want = "paused"
				}
				if state := link.context.Execution.State().Status; state != want {
					t.Fatalf("the stopped run ended %s, not %s", state, want)
				}
				if saved := stored(t, memory); at == "before the call" && saved.Approval != nil {
					t.Fatalf("a cancelled ask was kept %q", held(saved))
				}
				// The host finalizes its stop (RunWriter.FinalizeStop). A provider that answered the caller's own cancellation
				// with its context's error is recorded stopped by it, never as the pause.
				if at == "once the person is shown it" && h.name == "its context's error" {
					binding := link.context.Persistence("author")
					if _, err := binding.Writer.FinalizeStop(context.Background(), *binding); err != nil {
						t.Fatal(err)
					}
					for _, row := range stored(t, memory).Durable.Dispatches {
						if row.Kind == "agent" && (row.Failure == nil || row.Failure.Code != "cancelled") {
							t.Fatalf("the caller's stop was recorded as %+v", row.Failure)
						}
					}
				}
			})
		}
	}
}

// The call's own context ending once the ask is made (a provider's per-call deadline) does not withdraw it: the ask is saved
// under the invocation's context, as TypeScript saves it under the run's signal, and the run ends paused with it.
func TestTheCallsOwnContextDoesNotWithdrawAnAsk(t *testing.T) {
	for _, h := range hosts {
		t.Run(h.name, func(t *testing.T) {
			s := &scene{store: runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})}
			given := h.host("writes__write_note", `{}`)
			host := func(c context.Context, request adapter.AgentRequest, cb adapter.AgentCallbacks, n int) error {
				call, end := context.WithCancel(c)
				defer end()
				s.approving = end
				return given(call, request, cb, n)
			}
			if err := s.run(t, context.Background(), false, nil, false, host); err != nil {
				t.Fatal(err)
			}
			saved := stored(t, s.store)
			if held(saved) != "paused/asked" || s.count(asked) != 1 || len(s.errors()) != 0 || s.ran != 0 {
				t.Fatalf("ended %q, asked %d, errors %v, ran %d", held(saved), s.count(asked), s.errors(), s.ran)
			}
			proof := runcontext.ApprovalShownDigest(*saved.Approval)
			decision := &contract.CheckpointApprovalDecision{CallID: saved.Approval.CallID, Approved: true, ShownDigest: &proof}
			s.events, s.approving = nil, nil
			if err := s.run(t, context.Background(), true, decision, false); err != nil {
				t.Fatal(err)
			}
			if s.ran != 1 || len(s.errors()) != 0 || !s.finished() {
				t.Fatalf("the decision: ran %d, errors %v, finished %v", s.ran, s.errors(), s.finished())
			}
		})
	}
}

// #312's guard: an approved continuation that delegates, its host answering the handoff with its context's error, runs the
// child once.
func TestAHandoffStoppedByItsOwnPauseRunsTheChildOnce(t *testing.T) {
	s := &scene{store: runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})}
	if err := s.run(t, context.Background(), false, nil, true, stoppedByContext("writes__write_note", `{}`)); err != nil {
		t.Fatal(err)
	}
	saved := stored(t, s.store)
	if held(saved) != "paused/asked" || s.ran != 0 {
		t.Fatalf("not paused: %q, ran %d, %v", held(saved), s.ran, s.errors())
	}
	proof := runcontext.ApprovalShownDigest(*saved.Approval)
	decision := &contract.CheckpointApprovalDecision{CallID: saved.Approval.CallID, Approved: true, ShownDigest: &proof}
	s.events = nil
	if err := s.run(t, context.Background(), true, decision, true, stoppedByContext("delegate_tasks", `{"tasks":[{"title":"Check","profile":"review","objective":"Check the note."}]}`)); err != nil {
		t.Fatal(err)
	}
	tasks := s.count(func(e contract.RunEvent) bool {
		return e.DelegatedTask != nil && (e.DelegatedTask.Task.State == "complete" || e.DelegatedTask.Task.State == "failed")
	})
	if s.ran != 1 || s.children != 1 || tasks != 1 || len(s.errors()) != 0 || !s.finished() {
		t.Fatalf("ran %d, children %d, tasks ended %d, errors %v, finished %v", s.ran, s.children, tasks, s.errors(), s.finished())
	}
}
