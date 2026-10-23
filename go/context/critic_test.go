package runcontext

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
)

// The critic's switch (ADR-0010; criticSwitch.test.ts), held to the contract's vectors.

type switchCase struct {
	Name    string `json:"name"`
	Session struct {
		Critic            string `json:"critic"`
		CriticProject     string `json:"criticProject"`
		CriticReasoning   string `json:"criticReasoning"`
		AdversarialReview *bool  `json:"adversarialReview"`
	} `json:"session"`
	Table struct {
		CriticDefault        string `json:"criticDefault"`
		CriticReasoningFloor string `json:"criticReasoningFloor"`
		AdversarialReview    *bool  `json:"adversarialReview"`
	} `json:"table"`
	Forced struct {
		Child            bool   `json:"child"`
		Phase            string `json:"phase"`
		Purpose          string `json:"purpose"`
		ReviewerAssigned bool   `json:"reviewerAssigned"`
	} `json:"forced"`
	Run struct {
		Proposal        bool `json:"proposal"`
		Cases           bool `json:"cases"`
		ChangesExisting bool `json:"changesExisting"`
		ModelAsked      bool `json:"modelAsked"`
		Room            bool `json:"room"`
	} `json:"run"`
	Author     string `json:"author"`
	Capability string `json:"capability"`
}

func criticVector(t *testing.T, name string, into any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repositoryRoot(t), "contract", "vectors", "critic", name))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatal(err)
	}
}

func marshaled(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestTheDecisionIsTheVectors(t *testing.T) {
	for _, name := range []string{"switch-modes.json", "switch-triggers.json", "switch-forced-off.json", "switch-budget.json", "switch-tier.json", "switch-alias.json"} {
		var vector struct {
			Cases []switchCase                `json:"cases"`
			Items []contract.CheckpointCritic `json:"items"`
		}
		criticVector(t, name, &vector)
		if len(vector.Cases) == 0 || len(vector.Cases) != len(vector.Items) {
			t.Fatalf("%s: %d cases, %d items", name, len(vector.Cases), len(vector.Items))
		}
		for at, held := range vector.Cases {
			settings := CriticSettings{Critic: held.Session.Critic, CriticProject: held.Session.CriticProject, CriticReasoning: held.Session.CriticReasoning, AdversarialReview: held.Session.AdversarialReview}
			table := CriticTable{CriticDefault: held.Table.CriticDefault, CriticReasoningFloor: held.Table.CriticReasoningFloor, AdversarialReview: held.Table.AdversarialReview}
			forced := CriticForcedFacts{Child: held.Forced.Child, Phase: held.Forced.Phase, Purpose: held.Forced.Purpose, ReviewerAssigned: held.Forced.ReviewerAssigned}
			run := CriticRunFacts{Proposal: held.Run.Proposal, Cases: held.Run.Cases, ChangesExisting: held.Run.ChangesExisting, ModelAsked: held.Run.ModelAsked, Room: held.Run.Room}
			decided := DecideCritic(settings, table, forced, run)
			decided.Reasoning = CriticTier(held.Author, settings.CriticReasoning, table.CriticReasoningFloor)
			if got, want := marshaled(t, decided.Record()), marshaled(t, vector.Items[at]); got != want {
				t.Errorf("%s, %s: %s, not %s", name, held.Name, got, want)
			}
			if err := ReadCriticSettings(settings, held.Capability == "agent"); err != nil {
				t.Errorf("%s, %s: %v", name, held.Name, err)
			}
		}
	}
}

func TestDroppedCriticTableDefaultsStayOutOfDecisions(t *testing.T) {
	var vector struct {
		Dropped []struct {
			Name, Role, Capability, Raw string
			Resolved                    PolicyMember
		}
	}
	raw, err := os.ReadFile(filepath.Join(repositoryRoot(t), "contract", "vectors", "providers", "reasoning-omission.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &vector); err != nil {
		t.Fatal(err)
	}
	for _, row := range vector.Dropped {
		if row.Role != "critic" {
			continue
		}
		t.Run(row.Name, func(t *testing.T) {
			execution := &Execution{}
			members := PolicyMembers{"models.critic.reasoning": row.Resolved}
			switcher := NewCriticSwitch(CriticSettings{ResolvedPolicy: members, Critic: "on"}, CriticTable{CriticReasoningFloor: "author"}, CriticForcedFacts{}, func() *Execution { return execution })
			decision := switcher.Decide(CriticRunFacts{Proposal: true, Room: true}, row.Raw)
			if decision.Reasoning != "" || decision.Record().Reasoning != nil || CriticDecidedEvent(decision).CriticDecided.Reasoning != nil {
				t.Fatalf("%s decision restored reasoning: %+v", row.Capability, decision)
			}
		})
	}
}

func TestTheTriggerOrderIsTheContract(t *testing.T) {
	schema := readJSON(t, "contract/schemas/provenance.json")
	values := schema.Get("$defs").Get("criticDecision").Get("properties").Get("trigger").Get("enum").Items()
	want := make([]string, len(values))
	for at, value := range values {
		want[at] = value.Str()
	}
	if !slices.Equal(CriticTriggers, want) {
		t.Fatalf("critic trigger order is %v; contract order is %v", CriticTriggers, want)
	}
}

func TestTheSessionMembersAreReadClosed(t *testing.T) {
	var vector struct {
		Items []contract.ToolOffer `json:"items"`
	}
	criticVector(t, "switch-sessions.json", &vector)
	for at, item := range vector.Items {
		settings := CriticSettings{AdversarialReview: item.AdversarialReview}
		for _, member := range []struct {
			from *string
			into *string
		}{{item.Critic, &settings.Critic}, {item.CriticProject, &settings.CriticProject}, {item.CriticReasoning, &settings.CriticReasoning}} {
			if member.from != nil {
				*member.into = *member.from
			}
		}
		if err := ReadCriticSettings(settings, at == 2); err != nil {
			t.Errorf("session %d: %v", at, err)
		}
	}
	for _, refused := range []struct {
		settings CriticSettings
		agent    bool
		says     string
	}{
		{CriticSettings{Critic: "sometimes"}, false, "critic must be off, auto or on"},
		{CriticSettings{CriticProject: "always"}, false, "criticProject must be off, auto or on"},
		{CriticSettings{CriticReasoning: "max"}, false, "criticReasoning must be one of off, low, on, medium, ultra, high"},
		{CriticSettings{CriticReasoning: "ultra"}, true, "criticReasoning must be one of none, minimal, low, medium, high, xhigh, max"},
	} {
		if err := ReadCriticSettings(refused.settings, refused.agent); err == nil || !strings.Contains(err.Error(), refused.says) {
			t.Errorf("%+v: %v", refused.settings, err)
		}
	}
	// A session the reader refuses ends the run before any request, a configuration failure.
	session := modelSession()
	session.CriticReasoning = "max"
	if _, err := ContextualSession(context.Background(), session); err == nil || failure.Normalize(err, "context", time.Time{}).Code != "configuration" {
		t.Fatalf("the run opened, or failed with %v", err)
	}
}

func TestRequestReviewIsDeclaredAsTheVectorAndOfferedAfterTheContextTools(t *testing.T) {
	var vector struct {
		Items []contract.ToolOfferHostTool `json:"items"`
	}
	criticVector(t, "request-review.json", &vector)
	want := marshaled(t, vector.Items[0])
	offered := OfferedReviewTool(Session{}, Defaults)
	if len(offered) != 1 || marshaled(t, offered[0]) != want {
		t.Fatalf("the host's declaration is %s", marshaled(t, offered))
	}
	run, err := ContextualSession(context.Background(), modelSession())
	if err != nil {
		t.Fatal(err)
	}
	defer run.Context.Close()
	tools := run.Session.HostTools
	last := tools[len(tools)-1]
	if last.Name != RequestReview || marshaled(t, last.Declaration()) != want {
		t.Fatalf("the boundary's last tool is %s", marshaled(t, last.Declaration()))
	}
	for _, patch := range []func(*Session){
		func(s *Session) { s.Critic = "off" },
		func(s *Session) { s.Critic = "on" },
		func(s *Session) { s.Context = &Input{Objective: "Read the policy."} },
		func(s *Session) { s.Context = &Input{Phase: "plan"} },
		func(s *Session) { s.Purpose = "graph" },
		func(s *Session) { s.ReviewerAssigned = true },
	} {
		session := modelSession()
		patch(&session)
		if len(OfferedReviewTool(session, Defaults)) != 0 {
			t.Errorf("the host is told to declare request_review for %+v", session)
		}
		run, err := ContextualSession(context.Background(), session)
		if err != nil {
			t.Fatal(err)
		}
		for _, tool := range run.Session.HostTools {
			if tool.Name == RequestReview {
				t.Errorf("request_review is offered on %+v", session)
			}
		}
		run.Context.Close()
	}
}

func TestTheAskIsTakenOnceAndRefusedAfterWithTheGuardrail(t *testing.T) {
	var refused struct {
		Items []json.RawMessage `json:"items"`
	}
	criticVector(t, "review-refused.json", &refused)
	run, err := ContextualSession(context.Background(), modelSession())
	if err != nil {
		t.Fatal(err)
	}
	defer run.Context.Close()
	tool := run.Session.HostTools[len(run.Session.HostTools)-1]
	answer, err := tool.Execute(context.Background(), json.RawMessage(`{"reason":"The pack changes the approval threshold."}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := answer.StructuredContent.Text(); got != `{"asked":true,"note":`+QuoteJSON(ReviewTaken)+`}` {
		t.Fatalf("the ask was answered %s", got)
	}
	if critic := run.Context.Execution.State.Critic; critic == nil || marshaled(t, critic) != `{"mode":"auto","trigger":"model-asked"}` {
		t.Fatalf("the ask was recorded as %s", marshaled(t, critic))
	}
	_, err = tool.Execute(context.Background(), json.RawMessage(`{}`))
	var held *failure.Error
	if !errors.As(err, &held) || held.Failure.Code != "tool-failed" || held.Failure.Outcome != "not-started" || held.Failure.Detail == nil || *held.Failure.Detail != ReviewRefusal("asked") {
		t.Fatalf("the second ask failed with %v", err)
	}
	notices := run.Context.Review.TakeNotices()
	if len(notices) != 1 || marshaled(t, notices[0]) != string(compactJSON(t, refused.Items[1])) {
		t.Fatalf("the second ask's notices were %s", marshaled(t, notices))
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"reason":7}`)); err == nil {
		t.Fatal("a reason that is not a string was taken")
	}
	// Outside the author's phase: the critic, the reformat run.
	for _, set := range []func(bool){func(on bool) { run.Context.CriticReading = on }, func(on bool) { run.Context.Reformatting = on }} {
		set(true)
		if run.Context.ReviewAdmitted() || run.Context.RefuseReview() != ReviewRefusal("outside") {
			t.Error("request_review was admitted outside the author's phase")
		}
		set(false)
	}
	// Under off and on, the switch refuses the ask even in the author's phase and records nothing.
	for _, mode := range []string{"off", "on"} {
		held := &Execution{}
		one := NewCriticSwitch(CriticSettings{Critic: mode}, CriticTable{CriticDefault: "auto"}, CriticForcedFacts{}, func() *Execution { return held })
		if one.Offered() || one.Ask(true) != ReviewRefusal(mode) || held.Critic != nil {
			t.Errorf("under %s the ask was offered or taken", mode)
		}
	}
	// Under off: refused before anything runs, with the sentence the vector carries.
	off := modelSession()
	off.Critic = "off"
	offRun, err := ContextualSession(context.Background(), off)
	if err != nil {
		t.Fatal(err)
	}
	defer offRun.Context.Close()
	if offRun.Context.ReviewAdmitted() || offRun.Context.RefuseReview() != ReviewRefusal("off") {
		t.Fatal("request_review under off was admitted")
	}
	if notices := offRun.Context.Review.TakeNotices(); len(notices) != 1 || marshaled(t, notices[0]) != string(compactJSON(t, refused.Items[4])) {
		t.Fatalf("the ask under off earned %s", marshaled(t, notices))
	}
	if offRun.Context.Execution.State.Critic != nil {
		t.Fatal("the ask under off was recorded")
	}
}

func compactJSON(t *testing.T, raw json.RawMessage) []byte {
	t.Helper()
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	var event contract.RunEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestTheDecidedEventsAreTheVectors(t *testing.T) {
	var vector struct {
		Items []json.RawMessage `json:"items"`
	}
	criticVector(t, "decided-events.json", &vector)
	for _, item := range vector.Items {
		var held contract.RunEventCriticDecided
		if err := json.Unmarshal(item, &held); err != nil {
			t.Fatal(err)
		}
		decision := CriticDecision{Mode: held.Mode, Ran: held.Ran, Reason: held.Reason}
		if held.Reasoning != nil {
			decision.Reasoning = *held.Reasoning
		}
		if held.Trigger != nil {
			decision.Trigger = *held.Trigger
		}
		if got, want := marshaled(t, CriticDecidedEvent(decision)), string(compactJSON(t, item)); got != want {
			t.Errorf("%s, not %s", got, want)
		}
		if !ValidCriticRecord(decision.Record()) {
			t.Errorf("%s is not a record the checkpoint keeps", item)
		}
	}
}

func TestTheCheckpointCarriesTheAskThroughAResume(t *testing.T) {
	items := vectorItems(t, "critic/checkpoint.json")
	for at, item := range items {
		if _, err := ReadExecution(item); err != nil {
			t.Fatalf("checkpoint %d: %v", at, err)
		}
	}
	for _, broken := range []string{`{"mode":"sometimes"}`, `{"mode":"auto","ran":true}`, `{"mode":"auto","verdict":"refuted"}`, `{"mode":"auto","reasoning":"deep"}`, `{"mode":"auto","trigger":"prose"}`} {
		held := readJSON(t, "contract/vectors/critic/checkpoint.json").Get("items").Items()[0]
		value, err := ParseJSON([]byte(broken))
		if err != nil {
			t.Fatal(err)
		}
		held.Set("critic", value)
		if _, err := ReadExecution(held); err == nil {
			t.Errorf("a checkpoint whose critic is %s was read", broken)
		}
	}
	saved := items[0]
	saved.Set("requestKey", String(ContextDigest(ObjectOf("request", ObjectOf("message", String("Current request"), "input", String("Current request")), "phase", String("work"), "purpose", Null()).Text())))
	var checkpoint contract.Checkpoint
	if err := json.Unmarshal(saved.JSON(), &checkpoint); err != nil {
		t.Fatal(err)
	}
	session := modelSession()
	session.Model.Thinking = "on"
	session.Context = &Input{CurrentRequest: &CurrentRequest{Message: "Current request", Input: "Current request"}, Execution: &checkpoint}
	run, err := ContextualSession(context.Background(), session)
	if err != nil {
		t.Fatal(err)
	}
	defer run.Context.Close()
	tool := run.Session.HostTools[len(run.Session.HostTools)-1]
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Fatal("a second ask after the resume was taken")
	}
	decided := run.Context.DecideCritic(true, false)
	if got := marshaled(t, decided.Record()); got != `{"mode":"auto","ran":true,"reason":"model-asked","reasoning":"on","trigger":"model-asked"}` {
		t.Fatalf("the resumed run decided %s", got)
	}
	state, err := run.Context.Execution.Snapshot("running")
	if err != nil {
		t.Fatal(err)
	}
	if got := marshaled(t, state.Critic); got != `{"mode":"auto","ran":true,"reason":"model-asked","reasoning":"on","trigger":"model-asked"}` {
		t.Fatalf("the checkpoint keeps %s", got)
	}
}

func TestTheCriticsInvocationIsHeldBackUnderAutoAndOn(t *testing.T) {
	table := CriticTable{CriticDefault: Defaults.CriticDefault, CriticReasoningFloor: Defaults.CriticReasoningFloor}
	held := func(settings CriticSettings, forced CriticForcedFacts) bool {
		return NewCriticSwitch(settings, table, forced, func() *Execution { return &Execution{} }).Reserved()
	}
	if !held(CriticSettings{}, CriticForcedFacts{}) || !held(CriticSettings{Critic: "on"}, CriticForcedFacts{}) {
		t.Error("auto or on holds nothing back for the critic")
	}
	if held(CriticSettings{Critic: "off"}, CriticForcedFacts{}) || held(CriticSettings{Critic: "on"}, CriticForcedFacts{ReviewerAssigned: true}) {
		t.Error("off, or a reviewer assigned, holds an invocation back")
	}
	if Defaults.CriticDefault != "auto" || Defaults.CriticReasoningFloor != "author" || Defaults.AdversarialReview != nil {
		t.Errorf("the table states %q, %q, %v", Defaults.CriticDefault, Defaults.CriticReasoningFloor, Defaults.AdversarialReview)
	}
}

// The adapter regression also exercises durable saves; its lock-removal runs
// and observed race reports are documented in pr.md.
func TestConcurrentReviewRequests(t *testing.T) {
	for _, tc := range []struct {
		name       string
		calls      int
		already    bool
		wantTaken  int
		wantDenied int
	}{
		{"two first requests", 2, false, 1, 1},
		{"fifteen refused requests", 15, true, 0, 15},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run, err := ContextualSession(context.Background(), modelSession())
			if err != nil {
				t.Fatal(err)
			}
			defer run.Context.Close()
			tool := run.Session.HostTools[len(run.Session.HostTools)-1]
			if tc.already {
				if _, err := tool.Execute(context.Background(), json.RawMessage(`{}`)); err != nil {
					t.Fatal(err)
				}
			}
			type result struct {
				answer  ToolResult
				err     error
				notices []contract.RunEvent
			}
			results := make(chan result, tc.calls)
			start := make(chan struct{})
			var ready sync.WaitGroup
			ready.Add(tc.calls)
			for range tc.calls {
				go func() {
					ready.Done()
					<-start
					answer, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
					// Readers and drainers overlap other requests, as on the adapter path.
					_ = run.Context.Review.Asked()
					if _, snapshotErr := run.Context.Execution.Snapshot("running"); snapshotErr != nil {
						t.Error(snapshotErr)
					}
					results <- result{answer, err, run.Context.Review.TakeNotices()}
				}()
			}
			ready.Wait()
			close(start)
			taken, denied := 0, 0
			var notices []contract.RunEvent
			for range tc.calls {
				got := <-results
				notices = append(notices, got.notices...)
				if got.err == nil {
					taken++
					if got.answer.StructuredContent == nil || got.answer.StructuredContent.Text() != `{"asked":true,"note":`+QuoteJSON(ReviewTaken)+`}` {
						t.Errorf("unexpected accepted answer: %+v", got.answer)
					}
				} else {
					denied++
					var held *failure.Error
					if !errors.As(got.err, &held) || held.Failure.Code != "tool-failed" || held.Failure.Outcome != "not-started" || held.Failure.Detail == nil || *held.Failure.Detail != ReviewRefusal("asked") {
						t.Errorf("unexpected refusal: %v", got.err)
					}
				}
			}
			notices = append(notices, run.Context.Review.TakeNotices()...)
			if taken != tc.wantTaken || denied != tc.wantDenied || len(notices) != tc.wantDenied {
				t.Fatalf("taken=%d refused=%d guardrails=%d; want %d, %d, %d", taken, denied, len(notices), tc.wantTaken, tc.wantDenied, tc.wantDenied)
			}
			for _, event := range notices {
				g := event.Guardrail
				if g == nil || g.Tool != RequestReview || g.Action != "refused" || g.Reason == nil || *g.Reason != "review refused" || g.Detail != ReviewRefusal("asked") {
					t.Errorf("unexpected guardrail: %+v", g)
				}
			}
			if got := marshaled(t, run.Context.Execution.State.Critic); got != `{"mode":"auto","trigger":"model-asked"}` {
				t.Fatalf("the checkpoint lost the ask: %s", got)
			}
			if len(run.Context.Review.TakeNotices()) != 0 {
				t.Fatal("a drained refusal was delivered twice")
			}
		})
	}
}
