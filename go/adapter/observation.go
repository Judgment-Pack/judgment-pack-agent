package adapter

import (
	"encoding/json"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

// ObservationOptions declares a fixed schema-only offer. No executor or PreparedRequest crosses it.
type ObservationOptions struct{ Tools []contract.ToolOfferHostTool }
type ObservedToolCall struct {
	ID, Name  string
	Arguments json.RawMessage
}
type ObservationResult struct {
	Text       string
	Calls      []ObservedToolCall
	Completion contract.ProvenanceCompletion
	Checkpoint contract.Checkpoint
	Receipt    runcontext.RunReceipt
}

const (
	ObservationMaxTools     = 16
	ObservationMaxCalls     = 16
	ObservationMaxArguments = 16 * 1024
	ObservationMaxText      = 64 * 1024
	ObservationMaxBytes     = 256 * 1024
)
