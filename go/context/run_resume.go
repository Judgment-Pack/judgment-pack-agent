package runcontext

// Deadlines, approval expiry and resume revalidation for unattended runs (ADR-0025 decisions 3 and 5,
// #225 and #227; contract/schemas/run-resume.json). The package validates and records: the host
// supplies its deadline policy, the model it admitted the run with and, on resume, its current
// evidence; the store decides every time by its own clock. Nothing here schedules, polls or substitutes
// a model. The TypeScript twin is packages/harness/storage/resume.ts.
import (
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
)

const ResumeRecordVersion = 1

// DeadlineDurationMaxMs bounds a wait or approval duration: 1 ms to 30 days of the store clock.
const DeadlineDurationMaxMs = 2_592_000_000
const ResumeSourceLimit = 64
const ResumeArtifactLimit = 256
const ResumeAdmissionLimit = 64
const ResumeChangeLimit = 384

// The wire records are the generated contract types of run-resume.json.
type WaitLimits = contract.RunResumeLimits
type RunLimit = contract.RunResumeRunLimit
type WaitDeadline = contract.RunResumeDeadline
type ApprovalBinding = contract.RunResumeApproval
type PinnedModel = contract.RunResumeModel
type SourcePin = contract.RunResumeSourcePin
type ArtifactPin = contract.RunResumeArtifactPin
type PlanPin = contract.RunResumePlanPin
type ResumePins = contract.RunResumePins
type ResumeAdmission = contract.RunResumeAdmission
type ResumeEvidence = contract.RunResumeEvidence
type ResumeChange = contract.RunResumeChange
type Revalidation = contract.RunResume

var ResumeRefusalReasons = []string{"invalid", "evidence-missing", "identity-changed", "lineage-changed", "model-retired", "model-changed", "reference-changed", "policy-changed", "approval-changed", "admission-invalid"}

// ResumeRefused is a named revalidation refusal. Nothing was claimed or dispatched; the wait still holds
// the run.
type ResumeRefused struct{ Reason string }

func (e *ResumeRefused) Error() string { return "ResumeRefused: " + e.Reason + "." }
func (e *ResumeRefused) Unwrap() error {
	f := failure.New("stale-state", "storage", "not-started")
	detail := e.Error()
	f.Detail = &detail
	return failure.NewError(f)
}
func (e *ResumeRefused) Record() contract.RunResumeRefusal {
	return contract.RunResumeRefusal{Version: ResumeRecordVersion, Reason: e.Reason}
}

func resumeInvalid() error              { return &ResumeRefused{Reason: "invalid"} }
func resumeRefused(reason string) error { return &ResumeRefused{Reason: reason} }

var resumeSourcePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]{0,23}$`)
var resumeProviderPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
var resumeReferencePattern = regexp.MustCompile(`^(source|artifact|dialect):`)

// resumeShape is waitShape with the resume's own invalid.
func resumeShape(raw json.RawMessage, required, optional []string) (waitMembers, error) {
	m, err := waitShape(raw, required, optional)
	if err != nil {
		return nil, resumeInvalid()
	}
	return m, nil
}

// asResume and asWait keep each record's own invalid: a malformed member of a wait record is the
// wait's invalid, and the resume's readers answer with the resume's.
func asResume(err error) error {
	if err == nil {
		return nil
	}
	var wait *WaitRefused
	if errors.As(err, &wait) {
		return resumeInvalid()
	}
	return err
}
func asWait(err error) error {
	if err == nil {
		return nil
	}
	var resume *ResumeRefused
	if errors.As(err, &resume) {
		return waitInvalid()
	}
	return err
}

// resumeLabel: an admission's subject or value, 1 to 320 UTF-8 bytes, no control characters.
func resumeLabel(m waitMembers, name string) (string, error) {
	value, err := m.str(name)
	if err != nil || value == "" || len(value) > 320 || !utf8.ValidString(value) || strings.ContainsFunc(value, func(r rune) bool { return r < 0x20 || r >= 0x7f && r <= 0x9f }) {
		return "", resumeInvalid()
	}
	return value, nil
}
func resumeDuration(m waitMembers, name string) (*int64, error) {
	if !m.has(name) {
		return nil, nil
	}
	value, err := m.integer(name, 1, DeadlineDurationMaxMs)
	if err != nil {
		return nil, resumeInvalid()
	}
	return &value, nil
}
func resumeOptionalTime(m waitMembers, name string) (*string, error) {
	if !m.has(name) {
		return nil, nil
	}
	value, err := m.time(name)
	if err != nil {
		return nil, resumeInvalid()
	}
	return &value, nil
}
func resumeOptionalDigest(m waitMembers, name string) (*string, error) {
	if !m.has(name) {
		return nil, nil
	}
	value, err := m.digest(name)
	if err != nil {
		return nil, resumeInvalid()
	}
	return &value, nil
}

// ReadWaitLimits reads the host's deadline policy for one park.
func ReadWaitLimits(raw []byte) (WaitLimits, error) {
	m, err := resumeShape(raw, nil, []string{"run", "waitMs", "approvalTtlMs"})
	if err != nil || len(m) == 0 {
		return WaitLimits{}, resumeInvalid()
	}
	limits := WaitLimits{}
	if limits.Run, err = resumeOptionalTime(m, "run"); err != nil {
		return WaitLimits{}, err
	}
	if limits.WaitMs, err = resumeDuration(m, "waitMs"); err != nil {
		return WaitLimits{}, err
	}
	if limits.ApprovalTtlMs, err = resumeDuration(m, "approvalTtlMs"); err != nil {
		return WaitLimits{}, err
	}
	return limits, nil
}

// ReadRunLimit reads a run's deadline as the host sets it and the store answers it.
func ReadRunLimit(raw []byte) (RunLimit, error) {
	m, err := resumeShape(raw, []string{"version", "resource", "run"}, nil)
	if err != nil || m.version() != nil {
		return RunLimit{}, resumeInvalid()
	}
	limit := RunLimit{Version: ResumeRecordVersion}
	if limit.Resource, err = m.id("resource"); err != nil {
		return RunLimit{}, resumeInvalid()
	}
	if limit.Run, err = m.time("run"); err != nil {
		return RunLimit{}, resumeInvalid()
	}
	return limit, nil
}

// StampDeadline is the deadline a park stamps: the earliest of the run's deadline (the earliest the
// store holds and this park's), the park time plus waitMs and, for an approval wait, plus approvalTtlMs.
// nil when none applies. ceiling is the store's run deadline in milliseconds, or 0 for none.
func StampDeadline(limits *WaitLimits, parkedAt, ceiling int64) (*WaitDeadline, error) {
	run := int64(math.MaxInt64)
	if limits != nil && limits.Run != nil {
		run = waitTimeMillis(*limits.Run)
	}
	if ceiling != 0 && ceiling < run {
		run = ceiling
	}
	times := map[string]int64{}
	if run != math.MaxInt64 {
		times["run"] = run
	}
	if limits != nil && limits.WaitMs != nil {
		times["wait"] = parkedAt + *limits.WaitMs
	}
	if limits != nil && limits.ApprovalTtlMs != nil {
		times["approval"] = parkedAt + *limits.ApprovalTtlMs
	}
	if len(times) == 0 {
		return nil, nil
	}
	deadline := WaitDeadline{}
	at := int64(math.MaxInt64)
	// #225: the earliest applicable deadline wins; on a tie the run's is named first.
	for _, by := range []string{"run", "wait", "approval"} {
		t, ok := times[by]
		if !ok {
			continue
		}
		text, err := leaseExpiryAt(t)
		if err != nil {
			return nil, resumeInvalid()
		}
		switch by {
		case "run":
			deadline.Run = &text
		case "wait":
			deadline.Wait = &text
		case "approval":
			deadline.Approval = &text
		}
		if t < at {
			at, deadline.By, deadline.At = t, by, text
		}
	}
	return &deadline, nil
}

// readWaitDeadline reads a stamped deadline and holds its rules against the park's limits and time.
func readWaitDeadline(raw json.RawMessage, limits *WaitLimits, parkedAt string) (WaitDeadline, error) {
	m, err := resumeShape(raw, []string{"at", "by"}, []string{"run", "wait", "approval"})
	if err != nil {
		return WaitDeadline{}, err
	}
	deadline := WaitDeadline{}
	if deadline.At, err = m.time("at"); err != nil {
		return WaitDeadline{}, resumeInvalid()
	}
	if deadline.By, err = m.enum("by", "run", "wait", "approval"); err != nil {
		return WaitDeadline{}, resumeInvalid()
	}
	if deadline.Run, err = resumeOptionalTime(m, "run"); err != nil {
		return WaitDeadline{}, err
	}
	if deadline.Wait, err = resumeOptionalTime(m, "wait"); err != nil {
		return WaitDeadline{}, err
	}
	if deadline.Approval, err = resumeOptionalTime(m, "approval"); err != nil {
		return WaitDeadline{}, err
	}
	parked := waitTimeMillis(parkedAt)
	at, by := int64(math.MaxInt64), ""
	for _, candidate := range []struct {
		name string
		time *string
	}{{"run", deadline.Run}, {"wait", deadline.Wait}, {"approval", deadline.Approval}} {
		if candidate.time != nil && waitTimeMillis(*candidate.time) < at {
			at, by = waitTimeMillis(*candidate.time), candidate.name
		}
	}
	if by == "" || waitTimeMillis(deadline.At) != at || deadline.By != by || at <= parked {
		return WaitDeadline{}, resumeInvalid()
	}
	// The wait and approval times are the park's own durations; the run's is never later than this
	// park's run limit.
	waitMs, ttl := (*int64)(nil), (*int64)(nil)
	if limits != nil {
		waitMs, ttl = limits.WaitMs, limits.ApprovalTtlMs
	}
	if (waitMs == nil) != (deadline.Wait == nil) || deadline.Wait != nil && waitTimeMillis(*deadline.Wait) != parked+*waitMs {
		return WaitDeadline{}, resumeInvalid()
	}
	if (ttl == nil) != (deadline.Approval == nil) || deadline.Approval != nil && waitTimeMillis(*deadline.Approval) != parked+*ttl {
		return WaitDeadline{}, resumeInvalid()
	}
	if limits != nil && limits.Run != nil && (deadline.Run == nil || waitTimeMillis(*deadline.Run) > waitTimeMillis(*limits.Run)) {
		return WaitDeadline{}, resumeInvalid()
	}
	return deadline, nil
}
func readApprovalBinding(raw json.RawMessage) (ApprovalBinding, error) {
	m, err := resumeShape(raw, []string{"callId", "callDigest", "source"}, nil)
	if err != nil {
		return ApprovalBinding{}, err
	}
	binding := ApprovalBinding{}
	if binding.CallID, err = m.text("callId", 1, 256); err != nil {
		return ApprovalBinding{}, resumeInvalid()
	}
	if binding.CallDigest, err = m.digest("callDigest"); err != nil {
		return ApprovalBinding{}, resumeInvalid()
	}
	if binding.Source, err = m.str("source"); err != nil || !resumeSourcePattern.MatchString(binding.Source) {
		return ApprovalBinding{}, resumeInvalid()
	}
	return binding, nil
}
func readPinnedModel(raw json.RawMessage, status bool) (PinnedModel, string, error) {
	members := []string{"provider", "model"}
	if status {
		members = append(members, "status")
	}
	m, err := resumeShape(raw, members, nil)
	if err != nil {
		return PinnedModel{}, "", err
	}
	model := PinnedModel{}
	if model.Provider, err = m.str("provider"); err != nil || !resumeProviderPattern.MatchString(model.Provider) {
		return PinnedModel{}, "", resumeInvalid()
	}
	if model.Model, err = m.id("model"); err != nil {
		return PinnedModel{}, "", resumeInvalid()
	}
	state := ""
	if status {
		if state, err = m.enum("status", "available", "retired"); err != nil {
			return PinnedModel{}, "", resumeInvalid()
		}
	}
	return model, state, nil
}
func readSourcePins(raw json.RawMessage) ([]SourcePin, error) {
	items, err := (waitMembers{"list": raw}).list("list", ResumeSourceLimit)
	if err != nil {
		return nil, resumeInvalid()
	}
	out := []SourcePin{}
	for _, item := range items {
		m, err := resumeShape(item, []string{"source"}, []string{"identityDigest", "descriptorDigest"})
		if err != nil {
			return nil, err
		}
		pin := SourcePin{}
		if pin.Source, err = m.str("source"); err != nil || !resumeSourcePattern.MatchString(pin.Source) {
			return nil, resumeInvalid()
		}
		if pin.IdentityDigest, err = resumeOptionalDigest(m, "identityDigest"); err != nil {
			return nil, err
		}
		if pin.DescriptorDigest, err = resumeOptionalDigest(m, "descriptorDigest"); err != nil {
			return nil, err
		}
		// Unique and sorted by source: one pin per source, and one canonical order for the digest.
		if len(out) > 0 && out[len(out)-1].Source >= pin.Source {
			return nil, resumeInvalid()
		}
		out = append(out, pin)
	}
	return out, nil
}
func artifactBefore(a, b ArtifactPin) bool {
	if a.Kind != b.Kind {
		return a.Kind == "artifact"
	}
	return a.ID < b.ID
}
func readArtifactPins(raw json.RawMessage) ([]ArtifactPin, error) {
	items, err := (waitMembers{"list": raw}).list("list", ResumeArtifactLimit)
	if err != nil {
		return nil, resumeInvalid()
	}
	out := []ArtifactPin{}
	for _, item := range items {
		m, err := resumeShape(item, []string{"kind", "id", "digest"}, nil)
		if err != nil {
			return nil, err
		}
		pin := ArtifactPin{}
		if pin.Kind, err = m.enum("kind", "artifact", "dialect"); err != nil {
			return nil, resumeInvalid()
		}
		if pin.ID, err = m.id("id"); err != nil {
			return nil, resumeInvalid()
		}
		if pin.Digest, err = m.digest("digest"); err != nil {
			return nil, resumeInvalid()
		}
		if len(out) > 0 && !artifactBefore(out[len(out)-1], pin) {
			return nil, resumeInvalid()
		}
		out = append(out, pin)
	}
	return out, nil
}
func readPlanPin(raw json.RawMessage) (*PlanPin, error) {
	m, err := resumeShape(raw, []string{"revision"}, []string{"accepted"})
	if err != nil {
		return nil, err
	}
	pin := PlanPin{}
	if pin.Revision, err = m.digest("revision"); err != nil {
		return nil, resumeInvalid()
	}
	if m.has("accepted") {
		accepted, err := m.id("accepted")
		if err != nil {
			return nil, resumeInvalid()
		}
		pin.Accepted = &accepted
	}
	return &pin, nil
}

// ReadResumePins reads the execution authority a parked run continues under.
func ReadResumePins(raw []byte) (ResumePins, error) {
	m, err := resumeShape(raw, []string{"model", "policy", "sources", "artifacts"}, []string{"plan"})
	if err != nil {
		return ResumePins{}, err
	}
	pins := ResumePins{}
	if pins.Model, _, err = readPinnedModel(m["model"], false); err != nil {
		return ResumePins{}, err
	}
	if pins.Policy, err = m.digest("policy"); err != nil {
		return ResumePins{}, resumeInvalid()
	}
	if m.has("plan") {
		if pins.Plan, err = readPlanPin(m["plan"]); err != nil {
			return ResumePins{}, err
		}
	}
	if pins.Sources, err = readSourcePins(m["sources"]); err != nil {
		return ResumePins{}, err
	}
	if pins.Artifacts, err = readArtifactPins(m["artifacts"]); err != nil {
		return ResumePins{}, err
	}
	return pins, nil
}
func checkResumePins(pins ResumePins) (ResumePins, error) {
	if pins.Sources == nil {
		pins.Sources = []SourcePin{}
	}
	if pins.Artifacts == nil {
		pins.Artifacts = []ArtifactPin{}
	}
	raw, err := json.Marshal(pins)
	if err != nil {
		return ResumePins{}, resumeInvalid()
	}
	return ReadResumePins(raw)
}

// ReadResumeEvidence reads the host's current evidence for one resume.
func ReadResumeEvidence(raw []byte) (ResumeEvidence, error) {
	m, err := resumeShape(raw, []string{"version", "resource", "wait", "model", "policy", "sources", "artifacts"}, []string{"plan", "admissions"})
	if err != nil || m.version() != nil {
		return ResumeEvidence{}, resumeInvalid()
	}
	evidence := ResumeEvidence{Version: ResumeRecordVersion}
	if evidence.Resource, err = m.id("resource"); err != nil {
		return ResumeEvidence{}, resumeInvalid()
	}
	if evidence.Wait, err = m.id("wait"); err != nil {
		return ResumeEvidence{}, resumeInvalid()
	}
	model, status, err := readPinnedModel(m["model"], true)
	if err != nil {
		return ResumeEvidence{}, err
	}
	evidence.Model = contract.RunResumeCurrentModel{Provider: model.Provider, Model: model.Model, Status: status}
	policy, err := resumeShape(m["policy"], []string{"digest"}, []string{"layer"})
	if err != nil {
		return ResumeEvidence{}, err
	}
	if evidence.Policy.Digest, err = policy.digest("digest"); err != nil {
		return ResumeEvidence{}, resumeInvalid()
	}
	if evidence.Policy.Layer, err = resumeOptionalDigest(policy, "layer"); err != nil {
		return ResumeEvidence{}, err
	}
	if m.has("plan") {
		if evidence.Plan, err = readPlanPin(m["plan"]); err != nil {
			return ResumeEvidence{}, err
		}
	}
	if evidence.Sources, err = readSourcePins(m["sources"]); err != nil {
		return ResumeEvidence{}, err
	}
	if evidence.Artifacts, err = readArtifactPins(m["artifacts"]); err != nil {
		return ResumeEvidence{}, err
	}
	if m.has("admissions") {
		items, err := m.list("admissions", ResumeAdmissionLimit)
		if err != nil {
			return ResumeEvidence{}, resumeInvalid()
		}
		evidence.Admissions = []ResumeAdmission{}
		seen := map[[2]string]bool{}
		for _, item := range items {
			a, err := resumeShape(item, []string{"kind", "subject", "from", "to", "actor", "at"}, nil)
			if err != nil {
				return ResumeEvidence{}, err
			}
			admission := ResumeAdmission{}
			if admission.Kind, err = a.enum("kind", "model", "reference"); err != nil {
				return ResumeEvidence{}, resumeInvalid()
			}
			if admission.Subject, err = resumeLabel(a, "subject"); err != nil {
				return ResumeEvidence{}, err
			}
			if admission.From, err = resumeLabel(a, "from"); err != nil {
				return ResumeEvidence{}, err
			}
			if admission.To, err = resumeLabel(a, "to"); err != nil {
				return ResumeEvidence{}, err
			}
			if admission.Actor, err = a.id("actor"); err != nil {
				return ResumeEvidence{}, resumeInvalid()
			}
			if admission.At, err = a.time("at"); err != nil {
				return ResumeEvidence{}, resumeInvalid()
			}
			key := [2]string{admission.Kind, admission.Subject}
			if seen[key] {
				return ResumeEvidence{}, resumeInvalid()
			}
			seen[key] = true
			evidence.Admissions = append(evidence.Admissions, admission)
		}
	}
	return evidence, nil
}
func checkResumeEvidence(evidence ResumeEvidence) (ResumeEvidence, error) {
	if evidence.Sources == nil {
		evidence.Sources = []SourcePin{}
	}
	if evidence.Artifacts == nil {
		evidence.Artifacts = []ArtifactPin{}
	}
	raw, err := json.Marshal(evidence)
	if err != nil {
		return ResumeEvidence{}, resumeInvalid()
	}
	return ReadResumeEvidence(raw)
}
func readResumeChange(raw json.RawMessage) (ResumeChange, error) {
	m, err := resumeShape(raw, []string{"kind"}, []string{"subject", "from", "to", "actor", "at", "layer"})
	if err != nil {
		return ResumeChange{}, err
	}
	change := ResumeChange{}
	if change.Kind, err = m.enum("kind", "model", "reference", "policy"); err != nil {
		return ResumeChange{}, resumeInvalid()
	}
	required := []string{"kind", "subject", "from", "to", "actor", "at"}
	if change.Kind == "policy" {
		required = []string{"kind", "subject", "from", "to", "layer"}
	}
	if m, err = resumeShape(raw, required, nil); err != nil {
		return ResumeChange{}, err
	}
	if change.Subject, err = resumeLabel(m, "subject"); err != nil {
		return ResumeChange{}, err
	}
	if change.From, err = resumeLabel(m, "from"); err != nil {
		return ResumeChange{}, err
	}
	if change.To, err = resumeLabel(m, "to"); err != nil {
		return ResumeChange{}, err
	}
	digests := func() bool {
		return leaseDigestPattern.MatchString(change.From) && leaseDigestPattern.MatchString(change.To)
	}
	switch change.Kind {
	case "policy":
		layer, err := m.digest("layer")
		if err != nil || change.Subject != "policy" || !digests() {
			return ResumeChange{}, resumeInvalid()
		}
		change.Layer = &layer
	default:
		if change.Kind == "model" && change.Subject != "model" || change.Kind == "reference" && (!resumeReferencePattern.MatchString(change.Subject) || !digests()) {
			return ResumeChange{}, resumeInvalid()
		}
		actor, err := m.id("actor")
		if err != nil {
			return ResumeChange{}, resumeInvalid()
		}
		at, err := m.time("at")
		if err != nil {
			return ResumeChange{}, resumeInvalid()
		}
		change.Actor, change.At = &actor, &at
	}
	if change.From == change.To {
		return ResumeChange{}, resumeInvalid()
	}
	return change, nil
}

// ReadRevalidation reads a version 1 revalidation record.
func ReadRevalidation(raw []byte) (Revalidation, error) {
	m, err := resumeShape(raw, []string{"version", "resource", "wait", "pins", "evidence", "changes"}, nil)
	if err != nil || m.version() != nil {
		return Revalidation{}, resumeInvalid()
	}
	record := Revalidation{Version: ResumeRecordVersion, Changes: []ResumeChange{}}
	if record.Resource, err = m.id("resource"); err != nil {
		return Revalidation{}, resumeInvalid()
	}
	if record.Wait, err = m.id("wait"); err != nil {
		return Revalidation{}, resumeInvalid()
	}
	if record.Pins, err = m.digest("pins"); err != nil {
		return Revalidation{}, resumeInvalid()
	}
	if record.Evidence, err = m.digest("evidence"); err != nil {
		return Revalidation{}, resumeInvalid()
	}
	items, err := m.list("changes", ResumeChangeLimit)
	if err != nil {
		return Revalidation{}, resumeInvalid()
	}
	seen := map[[2]string]bool{}
	for _, item := range items {
		change, err := readResumeChange(item)
		if err != nil {
			return Revalidation{}, err
		}
		key := [2]string{change.Kind, change.Subject}
		if seen[key] {
			return Revalidation{}, resumeInvalid()
		}
		seen[key] = true
		record.Changes = append(record.Changes, change)
	}
	return record, nil
}
func checkRevalidation(record Revalidation) (Revalidation, error) {
	if record.Changes == nil {
		record.Changes = []ResumeChange{}
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return Revalidation{}, resumeInvalid()
	}
	return ReadRevalidation(raw)
}

// ReadResumeRefusal reads a named revalidation refusal.
func ReadResumeRefusal(raw []byte) (contract.RunResumeRefusal, error) {
	m, err := resumeShape(raw, []string{"version", "reason"}, nil)
	if err != nil || m.version() != nil {
		return contract.RunResumeRefusal{}, resumeInvalid()
	}
	reason, err := m.enum("reason", ResumeRefusalReasons...)
	if err != nil {
		return contract.RunResumeRefusal{}, resumeInvalid()
	}
	return contract.RunResumeRefusal{Version: ResumeRecordVersion, Reason: reason}, nil
}

// PolicyIdentity is the identity of a resolved policy: the SHA-256 of the canonical JSON of its members,
// or of {} without them.
func PolicyIdentity(members map[string]contract.PolicyMember) string {
	if members == nil {
		members = map[string]contract.PolicyMember{}
	}
	return waitCanonicalDigest(members)
}

// PinsDigest is the SHA-256 of the canonical JSON of a wait's pins.
func PinsDigest(pins ResumePins) (string, error) {
	held, err := checkResumePins(pins)
	if err != nil {
		return "", err
	}
	return waitCanonicalDigest(held), nil
}

// ResumeEvidenceDigest is the SHA-256 of the canonical JSON of the host's current evidence.
func ResumeEvidenceDigest(evidence ResumeEvidence) (string, error) {
	held, err := checkResumeEvidence(evidence)
	if err != nil {
		return "", err
	}
	return waitCanonicalDigest(held), nil
}

// ApprovalBindingOf is an approval wait's binding: the asked call's id, the digest of the call the
// person reviewed (its wire name, tool name, arguments and Runtime binding) and its source. Read from
// the retained checkpoint, never from the caller.
func ApprovalBindingOf(approval contract.CheckpointApproval) (ApprovalBinding, error) {
	call := map[string]any{"name": approval.Name, "tool": approval.Tool, "args": approval.Args}
	if approval.Args == nil {
		call["args"] = map[string]json.RawMessage{}
	}
	if approval.Runtime != nil {
		call["runtime"] = approval.Runtime
	}
	raw, err := json.Marshal(ApprovalBinding{CallID: approval.CallID, CallDigest: waitCanonicalDigest(call), Source: approval.Source.ID})
	if err != nil {
		return ApprovalBinding{}, resumeInvalid()
	}
	return readApprovalBinding(raw)
}

// PinsFrom is the execution authority a park pins, read from the retained checkpoint, with the model the
// trusted host admitted the run with: the resolved policy's identity, the Plan lineage, each source's
// identity digest (and the asked approval's source with its descriptor digest), and the last observed
// revision of each artifact.
func PinsFrom(checkpoint *contract.Checkpoint, model PinnedModel) (ResumePins, error) {
	sources := map[string]SourcePin{}
	for _, row := range checkpoint.SourceIdentities {
		identity := row.IdentityDigest
		sources[row.Source] = SourcePin{Source: row.Source, IdentityDigest: &identity}
	}
	if asked := checkpoint.Approval; asked != nil && asked.State == "asked" {
		record := asked.Source
		pin, ok := sources[record.ID]
		if !ok {
			pin = SourcePin{Source: record.ID}
		}
		if record.IdentityDigest != nil {
			if pin.IdentityDigest != nil && *pin.IdentityDigest != *record.IdentityDigest {
				return ResumePins{}, resumeInvalid()
			}
			identity := *record.IdentityDigest
			pin.IdentityDigest = &identity
		}
		if record.ToolListDigest != nil {
			descriptor := *record.ToolListDigest
			pin.DescriptorDigest = &descriptor
		}
		sources[record.ID] = pin
	}
	pins := ResumePins{Model: model, Policy: PolicyIdentity(checkpoint.Policy), Sources: []SourcePin{}, Artifacts: []ArtifactPin{}}
	for _, pin := range sources {
		pins.Sources = append(pins.Sources, pin)
	}
	slices.SortFunc(pins.Sources, func(a, b SourcePin) int { return strings.Compare(a.Source, b.Source) })
	if checkpoint.Durable != nil {
		last := map[[2]string]int{}
		for _, row := range checkpoint.Durable.Observations {
			if row.Artifact == nil {
				continue
			}
			pin := ArtifactPin{Kind: row.Artifact.Kind, ID: row.Artifact.ID, Digest: row.Artifact.Digest}
			key := [2]string{pin.Kind, pin.ID}
			if at, ok := last[key]; ok {
				pins.Artifacts[at] = pin
				continue
			}
			last[key] = len(pins.Artifacts)
			pins.Artifacts = append(pins.Artifacts, pin)
		}
		slices.SortFunc(pins.Artifacts, func(a, b ArtifactPin) int {
			if artifactBefore(a, b) {
				return -1
			}
			if artifactBefore(b, a) {
				return 1
			}
			return 0
		})
	}
	if plan := checkpoint.PlanState; plan != nil {
		pins.Plan = &PlanPin{Revision: plan.Revision}
		if plan.Accepted != nil {
			accepted := plan.Accepted.Revision
			pins.Plan.Accepted = &accepted
		}
	}
	return checkResumePins(pins)
}

// RevalidatedWait is what revalidation reads of a wait: its run, identity, reason, pins and an approval
// wait's binding.
type RevalidatedWait struct {
	Resource string
	Wait     string
	Reason   string
	Pins     ResumePins
	Approval *ApprovalBinding
}

func optionalEqual(a, b *string) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

// RevalidateResume (#227) revalidates a woken wait's pinned execution authority against the host's
// current evidence, before anything of its continuation is claimed or dispatched. Missing evidence
// refuses; a changed source identity or Plan lineage needs a new run; a retired model parks for an
// explicit decision and is never replaced; a changed model or reference needs the matching explicit
// admission; a changed policy identity needs the trusted host's new layer; an approval whose
// source's descriptor or policy changed cannot run. It returns the record of what the continuation runs under,
// old and new.
func RevalidateResume(wait RevalidatedWait, input ResumeEvidence) (Revalidation, error) {
	current, err := checkResumeEvidence(input)
	if err != nil {
		return Revalidation{}, err
	}
	pins, err := checkResumePins(wait.Pins)
	if err != nil {
		return Revalidation{}, err
	}
	if current.Resource != wait.Resource || current.Wait != wait.Wait {
		return Revalidation{}, resumeInvalid()
	}
	sources := map[string]SourcePin{}
	for _, s := range current.Sources {
		sources[s.Source] = s
	}
	artifacts := map[[2]string]ArtifactPin{}
	for _, a := range current.Artifacts {
		artifacts[[2]string{a.Kind, a.ID}] = a
	}
	for _, pin := range pins.Sources {
		now, ok := sources[pin.Source]
		if !ok || pin.IdentityDigest != nil && now.IdentityDigest == nil || pin.DescriptorDigest != nil && now.DescriptorDigest == nil {
			return Revalidation{}, resumeRefused("evidence-missing")
		}
	}
	for _, pin := range pins.Artifacts {
		if _, ok := artifacts[[2]string{pin.Kind, pin.ID}]; !ok {
			return Revalidation{}, resumeRefused("evidence-missing")
		}
	}
	if pins.Plan != nil && (current.Plan == nil || pins.Plan.Accepted != nil && current.Plan.Accepted == nil) {
		return Revalidation{}, resumeRefused("evidence-missing")
	}
	// ADR-0019: a changed identity is never relabelled as a refresh; it needs a new admitted run.
	for _, pin := range pins.Sources {
		if pin.IdentityDigest != nil && !optionalEqual(sources[pin.Source].IdentityDigest, pin.IdentityDigest) {
			return Revalidation{}, resumeRefused("identity-changed")
		}
	}
	if (pins.Plan == nil) != (current.Plan == nil) || pins.Plan != nil && (pins.Plan.Revision != current.Plan.Revision || !optionalEqual(pins.Plan.Accepted, current.Plan.Accepted)) {
		return Revalidation{}, resumeRefused("lineage-changed")
	}
	// A retired model parks for an explicit decision: nothing is substituted, whatever an admission says.
	if current.Model.Status == "retired" {
		return Revalidation{}, resumeRefused("model-retired")
	}
	used := map[int]bool{}
	changes := []ResumeChange{}
	admitted := func(kind, subject, from, to string) (*ResumeAdmission, error) {
		for at, admission := range current.Admissions {
			if admission.Kind != kind || admission.Subject != subject {
				continue
			}
			// An admission is exactly this change: from the pin to the current value.
			if admission.From != from || admission.To != to {
				return nil, resumeRefused("admission-invalid")
			}
			used[at] = true
			return &admission, nil
		}
		return nil, nil
	}
	pinnedModel, currentModel := pins.Model.Provider+"/"+pins.Model.Model, current.Model.Provider+"/"+current.Model.Model
	if pinnedModel != currentModel {
		admission, err := admitted("model", "model", pinnedModel, currentModel)
		if err != nil {
			return Revalidation{}, err
		}
		if admission == nil {
			return Revalidation{}, resumeRefused("model-changed")
		}
		changes = append(changes, ResumeChange{Kind: "model", Subject: "model", From: pinnedModel, To: currentModel, Actor: &admission.Actor, At: &admission.At})
	}
	reference := func(subject, from, to string) error {
		admission, err := admitted("reference", subject, from, to)
		if err != nil {
			return err
		}
		if admission == nil {
			return resumeRefused("reference-changed")
		}
		changes = append(changes, ResumeChange{Kind: "reference", Subject: subject, From: from, To: to, Actor: &admission.Actor, At: &admission.At})
		return nil
	}
	for _, pin := range pins.Sources {
		now := sources[pin.Source]
		if pin.DescriptorDigest != nil && !optionalEqual(now.DescriptorDigest, pin.DescriptorDigest) {
			if err := reference("source:"+pin.Source, *pin.DescriptorDigest, *now.DescriptorDigest); err != nil {
				return Revalidation{}, err
			}
		}
	}
	for _, pin := range pins.Artifacts {
		now := artifacts[[2]string{pin.Kind, pin.ID}]
		if now.Digest != pin.Digest {
			if err := reference(pin.Kind+":"+pin.ID, pin.Digest, now.Digest); err != nil {
				return Revalidation{}, err
			}
		}
	}
	// The pinned policy holds unless the trusted host supplies a new layer; both identities are kept.
	if current.Policy.Digest != pins.Policy {
		if current.Policy.Layer == nil {
			return Revalidation{}, resumeRefused("policy-changed")
		}
		layer := *current.Policy.Layer
		changes = append(changes, ResumeChange{Kind: "policy", Subject: "policy", From: pins.Policy, To: current.Policy.Digest, Layer: &layer})
	}
	// #225: an approval is bound to the reviewed call's source, descriptor and policy identities; a
	// changed one cannot run.
	if wait.Reason == "approval" {
		if wait.Approval == nil {
			return Revalidation{}, resumeInvalid()
		}
		raw, _ := json.Marshal(wait.Approval)
		binding, err := readApprovalBinding(raw)
		if err != nil {
			return Revalidation{}, err
		}
		if slices.ContainsFunc(changes, func(c ResumeChange) bool {
			return c.Kind == "policy" || c.Kind == "reference" && c.Subject == "source:"+binding.Source
		}) {
			return Revalidation{}, resumeRefused("approval-changed")
		}
	}
	if len(used) != len(current.Admissions) {
		return Revalidation{}, resumeRefused("admission-invalid")
	}
	pinsDigest, err := PinsDigest(pins)
	if err != nil {
		return Revalidation{}, err
	}
	return checkRevalidation(Revalidation{Version: ResumeRecordVersion, Resource: wait.Resource, Wait: wait.Wait, Pins: pinsDigest, Evidence: waitCanonicalDigest(current), Changes: changes})
}
