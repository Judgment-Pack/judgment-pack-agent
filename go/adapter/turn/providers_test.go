package turn

import (
	"encoding/json"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"os"
	"reflect"
	"testing"
)

func TestProviderOnCritique(t *testing.T) {
	raw, err := os.ReadFile("../../../contract/vectors/providers/critique.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct{ Items []contract.RunEvent }
	if err = json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	got := CritiqueEvent(Critique{Text: "No runtime checks."}, &contract.ProvenanceProvider{Connection: "reviewer", Family: "anthropic"})
	if !reflect.DeepEqual(got, v.Items[0]) {
		t.Fatalf("provider omitted: %+v", got.Critique)
	}
}
