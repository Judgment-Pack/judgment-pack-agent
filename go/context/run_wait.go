package runcontext

// Durable waits, attention and the park/wake report (ADR-0025 decisions 2 and 6, #224 and #228;
// contract/schemas/run-wait.json), with the deadlines, approval expiry and resume revalidation of
// decisions 3 and 5 (#225, #227; contract/schemas/run-resume.json). The package validates and records: a
// park reads the pending decision and the pins from the run's retained checkpoint or event log, and the
// store commits the wait and its one needs_attention event and releases the run's lease in one
// transaction. Until the wait is resumed it holds the run: no lease of the run is granted but the
// resume's own claim. The host owns scheduling,
// clocks, wake delivery, keys, notifications and processes; nothing here polls, schedules or sends. A
// wake is never an approval, a budget grant or an answer of another reason. The TypeScript twin is
// packages/harness/storage/wait.ts. Go has no ChatStore: a wait bound to a chat is resumed through the
// TypeScript ChatStore port, and Go refuses it as chat-store-unsupported.
import (
	"bytes"
	"context"
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
)

const WaitRecordVersion = 1
const WaitLifetimeDurable = "durable"
const WaitLifetimeProcess = "process"

// WaitReleaseLimit bounds the release entries of one park; WaitExportLimit the waits of one export.
const WaitReleaseLimit = 16
const WaitExportLimit = 1024

// The wire records are the generated contract types of run-wait.json.
type RunWait = contract.RunWait
type WaitCondition = contract.RunWaitCondition
type WaitRelease = contract.RunWaitRelease
type WakeEvidence = contract.RunWaitWakeEvidence
type WakeDisposition = contract.RunWaitDisposition
type AttentionEvent = contract.RunWaitAttention
type AttentionDelivery = contract.RunWaitDelivery
type WaitReport = contract.RunWaitReport
type WaitReportEntry = contract.RunWaitReportEntry
type WaitExport = contract.RunWaitExport

var WaitRefusalReasons = []string{"invalid", "nothing-pending", "wait-pending", "conflict", "checkpoint-changed", "no-wait", "unauthenticated", "mismatch", "not-due", "stale", "unacknowledged", "already-resumed", "decision-required", "timed-out", "release-uncertain", "chat-changed", "deadline-passed", "closed", "approval-ttl-required", "key-reused", "not-resumable"}
var WaitCapabilityReasons = []string{"store-unsupported", "store-lifetime", "chat-store-unsupported"}

// WaitRefused is a named wait refusal. Nothing changed.
type WaitRefused struct{ Reason string }

func (e *WaitRefused) Error() string { return "WaitRefused: " + e.Reason + "." }
func (e *WaitRefused) Unwrap() error {
	f := failure.New("stale-state", "storage", "not-started")
	detail := e.Error()
	f.Detail = &detail
	return failure.NewError(f)
}
func (e *WaitRefused) Record() contract.RunWaitRefusal {
	return contract.RunWaitRefusal{Version: WaitRecordVersion, Reason: e.Reason}
}

// WaitCapabilityRefused: a store that cannot keep the wait guarantee refuses by name; it never pretends.
type WaitCapabilityRefused struct{ Reason string }

func (e *WaitCapabilityRefused) Error() string { return "WaitCapabilityRefused: " + e.Reason + "." }
func (e *WaitCapabilityRefused) Unwrap() error {
	f := failure.New("configuration", "storage", "not-started")
	detail := e.Error()
	f.Detail = &detail
	return failure.NewError(f)
}
func (e *WaitCapabilityRefused) Record() contract.RunWaitCapabilityRefusal {
	return contract.RunWaitCapabilityRefusal{Version: WaitRecordVersion, Reason: e.Reason}
}

func waitInvalid() error              { return &WaitRefused{Reason: "invalid"} }
func waitRefused(reason string) error { return &WaitRefused{Reason: reason} }

var waitKeyPattern = regexp.MustCompile(`^[!-~]{32,512}$`)

// ValidWakeKey (#265 M1): a wake key is 32 to 512 printable ASCII characters, at least 16 of them
// distinct. The host mints a fresh one for each wait from a cryptographic random source (32 random bytes
// in base64url, for example); the rule refuses a short or repetitive key, and cannot tell a guessable one.
func ValidWakeKey(key string) bool {
	if !waitKeyPattern.MatchString(key) {
		return false
	}
	distinct := map[byte]bool{}
	for i := 0; i < len(key); i++ {
		distinct[key[i]] = true
	}
	return len(distinct) >= 16
}

// waitSpace (#265 M4) is one whitespace set in both languages: unicode.IsSpace and U+FEFF, which is
// JavaScript's \s and U+0085.
func waitSpace(r rune) bool { return unicode.IsSpace(r) || r == '\uFEFF' }

// waitBlank: a text made only of waitSpace.
func waitBlank(text string) bool { return strings.TrimFunc(text, waitSpace) == "" }

// waitFold (#265 M4) is one case fold in both languages: the text trimmed of waitSpace, each code point
// lowercased on its own (U+0130 is i with U+0307, as JavaScript lowercases it) and final sigma read as
// sigma.
func waitFold(text string) string {
	var out strings.Builder
	for _, r := range strings.TrimFunc(text, waitSpace) {
		switch {
		case r == '\u0130':
			out.WriteString("i\u0307")
		case r == '\u03C2':
			out.WriteRune('\u03C3')
		default:
			if r = unicode.ToLower(r); r == '\u03C2' {
				r = '\u03C3'
			}
			out.WriteRune(r)
		}
	}
	return out.String()
}

var waitOptionPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,100}$`)

// jsLength is a string's length as JavaScript counts it, in UTF-16 code units.
func jsLength(s string) int { return len(utf16.Encode([]rune(s))) }

type waitMembers map[string]json.RawMessage

// waitShape decodes one object with exactly the required members, and of the optional ones only those
// named; an optional member present as null is not absent.
func waitShape(raw json.RawMessage, required, optional []string) (waitMembers, error) {
	var members map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&members); err != nil || members == nil || decoder.More() {
		return nil, waitInvalid()
	}
	for _, name := range required {
		if _, ok := members[name]; !ok {
			return nil, waitInvalid()
		}
	}
	for name, value := range members {
		if !slices.Contains(required, name) && !slices.Contains(optional, name) {
			return nil, waitInvalid()
		}
		if slices.Contains(optional, name) && string(bytes.TrimSpace(value)) == "null" {
			return nil, waitInvalid()
		}
	}
	return members, nil
}
func (m waitMembers) has(name string) bool { _, ok := m[name]; return ok }
func (m waitMembers) str(name string) (string, error) {
	raw := bytes.TrimSpace(m[name])
	var value string
	if len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, &value) != nil {
		return "", waitInvalid()
	}
	return value, nil
}
func (m waitMembers) id(name string) (string, error) {
	value, err := m.str(name)
	if err != nil || !validStorageID(value) {
		return "", waitInvalid()
	}
	return value, nil
}
func (m waitMembers) text(name string, min, max int) (string, error) {
	value, err := m.str(name)
	if err != nil || jsLength(value) < min || jsLength(value) > max {
		return "", waitInvalid()
	}
	return value, nil
}
func (m waitMembers) digest(name string) (string, error) {
	value, err := m.str(name)
	if err != nil || !leaseDigestPattern.MatchString(value) {
		return "", waitInvalid()
	}
	return value, nil
}
func (m waitMembers) time(name string) (string, error) {
	value, err := m.str(name)
	if err != nil {
		return "", err
	}
	if _, ok := leaseExpiryMillis(value); !ok {
		return "", waitInvalid()
	}
	return value, nil
}
func (m waitMembers) epoch(name string) (string, error) {
	value, err := m.str(name)
	if err != nil || !ValidLeaseEpoch(value) {
		return "", waitInvalid()
	}
	return value, nil
}
func (m waitMembers) enum(name string, values ...string) (string, error) {
	value, err := m.str(name)
	if err != nil || !slices.Contains(values, value) {
		return "", waitInvalid()
	}
	return value, nil
}
func (m waitMembers) integer(name string, min, max int64) (int64, error) {
	var number float64
	if json.Unmarshal(m[name], &number) != nil || number != float64(int64(number)) || int64(number) < min || int64(number) > max {
		return 0, waitInvalid()
	}
	return int64(number), nil
}
func (m waitMembers) version() error {
	var number float64
	if json.Unmarshal(m["version"], &number) != nil || number != WaitRecordVersion {
		return waitInvalid()
	}
	return nil
}
func (m waitMembers) list(name string, max int) ([]json.RawMessage, error) {
	var items []json.RawMessage
	raw := bytes.TrimSpace(m[name])
	if len(raw) == 0 || raw[0] != '[' || json.Unmarshal(raw, &items) != nil || len(items) > max {
		return nil, waitInvalid()
	}
	return items, nil
}
func waitTimeMillis(text string) int64 { ms, _ := leaseExpiryMillis(text); return ms }

// readWaitQuestion holds readUserQuestion's rules: exact members, bounded non-blank text, no or two to
// four options with unique ids and labels, and a recommendation that names an offered option.
func readWaitQuestion(raw json.RawMessage) (contract.Question, error) {
	m, err := waitShape(raw, []string{"title", "question", "selection", "options", "recommendedOptionId", "recommendation"}, nil)
	if err != nil {
		return contract.Question{}, err
	}
	nonBlank := func(name string, max int) error {
		value, err := m.text(name, 1, max)
		if err != nil || waitBlank(value) {
			return waitInvalid()
		}
		return nil
	}
	if nonBlank("title", 40) != nil || nonBlank("question", 600) != nil {
		return contract.Question{}, waitInvalid()
	}
	if _, err := m.enum("selection", "single", "multiple"); err != nil {
		return contract.Question{}, err
	}
	options, err := m.list("options", 4)
	if err != nil || len(options) == 1 {
		return contract.Question{}, waitInvalid()
	}
	ids, labels := map[string]bool{}, map[string]bool{}
	for _, item := range options {
		o, err := waitShape(item, []string{"id", "label", "description"}, nil)
		if err != nil {
			return contract.Question{}, err
		}
		id, _ := o.str("id")
		label, err := o.text("label", 1, 100)
		if err != nil || !waitOptionPattern.MatchString(id) || waitBlank(label) || ids[id] || labels[waitFold(label)] {
			return contract.Question{}, waitInvalid()
		}
		if _, err := o.text("description", 0, 240); err != nil {
			return contract.Question{}, err
		}
		ids[id], labels[waitFold(label)] = true, true
	}
	null := func(name string) bool { return string(bytes.TrimSpace(m[name])) == "null" }
	if null("recommendedOptionId") != null("recommendation") {
		return contract.Question{}, waitInvalid()
	}
	if !null("recommendedOptionId") {
		id, err := m.str("recommendedOptionId")
		if err != nil || !waitOptionPattern.MatchString(id) || !ids[id] || nonBlank("recommendation", 240) != nil {
			return contract.Question{}, waitInvalid()
		}
	}
	var question contract.Question
	if json.Unmarshal(raw, &question) != nil {
		return contract.Question{}, waitInvalid()
	}
	return question, nil
}
func waitOptionOffered(question *contract.Question, id string) bool {
	for _, option := range question.Options {
		if option.ID == id {
			return true
		}
	}
	return false
}

// readWaitAnswer reads a draft {selected, text}; with a question, every option is offered and a single
// selection chooses at most one.
func readWaitAnswer(raw json.RawMessage, question *contract.Question) (contract.QuestionDraft, error) {
	m, err := waitShape(raw, []string{"selected", "text"}, nil)
	if err != nil {
		return contract.QuestionDraft{}, err
	}
	items, err := m.list("selected", 4)
	if err != nil {
		return contract.QuestionDraft{}, err
	}
	draft := contract.QuestionDraft{Selected: []string{}}
	for _, item := range items {
		var id string
		if len(item) == 0 || item[0] != '"' || json.Unmarshal(item, &id) != nil || !waitOptionPattern.MatchString(id) || slices.Contains(draft.Selected, id) {
			return contract.QuestionDraft{}, waitInvalid()
		}
		if question != nil && !waitOptionOffered(question, id) {
			return contract.QuestionDraft{}, waitInvalid()
		}
		draft.Selected = append(draft.Selected, id)
	}
	if draft.Text, err = m.text("text", 0, 4000); err != nil {
		return contract.QuestionDraft{}, err
	}
	if question != nil && question.Selection == "single" && len(draft.Selected) > 1 {
		return contract.QuestionDraft{}, waitInvalid()
	}
	return draft, nil
}

// realAnswer: a chosen option or non-blank free text. An empty draft is not an answer.
func realAnswer(draft contract.QuestionDraft) bool {
	return len(draft.Selected) > 0 || !waitBlank(draft.Text)
}
func waitCanonicalDigest(value any) string {
	raw, err := canonicalRaw(value)
	if err != nil {
		return ""
	}
	return durableDigest(raw)
}

// WaitQuestionDigest is the SHA-256 of the question's canonical JSON.
func WaitQuestionDigest(question contract.Question) string { return waitCanonicalDigest(question) }

// WaitAnswerDigest is the SHA-256 of the answer's canonical JSON.
func WaitAnswerDigest(draft contract.QuestionDraft) string {
	if draft.Selected == nil {
		draft.Selected = []string{}
	}
	return waitCanonicalDigest(draft)
}

// WaitAttentionID is the attention event's identity: the SHA-256 of the canonical JSON of {resource,
// wait}. A notifier deduplicates by it.
func WaitAttentionID(resource, wait string) string {
	return durableDigest([]byte(CanonicalJSON(ObjectOf("resource", String(resource), "wait", String(wait)))))
}

// WakeKeyDigest is the SHA-256 of the host's wake key; the key itself stays the host's.
func WakeKeyDigest(key string) (string, error) {
	if !ValidWakeKey(key) {
		return "", waitInvalid()
	}
	return durableDigest([]byte(key)), nil
}

// CheckpointBytesDigest is the SHA-256 of a run head's checkpoint bytes.
func CheckpointBytesDigest(raw []byte) string { return durableDigest(raw) }

// WakeEvidenceDigest is the evidence's digest without its key: a duplicate is the same evidence id
// with this digest.
func WakeEvidenceDigest(evidence WakeEvidence) (string, error) {
	held, err := checkWakeEvidence(evidence)
	if err != nil {
		return "", err
	}
	raw, _ := json.Marshal(held)
	var members map[string]json.RawMessage
	_ = json.Unmarshal(raw, &members)
	delete(members, "key")
	return waitCanonicalDigest(members), nil
}

func readEventCondition(raw json.RawMessage) (contract.RunWaitEventCondition, error) {
	m, err := waitShape(raw, []string{"kind", "subject"}, nil)
	if err != nil {
		return contract.RunWaitEventCondition{}, err
	}
	kind, err := m.id("kind")
	if err != nil {
		return contract.RunWaitEventCondition{}, err
	}
	subject, err := m.id("subject")
	return contract.RunWaitEventCondition{Kind: kind, Subject: subject}, err
}

// ReadWaitCondition reads an event condition, a wake time, or both.
func ReadWaitCondition(raw json.RawMessage) (WaitCondition, error) {
	m, err := waitShape(raw, nil, []string{"event", "wakeAt"})
	if err != nil || len(m) == 0 {
		return WaitCondition{}, waitInvalid()
	}
	var condition WaitCondition
	if m.has("event") {
		event, err := readEventCondition(m["event"])
		if err != nil {
			return WaitCondition{}, err
		}
		condition.Event = &event
	}
	if m.has("wakeAt") {
		at, err := m.time("wakeAt")
		if err != nil {
			return WaitCondition{}, err
		}
		condition.WakeAt = &at
	}
	return condition, nil
}
func readWaitPending(raw json.RawMessage, reason string, redacted bool) (contract.RunWaitPending, error) {
	probe, err := waitShape(raw, []string{"kind"}, []string{"callId", "name", "tool", "unit", "used", "limit", "event", "questionDigest", "question", "operation", "source", "connection", "cause"})
	if err != nil {
		return contract.RunWaitPending{}, err
	}
	if kind, _ := probe.str("kind"); kind != reason {
		return contract.RunWaitPending{}, waitInvalid()
	}
	switch reason {
	case "approval":
		m, err := waitShape(raw, []string{"kind", "callId", "name", "tool"}, nil)
		if err != nil {
			return contract.RunWaitPending{}, err
		}
		approval := contract.RunWaitPendingApproval{Kind: "approval"}
		if approval.CallID, err = m.text("callId", 1, 256); err != nil {
			return contract.RunWaitPending{}, err
		}
		if approval.Name, err = m.text("name", 1, 256); err != nil {
			return contract.RunWaitPending{}, err
		}
		if approval.Tool, err = m.text("tool", 1, 128); err != nil {
			return contract.RunWaitPending{}, err
		}
		return contract.RunWaitPending{Approval: &approval}, nil
	case "budget":
		m, err := waitShape(raw, []string{"kind", "unit", "used", "limit"}, nil)
		if err != nil {
			return contract.RunWaitPending{}, err
		}
		budget := contract.RunWaitPendingBudget{Kind: "budget"}
		// #265 M3: a provider-managed allowance takes no grant, so no wait carries one.
		if budget.Unit, err = m.enum("unit", "model-requests"); err != nil {
			return contract.RunWaitPending{}, err
		}
		if budget.Limit, err = m.integer("limit", 1, 100); err != nil {
			return contract.RunWaitPending{}, err
		}
		if budget.Used, err = m.integer("used", 0, budget.Limit); err != nil {
			return contract.RunWaitPending{}, err
		}
		return contract.RunWaitPending{Budget: &budget}, nil
	case "reauthorization":
		// #226: the never-admitted operation of a trusted disposition, and its connection.
		m, err := waitShape(raw, []string{"kind", "operation", "source", "tool", "connection", "cause"}, nil)
		if err != nil {
			return contract.RunWaitPending{}, err
		}
		pending := contract.RunWaitPendingReauthorization{Kind: "reauthorization"}
		for _, field := range []struct {
			name string
			to   *string
		}{{"operation", &pending.Operation}, {"source", &pending.Source}, {"tool", &pending.Tool}} {
			if *field.to, err = m.id(field.name); err != nil {
				return contract.RunWaitPending{}, err
			}
		}
		if pending.Connection, err = m.str("connection"); err != nil || !ValidConnectionReference(pending.Connection) {
			return contract.RunWaitPending{}, waitInvalid()
		}
		if pending.Cause, err = m.enum("cause", "expired", "revoked"); err != nil {
			return contract.RunWaitPending{}, err
		}
		return contract.RunWaitPending{Reauthorization: &pending}, nil
	}
	m, err := waitShape(raw, []string{"kind", "event", "questionDigest"}, []string{"question"})
	if err != nil {
		return contract.RunWaitPending{}, err
	}
	// A question is carried until the wait is redacted, and never after; its digest is always kept.
	if redacted == m.has("question") {
		return contract.RunWaitPending{}, waitInvalid()
	}
	question := contract.RunWaitPendingQuestion{Kind: "question"}
	if question.Event, err = m.id("event"); err != nil {
		return contract.RunWaitPending{}, err
	}
	if question.QuestionDigest, err = m.digest("questionDigest"); err != nil {
		return contract.RunWaitPending{}, err
	}
	if m.has("question") {
		held, err := readWaitQuestion(m["question"])
		if err != nil || WaitQuestionDigest(held) != question.QuestionDigest {
			return contract.RunWaitPending{}, waitInvalid()
		}
		question.Question = &held
	}
	return contract.RunWaitPending{Question: &question}, nil
}
func readWaitReleaseList(raw json.RawMessage) ([]WaitRelease, error) {
	items, err := waitMembers{"list": raw}.list("list", WaitReleaseLimit)
	if err != nil {
		return nil, err
	}
	list := []WaitRelease{}
	for _, item := range items {
		m, err := waitShape(item, []string{"resource", "outcome"}, nil)
		if err != nil {
			return nil, err
		}
		resource, err := m.id("resource")
		if err != nil {
			return nil, err
		}
		outcome, err := m.enum("outcome", "released", "not-held", "uncertain")
		if err != nil {
			return nil, err
		}
		for _, prior := range list {
			if prior.Resource == resource {
				return nil, waitInvalid()
			}
		}
		list = append(list, WaitRelease{Resource: resource, Outcome: outcome})
	}
	return list, nil
}
func readWaitActor(raw json.RawMessage) (contract.RunWaitActor, error) {
	m, err := waitShape(raw, []string{"owner", "epoch", "at"}, nil)
	if err != nil {
		return contract.RunWaitActor{}, err
	}
	var actor contract.RunWaitActor
	if actor.Owner, err = m.id("owner"); err != nil {
		return actor, err
	}
	if actor.Epoch, err = m.epoch("epoch"); err != nil {
		return actor, err
	}
	actor.At, err = m.time("at")
	return actor, err
}
func readWaitDisposition(raw json.RawMessage) (WakeDisposition, error) {
	m, err := waitShape(raw, []string{"evidence", "kind", "at", "digest"}, nil)
	if err != nil {
		return WakeDisposition{}, err
	}
	var d WakeDisposition
	if d.Evidence, err = m.id("evidence"); err != nil {
		return d, err
	}
	if d.Kind, err = m.enum("kind", "event", "time", "timeout", "deadline"); err != nil {
		return d, err
	}
	if d.At, err = m.time("at"); err != nil {
		return d, err
	}
	d.Digest, err = m.digest("digest")
	return d, err
}
func readWaitResolution(raw json.RawMessage, question *contract.Question, redacted bool) (contract.RunWaitResolution, error) {
	probe, err := waitShape(raw, []string{"kind", "at"}, []string{"actor", "answerDigest", "answer"})
	if err != nil {
		return contract.RunWaitResolution{}, err
	}
	kind, err := probe.enum("kind", "answered", "unanswered")
	if err != nil {
		return contract.RunWaitResolution{}, err
	}
	if kind == "unanswered" {
		m, err := waitShape(raw, []string{"kind", "at"}, nil)
		if err != nil {
			return contract.RunWaitResolution{}, err
		}
		at, err := m.time("at")
		return contract.RunWaitResolution{Kind: "unanswered", At: at}, err
	}
	m, err := waitShape(raw, []string{"kind", "at", "actor", "answerDigest"}, []string{"answer"})
	if err != nil || redacted == m.has("answer") {
		return contract.RunWaitResolution{}, waitInvalid()
	}
	resolution := contract.RunWaitResolution{Kind: "answered"}
	if resolution.At, err = m.time("at"); err != nil {
		return resolution, err
	}
	actor, err := m.id("actor")
	if err != nil {
		return resolution, err
	}
	answerDigest, err := m.digest("answerDigest")
	if err != nil {
		return resolution, err
	}
	resolution.Actor, resolution.AnswerDigest = &actor, &answerDigest
	if m.has("answer") {
		draft, err := readWaitAnswer(m["answer"], question)
		if err != nil || !realAnswer(draft) || WaitAnswerDigest(draft) != answerDigest {
			return resolution, waitInvalid()
		}
		resolution.Answer = &draft
	}
	return resolution, nil
}

var waitParkRequired = []string{"version", "resource", "wait", "reason", "pending", "condition", "continuation", "checkpoint", "pins", "wakeKeyDigest", "release"}
var waitParkOptional = []string{"timeoutAt", "limits", "approval", "chat", "retainUntil", "reauthorization"}
var waitReasons = []string{"approval", "budget", "question", "reauthorization"}

// readWaitParkMembers reads a park's members; stored, a wait as the store keeps it, which may carry the
// approval binding the store attached to a reauthorization wait (a park never does).
func readWaitParkMembers(m waitMembers, redacted, stored bool) (RunWait, error) {
	if err := m.version(); err != nil {
		return RunWait{}, err
	}
	wait := RunWait{Version: WaitRecordVersion}
	var err error
	if wait.Reason, err = m.enum("reason", waitReasons...); err != nil {
		return RunWait{}, err
	}
	if wait.Condition, err = ReadWaitCondition(m["condition"]); err != nil {
		return RunWait{}, err
	}
	// A question waits for its answer event, and a reauthorization for the host's or the person's action;
	// a wake time would end either with neither.
	if (wait.Reason == "question" || wait.Reason == "reauthorization") && (wait.Condition.Event == nil || wait.Condition.WakeAt != nil) {
		return RunWait{}, waitInvalid()
	}
	if wait.Resource, err = m.id("resource"); err != nil {
		return RunWait{}, err
	}
	if wait.Wait, err = m.id("wait"); err != nil {
		return RunWait{}, err
	}
	if wait.Pending, err = readWaitPending(m["pending"], wait.Reason, redacted); err != nil {
		return RunWait{}, err
	}
	if m.has("timeoutAt") {
		at, err := m.time("timeoutAt")
		if err != nil {
			return RunWait{}, err
		}
		wait.TimeoutAt = &at
	}
	if wait.Continuation, err = m.id("continuation"); err != nil {
		return RunWait{}, err
	}
	checkpoint, err := waitShape(m["checkpoint"], []string{"revision", "digest"}, nil)
	if err != nil {
		return RunWait{}, err
	}
	if wait.Checkpoint.Revision, err = checkpoint.id("revision"); err != nil {
		return RunWait{}, err
	}
	if wait.Checkpoint.Digest, err = checkpoint.digest("digest"); err != nil {
		return RunWait{}, err
	}
	if m.has("chat") {
		chat, err := waitShape(m["chat"], []string{"id", "revision"}, nil)
		if err != nil {
			return RunWait{}, err
		}
		binding := contract.RunWaitChatBinding{}
		if binding.ID, err = chat.id("id"); err != nil {
			return RunWait{}, err
		}
		if binding.Revision, err = chat.id("revision"); err != nil {
			return RunWait{}, err
		}
		wait.Chat = &binding
	}
	if wait.WakeKeyDigest, err = m.digest("wakeKeyDigest"); err != nil {
		return RunWait{}, err
	}
	if wait.Release, err = readWaitReleaseList(m["release"]); err != nil {
		return RunWait{}, err
	}
	if m.has("retainUntil") {
		at, err := m.time("retainUntil")
		if err != nil {
			return RunWait{}, err
		}
		wait.RetainUntil = &at
	}
	if wait.Pins, err = ReadResumePins(m["pins"]); err != nil {
		return RunWait{}, asWait(err)
	}
	if m.has("limits") {
		limits, err := ReadWaitLimits(m["limits"])
		if err != nil {
			return RunWait{}, asWait(err)
		}
		wait.Limits = &limits
	}
	if m.has("approval") {
		binding, err := readApprovalBinding(m["approval"])
		if err != nil {
			return RunWait{}, asWait(err)
		}
		wait.Approval = &binding
	}
	if m.has("reauthorization") {
		disposition, err := ReadReauthDisposition(m["reauthorization"])
		if err != nil {
			return RunWait{}, waitInvalid()
		}
		wait.Reauthorization = &disposition
	}
	// #225: an approval wait is bound to its pending call and pinned source, and expires; no other wait
	// carries either.
	ttl := wait.Limits != nil && wait.Limits.ApprovalTtlMs != nil
	pinned := func() bool {
		return slices.ContainsFunc(wait.Pins.Sources, func(s SourcePin) bool { return s.Source == wait.Approval.Source })
	}
	if wait.Reason == "approval" {
		if wait.Approval == nil || wait.Approval.CallID != wait.Pending.Approval.CallID || !ttl || !pinned() {
			return RunWait{}, waitInvalid()
		}
	} else if wait.Reason == "reauthorization" {
		// #226: the trusted disposition is this run's, and its operation, connection and cause are the
		// pending ones. An unsettled approval's binding is the store's to attach, and its source is pinned;
		// no TTL is the park's.
		pending, held := wait.Pending.Reauthorization, wait.Reauthorization
		if held == nil || held.Resource != wait.Resource || held.Operation.Operation != pending.Operation || held.Operation.Source != pending.Source || held.Operation.Tool != pending.Tool || held.Connection != pending.Connection || held.Cause != pending.Cause {
			return RunWait{}, waitInvalid()
		}
		if ttl || wait.Approval != nil && (!stored || !pinned()) {
			return RunWait{}, waitInvalid()
		}
	} else if wait.Approval != nil || ttl {
		return RunWait{}, waitInvalid()
	}
	if wait.Reason != "reauthorization" && wait.Reauthorization != nil {
		return RunWait{}, waitInvalid()
	}
	return wait, nil
}

// readWaitPark reads the store's input: a park before the store stamps who parked it, when, and its
// attention.
func readWaitPark(park RunWait) (RunWait, error) {
	if park.Parked != (contract.RunWaitActor{}) || park.Attention != "" || park.State != "" || park.Deadline != nil || park.Disposition != nil || park.Expired != nil || park.Resolution != nil || park.Resumed != nil || park.Revalidation != nil || park.Reconciled != nil || park.Redacted != nil || park.Reauthorized != nil {
		return RunWait{}, waitInvalid()
	}
	if park.Release == nil {
		park.Release = []WaitRelease{}
	}
	if park.Pins.Sources == nil {
		park.Pins.Sources = []SourcePin{}
	}
	if park.Pins.Artifacts == nil {
		park.Pins.Artifacts = []ArtifactPin{}
	}
	raw, err := json.Marshal(park)
	if err != nil {
		return RunWait{}, waitInvalid()
	}
	var members map[string]json.RawMessage
	if json.Unmarshal(raw, &members) != nil {
		return RunWait{}, waitInvalid()
	}
	for _, stamped := range []string{"parked", "attention", "state"} {
		delete(members, stamped)
	}
	stripped, _ := json.Marshal(members)
	m, err := waitShape(stripped, waitParkRequired, waitParkOptional)
	if err != nil {
		return RunWait{}, err
	}
	return readWaitParkMembers(m, false, false)
}

// ReadRunWait reads a version 1 wait record and holds every rule the schema cannot state.
func ReadRunWait(raw []byte) (RunWait, error) {
	m, err := waitShape(raw, append(slices.Clone(waitParkRequired), "parked", "attention", "state"), append(slices.Clone(waitParkOptional), "deadline", "disposition", "expired", "resolution", "resumed", "revalidation", "reconciled", "redacted", "reauthorized"))
	if err != nil {
		return RunWait{}, err
	}
	redacted := false
	if m.has("redacted") {
		if string(bytes.TrimSpace(m["redacted"])) != "true" {
			return RunWait{}, waitInvalid()
		}
		redacted = true
		if reason, _ := m.str("reason"); reason != "question" {
			return RunWait{}, waitInvalid()
		}
	}
	wait, err := readWaitParkMembers(m, redacted, true)
	if err != nil {
		return RunWait{}, err
	}
	if wait.Parked, err = readWaitActor(m["parked"]); err != nil {
		return RunWait{}, err
	}
	if wait.Attention, err = m.digest("attention"); err != nil || wait.Attention != WaitAttentionID(wait.Resource, wait.Wait) {
		return RunWait{}, waitInvalid()
	}
	if wait.State, err = m.enum("state", "parked", "woken", "timed-out", "resumed", "expired"); err != nil {
		return RunWait{}, err
	}
	// #225: the stamped deadline follows the park's limits and time; any limit always stamps one. #226: a
	// reauthorization wait carries the run's unsettled approval binding exactly when its deadline names
	// that approval's expiry.
	bound := wait.Reason == "reauthorization" && wait.Approval != nil
	if m.has("deadline") {
		deadline, err := readWaitDeadlineBound(m["deadline"], wait.Limits, wait.Parked.At, bound)
		if err != nil {
			return RunWait{}, asWait(err)
		}
		wait.Deadline = &deadline
	} else if wait.Limits != nil || bound {
		return RunWait{}, waitInvalid()
	}
	if wait.State == "parked" {
		if m.has("disposition") || m.has("expired") || m.has("resolution") || m.has("resumed") {
			return RunWait{}, waitInvalid()
		}
	} else if wait.State == "expired" {
		// A deadline ends the wait for good: it keeps the wake it was woken with, if any, and is never
		// resumed.
		expired, err := readWaitDisposition(m["expired"])
		if err != nil {
			return RunWait{}, err
		}
		if expired.Kind != "deadline" || wait.Deadline == nil || waitTimeMillis(expired.At) < waitTimeMillis(wait.Deadline.At) {
			return RunWait{}, waitInvalid()
		}
		wait.Expired = &expired
		if m.has("disposition") {
			disposition, err := readWaitDisposition(m["disposition"])
			if err != nil {
				return RunWait{}, err
			}
			if disposition.Kind == "deadline" || waitTimeMillis(disposition.At) < waitTimeMillis(wait.Parked.At) || waitTimeMillis(expired.At) < waitTimeMillis(disposition.At) {
				return RunWait{}, waitInvalid()
			}
			wait.Disposition = &disposition
		}
		// #266 N1: an approved call that did not settle by the approval's expiry ends its resumed wait there;
		// its outcome is unknown. Only an approval wait, woken and claimed, is expired after its claim.
		if m.has("resumed") {
			resumed, err := readWaitActor(m["resumed"])
			if err != nil || wait.Reason != "approval" || wait.Disposition == nil || wait.Disposition.Kind == "timeout" || wait.Deadline.Approval == nil || waitTimeMillis(expired.At) < waitTimeMillis(*wait.Deadline.Approval) || waitTimeMillis(resumed.At) < waitTimeMillis(wait.Disposition.At) || waitTimeMillis(expired.At) < waitTimeMillis(resumed.At) {
				return RunWait{}, waitInvalid()
			}
			wait.Resumed = &resumed
		}
	} else {
		if m.has("expired") {
			return RunWait{}, waitInvalid()
		}
		disposition, err := readWaitDisposition(m["disposition"])
		if err != nil {
			return RunWait{}, err
		}
		wait.Disposition = &disposition
		// A timeout ends the wait timed-out; a resumed wait keeps the disposition it was resumed after (a
		// timed-out approval or budget wait is never resumed).
		if disposition.Kind == "deadline" || wait.State == "woken" && disposition.Kind == "timeout" || wait.State == "timed-out" && disposition.Kind != "timeout" || wait.State == "resumed" && disposition.Kind == "timeout" && wait.Reason != "question" {
			return RunWait{}, waitInvalid()
		}
		if wait.State == "resumed" {
			resumed, err := readWaitActor(m["resumed"])
			if err != nil || waitTimeMillis(resumed.At) < waitTimeMillis(disposition.At) {
				return RunWait{}, waitInvalid()
			}
			wait.Resumed = &resumed
		} else if m.has("resumed") {
			return RunWait{}, waitInvalid()
		}
		if waitTimeMillis(disposition.At) < waitTimeMillis(wait.Parked.At) {
			return RunWait{}, waitInvalid()
		}
	}
	var question *contract.Question
	if wait.Pending.Question != nil {
		question = wait.Pending.Question.Question
	}
	if wait.Reason == "question" && wait.State != "parked" {
		resolution, err := readWaitResolution(m["resolution"], question, redacted)
		if err != nil {
			return RunWait{}, err
		}
		// A timeout ends the question unanswered, and so does a deadline that came before any wake; only an
		// event wake carries an answer.
		ended := wait.Disposition
		if ended == nil {
			ended = wait.Expired
		}
		if (ended.Kind == "timeout" || ended.Kind == "deadline") != (resolution.Kind == "unanswered") || resolution.At != ended.At {
			return RunWait{}, waitInvalid()
		}
		wait.Resolution = &resolution
	} else if m.has("resolution") {
		return RunWait{}, waitInvalid()
	}
	// #227: a claimed continuation records the revalidation it was claimed after, for this wait and its
	// pins.
	if m.has("revalidation") {
		if wait.Resumed == nil {
			return RunWait{}, waitInvalid()
		}
		revalidation, err := ReadRevalidation(m["revalidation"])
		if err != nil {
			return RunWait{}, asWait(err)
		}
		pins, err := PinsDigest(wait.Pins)
		if err != nil || revalidation.Resource != wait.Resource || revalidation.Wait != wait.Wait || revalidation.Pins != pins {
			return RunWait{}, waitInvalid()
		}
		wait.Revalidation = &revalidation
	} else if wait.Resumed != nil {
		return RunWait{}, waitInvalid()
	}
	if m.has("reconciled") {
		if wait.Resumed == nil {
			return RunWait{}, waitInvalid()
		}
		if wait.Reconciled, err = readWaitReleaseList(m["reconciled"]); err != nil {
			return RunWait{}, err
		}
	}
	// #226: a resumed reauthorization wait records the refresh it was claimed after: the parked principal,
	// permissions that cover what the operation requires, taken after the park and no later than the claim.
	if m.has("reauthorized") {
		if wait.Reason != "reauthorization" || wait.Resumed == nil {
			return RunWait{}, waitInvalid()
		}
		reauthorized, err := ReadReauthorized(m["reauthorized"])
		disposition := wait.Reauthorization
		if err != nil || reauthorized.Principal != disposition.Principal || !permitted(disposition.Requires, reauthorized.Permissions) || waitTimeMillis(reauthorized.At) < waitTimeMillis(wait.Parked.At) || waitTimeMillis(reauthorized.At) > waitTimeMillis(wait.Resumed.At) {
			return RunWait{}, waitInvalid()
		}
		wait.Reauthorized = &reauthorized
	} else if wait.Reason == "reauthorization" && wait.Resumed != nil {
		return RunWait{}, waitInvalid()
	}
	if redacted {
		yes := true
		wait.Redacted = &yes
	}
	return wait, nil
}

// ReadWakeEvidence reads bounded wake evidence. A model-supplied event name, job id or time is never
// evidence: only the host presents the wait's key.
func ReadWakeEvidence(raw []byte) (WakeEvidence, error) {
	m, err := waitShape(raw, []string{"version", "resource", "wait", "evidence", "kind", "key"}, []string{"event", "answer", "actor"})
	if err != nil {
		return WakeEvidence{}, err
	}
	if err := m.version(); err != nil {
		return WakeEvidence{}, err
	}
	evidence := WakeEvidence{Version: WaitRecordVersion}
	if evidence.Kind, err = m.enum("kind", "event", "time", "timeout", "deadline"); err != nil {
		return WakeEvidence{}, err
	}
	if (evidence.Kind == "event") != m.has("event") || m.has("answer") != m.has("actor") || m.has("answer") && evidence.Kind != "event" {
		return WakeEvidence{}, waitInvalid()
	}
	if evidence.Key, err = m.str("key"); err != nil || !ValidWakeKey(evidence.Key) {
		return WakeEvidence{}, waitInvalid()
	}
	if evidence.Resource, err = m.id("resource"); err != nil {
		return WakeEvidence{}, err
	}
	if evidence.Wait, err = m.id("wait"); err != nil {
		return WakeEvidence{}, err
	}
	if evidence.Evidence, err = m.id("evidence"); err != nil {
		return WakeEvidence{}, err
	}
	if m.has("event") {
		event, err := readEventCondition(m["event"])
		if err != nil {
			return WakeEvidence{}, err
		}
		evidence.Event = &event
	}
	if m.has("answer") {
		answer, err := readWaitAnswer(m["answer"], nil)
		if err != nil {
			return WakeEvidence{}, err
		}
		actor, err := m.id("actor")
		if err != nil {
			return WakeEvidence{}, err
		}
		evidence.Answer, evidence.Actor = &answer, &actor
	}
	return evidence, nil
}
func checkWakeEvidence(evidence WakeEvidence) (WakeEvidence, error) {
	raw, err := json.Marshal(evidence)
	if err != nil {
		return WakeEvidence{}, waitInvalid()
	}
	return ReadWakeEvidence(raw)
}

var waitNeeds = map[string]string{"approval": "approval-decision", "budget": "budget-grant", "question": "question-answer", "reauthorization": "reauthorization"}

// ReadAttentionEvent reads a version 1 needs_attention event.
func ReadAttentionEvent(raw []byte) (AttentionEvent, error) {
	m, err := waitShape(raw, []string{"version", "type", "id", "resource", "wait", "reason", "needs", "condition", "releaseUncertain", "at"}, []string{"subject", "timeoutAt", "deadline"})
	if err != nil {
		return AttentionEvent{}, err
	}
	if err := m.version(); err != nil {
		return AttentionEvent{}, err
	}
	event := AttentionEvent{Version: WaitRecordVersion}
	if event.Type, err = m.enum("type", "needs_attention"); err != nil {
		return event, err
	}
	if json.Unmarshal(m["releaseUncertain"], &event.ReleaseUncertain) != nil || string(bytes.TrimSpace(m["releaseUncertain"])) != "true" && string(bytes.TrimSpace(m["releaseUncertain"])) != "false" {
		return AttentionEvent{}, waitInvalid()
	}
	for _, read := range []func() error{
		func() (err error) { event.ID, err = m.digest("id"); return },
		func() (err error) { event.Resource, err = m.id("resource"); return },
		func() (err error) { event.Wait, err = m.id("wait"); return },
		func() (err error) { event.Reason, err = m.enum("reason", waitReasons...); return },
		func() (err error) {
			event.Needs, err = m.enum("needs", "approval-decision", "budget-grant", "question-answer", "reauthorization")
			return
		},
		func() (err error) { event.Condition, err = ReadWaitCondition(m["condition"]); return },
		func() (err error) { event.At, err = m.time("at"); return },
	} {
		if err := read(); err != nil {
			return AttentionEvent{}, err
		}
	}
	if m.has("subject") {
		subject, err := m.id("subject")
		if err != nil {
			return AttentionEvent{}, err
		}
		event.Subject = &subject
	}
	if m.has("timeoutAt") {
		at, err := m.time("timeoutAt")
		if err != nil {
			return AttentionEvent{}, err
		}
		event.TimeoutAt = &at
	}
	if m.has("deadline") {
		at, err := m.time("deadline")
		if err != nil || waitTimeMillis(at) <= waitTimeMillis(event.At) {
			return AttentionEvent{}, waitInvalid()
		}
		event.Deadline = &at
	}
	if event.ID != WaitAttentionID(event.Resource, event.Wait) || event.Needs != waitNeeds[event.Reason] || (event.Reason == "budget") != (event.Subject == nil) {
		return AttentionEvent{}, waitInvalid()
	}
	return event, nil
}

// ReadAttentionDelivery reads a version 1 delivery attempt.
func ReadAttentionDelivery(raw []byte) (AttentionDelivery, error) {
	m, err := waitShape(raw, []string{"version", "attention", "delivery", "outcome", "at"}, nil)
	if err != nil {
		return AttentionDelivery{}, err
	}
	if err := m.version(); err != nil {
		return AttentionDelivery{}, err
	}
	delivery := AttentionDelivery{Version: WaitRecordVersion}
	if delivery.Attention, err = m.digest("attention"); err != nil {
		return AttentionDelivery{}, err
	}
	if delivery.Delivery, err = m.id("delivery"); err != nil {
		return AttentionDelivery{}, err
	}
	if delivery.Outcome, err = m.enum("outcome", "sent", "failed"); err != nil {
		return AttentionDelivery{}, err
	}
	if delivery.At, err = m.time("at"); err != nil {
		return AttentionDelivery{}, err
	}
	return delivery, nil
}
func readWaitContent(raw json.RawMessage) (contract.RunWaitContent, error) {
	m, err := waitShape(raw, []string{"state", "digest"}, []string{"question", "answer"})
	if err != nil {
		return contract.RunWaitContent{}, err
	}
	content := contract.RunWaitContent{}
	if content.State, err = m.enum("state", "available", "redacted", "expired"); err != nil {
		return content, err
	}
	if content.Digest, err = m.digest("digest"); err != nil {
		return content, err
	}
	if content.State != "available" {
		if m.has("question") || m.has("answer") {
			return content, waitInvalid()
		}
		return content, nil
	}
	if m.has("question") == m.has("answer") {
		return content, waitInvalid()
	}
	if m.has("question") {
		question, err := readWaitQuestion(m["question"])
		if err != nil || WaitQuestionDigest(question) != content.Digest {
			return content, waitInvalid()
		}
		content.Question = &question
		return content, nil
	}
	answer, err := readWaitAnswer(m["answer"], nil)
	if err != nil || WaitAnswerDigest(answer) != content.Digest {
		return content, waitInvalid()
	}
	content.Answer = &answer
	return content, nil
}

var waitEntryMembers = map[string][]string{"park": {"owner", "epoch", "subject", "condition", "timeoutAt", "deadline", "release", "content"}, "wake": {"evidence", "wake"}, "timeout": {"evidence", "wake"}, "answer": {"actor", "content"}, "unanswered": {"content"}, "resume": {"owner", "epoch"}, "deadline": {"evidence", "wake"}}

func readWaitEntry(raw json.RawMessage) (WaitReportEntry, error) {
	probe, err := waitShape(raw, []string{"wait", "decision", "reason", "at"}, []string{"owner", "epoch", "evidence", "wake", "actor", "subject", "condition", "timeoutAt", "deadline", "release", "content"})
	if err != nil {
		return WaitReportEntry{}, err
	}
	decision, err := probe.enum("decision", "park", "wake", "timeout", "answer", "unanswered", "resume", "deadline")
	if err != nil {
		return WaitReportEntry{}, err
	}
	m, err := waitShape(raw, []string{"wait", "decision", "reason", "at"}, waitEntryMembers[decision])
	if err != nil {
		return WaitReportEntry{}, err
	}
	entry := WaitReportEntry{Decision: decision}
	if entry.Wait, err = m.id("wait"); err != nil {
		return entry, err
	}
	if entry.Reason, err = m.enum("reason", waitReasons...); err != nil {
		return entry, err
	}
	if entry.At, err = m.time("at"); err != nil {
		return entry, err
	}
	optional := func(name string, read func(string) (string, error)) (*string, error) {
		value, err := read(name)
		if err != nil {
			return nil, err
		}
		return &value, nil
	}
	if decision == "park" || decision == "resume" {
		if entry.Owner, err = optional("owner", m.id); err != nil {
			return entry, err
		}
		if entry.Epoch, err = optional("epoch", m.epoch); err != nil {
			return entry, err
		}
	}
	if decision == "park" {
		if m.has("subject") {
			if entry.Subject, err = optional("subject", m.id); err != nil {
				return entry, err
			}
		}
		condition, err := ReadWaitCondition(m["condition"])
		if err != nil {
			return entry, err
		}
		entry.Condition = &condition
		if m.has("timeoutAt") {
			if entry.TimeoutAt, err = optional("timeoutAt", m.time); err != nil {
				return entry, err
			}
		}
		if m.has("deadline") {
			if entry.Deadline, err = optional("deadline", m.time); err != nil {
				return entry, err
			}
		}
		if entry.Release, err = optional("release", func(name string) (string, error) { return m.enum(name, "clean", "uncertain") }); err != nil {
			return entry, err
		}
		if (entry.Reason == "question") != m.has("content") {
			return entry, waitInvalid()
		}
	}
	if decision == "wake" || decision == "timeout" || decision == "deadline" {
		if entry.Evidence, err = optional("evidence", m.id); err != nil {
			return entry, err
		}
		if entry.Wake, err = optional("wake", func(name string) (string, error) { return m.enum(name, "event", "time", "timeout", "deadline") }); err != nil {
			return entry, err
		}
		if (decision == "timeout") != (*entry.Wake == "timeout") || (decision == "deadline") != (*entry.Wake == "deadline") {
			return entry, waitInvalid()
		}
	}
	if (decision == "answer" || decision == "unanswered") && entry.Reason != "question" {
		return entry, waitInvalid()
	}
	if decision == "answer" {
		if entry.Actor, err = optional("actor", m.id); err != nil {
			return entry, err
		}
	}
	if m.has("content") {
		content, err := readWaitContent(m["content"])
		if err != nil || decision == "answer" && content.Question != nil || decision != "answer" && content.Answer != nil {
			return entry, waitInvalid()
		}
		entry.Content = &content
	} else if decision == "answer" || decision == "unanswered" {
		return entry, waitInvalid()
	}
	return entry, nil
}

// ReadWaitReport reads a version 1 park/wake report.
func ReadWaitReport(raw []byte) (WaitReport, error) {
	m, err := waitShape(raw, []string{"version", "resource", "entries"}, nil)
	if err != nil {
		return WaitReport{}, err
	}
	if err := m.version(); err != nil {
		return WaitReport{}, err
	}
	report := WaitReport{Version: WaitRecordVersion, Entries: []WaitReportEntry{}}
	if report.Resource, err = m.id("resource"); err != nil {
		return WaitReport{}, err
	}
	items, err := m.list("entries", 4096)
	if err != nil {
		return WaitReport{}, err
	}
	for _, item := range items {
		entry, err := readWaitEntry(item)
		if err != nil {
			return WaitReport{}, err
		}
		report.Entries = append(report.Entries, entry)
	}
	return report, nil
}

// ReadWaitExport reads the retained wait records of one run: every record is this run's, each wait has
// exactly its one attention event in order, and a delivery names one of them.
func ReadWaitExport(raw []byte) (WaitExport, error) {
	m, err := waitShape(raw, []string{"version", "resource", "waits", "attention", "deliveries"}, nil)
	if err != nil {
		return WaitExport{}, err
	}
	if err := m.version(); err != nil {
		return WaitExport{}, err
	}
	out := WaitExport{Version: WaitRecordVersion, Waits: []RunWait{}, Attention: []AttentionEvent{}, Deliveries: []AttentionDelivery{}}
	if out.Resource, err = m.id("resource"); err != nil {
		return WaitExport{}, err
	}
	waits, err := m.list("waits", WaitExportLimit)
	if err != nil {
		return WaitExport{}, err
	}
	attention, err := m.list("attention", WaitExportLimit)
	if err != nil {
		return WaitExport{}, err
	}
	deliveries, err := m.list("deliveries", 4096)
	if err != nil {
		return WaitExport{}, err
	}
	seen := map[string]bool{}
	for _, item := range waits {
		wait, err := ReadRunWait(item)
		if err != nil {
			return WaitExport{}, err
		}
		if wait.Resource != out.Resource || seen[wait.Wait] {
			return WaitExport{}, waitInvalid()
		}
		seen[wait.Wait] = true
		out.Waits = append(out.Waits, wait)
	}
	if len(attention) != len(out.Waits) {
		return WaitExport{}, waitInvalid()
	}
	for i, item := range attention {
		event, err := ReadAttentionEvent(item)
		if err != nil {
			return WaitExport{}, err
		}
		if event.Resource != out.Resource || event.ID != out.Waits[i].Attention {
			return WaitExport{}, waitInvalid()
		}
		out.Attention = append(out.Attention, event)
	}
	for _, item := range deliveries {
		delivery, err := ReadAttentionDelivery(item)
		if err != nil {
			return WaitExport{}, err
		}
		if !slices.ContainsFunc(out.Attention, func(event AttentionEvent) bool { return event.ID == delivery.Attention }) {
			return WaitExport{}, waitInvalid()
		}
		out.Deliveries = append(out.Deliveries, delivery)
	}
	return out, nil
}
func readWaitReason(raw []byte, reasons []string) (string, error) {
	m, err := waitShape(raw, []string{"version", "reason"}, nil)
	if err != nil {
		return "", err
	}
	if err := m.version(); err != nil {
		return "", err
	}
	return m.enum("reason", reasons...)
}

// ReadWaitRefusal reads a version 1 refusal record.
func ReadWaitRefusal(raw []byte) (contract.RunWaitRefusal, error) {
	reason, err := readWaitReason(raw, WaitRefusalReasons)
	return contract.RunWaitRefusal{Version: WaitRecordVersion, Reason: reason}, err
}

// ReadWaitCapabilityRefusal reads a version 1 capability refusal record.
func ReadWaitCapabilityRefusal(raw []byte) (contract.RunWaitCapabilityRefusal, error) {
	reason, err := readWaitReason(raw, WaitCapabilityReasons)
	return contract.RunWaitCapabilityRefusal{Version: WaitRecordVersion, Reason: reason}, err
}

// AttentionFor is the one needs_attention event of a committed park.
func AttentionFor(wait RunWait) AttentionEvent {
	event := AttentionEvent{Version: WaitRecordVersion, Type: "needs_attention", ID: wait.Attention, Resource: wait.Resource, Wait: wait.Wait, Reason: wait.Reason, Needs: waitNeeds[wait.Reason], Condition: cloneWaitCondition(wait.Condition), At: wait.Parked.At}
	switch {
	case wait.Pending.Approval != nil:
		subject := wait.Pending.Approval.CallID
		event.Subject = &subject
	case wait.Pending.Question != nil:
		subject := wait.Pending.Question.Event
		event.Subject = &subject
	case wait.Pending.Reauthorization != nil:
		subject := wait.Pending.Reauthorization.Connection
		event.Subject = &subject
	}
	if wait.TimeoutAt != nil {
		at := *wait.TimeoutAt
		event.TimeoutAt = &at
	}
	if wait.Deadline != nil {
		at := wait.Deadline.At
		event.Deadline = &at
	}
	for _, release := range wait.Release {
		event.ReleaseUncertain = event.ReleaseUncertain || release.Outcome == "uncertain"
	}
	return event
}
func cloneWaitCondition(c WaitCondition) WaitCondition {
	out := WaitCondition{}
	if c.Event != nil {
		event := *c.Event
		out.Event = &event
	}
	if c.WakeAt != nil {
		at := *c.WakeAt
		out.WakeAt = &at
	}
	return out
}

// cloneRunWait is a deep copy through the record's own encoding.
func cloneRunWait(wait RunWait) RunWait {
	raw, err := json.Marshal(wait)
	if err != nil {
		panic(err)
	}
	var out RunWait
	if err := json.Unmarshal(raw, &out); err != nil {
		panic(err)
	}
	if out.Release == nil {
		out.Release = []WaitRelease{}
	}
	return out
}

// WaitClaim is a resume's claim: the wake whose acknowledgement the host holds, the lease the
// continuation takes (against the parked revision), the host's reconciliation of each uncertain
// release, and the revalidation it was claimed after.
type WaitClaim struct {
	Resource     string
	Wait         string
	Evidence     string
	Lease        LeaseAcquire
	Reconciled   []WaitRelease
	Revalidation Revalidation
	// Reauthorized (#226): a reauthorization wait's claim carries the refresh it continues after; no other
	// claim does.
	Reauthorized *Reauthorized
}

// WaitStore is the wait port a store kind implements beside its lease port. Every transition is atomic
// with the store's commits and checked against its own clock, and everything it decides by survives the
// store's restarts: the waits, their attention events and deliveries, each run's earliest deadline, and
// an approved call not yet settled with its expiry and binding. Until a wait is resumed it holds its run:
// the store's AcquireLease refuses the run (wait-pending, or closed once a wait ended terminally), so does
// its unfenced Commit (#266 M4), and only ResumeWait leases it. From a run's deadline every lease of the
// run expires no later than that deadline, and AcquireLease and an unfenced Commit refuse deadline-passed.
// The package checks none of this in a host store; CheckRunStoreWaitConformance does.
type WaitStore interface {
	WaitVersion() int
	// WaitLifetime is durable when waits and attention events survive the store's process, process when
	// they end with it (the memory kind).
	WaitLifetime() string
	// ParkWait is one transaction: check the fence and the run head (checkpoint-changed), refuse while a
	// wait not yet resumed holds the run (wait-pending; closed after a terminal one), stamp the deadline
	// from the limits, the run's earliest deadline and the store clock (deadline-passed when it is not
	// after the park), refuse a WakeKeyDigest any other wait of the store holds (key-reused), commit the
	// wait and its one needs_attention event, and release the lease. A repeat of the same park under the
	// same fence returns it; anything else under its wait id is conflict. A refused park changes nothing
	// and leaves the lease held. #226: a reauthorization park also refuses a disposition minted under
	// another fence (stale) and a head that does not hold its operation as the never-admitted dispatch
	// (unbound, as HoldsNeverAdmitted decides), and, when the run's approved call is unsettled, the store
	// attaches that approval's binding to the wait and stamps its expiry as the deadline's approval time,
	// from its own record and never from the park. It records the attention event needing reauthorization
	// and naming the connection, and keeps the disposition and the attached approval across its restarts.
	// The attached approval is no part of a repeated park's comparison.
	ParkWait(context.Context, RunWait, LeaseFence) (RunWait, AttentionEvent, error)
	// WakeWait: the first valid wake wins, decided against the named wait only; a duplicate returns the
	// retained disposition; anything else changes nothing, and a resumed wait is never woken again. At or
	// after the wait's deadline or the run's only a deadline wake is accepted, and it ends the wait
	// terminally (expired). A resumed approval wait whose approved call has not settled is ended by a
	// deadline wake at the approval's expiry (#266 N1): expired, its outcome unknown. #226: a reauthorization
	// wait's timeout or deadline wake is terminal, as an approval wait's is: its run is closed for good.
	WakeWait(context.Context, WakeEvidence) (WakeDisposition, error)
	// ResumeWait is one transaction: check the acknowledged wake, the deadline, the head and the release
	// reconciliation, then lease the run (the only lease a held run is granted) and claim the
	// continuation once; a ceiling of the run not after the store's time refuses it (deadline-passed).
	// The lease ends no later than the run's deadline and any unsettled approval's expiry. From an
	// approval wait's claim until its approved call settles, no lease of the run (the claim's, a
	// takeover's or a re-acquired one) is granted, renewed or held past the approval's expiry, and every
	// checkpoint the run commits, fenced or not, must carry exactly the reviewed call (approval-changed
	// otherwise); the commit that records the call executed, refused or answered settles it in the same
	// transaction. ResumeParked checks what this returns. #226: a reauthorization wait's claim carries the
	// refresh and no other claim does. It refuses a claim with none (decision-required), for another
	// principal than the disposition's (identity-changed), taken before the park or after the store's time
	// (stale), or whose permissions do not cover what the operation requires (permission-insufficient),
	// changing nothing, and it records the refresh (Reauthorized) and keeps it across its restarts. The
	// lease ends no later than the attached approval's expiry too.
	ResumeWait(context.Context, WaitClaim) (RunWait, RunLease, error)
	// LimitRun sets the run's deadline; the earliest one the store holds wins, a held lease is shortened
	// to it, and every wait of the run not yet resumed or ended is stamped again with it (#266 M1). #226:
	// stamping a reauthorization wait again keeps its attached approval's time.
	LimitRun(context.Context, RunLimit) (RunLimit, error)
	ReadWait(ctx context.Context, resource, wait string) (*RunWait, error)
	// ExportWaits is the run's waits in park order, with their attention events and deliveries: the
	// report's source.
	ExportWaits(ctx context.Context, resource string) (WaitExport, error)
	// RecordDelivery records a delivery attempt apart from the event. A repeat delivery id returns the
	// first record.
	RecordDelivery(ctx context.Context, attention, delivery, outcome string) (AttentionDelivery, error)
	// RedactWait removes the question and answer content; every identity and digest is kept.
	RedactWait(ctx context.Context, resource, wait string) (RunWait, error)
}

// WaitRunStore is a lease run store with the wait port.
type WaitRunStore interface {
	LeaseRunStore
	WaitStore
}

// waitApproval is an approved call not yet settled: its expiry bounds every lease of the run, and every
// checkpoint the run commits must carry exactly its binding.
type waitApproval struct {
	at      int64
	binding ApprovalBinding
	wait    string
}

// WaitTable holds the wait transitions in memory. MemoryRunStore calls it under its own lock; a host
// store kind implements the same transitions against its own durable rows. It is not safe for
// concurrent use on its own.
type WaitTable struct {
	rows       map[string][]*RunWait
	events     map[string][]AttentionEvent
	deliveries map[string][]AttentionDelivery
	// #225: each run's earliest deadline, and the expiry and binding of an approved call not yet settled.
	ceilings  map[string]int64
	approvals map[string]waitApproval
	now       func() time.Time
}

func NewWaitTable(now func() time.Time) *WaitTable {
	if now == nil {
		now = time.Now
	}
	return &WaitTable{rows: map[string][]*RunWait{}, events: map[string][]AttentionEvent{}, deliveries: map[string][]AttentionDelivery{}, ceilings: map[string]int64{}, approvals: map[string]waitApproval{}, now: now}
}
func (t *WaitTable) stamp() (string, error) {
	at := t.now()
	if at.IsZero() {
		return "", waitInvalid()
	}
	return leaseExpiryAt(at.UnixMilli())
}
func (t *WaitTable) find(resource, wait string) *RunWait {
	for _, row := range t.rows[resource] {
		if row.Wait == wait {
			return row
		}
	}
	return nil
}
func (t *WaitTable) Read(resource, wait string) (*RunWait, error) {
	if !validStorageID(resource) || !validStorageID(wait) {
		return nil, waitInvalid()
	}
	row := t.find(resource, wait)
	if row == nil {
		return nil, nil
	}
	held := cloneRunWait(*row)
	return &held, nil
}
func waitHeadMatches(head *RunHead, binding contract.RunWaitCheckpointBinding) bool {
	return head != nil && head.Checkpoint != nil && head.Revision == binding.Revision && CheckpointBytesDigest(head.Checkpoint) == binding.Digest
}
func (t *WaitTable) attentionOf(resource, id string) AttentionEvent {
	for _, event := range t.events[resource] {
		if event.ID == id {
			return event
		}
	}
	return AttentionEvent{}
}

// terminalWait: a terminal wait ends its run, a timed-out approval or budget wait, or one past its
// deadline.
func terminalWait(row *RunWait) bool {
	return row.State == "expired" || row.State == "timed-out" && row.Reason != "question"
}

// holds refuses while a wait of the run is not yet resumed: closed for good after a terminal one.
func (t *WaitTable) holds(resource string) error {
	rows := t.rows[resource]
	if slices.ContainsFunc(rows, terminalWait) {
		return waitRefused("closed")
	}
	if slices.ContainsFunc(rows, func(row *RunWait) bool { return row.State != "resumed" }) {
		return waitRefused("wait-pending")
	}
	return nil
}

// Ceiling is the latest any lease of the run may expire, in store-clock milliseconds, or nil for no
// limit: its deadline and, until the approved call settles, the approval's expiry (#225 H1: every
// lease, not only the claim's, so neither a takeover nor a re-acquired lease outlives the approval).
func (t *WaitTable) Ceiling(resource string) *int64 {
	var limit *int64
	if held, ok := t.ceilings[resource]; ok {
		limit = &held
	}
	if held, ok := t.approvals[resource]; ok && (limit == nil || held.at < *limit) {
		at := held.at
		limit = &at
	}
	return limit
}

// CommitApproval (#225 H1 and M3): while the run's approved call is unsettled, every checkpoint it
// commits must carry exactly the call the person reviewed (its call id, the digest of the call and its
// arguments, and its source), or the commit is refused approval-changed; nothing else can be claimed or
// dispatched as that call. It reports whether the checkpoint records the call executed, refused or
// answered: that commit settles it, and the approval's expiry stops bounding the run.
func (t *WaitTable) CommitApproval(resource string, checkpoint []byte) (bool, error) {
	held, ok := t.approvals[resource]
	if !ok || checkpoint == nil {
		return false, nil
	}
	var members struct {
		Approval json.RawMessage `json:"approval"`
	}
	var approval contract.CheckpointApproval
	if json.Unmarshal(checkpoint, &members) != nil || len(members.Approval) == 0 || json.Unmarshal(members.Approval, &approval) != nil {
		return false, resumeRefused("approval-changed")
	}
	binding, err := ApprovalBindingOf(approval)
	if err != nil || binding != held.binding {
		return false, resumeRefused("approval-changed")
	}
	return approval.State == "executed" || approval.State == "refused" || approval.State == "answered", nil
}

// SettleApproval lifts a settled approval's expiry from the run.
func (t *WaitTable) SettleApproval(resource string) { delete(t.approvals, resource) }

// passed (#266 M1): whether the run's deadline the store holds is at or before now (equal time is past).
func (t *WaitTable) passed(resource, now string) bool {
	ceiling, ok := t.ceilings[resource]
	return ok && waitTimeMillis(now) >= ceiling
}

// AdmitAcquire (#265 M2): a committed park closes the run. It refuses acquiring the run's lease while a
// wait not yet resumed holds it, for good once a wait ended terminally, and from the run's deadline.
func (t *WaitTable) AdmitAcquire(resource string) error {
	rows := t.rows[resource]
	if slices.ContainsFunc(rows, terminalWait) {
		return waitRefused("closed")
	}
	// #225: from the run's deadline, or an unsettled approval's expiry, nothing leases the run; equal
	// time is past.
	if ceiling := t.Ceiling(resource); ceiling != nil {
		now := t.now()
		if now.IsZero() {
			return waitInvalid()
		}
		if now.UnixMilli() >= *ceiling {
			return waitRefused("deadline-passed")
		}
	}
	if slices.ContainsFunc(rows, func(row *RunWait) bool { return row.State != "resumed" }) {
		return waitRefused("wait-pending")
	}
	return nil
}

// Limit sets the run's deadline: the earliest one held wins.
func (t *WaitTable) Limit(input RunLimit) (RunLimit, error) {
	raw, err := json.Marshal(input)
	if err != nil {
		return RunLimit{}, waitInvalid()
	}
	limit, err := ReadRunLimit(raw)
	if err != nil {
		return RunLimit{}, asWait(err)
	}
	next := waitTimeMillis(limit.Run)
	if held, ok := t.ceilings[limit.Resource]; ok && held < next {
		next = held
	}
	t.ceilings[limit.Resource] = next
	// #266 M1: a run deadline set after a park reaches the run's waits. Each wait not yet resumed or ended
	// is stamped again with it, so no wake, answer, decision or claim is accepted from it and its deadline
	// wake is due at it; a deadline before the park is stamped as the earliest a committed park can carry,
	// a millisecond after it.
	for _, row := range t.rows[limit.Resource] {
		if row.State == "resumed" || terminalWait(row) {
			continue
		}
		parked := waitTimeMillis(row.Parked.At)
		ceiling := max(next, parked+1)
		var approvalAt *int64
		if row.Reason == "reauthorization" && row.Deadline != nil && row.Deadline.Approval != nil {
			at := waitTimeMillis(*row.Deadline.Approval)
			approvalAt = &at
		}
		deadline, err := stampDeadlineWithin(row.Limits, parked, &ceiling, approvalAt)
		if err != nil {
			return RunLimit{}, asWait(err)
		}
		row.Deadline = deadline
	}
	run, _ := leaseExpiryAt(next)
	return RunLimit{Version: ResumeRecordVersion, Resource: limit.Resource, Run: run}, nil
}

// Park commits the wait and its attention event and releases the lease, or changes nothing.
func (t *WaitTable) Park(input RunWait, fence LeaseFence, head *RunHead, leases *LeaseTable) (RunWait, AttentionEvent, error) {
	park, err := readWaitPark(input)
	if err != nil {
		return RunWait{}, AttentionEvent{}, err
	}
	if !validLeaseFence(fence) {
		return RunWait{}, AttentionEvent{}, leaseInvalid()
	}
	if fence.Resource != park.Resource {
		return RunWait{}, AttentionEvent{}, &LeaseRefused{Reason: "resource-mismatch"}
	}
	if existing := t.find(park.Resource, park.Wait); existing != nil {
		// A repeated park acknowledges the committed one: read, never parked twice.
		prior := cloneRunWait(*existing)
		prior.Parked, prior.Attention, prior.State, prior.Deadline, prior.Disposition, prior.Expired, prior.Resolution, prior.Resumed, prior.Revalidation, prior.Reconciled, prior.Redacted, prior.Reauthorized = contract.RunWaitActor{}, "", "", nil, nil, nil, nil, nil, nil, nil, nil, nil
		// #226: the approval binding the store attached to a reauthorization wait is not the park's.
		if prior.Reason == "reauthorization" {
			prior.Approval = nil
		}
		a, _ := canonicalRaw(prior)
		b, _ := canonicalRaw(park)
		if !bytes.Equal(a, b) || existing.Parked.Owner != fence.Owner || existing.Parked.Epoch != fence.Epoch {
			return RunWait{}, AttentionEvent{}, waitRefused("conflict")
		}
		return cloneRunWait(*existing), t.attentionOf(park.Resource, existing.Attention), nil
	}
	if _, err := leases.Check(fence); err != nil {
		return RunWait{}, AttentionEvent{}, err
	}
	// #265 M2: every wait not yet resumed holds the run, so one pause is never parked, and claimed, twice.
	if err := t.holds(park.Resource); err != nil {
		return RunWait{}, AttentionEvent{}, err
	}
	// #265 M1: a wake key belongs to one wait. A park whose key digest any other wait of the store holds
	// is refused.
	for _, rows := range t.rows {
		if slices.ContainsFunc(rows, func(row *RunWait) bool { return row.WakeKeyDigest == park.WakeKeyDigest }) {
			return RunWait{}, AttentionEvent{}, waitRefused("key-reused")
		}
	}
	if !waitHeadMatches(head, park.Checkpoint) {
		return RunWait{}, AttentionEvent{}, waitRefused("checkpoint-changed")
	}
	// #226: a reauthorization park presents the disposition minted under its own lease (the lease this park
	// releases, so the disposition parks once), for the never-admitted dispatch its head holds. The binding
	// of an approved call not yet settled is the store's to attach.
	var unsettled *waitApproval
	if disposition := park.Reauthorization; disposition != nil {
		if disposition.Fence.Owner != fence.Owner || disposition.Fence.Epoch != fence.Epoch || disposition.Fence.Resource != fence.Resource {
			return RunWait{}, AttentionEvent{}, reauthRefused("stale")
		}
		if !HoldsNeverAdmitted(head.Checkpoint, *disposition) {
			return RunWait{}, AttentionEvent{}, reauthRefused("unbound")
		}
		if held, ok := t.approvals[park.Resource]; ok {
			unsettled = &held
		}
	}
	at, err := t.stamp()
	if err != nil {
		return RunWait{}, AttentionEvent{}, err
	}
	// #225: the earliest applicable deadline wins, and this park's run limit never extends the one the
	// store holds.
	var runCeiling, approvalAt *int64
	if ceiling, ok := t.ceilings[park.Resource]; ok {
		runCeiling = &ceiling
	}
	if unsettled != nil {
		approvalAt = &unsettled.at
	}
	deadline, err := stampDeadlineWithin(park.Limits, waitTimeMillis(at), runCeiling, approvalAt)
	if err != nil {
		return RunWait{}, AttentionEvent{}, asWait(err)
	}
	if deadline != nil && waitTimeMillis(deadline.At) <= waitTimeMillis(at) {
		return RunWait{}, AttentionEvent{}, waitRefused("deadline-passed")
	}
	wait := cloneRunWait(park)
	if unsettled != nil {
		binding := unsettled.binding
		wait.Approval = &binding
	}
	wait.Deadline = deadline
	wait.Parked = contract.RunWaitActor{Owner: fence.Owner, Epoch: fence.Epoch, At: at}
	wait.Attention = WaitAttentionID(wait.Resource, wait.Wait)
	wait.State = "parked"
	attention := AttentionFor(wait)
	// #224: the lease is released in the same transaction that commits the wait and its attention event.
	if _, err := leases.Release(fence); err != nil {
		return RunWait{}, AttentionEvent{}, err
	}
	if deadline != nil && deadline.Run != nil {
		t.ceilings[wait.Resource] = waitTimeMillis(*deadline.Run)
	}
	held := wait
	t.rows[wait.Resource] = append(t.rows[wait.Resource], &held)
	t.events[wait.Resource] = append(t.events[wait.Resource], attention)
	return cloneRunWait(wait), attention, nil
}

// Wake: the first valid wake wins; a duplicate returns the retained disposition.
func (t *WaitTable) Wake(input WakeEvidence) (WakeDisposition, error) {
	evidence, err := checkWakeEvidence(input)
	if err != nil {
		return WakeDisposition{}, err
	}
	row := t.find(evidence.Resource, evidence.Wait)
	if row == nil {
		return WakeDisposition{}, waitRefused("no-wait")
	}
	if key, _ := WakeKeyDigest(evidence.Key); key != row.WakeKeyDigest {
		return WakeDisposition{}, waitRefused("unauthenticated")
	}
	digest, err := WakeEvidenceDigest(evidence)
	if err != nil {
		return WakeDisposition{}, err
	}
	if evidence.Kind == "deadline" {
		if row.Expired != nil {
			if row.Expired.Evidence != evidence.Evidence {
				return WakeDisposition{}, waitRefused("stale")
			}
			if row.Expired.Digest != digest {
				return WakeDisposition{}, waitRefused("conflict")
			}
			return *row.Expired, nil
		}
		// #266 N1: an approved call that has not settled by the approval's expiry ends its resumed wait
		// there. Its outcome is unknown (it may have been performed); nothing dispatches it, and the run is
		// closed for good.
		if unsettled, ok := t.approvals[row.Resource]; ok && row.State == "resumed" && unsettled.wait == row.Wait {
			now, err := t.stamp()
			if err != nil {
				return WakeDisposition{}, err
			}
			if waitTimeMillis(now) < unsettled.at {
				return WakeDisposition{}, waitRefused("not-due")
			}
			expired := WakeDisposition{Evidence: evidence.Evidence, Kind: "deadline", At: now, Digest: digest}
			row.Expired = &expired
			row.State = "expired"
			delete(t.approvals, row.Resource)
			return expired, nil
		}
		// A resumed wait whose call settled, or one that already ended, has nothing left for its deadline
		// to end.
		if row.State == "resumed" || terminalWait(row) {
			return WakeDisposition{}, waitRefused("stale")
		}
		if row.Deadline == nil {
			return WakeDisposition{}, waitRefused("mismatch")
		}
		now, err := t.stamp()
		if err != nil {
			return WakeDisposition{}, err
		}
		// Equal time is due.
		if waitTimeMillis(now) < waitTimeMillis(row.Deadline.At) {
			return WakeDisposition{}, waitRefused("not-due")
		}
		expired := WakeDisposition{Evidence: evidence.Evidence, Kind: "deadline", At: now, Digest: digest}
		row.Expired = &expired
		row.State = "expired"
		// A question its deadline ends before any answer is unanswered; the recommended option is never
		// chosen.
		if row.Reason == "question" && row.Resolution == nil {
			row.Resolution = &contract.RunWaitResolution{Kind: "unanswered", At: now}
		}
		return expired, nil
	}
	if row.Disposition != nil {
		if row.Disposition.Evidence != evidence.Evidence {
			return WakeDisposition{}, waitRefused("stale")
		}
		if row.Disposition.Digest != digest {
			return WakeDisposition{}, waitRefused("conflict")
		}
		return *row.Disposition, nil
	}
	if row.Expired != nil {
		return WakeDisposition{}, waitRefused("stale")
	}
	now, err := t.stamp()
	if err != nil {
		return WakeDisposition{}, err
	}
	// #225: late evidence changes nothing. From the deadline only the deadline wake ends the wait; from
	// the timeout only the timeout does: an answer or a decision delivered after either is refused, equal
	// time included.
	if row.Deadline != nil && waitTimeMillis(now) >= waitTimeMillis(row.Deadline.At) || t.passed(row.Resource, now) {
		return WakeDisposition{}, waitRefused("deadline-passed")
	}
	if evidence.Kind != "timeout" && row.TimeoutAt != nil && waitTimeMillis(now) >= waitTimeMillis(*row.TimeoutAt) {
		return WakeDisposition{}, waitRefused("timed-out")
	}
	var question *contract.Question
	if row.Pending.Question != nil {
		question = row.Pending.Question.Question
	}
	if evidence.Kind == "event" {
		expected := row.Condition.Event
		if expected == nil || expected.Kind != evidence.Event.Kind || expected.Subject != evidence.Event.Subject {
			return WakeDisposition{}, waitRefused("mismatch")
		}
		// An answer belongs to a question wait only, and must be a real answer to the retained question.
		if row.Reason == "question" {
			if question == nil || evidence.Answer == nil || !realAnswer(*evidence.Answer) {
				return WakeDisposition{}, waitRefused("mismatch")
			}
			raw, _ := json.Marshal(evidence.Answer)
			if _, err := readWaitAnswer(raw, question); err != nil {
				return WakeDisposition{}, waitRefused("mismatch")
			}
		} else if evidence.Answer != nil {
			return WakeDisposition{}, waitRefused("mismatch")
		}
	} else {
		due := row.Condition.WakeAt
		if evidence.Kind == "timeout" {
			due = row.TimeoutAt
		}
		if due == nil {
			return WakeDisposition{}, waitRefused("mismatch")
		}
		if waitTimeMillis(now) < waitTimeMillis(*due) {
			return WakeDisposition{}, waitRefused("not-due")
		}
	}
	disposition := WakeDisposition{Evidence: evidence.Evidence, Kind: evidence.Kind, At: now, Digest: digest}
	row.Disposition = &disposition
	row.State = "woken"
	if evidence.Kind == "timeout" {
		row.State = "timed-out"
	}
	if row.Reason == "question" {
		if evidence.Kind == "timeout" {
			row.Resolution = &contract.RunWaitResolution{Kind: "unanswered", At: now}
		} else {
			actor, answerDigest, answer := *evidence.Actor, WaitAnswerDigest(*evidence.Answer), *evidence.Answer
			answer.Selected = slices.Clone(answer.Selected)
			row.Resolution = &contract.RunWaitResolution{Kind: "answered", At: now, Actor: &actor, AnswerDigest: &answerDigest, Answer: &answer}
		}
	}
	return disposition, nil
}

// Resume checks the claim and, in the same transaction, leases the run and claims the continuation once.
func (t *WaitTable) Resume(claim WaitClaim, head *RunHead, leases *LeaseTable) (RunWait, RunLease, error) {
	if !validStorageID(claim.Resource) || !validStorageID(claim.Wait) || !validStorageID(claim.Evidence) {
		return RunWait{}, RunLease{}, waitInvalid()
	}
	reconciled := claim.Reconciled
	if reconciled == nil {
		reconciled = []WaitRelease{}
	}
	raw, _ := json.Marshal(reconciled)
	settled, err := readWaitReleaseList(raw)
	if err != nil {
		return RunWait{}, RunLease{}, err
	}
	revalidation, err := checkRevalidation(claim.Revalidation)
	if err != nil {
		return RunWait{}, RunLease{}, asWait(err)
	}
	row := t.find(claim.Resource, claim.Wait)
	if row == nil {
		return RunWait{}, RunLease{}, waitRefused("no-wait")
	}
	// An acknowledgement the store does not hold is reconciled by reading, never by dispatching the
	// continuation.
	if row.Disposition == nil || row.Disposition.Evidence != claim.Evidence {
		return RunWait{}, RunLease{}, waitRefused("unacknowledged")
	}
	if row.State == "resumed" {
		return RunWait{}, RunLease{}, waitRefused("already-resumed")
	}
	if claim.Lease.Resource != row.Resource {
		return RunWait{}, RunLease{}, &LeaseRefused{Reason: "resource-mismatch"}
	}
	if row.State == "timed-out" && row.Reason != "question" {
		return RunWait{}, RunLease{}, waitRefused("timed-out")
	}
	now, err := t.stamp()
	if err != nil {
		return RunWait{}, RunLease{}, err
	}
	// #225: nothing continues past the wait's deadline, an approval's expiry or the run's deadline; equal
	// time is past.
	if row.State == "expired" || row.Deadline != nil && waitTimeMillis(now) >= waitTimeMillis(row.Deadline.At) {
		return RunWait{}, RunLease{}, waitRefused("deadline-passed")
	}
	if !waitHeadMatches(head, row.Checkpoint) {
		return RunWait{}, RunLease{}, waitRefused("checkpoint-changed")
	}
	uncertain := []string{}
	for _, release := range row.Release {
		if release.Outcome == "uncertain" {
			uncertain = append(uncertain, release.Resource)
		}
	}
	for _, resource := range uncertain {
		if !slices.ContainsFunc(settled, func(s WaitRelease) bool { return s.Resource == resource && s.Outcome != "uncertain" }) {
			return RunWait{}, RunLease{}, waitRefused("release-uncertain")
		}
	}
	for _, s := range settled {
		if !slices.Contains(uncertain, s.Resource) {
			return RunWait{}, RunLease{}, waitRefused("release-uncertain")
		}
	}
	// #227: the claim records the revalidation of this wait's own pins.
	pins, err := PinsDigest(row.Pins)
	if err != nil || revalidation.Resource != row.Resource || revalidation.Wait != row.Wait || revalidation.Pins != pins {
		return RunWait{}, RunLease{}, waitInvalid()
	}
	// #226: a reauthorization wait continues only after a refresh for the same principal, taken after the
	// park and not after the store's time, whose permissions cover what the operation requires. A wake is
	// never one.
	var reauthorized *Reauthorized
	if (row.Reason == "reauthorization") != (claim.Reauthorized != nil) {
		if row.Reason == "reauthorization" {
			return RunWait{}, RunLease{}, waitRefused("decision-required")
		}
		return RunWait{}, RunLease{}, waitInvalid()
	}
	if claim.Reauthorized != nil {
		refresh, err := checkReauthorized(*claim.Reauthorized)
		if err != nil {
			return RunWait{}, RunLease{}, waitInvalid()
		}
		disposition := row.Reauthorization
		if refresh.Principal != disposition.Principal {
			return RunWait{}, RunLease{}, resumeRefused("identity-changed")
		}
		if waitTimeMillis(refresh.At) < waitTimeMillis(row.Parked.At) || waitTimeMillis(refresh.At) > waitTimeMillis(now) {
			return RunWait{}, RunLease{}, reauthRefused("stale")
		}
		if !permitted(disposition.Requires, refresh.Permissions) {
			return RunWait{}, RunLease{}, reauthRefused("permission-insufficient")
		}
		reauthorized = &refresh
	}
	// #265 M2: the claim is the one lease a held run is granted, taken in this transaction. Every ceiling
	// of the run bounds it (#266 N2: an earlier approval not yet settled too), and so does this wait's own
	// approval, which then bounds every later lease of the run until the approved call settles (#266 H1).
	// A ceiling not after the store's time refuses it.
	ceiling, approval := t.Ceiling(row.Resource), (*int64)(nil)
	if row.Reason == "approval" && row.Deadline != nil && row.Deadline.Approval != nil {
		at := waitTimeMillis(*row.Deadline.Approval)
		approval = &at
		if ceiling == nil || at < *ceiling {
			ceiling = approval
		}
	}
	if ceiling != nil && *ceiling <= waitTimeMillis(now) {
		return RunWait{}, RunLease{}, waitRefused("deadline-passed")
	}
	lease, err := leases.acquireWithin(claim.Lease, head.Revision, ceiling)
	if err != nil {
		return RunWait{}, RunLease{}, err
	}
	if approval != nil {
		t.approvals[row.Resource] = waitApproval{at: *approval, binding: *row.Approval, wait: row.Wait}
	}
	row.State = "resumed"
	row.Resumed = &contract.RunWaitActor{Owner: lease.Owner, Epoch: lease.Epoch, At: now}
	row.Revalidation = &revalidation
	if len(settled) > 0 {
		row.Reconciled = settled
	}
	row.Reauthorized = reauthorized
	return cloneRunWait(*row), lease, nil
}
func (t *WaitTable) Export(resource string) (WaitExport, error) {
	if !validStorageID(resource) {
		return WaitExport{}, waitInvalid()
	}
	out := WaitExport{Version: WaitRecordVersion, Resource: resource, Waits: []RunWait{}, Attention: []AttentionEvent{}, Deliveries: []AttentionDelivery{}}
	for _, row := range t.rows[resource] {
		out.Waits = append(out.Waits, cloneRunWait(*row))
	}
	for _, event := range t.events[resource] {
		event.Condition = cloneWaitCondition(event.Condition)
		out.Attention = append(out.Attention, event)
		out.Deliveries = append(out.Deliveries, t.deliveries[event.ID]...)
	}
	return out, nil
}
func (t *WaitTable) Deliver(attention, delivery, outcome string) (AttentionDelivery, error) {
	if !leaseDigestPattern.MatchString(attention) || !validStorageID(delivery) || outcome != "sent" && outcome != "failed" {
		return AttentionDelivery{}, waitInvalid()
	}
	known := false
	for _, events := range t.events {
		known = known || slices.ContainsFunc(events, func(e AttentionEvent) bool { return e.ID == attention })
	}
	if !known {
		return AttentionDelivery{}, waitRefused("no-wait")
	}
	for _, prior := range t.deliveries[attention] {
		if prior.Delivery == delivery {
			if prior.Outcome != outcome {
				return AttentionDelivery{}, waitRefused("conflict")
			}
			return prior, nil
		}
	}
	at, err := t.stamp()
	if err != nil {
		return AttentionDelivery{}, err
	}
	record := AttentionDelivery{Version: WaitRecordVersion, Attention: attention, Delivery: delivery, Outcome: outcome, At: at}
	t.deliveries[attention] = append(t.deliveries[attention], record)
	return record, nil
}
func (t *WaitTable) Redact(resource, waitID string) (RunWait, error) {
	if !validStorageID(resource) || !validStorageID(waitID) {
		return RunWait{}, waitInvalid()
	}
	row := t.find(resource, waitID)
	if row == nil {
		return RunWait{}, waitRefused("no-wait")
	}
	if row.Reason == "question" {
		row.Pending.Question.Question = nil
		if row.Resolution != nil {
			row.Resolution.Answer = nil
		}
		yes := true
		row.Redacted = &yes
	}
	return cloneRunWait(*row), nil
}

// MemoryRunStore's waits (#224) end with its process: WaitLifetime is process, so a park on it refuses
// store-lifetime. Every wait transition runs under the same lock as its commits, against the same head.
func (m *MemoryRunStore) WaitVersion() int     { return WaitRecordVersion }
func (m *MemoryRunStore) WaitLifetime() string { return WaitLifetimeProcess }
func (m *MemoryRunStore) headOf(resource string) *RunHead {
	held, ok := m.runs[resource]
	if !ok {
		return nil
	}
	head := held.head
	return &head
}
func (m *MemoryRunStore) ParkWait(ctx context.Context, park RunWait, fence LeaseFence) (wait RunWait, attention AttentionEvent, err error) {
	err = m.leaseTransition(ctx, func() error {
		wait, attention, err = m.waits.Park(park, fence, m.headOf(park.Resource), m.leases)
		return err
	})
	return wait, attention, err
}
func (m *MemoryRunStore) WakeWait(ctx context.Context, evidence WakeEvidence) (disposition WakeDisposition, err error) {
	err = m.leaseTransition(ctx, func() error { disposition, err = m.waits.Wake(evidence); return err })
	return disposition, err
}
func (m *MemoryRunStore) ResumeWait(ctx context.Context, claim WaitClaim) (held RunWait, lease RunLease, err error) {
	err = m.leaseTransition(ctx, func() error {
		held, lease, err = m.waits.Resume(claim, m.headOf(claim.Resource), m.leases)
		return err
	})
	return held, lease, err
}
func (m *MemoryRunStore) LimitRun(ctx context.Context, limit RunLimit) (held RunLimit, err error) {
	err = m.leaseTransition(ctx, func() error {
		if held, err = m.waits.Limit(limit); err == nil {
			m.leases.Lower(held.Resource, waitTimeMillis(held.Run))
		}
		return err
	})
	return held, err
}
func (m *MemoryRunStore) ReadWait(ctx context.Context, resource, wait string) (held *RunWait, err error) {
	err = m.leaseTransition(ctx, func() error { held, err = m.waits.Read(resource, wait); return err })
	return held, err
}
func (m *MemoryRunStore) ExportWaits(ctx context.Context, resource string) (out WaitExport, err error) {
	err = m.leaseTransition(ctx, func() error { out, err = m.waits.Export(resource); return err })
	return out, err
}
func (m *MemoryRunStore) RecordDelivery(ctx context.Context, attention, delivery, outcome string) (out AttentionDelivery, err error) {
	err = m.leaseTransition(ctx, func() error { out, err = m.waits.Deliver(attention, delivery, outcome); return err })
	return out, err
}
func (m *MemoryRunStore) RedactWait(ctx context.Context, resource, wait string) (out RunWait, err error) {
	err = m.leaseTransition(ctx, func() error { out, err = m.waits.Redact(resource, wait); return err })
	return out, err
}

func waitStoreOf(store RunStore) (WaitRunStore, error) {
	held, ok := store.(WaitRunStore)
	if !ok || held.WaitVersion() != WaitRecordVersion || held.LeaseVersion() != LeaseRecordVersion || store.Durability() == RunStoreSingleWriterCallback {
		return nil, &WaitCapabilityRefused{Reason: "store-unsupported"}
	}
	// A wait that ends with the store's process is not a durable wait.
	if held.WaitLifetime() != WaitLifetimeDurable || held.LeaseLifetime() != LeaseLifetimeDurable {
		return nil, &WaitCapabilityRefused{Reason: "store-lifetime"}
	}
	return held, nil
}

// AdmitWaits: durable waits need a store whose waits and leases outlive its process. Anything else
// refuses by name before a park.
func AdmitWaits(store RunStore) error { _, err := waitStoreOf(store); return err }

// LimitRun (#225) sets the run's deadline in its store. The earliest deadline the store holds wins; from
// it every lease of the run expires no later than it, so no commit, claim or effect admission is
// accepted at or after it.
func LimitRun(ctx context.Context, store RunStore, limit RunLimit) (RunLimit, error) {
	held, err := waitStoreOf(store)
	if err != nil {
		return RunLimit{}, err
	}
	raw, err := json.Marshal(limit)
	if err != nil {
		return RunLimit{}, waitInvalid()
	}
	if limit, err = ReadRunLimit(raw); err != nil {
		return RunLimit{}, asWait(err)
	}
	out, err := held.LimitRun(ctx, limit)
	if err != nil {
		return RunLimit{}, err
	}
	raw, _ = json.Marshal(out)
	out, err = ReadRunLimit(raw)
	return out, asWait(err)
}

// ParkInput is a host's park request.
type ParkInput struct {
	Store        RunStore
	Fence        LeaseFence
	Wait         string
	Continuation string
	Condition    WaitCondition
	TimeoutAt    *string
	// Model is the model the trusted host admitted the run with; pinned with the rest of the run's
	// execution authority.
	Model PinnedModel
	// Limits (#225) is the host's deadline policy. An approval wait needs ApprovalTtlMs; it is left out of
	// any other wait.
	Limits *WaitLimits
	// WakeKeyDigest is the SHA-256 of the host's wake key (WakeKeyDigest(key)); the key stays the host's.
	WakeKeyDigest string
	// Release is the host's evidence for each provider resource the paused run held. Uncertain evidence
	// is retained, never a clean park.
	Release []WaitRelease
	// Question is the retained user_question event a question wait is for.
	Question    string
	RetainUntil *string
	// Reauthorization (#226) is the trusted re-authorization refusal the run's dispatch ended in
	// (ReauthorizationOf on the fenced view the run used). It parks one reauthorization wait, under the same
	// lease it was minted under.
	Reauthorization *ReauthorizationRequired
}

func retainedQuestion(ctx context.Context, store RunStore, run, event string) (*contract.Question, error) {
	after := ""
	var found *contract.Question
	last := ""
	for {
		page, err := store.ReadEvents(ctx, run, after, StoragePageLimit)
		if err != nil {
			return nil, err
		}
		for _, item := range page.Items {
			stored, err := ReadStoredRunEvent(item.Bytes)
			if err != nil {
				return nil, err
			}
			if question := stored.Event.UserQuestion; question != nil {
				last = question.ID
				if question.ID == event {
					held := question.Question
					found = &held
				}
			}
		}
		if !page.HasMore || page.NextCursor == "" || page.NextCursor == after {
			break
		}
		after = page.NextCursor
	}
	// Only the run's latest question is pending; an earlier one was answered by a later turn.
	if last != event {
		return nil, nil
	}
	return found, nil
}

// ParkRun parks a paused run: it reads the pending decision from the retained checkpoint or event log,
// then commits the wait and its one needs_attention event and releases the lease in one store
// transaction.
func ParkRun(ctx context.Context, input ParkInput) (RunWait, AttentionEvent, error) {
	store, err := waitStoreOf(input.Store)
	if err != nil {
		return RunWait{}, AttentionEvent{}, err
	}
	if !validLeaseFence(input.Fence) {
		return RunWait{}, AttentionEvent{}, leaseInvalid()
	}
	head, err := store.Load(ctx, input.Fence.Resource)
	if err != nil {
		return RunWait{}, AttentionEvent{}, err
	}
	if head == nil || head.Checkpoint == nil {
		return RunWait{}, AttentionEvent{}, waitRefused("nothing-pending")
	}
	value, err := ParseJSON(head.Checkpoint)
	if err != nil {
		return RunWait{}, AttentionEvent{}, waitRefused("nothing-pending")
	}
	checkpoint, err := ReadExecution(value)
	if err != nil || checkpoint == nil {
		return RunWait{}, AttentionEvent{}, waitRefused("nothing-pending")
	}
	park := RunWait{Version: WaitRecordVersion, Resource: input.Fence.Resource, Wait: input.Wait, Condition: input.Condition, TimeoutAt: input.TimeoutAt, Continuation: input.Continuation, Checkpoint: contract.RunWaitCheckpointBinding{Revision: head.Revision, Digest: CheckpointBytesDigest(head.Checkpoint)}, WakeKeyDigest: input.WakeKeyDigest, Release: slices.Clone(input.Release), RetainUntil: input.RetainUntil}
	if input.Limits != nil {
		raw, err := json.Marshal(input.Limits)
		if err != nil {
			return RunWait{}, AttentionEvent{}, waitInvalid()
		}
		limits, err := ReadWaitLimits(raw)
		if err != nil {
			return RunWait{}, AttentionEvent{}, asWait(err)
		}
		park.Limits = &limits
	}
	if input.Reauthorization != nil {
		// #226: only a trusted, invocation-bound disposition parks a run for re-authorization: one the package
		// minted from the run's proven effect host during that live dispatch, not yet used, under this park's
		// own lease, for the never-admitted dispatch the head holds. Anything else keeps the operation's
		// unknown outcome and is reconciled.
		if input.Question != "" {
			return RunWait{}, AttentionEvent{}, waitInvalid()
		}
		disposition, trusted := reauthDispositionOf(input.Reauthorization)
		if disposition == nil {
			if trusted {
				return RunWait{}, AttentionEvent{}, reauthRefused("used")
			}
			return RunWait{}, AttentionEvent{}, reauthRefused("untrusted")
		}
		if disposition.Fence.Owner != input.Fence.Owner || disposition.Fence.Epoch != input.Fence.Epoch || disposition.Resource != input.Fence.Resource {
			return RunWait{}, AttentionEvent{}, reauthRefused("stale")
		}
		if !HoldsNeverAdmitted(head.Checkpoint, *disposition) {
			return RunWait{}, AttentionEvent{}, reauthRefused("unbound")
		}
		park.Reason = "reauthorization"
		park.Pending = contract.RunWaitPending{Reauthorization: &contract.RunWaitPendingReauthorization{Kind: "reauthorization", Operation: disposition.Operation.Operation, Source: disposition.Operation.Source, Tool: disposition.Operation.Tool, Connection: disposition.Connection, Cause: disposition.Cause}}
		park.Reauthorization = disposition
	} else if checkpoint.Status == "paused" {
		if input.Question != "" {
			return RunWait{}, AttentionEvent{}, waitInvalid()
		}
		asked := checkpoint.Approval != nil && checkpoint.Approval.State == "asked"
		// #265 M3, #266 M5: a pause no package input can resume is never parked, so it never closes its run.
		// A plan decision is no grant, and a provider-managed allowance takes none (extendExecution).
		if !asked && (checkpoint.PlanState != nil && checkpoint.PlanState.Pending != nil || checkpoint.Unit != "model-requests") {
			return RunWait{}, AttentionEvent{}, waitRefused("not-resumable")
		}
		if approval := checkpoint.Approval; asked {
			park.Reason = "approval"
			park.Pending = contract.RunWaitPending{Approval: &contract.RunWaitPendingApproval{Kind: "approval", CallID: approval.CallID, Name: approval.Name, Tool: approval.Tool}}
			// #225: an unattended approval never waits indefinitely; it is bound to the exact call the
			// person reviews.
			if park.Limits == nil || park.Limits.ApprovalTtlMs == nil {
				return RunWait{}, AttentionEvent{}, waitRefused("approval-ttl-required")
			}
			binding, err := ApprovalBindingOf(*approval)
			if err != nil {
				return RunWait{}, AttentionEvent{}, asWait(err)
			}
			park.Approval = &binding
		} else {
			park.Reason = "budget"
			park.Pending = contract.RunWaitPending{Budget: &contract.RunWaitPendingBudget{Kind: "budget", Unit: "model-requests", Used: checkpoint.Used, Limit: checkpoint.Limit}}
		}
	} else {
		if input.Question == "" {
			return RunWait{}, AttentionEvent{}, waitRefused("nothing-pending")
		}
		if !validStorageID(input.Question) {
			return RunWait{}, AttentionEvent{}, waitInvalid()
		}
		question, err := retainedQuestion(ctx, store, input.Fence.Resource, input.Question)
		if err != nil {
			return RunWait{}, AttentionEvent{}, err
		}
		if question == nil {
			return RunWait{}, AttentionEvent{}, waitRefused("nothing-pending")
		}
		park.Reason = "question"
		park.Pending = contract.RunWaitPending{Question: &contract.RunWaitPendingQuestion{Kind: "question", Event: input.Question, QuestionDigest: WaitQuestionDigest(*question), Question: question}}
	}
	if park.Approval == nil && park.Limits != nil && park.Limits.ApprovalTtlMs != nil {
		limits := *park.Limits
		limits.ApprovalTtlMs = nil
		park.Limits = &limits
		if limits.Run == nil && limits.WaitMs == nil {
			park.Limits = nil
		}
	}
	if park.Pins, err = pinsFromFor(checkpoint, input.Model, input.Reauthorization != nil); err != nil {
		return RunWait{}, AttentionEvent{}, asWait(err)
	}
	if park, err = readWaitPark(park); err != nil {
		return RunWait{}, AttentionEvent{}, err
	}
	wait, attention, err := store.ParkWait(ctx, park, input.Fence)
	if err == nil && input.Reauthorization != nil {
		input.Reauthorization.use()
	}
	return wait, attention, err
}

// WakeRun delivers host wake evidence to the store. A store error leaves the outcome unknown: read the
// wait to reconcile it.
func WakeRun(ctx context.Context, store RunStore, evidence WakeEvidence) (WakeDisposition, error) {
	held, err := waitStoreOf(store)
	if err != nil {
		return WakeDisposition{}, err
	}
	if evidence, err = checkWakeEvidence(evidence); err != nil {
		return WakeDisposition{}, err
	}
	return held.WakeWait(ctx, evidence)
}

// ResumeInput is a host's resume request.
type ResumeInput struct {
	Store    RunStore
	Resource string
	Wait     string
	// Evidence is the wake evidence id whose acknowledgement the host holds.
	Evidence   string
	Owner      string
	DurationMs int64
	// Approval: an approval wait continues only with the person's decision for its call; a wake is never
	// one.
	Approval *contract.CheckpointApprovalDecision
	// Grant: a budget wait continues only with the person's grant from its paused limit; a wake is never
	// one.
	Grant *contract.CheckpointGrant
	// Reconciled is the host's reconciliation of each uncertain release.
	Reconciled []WaitRelease
	// Current (#227) is the host's current evidence: what the continuation would run under now, with any
	// explicit admission of a changed model or reference. Without it nothing resumes.
	Current *ResumeEvidence
	// Reauthorization (#226): a reauthorization wait continues only with the host's or the person's
	// explicit action; a wake is never one.
	Reauthorization *ReauthAction
}

// ResumedWait is what a resume hands the host: the new lease and fence, the claimed wait, and the
// decision the run continues with.
type ResumedWait struct {
	Lease        RunLease
	Fence        LeaseFence
	Wait         RunWait
	Revalidation Revalidation
	Approval     *contract.CheckpointApprovalDecision
	Grant        *contract.CheckpointGrant
	Resolution   *contract.RunWaitResolution
	// Reauthorized (#226) is the refresh a reauthorization wait's claim recorded.
	Reauthorized *Reauthorized
}

func waitDecision(wait RunWait, input ResumeInput) error {
	switch wait.Reason {
	case "approval":
		decision := input.Approval
		if input.Grant != nil || input.Reauthorization != nil || decision == nil || decision.CallID != wait.Pending.Approval.CallID || decision.Note != nil && jsLength(*decision.Note) > 2000 {
			return waitRefused("decision-required")
		}
	case "budget":
		grant, pending := input.Grant, wait.Pending.Budget
		if input.Approval != nil || input.Reauthorization != nil || grant == nil || pending.Unit != "model-requests" || grant.From != pending.Limit || grant.To <= grant.From || grant.To > 100 {
			return waitRefused("decision-required")
		}
		// #265 M4: the grant is dated by a store-clock time, the form both writers produce, in both languages.
		if _, ok := leaseExpiryMillis(grant.At); !ok {
			return waitRefused("decision-required")
		}
	case "reauthorization":
		// #226: the host's or the person's action is checked against the wait (Reauthorize) after the
		// revalidation.
		if input.Approval != nil || input.Grant != nil || input.Reauthorization == nil {
			return waitRefused("decision-required")
		}
	default:
		// A question's continuation is its resolution; an approval, a grant or a re-authorization is not a
		// question's to take.
		if input.Approval != nil || input.Grant != nil || input.Reauthorization != nil {
			return waitRefused("decision-required")
		}
	}
	return nil
}

// ResumeParked resumes a woken wait: it checks the host's decision, revalidates the pinned execution
// authority against the host's current evidence (#227), then has the store check the deadline, the head
// and the release reconciliation, lease the run and claim the continuation once, in one transaction. The
// host then continues the run through the fenced store with the returned decision; nothing is dispatched
// before that claim.
func ResumeParked(ctx context.Context, input ResumeInput) (ResumedWait, error) {
	store, err := waitStoreOf(input.Store)
	if err != nil {
		return ResumedWait{}, err
	}
	if !validStorageID(input.Resource) || !validStorageID(input.Wait) {
		return ResumedWait{}, waitInvalid()
	}
	held, err := store.ReadWait(ctx, input.Resource, input.Wait)
	if err != nil {
		return ResumedWait{}, err
	}
	if held == nil {
		return ResumedWait{}, waitRefused("no-wait")
	}
	// #265 M6: the store's record is read through the reader, and it must be the wait asked for; a
	// malformed one is invalid, never dereferenced.
	wait, err := readStoreWait(*held)
	if err != nil || wait.Resource != input.Resource || wait.Wait != input.Wait {
		return ResumedWait{}, waitInvalid()
	}
	if wait.Disposition == nil || wait.Disposition.Evidence != input.Evidence {
		return ResumedWait{}, waitRefused("unacknowledged")
	}
	if wait.State == "resumed" {
		return ResumedWait{}, waitRefused("already-resumed")
	}
	if wait.State == "timed-out" && wait.Reason != "question" {
		return ResumedWait{}, waitRefused("timed-out")
	}
	if wait.State == "expired" {
		return ResumedWait{}, waitRefused("deadline-passed")
	}
	if err := waitDecision(wait, input); err != nil {
		return ResumedWait{}, err
	}
	if wait.Chat != nil {
		return ResumedWait{}, &WaitCapabilityRefused{Reason: "chat-store-unsupported"}
	}
	// #227: missing current evidence refuses; nothing of the continuation is claimed before the
	// revalidation passes.
	if input.Current == nil {
		return ResumedWait{}, resumeRefused("evidence-missing")
	}
	revalidation, err := RevalidateResume(RevalidatedWait{Resource: wait.Resource, Wait: wait.Wait, Reason: wait.Reason, Pins: wait.Pins, Approval: wait.Approval}, *input.Current)
	if err != nil {
		return ResumedWait{}, err
	}
	// #226: the identity, the action's time and the permissions, before anything is claimed.
	var reauthorized *Reauthorized
	if wait.Reason == "reauthorization" {
		refresh, err := Reauthorize(ReauthWait{Resource: wait.Resource, Wait: wait.Wait, ParkedAt: wait.Parked.At, Connection: wait.Pending.Reauthorization.Connection, Reauthorization: wait.Reauthorization}, *input.Reauthorization)
		if err != nil {
			return ResumedWait{}, err
		}
		reauthorized = &refresh
	}
	claimedWait, claimedLease, err := store.ResumeWait(ctx, WaitClaim{Resource: wait.Resource, Wait: wait.Wait, Evidence: input.Evidence, Lease: LeaseAcquire{Resource: wait.Resource, Owner: input.Owner, ExpectedRevision: wait.Checkpoint.Revision, DurationMs: input.DurationMs}, Reconciled: input.Reconciled, Revalidation: revalidation, Reauthorized: reauthorized})
	if err != nil {
		return ResumedWait{}, err
	}
	resumed, lease, err := readClaimed(claimedWait, claimedLease, wait, input.Owner, revalidation, reauthorized)
	if err != nil {
		return ResumedWait{}, err
	}
	out := ResumedWait{Lease: lease, Fence: FenceOf(lease), Wait: resumed, Revalidation: revalidation, Approval: input.Approval, Grant: input.Grant}
	if resumed.Reauthorized != nil {
		refresh := *cloneRunWait(resumed).Reauthorized
		out.Reauthorized = &refresh
	}
	if resumed.Resolution != nil {
		resolution := *cloneRunWait(resumed).Resolution
		out.Resolution = &resolution
	}
	return out, nil
}

// readStoreWait reads a wait a store returned through ReadRunWait.
func readStoreWait(held RunWait) (RunWait, error) {
	raw, err := json.Marshal(held)
	if err != nil {
		return RunWait{}, waitInvalid()
	}
	return ReadRunWait(raw)
}

// readClaimed (#266 M6) checks the store's claim before its fence is handed out. It must be this wait,
// resumed by the lease it returns, with the revalidation it was given, and that lease must be held by the
// claimer for this run and end no later than the run's deadline and an approval's expiry the wait records.
// A store that answers otherwise is refused invalid: nothing of the continuation is handed to the host.
func readClaimed(claimed RunWait, claimedLease RunLease, wait RunWait, owner string, revalidation Revalidation, reauthorized *Reauthorized) (RunWait, RunLease, error) {
	held, err := readStoreWait(claimed)
	if err != nil {
		return RunWait{}, RunLease{}, waitInvalid()
	}
	raw, err := json.Marshal(claimedLease)
	if err != nil {
		return RunWait{}, RunLease{}, waitInvalid()
	}
	lease, err := ReadRunLease(raw)
	if err != nil {
		return RunWait{}, RunLease{}, waitInvalid()
	}
	a, _ := canonicalRaw(held.Revalidation)
	b, _ := canonicalRaw(revalidation)
	expiry := waitTimeMillis(lease.Expiry)
	bounds := []*string{}
	if held.Deadline != nil {
		bounds = append(bounds, held.Deadline.Run)
		if held.Reason == "approval" {
			bounds = append(bounds, held.Deadline.Approval)
		}
	}
	beyond := held.Reason == "approval" && (held.Deadline == nil || held.Deadline.Approval == nil)
	// #226: the claim recorded the refresh it was given, and a reauthorization wait's claim is bounded by
	// the unsettled approval it carries.
	c, _ := canonicalRaw(held.Reauthorized)
	d, _ := canonicalRaw(reauthorized)
	beyond = beyond || !bytes.Equal(c, d)
	if held.Reason == "reauthorization" && held.Deadline != nil {
		bounds = append(bounds, held.Deadline.Approval)
	}
	for _, bound := range bounds {
		beyond = beyond || bound != nil && expiry > waitTimeMillis(*bound)
	}
	if held.Resource != wait.Resource || held.Wait != wait.Wait || held.State != "resumed" || held.Resumed == nil || held.Resumed.Owner != lease.Owner || held.Resumed.Epoch != lease.Epoch || !bytes.Equal(a, b) || held.Checkpoint != wait.Checkpoint ||
		lease.Resource != wait.Resource || lease.Owner != owner || lease.State != "held" || beyond {
		return RunWait{}, RunLease{}, waitInvalid()
	}
	return held, lease, nil
}

// ReportFromExport is the park/wake report of one run, read from its retained waits, never from a
// prompt. Content whose wait was redacted, or whose retainUntil is at or before now (the host's time),
// is unavailable with only its digest. A zero now expires nothing.
func ReportFromExport(source WaitExport, now time.Time) (WaitReport, error) {
	raw, err := json.Marshal(source)
	if err != nil {
		return WaitReport{}, waitInvalid()
	}
	held, err := ReadWaitExport(raw)
	if err != nil {
		return WaitReport{}, err
	}
	report := WaitReport{Version: WaitRecordVersion, Resource: held.Resource, Entries: []WaitReportEntry{}}
	str := func(s string) *string { return &s }
	for _, wait := range held.Waits {
		expired := !now.IsZero() && wait.RetainUntil != nil && waitTimeMillis(*wait.RetainUntil) <= now.UnixMilli()
		content := func(question *contract.Question, answer *contract.QuestionDraft, digest string) *contract.RunWaitContent {
			switch {
			case expired:
				return &contract.RunWaitContent{State: "expired", Digest: digest}
			case wait.Redacted != nil:
				return &contract.RunWaitContent{State: "redacted", Digest: digest}
			}
			return &contract.RunWaitContent{State: "available", Digest: digest, Question: question, Answer: answer}
		}
		release := "clean"
		for _, r := range wait.Release {
			if r.Outcome == "uncertain" {
				release = "uncertain"
			}
		}
		condition := cloneWaitCondition(wait.Condition)
		park := WaitReportEntry{Wait: wait.Wait, Decision: "park", Reason: wait.Reason, At: wait.Parked.At, Owner: str(wait.Parked.Owner), Epoch: str(wait.Parked.Epoch), Condition: &condition, TimeoutAt: wait.TimeoutAt, Release: &release}
		if wait.Deadline != nil {
			park.Deadline = str(wait.Deadline.At)
		}
		question := wait.Pending.Question
		switch {
		case wait.Pending.Approval != nil:
			park.Subject = str(wait.Pending.Approval.CallID)
		case question != nil:
			park.Subject = str(question.Event)
			park.Content = content(question.Question, nil, question.QuestionDigest)
		case wait.Pending.Reauthorization != nil:
			park.Subject = str(wait.Pending.Reauthorization.Connection)
		}
		report.Entries = append(report.Entries, park)
		if d := wait.Disposition; d != nil {
			decision := "wake"
			if d.Kind == "timeout" {
				decision = "timeout"
			}
			report.Entries = append(report.Entries, WaitReportEntry{Wait: wait.Wait, Decision: decision, Reason: wait.Reason, At: d.At, Evidence: str(d.Evidence), Wake: str(d.Kind)})
		}
		resolution := func() {
			if r := wait.Resolution; r != nil && question != nil {
				if r.Kind == "answered" {
					report.Entries = append(report.Entries, WaitReportEntry{Wait: wait.Wait, Decision: "answer", Reason: wait.Reason, At: r.At, Actor: r.Actor, Content: content(nil, r.Answer, *r.AnswerDigest)})
				} else {
					report.Entries = append(report.Entries, WaitReportEntry{Wait: wait.Wait, Decision: "unanswered", Reason: wait.Reason, At: r.At, Content: content(question.Question, nil, question.QuestionDigest)})
				}
			}
		}
		// #225: a deadline that ended the wait is reported after what woke it, and before the unanswered
		// end it caused; #266 N1: one that ended an unsettled approved call after its claim is reported after
		// the resume.
		deadline := func() {
			if e := wait.Expired; e != nil {
				report.Entries = append(report.Entries, WaitReportEntry{Wait: wait.Wait, Decision: "deadline", Reason: wait.Reason, At: e.At, Evidence: str(e.Evidence), Wake: str("deadline")})
			}
		}
		if wait.Disposition != nil {
			resolution()
		}
		if wait.Resumed == nil {
			deadline()
		}
		if wait.Disposition == nil {
			resolution()
		}
		if r := wait.Resumed; r != nil {
			report.Entries = append(report.Entries, WaitReportEntry{Wait: wait.Wait, Decision: "resume", Reason: wait.Reason, At: r.At, Owner: str(r.Owner), Epoch: str(r.Epoch)})
			deadline()
		}
	}
	out, err := json.Marshal(report)
	if err != nil {
		return WaitReport{}, waitInvalid()
	}
	return ReadWaitReport(out)
}

// ExportWaits is the retained wait records of one run, exactly as the store holds them.
func ExportWaits(ctx context.Context, store RunStore, resource string) (WaitExport, error) {
	held, ok := store.(WaitStore)
	if !ok || held.WaitVersion() != WaitRecordVersion {
		return WaitExport{}, &WaitCapabilityRefused{Reason: "store-unsupported"}
	}
	out, err := held.ExportWaits(ctx, resource)
	if err != nil {
		return WaitExport{}, err
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return WaitExport{}, waitInvalid()
	}
	return ReadWaitExport(raw)
}

// WaitReportOf reads the run's retained waits and reports them at the host's time.
func WaitReportOf(ctx context.Context, store RunStore, resource string, now time.Time) (WaitReport, error) {
	source, err := ExportWaits(ctx, store, resource)
	if err != nil {
		return WaitReport{}, err
	}
	return ReportFromExport(source, now)
}
