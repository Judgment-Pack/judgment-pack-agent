package adapter

import (
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"testing"
)

func TestRouteOffRuntimeToolsPreservesNilAndEmpty(t *testing.T) {
	nilSession := &RequestSession{offered: func(string) bool { return true }}
	if nilSession.RuntimeTools() != nil {
		t.Fatal("omitted runtime tools became an empty offer")
	}
	emptySession := &RequestSession{tools: []contract.ToolOfferRuntimeTool{}, offered: func(string) bool { return true }}
	if emptySession.RuntimeTools() == nil {
		t.Fatal("explicit empty runtime offer was omitted")
	}
}
