package turn

// The question a model asks the person. The TypeScript has no unit test file of its own for
// questions.ts: the cases ported here are the ones its engines' suites pin
// (engines/codex/codex.test.ts, "corrects a malformed question through the tool result before
// pausing", and engines/vercel/engine.test.ts, the same for each family and "opens a question with
// omitted optional presentation fields"), over the Desk's own interview fixture
// (contract/vectors/question/interview.json).

import (
	"encoding/json"
	"strings"
	"testing"
)

// interview is the vector's question, as a value this test can vary.
func interview(t *testing.T) *jvalue {
	t.Helper()
	return value(vector(t, "question/interview.json").Items[0]).clone()
}

// codex.test.ts: a title over forty characters is the failure named title, and the model is told
// to correct it and call ask_user again in this turn.
func TestMalformedQuestionNamesItsField(t *testing.T) {
	question := interview(t)
	question.set("title", jstr(strings.Repeat("x", 41)))
	_, err := ReadModelQuestion(raw(question))
	held, ok := err.(*QuestionFormatError)
	if !ok || held.Code != QuestionTitle {
		t.Fatalf("a title over the bound is the title failure: %v", err)
	}
	result := QuestionFailureResult(err)
	if !result.IsError {
		t.Error("a refused question is a failed tool result")
	}
	for _, want := range []string{"40 characters", "call ask_user again"} {
		if !strings.Contains(result.Text, want) {
			t.Errorf("the model is told %q: %s", want, result.Text)
		}
	}
	same(t, "what the host persists", result.Structured, json.RawMessage(`{"questionFailure":"title"}`))

	// engine.test.ts: two choices with the same id is the failure named options.
	repeated := interview(t)
	first := repeated.get("options").a[0]
	repeated.set("options", &jvalue{kind: jarray, a: []*jvalue{first, first}})
	_, err = ReadModelQuestion(raw(repeated))
	if held, ok := err.(*QuestionFormatError); !ok || held.Code != QuestionOptions {
		t.Fatalf("repeated choices are the options failure: %v", err)
	}
	same(t, "what the host persists", QuestionFailureResult(err).Structured, json.RawMessage(`{"questionFailure":"options"}`))

	// Anything that is not the reader's refusal is unavailable, and carries no correction.
	result = QuestionFailureResult(errContext)
	same(t, "a chat that could not accept the question", result.Structured, json.RawMessage(`{"questionFailure":"unavailable"}`))
	text(t, "what the model is told", result.Text, QuestionFailureMessage(QuestionUnavailable))
}

// engine.test.ts: "opens a question with omitted optional presentation fields" — the model
// boundary normalizes an absent, null or blank recommendation and an option with no description;
// the saved history's reader stays strict.
func TestModelQuestionNormalizesPresentation(t *testing.T) {
	question := interview(t)
	question.set("recommendedOptionId", jstr("  "))
	question.set("recommendation", &jvalue{kind: jnull})
	options := question.get("options")
	for _, option := range options.a {
		option.set("description", &jvalue{kind: jnull})
	}
	read, err := ReadModelQuestion(raw(question))
	if err != nil {
		t.Fatalf("the model boundary normalizes it: %v", err)
	}
	if !read.RecommendedOptionID.Null || !read.Recommendation.Null {
		t.Error("a blank recommendation is null")
	}
	for _, option := range read.Options {
		if option.Description != "" {
			t.Errorf("an option with no description has an empty one: %q", option.Description)
		}
	}
	// The strict reader refuses the same question: the saved history is not normalized.
	if _, err := ReadUserQuestion(raw(question)); err == nil {
		t.Error("the saved history's reader stays strict")
	}
}

// The bounds and the closed shape the reader holds a question to.
func TestQuestionReaderBounds(t *testing.T) {
	for _, held := range []struct {
		why    string
		change func(*jvalue)
		code   QuestionFailureCode
	}{
		{"a member the shape does not name", func(v *jvalue) { v.set("urgent", jbln(true)) }, QuestionShape},
		{"a blank title", func(v *jvalue) { v.set("title", jstr(" ")) }, QuestionTitle},
		{"a question over 600 characters", func(v *jvalue) { v.set("question", jstr(strings.Repeat("y", 601))) }, QuestionText},
		{"a selection that is neither", func(v *jvalue) { v.set("selection", jstr("one")) }, QuestionSelection},
		{"one choice", func(v *jvalue) { v.set("options", &jvalue{kind: jarray, a: v.get("options").a[:1]}) }, QuestionOptions},
		{"five choices", func(v *jvalue) {
			options := v.get("options")
			extra := []*jvalue{}
			for at := 0; at < 5; at++ {
				held := options.a[0].clone()
				held.set("id", jstr("o"+jsNumber(float64(at))))
				held.set("label", jstr("Label "+jsNumber(float64(at))))
				extra = append(extra, held)
			}
			v.set("options", &jvalue{kind: jarray, a: extra})
		}, QuestionOptions},
		{"two choices whose labels differ only by case", func(v *jvalue) {
			options := v.get("options")
			second := options.a[1].clone()
			second.set("label", jstr(strings.ToUpper(options.a[0].get("label").s)))
			v.set("options", &jvalue{kind: jarray, a: []*jvalue{options.a[0], second}})
		}, QuestionOptions},
		{"an id that is not one", func(v *jvalue) { v.get("options").a[0].set("id", jstr("not an id")) }, QuestionOptions},
		{"a recommendation naming no offered choice", func(v *jvalue) { v.set("recommendedOptionId", jstr("nobody")) }, QuestionRecommendation},
		{"a recommended choice with no reason", func(v *jvalue) { v.set("recommendation", &jvalue{kind: jnull}) }, QuestionRecommendation},
		{"a reason with no choice", func(v *jvalue) { v.set("recommendedOptionId", &jvalue{kind: jnull}) }, QuestionRecommendation},
	} {
		question := interview(t)
		held.change(question)
		_, err := ReadModelQuestion(raw(question))
		got, ok := err.(*QuestionFormatError)
		if !ok || got.Code != held.code {
			t.Errorf("%s: want the %s failure, got %v", held.why, held.code, err)
		}
	}
	// No options at all is an open text question.
	question := interview(t)
	question.set("options", &jvalue{kind: jarray, a: []*jvalue{}})
	question.set("recommendedOptionId", &jvalue{kind: jnull})
	question.set("recommendation", &jvalue{kind: jnull})
	if _, err := ReadModelQuestion(raw(question)); err != nil {
		t.Errorf("no options is an open text question: %v", err)
	}
}

// errContext is a failure that is not the reader's: the chat could not accept the question.
var errContext = errorString("the chat could not accept the question")
