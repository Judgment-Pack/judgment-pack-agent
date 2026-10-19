package failure

import (
	"slices"
	"strings"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

// CompletionReasons are the provider-neutral finish reasons (COMPLETION_REASONS in completion.ts).
var CompletionReasons = []string{"stop", "length", "content-filter", "tool-calls", "error", "other"}

// ReadCompletion is readCompletion: the completion where its reason is one of the six and every
// count it names is a non-negative integer.
func ReadCompletion(value contract.ProvenanceCompletion) (contract.ProvenanceCompletion, bool) {
	if !slices.Contains(CompletionReasons, value.FinishReason) {
		return contract.ProvenanceCompletion{}, false
	}
	for _, count := range []*int64{value.InputTokens, value.OutputTokens, value.ReasoningTokens, value.CacheReadTokens, value.CacheWriteTokens} {
		if count != nil && (*count < 0 || *count > 9007199254740991) {
			return contract.ProvenanceCompletion{}, false
		}
	}
	return contract.ProvenanceCompletion{FinishReason: value.FinishReason, InputTokens: value.InputTokens, OutputTokens: value.OutputTokens,
		ReasoningTokens: value.ReasoningTokens, CacheReadTokens: value.CacheReadTokens, CacheWriteTokens: value.CacheWriteTokens}, true
}

// Usage is what a provider said one answer cost, as the wire read it: each count where the
// provider returned it, nil where it did not.
type Usage struct {
	InputTokens      *int64
	OutputTokens     *int64
	ReasoningTokens  *int64
	CacheReadTokens  *int64
	CacheWriteTokens *int64
}

// ModelCompletion is modelCompletion: the completion one answer reported, a reason outside the
// six read as other, and only the counts the provider returned.
func ModelCompletion(finishReason string, usage Usage) contract.ProvenanceCompletion {
	if !slices.Contains(CompletionReasons, finishReason) {
		finishReason = "other"
	}
	keep := func(v *int64) *int64 {
		if v == nil || *v < 0 {
			return nil
		}
		held := *v
		return &held
	}
	return contract.ProvenanceCompletion{FinishReason: finishReason, InputTokens: keep(usage.InputTokens), OutputTokens: keep(usage.OutputTokens),
		ReasoningTokens: keep(usage.ReasoningTokens), CacheReadTokens: keep(usage.CacheReadTokens), CacheWriteTokens: keep(usage.CacheWriteTokens)}
}

// CanFinishResponse is canFinishResponse: one new, tool-free synthesis request, never a retry of
// tools or of an uncertain transport, where the answer was cut at its length or stopped with no
// text.
func CanFinishResponse(completion *contract.ProvenanceCompletion, text string, toolCalls, remaining int, attempted bool) bool {
	if attempted || remaining <= 0 || toolCalls != 0 || completion == nil {
		return false
	}
	return completion.FinishReason == "length" || completion.FinishReason == "stop" && strings.TrimSpace(text) == ""
}

// CompletionFailureCode is completionFailureCode: the class an unfinished answer earns.
func CompletionFailureCode(completion *contract.ProvenanceCompletion) string {
	if completion != nil {
		switch completion.FinishReason {
		case "length":
			return "output-limit"
		case "content-filter":
			return "content-filter"
		}
	}
	return "invalid-response"
}

// FinishResponseInstructions is FINISH_RESPONSE_INSTRUCTIONS: the one tool-free request that
// finishes a response cut short.
const FinishResponseInstructions = "The previous model response ended without a complete answer. Complete the current response using only the supplied request and completed results. Tools are unavailable. Do not restart discovery, repeat actions, or claim an unsaved change or an unperformed check succeeded. Keep the answer concise. If a complete supported proposal is not possible, explain the verified findings and the exact remaining work. Return a complete answer, not a continuation of a partial JSON block."
