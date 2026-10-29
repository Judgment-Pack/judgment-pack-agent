package runcontext

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

type dialectFixtureFile struct {
	Request  DialectReadRequest `json:"request"`
	Snapshot DialectSnapshot    `json:"snapshot"`
	Bindings struct {
		Facts    []DialectFactBinding     `json:"facts"`
		Evidence []DialectEvidenceBinding `json:"evidence"`
		Summary  string                   `json:"summary"`
	} `json:"bindings"`
	ExpectedDocument      string              `json:"expectedDocument"`
	ExpectedFingerprint   string              `json:"expectedFingerprint"`
	ExpectedMappingDigest string              `json:"expectedMappingDigest"`
	Conversions           []DialectConversion `json:"conversions"`
	Refusals              []struct {
		Name string          `json:"name"`
		Fact json.RawMessage `json:"fact"`
	} `json:"refusals"`
}

// The host's complete original inventory; the test read port serves its index and pages.
type dialectFixtureInventory struct {
	Snapshot struct {
		ResearchDeclarations []DialectResearchRevision `json:"researchDeclarations"`
	} `json:"snapshot"`
}
type dialectTestWorld struct {
	t        *testing.T
	fixture  dialectFixtureFile
	snapshot DialectSnapshot
	retained []DialectReviewProposal
	drafts   []DialectDraftCheckpoint
	calls    []string
	mode     string
	preview  *DialectCandidate
	tools    []HostTool
	ports    DialectAuthoringPorts
	// declarations is the host collection's complete original inventory (nil: unavailable).
	declarations []DialectResearchRevision
	pageSize     int
	pages        []DialectDeclarationPageRequest
	fieldIndexes []DialectFieldIndexRequest
}

// dialectServeFieldIndex is the test host's revision field index page for the requested top entry.
func dialectServeFieldIndex(inventory []DialectResearchRevision, request DialectFieldIndexRequest) (DialectFieldIndexPage, error) {
	page := DialectFieldIndexPage{ID: request.ID, PackKey: request.PackKey, Revision: request.Revision, InventoryDigest: request.InventoryDigest, Entry: request.Entry}
	if request.Entry < 0 || request.Entry >= len(inventory) || inventory[request.Entry].Fields == nil {
		return page, errors.New("no revision field index at this entry")
	}
	page.FieldIndex = *DialectFieldIndexOf(inventory[request.Entry])
	return page, nil
}

// dialectServePage is the test host's handwritten pager: the flattened inventory from the cursor.
func dialectServePage(inventory []DialectResearchRevision, request DialectDeclarationPageRequest, size int) DialectDeclarationPage {
	flat := []DialectDeclaration{}
	for _, r := range inventory {
		for _, f := range r.Fields {
			flat = append(flat, DialectDeclaration{Revision: r.Revision, Name: f.Name, FieldJSON: f.FieldJSON})
		}
	}
	page := DialectDeclarationPage{ID: request.ID, PackKey: request.PackKey, Revision: request.Revision, InventoryDigest: request.InventoryDigest, Cursor: request.Cursor, Declarations: []DialectDeclaration{}}
	// The host fills a page up to the declaration limit and the page byte bound.
	empty, _ := dialectCanonical(page)
	total := len(empty)
	for at := request.Cursor; at < len(flat) && len(page.Declarations) < min(size, request.Limit); at++ {
		one, _ := dialectCanonical(flat[at])
		if total+len(one)+1 > DialectPageBytes {
			break
		}
		total += len(one) + 1
		page.Declarations = append(page.Declarations, flat[at])
	}
	return page
}

func newDialectWorld(t *testing.T) *dialectTestWorld {
	t.Helper()
	raw, err := os.ReadFile("../../contract/testdata/dialect-authoring/authoring.json")
	if err != nil {
		t.Fatal(err)
	}
	w := &dialectTestWorld{t: t}
	if err = json.Unmarshal(raw, &w.fixture); err != nil {
		t.Fatal(err)
	}
	var inventory dialectFixtureInventory
	if err = json.Unmarshal(raw, &inventory); err != nil {
		t.Fatal(err)
	}
	w.declarations = inventory.Snapshot.ResearchDeclarations
	w.pageSize = DialectPageDeclarations
	w.snapshot, _ = dialectClone(w.fixture.Snapshot)
	selection := DialectRetainedColumnDraft()
	w.ports = DialectAuthoringPorts{
		ConversionContract: &selection,
		Read: func(context.Context, DialectReadRequest) (DialectSnapshot, error) {
			w.calls = append(w.calls, "read")
			s, err := dialectClone(w.snapshot)
			if err == nil {
				s.ResearchIndex, err = DialectResearchIndexOf(w.declarations)
			}
			return s, err
		},
		ReadFieldIndex: func(_ context.Context, request DialectFieldIndexRequest) (DialectFieldIndexPage, error) {
			w.calls = append(w.calls, "field-index")
			w.fieldIndexes = append(w.fieldIndexes, request)
			return dialectServeFieldIndex(w.declarations, request)
		},
		ReadDeclarations: func(_ context.Context, request DialectDeclarationPageRequest) (DialectDeclarationPage, error) {
			w.calls = append(w.calls, "page")
			w.pages = append(w.pages, request)
			return dialectServePage(w.declarations, request, w.pageSize), nil
		},
		Runtime: func(_ context.Context, name string, args map[string]string) (ToolResult, error) {
			w.calls = append(w.calls, name)
			limits := map[string]any{"documentBytes": 1048576, "frameBytes": 2097152}
			r := map[string]any{"contractVersion": "1", "specification": "https://github.com/Judgment-Pack/judgment-pack-spec/blob/" + selection.Spec.Revision + "/spec/retained-column.md", "specRevision": selection.Spec.Revision, "diagnostics": []any{}, "contract": selection.Contract, "profile": selection.Profile, "spec": selection.Spec, "role": "document-validation", "maturity": "draft", "limits": limits}
			if name == "get_dialect_contract" {
				r["status"] = "available"
				r["capabilities"] = map[string]any{"conversionContracts": []any{map[string]any{"contract": selection.Contract, "spec": selection.Spec, "profile": selection.Profile, "role": "document-validation", "limits": limits}}, "packCheckScope": "declared-inventory-coverage", "operations": []string{"validate", "check"}, "maxPackBytes": 10485760}
				if w.mode == "small-runtime-bound" {
					limits["documentBytes"] = 1
				}
				if w.mode == "unsupported" {
					r["capabilities"].(map[string]any)["conversionContracts"] = []any{}
				}
			} else {
				r["dialectVersion"] = 2
				r["document"] = args["document"]
				r["digest"] = dialectHash(args["document"])
				if name == "dialect_validate" {
					r["status"] = "document-valid"
					r["validationScope"] = "experimental-retained-column-base-document"
				} else {
					status := "complete"
					var mapping struct {
						Unmapped         []string `json:"unmapped"`
						UnmappedEvidence []string `json:"unmappedEvidence"`
					}
					_ = json.Unmarshal([]byte(args["document"]), &mapping)
					if len(mapping.Unmapped)+len(mapping.UnmappedEvidence) > 0 {
						status = "review-required"
					}
					if w.mode == "incomplete" {
						status = "incomplete"
					}
					r["status"] = status
					r["validationScope"] = "experimental-retained-column-base-document-and-declared-pack-inventory"
					r["compatibility"] = map[string]any{"status": status, "scope": "declared-inventory-coverage", "pack": map[string]any{"sha256": dialectHash(args["pack"])[7:]}, "valueTypesChecked": false, "executionChecked": false, "facts": []map[string]any{{"target": "/amount"}}, "evidence": []map[string]any{{"target": "receipt"}}}
				}
				if w.mode == "wrong-document" {
					r["document"] = "{}"
					r["digest"] = dialectHash("{}")
				}
				if w.mode == "wrong-digest" && name == "dialect_validate" {
					r["digest"] = "sha256:" + strings.Repeat("0", 64)
				}
				if w.mode == "encoded" && name == "dialect_validate" {
					r["status"] = "encoded"
				}
				if w.mode == "wrong-pack" && name == "dialect_check" {
					r["compatibility"].(map[string]any)["pack"] = map[string]any{"sha256": strings.Repeat("0", 64)}
				}
				if w.mode == "wrong-inventory" && name == "dialect_check" {
					r["compatibility"].(map[string]any)["facts"] = []any{}
				}
			}
			if w.mode == "transport-failed" && name == "dialect_check" {
				return ToolResult{}, fmt.Errorf("Runtime unavailable")
			}
			if w.mode == "failed-validation" && name == "dialect_validate" {
				r["status"] = "refused"
			}
			if w.mode == "oversized-report" && name == "dialect_check" {
				return ResultOf([]Content{TextContent(strings.Repeat("x", DialectRuntimeBytes+1))}, nil), nil
			}
			if w.mode == "malformed-check" && name == "dialect_check" {
				return ResultOf([]Content{TextContent("{")}, nil), nil
			}
			if w.mode == "malformed" {
				return ResultOf([]Content{TextContent("{")}, nil), nil
			}
			encoded, _ := json.Marshal(r)
			return ResultOf([]Content{TextContent(string(encoded))}, nil), nil
		},
		Preview: func(_ context.Context, candidate DialectCandidate, fingerprint string) (DialectPreview, error) {
			w.calls = append(w.calls, "preview")
			w.preview = &candidate
			if w.mode == "preview-failed" {
				return DialectPreview{}, fmt.Errorf("Preview failed")
			}
			if w.mode == "changed-preview" {
				w.snapshot.PackJSON += " "
			}
			if w.mode == "wrong-preview" {
				fingerprint = "wrong"
			}
			return DialectPreview{Fingerprint: fingerprint, RecordCount: w.snapshot.RecordCount, Issues: []DialectPreviewIssue{}}, nil
		},
		RetainDraft: func(_ context.Context, c DialectDraftCheckpoint) (DialectDraftRetention, error) {
			w.calls = append(w.calls, "draft:"+c.Stage)
			w.drafts = append(w.drafts, c)
			return DialectDraftRetention{Fingerprint: c.Fingerprint, Stage: c.Stage, Digest: c.Digest, PreviousReceipt: c.PreviousReceipt, Receipt: "draft:" + c.Stage, Status: "draft"}, nil
		},
		Retain: func(_ context.Context, proposal DialectReviewProposal) (DialectRetention, error) {
			w.calls = append(w.calls, "retain")
			w.retained = append(w.retained, proposal)
			if w.mode == "lost-ack" {
				return DialectRetention{}, fmt.Errorf("unavailable")
			}
			fingerprint := proposal.Fingerprint
			if w.mode == "wrong-ack" {
				fingerprint = "wrong"
			}
			return DialectRetention{Draft: proposal.Draft, Fingerprint: fingerprint, Receipt: "review:one", Status: "review-required"}, nil
		},
	}
	w.tools = NewDialectTools(w.ports)
	return w
}
func (w *dialectTestWorld) read() DialectIdentity {
	w.t.Helper()
	raw, _ := json.Marshal(w.fixture.Request)
	result, err := w.tools[0].Execute(context.Background(), raw)
	if err != nil {
		w.t.Fatal(err)
	}
	var read DialectIdentity
	if err = json.Unmarshal([]byte(result.Content[0].Value().Get("text").Str()), &read); err != nil {
		w.t.Fatal(err)
	}
	return read
}
func (w *dialectTestWorld) args(bound DialectIdentity) map[string]any {
	encoded, _ := json.Marshal(bound)
	var args map[string]any
	_ = json.Unmarshal(encoded, &args)
	args["facts"] = w.fixture.Bindings.Facts
	args["evidence"] = w.fixture.Bindings.Evidence
	args["summary"] = w.fixture.Bindings.Summary
	return args
}
func (w *dialectTestWorld) propose(args map[string]any) (ToolResult, error) {
	raw, _ := json.Marshal(args)
	return w.tools[1].Execute(context.Background(), raw)
}
func TestDialectAuthoringSharedVectors(t *testing.T) {
	t.Run("all input schema witnesses", func(t *testing.T) {
		raw, err := os.ReadFile("../../contract/vectors/dialect-authoring/inputs.json")
		if err != nil {
			t.Fatal(err)
		}
		var vectors struct {
			Items []map[string]json.RawMessage `json:"items"`
		}
		if err = json.Unmarshal(raw, &vectors); err != nil {
			t.Fatal(err)
		}
		for _, row := range vectors.Items {
			w := newDialectWorld(t)
			encoded, _ := json.Marshal(row)
			_, limit := row["limit"]
			_, declarations := row["declarations"]
			_, cursor := row["cursor"]
			_, entry := row["entry"]
			_, fieldIndex := row["fieldIndex"]
			if fieldIndex {
				// The read port's revision field index page, served verbatim and accepted.
				var page DialectFieldIndexPage
				_ = json.Unmarshal(encoded, &page)
				w.ports.ReadFieldIndex = func(context.Context, DialectFieldIndexRequest) (DialectFieldIndexPage, error) { return page, nil }
				w.tools = NewDialectTools(w.ports)
				w.read()
			} else if entry {
				// The read port's revision field index request: the factory's own first request.
				var want DialectFieldIndexRequest
				_ = json.Unmarshal(encoded, &want)
				w.read()
				if len(w.fieldIndexes) == 0 || w.fieldIndexes[0] != want {
					t.Fatal("field index request witness differs", w.fieldIndexes)
				}
			} else if limit {
				// The read port's page request: the factory's own first request for the fixture inventory.
				var want DialectDeclarationPageRequest
				_ = json.Unmarshal(encoded, &want)
				w.read()
				if len(w.pages) == 0 || w.pages[0] != want {
					t.Fatal("page request witness differs", w.pages)
				}
			} else if declarations {
				// The read port's page, served verbatim and accepted.
				var page DialectDeclarationPage
				_ = json.Unmarshal(encoded, &page)
				w.ports.ReadDeclarations = func(context.Context, DialectDeclarationPageRequest) (DialectDeclarationPage, error) { return page, nil }
				w.tools = NewDialectTools(w.ports)
				w.read()
			} else if cursor {
				w.read()
				if _, err = w.tools[0].Execute(context.Background(), encoded); err != nil {
					t.Fatal(err)
				}
			} else if _, propose := row["revision"]; propose {
				if string(row["dialectDigest"]) == "null" {
					w.snapshot.DialectJSON = nil
				}
				w.read()
				if _, err = w.tools[1].Execute(context.Background(), encoded); err != nil {
					t.Fatal(err)
				}
				if len(w.retained) != 1 {
					t.Fatal("input witness lost retention")
				}
			} else if _, err = w.tools[0].Execute(context.Background(), encoded); err != nil {
				t.Fatal(err)
			}
		}
	})
	t.Run("otherwise exact input needs prior read", func(t *testing.T) {
		w := newDialectWorld(t)
		if _, err := w.propose(w.args(dialectIdentity(w.snapshot))); err == nil || !strings.Contains(err.Error(), "read the exact") {
			t.Fatal("prior read safeguard missing", err)
		}
	})
	t.Run("explicit omissions require review", func(t *testing.T) {
		w := newDialectWorld(t)
		a := w.args(w.read())
		a["facts"] = []DialectFactBinding{}
		a["evidence"] = []DialectEvidenceBinding{}
		r, err := w.propose(a)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(r.Content[0].Value().Get("text").Str(), `"coverageStatus":"review-required"`) {
			t.Fatal("omission became complete")
		}
	})

	t.Run("complete replacement exact data and one retention", func(t *testing.T) {
		w := newDialectWorld(t)
		bound := w.read()
		if _, err := w.propose(w.args(bound)); err != nil {
			t.Fatal(err)
		}
		if len(w.retained) != 1 || w.retained[0].DocumentJSON != w.fixture.ExpectedDocument || w.retained[0].Fingerprint != w.fixture.ExpectedFingerprint || w.retained[0].MappingDigest != w.fixture.ExpectedMappingDigest {
			t.Fatalf("wrong replacement: %#v", w.retained)
		}
		if strings.Join(w.calls, ",") != "read,field-index,page,read,field-index,page,draft:candidate,get_dialect_contract,dialect_validate,dialect_check,draft:checked,preview,read,retain" {
			t.Fatal(w.calls)
		}
		if w.retained[0].PackJSON != w.fixture.Snapshot.PackJSON || *bound.DialectDigest != dialectHash(*w.fixture.Snapshot.DialectJSON) {
			t.Fatal("raw identity changed")
		}
		left, _ := json.Marshal(w.preview.Columns)
		right, _ := json.Marshal(w.fixture.Snapshot.Columns)
		if string(left) != string(right) {
			t.Fatal("exact sample text changed")
		}
		if _, err := w.propose(w.args(bound)); err != nil {
			t.Fatal(err)
		}
		if len(w.retained) != 2 {
			t.Fatal("new explicit proposal did not create a fresh review artifact")
		}
	})
	w := newDialectWorld(t)
	for _, c := range w.fixture.Conversions {
		t.Run("conversion "+c.Kind, func(t *testing.T) {
			w := newDialectWorld(t)
			bound := w.read()
			args := w.args(bound)
			column := "amount/raw~value"
			if c.Kind == "research-value" {
				column = "typed"
			}
			args["facts"] = []DialectFactBinding{{Target: "/amount", Column: column, Conversion: c}}
			if _, err := w.propose(args); err != nil {
				t.Fatal(err)
			}
			if w.retained[0].Conversions["/amount"].Kind != c.Kind {
				t.Fatal("conversion lost")
			}
		})
	}
	for _, r := range w.fixture.Refusals {
		t.Run(r.Name, func(t *testing.T) {
			w := newDialectWorld(t)
			bound := w.read()
			args := w.args(bound)
			args["facts"] = json.RawMessage("[" + string(r.Fact) + "]")
			if _, err := w.propose(args); err == nil {
				t.Fatal("accepted malformed binding")
			}
			if len(w.retained) != 0 {
				t.Fatal("refusal retained proposal")
			}
		})
	}
	for _, mode := range []string{"small-runtime-bound", "unsupported", "encoded", "incomplete", "wrong-document", "wrong-digest", "wrong-pack", "wrong-inventory", "malformed", "wrong-preview", "changed-preview"} {
		t.Run(mode, func(t *testing.T) {
			w := newDialectWorld(t)
			bound := w.read()
			w.mode = mode
			if _, err := w.propose(w.args(bound)); err == nil {
				t.Fatal("accepted mismatched or refused check")
			}
			if len(w.retained) != 0 {
				t.Fatal("refusal retained proposal")
			}
		})
	}
	for _, member := range []string{"pack", "dialect"} {
		t.Run("exact "+member+" bytes repeated revision", func(t *testing.T) {
			w := newDialectWorld(t)
			bound := w.read()
			if member == "pack" {
				w.snapshot.PackJSON += " "
			} else {
				*w.snapshot.DialectJSON += " "
			}
			_, err := w.propose(w.args(bound))
			if strings.Join(w.calls, ",") != "read,field-index,page,read" {
				t.Fatal("stale snapshots reached Runtime", w.calls)
			}
			if err == nil || !strings.Contains(err.Error(), "changed") {
				t.Fatal("raw drift admitted", err)
			}
		})
	}
	for _, mode := range []string{"lost-ack", "wrong-ack"} {
		t.Run(mode, func(t *testing.T) {
			w := newDialectWorld(t)
			bound := w.read()
			w.mode = mode
			for i := 0; i < 2; i++ {
				if _, err := w.propose(w.args(bound)); err == nil || !strings.Contains(err.Error(), "unknown") {
					t.Fatal("unknown acknowledgment admitted", err)
				}
			}
			if len(w.retained) != 1 {
				t.Fatal("unknown retention retried")
			}
		})
	}
	t.Run("malformed bounds and declarations", func(t *testing.T) {
		w := newDialectWorld(t)
		for _, raw := range []string{`{}`, `{"id":"x","packKey":"p","extra":true}`, `{"id":"x","id":"y","packKey":"p"}`, `{"id":"` + strings.Repeat("x", 513) + `","packKey":"p"}`} {
			if _, err := w.tools[0].Execute(context.Background(), json.RawMessage(raw)); err == nil {
				t.Fatal("malformed read accepted")
			}
		}
		bound := w.read()
		args := w.args(bound)
		args["facts"] = append(w.fixture.Bindings.Facts, w.fixture.Bindings.Facts[0])
		if _, err := w.propose(args); err == nil {
			t.Fatal("duplicate accepted")
		}
		w.snapshot.Columns[2].Research.FieldJSON = "{}"
		raw, _ := json.Marshal(w.fixture.Request)
		if _, err := w.tools[0].Execute(context.Background(), raw); err == nil {
			t.Fatal("invented original declaration accepted")
		}
	})
	t.Run("sample display clipping and bounded source", func(t *testing.T) {
		w := newDialectWorld(t)
		encoded, _ := json.Marshal(strings.Repeat("界", 100))
		w.snapshot.Columns[0].Samples[0].ValueJSON = string(encoded)
		bound := w.read()
		if _, err := w.propose(w.args(bound)); err != nil {
			t.Fatal(err)
		}
		if w.preview.Columns[0].Samples[0].ValueJSON != string(encoded) {
			t.Fatal("clipped source replaced original")
		}
		w.snapshot.Columns[0].Samples = append(w.snapshot.Columns[0].Samples, DialectSample{ValueJSON: "0", Origin: "asserted"})
		raw, _ := json.Marshal(w.fixture.Request)
		if _, err := w.tools[0].Execute(context.Background(), raw); err == nil {
			t.Fatal("oversized sample count accepted")
		}
	})
	for _, stage := range []string{"get_dialect_contract", "dialect_validate", "dialect_check", "preview", "current-read", "retain"} {
		t.Run("cancellation acknowledgment "+stage, func(t *testing.T) {
			w := newDialectWorld(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			runtimePort, readPort, previewPort, retainPort := w.ports.Runtime, w.ports.Read, w.ports.Preview, w.ports.Retain
			reads := 0
			w.ports.Runtime = func(c context.Context, name string, args map[string]string) (ToolResult, error) {
				r, e := runtimePort(c, name, args)
				if stage == name {
					cancel()
				}
				return r, e
			}
			w.ports.Read = func(c context.Context, r DialectReadRequest) (DialectSnapshot, error) {
				s, e := readPort(c, r)
				reads++
				if stage == "current-read" && reads == 3 {
					cancel()
				}
				return s, e
			}
			w.ports.Preview = func(c context.Context, a DialectCandidate, f string) (DialectPreview, error) {
				r, e := previewPort(c, a, f)
				if stage == "preview" {
					cancel()
				}
				return r, e
			}
			w.ports.Retain = func(c context.Context, a DialectReviewProposal) (DialectRetention, error) {
				r, e := retainPort(c, a)
				if stage == "retain" {
					cancel()
				}
				return r, e
			}
			w.tools = NewDialectTools(w.ports)
			bound := w.read()
			raw, _ := json.Marshal(w.args(bound))
			_, err := w.tools[1].Execute(ctx, raw)
			if err == nil {
				t.Fatal("cancellation ignored")
			}
			expected := 0
			if stage == "retain" {
				expected = 1
				if !strings.Contains(err.Error(), "unknown") {
					t.Fatal("cancellation after retention is not unknown", err)
				}
			}
			if len(w.retained) != expected {
				t.Fatal("cancelled retention count", len(w.retained))
			}
		})
	}
	t.Run("cancelled before dispatch", func(t *testing.T) {
		w := newDialectWorld(t)
		bound := w.read()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		raw, _ := json.Marshal(w.args(bound))
		if _, err := w.tools[1].Execute(ctx, raw); err == nil {
			t.Fatal("cancellation ignored")
		}
		if len(w.retained) != 0 {
			t.Fatal("cancelled retention")
		}
	})
}
