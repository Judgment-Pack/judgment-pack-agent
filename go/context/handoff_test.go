package runcontext

import "testing"

// The delegation handoff on an agent capability (ADR-0007), read closed: every item of the vector is
// one the reader admits, inside a checkpoint and alone; a member it does not name, a state outside
// the four, an answer before the call is answered, none after, a task the request did not ask for
// and a task id twice are each refused, and the checkpoint carrying it with them.
func TestTheHandoffIsReadClosed(t *testing.T) {
	items := vectorItems(t, "delegation/handoff.json")
	if len(items) != 4 {
		t.Fatalf("%d items", len(items))
	}
	checkpoint := vectorItems(t, "delegation/checkpoint.json")[0]
	for at, item := range items {
		if !ValidHandoff(item) {
			t.Errorf("item %d was refused", at)
		}
		held := checkpoint.Clone()
		held.Set("handoff", item.Clone())
		if _, err := ReadExecution(held); err != nil {
			t.Errorf("a checkpoint carrying item %d was refused: %v", at, err)
		}
	}
	broken := map[string]func(v *Value){
		"a member it does not name": func(v *Value) { v.Set("note", String("kept")) },
		"a state outside the four":  func(v *Value) { v.Set("state", String("running")) },
		"an answer before the call is answered": func(v *Value) {
			v.Set("answer", items[2].Get("answer").Clone())
		},
		"a task the request did not ask for": func(v *Value) {
			tasks := v.Get("tasks").Items()
			v.Set("tasks", Array(append(tasks, tasks[0].Clone(), tasks[0].Clone())...))
		},
		"a task id twice": func(v *Value) {
			tasks := v.Get("tasks").Items()
			v.Set("tasks", Array(tasks[0].Clone(), tasks[0].Clone()))
		},
		"an empty call id": func(v *Value) { v.Set("callId", String("")) },
	}
	for name, change := range broken {
		held := items[1].Clone()
		change(held)
		if ValidHandoff(held) {
			t.Errorf("%s was admitted", name)
		}
		within := checkpoint.Clone()
		within.Set("handoff", held)
		if _, err := ReadExecution(within); err == nil {
			t.Errorf("a checkpoint carrying %s was admitted", name)
		}
	}
	answered := Object()
	for _, key := range items[3].Keys() {
		if key != "answer" {
			answered.Set(key, items[3].Get(key).Clone())
		}
	}
	if ValidHandoff(answered) {
		t.Error("a continued handoff with no answer was admitted")
	}
}
