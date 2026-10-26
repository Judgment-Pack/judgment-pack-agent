package runcontext

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"io"
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// The original durable journal admits at most 400 physical dispatches in one root.
const RepeatDispatchLimit = 400

type RepeatPolicy struct{ RemindAt, StopAt int64 }
type RepeatedToolLimit struct{}

func (*RepeatedToolLimit) Error() string {
	return "RepeatedToolLimit: repeated identical acknowledged tool results reached the host policy threshold."
}
func (e *RepeatedToolLimit) Unwrap() error {
	return storageNamedFailure("repeated-work", "desk", "not-started", e.Error())
}

type repeatDispatchSource struct {
	accept func(contract.CheckpointRepeatExecution)
	event  func(contract.RunEvent)
}

func repeatPolicy(members PolicyMembers) *RepeatPolicy {
	if members["repeatTools.mode"].Value != "on" {
		return nil
	}
	number := func(name string) int64 {
		raw, _ := json.Marshal(members[name].Value)
		return int64(MustParseJSON(string(raw)).Num())
	}
	return &RepeatPolicy{number("repeatTools.remindAt"), number("repeatTools.stopAt")}
}
func repeatKey(row DurableDispatch) (string, error) {
	if (row.Kind != "host" && row.Kind != "runtime") || row.State != "result" || len(row.Result) == 0 || row.Failure != nil {
		return "", errors.New("Repeat result is not acknowledged terminal evidence.")
	}
	_, err := ParseJSON([]byte(row.Request))
	if err != nil {
		return "", err
	}
	if row.RepeatResultJson == nil || len(*row.RepeatResultJson) == 0 || len(*row.RepeatResultJson) > StorageRecordBytes {
		return "", &ContinuationIdentityChanged{}
	}
	result, err := ParseJSON(row.Result)
	if err != nil {
		return "", err
	}
	exact, err := ParseJSON([]byte(*row.RepeatResultJson))
	if err != nil || CanonicalJSON(exact) != CanonicalJSON(result) {
		return "", &ContinuationIdentityChanged{}
	}
	canonical, err := canonicalRepeatArgs([]byte(row.Request))
	if err != nil {
		return "", err
	}
	return durableDigest([]byte(CanonicalJSON(ObjectOf("kind", String(row.Kind), "source", String(row.Source), "tool", String(row.Tool), "args", String(canonical), "resultDigest", String(durableDigest([]byte(*row.RepeatResultJson))))))), nil
}
func observeRepeat(state contract.CheckpointRepeatExecution, row DurableDispatch) (contract.CheckpointRepeatExecution, *contract.RunEventRepeatDetected, error) {
	if row.Scope.Execution != state.Execution {
		return state, nil, &ContinuationIdentityChanged{}
	}
	for _, item := range state.Seen {
		if item.DispatchID == row.ID {
			return state, nil, nil
		}
	}
	if len(state.Seen) >= RepeatDispatchLimit {
		return state, nil, &ContinuationNotRecoverable{}
	}
	key, err := repeatKey(row)
	if err != nil {
		return state, nil, err
	}
	repeated := false
	for _, item := range state.Seen {
		if item.KeyDigest == key {
			repeated = true
		}
	}
	state.Seen = append(append([]contract.CheckpointRepeatObservation{}, state.Seen...), contract.CheckpointRepeatObservation{DispatchID: row.ID, KeyDigest: key})
	if !repeated {
		return state, nil, nil
	}
	state.Count++
	reminder := !state.Reminded && state.Count >= state.RemindAt
	state.Reminded = state.Reminded || reminder
	state.Stopped = state.Stopped || state.Count >= state.StopAt
	return state, &contract.RunEventRepeatDetected{Type: "repeat_detected", Execution: state.Execution, DispatchID: row.ID, KeyDigest: key, Count: state.Count, Reminder: reminder, Stopped: state.Stopped}, nil
}
func validRepeatTools(value *Value) bool {
	exact := func(v *Value, names ...string) bool {
		return v.IsObject() && len(v.Keys()) == len(names) && hasOnly(v, names...)
	}
	text := func(v *Value) bool { return v.IsString() && v.Str() != "" && U16Length(v.Str()) <= 100 }
	count := func(v *Value, min float64) bool { return isCount(v, RepeatDispatchLimit) && v.Num() >= min }
	if !exact(value, "executions") || !value.Get("executions").IsArray() || value.Get("executions").Len() < 1 || value.Get("executions").Len() > 33 {
		return false
	}
	executions, dispatches := map[string]bool{}, map[string]bool{}
	total := 0
	for _, row := range value.Get("executions").Items() {
		if !exact(row, "execution", "remindAt", "stopAt", "count", "reminded", "reminderDelivered", "stopped", "seen") || !text(row.Get("execution")) || executions[row.Get("execution").Str()] || !count(row.Get("remindAt"), 1) || !count(row.Get("stopAt"), 1) || row.Get("remindAt").Num() > row.Get("stopAt").Num() || !count(row.Get("count"), 0) || row.Get("reminded").Kind() != KindBool || row.Get("reminderDelivered").Kind() != KindBool || row.Get("stopped").Kind() != KindBool || row.Get("reminderDelivered").Truth() && !row.Get("reminded").Truth() || !row.Get("seen").IsArray() {
			return false
		}
		executions[row.Get("execution").Str()] = true
		keys := map[string]bool{}
		repeats := 0
		total += row.Get("seen").Len()
		if total > RepeatDispatchLimit {
			return false
		}
		for _, item := range row.Get("seen").Items() {
			if !exact(item, "dispatchId", "keyDigest") || !text(item.Get("dispatchId")) || dispatches[item.Get("dispatchId").Str()] || !recordDigestPattern.MatchString(item.Get("keyDigest").Str()) {
				return false
			}
			dispatches[item.Get("dispatchId").Str()] = true
			key := item.Get("keyDigest").Str()
			if keys[key] {
				repeats++
			}
			keys[key] = true
		}
		if row.Get("count").Num() != float64(repeats) || row.Get("reminded").Truth() != (float64(repeats) >= row.Get("remindAt").Num()) || row.Get("stopped").Truth() != (float64(repeats) >= row.Get("stopAt").Num()) {
			return false
		}
	}
	return true
}
func (w *RunWriter) repeatStateLocked(execution string) *contract.CheckpointRepeatExecution {
	if w.repeatTools != nil {
		for i := range w.repeatTools.Executions {
			if w.repeatTools.Executions[i].Execution == execution {
				return &w.repeatTools.Executions[i]
			}
		}
	}
	return nil
}
func (w *RunWriter) RepeatState(binding RunBinding) *contract.CheckpointRepeatExecution {
	w.mu.Lock()
	defer w.mu.Unlock()
	if binding.Writer != w {
		return nil
	}
	state := w.repeatStateLocked(binding.Scope.Execution)
	if state == nil {
		return nil
	}
	copy := *state
	copy.Seen = append([]contract.CheckpointRepeatObservation{}, state.Seen...)
	return &copy
}
func (w *RunWriter) repeatEvidenceValid() bool {
	for _, row := range w.book.Dispatches {
		if row.RepeatResultJson != nil {
			state := w.repeatStateLocked(row.Scope.Execution)
			found := false
			if state != nil {
				for _, item := range state.Seen {
					found = found || item.DispatchID == row.ID
				}
			}
			if !found {
				return false
			}
			if _, err := repeatKey(row); err != nil {
				return false
			}
		}
	}
	for _, state := range w.repeatTools.Executions {
		seen := map[string]bool{}
		for _, item := range state.Seen {
			found := false
			for _, row := range w.book.Dispatches {
				if row.ID == item.DispatchID && row.Scope.Execution == state.Execution {
					key, err := repeatKey(row)
					if err == nil && key == item.KeyDigest {
						found = true
						seen[row.ID] = true
					}
					break
				}
			}
			if !found {
				return false
			}
		}
		for _, row := range w.book.Dispatches {
			if row.Scope.Execution == state.Execution && (row.Kind == "host" || row.Kind == "runtime") && row.State == "result" && row.Failure == nil && !seen[row.ID] {
				return false
			}
		}
	}
	return true
}
func (w *RunWriter) attachRepeatScope(binding RunBinding, configuration *RepeatPolicy, source repeatDispatchSource) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if binding.Writer != w {
		return &ContinuationIdentityChanged{}
	}
	prior := w.repeatStateLocked(binding.Scope.Execution)
	if configuration == nil {
		if prior != nil {
			return &ContinuationIdentityChanged{}
		}
		return nil
	}
	if prior != nil && (prior.RemindAt != configuration.RemindAt || prior.StopAt != configuration.StopAt) {
		return &ContinuationIdentityChanged{}
	}
	existing := binding.Root
	for _, child := range w.book.Children {
		existing = existing || child.Scope.Execution == binding.Scope.Execution
	}
	if prior == nil && w.restoredTaskHead && existing {
		return &ContinuationIdentityChanged{}
	}
	if prior == nil {
		if w.repeatTools == nil {
			w.repeatTools = &contract.CheckpointRepeatTools{Executions: []contract.CheckpointRepeatExecution{}}
		}
		if len(w.repeatTools.Executions) >= 33 {
			return &ContinuationNotRecoverable{}
		}
		w.repeatTools.Executions = append(w.repeatTools.Executions, contract.CheckpointRepeatExecution{Execution: binding.Scope.Execution, RemindAt: configuration.RemindAt, StopAt: configuration.StopAt, Seen: []contract.CheckpointRepeatObservation{}})
	}
	if w.repeatScopes == nil {
		w.repeatScopes = map[string]repeatDispatchSource{}
	}
	w.repeatScopes[binding.Scope.Execution] = source
	if source.accept != nil {
		source.accept(*w.repeatStateLocked(binding.Scope.Execution))
	}
	return nil
}
func (c *SessionContext) attachRepeatScope() error {
	configuration := repeatPolicy(c.ResolvedPolicy)
	binding := c.Persistence("author")
	if binding == nil {
		if configuration != nil {
			return &ContinuationNotRecoverable{}
		}
		return nil
	}
	err := binding.Writer.attachRepeatScope(*binding, configuration, repeatDispatchSource{accept: func(state contract.CheckpointRepeatExecution) {
		if state.Stopped {
			reason := "RepeatedToolLimit"
			c.Execution.MutateCheckpoint(CheckpointPatch{"Status": "stopped", "Reason": &reason})
		}
	}, event: func(event contract.RunEvent) { c.notices = append(c.notices, event) }})
	if err != nil {
		return err
	}
	if configuration != nil {
		c.Execution.Progress.BeginPhase()
		c.Execution.RepeatGuidance = func() string { return repeatReminder(binding.Writer.RepeatState(*binding), c.Marker) }
	}
	return nil
}
func (w *RunWriter) rememberRepeatEvent(binding RunBinding, event contract.RunEvent) {
	if w.planDelivery == nil {
		w.planDelivery = map[any]planDelivery{}
	}
	raw, _ := canonicalRaw(event)
	w.planDelivery[planEventIdentity(event)] = planDelivery{scope: binding.Scope, bytes: string(raw)}
}

var repeatNumberPattern = regexp.MustCompile(`^(-?)([0-9]+)(?:\.([0-9]+))?(?:[eE]([+-]?[0-9]+))?$`)

func exactRepeatNumber(text string) (string, error) {
	parts := repeatNumberPattern.FindStringSubmatch(text)
	if parts == nil {
		return "", &ContinuationNotRecoverable{}
	}
	digits := strings.TrimLeft(parts[2]+parts[3], "0")
	if digits == "" {
		return "0", nil
	}
	scale := new(big.Int)
	if parts[4] != "" {
		if _, ok := scale.SetString(parts[4], 10); !ok {
			return "", &ContinuationNotRecoverable{}
		}
	}
	scale.Sub(scale, big.NewInt(int64(len(parts[3]))))
	shortened := strings.TrimRight(digits, "0")
	scale.Add(scale, big.NewInt(int64(len(digits)-len(shortened))))
	return parts[1] + shortened + "e" + scale.String(), nil
}
func canonicalRepeatArgs(raw []byte) (string, error) {
	if len(raw) > StorageRecordBytes {
		return "", &ContinuationNotRecoverable{}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return "", &ContinuationNotRecoverable{}
	}
	nodes := 0
	var canonical func(any, int) (string, error)
	canonical = func(value any, depth int) (string, error) {
		nodes++
		if depth > 64 || nodes > StorageRecordBytes {
			return "", &ContinuationNotRecoverable{}
		}
		switch held := value.(type) {
		case json.Number:
			return exactRepeatNumber(string(held))
		case string:
			return QuoteJSON(held), nil
		case nil:
			return "null", nil
		case bool:
			return strconv.FormatBool(held), nil
		case []any:
			items := []string{}
			for _, item := range held {
				text, err := canonical(item, depth+1)
				if err != nil {
					return "", err
				}
				items = append(items, text)
			}
			return "[" + strings.Join(items, ",") + "]", nil
		case map[string]any:
			keys := []string{}
			for key := range held {
				keys = append(keys, key)
			}
			sort.Slice(keys, func(i, j int) bool { return lessUTF16(keys[i], keys[j]) })
			items := []string{}
			for _, key := range keys {
				text, err := canonical(held[key], depth+1)
				if err != nil {
					return "", err
				}
				items = append(items, QuoteJSON(key)+":"+text)
			}
			return "{" + strings.Join(items, ",") + "}", nil
		}
		return "", &ContinuationNotRecoverable{}
	}
	return canonical(value, 0)
}

// Consistency is not dispatch or completion authority: the original writer owns those ACK facts.
func validRepeatRecordCustody(value *Value) bool {
	for _, held := range value.Get("durable").Get("dispatches").Items() {
		if held.Has("repeatResultJson") {
			if value.Get("version").Num() != 3 || !validRepeatTools(value.Get("repeatTools")) {
				return false
			}
			var row DurableDispatch
			if json.Unmarshal(held.JSON(), &row) != nil {
				return false
			}
			key, err := repeatKey(row)
			if err != nil {
				return false
			}
			found := false
			for _, state := range value.Get("repeatTools").Get("executions").Items() {
				for _, item := range state.Get("seen").Items() {
					if state.Get("execution").Str() == row.Scope.Execution && item.Get("dispatchId").Str() == row.ID && item.Get("keyDigest").Str() == key {
						found = true
					}
				}
			}
			if !found {
				return false
			}
		}
	}
	return true
}
