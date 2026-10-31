package adapter_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	native "github.com/Judgment-Pack/judgment-pack-agent/go/adapter/agent"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/eino"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
	"github.com/Judgment-Pack/judgment-pack-agent/go/relay"
)

// #226: re-authorization waits through the real eino and agent adapters, the package's writer and the
// reference effect host. The TypeScript twin is packages/harness/storage/reauth.test.ts.

// reauthRun runs the Runtime action fixture with an allowed call: the model (or the native agent) asks for
// the Runtime evaluation, then ends with a final message.
func reauthRun(t *testing.T, f *runtimeActionFixture, store runcontext.RunStore, engine string, resume bool, requests *atomic.Int32) ([]contract.RunEvent, error) {
	t.Helper()
	events := []contract.RunEvent{}
	run := &adapter.Run{Prompt: "Evaluate the unsaved draft.", Wire: "openai-compatible", ModelName: "fixture", Thinking: "off", Critic: "off", Gate: f.gate.Calls(), Tools: f.gate.Offer().Tools, AllowConversation: true, Context: &runcontext.Input{CurrentRequest: &runcontext.CurrentRequest{Message: "Evaluate the unsaved draft.", Input: "draft"}, RunStore: &runcontext.DurableRunInput{Store: store, ID: "runtime-action-run", Resume: resume}}, Emit: func(e contract.RunEvent) { events = append(events, e) }}
	run.EnrollContext = func(ctx context.Context, offer adapter.ContextOffer) error {
		return f.gate.EnrollContext(offer.ID, offer.HostTools)
	}
	var implementation adapter.Adapter
	if engine == "native" {
		implementation = native.New()
		run.Agent = &adapter.Agent{Model: "fixture", Run: func(ctx context.Context, request adapter.AgentRequest, cb adapter.AgentCallbacks) error {
			if cb.ProviderRelease != nil {
				defer cb.ProviderRelease.Acknowledge(cb.ProviderRelease.Invocation)
			}
			requests.Add(1)
			for _, tool := range request.Tools {
				if strings.HasPrefix(tool.Description, "experimental_evaluate: ") {
					if _, err := cb.Tool(ctx, adapter.AgentToolCall{ID: "native-call", Name: tool.Name, Arguments: json.RawMessage(`{"value":7,"rehearsal":false}`)}); err != nil {
						return err
					}
					return cb.Event(ctx, adapter.AgentEvent{Type: "message", ID: "final", Text: "Evaluation completed.", Phase: "final"})
				}
			}
			return errors.New("Runtime evaluation was not offered")
		}}
	} else {
		implementation = eino.New()
		seen := 0
		run.Model = func(context.Context, string, relay.Request) (*http.Response, error) {
			requests.Add(1)
			seen++
			if seen == 1 {
				return runtimeActionResponse(), nil
			}
			return enrollmentFinal(), nil
		}
	}
	err := implementation.Run(context.Background(), run)
	return events, err
}

func TestReauthorizationWaitThroughTheAdapters(t *testing.T) {
	const key = "wk.adapter.reauth.Q7z2Xm9LpV4cR8tNb3Yh6Df1G"
	sum := sha256.Sum256([]byte("sentinel-person@example.invalid"))
	principal := "sha256:" + hex.EncodeToString(sum[:])
	for _, engine := range []string{"eino", "native"} {
		t.Run(engine, func(t *testing.T) {
			ctx := context.Background()
			clock := &waitClock{}
			t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			clock.at.Store(t0.UnixMilli())
			store := waitStore{runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{Now: clock.now})}
			refused := &runcontext.CredentialRefusal{Cause: "revoked", Connection: "runtime-conn", Principal: principal, Permissions: []string{"evaluate:write"}, Requires: []string{"evaluate:write"}}
			var current atomic.Bool
			// The host's credential store holds the token; the package never sees it.
			const token = "SENTINEL-ACCESS-TOKEN-4f1c9e7a2b"
			seen := []string{}
			host := runcontext.NewReferenceEffectHostWith(store, runcontext.ReferenceEffectHostOptions{Credentials: func(_ context.Context, op runcontext.EffectOperation) (*runcontext.CredentialRefusal, error) {
				seen = append(seen, op.Operation+":"+token)
				if current.Load() {
					return nil, nil
				}
				return refused, nil
			}})
			a, err := store.AcquireLease(ctx, runcontext.LeaseAcquire{Resource: "runtime-action-run", Owner: "worker-a", DurationMs: 3_600_000})
			if err != nil {
				t.Fatal(err)
			}
			view, err := runcontext.UnattendedRunStore(store, runcontext.FenceOf(a), host)
			if err != nil {
				t.Fatal(err)
			}
			f := newRuntimeActionFixture(t)
			f.approve = func(gate.RuntimeApprovalRequest) string { return "allow" }
			var requests atomic.Int32
			_, _ = reauthRun(t, f, view, engine, false, &requests)
			// The effect host refused the call at its admission boundary: nothing ran, the run stopped there.
			if len(f.calls) != 0 {
				t.Fatalf("the refused call ran %d times", len(f.calls))
			}
			proof := runcontext.ReauthorizationOf(view)
			if proof == nil {
				t.Fatal("the trusted refusal is not on the view")
			}
			keyDigest, _ := runcontext.WakeKeyDigest(key)
			park := runcontext.ParkInput{Store: store, Fence: runcontext.FenceOf(a), Wait: "wait-r", Continuation: "continue-r", Condition: runcontext.WaitCondition{Event: &contract.RunWaitEventCondition{Kind: "reauthorized", Subject: "runtime-conn"}}, Model: runcontext.PinnedModel{Provider: "openai", Model: "fixture"}, WakeKeyDigest: keyDigest, Release: []runcontext.WaitRelease{}, Reauthorization: proof}
			wait, attention, err := runcontext.ParkRun(ctx, park)
			if err != nil {
				t.Fatal(err)
			}
			if wait.Reason != "reauthorization" || wait.Pending.Reauthorization.Connection != "runtime-conn" || wait.Reauthorization.Cause != "revoked" || attention.Needs != "reauthorization" {
				t.Fatalf("wait %+v attention %+v", wait, attention)
			}
			var reauth *runcontext.ReauthorizationRefused
			if _, _, err := runcontext.ParkRun(ctx, park); !errors.As(err, &reauth) || reauth.Reason != "used" {
				t.Fatalf("a used proof: %v", err)
			}
			clock.at.Add(60_000)
			if _, err := runcontext.WakeRun(ctx, store, runcontext.WakeEvidence{Version: 1, Resource: "runtime-action-run", Wait: "wait-r", Evidence: "refreshed-1", Kind: "event", Event: &contract.RunWaitEventCondition{Kind: "reauthorized", Subject: "runtime-conn"}, Key: key}); err != nil {
				t.Fatal(err)
			}
			evidence := runcontext.ResumeEvidence{Version: 1, Resource: "runtime-action-run", Wait: "wait-r", Model: contract.RunResumeCurrentModel{Provider: "openai", Model: "fixture", Status: "available"}, Policy: contract.RunResumeCurrentPolicy{Digest: wait.Pins.Policy}, Plan: wait.Pins.Plan, Sources: wait.Pins.Sources, Artifacts: wait.Pins.Artifacts}
			action := func(change func(*runcontext.ReauthAction)) *runcontext.ReauthAction {
				held := runcontext.ReauthAction{Version: 1, Resource: "runtime-action-run", Wait: "wait-r", Connection: "runtime-conn", Action: "refresh", Principal: principal, Permissions: []string{"evaluate:write"}, Actor: "person-owner", At: "2026-01-01T00:00:30.000Z"}
				if change != nil {
					change(&held)
				}
				return &held
			}
			input := runcontext.ResumeInput{Store: store, Resource: "runtime-action-run", Wait: "wait-r", Evidence: "refreshed-1", Owner: "worker-b", DurationMs: 3_600_000, Current: &evidence}
			var waitRefused *runcontext.WaitRefused
			var resumeRefused *runcontext.ResumeRefused
			if _, err := runcontext.ResumeParked(ctx, input); !errors.As(err, &waitRefused) || waitRefused.Reason != "decision-required" {
				t.Fatalf("a wake is not the refresh: %v", err)
			}
			input.Reauthorization = action(func(a *runcontext.ReauthAction) { a.Action = "replacement" })
			if _, err := runcontext.ResumeParked(ctx, input); !errors.As(err, &resumeRefused) || resumeRefused.Reason != "identity-changed" {
				t.Fatalf("a replacement: %v", err)
			}
			input.Reauthorization = action(func(a *runcontext.ReauthAction) { a.Permissions = []string{"evaluate:read"} })
			if _, err := runcontext.ResumeParked(ctx, input); !errors.As(err, &reauth) || reauth.Reason != "permission-insufficient" {
				t.Fatalf("narrower permissions: %v", err)
			}
			input.Reauthorization = action(nil)
			resumed, err := runcontext.ResumeParked(ctx, input)
			if err != nil {
				t.Fatal(err)
			}
			if resumed.Reauthorized == nil || resumed.Reauthorized.Principal != principal {
				t.Fatalf("resumed %+v", resumed)
			}
			// The host refreshed the credentials: the continuation re-sends the never-admitted call once.
			current.Store(true)
			continued, err := runcontext.ReauthorizedRunStore(ctx, store, host, resumed)
			if err != nil {
				t.Fatal(err)
			}
			fresh := newRuntimeActionFixture(t)
			fresh.approve = func(gate.RuntimeApprovalRequest) string { return "allow" }
			if _, err := reauthRun(t, fresh, continued, engine, true, &requests); err != nil {
				t.Fatal(err)
			}
			if len(fresh.calls) != 1 {
				t.Fatalf("the re-authorized call ran %d times", len(fresh.calls))
			}
			// The agent adapter asks for the same request: the never-admitted row is claimed again under its own
			// identity and settles. The model adapter's model asks with a new call id: a new operation, and the
			// never-admitted row keeps its typed failure and unknown outcome.
			head, _ := store.Load(ctx, "runtime-action-run")
			var checkpoint struct {
				Durable struct {
					Dispatches []struct {
						ID    string `json:"id"`
						Kind  string `json:"kind"`
						State string `json:"state"`
					} `json:"dispatches"`
				} `json:"durable"`
			}
			_ = json.Unmarshal(head.Checkpoint, &checkpoint)
			runtime := []string{}
			for _, row := range checkpoint.Durable.Dispatches {
				if row.Kind == "runtime" {
					runtime = append(runtime, row.ID+":"+row.State)
				}
			}
			refusedID := wait.Pending.Reauthorization.Operation
			want := []string{refusedID + ":result"}
			if engine == "eino" {
				want = []string{refusedID + ":unknown", runtime[len(runtime)-1]}
			}
			if strings.Join(runtime, ",") != strings.Join(want, ",") || !strings.HasSuffix(runtime[len(runtime)-1], ":result") {
				t.Fatalf("runtime dispatches %v", runtime)
			}
			report, err := runcontext.WaitReportOf(ctx, store, "runtime-action-run", time.Time{})
			if err != nil || len(report.Entries) != 3 || report.Entries[0].Reason != "reauthorization" || report.Entries[2].Decision != "resume" {
				t.Fatalf("report %+v %v", report, err)
			}
			// Sentinel secrets: the credential check saw its token; nothing the package wrote, reports or
			// exports carries it, the principal's identity or a provider's error.
			if len(seen) == 0 {
				t.Fatal("the credential check never ran")
			}
			head, _ = store.Load(ctx, "runtime-action-run")
			exported, _ := runcontext.ExportWaits(ctx, store, "runtime-action-run")
			held, _ := store.ReadWait(ctx, "runtime-action-run", "wait-r")
			published := []string{string(head.Checkpoint)}
			for _, value := range []any{exported, held, report, wait, attention, resumed} {
				raw, _ := json.Marshal(value)
				published = append(published, string(raw))
			}
			page, _ := store.ReadEvents(ctx, "runtime-action-run", "", 500)
			for _, item := range page.Items {
				published = append(published, string(item.Bytes))
			}
			for _, secret := range []string{token, "sentinel-person@example.invalid"} {
				if strings.Contains(strings.Join(published, "\n"), secret) {
					t.Fatalf("%s was published", secret)
				}
			}
		})
	}
}

// An error the credential check gives, or a refusal no package dispatch minted, is no proof: nothing parks.
func TestReauthorizationNoProofWithoutTheTrustedRefusal(t *testing.T) {
	ctx := context.Background()
	store := waitStore{runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})}
	host := runcontext.NewReferenceEffectHostWith(store, runcontext.ReferenceEffectHostOptions{Credentials: func(context.Context, runcontext.EffectOperation) (*runcontext.CredentialRefusal, error) {
		return nil, errors.New("SENTINEL-PROVIDER-ERROR invalid_grant")
	}})
	a, err := store.AcquireLease(ctx, runcontext.LeaseAcquire{Resource: "runtime-action-run", Owner: "worker-a", DurationMs: 3_600_000})
	if err != nil {
		t.Fatal(err)
	}
	view, err := runcontext.UnattendedRunStore(store, runcontext.FenceOf(a), host)
	if err != nil {
		t.Fatal(err)
	}
	f := newRuntimeActionFixture(t)
	f.approve = func(gate.RuntimeApprovalRequest) string { return "allow" }
	var requests atomic.Int32
	_, _ = reauthRun(t, f, view, "eino", false, &requests)
	if len(f.calls) != 0 || runcontext.ReauthorizationOf(view) != nil {
		t.Fatalf("calls %d proof %v", len(f.calls), runcontext.ReauthorizationOf(view))
	}

	keyDigest, _ := runcontext.WakeKeyDigest("wk.adapter.reauth.Hs8Pq3Lz6Wd1Rm9Tc4Vb7Nx2Ab")
	var reauth *runcontext.ReauthorizationRefused
	if _, _, err := runcontext.ParkRun(ctx, runcontext.ParkInput{Store: store, Fence: runcontext.FenceOf(a), Wait: "wait-r", Continuation: "k", Condition: runcontext.WaitCondition{Event: &contract.RunWaitEventCondition{Kind: "reauthorized", Subject: "runtime-conn"}}, Model: runcontext.PinnedModel{Provider: "openai", Model: "fixture"}, WakeKeyDigest: keyDigest, Release: []runcontext.WaitRelease{}, Reauthorization: &runcontext.ReauthorizationRequired{}}); !errors.As(err, &reauth) || reauth.Reason != "untrusted" {
		t.Fatalf("a refusal nobody minted: %v", err)
	}
	head, _ := store.Load(ctx, "runtime-action-run")
	if head != nil && strings.Contains(string(head.Checkpoint), "SENTINEL") {
		t.Fatal("the credential check's error reached the checkpoint")
	}
}

// Re-authorization is never retry permission: without a resumed wait's grant, the continuation does not
// send again the call the run holds as unknown, though the credentials are current now and the call was
// never admitted. The agent adapter's continuation asks for the same request, under the same identity; the
// model adapter's model asks with a new call id, which is a new operation, so this is the agent adapter's.
// The TypeScript twin is "without a grant nothing the run holds as unknown is sent again".
func TestReauthorizationNoUnknownDispatchIsSentAgainWithoutAGrant(t *testing.T) {
	for _, engine := range []string{"native"} {
		t.Run(engine, func(t *testing.T) {
			ctx := context.Background()
			store := waitStore{runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})}
			var current atomic.Bool
			host := runcontext.NewReferenceEffectHostWith(store, runcontext.ReferenceEffectHostOptions{Credentials: func(context.Context, runcontext.EffectOperation) (*runcontext.CredentialRefusal, error) {
				if current.Load() {
					return nil, nil
				}
				return &runcontext.CredentialRefusal{Cause: "expired", Connection: "runtime-conn", Principal: "sha256:" + strings.Repeat("7", 64), Permissions: []string{}, Requires: []string{}}, nil
			}})
			a, err := store.AcquireLease(ctx, runcontext.LeaseAcquire{Resource: "runtime-action-run", Owner: "worker-a", DurationMs: 3_600_000})
			if err != nil {
				t.Fatal(err)
			}
			view, err := runcontext.UnattendedRunStore(store, runcontext.FenceOf(a), host)
			if err != nil {
				t.Fatal(err)
			}
			f := newRuntimeActionFixture(t)
			f.approve = func(gate.RuntimeApprovalRequest) string { return "allow" }
			var requests atomic.Int32
			_, _ = reauthRun(t, f, view, engine, false, &requests)
			if len(f.calls) != 0 || runcontext.ReauthorizationOf(view) == nil {
				t.Fatalf("calls %d", len(f.calls))
			}
			current.Store(true)
			plain, err := runcontext.UnattendedRunStore(store, runcontext.FenceOf(a), host)
			if err != nil {
				t.Fatal(err)
			}
			again := newRuntimeActionFixture(t)
			again.approve = func(gate.RuntimeApprovalRequest) string { return "allow" }
			_, _ = reauthRun(t, again, plain, engine, true, &requests)
			if len(again.calls) != 0 {
				t.Fatalf("the unknown call was sent again %d times", len(again.calls))
			}
		})
	}
}
