package runcontext

// The prompt is the adapter's (ADR-0005), the port of prompt.test.ts's units: the host's parts held
// to the contract's vector, placed in layer order, asked once and held to their bounds; and the
// prompt's digest held to its vector and to the checkpoint's reader.

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
)

func TestTheHostPartsAreTheContractVector(t *testing.T) {
	items := vectorItems(t, "budget/host-parts.json")
	if len(items) != len(HostPartsTable) {
		t.Fatalf("the vector has %d parts, the table %d", len(items), len(HostPartsTable))
	}
	for at, item := range items {
		part := HostPartsTable[at]
		if item.Get("name").Str() != part.Name || int(item.Get("layer").Num()) != part.Layer || item.Get("injection").Str() != part.Injection ||
			item.Get("header").Str() != part.Header || item.Get("end").Str() != part.End || item.Get("bound").Str() != part.Bound {
			t.Errorf("part %d: the table holds %+v and the vector %s", at, part, item.Text())
		}
		if part.bound(Defaults) <= 0 {
			t.Errorf("%s has no bound in the defaults", part.Name)
		}
	}
	if Defaults.ProjectInstructionsBytes != 4000 {
		t.Errorf("the project instructions' bound is %d", Defaults.ProjectInstructionsBytes)
	}
}

func TestTheStateHeaderIsTheOneTheWireUses(t *testing.T) {
	for at, item := range vectorItems(t, "budget/state-placement.json") {
		if item.Get("header").Str() != StateHeader {
			t.Errorf("item %d's header is not the one this package holds", at)
		}
	}
}

func TestTheSystemTextPutsTheHostsPartsAfterTheGuidanceInLayerOrder(t *testing.T) {
	if held := SystemText("GUIDANCE", HostParts{}, "\n\nTOOLS"); held != "GUIDANCE\n\nTOOLS" {
		t.Errorf("with no parts the system text is %q", held)
	}
	parts := HostParts{Memory: "Recalled.", Brief: "The brief.", ProjectInstructions: "Be brief."}
	want := "GUIDANCE\n\n" + BriefHeader + "\nThe brief.\nEND PROJECT BRIEF\n\n" + ProjectInstructionsHeader + "\nBe brief.\nEND PROJECT INSTRUCTIONS\n\n" +
		MemoryHeader + "\nRecalled.\nEND PROJECT MEMORY\n\nTOOLS"
	if held := SystemText("GUIDANCE", parts, "\n\nTOOLS"); held != want {
		t.Errorf("the system text is %q", held)
	}
	if held := SystemText("GUIDANCE", HostParts{Memory: "Recalled.", Brief: "The brief."}, ""); held != "GUIDANCE\n\n"+BriefHeader+"\nThe brief.\nEND PROJECT BRIEF\n\n"+MemoryHeader+"\nRecalled.\nEND PROJECT MEMORY" {
		t.Errorf("a part left out left %q", held)
	}
}

func TestTheHostsPartsAreAskedOnceAndHeldToTheirBounds(t *testing.T) {
	ctx := context.Background()
	var asked []string
	briefs := 0
	host := HostFuncs{
		BriefFunc:     func(context.Context) (string, error) { briefs++; return "The brief.", nil },
		MemoryForFunc: func(_ context.Context, query string) (string, error) { asked = append(asked, query); return "", nil },
	}
	parts, err := ReadHostParts(ctx, host, "the person's message", Defaults, true)
	if err != nil || parts != (HostParts{Brief: "The brief."}) || briefs != 1 || len(asked) != 1 || asked[0] != "the person's message" {
		t.Fatalf("parts %+v, err %v, briefs %d, asked %v", parts, err, briefs, asked)
	}
	// Memory off: not asked.
	asked = nil
	if _, err := ReadHostParts(ctx, host, "q", Defaults, false); err != nil || len(asked) != 0 {
		t.Errorf("memory off asked %v (%v)", asked, err)
	}
	// Each part at its bound is taken, and one byte past it refused before any request.
	answer := func(bytes int) string { return strings.Repeat("é", bytes/2) + strings.Repeat("x", bytes%2) }
	for _, part := range HostPartsTable {
		bound := part.bound(Defaults)
		for _, size := range []int{bound, bound + 1} {
			overhead := 0
			if part.Name == "memory" {
				overhead = 245
			} // compatibility metadata, before framing
			text := answer(size - overhead)
			held := HostFuncs{}
			switch part.Name {
			case "brief":
				held.BriefFunc = func(context.Context) (string, error) { return text, nil }
			case "projectInstructions":
				held.ProjectInstructionsFunc = func(context.Context) (string, error) { return text, nil }
			default:
				held.MemoryForFunc = func(context.Context, string) (string, error) { return text, nil }
			}
			parts, err := ReadHostParts(ctx, held, "q", Defaults, true)
			expected := text
			if part.Name == "memory" {
				expected = FrameBlock("memory", "kind: fact\nsource: {\"proposedBy\":\"host\",\"run\":\"host\"}\nid: \"legacy-memory\"\n"+text, "")
			}
			if size == bound && (err != nil || part.text(parts) != expected) {
				t.Errorf("%s at its bound: %v", part.Name, err)
			}
			if size > bound && (err == nil || !strings.Contains(err.Error(), "over "+part.Bound+" ("+strconv.Itoa(bound)+")")) {
				t.Errorf("%s past its bound: %v", part.Name, err)
			}
		}
	}
	// A host's own failure fails the run.
	failing := HostFuncs{ProjectInstructionsFunc: func(context.Context) (string, error) { return "", errors.New("unreadable") }}
	if _, err := ReadHostParts(ctx, failing, "q", Defaults, true); err == nil {
		t.Error("a host that failed was not refused")
	}
}

func TestTheRunAsksTheHostOnceWithThePersonsMessage(t *testing.T) {
	var asked []string
	session := modelSession()
	session.Context = &Input{CurrentRequest: &CurrentRequest{Message: "Check the pack.", Input: "the pack"}}
	session.Host = HostFuncs{MemoryForFunc: func(_ context.Context, query string) (string, error) {
		asked = append(asked, query)
		return "Recalled.", nil
	}}
	run, err := ContextualSession(context.Background(), session)
	if err != nil {
		t.Fatal(err)
	}
	defer run.Context.Close()
	if len(asked) != 1 || asked[0] != "Check the pack." || run.Context.HostParts.Memory != FrameBlock("memory", "kind: fact\nsource: {\"proposedBy\":\"host\",\"run\":\"host\"}\nid: \"legacy-memory\"\nRecalled.", run.Context.Marker) {
		t.Errorf("asked %v, parts %+v", asked, run.Context.HostParts)
	}
	if held := run.Context.System("GUIDANCE", ""); held != "GUIDANCE\n\n"+MemoryHeader+"\n"+FrameBlock("memory", "kind: fact\nsource: {\"proposedBy\":\"host\",\"run\":\"host\"}\nid: \"legacy-memory\"\nRecalled.", run.Context.Marker)+"\nEND PROJECT MEMORY" {
		t.Errorf("the system text is %q", held)
	}
	// A delegated child asks with its objective; memory off asks nothing.
	asked = nil
	child := session
	child.Context = &Input{Objective: "Check the threshold."}
	if run, err := ContextualSession(context.Background(), child); err != nil {
		t.Fatal(err)
	} else {
		run.Context.Close()
	}
	off := false
	quiet := session
	quiet.Context = &Input{Memory: &off}
	if run, err := ContextualSession(context.Background(), quiet); err != nil {
		t.Fatal(err)
	} else {
		run.Context.Close()
	}
	if len(asked) != 1 || asked[0] != "Check the threshold." {
		t.Errorf("asked %v", asked)
	}
	// An empty objective is no objective, as in TypeScript: memory is asked with the person's message.
	asked = nil
	empty := session
	empty.Context = &Input{Objective: "", CurrentRequest: &CurrentRequest{Message: "Check the pack.", Input: ""}}
	if run, err := ContextualSession(context.Background(), empty); err != nil {
		t.Fatal(err)
	} else {
		run.Context.Close()
	}
	if len(asked) != 1 || asked[0] != "Check the pack." {
		t.Errorf("with an empty objective, asked %v", asked)
	}
}

func TestThePromptDigestIsTheContractVector(t *testing.T) {
	vector := readJSON(t, "contract/vectors/provenance/prompt-digest.json")
	bodies, items := vector.Get("bodies").Items(), vector.Get("items").Items()
	if len(bodies) != len(items) || len(items) == 0 {
		t.Fatalf("%d bodies for %d items", len(bodies), len(items))
	}
	c := openContext(t, modelSession())
	if err := c.Execution.Initialize(); err != nil {
		t.Fatal(err)
	}
	for _, body := range bodies {
		c.Execution.RecordPrompt([]byte(body.Str()))
	}
	for at, item := range items {
		held := c.Execution.State().Prompts[at]
		if held.Request != int64(item.Get("request").Num()) || held.Sha256 != item.Get("sha256").Str() {
			t.Errorf("item %d: recorded %+v", at, held)
		}
	}
	// The checkpoint carries them and its reader reads them back; one out of order is refused.
	state, err := c.Snapshot("")
	if err != nil || len(state.Prompts) != len(items) {
		t.Fatalf("the checkpoint carries %d prompts (%v)", len(state.Prompts), err)
	}
	value := c.Execution.State().Value()
	if _, err := ReadExecution(value); err != nil {
		t.Errorf("the reader refused the checkpoint: %v", err)
	}
	value.Get("prompts").Items()[1].Set("request", Number(1))
	if _, err := ReadExecution(value); err == nil {
		t.Error("the reader took prompts out of order")
	}
	c.Close()
}
