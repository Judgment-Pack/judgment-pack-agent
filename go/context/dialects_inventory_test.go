package runcontext

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// #243, #252: the complete original inventory travels as a bounded top index; revision field indexes
// and declarations arrive through read-port pages.

type dialectPagingVector struct {
	Items              []DialectDeclarationPage   `json:"items"`
	FieldIndexes       []DialectFieldIndexPage    `json:"fieldIndexes"`
	FieldIndexRequests []DialectFieldIndexRequest `json:"fieldIndexRequests"`
	Inventory          []DialectResearchRevision  `json:"inventory"`
	Index              *DialectResearchIndex      `json:"index"`
	ChangedInventory   []DialectResearchRevision  `json:"changedInventory"`
	ChangedIndex       *DialectResearchIndex      `json:"changedIndex"`
	Cases              []struct {
		Name                string                          `json:"name"`
		Pages               []int                           `json:"pages"`
		FieldIndexPages     []int                           `json:"fieldIndexPages"`
		Expect              string                          `json:"expect"`
		Message             string                          `json:"message"`
		Requests            []DialectDeclarationPageRequest `json:"requests"`
		ChangeDuringPreview bool                            `json:"changeDuringPreview"`
	} `json:"cases"`
	Bounds []dialectBoundCase `json:"bounds"`
}

// dialectBoundCase is one row of the #252 bound proof: a generated inventory or top index and the
// canonical byte lengths computed outside the package.
type dialectBoundCase struct {
	Name      string `json:"name"`
	Inventory *struct {
		Revisions int    `json:"revisions"`
		Fields    int    `json:"fields"`
		Label     string `json:"label"`
		Name      string `json:"name"`
	} `json:"inventory"`
	Top *struct {
		Revisions int `json:"revisions"`
		Fields    int `json:"fields"`
		Bytes     int `json:"bytes"`
	} `json:"top"`
	IndexBytes         int    `json:"indexBytes"`
	FieldIndexBytes    int    `json:"fieldIndexBytes"`
	OneLevelIndexBytes int    `json:"oneLevelIndexBytes"`
	Expect             string `json:"expect"`
	Message            string `json:"message"`
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

// dialectBoundLabel and dialectBoundName follow the vector's generation rules.
func dialectBoundLabel(kind string, r int) string {
	switch kind {
	case "issue-252":
		return "prep_" + strings.Repeat("c", 32) + ":recipe:" + strconv.Itoa(r+1)
	case "max":
		return strings.Repeat("r", 156) + fmt.Sprintf("%04d", r)
	}
	// "escaped": 157 U+0001, then r in base 27 over the control characters JSON escapes sixfold.
	digits := []rune{}
	for c := rune(0); c < 32; c++ {
		if c != 8 && c != 9 && c != 10 && c != 12 && c != 13 {
			digits = append(digits, c)
		}
	}
	return strings.Repeat("\u0001", 157) + string([]rune{digits[r/729], digits[r/27%27], digits[r%27]})
}
func dialectBoundName(kind string, f int) string {
	if kind == "wide" {
		return strings.Repeat("w", 157) + fmt.Sprintf("%03d", f)
	}
	return "f" + strings.Repeat("a", 61) + fmt.Sprintf("%02d", f)
}
func dialectBoundInventory(c dialectBoundCase) []DialectResearchRevision {
	out := make([]DialectResearchRevision, 0, c.Inventory.Revisions)
	for r := 0; r < c.Inventory.Revisions; r++ {
		row := DialectResearchRevision{Revision: dialectBoundLabel(c.Inventory.Label, r), Fields: make([]DialectResearchField, 0, c.Inventory.Fields)}
		for f := 0; f < c.Inventory.Fields; f++ {
			name := dialectBoundName(c.Inventory.Name, f)
			row.Fields = append(row.Fields, DialectResearchField{Name: name, FieldJSON: fmt.Sprintf(`{"name":"%s","description":"r%03d","type":"text"}`, name, r+1)})
		}
		out = append(out, row)
	}
	return out
}
func dialectBoundTop(t *testing.T, c dialectBoundCase) *DialectResearchIndex {
	t.Helper()
	index := &DialectResearchIndex{Revisions: make([]DialectIndexRevision, 0, c.Top.Revisions)}
	for r := 0; r < c.Top.Revisions; r++ {
		fields, size := c.Top.Fields, c.Top.Bytes
		index.Revisions = append(index.Revisions, DialectIndexRevision{Revision: dialectBoundLabel("escaped", r), Fields: &fields, Digest: dialectStringPointer("sha256:" + strings.Repeat("ab", 32)), Bytes: &size})
	}
	digest, err := dialectInventoryDigest(index.Revisions)
	if err != nil {
		t.Fatal(err)
	}
	index.Digest = digest
	return index
}

// TestDialectInventoryBounds is the #252 bound proof: every collection the Desk accepts (at most
// 1,000 records, so at most 1,000 original revisions; 30 fields of at most 64-byte names; revision
// labels at the 160-byte limit) gives a top index and revision field indexes under their caps, and is
// read, proposed and retained with every envelope and page within its bound. The issue's family,
// whose #250 one-level index exceeded the cap, is read the same way.
func TestDialectInventoryBounds(t *testing.T) {
	v := dialectPagingVectorFile(t)
	if len(v.Bounds) != 6 {
		t.Fatal("bound rows missing", len(v.Bounds))
	}
	for _, c := range v.Bounds {
		t.Run(c.Name, func(t *testing.T) {
			w := newDialectWorld(t)
			if c.Top != nil {
				index := dialectBoundTop(t, c)
				if canonical, _ := dialectCanonical(index); len(canonical) != c.IndexBytes {
					t.Fatal("generated top index differs from the vector's byte count", len(canonical))
				}
				w.ports.Read = func(context.Context, DialectReadRequest) (DialectSnapshot, error) {
					s, err := dialectClone(w.snapshot)
					s.ResearchIndex = index
					return s, err
				}
				w.ports.ReadFieldIndex = func(_ context.Context, request DialectFieldIndexRequest) (DialectFieldIndexPage, error) {
					w.fieldIndexes = append(w.fieldIndexes, request)
					return DialectFieldIndexPage{}, fmt.Errorf("the top index was accepted")
				}
				w.tools = NewDialectTools(w.ports)
				raw, _ := json.Marshal(w.fixture.Request)
				_, err := w.tools[0].Execute(context.Background(), raw)
				if c.Expect == "top-accepted" {
					if c.IndexBytes > DialectIndexBytes || err == nil || !strings.Contains(err.Error(), "the top index was accepted") || len(w.fieldIndexes) != 1 || w.fieldIndexes[0].Entry != 0 {
						t.Fatal("the largest top index the shape admits was refused", err)
					}
					return
				}
				if err == nil || !strings.Contains(err.Error(), c.Message) || len(w.fieldIndexes) != 0 {
					t.Fatalf("expected refusal %q, got %v", c.Message, err)
				}
				return
			}
			inventory := dialectBoundInventory(c)
			index, err := DialectResearchIndexOf(inventory)
			if err != nil {
				t.Fatal(err)
			}
			largest := 0
			for _, r := range index.Revisions {
				largest = max(largest, *r.Bytes)
			}
			if canonical, _ := dialectCanonical(index); len(canonical) != c.IndexBytes || c.IndexBytes > DialectIndexBytes || largest != c.FieldIndexBytes || largest > DialectPageBytes {
				t.Fatal("the bound proof's sizes differ", len(canonical), largest)
			}
			if c.OneLevelIndexBytes != 0 {
				// #250's one-level index of the same inventory, encoded by hand: it exceeded the cap.
				type oneLevel struct {
					Revision string              `json:"revision"`
					Fields   []DialectIndexField `json:"fields"`
				}
				rows := []oneLevel{}
				for _, r := range inventory {
					rows = append(rows, oneLevel{r.Revision, DialectFieldIndexOf(r).Fields})
				}
				revisions, _ := dialectCanonical(rows)
				canonical, _ := dialectCanonical(map[string]any{"digest": dialectHash(revisions), "revisions": rows})
				if len(canonical) != c.OneLevelIndexBytes || len(canonical) <= DialectIndexBytes {
					t.Fatal("the one-level index differs from the issue's size", len(canonical))
				}
			}
			// The host serves the precomputed index, field indexes and flattened declarations; every
			// page it serves is measured against its bound.
			w.declarations = inventory
			flat := []DialectDeclaration{}
			for _, r := range inventory {
				for _, f := range r.Fields {
					flat = append(flat, DialectDeclaration{Revision: r.Revision, Name: f.Name, FieldJSON: f.FieldJSON})
				}
			}
			w.ports.Read = func(context.Context, DialectReadRequest) (DialectSnapshot, error) {
				s, err := dialectClone(w.snapshot)
				s.ResearchIndex = index
				return s, err
			}
			w.ports.ReadFieldIndex = func(_ context.Context, request DialectFieldIndexRequest) (DialectFieldIndexPage, error) {
				page := DialectFieldIndexPage{ID: request.ID, PackKey: request.PackKey, Revision: request.Revision, InventoryDigest: request.InventoryDigest, Entry: request.Entry, FieldIndex: *DialectFieldIndexOf(inventory[request.Entry])}
				// json.Marshal escapes at least what canonical JSON does, so its length bounds the canonical length.
				if encoded, _ := json.Marshal(page); len(encoded) > DialectPageBytes {
					t.Fatal("a served field index page exceeds its bound", len(encoded))
				}
				return page, nil
			}
			w.ports.ReadDeclarations = func(_ context.Context, request DialectDeclarationPageRequest) (DialectDeclarationPage, error) {
				page := DialectDeclarationPage{ID: request.ID, PackKey: request.PackKey, Revision: request.Revision, InventoryDigest: request.InventoryDigest, Cursor: request.Cursor, Declarations: flat[request.Cursor:min(len(flat), request.Cursor+request.Limit)]}
				if encoded, _ := json.Marshal(page); len(encoded) > DialectPageBytes || len(page.Declarations) > request.Limit || request.Limit > DialectPageDeclarations {
					t.Fatal("a served page exceeds its bounds", len(encoded))
				}
				return page, nil
			}
			w.tools = NewDialectTools(w.ports)
			if _, err := w.propose(w.args(w.read())); err != nil || len(w.retained) != 1 {
				t.Fatal("an accepted collection was refused", err)
			}
			snapshot, _ := w.ports.Read(context.Background(), w.fixture.Request)
			for name, value := range map[string]any{"snapshot": snapshot, "candidate": w.drafts[0].Candidate, "draft": w.drafts[1], "proposal": w.retained[0]} {
				encoded, _ := json.Marshal(value)
				limit := map[string]int{"snapshot": DialectSnapshotBytes, "candidate": DialectCandidateBytes, "draft": DialectDraftBytes, "proposal": DialectRuntimeBytes}[name]
				if len(encoded) > limit {
					t.Fatal(name, "exceeds its bound", len(encoded))
				}
			}
			if !reflect.DeepEqual(w.retained[0].ResearchIndex, index) {
				t.Fatal("the proposal lost the top index")
			}
			read, err := ReadDialectDeclarations(context.Background(), w.ports.ReadFieldIndex, w.ports.ReadDeclarations, w.retained[0].ID, w.retained[0].PackKey, w.retained[0].Revision, index)
			if err != nil || !reflect.DeepEqual(read, inventory) {
				t.Fatal("the paged reader did not recover the complete original inventory", err)
			}
		})
	}
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
			served := row.FieldIndexPages
			if served == nil {
				served = []int{0, 1}
			}
			w.ports.ReadFieldIndex = func(_ context.Context, request DialectFieldIndexRequest) (DialectFieldIndexPage, error) {
				w.calls = append(w.calls, "field-index")
				w.fieldIndexes = append(w.fieldIndexes, request)
				for _, at := range served {
					if v.FieldIndexes[at].Entry == request.Entry {
						return dialectClone(v.FieldIndexes[at])
					}
				}
				return DialectFieldIndexPage{}, fmt.Errorf("no vector field index at entry %d", request.Entry)
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
				if len(w.fieldIndexes) < len(v.FieldIndexRequests) || !reflect.DeepEqual(w.fieldIndexes[:len(v.FieldIndexRequests)], v.FieldIndexRequests) {
					t.Fatal("field index requests differ from the vector", w.fieldIndexes)
				}
				read, err := ReadDialectDeclarations(context.Background(), w.ports.ReadFieldIndex, w.ports.ReadDeclarations, w.retained[0].ID, w.retained[0].PackKey, w.retained[0].Revision, w.retained[0].ResearchIndex)
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
	if index == nil || len(index.Revisions) != 150 || index.Revisions[149].Revision != "recipe-revision:150" || index.Revisions[0].Fields == nil || *index.Revisions[0].Fields != 30 {
		t.Fatal("the index lost revisions or field counts")
	}
	read, err := ReadDialectDeclarations(context.Background(), w.ports.ReadFieldIndex, w.ports.ReadDeclarations, w.retained[0].ID, w.retained[0].PackKey, w.retained[0].Revision, index)
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
		{"page echoing another cursor", "another collection revision or inventory"},
		{"page echoing another id", "another collection revision or inventory"},
		{"page echoing another packKey", "another collection revision or inventory"},
		{"malformed declaration on a page", "actual original field declaration"},
		{"missing field index port", "requires the revision field index read port"},
		{"unavailable entry with a digest", "index is malformed"},
		{"field index page over its byte bound", "field index page exceeds its byte limit"},
		{"field index page from another inventory", "field index page is bound to another collection revision or inventory"},
		{"field index page echoing another entry", "field index page is bound to another collection revision or inventory"},
		{"field index differs from its top entry", "field index does not match its indexed digest"},
		{"field index with another field count", "revision field index is malformed"},
		{"field index for another revision", "revision field index is malformed"},
		{"empty revision with another field index digest", "field index does not match its indexed digest"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newDialectWorld(t)
			w.declarations = inventory
			read, fieldIndexes, pages := w.ports.Read, w.ports.ReadFieldIndex, w.ports.ReadDeclarations
			w.ports.Read = func(ctx context.Context, r DialectReadRequest) (DialectSnapshot, error) {
				s, err := read(ctx, r)
				top := s.ResearchIndex.Revisions
				switch c.name {
				case "index digest differs from its entries":
					s.ResearchIndex.Digest = dialectHash("another inventory")
				case "malformed index entry":
					top[0].Digest = dialectStringPointer("sha256:short")
				case "unavailable entry with a digest":
					top[0].Fields = nil
				case "field index with another field count":
					// The host's top entry and its digest agree, but the count is not the field index's.
					count := *top[0].Fields + 1
					top[0].Fields = &count
				case "empty revision with another field index digest":
					top[len(top)-1].Digest = dialectStringPointer(dialectHash("another field index"))
				case "field index for another revision":
					other := *DialectFieldIndexOf(w.declarations[0])
					other.Revision = "another revision"
					canonical, _ := dialectCanonical(other)
					size := len(canonical)
					top[0].Digest, top[0].Bytes = dialectStringPointer(dialectHash(canonical)), &size
				}
				if c.name != "index digest differs from its entries" && c.name != "malformed index entry" {
					s.ResearchIndex.Digest, _ = dialectInventoryDigest(top)
				}
				return s, err
			}
			w.ports.ReadFieldIndex = func(ctx context.Context, r DialectFieldIndexRequest) (DialectFieldIndexPage, error) {
				p, err := fieldIndexes(ctx, r)
				switch c.name {
				case "field index page over its byte bound":
					p.FieldIndex.Fields[0].Name += strings.Repeat(" ", DialectPageBytes)
				case "field index page from another inventory":
					p.InventoryDigest = dialectHash("another inventory")
				case "field index page echoing another entry":
					p.Entry++
				case "field index differs from its top entry":
					p.FieldIndex.Fields[0].Digest = dialectHash("another declaration")
				case "field index for another revision":
					p.FieldIndex.Revision = "another revision"
				}
				return p, err
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
				case "page echoing another cursor":
					p.Cursor++
				case "page echoing another id":
					p.ID = "collection:opaque/other"
				case "page echoing another packKey":
					p.PackKey = "declared:other"
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
			if c.name == "missing field index port" {
				w.ports.ReadFieldIndex = nil
			}
			if c.name == "empty revision with another field index digest" {
				w.declarations = append(w.declarations, DialectResearchRevision{Revision: "original:empty", Fields: []DialectResearchField{}})
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
