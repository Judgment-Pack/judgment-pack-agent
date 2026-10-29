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

// #243: the complete original inventory travels by digest and its declarations through read-port pages.

type dialectPagingVector struct {
	Items            []DialectDeclarationPage  `json:"items"`
	Inventory        []DialectResearchRevision `json:"inventory"`
	Index            *DialectResearchIndex     `json:"index"`
	ChangedInventory []DialectResearchRevision `json:"changedInventory"`
	ChangedIndex     *DialectResearchIndex     `json:"changedIndex"`
	Cases            []struct {
		Name                string                          `json:"name"`
		Pages               []int                           `json:"pages"`
		Expect              string                          `json:"expect"`
		Message             string                          `json:"message"`
		Requests            []DialectDeclarationPageRequest `json:"requests"`
		ChangeDuringPreview bool                            `json:"changeDuringPreview"`
		Generate            *struct {
			Revisions int `json:"revisions"`
			Fields    int `json:"fields"`
			NameBytes int `json:"nameBytes"`
		} `json:"generate"`
		IndexBytes int `json:"indexBytes"`
	} `json:"cases"`
}

func dialectPagingVectorFile(t *testing.T) dialectPagingVector {
	t.Helper()
	raw, err := os.ReadFile("../../contract/vectors/dialect-authoring/paging.json")
	if err != nil {
		t.Fatal(err)
	}
	var v dialectPagingVector
	if err = json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// dialectGeneratedInventory follows the vector's generation rule: names padded to nameBytes.
func dialectGeneratedInventory(revisions, fields, nameBytes int) []DialectResearchRevision {
	out := []DialectResearchRevision{}
	for i := 0; i < revisions; i++ {
		row := DialectResearchRevision{Revision: fmt.Sprintf("revision-%0*d", nameBytes-9, i), Fields: []DialectResearchField{}}
		for j := 0; j < fields; j++ {
			name := fmt.Sprintf("field-%0*d", nameBytes-6, j)
			row.Fields = append(row.Fields, DialectResearchField{Name: name, FieldJSON: `{"name":"` + name + `","type":"text"}`})
		}
		out = append(out, row)
	}
	return out
}

func TestDialectInventoryPagingVectors(t *testing.T) {
	v := dialectPagingVectorFile(t)
	if index, err := DialectResearchIndexOf(v.Inventory); err != nil || !reflect.DeepEqual(index, v.Index) {
		t.Fatal("the package index differs from the handwritten digests", err)
	}
	for _, row := range v.Cases {
		t.Run(row.Name, func(t *testing.T) {
			w := newDialectWorld(t)
			current := v.Index
			if row.Generate != nil {
				inventory := dialectGeneratedInventory(row.Generate.Revisions, row.Generate.Fields, row.Generate.NameBytes)
				index, err := DialectResearchIndexOf(inventory)
				if err != nil {
					t.Fatal(err)
				}
				if canonical, _ := dialectCanonical(index); len(canonical) != row.IndexBytes || len(canonical) <= DialectIndexBytes {
					t.Fatal("generated index differs from the vector's byte count", len(canonical))
				}
				current = index
			}
			w.ports.Read = func(context.Context, DialectReadRequest) (DialectSnapshot, error) {
				w.calls = append(w.calls, "read")
				s, err := dialectClone(w.snapshot)
				s.ResearchIndex = current
				return s, err
			}
			w.ports.ReadDeclarations = func(_ context.Context, request DialectDeclarationPageRequest) (DialectDeclarationPage, error) {
				w.calls = append(w.calls, "page")
				w.pages = append(w.pages, request)
				if len(w.pages) > 50 {
					return DialectDeclarationPage{}, fmt.Errorf("page call bound exceeded")
				}
				for _, at := range row.Pages {
					if v.Items[at].Cursor == request.Cursor {
						return dialectClone(v.Items[at])
					}
				}
				return DialectDeclarationPage{}, fmt.Errorf("no vector page at cursor %d", request.Cursor)
			}
			preview := w.ports.Preview
			w.ports.Preview = func(ctx context.Context, c DialectCandidate, f string) (DialectPreview, error) {
				if row.ChangeDuringPreview {
					current = v.ChangedIndex
				}
				return preview(ctx, c, f)
			}
			w.tools = NewDialectTools(w.ports)
			raw, _ := json.Marshal(w.fixture.Request)
			result, err := w.tools[0].Execute(context.Background(), raw)
			if err == nil {
				var bound DialectIdentity
				_ = json.Unmarshal([]byte(result.Content[0].Value().Get("text").Str()), &bound)
				_, err = w.propose(w.args(bound))
			}
			if row.Expect == "retained" {
				if err != nil || len(w.retained) != 1 {
					t.Fatal("paged inventory refused", err)
				}
				if !reflect.DeepEqual(w.retained[0].ResearchIndex, v.Index) || !reflect.DeepEqual(w.drafts[0].Candidate.ResearchIndex, v.Index) || !reflect.DeepEqual(w.preview.ResearchIndex, v.Index) {
					t.Fatal("the candidate lost the exact inventory index")
				}
				if !reflect.DeepEqual(w.pages[:len(row.Requests)], row.Requests) {
					t.Fatal("page requests differ from the vector", w.pages)
				}
				read, err := ReadDialectDeclarations(context.Background(), w.ports.ReadDeclarations, w.retained[0].ID, w.retained[0].PackKey, w.retained[0].Revision, w.retained[0].ResearchIndex)
				if err != nil || !reflect.DeepEqual(read, v.Inventory) {
					t.Fatal("the paged reader did not recover the exact inventory", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), row.Message) {
				t.Fatalf("expected refusal %q, got %v", row.Message, err)
			}
			if len(w.retained) != 0 {
				t.Fatal("a refused inventory reached retention")
			}
			if row.ChangeDuringPreview && (len(w.drafts) != 2 || w.preview == nil) {
				t.Fatal("the inventory change was not observed after preview")
			}
			if !row.ChangeDuringPreview && len(w.drafts) != 0 {
				t.Fatal("a refused page reached draft retention")
			}
		})
	}
}

// The issue's reproduction, built synthetically: 150 retained original revisions, 30 text fields each,
// each field with a distinct legal 1,000-byte description. Inline, the complete inventory exceeds the
// snapshot bound (v0.11.0 refused it); by digest with paged declarations it is read, proposed and retained.
func TestDialectInventoryDeskDomainReproduction(t *testing.T) {
	inventory := make([]DialectResearchRevision, 0, 150)
	for r := 1; r <= 150; r++ {
		row := DialectResearchRevision{Revision: fmt.Sprintf("recipe-revision:%03d", r), Fields: []DialectResearchField{}}
		for f := 1; f <= 30; f++ {
			name := fmt.Sprintf("field_%02d", f)
			description := fmt.Sprintf("revision %03d field %02d: ", r, f)
			description += strings.Repeat(string(rune('a'+(r*31+f)%26)), 1000-len(description))
			raw, _ := json.Marshal(map[string]any{"name": name, "type": "text", "required": false, "description": description})
			row.Fields = append(row.Fields, DialectResearchField{Name: name, FieldJSON: string(raw)})
		}
		inventory = append(inventory, row)
	}
	inline, _ := json.Marshal(inventory)
	if len(inline) <= DialectSnapshotBytes || len(inline) <= DialectCandidateBytes {
		t.Fatal("the reproduction must exceed the snapshot and candidate bounds inline", len(inline))
	}
	w := newDialectWorld(t)
	w.declarations = inventory
	served := 0
	pages := w.ports.ReadDeclarations
	w.ports.ReadDeclarations = func(ctx context.Context, request DialectDeclarationPageRequest) (DialectDeclarationPage, error) {
		page, err := pages(ctx, request)
		canonical, _ := dialectCanonical(page)
		if len(canonical) > DialectPageBytes || len(page.Declarations) > request.Limit || request.Limit > DialectPageDeclarations {
			t.Fatal("a served page exceeds its bounds", len(canonical))
		}
		served += len(page.Declarations)
		return page, err
	}
	w.tools = NewDialectTools(w.ports)
	if _, err := w.propose(w.args(w.read())); err != nil {
		t.Fatal(err)
	}
	if len(w.retained) != 1 || served != 2*150*30 || len(w.pages) < 2*5 {
		t.Fatal("the complete inventory was not paged twice", served, len(w.pages))
	}
	snapshot, _ := w.ports.Read(context.Background(), w.fixture.Request)
	for name, value := range map[string]any{"snapshot": snapshot, "candidate": w.drafts[0].Candidate, "draft": w.drafts[1], "proposal": w.retained[0]} {
		encoded, _ := json.Marshal(value)
		limit := map[string]int{"snapshot": DialectSnapshotBytes, "candidate": DialectCandidateBytes, "draft": DialectDraftBytes, "proposal": DialectRuntimeBytes}[name]
		if len(encoded) > limit {
			t.Fatal(name, "exceeds its bound", len(encoded))
		}
	}
	index := w.retained[0].ResearchIndex
	if index == nil || len(index.Revisions) != 150 || index.Revisions[149].Revision != "recipe-revision:150" || len(index.Revisions[0].Fields) != 30 || index.Revisions[0].Fields[29].Name != "field_30" {
		t.Fatal("the index lost revisions or field order")
	}
	read, err := ReadDialectDeclarations(context.Background(), w.ports.ReadDeclarations, w.retained[0].ID, w.retained[0].PackKey, w.retained[0].Revision, index)
	if err != nil || !reflect.DeepEqual(read, inventory) {
		t.Fatal("the paged reader did not recover the complete original inventory", err)
	}
}

func TestDialectInventoryPagedReadTool(t *testing.T) {
	inventory := retainedPolicyFixture(t)
	page := func(w *dialectTestWorld, cursor any) (map[string]any, error) {
		raw, _ := json.Marshal(map[string]any{"id": w.fixture.Request.ID, "packKey": w.fixture.Request.PackKey, "cursor": cursor})
		result, err := w.tools[0].Execute(context.Background(), raw)
		if err != nil {
			return nil, err
		}
		var out map[string]any
		_ = json.Unmarshal([]byte(result.Content[0].Value().Get("text").Str()), &out)
		return out, nil
	}
	t.Run("pages follow nextCursor to the end of the inventory", func(t *testing.T) {
		w := newDialectWorld(t)
		w.declarations = inventory
		w.pageSize = 2
		raw, _ := json.Marshal(w.fixture.Request)
		result, err := w.tools[0].Execute(context.Background(), raw)
		if err != nil {
			t.Fatal(err)
		}
		var shown map[string]any
		_ = json.Unmarshal([]byte(result.Content[0].Value().Get("text").Str()), &shown)
		summary, _ := shown["researchInventory"].(map[string]any)
		if _, inline := shown["researchDeclarations"]; inline || summary == nil || summary["digest"] != dialectTestIndex(t, inventory).Digest {
			t.Fatal("the read did not summarize the inventory by digest", shown)
		}
		seen := []DialectResearchField{}
		var cursor any = 0
		for cursor != nil {
			out, err := page(w, cursor)
			if err != nil {
				t.Fatal(err)
			}
			for _, d := range out["declarations"].([]any) {
				row := d.(map[string]any)
				seen = append(seen, DialectResearchField{Name: row["name"].(string), FieldJSON: row["fieldJson"].(string)})
			}
			cursor = out["nextCursor"]
		}
		want := []DialectResearchField{}
		for _, r := range inventory {
			want = append(want, r.Fields...)
		}
		if !reflect.DeepEqual(seen, want) {
			t.Fatal("paged declarations differ from the inventory")
		}
	})
	t.Run("refusals", func(t *testing.T) {
		for _, c := range []struct {
			name, message string
			change        func(*dialectTestWorld)
			cursor        any
		}{
			{"paging needs a prior read", "before paging", nil, 0},
			{"cursor outside the inventory", "outside the original declaration inventory", func(*dialectTestWorld) {}, 1000},
			{"negative cursor", "outside the original declaration inventory", func(*dialectTestWorld) {}, -1},
			{"null cursor", "outside the original declaration inventory", func(*dialectTestWorld) {}, nil},
			{"fractional cursor", "outside the original declaration inventory", func(*dialectTestWorld) {}, 1.5},
			{"inventory changed since the read", "changed; read it again", func(w *dialectTestWorld) { w.declarations = inventory[:1] }, 0},
		} {
			t.Run(c.name, func(t *testing.T) {
				w := newDialectWorld(t)
				w.declarations = inventory
				if c.change != nil {
					w.read()
					c.change(w)
				}
				if _, err := page(w, c.cursor); err == nil || !strings.Contains(err.Error(), c.message) {
					t.Fatalf("expected %q, got %v", c.message, err)
				}
			})
		}
	})
}

func TestDialectInventoryHostRefusals(t *testing.T) {
	inventory := retainedPolicyFixture(t)
	for _, c := range []struct{ name, message string }{
		{"missing page port", "requires the paged declaration read port"},
		{"index digest differs from its entries", "index digest does not match"},
		{"malformed index entry", "index is malformed"},
		{"page over its byte bound", "page exceeds its byte limit"},
		{"page over its declaration limit", "exceeds its declaration bound"},
		{"page from another inventory", "another collection revision or inventory"},
		{"page out of index order", "broke its order"},
		{"malformed declaration on a page", "actual original field declaration"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newDialectWorld(t)
			w.declarations = inventory
			read, pages := w.ports.Read, w.ports.ReadDeclarations
			w.ports.Read = func(ctx context.Context, r DialectReadRequest) (DialectSnapshot, error) {
				s, err := read(ctx, r)
				switch c.name {
				case "index digest differs from its entries":
					s.ResearchIndex.Digest = dialectHash("another inventory")
				case "malformed index entry":
					s.ResearchIndex.Revisions[0].Fields[0].Digest = "sha256:short"
				}
				return s, err
			}
			w.ports.ReadDeclarations = func(ctx context.Context, r DialectDeclarationPageRequest) (DialectDeclarationPage, error) {
				p, err := pages(ctx, r)
				switch c.name {
				case "page over its byte bound":
					p.Declarations[0].FieldJSON += strings.Repeat(" ", DialectPageBytes)
				case "page over its declaration limit":
					p.Declarations = append(p.Declarations, p.Declarations[0])
				case "page from another inventory":
					p.InventoryDigest = dialectHash("another inventory")
				case "page out of index order":
					p.Declarations[0], p.Declarations[1] = p.Declarations[1], p.Declarations[0]
				case "malformed declaration on a page":
					// The host's index and page agree on bytes that are not a declaration of that field.
					p.Declarations[0].FieldJSON = `{"name":"other","type":"text"}`
				}
				return p, err
			}
			if c.name == "missing page port" {
				w.ports.ReadDeclarations = nil
			}
			if c.name == "malformed declaration on a page" {
				w.declarations = []DialectResearchRevision{{Revision: "original", Fields: []DialectResearchField{{Name: "typed", FieldJSON: `{"name":"other","type":"text"}`}}}}
			}
			w.tools = NewDialectTools(w.ports)
			raw, _ := json.Marshal(w.fixture.Request)
			if _, err := w.tools[0].Execute(context.Background(), raw); err == nil || !strings.Contains(err.Error(), c.message) {
				t.Fatalf("expected %q, got %v", c.message, err)
			}
		})
	}
	t.Run("an index over its bound inside the snapshot bound", func(t *testing.T) {
		w := newDialectWorld(t)
		w.declarations = dialectGeneratedInventory(9, 1000, 160)
		snapshot, _ := w.ports.Read(context.Background(), w.fixture.Request)
		if encoded, _ := json.Marshal(snapshot); len(encoded) > DialectSnapshotBytes {
			t.Fatal("the over-bound index must fit the snapshot bound", len(encoded))
		}
		raw, _ := json.Marshal(w.fixture.Request)
		if _, err := w.tools[0].Execute(context.Background(), raw); err == nil || !strings.Contains(err.Error(), "index exceeds its byte limit") {
			t.Fatal("index bound missing", err)
		}
	})
}

func TestDialectInventoryChangedBeforePropose(t *testing.T) {
	w := newDialectWorld(t)
	w.declarations = retainedPolicyFixture(t)
	bound := w.read()
	w.declarations = w.declarations[:1]
	if _, err := w.propose(w.args(bound)); err == nil || !strings.Contains(err.Error(), "original declaration inventory changed; read it again before proposing") {
		t.Fatal("a proposal crossed an inventory change since its read", err)
	}
	if len(w.drafts) != 0 || len(w.retained) != 0 {
		t.Fatal("an inventory change reached retention")
	}
}
