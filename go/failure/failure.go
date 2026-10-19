// Package failure is the failure catalogue in Go: the port of packages/harness/failures.ts and
// completion.ts. A failure is failure.json's object (contract.Failure): one of the catalogue's
// classes, its source, its outcome and the HTTP metadata a refusal carries, never provider prose,
// bodies, URLs, headers or stacks.
package failure

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

// Sources are where a failure may come from (FailureSource).
var Sources = []string{"model", "agent", "tool", "context", "storage", "desk"}

// Outcomes are what a failure says happened (AIFailure.outcome).
var Outcomes = []string{"not-started", "refused", "unknown"}

var (
	requestID = regexp.MustCompile(`^[-a-zA-Z0-9_]{1,128}$`)
	seconds   = regexp.MustCompile(`^\d+(?:\.\d+)?$`)
)

// Known reports a class the catalogue names.
func Known(code string) bool {
	_, ok := Catalog[code]
	return ok
}

// Alias is the class a code names (the catalogue's aliases), or "".
func Alias(code string) string { return Aliases[code] }

// New is a failure of a class, from a source, with an outcome (failure in failures.ts).
func New(code, source, outcome string) contract.Failure {
	return contract.Failure{Version: 1, Code: code, Source: source, Outcome: outcome}
}

// Read is readFailure: the failure where value is one the catalogue admits, with its members
// checked, or false.
func Read(value contract.Failure) (contract.Failure, bool) {
	if value.Version != 1 || !Known(value.Code) || !slices.Contains(Sources, value.Source) || !slices.Contains(Outcomes, value.Outcome) {
		return contract.Failure{}, false
	}
	if value.Status != nil && (*value.Status < 100 || *value.Status > 599) {
		return contract.Failure{}, false
	}
	if value.RequestID != nil && !requestID.MatchString(*value.RequestID) {
		return contract.Failure{}, false
	}
	if value.Detail != nil && utf16Length(*value.Detail) > 1000 {
		return contract.Failure{}, false
	}
	if value.RetryAt != nil {
		if len(*value.RetryAt) > 64 {
			return contract.Failure{}, false
		}
		if _, err := time.Parse(time.RFC3339Nano, *value.RetryAt); err != nil {
			return contract.Failure{}, false
		}
	}
	if value.Completion != nil {
		if _, ok := ReadCompletion(*value.Completion); !ok {
			return contract.Failure{}, false
		}
	}
	out := contract.Failure{Version: 1, Code: value.Code, Source: value.Source, Outcome: value.Outcome,
		Status: value.Status, RequestID: value.RequestID, RetryAt: value.RetryAt, Detail: value.Detail, Completion: value.Completion}
	return out, true
}

func utf16Length(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// Message is the sentence a person reads: the failure's own detail, or the catalogue's message.
func Message(f contract.Failure) string {
	if f.Detail != nil {
		return *f.Detail
	}
	return Catalog[f.Code].Message
}

// Recovery is the recovery the catalogue names for the failure's class.
func Recovery(f contract.Failure) string { return Catalog[f.Code].Recovery }

// Error is AIFailureError: a failure carried as a Go error, its message the failure's.
type Error struct{ Failure contract.Failure }

// NewError is a failure as an error. A failure the catalogue does not admit is a programming
// error and panics, as AIFailureError's constructor throws.
func NewError(f contract.Failure) *Error {
	safe, ok := Read(f)
	if !ok {
		panic("invalid AI failure: " + f.Code)
	}
	return &Error{Failure: safe}
}

func (e *Error) Error() string { return Message(e.Failure) }

// Err is failureError: a failure of a class as an error.
func Err(code, source, outcome string) *Error { return NewError(New(code, source, outcome)) }

// Authored is authoredFailure: a host-authored failure with its detail, cut to a thousand
// characters. Never provider prose or an arbitrary caught message.
func Authored(code, detail, source string) *Error {
	if source == "" {
		source = "desk"
	}
	f := New(code, source, "unknown")
	cut := cutUTF16(detail, 1000)
	f.Detail = &cut
	return NewError(f)
}

// cutUTF16 is String.prototype.slice(0, n): at most n UTF-16 code units, never half a pair.
func cutUTF16(s string, n int) string {
	units := 0
	for at, r := range s {
		size := 1
		if r >= 0x10000 {
			size = 2
		}
		if units+size > n {
			return s[:at]
		}
		units += size
	}
	return s
}

// ToolInput is toolInputFailure: host validation the model reads and corrects.
func ToolInput(detail string) *Error { return Authored("invalid-tool-input", detail, "tool") }

// Cancelled is the run ending while something waited on it (RunCancelled): never an error event.
var Cancelled = errors.New("the run was cancelled while this was still waiting on it")

// IsCancelled reports a run that ended rather than one that went wrong (isCancelled).
func IsCancelled(err error) bool {
	return errors.Is(err, Cancelled) || errors.Is(err, context.Canceled)
}

// Refusal is an HTTP answer that was not a success, as the wire reads it: its status, its body
// (read only for the catalogue's codes) and its headers (read only for the request id and the
// retry time).
type Refusal interface {
	error
	StatusCode() int
	ResponseBody() string
	ResponseHeader() http.Header
}

// Coded is an error that names its own class (an error's failureCode).
type Coded interface {
	error
	FailureCode() string
}

func metadata(f *contract.Failure, header http.Header, now time.Time) {
	if header == nil {
		return
	}
	id := header.Get("request-id")
	if id == "" {
		id = header.Get("x-request-id")
	}
	if requestID.MatchString(id) {
		f.RequestID = &id
	}
	after := header.Get("retry-after")
	if after == "" {
		return
	}
	var delay time.Duration
	if count, err := strconv.ParseFloat(after, 64); err == nil && seconds.MatchString(after) {
		delay = time.Duration(count * float64(time.Second))
	} else if at, err := http.ParseTime(after); err == nil {
		delay = at.Sub(now)
	} else {
		return
	}
	if delay < 0 {
		return
	}
	if delay > 24*time.Hour {
		delay = 24 * time.Hour
	}
	at := now.Add(delay).UTC().Format("2006-01-02T15:04:05.000Z")
	f.RetryAt = &at
}

// byStatus is the class a status alone says.
func byStatus(status int) string {
	switch {
	case status == 401:
		return "authentication"
	case status == 403:
		return "permission"
	case status == 429:
		return "rate-limited"
	case status == 408 || status == 504:
		return "timeout"
	case status == 413:
		return "context-limit"
	case status == 409:
		return "stale-state"
	case status >= 500 && status <= 599:
		return "unavailable"
	case status >= 400 && status <= 499:
		return "invalid-request"
	}
	return ""
}

// bodyCode is the catalogue alias a refusal's body names (error.code, error.type, then code).
func bodyCode(body string) string {
	if len(body) > 16384 {
		return ""
	}
	var parsed map[string]any
	if json.Unmarshal([]byte(body), &parsed) != nil || parsed == nil {
		return ""
	}
	upstream := parsed
	if inner, ok := parsed["error"].(map[string]any); ok {
		upstream = inner
	}
	for _, value := range []any{upstream["code"], upstream["type"], parsed["code"]} {
		if s, ok := value.(string); ok && Alias(s) != "" {
			return s
		}
	}
	return ""
}

// RefusalCode is refusalCode: the catalogue alias a refusal's body names, or "".
func RefusalCode(body string) string { return bodyCode(body) }

// Normalize is normalizeFailure: the class of what went wrong, from typed codes and statuses
// only. A failure already carried as an *Error is kept; a cancellation is cancelled; a refusal is
// classified by its own code, its body's catalogue code, then its status; a response that could
// not be read is invalid-response; a deadline is a timeout; what wraps another is classified by
// what it wraps, four levels down; anything else is unknown.
func Normalize(cause error, source string, now time.Time) contract.Failure {
	if source == "" {
		source = "desk"
	}
	var classify func(err error, depth int) contract.Failure
	classify = func(err error, depth int) contract.Failure {
		if err == nil || depth > 4 {
			return New("unknown", source, "unknown")
		}
		if held, ok := err.(*Error); ok {
			if f, ok := Read(held.Failure); ok {
				return f
			}
		}
		if err == Cancelled || err == context.Canceled {
			return New("cancelled", source, "unknown")
		}
		own := ""
		if coded, ok := err.(Coded); ok && Known(coded.FailureCode()) {
			own = coded.FailureCode()
		}
		status := 0
		var header http.Header
		fromBody := ""
		if refusal, ok := err.(Refusal); ok {
			status = refusal.StatusCode()
			header = refusal.ResponseHeader()
			if code := bodyCode(refusal.ResponseBody()); code != "" {
				fromBody = Alias(code)
			}
		}
		if status < 100 || status > 599 {
			status = 0
		}
		classified := own
		if classified == "" {
			classified = fromBody
		}
		if classified == "" {
			classified = byStatus(status)
		}
		if classified != "" {
			outcome := "unknown"
			if classified == "busy" {
				outcome = "not-started"
			} else if status >= 400 && status < 500 && status != 408 {
				outcome = "refused"
			}
			f := New(classified, source, outcome)
			if status != 0 {
				s := int64(status)
				f.Status = &s
			}
			metadata(&f, header, now)
			return f
		}
		var syntax *json.SyntaxError
		var typed *json.UnmarshalTypeError
		if errors.As(err, &syntax) || errors.As(err, &typed) || errors.Is(err, ErrInvalidResponse) {
			return New("invalid-response", source, "unknown")
		}
		if err == context.DeadlineExceeded {
			return New("timeout", source, "unknown")
		}
		var nested []error
		switch wrapped := err.(type) {
		case interface{ Unwrap() []error }:
			nested = wrapped.Unwrap()
		case interface{ Unwrap() error }:
			nested = []error{wrapped.Unwrap()}
		}
		for _, inner := range nested {
			if inner == nil {
				continue
			}
			if found := classify(inner, depth+1); found.Code != "unknown" {
				return found
			}
		}
		return New("unknown", source, "unknown")
	}
	return classify(cause, 0)
}

// ErrInvalidResponse is an answer the wire could not read as its format: invalid-response.
var ErrInvalidResponse = errors.New("the answer is not one the wire format reads")

// Event is failureEvent: the error event for a cause, its message the catalogue's sentence or the
// failure's own detail.
func Event(cause error, source string) contract.RunEvent {
	f := Normalize(cause, source, time.Now())
	return EventOf(f)
}

// EventOf is the error event for a failure.
func EventOf(f contract.Failure) contract.RunEvent {
	held := f
	return contract.RunEvent{Error: &contract.RunEventError{Type: "error", Message: Message(f), Failure: &held}}
}

// CanRetryAdmission is canRetryAdmission: only a positive admission refusal (busy, not started)
// is sent again, and only below the attempts allowed.
func CanRetryAdmission(f contract.Failure, attempt, maxAttempts int) bool {
	return f.Code == "busy" && f.Outcome == "not-started" && attempt < maxAttempts
}

// RetryableRefusalClasses and RetryableOverloadCodes are the refusals a model request is sent
// again after (agent#2).
var (
	RetryableRefusalClasses = []string{"rate-limited"}
	RetryableOverloadCodes  = []string{"overloaded_error"}
)

// CanRetryRefusal is canRetryRefusal: a refused request (a 4xx other than 408) of a retryable
// class, or one whose body names the overload code; never an unknown outcome.
func CanRetryRefusal(f contract.Failure, code string) bool {
	if f.Outcome != "refused" {
		return false
	}
	return slices.Contains(RetryableRefusalClasses, f.Code) || code != "" && slices.Contains(RetryableOverloadCodes, code) && Alias(code) == f.Code
}

// RetryReady is retryReady: the failure names no retry time, or it has come.
func RetryReady(f *contract.Failure, now time.Time) bool {
	if f == nil || f.RetryAt == nil {
		return true
	}
	at, err := time.Parse(time.RFC3339Nano, *f.RetryAt)
	return err != nil || !at.After(now)
}

// ToolFailure is toolFailureResult: a failed tool result as working data the model reads, its
// text the catalogue's sentence and its structured content {"failure": …}. An unknown class is
// tool-failed.
func ToolFailure(cause error) (text string, structured json.RawMessage) {
	classified := Normalize(cause, "tool", time.Now())
	if classified.Code == "unknown" {
		classified = New("tool-failed", "tool", "unknown")
	}
	encoded, _ := json.Marshal(map[string]any{"failure": classified})
	return Message(classified), encoded
}
