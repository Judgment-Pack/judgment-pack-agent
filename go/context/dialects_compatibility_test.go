package runcontext

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestDialectCompatibilityRetainedDrafts(t *testing.T) {
	raw, err := os.ReadFile("../../contract/testdata/dialect-authoring/compatibility.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors struct {
		Cases []struct {
			Mode        string   `json:"mode"`
			DraftStages []string `json:"draftStages"`
			Finals      int      `json:"finals"`
			Report      bool     `json:"report"`
			Preview     bool     `json:"preview"`
		} `json:"cases"`
	}
	if err = json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	for _, row := range vectors.Cases {
		t.Run(row.Mode, func(t *testing.T) {
			w := newDialectWorld(t)
			w.mode = row.Mode
			bound := w.read()
			_, err := w.propose(w.args(bound))
			if (err == nil) != (row.Finals == 1) {
				t.Fatalf("wrong final success %v", err)
			}
			stages := []string{}
			for _, d := range w.drafts {
				stages = append(stages, d.Stage)
				if d.Candidate.DocumentJSON != w.fixture.ExpectedDocument {
					t.Fatal("lost exact inspectable candidate")
				}
			}
			if !reflect.DeepEqual(stages, row.DraftStages) || len(w.retained) != row.Finals {
				t.Fatalf("wrong artifact trace %v finals%d", stages, len(w.retained))
			}
			if strings.Contains(strings.Join(w.calls, ","), "preview") != row.Preview {
				t.Fatal("wrong preview boundary")
			}
			if row.Report && (w.drafts[1].Check == nil || len(w.drafts[1].Check.Results) == 0) {
				t.Fatal("lost exact refused/accepted Runtime report")
			}
			if row.Mode == "complete" {
				if _, err = w.propose(w.args(bound)); err != nil {
					t.Fatal(err)
				}
				if len(w.retained) != 2 || len(w.drafts) != 4 || w.retained[0].Draft.AttemptID == w.retained[1].Draft.AttemptID || w.retained[0].Fingerprint != w.retained[1].Fingerprint {
					t.Fatal("explicit repeat did not preserve new review artifact and canonical identity")
				}
			}
		})
	}
}
func TestDialectCompatibilityUnknownDraftAcknowledgment(t *testing.T) {
	for _, stage := range []string{"candidate", "checked"} {
		for _, drift := range []string{"lost", "stage", "digest", "previousReceipt", "fingerprint", "abort"} {
			t.Run(stage+"/"+drift, func(t *testing.T) {
				w := newDialectWorld(t)
				original := w.ports.RetainDraft
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				w.ports.RetainDraft = func(c context.Context, d DialectDraftCheckpoint) (DialectDraftRetention, error) {
					ack, err := original(c, d)
					if d.Stage == stage {
						switch drift {
						case "lost":
							return ack, fmt.Errorf("unknown")
						case "stage":
							ack.Stage = "wrong"
						case "digest":
							ack.Digest = "wrong"
						case "previousReceipt":
							ack.PreviousReceipt = dialectStringPointer("wrong")
						case "fingerprint":
							ack.Fingerprint = "wrong"
						case "abort":
							cancel()
						}
					}
					return ack, err
				}
				w.tools = NewDialectTools(w.ports)
				bound := w.read()
				raw, _ := json.Marshal(w.args(bound))
				_, err := w.tools[1].Execute(ctx, raw)
				if err == nil || !strings.Contains(err.Error(), "unknown") {
					t.Fatalf("ack did not become unknown: %v", err)
				}
				before := len(w.calls)
				_, err = w.propose(w.args(bound))
				if err == nil || !strings.Contains(err.Error(), "unknown") {
					t.Fatal("nonce bypassed unknown stage")
				}
				if !reflect.DeepEqual(w.calls[before:], []string{"read", "field-index", "page"}) {
					t.Fatal("unknown acknowledgment continued effects", w.calls[before:])
				}
				expected := 1
				if stage == "checked" {
					expected = 2
				}
				if len(w.drafts) != expected || len(w.retained) != 0 || strings.Contains(strings.Join(w.calls, ","), "preview") {
					t.Fatal("unknown stage leaked effects")
				}
			})
		}
	}
}
func TestDialectCompatibilityBoundedFailureAndLineage(t *testing.T) {
	for _, mode := range []string{"malformed-check", "oversized-report"} {
		t.Run(mode, func(t *testing.T) {
			w := newDialectWorld(t)
			w.mode = mode
			bound := w.read()
			if _, err := w.propose(w.args(bound)); err == nil {
				t.Fatal("malformed report accepted")
			}
			last := w.drafts[1]
			raw, _ := json.Marshal(last)
			if len(raw) > DialectDraftBytes || last.Check == nil || last.Check.Failure == nil || last.Check.Failure.Code != "report-unavailable" || len(last.Check.Results) != 0 || !reflect.DeepEqual(last.Candidate, w.drafts[0].Candidate) {
				t.Fatal("bounded failure lost original or claimed report")
			}
		})
	}
	t.Run("canonical spelling preserves initial receipt lineage", func(t *testing.T) {
		w := newDialectWorld(t)
		original := w.ports.Runtime
		w.ports.Runtime = func(c context.Context, name string, args map[string]string) (ToolResult, error) {
			r, err := original(c, name, args)
			if err != nil || name == "get_dialect_contract" {
				return r, err
			}
			var body map[string]any
			_ = json.Unmarshal([]byte(r.Content[0].Value().Get("text").Str()), &body)
			var mapping any
			_ = json.Unmarshal([]byte(args["document"]), &mapping)
			canonical, _ := json.MarshalIndent(mapping, "", "  ")
			body["document"] = string(canonical)
			body["digest"] = dialectHash(string(canonical))
			encoded, _ := json.Marshal(body)
			return ResultOf([]Content{TextContent(string(encoded))}, nil), nil
		}
		w.tools = NewDialectTools(w.ports)
		bound := w.read()
		if _, err := w.propose(w.args(bound)); err != nil {
			t.Fatal(err)
		}
		for _, d := range w.drafts {
			raw, _ := json.Marshal(d)
			var content map[string]any
			_ = json.Unmarshal(raw, &content)
			delete(content, "digest")
			canonical, _ := dialectCanonical(content)
			if d.Digest != dialectHash(canonical) {
				t.Fatal("checkpoint digest did not bind exact content")
			}
		}
		initial, checked := w.drafts[0], w.drafts[1]
		lineage := w.retained[0].Draft
		if initial.Candidate.DocumentJSON != w.fixture.ExpectedDocument || w.retained[0].DocumentJSON == initial.Candidate.DocumentJSON || checked.PreviousReceipt == nil || *checked.PreviousReceipt != "draft:candidate" || lineage.AttemptID != initial.AttemptID || lineage.Fingerprint != initial.Fingerprint || lineage.CheckedDigest != checked.Digest {
			t.Fatal("canonical final silently rebound initial receipt")
		}
	})
	t.Run("final lineage drift latches candidate", func(t *testing.T) {
		w := newDialectWorld(t)
		original := w.ports.Retain
		w.ports.Retain = func(c context.Context, d DialectReviewProposal) (DialectRetention, error) {
			ack, err := original(c, d)
			ack.Draft.CheckedReceipt = "wrong"
			return ack, err
		}
		w.tools = NewDialectTools(w.ports)
		bound := w.read()
		if _, err := w.propose(w.args(bound)); err == nil {
			t.Fatal("lineage drift accepted")
		}
		before := len(w.calls)
		if _, err := w.propose(w.args(bound)); err == nil {
			t.Fatal("unknown final retried")
		}
		if len(w.retained) != 1 || !reflect.DeepEqual(w.calls[before:], []string{"read", "field-index", "page"}) {
			t.Fatal("unknown final continued effects")
		}
	})
}
func TestDialectCompatibilityRetainedDraftReviewAndFreshSave(t *testing.T) {
	w := newDialectWorld(t)
	w.mode = "failed-validation"
	bound := w.read()
	_, _ = w.propose(w.args(bound))
	if len(w.drafts) != 2 || len(w.retained) != 0 {
		t.Fatal("failed draft unavailable for review")
	}
	// Host keeps editable intent, then uses fresh tools for edited review. Its own Save CAS remains separate.
	edited := w.drafts[1].Candidate
	edited.Summary = "Human edited draft"
	w.mode = "complete"
	args := w.args(bound)
	args["summary"] = edited.Summary
	if _, err := w.propose(args); err != nil {
		t.Fatal(err)
	}
	currentRevision := "changed"
	saves := 0
	hostSave := func(proposal DialectReviewProposal) error {
		if proposal.Revision != currentRevision {
			return fmt.Errorf("Revision changed")
		}
		saves++
		return nil
	}
	if err := hostSave(w.retained[0]); err == nil || saves != 0 {
		t.Fatal("fresh Save CAS was bypassed by retained draft/proposal")
	}
}
func TestDialectCompatibilityRetentionVectors(t *testing.T) {
	raw, err := os.ReadFile("../../contract/vectors/dialect-authoring/retention.json")
	if err != nil {
		t.Fatal(err)
	}
	var rows struct {
		Items []json.RawMessage `json:"items"`
	}
	if err = json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	privateRaw, err := os.ReadFile("../../contract/testdata/dialect-authoring/superseded-private-draft-identity.json")
	if err != nil {
		t.Fatal(err)
	}
	var privateDraft DialectConversionContract
	if err = json.Unmarshal(privateRaw, &privateDraft); err != nil {
		t.Fatal(err)
	}
	rc2Raw, err := os.ReadFile("../../contract/testdata/dialect-authoring/superseded-rc2-identity.json")
	if err != nil {
		t.Fatal(err)
	}
	var rc2 DialectConversionContract
	if err = json.Unmarshal(rc2Raw, &rc2); err != nil {
		t.Fatal(err)
	}
	stages, operations, failures := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, raw := range rows.Items {
		var tag map[string]json.RawMessage
		_ = json.Unmarshal(raw, &tag)
		var target any
		if _, ok := tag["candidate"]; ok {
			target = &DialectDraftCheckpoint{}
		} else if _, ok = tag["status"]; ok {
			target = &DialectDraftRetention{}
		} else {
			target = &DialectDraftLineage{}
		}
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		decoder.DisallowUnknownFields()
		if err = decoder.Decode(target); err != nil {
			t.Fatal(err)
		}
		if c, ok := target.(*DialectDraftCheckpoint); ok {
			stages[c.Stage] = true
			if c.Candidate.ConversionContract.Contract != "" && c.Candidate.ConversionContract != DialectRetainedColumnDraft() && c.Candidate.ConversionContract != privateDraft && c.Candidate.ConversionContract != rc2 {
				t.Fatal("unsupported retention vector tuple")
			}
			if len(c.AttemptID) != 32 || c.Candidate.Columns[0].Samples[0].ValueJSON != "false" {
				t.Fatal("lost vector member")
			}
			if c.Check != nil {
				for _, r := range c.Check.Results {
					operations[r.Operation] = true
				}
				if c.Check.Failure != nil {
					failures[c.Check.Failure.Code] = true
				}
			}
		}
	}
	if len(stages) != 2 || len(operations) != 2 || len(failures) != 2 {
		t.Fatal("lost retention enum witnesses")
	}
}
