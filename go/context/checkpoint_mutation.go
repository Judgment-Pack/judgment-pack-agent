package runcontext

import (
	"encoding/json"
	"fmt"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
	"reflect"
	"sync"
)

// CheckpointPatch replaces named Execution members atomically; nil clears an optional member.
// Build and validate member copies before committing. Callers never receive a mutable aggregate.
type CheckpointPatch map[string]any

// CheckpointIncrement is an atomic numeric member update, evaluated under the live store lock.
// It avoids a detached read-modify-write race for counters without exposing aggregate mutation.
type CheckpointIncrement int64

type checkpointMutation struct {
	mu            sync.RWMutex
	value         *Execution
	revision      uint64
	requestCap    int
	capConfigured bool
	capFailure    error
}

func cloneCheckpointValue(v reflect.Value) reflect.Value {
	if !v.IsValid() {
		return v
	}
	if v.Type() == reflect.TypeOf((*Value)(nil)) {
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		return reflect.ValueOf(v.Interface().(*Value).Clone())
	}
	switch v.Kind() {
	case reflect.Interface:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.New(v.Type()).Elem()
		out.Set(cloneCheckpointValue(v.Elem()))
		return out
	case reflect.Pointer:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.New(v.Type().Elem())
		out.Elem().Set(cloneCheckpointValue(v.Elem()))
		return out
	case reflect.Slice:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := 0; i < v.Len(); i++ {
			out.Index(i).Set(cloneCheckpointValue(v.Index(i)))
		}
		return out
	case reflect.Map:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.MakeMapWithSize(v.Type(), v.Len())
		iter := v.MapRange()
		for iter.Next() {
			out.SetMapIndex(iter.Key(), cloneCheckpointValue(iter.Value()))
		}
		return out
	case reflect.Struct:
		out := reflect.New(v.Type()).Elem()
		out.Set(v)
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).PkgPath == "" {
				out.Field(i).Set(cloneCheckpointValue(v.Field(i)))
			}
		}
		return out
	default:
		return v
	}
}
func cloneExecution(value *Execution) *Execution {
	if value == nil {
		return nil
	}
	return cloneCheckpointValue(reflect.ValueOf(value)).Interface().(*Execution)
}

// State returns a detached read projection. Nested pointers, maps, slices and JSON values are copied.
func (c *ExecutionController) State() *Execution {
	c.checkpoint.mu.RLock()
	defer c.checkpoint.mu.RUnlock()
	return cloneExecution(c.checkpoint.value)
}
func (c *ExecutionController) CheckpointRevision() uint64 {
	c.checkpoint.mu.RLock()
	defer c.checkpoint.mu.RUnlock()
	return c.checkpoint.revision
}

// MutateCheckpoint is the sole live mutation boundary. Validation sees a detached candidate;
// unknown members, wrong types and validation failures leave both value and revision unchanged.
func (c *ExecutionController) MutateCheckpoint(patch CheckpointPatch, validate ...func(*Execution) error) error {
	c.checkpoint.mu.Lock()
	defer c.checkpoint.mu.Unlock()
	if c.checkpoint.capFailure != nil {
		return c.checkpoint.capFailure
	}
	current := c.checkpoint.value
	if current == nil {
		current = &Execution{}
	}
	next, changed, err := MutateCheckpoint(current, patch, validate...)
	if err != nil {
		return err
	}
	if c.checkpoint.capConfigured && ((next.RequestCap == nil) != (c.checkpoint.requestCap == 0) || next.RequestCap != nil && *next.RequestCap != c.checkpoint.requestCap) {
		return requestCapRefusal("configuration")
	}
	if next.RequestCap != nil && (*next.RequestCap < 1 || *next.RequestCap > MaxTurns || next.Unit != "model-requests" || next.Limit != *next.RequestCap || next.Used < 0 || next.Used > *next.RequestCap || c.checkpoint.capConfigured && current != nil && next.Used < current.Used || len(next.Grants) != 0) {
		return requestCapRefusal("configuration")
	}
	if !changed && c.checkpoint.value != nil {
		return nil
	}
	c.checkpoint.value = next
	c.checkpoint.revision++
	return nil
}

// checkpointMembers supports initialization and restoration through the same mutation boundary.
func checkpointMembers(value *Execution) CheckpointPatch {
	patch := CheckpointPatch{}
	fields := reflect.ValueOf(value).Elem()
	for i := 0; i < fields.NumField(); i++ {
		if fields.Type().Field(i).PkgPath == "" {
			patch[fields.Type().Field(i).Name] = fields.Field(i).Interface()
		}
	}
	return patch
}

// checkpointWriter lets helpers publish a member patch while retaining only their own copy.
// A standalone helper owns its state; a session supplies its live mutation boundary.
func checkpointWriter(state *Execution, write []func(CheckpointPatch)) func(CheckpointPatch) {
	return func(patch CheckpointPatch) {
		holder := &ExecutionController{}
		holder.MutateCheckpoint(checkpointMembers(state))
		if err := holder.MutateCheckpoint(patch); err != nil {
			panic(err)
		}
		reflect.ValueOf(state).Elem().Set(reflect.ValueOf(holder.State()).Elem())
		if len(write) > 0 {
			write[0](patch)
		}
	}
}

// MutateCheckpoint applies a patch to a detached Execution or contract.Checkpoint projection.
// The live controller serializes this operation and increments its revision once on change.
type checkpointValue interface {
	Execution | *Execution | contract.Checkpoint | *contract.Checkpoint
}

func MutateCheckpoint[T checkpointValue](current T, patch CheckpointPatch, validate ...func(T) error) (T, bool, error) {
	next := cloneCheckpointValue(reflect.ValueOf(current)).Interface().(T)
	target := reflect.ValueOf(&next).Elem()
	if target.Kind() == reflect.Pointer {
		if target.IsNil() {
			return current, false, fmt.Errorf("nil checkpoint projection")
		}
		target = target.Elem()
	}
	for name, value := range patch {
		field := target.FieldByName(name)
		if !field.IsValid() || !field.CanSet() {
			return current, false, fmt.Errorf("unknown checkpoint member %q", name)
		}
		if value == nil {
			field.SetZero()
			continue
		}
		if delta, ok := value.(CheckpointIncrement); ok {
			if name != "Used" && name != "Limit" && name != "TestCalls" {
				return current, false, fmt.Errorf("checkpoint member %q is not a counter", name)
			}
			counter := field
			if counter.Kind() == reflect.Pointer {
				copied := reflect.New(counter.Type().Elem())
				if !counter.IsNil() {
					copied.Elem().Set(counter.Elem())
				}
				field.Set(copied)
				counter = copied.Elem()
			}
			if counter.Kind() != reflect.Int && counter.Kind() != reflect.Int64 {
				return current, false, fmt.Errorf("checkpoint member %q is not a counter", name)
			}
			before := counter.Int()
			after := before + int64(delta)
			if (delta > 0 && after < before) || (delta < 0 && after > before) || counter.OverflowInt(after) {
				return current, false, fmt.Errorf("checkpoint counter %q overflow", name)
			}
			counter.SetInt(after)
			continue
		}
		incoming := reflect.ValueOf(value)
		if !incoming.Type().AssignableTo(field.Type()) {
			return current, false, fmt.Errorf("invalid checkpoint member %q", name)
		}
		field.Set(cloneCheckpointValue(incoming))
	}
	for _, check := range validate {
		candidate := cloneCheckpointValue(reflect.ValueOf(next)).Interface().(T)
		if err := check(candidate); err != nil {
			return current, false, err
		}
	}
	return next, !checkpointEqual(reflect.ValueOf(current), reflect.ValueOf(next)), nil
}

// Equality ignores JSON object member order while preserving array order and omission.
func checkpointEqual(a, b reflect.Value) bool {
	if !a.IsValid() || !b.IsValid() {
		return a.IsValid() == b.IsValid()
	}
	if a.Type() != b.Type() {
		return false
	}
	if a.Type() == reflect.TypeOf((*Value)(nil)) {
		if a.IsNil() || b.IsNil() {
			return a.IsNil() == b.IsNil()
		}
		return CanonicalJSON(a.Interface().(*Value)) == CanonicalJSON(b.Interface().(*Value))
	}
	if a.Type() == reflect.TypeOf(json.RawMessage{}) {
		av, ae := ParseJSON(a.Interface().(json.RawMessage))
		bv, be := ParseJSON(b.Interface().(json.RawMessage))
		if ae == nil && be == nil {
			return CanonicalJSON(av) == CanonicalJSON(bv)
		}
	}
	switch a.Kind() {
	case reflect.Interface, reflect.Pointer:
		if a.IsNil() || b.IsNil() {
			return a.IsNil() == b.IsNil()
		}
		return checkpointEqual(a.Elem(), b.Elem())
	case reflect.Map:
		if a.IsNil() != b.IsNil() || a.Len() != b.Len() {
			return false
		}
		for _, key := range a.MapKeys() {
			if !checkpointEqual(a.MapIndex(key), b.MapIndex(key)) {
				return false
			}
		}
		return true
	case reflect.Slice:
		if a.IsNil() != b.IsNil() || a.Len() != b.Len() {
			return false
		}
		for i := 0; i < a.Len(); i++ {
			if !checkpointEqual(a.Index(i), b.Index(i)) {
				return false
			}
		}
		return true
	case reflect.Struct:
		for i := 0; i < a.NumField(); i++ {
			if a.Type().Field(i).PkgPath == "" && !checkpointEqual(a.Field(i), b.Field(i)) {
				return false
			}
		}
		return true
	default:
		return reflect.DeepEqual(a.Interface(), b.Interface())
	}
}

func requestCapRefusal(code string) error {
	f := failure.New(code, "context", "not-started")
	detail := "The fixed request cap must match the admitted API execution and cannot change after initialization."
	f.Detail = &detail
	return failure.NewError(f)
}
func (c *ExecutionController) requestCapFailure() error {
	c.checkpoint.mu.RLock()
	defer c.checkpoint.mu.RUnlock()
	return c.checkpoint.capFailure
}

// configureRequestCap copies trusted admission; only a fresh helper may establish it later.
func (c *ExecutionController) configureRequestCap(cap int, helper bool) error {
	c.checkpoint.mu.Lock()
	defer c.checkpoint.mu.Unlock()
	if c.checkpoint.capFailure != nil {
		return c.checkpoint.capFailure
	}
	if !helper && c.checkpoint.capConfigured && (c.checkpoint.requestCap != 0 || cap != 0) {
		return requestCapRefusal("configuration")
	}
	if helper {
		state := c.checkpoint.value
		if cap < 1 || cap > MaxTurns || state == nil || state.Unit != "model-requests" || state.Used != 0 || len(state.Grants) != 0 || c.restored || c.checkpoint.requestCap != 0 && c.checkpoint.requestCap != cap {
			c.checkpoint.capFailure = requestCapRefusal("configuration")
			return c.checkpoint.capFailure
		}
		next, changed, err := MutateCheckpoint(state, CheckpointPatch{"RequestCap": &cap, "Limit": cap})
		if err != nil {
			c.checkpoint.capFailure = err
			return err
		}
		if changed {
			c.checkpoint.value = next
			c.checkpoint.revision++
		}
	}
	c.checkpoint.requestCap = cap
	c.checkpoint.capConfigured = true
	return nil
}
