package certification

import (
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/turn"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
)

// StateHeader heads the execution state the host puts on a request (STATE_HEADER in
// packages/harness/engines/contract.ts; runcontext.StateHeader).
const StateHeader = runcontext.StateHeader

// RefutationMarker is the literal a critic's request carries (REFUTATION_MARKER in refutation.ts).
const RefutationMarker = turn.RefutationMarker

// RecallQuery is what the recall rule found: the request text and the JSON pointer to it.
type RecallQuery = runcontext.RecallQuery

// Recall applies the recall rule (agent#11, budget.json#/$defs/recallQuery): the one
// implementation is go/context's (runcontext.Recall), held to the recall vectors there and here.
func Recall(api string, body []byte) *RecallQuery { return runcontext.Recall(api, body) }
