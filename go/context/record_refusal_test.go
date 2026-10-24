package runcontext

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

func TestRecordRefusalStartupDoesNotPublishMember(t *testing.T) {
	const marker = "boundary_sentinel"
	for _, mode := range []string{"redact", "refuse"} {
		t.Run(mode, func(t *testing.T) {
			patterns := []OutputPattern{{Name: "record-marker", Pattern: marker}}
			guard, err := NewOutputGuard(PolicyMembers{"outputGuard": {Value: mode}, "outputPatterns": {Value: patterns}}, func() map[string]int64 { return map[string]int64{} })
			if err != nil {
				t.Fatal(err)
			}
			guarded, err := guard.Text(marker, "message", true)
			if mode == "refuse" {
				var refused *OutputRefused
				if !errors.As(err, &refused) {
					t.Fatal("guard control did not refuse")
				}
			} else if err != nil || guarded != "[redacted: record-marker]" {
				t.Fatal("guard control did not redact")
			}
			for _, kind := range []string{"checkpoint", "policy"} {
				for _, invalidPatterns := range []bool{false, true} {
					policy := PolicyLayer{"version": 1, "outputGuard": mode, "outputPatterns": patterns}
					if invalidPatterns {
						policy["outputPatterns"] = []OutputPattern{{Name: "record-marker", Pattern: "("}}
					}
					if invalidPatterns {
						if _, err := NewOutputGuard(PolicyMembers{"outputGuard": {Value: mode}, "outputPatterns": {Value: policy["outputPatterns"]}}, func() map[string]int64 { return map[string]int64{} }); err == nil {
							t.Fatal("invalid guard unexpectedly constructed")
						}
					}
					var cause error
					if kind == "policy" {
						session := modelSession()
						calls := 0
						session.CallTool = func(context.Context, string, json.RawMessage) (ToolResult, error) {
							calls++
							t.Error("startup called a tool")
							return ToolResult{}, nil
						}
						policy[marker] = true
						session.Policy = policy
						run, openErr := ContextualSession(context.Background(), session)
						if run != nil {
							run.Context.Close()
							t.Fatal("invalid policy opened a run")
						}
						cause = openErr
						if calls != 0 {
							t.Fatal("refused startup performed work")
						}
					} else {
						// Saved JSON is admitted before it can become the typed session checkpoint.
						saved := recordVector(t, "checkpoint").Clone().Set("version", Number(2)).Set(marker, Bool(true))
						_, cause = ReadExecution(saved)
					}
					var unknown *contract.UnknownRecordMember
					if !errors.As(cause, &unknown) || unknown.Member != "/"+marker || !strings.Contains(unknown.Error(), marker) {
						t.Fatalf("lost diagnostic member for %s: %v", kind, cause)
					}
					events := FailedToOpen(cause)
					if got := eventTypes(events); strings.Join(got, ",") != "guardrail,error,usage,end" {
						t.Fatalf("startup stream: %v", got)
					}
					notice := events[0].Guardrail
					if notice == nil || notice.Tool != "records" || notice.Action != "refused" || notice.Reason == nil || *notice.Reason != "record member unsupported" || notice.Detail != "Record contains an unsupported member." {
						t.Fatalf("named fixed refusal missing: %+v", notice)
					}
					for _, event := range events {
						raw, err := json.Marshal(event)
						if err != nil {
							t.Fatal(err)
						}
						if strings.Contains(string(raw), marker) {
							t.Fatalf("startup published member: %s", raw)
						}
						var obj map[string]any
						if err = json.Unmarshal(raw, &obj); err != nil {
							t.Fatal(err)
						}
						if obj["version"] != float64(1) {
							t.Fatalf("unversioned event: %s", raw)
						}
						if err = contract.CheckRecord("event", raw); err != nil {
							t.Fatalf("invalid startup event: %v", err)
						}
					}
					if events[1].Error == nil || events[1].Error.Failure == nil {
						t.Fatal("missing failure")
					}
					expectedCode := "stale-state"
					if kind == "policy" {
						expectedCode = "configuration"
					}
					if events[1].Error.Failure.Code != expectedCode {
						t.Fatal("failure classification changed")
					}
				}
			}
		})
	}
}

func TestRecordRefusalDiagnosticEventIsIndependent(t *testing.T) {
	for _, kind := range []string{"checkpoint", "event", "policy"} {
		for _, member := range []string{"", "boundary_sentinel", "boundary_sentinel" + strings.Repeat("x", 10000)} {
			var raw []byte
			if kind == "checkpoint" {
				raw = recordVector(t, "checkpoint").Clone().Set("version", Number(2)).Set(member, Bool(true)).JSON()
			} else if kind == "event" {
				raw, _ = json.Marshal(map[string]any{"version": 1, "type": "end", member: true})
			} else {
				raw, _ = json.Marshal(map[string]any{"version": 1, member: true})
			}
			err := contract.CheckRecord(kind, raw)
			var unknown *contract.UnknownRecordMember
			if !errors.As(err, &unknown) || unknown.Member != "/"+member {
				t.Fatalf("diagnostic pointer lost for %s: %v", kind, err)
			}
			bounded := "/" + member
			if len(bounded) > 256 {
				bounded = bounded[:256]
			}
			if unknown.Error() != "Unknown record member: "+bounded+"." {
				t.Fatal("diagnostic message changed")
			}
			event := unknown.Event()
			if event.Guardrail == nil || event.Guardrail.Detail != "Record contains an unsupported member." {
				t.Fatal("public event used diagnostic detail")
			}
			held, err := json.Marshal(event)
			if err != nil {
				t.Fatal(err)
			}
			if err = contract.CheckRecord("event", held); err != nil {
				t.Fatal(err)
			}
		}
	}
}
