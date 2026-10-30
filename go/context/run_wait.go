package runcontext

// Durable waits, attention and the park/wake report (ADR-0025 decisions 2 and 6, #224 and #228;
// contract/schemas/run-wait.json). The package validates and records: a park reads the pending
// decision from the run's retained checkpoint or event log, and the store commits the wait and its one
// needs_attention event and releases the run's lease in one transaction. The host owns scheduling,
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

var WaitRefusalReasons = []string{"invalid", "nothing-pending", "wait-pending", "conflict", "checkpoint-changed", "no-wait", "unauthenticated", "mismatch", "not-due", "stale", "unacknowledged", "already-resumed", "decision-required", "timed-out", "release-uncertain", "chat-changed"}
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

var waitKeyPattern = regexp.MustCompile(`^[!-~]{16,512}$`)
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
		if err != nil || strings.TrimSpace(value) == "" {
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
		if err != nil || !waitOptionPattern.MatchString(id) || strings.TrimSpace(label) == "" || ids[id] || labels[strings.ToLower(strings.TrimSpace(label))] {
			return contract.Question{}, waitInvalid()
		}
		if _, err := o.text("description", 0, 240); err != nil {
			return contract.Question{}, err
		}
		ids[id], labels[strings.ToLower(strings.TrimSpace(label))] = true, true
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
	return len(draft.Selected) > 0 || strings.TrimSpace(draft.Text) != ""
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
	if !waitKeyPattern.MatchString(key) {
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
	probe, err := waitShape(raw, []string{"kind"}, []string{"callId", "name", "tool", "unit", "used", "limit", "event", "questionDigest", "question"})
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
		if budget.Unit, err = m.enum("unit", "model-requests", "provider-managed"); err != nil {
			return contract.RunWaitPending{}, err
		}
		if budget.Limit, err = m.integer("limit", 1, 100); err != nil {
			return contract.RunWaitPending{}, err
		}
		if budget.Used, err = m.integer("used", 0, budget.Limit); err != nil {
			return contract.RunWaitPending{}, err
		}
		return contract.RunWaitPending{Budget: &budget}, nil
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
	if d.Kind, err = m.enum("kind", "event", "time", "timeout"); err != nil {
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

var waitParkRequired = []string{"version", "resource", "wait", "reason", "pending", "condition", "continuation", "checkpoint", "wakeKeyDigest", "release"}
var waitParkOptional = []string{"timeoutAt", "chat", "retainUntil"}

func readWaitParkMembers(m waitMembers, redacted bool) (RunWait, error) {
	if err := m.version(); err != nil {
		return RunWait{}, err
	}
	wait := RunWait{Version: WaitRecordVersion}
	var err error
	if wait.Reason, err = m.enum("reason", "approval", "budget", "question"); err != nil {
		return RunWait{}, err
	}
	if wait.Condition, err = ReadWaitCondition(m["condition"]); err != nil {
		return RunWait{}, err
	}
	// A question waits for its answer event; a wake time would end it with no answer.
	if wait.Reason == "question" && (wait.Condition.Event == nil || wait.Condition.WakeAt != nil) {
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
	return wait, nil
}

// readWaitPark reads the store's input: a park before the store stamps who parked it, when, and its
// attention.
func readWaitPark(park RunWait) (RunWait, error) {
	if park.Parked != (contract.RunWaitActor{}) || park.Attention != "" || park.State != "" || park.Disposition != nil || park.Resolution != nil || park.Resumed != nil || park.Reconciled != nil || park.Redacted != nil {
		return RunWait{}, waitInvalid()
	}
	if park.Release == nil {
		park.Release = []WaitRelease{}
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
	return readWaitParkMembers(m, false)
}

// ReadRunWait reads a version 1 wait record and holds every rule the schema cannot state.
func ReadRunWait(raw []byte) (RunWait, error) {
	m, err := waitShape(raw, append(slices.Clone(waitParkRequired), "parked", "attention", "state"), append(slices.Clone(waitParkOptional), "disposition", "resolution", "resumed", "reconciled", "redacted"))
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
	wait, err := readWaitParkMembers(m, redacted)
	if err != nil {
		return RunWait{}, err
	}
	if wait.Parked, err = readWaitActor(m["parked"]); err != nil {
		return RunWait{}, err
	}
	if wait.Attention, err = m.digest("attention"); err != nil || wait.Attention != WaitAttentionID(wait.Resource, wait.Wait) {
		return RunWait{}, waitInvalid()
	}
	if wait.State, err = m.enum("state", "parked", "woken", "timed-out", "resumed"); err != nil {
		return RunWait{}, err
	}
	if wait.State == "parked" {
		if m.has("disposition") || m.has("resolution") || m.has("resumed") {
			return RunWait{}, waitInvalid()
		}
	} else {
		disposition, err := readWaitDisposition(m["disposition"])
		if err != nil {
			return RunWait{}, err
		}
		wait.Disposition = &disposition
		// A timeout ends the wait timed-out; a resumed wait keeps the disposition it was resumed after (a
		// timed-out approval or budget wait is never resumed).
		if wait.State == "woken" && disposition.Kind == "timeout" || wait.State == "timed-out" && disposition.Kind != "timeout" || wait.State == "resumed" && disposition.Kind == "timeout" && wait.Reason != "question" {
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
		// A timeout ends the question unanswered; only an event wake carries an answer.
		if (wait.Disposition.Kind == "timeout") != (resolution.Kind == "unanswered") || resolution.At != wait.Disposition.At {
			return RunWait{}, waitInvalid()
		}
		wait.Resolution = &resolution
	} else if m.has("resolution") {
		return RunWait{}, waitInvalid()
	}
	if m.has("reconciled") {
		if wait.State != "resumed" {
			return RunWait{}, waitInvalid()
		}
		if wait.Reconciled, err = readWaitReleaseList(m["reconciled"]); err != nil {
			return RunWait{}, err
		}
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
	if evidence.Kind, err = m.enum("kind", "event", "time", "timeout"); err != nil {
		return WakeEvidence{}, err
	}
	if (evidence.Kind == "event") != m.has("event") || m.has("answer") != m.has("actor") || m.has("answer") && evidence.Kind != "event" {
		return WakeEvidence{}, waitInvalid()
	}
	if evidence.Key, err = m.str("key"); err != nil || !waitKeyPattern.MatchString(evidence.Key) {
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

var waitNeeds = map[string]string{"approval": "approval-decision", "budget": "budget-grant", "question": "question-answer"}

// ReadAttentionEvent reads a version 1 needs_attention event.
func ReadAttentionEvent(raw []byte) (AttentionEvent, error) {
	m, err := waitShape(raw, []string{"version", "type", "id", "resource", "wait", "reason", "needs", "condition", "releaseUncertain", "at"}, []string{"subject", "timeoutAt"})
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
		func() (err error) { event.Reason, err = m.enum("reason", "approval", "budget", "question"); return },
		func() (err error) {
			event.Needs, err = m.enum("needs", "approval-decision", "budget-grant", "question-answer")
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

var waitEntryMembers = map[string][]string{"park": {"owner", "epoch", "subject", "condition", "timeoutAt", "release", "content"}, "wake": {"evidence", "wake"}, "timeout": {"evidence", "wake"}, "answer": {"actor", "content"}, "unanswered": {"content"}, "resume": {"owner", "epoch"}}

func readWaitEntry(raw json.RawMessage) (WaitReportEntry, error) {
	probe, err := waitShape(raw, []string{"wait", "decision", "reason", "at"}, []string{"owner", "epoch", "evidence", "wake", "actor", "subject", "condition", "timeoutAt", "release", "content"})
	if err != nil {
		return WaitReportEntry{}, err
	}
	decision, err := probe.enum("decision", "park", "wake", "timeout", "answer", "unanswered", "resume")
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
	if entry.Reason, err = m.enum("reason", "approval", "budget", "question"); err != nil {
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
		if entry.Release, err = optional("release", func(name string) (string, error) { return m.enum(name, "clean", "uncertain") }); err != nil {
			return entry, err
		}
		if (entry.Reason == "question") != m.has("content") {
			return entry, waitInvalid()
		}
	}
	if decision == "wake" || decision == "timeout" {
		if entry.Evidence, err = optional("evidence", m.id); err != nil {
			return entry, err
		}
		if entry.Wake, err = optional("wake", func(name string) (string, error) { return m.enum(name, "event", "time", "timeout") }); err != nil {
			return entry, err
		}
		if (decision == "timeout") != (*entry.Wake == "timeout") {
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
	}
	if wait.TimeoutAt != nil {
		at := *wait.TimeoutAt
		event.TimeoutAt = &at
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

// WaitStore is the wait port a store kind implements beside its lease port. Every transition is atomic
// with the store's commits and checked against its own clock.
type WaitStore interface {
	WaitVersion() int
	// WaitLifetime is durable when waits and attention events survive the store's process, process when
	// they end with it (the memory kind).
	WaitLifetime() string
	// ParkWait is one transaction: check the fence and the run head (checkpoint-changed), refuse a second
	// parked wait (wait-pending), commit the wait and its one needs_attention event, and release the
	// lease. A repeat of the same park returns it.
	ParkWait(context.Context, RunWait, LeaseFence) (RunWait, AttentionEvent, error)
	// WakeWait: the first valid wake wins; a duplicate returns the retained disposition; anything else
	// changes nothing.
	WakeWait(context.Context, WakeEvidence) (WakeDisposition, error)
	// ResumeWait claims the continuation once, under a current fence, after the wake the caller saw
	// acknowledged.
	ResumeWait(ctx context.Context, resource, wait, evidence string, fence LeaseFence, reconciled []WaitRelease) (RunWait, error)
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

// WaitTable holds the wait transitions in memory. MemoryRunStore calls it under its own lock; a host
// store kind implements the same transitions against its own durable rows. It is not safe for
// concurrent use on its own.
type WaitTable struct {
	rows       map[string][]*RunWait
	events     map[string][]AttentionEvent
	deliveries map[string][]AttentionDelivery
	now        func() time.Time
}

func NewWaitTable(now func() time.Time) *WaitTable {
	if now == nil {
		now = time.Now
	}
	return &WaitTable{rows: map[string][]*RunWait{}, events: map[string][]AttentionEvent{}, deliveries: map[string][]AttentionDelivery{}, now: now}
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
		prior.Parked, prior.Attention, prior.State, prior.Disposition, prior.Resolution, prior.Resumed, prior.Reconciled, prior.Redacted = contract.RunWaitActor{}, "", "", nil, nil, nil, nil, nil
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
	for _, row := range t.rows[park.Resource] {
		if row.State == "parked" {
			return RunWait{}, AttentionEvent{}, waitRefused("wait-pending")
		}
	}
	if !waitHeadMatches(head, park.Checkpoint) {
		return RunWait{}, AttentionEvent{}, waitRefused("checkpoint-changed")
	}
	at, err := t.stamp()
	if err != nil {
		return RunWait{}, AttentionEvent{}, err
	}
	wait := cloneRunWait(park)
	wait.Parked = contract.RunWaitActor{Owner: fence.Owner, Epoch: fence.Epoch, At: at}
	wait.Attention = WaitAttentionID(wait.Resource, wait.Wait)
	wait.State = "parked"
	attention := AttentionFor(wait)
	// #224: the lease is released in the same transaction that commits the wait and its attention event.
	if _, err := leases.Release(fence); err != nil {
		return RunWait{}, AttentionEvent{}, err
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
	if row.Disposition != nil {
		if row.Disposition.Evidence != evidence.Evidence {
			return WakeDisposition{}, waitRefused("stale")
		}
		if row.Disposition.Digest != digest {
			return WakeDisposition{}, waitRefused("conflict")
		}
		return *row.Disposition, nil
	}
	now, err := t.stamp()
	if err != nil {
		return WakeDisposition{}, err
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

// Resume claims the continuation once under a current fence.
func (t *WaitTable) Resume(resource, waitID, evidence string, fence LeaseFence, reconciled []WaitRelease, head *RunHead, leases *LeaseTable) (RunWait, error) {
	if !validStorageID(resource) || !validStorageID(waitID) || !validStorageID(evidence) {
		return RunWait{}, waitInvalid()
	}
	if reconciled == nil {
		reconciled = []WaitRelease{}
	}
	raw, _ := json.Marshal(reconciled)
	settled, err := readWaitReleaseList(raw)
	if err != nil {
		return RunWait{}, err
	}
	row := t.find(resource, waitID)
	if row == nil {
		return RunWait{}, waitRefused("no-wait")
	}
	// An acknowledgement the store does not hold is reconciled by reading, never by dispatching the
	// continuation.
	if row.State == "parked" || row.Disposition.Evidence != evidence {
		return RunWait{}, waitRefused("unacknowledged")
	}
	if row.State == "resumed" {
		return RunWait{}, waitRefused("already-resumed")
	}
	if !validLeaseFence(fence) {
		return RunWait{}, leaseInvalid()
	}
	if fence.Resource != row.Resource {
		return RunWait{}, &LeaseRefused{Reason: "resource-mismatch"}
	}
	if _, err := leases.Check(fence); err != nil {
		return RunWait{}, err
	}
	if row.State == "timed-out" && row.Reason != "question" {
		return RunWait{}, waitRefused("timed-out")
	}
	if !waitHeadMatches(head, row.Checkpoint) {
		return RunWait{}, waitRefused("checkpoint-changed")
	}
	uncertain := []string{}
	for _, release := range row.Release {
		if release.Outcome == "uncertain" {
			uncertain = append(uncertain, release.Resource)
		}
	}
	for _, resource := range uncertain {
		if !slices.ContainsFunc(settled, func(s WaitRelease) bool { return s.Resource == resource && s.Outcome != "uncertain" }) {
			return RunWait{}, waitRefused("release-uncertain")
		}
	}
	for _, s := range settled {
		if !slices.Contains(uncertain, s.Resource) {
			return RunWait{}, waitRefused("release-uncertain")
		}
	}
	at, err := t.stamp()
	if err != nil {
		return RunWait{}, err
	}
	row.State = "resumed"
	row.Resumed = &contract.RunWaitActor{Owner: fence.Owner, Epoch: fence.Epoch, At: at}
	if len(settled) > 0 {
		row.Reconciled = settled
	}
	return cloneRunWait(*row), nil
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
func (m *MemoryRunStore) ResumeWait(ctx context.Context, resource, wait, evidence string, fence LeaseFence, reconciled []WaitRelease) (held RunWait, err error) {
	err = m.leaseTransition(ctx, func() error {
		held, err = m.waits.Resume(resource, wait, evidence, fence, reconciled, m.headOf(resource), m.leases)
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

// ParkInput is a host's park request.
type ParkInput struct {
	Store        RunStore
	Fence        LeaseFence
	Wait         string
	Continuation string
	Condition    WaitCondition
	TimeoutAt    *string
	// WakeKeyDigest is the SHA-256 of the host's wake key (WakeKeyDigest(key)); the key stays the host's.
	WakeKeyDigest string
	// Release is the host's evidence for each provider resource the paused run held. Uncertain evidence
	// is retained, never a clean park.
	Release []WaitRelease
	// Question is the retained user_question event a question wait is for.
	Question    string
	RetainUntil *string
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
	if checkpoint.Status == "paused" {
		if input.Question != "" {
			return RunWait{}, AttentionEvent{}, waitInvalid()
		}
		if approval := checkpoint.Approval; approval != nil && approval.State == "asked" {
			park.Reason = "approval"
			park.Pending = contract.RunWaitPending{Approval: &contract.RunWaitPendingApproval{Kind: "approval", CallID: approval.CallID, Name: approval.Name, Tool: approval.Tool}}
		} else {
			park.Reason = "budget"
			park.Pending = contract.RunWaitPending{Budget: &contract.RunWaitPendingBudget{Kind: "budget", Unit: checkpoint.Unit, Used: checkpoint.Used, Limit: checkpoint.Limit}}
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
	if park, err = readWaitPark(park); err != nil {
		return RunWait{}, AttentionEvent{}, err
	}
	return store.ParkWait(ctx, park, input.Fence)
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
}

// ResumedWait is what a resume hands the host: the new lease and fence, the claimed wait, and the
// decision the run continues with.
type ResumedWait struct {
	Lease      RunLease
	Fence      LeaseFence
	Wait       RunWait
	Approval   *contract.CheckpointApprovalDecision
	Grant      *contract.CheckpointGrant
	Resolution *contract.RunWaitResolution
}

func waitDecision(wait RunWait, input ResumeInput) error {
	switch wait.Reason {
	case "approval":
		decision := input.Approval
		if input.Grant != nil || decision == nil || decision.CallID != wait.Pending.Approval.CallID || decision.Note != nil && jsLength(*decision.Note) > 2000 {
			return waitRefused("decision-required")
		}
	case "budget":
		grant, pending := input.Grant, wait.Pending.Budget
		if input.Approval != nil || grant == nil || pending.Unit != "model-requests" || grant.From != pending.Limit || grant.To <= grant.From || grant.To > 100 {
			return waitRefused("decision-required")
		}
		if _, err := time.Parse(time.RFC3339Nano, grant.At); err != nil {
			return waitRefused("decision-required")
		}
	default:
		// A question's continuation is its resolution; an approval or a grant is not a question's to take.
		if input.Approval != nil || input.Grant != nil {
			return waitRefused("decision-required")
		}
	}
	return nil
}

// ResumeParked resumes a woken wait: it checks the host's decision, takes a new lease against the parked
// revision, revalidates, and claims the continuation once. The host then continues the run through the
// fenced store with the returned decision.
func ResumeParked(ctx context.Context, input ResumeInput) (ResumedWait, error) {
	store, err := waitStoreOf(input.Store)
	if err != nil {
		return ResumedWait{}, err
	}
	wait, err := store.ReadWait(ctx, input.Resource, input.Wait)
	if err != nil {
		return ResumedWait{}, err
	}
	if wait == nil {
		return ResumedWait{}, waitRefused("no-wait")
	}
	if wait.State == "parked" || wait.Disposition.Evidence != input.Evidence {
		return ResumedWait{}, waitRefused("unacknowledged")
	}
	if wait.State == "resumed" {
		return ResumedWait{}, waitRefused("already-resumed")
	}
	if wait.State == "timed-out" && wait.Reason != "question" {
		return ResumedWait{}, waitRefused("timed-out")
	}
	if err := waitDecision(*wait, input); err != nil {
		return ResumedWait{}, err
	}
	if wait.Chat != nil {
		return ResumedWait{}, &WaitCapabilityRefused{Reason: "chat-store-unsupported"}
	}
	lease, err := store.AcquireLease(ctx, LeaseAcquire{Resource: wait.Resource, Owner: input.Owner, ExpectedRevision: wait.Checkpoint.Revision, DurationMs: input.DurationMs})
	if err != nil {
		return ResumedWait{}, err
	}
	fence := FenceOf(lease)
	resumed, err := store.ResumeWait(ctx, wait.Resource, wait.Wait, input.Evidence, fence, input.Reconciled)
	if err != nil {
		_, _ = store.ReleaseLease(context.WithoutCancel(ctx), fence)
		return ResumedWait{}, err
	}
	out := ResumedWait{Lease: lease, Fence: fence, Wait: resumed, Approval: input.Approval, Grant: input.Grant}
	if resumed.Resolution != nil {
		resolution := *cloneRunWait(resumed).Resolution
		out.Resolution = &resolution
	}
	return out, nil
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
		question := wait.Pending.Question
		switch {
		case wait.Pending.Approval != nil:
			park.Subject = str(wait.Pending.Approval.CallID)
		case question != nil:
			park.Subject = str(question.Event)
			park.Content = content(question.Question, nil, question.QuestionDigest)
		}
		report.Entries = append(report.Entries, park)
		if d := wait.Disposition; d != nil {
			decision := "wake"
			if d.Kind == "timeout" {
				decision = "timeout"
			}
			report.Entries = append(report.Entries, WaitReportEntry{Wait: wait.Wait, Decision: decision, Reason: wait.Reason, At: d.At, Evidence: str(d.Evidence), Wake: str(d.Kind)})
		}
		if r := wait.Resolution; r != nil && question != nil {
			if r.Kind == "answered" {
				report.Entries = append(report.Entries, WaitReportEntry{Wait: wait.Wait, Decision: "answer", Reason: wait.Reason, At: r.At, Actor: r.Actor, Content: content(nil, r.Answer, *r.AnswerDigest)})
			} else {
				report.Entries = append(report.Entries, WaitReportEntry{Wait: wait.Wait, Decision: "unanswered", Reason: wait.Reason, At: r.At, Content: content(question.Question, nil, question.QuestionDigest)})
			}
		}
		if r := wait.Resumed; r != nil {
			report.Entries = append(report.Entries, WaitReportEntry{Wait: wait.Wait, Decision: "resume", Reason: wait.Reason, At: r.At, Owner: str(r.Owner), Epoch: str(r.Epoch)})
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
