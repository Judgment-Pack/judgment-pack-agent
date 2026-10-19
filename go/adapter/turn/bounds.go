package turn

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
)

// WithDeadline is withDeadline: one call bounded by a deadline as well as by the run.
//
// The run's context ending is the run unwinding, reported as failure.Cancelled; the deadline
// passing is the call's own failure, late(), which a tool dispatch reports to the model as a
// failed result rather than ending the run. The work is handed the bounded context and is
// expected to honour it: Go cannot abandon a call that ignores its context, where the TypeScript
// settles the await and leaves the promise running.
func WithDeadline[T any](ctx context.Context, limit time.Duration, late func() error, work func(context.Context) (T, error)) (T, error) {
	var zero T
	// A thunk, so the work is not even started on a closed run.
	if ctx.Err() != nil {
		return zero, failure.Cancelled
	}
	bounded, stop := context.WithTimeout(ctx, limit)
	defer stop()
	held, err := work(bounded)
	if err != nil {
		if errors.Is(bounded.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			return zero, late()
		}
		if bounded.Err() != nil {
			return zero, failure.Cancelled
		}
		return zero, err
	}
	// Read the run again, after the work: a run that closed while the work ran is owed nothing,
	// whatever the work then answered.
	if ctx.Err() != nil {
		return zero, failure.Cancelled
	}
	return held, nil
}

// LimitText is limitText: a time limit as a person reads it, in the largest unit that states it
// whole. The limit is read in milliseconds, as the TypeScript states it.
func LimitText(limit time.Duration) string {
	ms := limit.Milliseconds()
	count := func(n int64, unit string) string {
		text := jsNumber(float64(n)) + " " + unit
		if n != 1 {
			text += "s"
		}
		return text
	}
	if ms >= 60_000 && ms%60_000 == 0 {
		return count(ms/60_000, "minute")
	}
	if ms >= 1_000 && ms%1_000 == 0 {
		return count(ms/1_000, "second")
	}
	return count(ms, "millisecond")
}

// ToolTimeout is toolTimeout: the failure a tool call past its deadline earns. The model reads it
// and decides.
func ToolTimeout(limit time.Duration) error {
	detail := "The tool did not answer within " + LimitText(limit) +
		" and is no longer waited for; it may still complete. Its outcome is unknown; check it before repeating the call."
	held := contract.Failure{Version: 1, Code: "timeout", Source: "tool", Outcome: "unknown", Detail: &detail}
	return failure.NewError(held)
}

// UnofferedRefusal is unofferedRefusal: what the model is told of a call to a tool it was not
// offered, whoever refused it. The sentence is the host gate's (gate.UnofferedRefusal), reused
// rather than written twice.
func UnofferedRefusal(refusal string) string { return gate.UnofferedRefusal(refusal) }

// RefusedUnoffered is refusedUnoffered: a call to a tool this phase never offered, put through the
// dispatch as the model made it, so the layer that holds is the one that reports, and answered
// refused whatever it said (agent#4). A cancelled run stops here rather than reporting a refusal
// that never happened.
//
// It is for a dispatch that reports a refusal as an error, as the TypeScript session's callTool
// does. gate.Gate.CallTool answers UnofferedRefusal itself, so an adapter on the Go gate passes
// that Result's text through instead of calling this.
func RefusedUnoffered(ctx context.Context, call func(context.Context, string, json.RawMessage) error, name string, args json.RawMessage) (string, error) {
	refusal := "the call did not leave the page and nothing was written"
	if err := call(ctx, name, args); err != nil {
		if failure.IsCancelled(err) {
			return "", err
		}
		refusal = err.Error()
	}
	return UnofferedRefusal(refusal), nil
}

// TextOf is textOf: the text half of one tool answer, joined in the runtime's own order.
func TextOf(result contract.ToolResultMCPToolResult) string {
	text := ""
	first := true
	for _, block := range result.Content {
		if block.Option1 == nil || block.Option1.Type == nil || *block.Option1.Type != "text" {
			continue
		}
		if !first {
			text += "\n"
		}
		first = false
		if block.Option1.Text != nil {
			text += *block.Option1.Text
		}
	}
	return text
}
