package turn

// The question a model asks the person (questions.ts) and the failures a malformed one earns
// (questionFailure.ts). The question ends the model turn and waits for a real answer; a question
// the reader refuses is a failed tool result naming the field to correct, never an error event.

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
)

// UserQuestion is UserQuestion: one question with its choices, as question.json states it
// (contract.Question).
type UserQuestion = contract.Question

// QuestionFailureCode is QuestionFailureCode: the Desk-owned reasons a question was not asked.
// Only these are persisted, never a raw callback error or the arguments.
type QuestionFailureCode string

// The reasons a question was not asked.
const (
	QuestionShape          QuestionFailureCode = "shape"
	QuestionTitle          QuestionFailureCode = "title"
	QuestionText           QuestionFailureCode = "question"
	QuestionSelection      QuestionFailureCode = "selection"
	QuestionOptions        QuestionFailureCode = "options"
	QuestionRecommendation QuestionFailureCode = "recommendation"
	QuestionUnavailable    QuestionFailureCode = "unavailable"
)

// QuestionFailureMessage is questionFailureMessage: the sentence the model reads for a code, or ""
// for a code the table does not name (the TypeScript's undefined). The texts pass through the
// Desk's sourceMessage, which carries no values here and is the identity on them.
func QuestionFailureMessage(code QuestionFailureCode) string {
	switch code {
	case QuestionShape:
		return "The question format was invalid. Use only title, question, selection, options, recommendedOptionId and recommendation."
	case QuestionTitle:
		return "Use a question title between 1 and 40 characters."
	case QuestionText:
		return "Use one question between 1 and 600 characters."
	case QuestionSelection:
		return "Set selection to single or multiple."
	case QuestionOptions:
		return "Use two to four distinct choices, or none for free text. Each choice needs a unique ID (letters, numbers, hyphens or underscores), a label up to 100 characters, and an optional description up to 240 characters."
	case QuestionRecommendation:
		return "A recommendation needs an existing choice ID and a reason up to 240 characters. Otherwise leave both recommendation fields empty."
	case QuestionUnavailable:
		return "This chat could not accept the question. You can answer in the message field."
	}
	return ""
}

// QuestionFormatError is QuestionFormatError: a question the reader refused, by the field to
// correct.
type QuestionFormatError struct{ Code QuestionFailureCode }

func (e *QuestionFormatError) Error() string { return QuestionFailureMessage(e.Code) }

// QuestionFailureResult is questionFailureResult: the failed tool result a refused question
// earns. A reader's refusal names the field and asks for the call again in the same turn; anything
// else is unavailable. The result is always an error result.
func QuestionFailureResult(cause error) gate.Result {
	code, correction := QuestionUnavailable, ""
	if held, ok := cause.(*QuestionFormatError); ok {
		code = held.Code
		correction = " Correct these arguments and call ask_user again in this turn. Do not claim the interface is unavailable, choose an answer, or perform unrelated work."
	}
	structured, _ := json.Marshal(contract.QuestionFormatFailure{QuestionFailure: string(code)})
	return gate.Result{Text: QuestionFailureMessage(code) + correction, IsError: true, Structured: structured}
}

var questionID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,100}$`)

func questionIdentifier(v *jvalue) bool {
	return v != nil && v.kind == jstring && questionID.MatchString(v.s)
}

// exactMembers is exact(v, names): every member the value carries is one of names. A member the
// list names may still be absent, and the checks that follow report it.
func exactMembers(v *jvalue, names []string) bool {
	for _, member := range v.keys() {
		if !contains(names, member) {
			return false
		}
	}
	return true
}

var questionMembers = []string{"title", "question", "selection", "options", "recommendedOptionId", "recommendation"}

// ReadUserQuestion is readUserQuestion: a question read closed, or a *QuestionFormatError naming
// the field to correct. The saved history's reader; the model boundary's is ReadModelQuestion.
func ReadUserQuestion(question json.RawMessage) (UserQuestion, error) {
	return readQuestion(value(question))
}

func readQuestion(v *jvalue) (UserQuestion, error) {
	refuse := func(code QuestionFailureCode) (UserQuestion, error) {
		return UserQuestion{}, &QuestionFormatError{Code: code}
	}
	if !v.isObject() || !exactMembers(v, questionMembers) {
		return refuse(QuestionShape)
	}
	if !boundedText(v.get("title"), 40, false) {
		return refuse(QuestionTitle)
	}
	if !boundedText(v.get("question"), 600, false) {
		return refuse(QuestionText)
	}
	selection := jsString(v.get("selection"))
	if selection != "single" && selection != "multiple" {
		return refuse(QuestionSelection)
	}
	options := v.get("options")
	if !options.isArray() || len(options.a) == 1 || len(options.a) > 4 {
		return refuse(QuestionOptions)
	}
	ids, labels := map[string]bool{}, map[string]bool{}
	read := make([]contract.QuestionOption, 0, len(options.a))
	for _, option := range options.a {
		if !option.isObject() || !exactMembers(option, []string{"id", "label", "description"}) ||
			!questionIdentifier(option.get("id")) || !boundedText(option.get("label"), 100, false) ||
			!boundedText(option.get("description"), 240, true) {
			return refuse(QuestionOptions)
		}
		ids[option.get("id").s] = true
		// toLocaleLowerCase with no locale; Go's ToLower is the locale-independent mapping, which
		// differs only for a locale with its own casing rules (Turkish dotted I).
		labels[strings.ToLower(strings.Trim(option.get("label").s, jsSpace))] = true
		read = append(read, contract.QuestionOption{ID: option.get("id").s, Label: option.get("label").s, Description: option.get("description").s})
	}
	if len(ids) != len(options.a) || len(labels) != len(options.a) {
		return refuse(QuestionOptions)
	}
	recommended, recommendation := v.get("recommendedOptionId"), v.get("recommendation")
	recommendedNull := recommended != nil && recommended.kind == jnull
	recommendationNull := recommendation != nil && recommendation.kind == jnull
	if !recommendedNull {
		if !questionIdentifier(recommended) || !ids[recommended.s] {
			return refuse(QuestionRecommendation)
		}
	}
	if !recommendationNull && !boundedText(recommendation, 240, false) {
		return refuse(QuestionRecommendation)
	}
	if recommendedNull != recommendationNull {
		return refuse(QuestionRecommendation)
	}
	out := UserQuestion{Title: v.get("title").s, Question: v.get("question").s, Selection: selection, Options: read,
		RecommendedOptionID: contract.Nullable[string]{Set: true, Null: recommendedNull},
		Recommendation:      contract.Nullable[string]{Set: true, Null: recommendationNull}}
	if !recommendedNull {
		out.RecommendedOptionID.Value = recommended.s
	}
	if !recommendationNull {
		out.Recommendation.Value = recommendation.s
	}
	return out, nil
}

// ReadModelQuestion is readModelQuestion: the question a model asked, normalized at the model
// boundary — an absent, null or blank recommendation is null, and an option with no description
// has an empty one. The saved history stays strict (ReadUserQuestion).
func ReadModelQuestion(question json.RawMessage) (UserQuestion, error) {
	v := value(question)
	if !v.isObject() {
		return readQuestion(v)
	}
	normalized := v.clone()
	absent := func(held *jvalue) bool {
		return held == nil || held.kind == jnull || (held.kind == jstring && strings.Trim(held.s, jsSpace) == "")
	}
	if absent(normalized.get("recommendedOptionId")) {
		normalized.set("recommendedOptionId", &jvalue{kind: jnull})
	}
	if absent(normalized.get("recommendation")) {
		normalized.set("recommendation", &jvalue{kind: jnull})
	}
	if options := normalized.get("options"); options.isArray() {
		for _, option := range options.a {
			if option.isObject() {
				if held := option.get("description"); held == nil || held.kind == jnull {
					option.set("description", jstr(""))
				}
			}
		}
	}
	return readQuestion(normalized)
}
