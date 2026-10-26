package gate

import (
	"encoding/json"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"strings"
	"testing"
)

func TestTaxonomyDirectOfferCannotBypassDeclarations(t *testing.T) {
	events := []contract.RunEvent{}
	g := New(Options{Granted: []string{"get_schema", "experimental_evaluate"}, Emit: func(e contract.RunEvent) { events = append(events, e) }})
	g.SetOffer(contract.ToolOffer{Tools: []contract.ToolOfferRuntimeTool{{Name: "get_schema"}, {Name: "experimental_evaluate", Meta: contract.ToolTaxonomy{Operation: "act", Effect: "act"}}}, HostTools: []contract.ToolOfferHostTool{{Name: strings.Repeat("x", 2048)}}})
	if len(g.Offer().Tools) != 0 || len(g.Offer().HostTools) != 0 || len(events) != 3 {
		t.Fatal("missing or contradictory taxonomy bypassed direct gate")
	}
	for _, event := range events {
		raw, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		var read contract.RunEvent
		if err = json.Unmarshal(raw, &read); err != nil {
			t.Fatal("malformed refusal event", err)
		}
	}
	if r := g.AdmitHost(strings.Repeat("x", 2048)); r == nil {
		t.Fatal("invalid host offer remained callable")
	}
}
