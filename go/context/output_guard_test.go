package runcontext

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestOutputSharedVectors(t *testing.T) {
	files, err := filepath.Glob("../../contract/vectors/output/*.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var vector struct {
			Guard *struct {
				Input, Output, Negative, Where, Mode, Refused string
				Allow                                         []string
				Patterns                                      []OutputPattern
				Counts                                        map[string]int64
			}
		}
		if err = json.Unmarshal(raw, &vector); err != nil {
			t.Fatal(err)
		}
		if vector.Guard == nil {
			continue
		}
		t.Run(filepath.Base(file), func(t *testing.T) {
			v := vector.Guard
			counts := map[string]int64{}
			allow := v.Allow
			if allow == nil {
				allow = []string{}
			}
			patterns := v.Patterns
			if patterns == nil {
				patterns = []OutputPattern{}
			}
			g, err := NewOutputGuard(PolicyMembers{"outputGuard": {Value: v.Mode}, "outputPatterns": {Value: patterns}, "outputAllow": {Value: allow}}, func() map[string]int64 { return counts })
			if err != nil {
				t.Fatal(err)
			}
			got, err := g.Text(v.Input, v.Where, true)
			if v.Refused != "" {
				if err == nil || err.Error() != (&OutputRefused{v.Refused}).Error() {
					t.Fatalf("refusal: %v", err)
				}
			} else if err != nil || got != v.Output {
				t.Fatalf("got %q, %v; want %q", got, err, v.Output)
			}
			if !reflect.DeepEqual(counts, v.Counts) {
				t.Fatalf("counts %v want %v", counts, v.Counts)
			}
			if v.Negative != "" {
				got, err = g.Text(v.Negative, v.Where, true)
				if err != nil || got != v.Negative || !reflect.DeepEqual(counts, v.Counts) {
					t.Fatalf("negative %q %v %v", got, err, counts)
				}
			}
		})
	}
}

func TestOutputValidationRefusals(t *testing.T) {
	schemaRaw, err := os.ReadFile("../../contract/schemas/run-event.json")
	if err != nil {
		t.Fatal(err)
	}
	schema, _ := ParseJSON(schemaRaw)
	for _, response := range []*Value{nil, Object(), ObjectOf("status", String("")), ObjectOf("status", String(strings.Repeat("x", 10000)))} {
		s := modelSession()
		s.Context = &Input{AcceptedDocuments: []AcceptedDocument{{ID: "pack", Document: json.RawMessage(`{"title":"Before"}`)}}}
		s.Tools = []RuntimeTool{{Name: "validate"}}
		calls := 0
		s.CallTool = func(_ context.Context, _ string, args json.RawMessage) (ToolResult, error) {
			calls++
			if strings.Contains(string(args), "AKIAABCDEFGHIJKLMNOP") || !strings.Contains(string(args), "[redacted: aws-access-key]") {
				t.Fatalf("validation did not see hole: %s", args)
			}
			return Answer(response), nil
		}
		opened, err := ContextualSession(context.Background(), s)
		if err != nil {
			t.Fatal(err)
		}
		g := opened.Context.Drafts
		if err = g.SeedContext(context.Background(), json.RawMessage(`{"title":"AKIAABCDEFGHIJKLMNOP"}`), nil); err == nil {
			t.Fatal("invalid proposal delivered")
		}
		if _, err = g.Edit(context.Background(), "propose_edit", json.RawMessage(`{"document":"pack","old_text":"Before","new_text":"AKIAABCDEFGHIJKLMNOP"}`)); err == nil {
			t.Fatal("invalid draft delivered")
		}
		if calls != 2 || !strings.Contains(string(g.Proposal().Document), "Before") {
			t.Fatal("validation or atomicity lost")
		}
		for _, event := range opened.Context.Output.TakeNotices() {
			raw, _ := json.Marshal(event)
			value, _ := ParseJSON(raw)
			if problem := ArgumentProblem(schema.Get("$defs").Get(value.Get("type").Str()), value); problem != "" {
				t.Fatal(problem)
			}
		}
		opened.Context.Close()
	}
}

func TestOutputMalformedRefusals(t *testing.T) {
	schemaRaw, err := os.ReadFile("../../contract/schemas/run-event.json")
	if err != nil {
		t.Fatal(err)
	}
	schema, _ := ParseJSON(schemaRaw)
	for _, input := range []json.RawMessage{nil, json.RawMessage(``), json.RawMessage(`{"secret":"AKIAABCDEFGHIJKLMNOP"}`), json.RawMessage(`{"oversized":"` + strings.Repeat("x", 200000) + `"}`)} {
		counts := map[string]int64{}
		g, err := NewOutputGuard(PolicyMembers{"outputGuard": {Value: "refuse"}}, func() map[string]int64 { return counts })
		if err != nil {
			t.Fatal(err)
		}
		if _, err = g.JSON(input, "tool argument", false); err == nil {
			t.Fatal("malformed or sensitive output was released")
		}
		notices := g.TakeNotices()
		if len(notices) != 2 {
			t.Fatalf("refusal notices: %d", len(notices))
		}
		for _, event := range notices {
			raw, _ := json.Marshal(event)
			value, _ := ParseJSON(raw)
			if problem := ArgumentProblem(schema.Get("$defs").Get(value.Get("type").Str()), value); problem != "" {
				t.Fatal(problem)
			}
		}
	}
}

func TestOutputNoticeGuard(t *testing.T) {
	const key = "AKIAABCDEFGHIJKLMNOP"
	for _, mode := range []string{"redact", "refuse", "off"} {
		t.Run(mode, func(t *testing.T) {
			s := modelSession()
			s.Policy = PolicyLayer{"outputGuard": mode}
			opened, err := ContextualSession(context.Background(), s)
			if err != nil {
				t.Fatal(err)
			}
			defer opened.Context.Close()
			_, err = opened.Context.Drafts.Edit(context.Background(), "propose_edit", json.RawMessage(`{"document":"AKIAABCDEFGHIJKLMNOP","old_text":"before","new_text":"after"}`))
			if err == nil {
				t.Fatal("missing document was accepted")
			}
			boundary := NewBoundary(opened)
			events, _ := boundary.Event(contract.RunEvent{End: &contract.RunEventEnd{Type: "end"}})
			found, guarded := false, false
			for _, event := range events {
				if event.OutputGuarded != nil && event.OutputGuarded.Pattern == "aws-access-key" {
					guarded = true
				}
				if event.EditRefused != nil {
					found = true
					want := "[redacted: aws-access-key]"
					if mode == "off" {
						want = key
					}
					if mode == "refuse" || event.EditRefused.Document == nil || *event.EditRefused.Document != want {
						t.Fatalf("notice: %+v", event.EditRefused)
					}
				}
				raw, _ := json.Marshal(event)
				if mode != "off" && strings.Contains(string(raw), key) {
					t.Fatalf("notice leaked: %s", raw)
				}
			}
			if found != (mode != "refuse") || guarded != (mode != "off") {
				t.Fatalf("found=%v guarded=%v", found, guarded)
			}
		})
	}
}

func TestOutputDraftScalars(t *testing.T) {
	for _, scalar := range []struct{ before, after, pattern string }{{"42", "123456", "123456"}, {"false", "true", "true"}, {`"before"`, "null", "null"}} {
		for _, mode := range []string{"redact", "refuse", "off"} {
			for _, valid := range []bool{true, false} {
				t.Run(fmt.Sprintf("%s/%s/%v", scalar.pattern, mode, valid), func(t *testing.T) {
					s := modelSession()
					s.Policy = PolicyLayer{"outputGuard": mode, "outputPatterns": []OutputPattern{{Name: "host-scalar", Pattern: scalar.pattern}}}
					prior := `{"value":` + scalar.before + `,"untouched":7}`
					s.Context = &Input{AcceptedDocuments: []AcceptedDocument{{ID: "pack", Document: json.RawMessage(prior)}}}
					s.Tools = []RuntimeTool{{Name: "validate"}}
					calls := 0
					s.CallTool = func(_ context.Context, _ string, args json.RawMessage) (ToolResult, error) {
						calls++
						v, _ := ParseJSON(args)
						var candidate map[string]any
						if err := json.Unmarshal([]byte(v.Get("content").Str()), &candidate); err != nil {
							t.Fatal(err)
						}
						if mode == "redact" && candidate["value"] != "[redacted: host-scalar]" {
							t.Fatalf("missing scalar hole: %v", candidate)
						}
						status := "invalid"
						if valid {
							status = "valid"
						}
						return Answer(ObjectOf("status", String(status))), nil
					}
					opened, err := ContextualSession(context.Background(), s)
					if err != nil {
						t.Fatal(err)
					}
					defer opened.Context.Close()
					before, _ := json.Marshal(opened.Context.Execution.State().Draft)
					args, _ := json.Marshal(map[string]string{"document": "pack", "old_text": scalar.before, "new_text": scalar.after})
					_, err = opened.Context.Drafts.Edit(context.Background(), "propose_edit", args)
					if (err != nil) != (mode == "refuse" || !valid) {
						t.Fatalf("unexpected edit error: %v", err)
					}
					wantCalls := 1
					if mode == "refuse" {
						wantCalls = 0
					}
					if calls != wantCalls {
						t.Fatalf("validation calls=%d", calls)
					}
					if mode == "refuse" || !valid {
						after, _ := json.Marshal(opened.Context.Execution.State().Draft)
						if string(before) != string(after) {
							t.Fatal("refusal changed prior draft checkpoint")
						}
						var want, got any
						json.Unmarshal([]byte(prior), &want)
						json.Unmarshal(opened.Context.Drafts.Proposal().Document, &got)
						if !reflect.DeepEqual(want, got) {
							t.Fatalf("prior draft changed: %v", got)
						}
					}
					if mode != "off" && opened.Context.Output.Counts()["host-scalar"] != 1 {
						t.Fatalf("scalar was not guarded: %v", opened.Context.Output.Counts())
					}
					if mode == "redact" && !valid && opened.Context.Output.Counts()["proposal-validation"] != 1 {
						t.Fatal("invalid hole was not refused")
					}
				})
			}
		}
	}
}

func TestOutputNoticeFields(t *testing.T) {
	key := "AKIAABCDEFGHIJKLMNOP"
	reason := "edit refused"
	for _, mode := range []string{"redact", "refuse", "off"} {
		for _, queued := range []bool{true, false} {
			s := modelSession()
			s.Policy = PolicyLayer{"outputGuard": mode}
			opened, err := ContextualSession(context.Background(), s)
			if err != nil {
				t.Fatal(err)
			}
			notices := []contract.RunEvent{
				{EditRefused: &contract.RunEventEditRefused{Type: "edit_refused", Document: &key, Reason: key}},
				{EditApplied: &contract.RunEventEditApplied{Type: "edit_applied", Document: key, Line: 1, Bytes: 1, Valid: true}},
				{Guardrail: &contract.RunEventGuardrail{Type: "guardrail", Tool: key, Detail: key, Action: "refused", Reason: &reason}},
			}
			b := NewBoundary(opened)
			var out []contract.RunEvent
			if queued {
				opened.Context.Drafts.notices = notices
				out = b.notices()
			} else {
				for _, n := range notices {
					events, stop := b.Event(n)
					out = append(out, events...)
					if stop {
						break
					}
				}
			}
			found := 0
			for _, e := range out {
				if e.EditRefused == nil && e.EditApplied == nil && (e.Guardrail == nil || e.Guardrail.Reason != nil && *e.Guardrail.Reason == "output refused") {
					continue
				}
				found++
				raw, _ := json.Marshal(e)
				if mode == "refuse" || mode == "redact" && strings.Contains(string(raw), key) {
					t.Fatalf("%s notice leaked: %s", mode, raw)
				}
				want := key
				if mode == "redact" {
					want = "[redacted: aws-access-key]"
				}
				if e.EditRefused != nil && (*e.EditRefused.Document != want || e.EditRefused.Reason != want) || e.EditApplied != nil && e.EditApplied.Document != want || e.Guardrail != nil && (e.Guardrail.Tool != want || e.Guardrail.Detail != want) {
					t.Fatalf("fields not guarded: %s", raw)
				}
			}
			if mode != "refuse" && found != 3 {
				t.Fatalf("missing notices: %d", found)
			}
			if queued || mode != "refuse" {
				events, _ := b.Event(contract.RunEvent{ToolResult: &contract.ToolResult{Type: "tool_result", Name: "fixture", Text: key}})
				if len(events) == 0 || events[len(events)-1].ToolResult == nil || events[len(events)-1].ToolResult.Text != key {
					t.Fatal("tool result changed")
				}
			}
			opened.Context.Close()
		}
	}
}

func TestOutputNoticeRedactionBound(t *testing.T) {
	opened, err := ContextualSession(context.Background(), modelSession())
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Context.Close()
	document := strings.Repeat("AKIAABCDEFGHIJKLMNOP ", 24) + strings.Repeat("x", 8)
	opened.Context.Drafts.notices = []contract.RunEvent{{EditRefused: &contract.RunEventEditRefused{Type: "edit_refused", Document: &document, Reason: "No draft."}}}
	for _, e := range NewBoundary(opened).notices() {
		if e.EditRefused != nil {
			t.Fatal("oversized guarded notice delivered")
		}
	}
	if opened.Context.Output.Counts()["invalid-output"] != 1 {
		t.Fatal("oversized guarded notice not refused")
	}
}

func TestOutputGateNoticeMalformedRefusal(t *testing.T) {
	schemaRaw, err := os.ReadFile("../../contract/schemas/run-event.json")
	if err != nil {
		t.Fatal(err)
	}
	schema, _ := ParseJSON(schemaRaw)
	for _, mode := range []string{"redact", "refuse"} {
		t.Run(mode, func(t *testing.T) {
			s := modelSession()
			s.Policy = PolicyLayer{"outputGuard": mode}
			opened, err := ContextualSession(context.Background(), s)
			if err != nil {
				t.Fatal(err)
			}
			boundary := NewBoundary(opened)
			defer boundary.Close()
			var events []contract.RunEvent
			g := gate.New(gate.Options{Emit: func(e contract.RunEvent) { events = append(events, e) }})
			defer boundary.GuardGateNotices(g.Calls())()
			result, err := g.CallTool(context.Background(), strings.Repeat("x", 200000), json.RawMessage(`{}`))
			if err != nil || !result.IsError {
				t.Fatalf("gate refusal changed: %v", err)
			}
			if len(events) != 0 {
				t.Fatal("oversized notice delivered")
			}
			out, _ := boundary.Event(contract.RunEvent{End: &contract.RunEventEnd{Type: "end"}})
			for _, event := range out {
				if event.Guardrail == nil && event.OutputGuarded == nil {
					continue
				}
				events = append(events, event)
				raw, _ := json.Marshal(event)
				value, _ := ParseJSON(raw)
				if problem := ArgumentProblem(schema.Get("$defs").Get(value.Get("type").Str()), value); problem != "" {
					t.Fatal(problem)
				}
			}
			if len(events) != 2 || events[0].OutputGuarded == nil || events[0].OutputGuarded.Pattern != "work-bound" || events[1].Guardrail == nil || events[1].Guardrail.Reason == nil || *events[1].Guardrail.Reason != "output refused" {
				t.Fatalf("refusal labels missing: %+v", events)
			}
		})
	}
}

func TestOutputUndeclaredNameMalformedRefusal(t *testing.T) {
	schemaRaw, err := os.ReadFile("../../contract/schemas/run-event.json")
	if err != nil {
		t.Fatal(err)
	}
	schema, _ := ParseJSON(schemaRaw)
	for _, mode := range []string{"redact", "refuse"} {
		t.Run(mode, func(t *testing.T) {
			s := modelSession()
			s.Policy = PolicyLayer{"outputGuard": mode}
			opened, err := ContextualSession(context.Background(), s)
			if err != nil {
				t.Fatal(err)
			}
			boundary := NewBoundary(opened)
			defer boundary.Close()
			original := &contract.ToolCall{Type: "tool_call", Name: strings.Repeat("x", 151), Args: json.RawMessage(`{}`)}
			events, stop := boundary.Event(contract.RunEvent{ToolCall: original})
			if stop || len(original.Name) != 151 {
				t.Fatal("dispatch changed")
			}
			labelled, called := false, false
			for _, event := range events {
				if event.ToolCall != nil {
					called = event.ToolCall.Name == "Output refused: invalid-output."
				}
				if event.OutputGuarded != nil {
					labelled = event.OutputGuarded.Pattern == "invalid-output"
				}
				if event.Guardrail != nil || event.OutputGuarded != nil {
					raw, _ := json.Marshal(event)
					value, _ := ParseJSON(raw)
					if problem := ArgumentProblem(schema.Get("$defs").Get(value.Get("type").Str()), value); problem != "" {
						t.Fatal(problem)
					}
				}
			}
			if !labelled || !called {
				t.Fatal("missing guarded name or refusal labels")
			}
		})
	}
}
