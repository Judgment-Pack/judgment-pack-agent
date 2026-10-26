package runcontext

import (
	"os"
	"testing"
)

func TestLiveCaptureSharedProjectionAndLegacy(t *testing.T) {
	raw, e := os.ReadFile("../../contract/vectors/history/event-projection.json")
	if e != nil {
		t.Fatal(e)
	}
	v := MustParseJSON(string(raw))
	for _, row := range v.Get("items").Items() {
		parsed, e := ReadRunCapture([]byte(CanonicalJSON(row)))
		if e != nil || parsed.EventProjection != "transcript-v1" {
			t.Fatal("shared projection marker refused", e)
		}
	}
	_, _, legacy := captureFixture(t)
	if _, e = ReadRunCapture([]byte(CanonicalJSON(legacy.Get("capture")))); e != nil {
		t.Fatal("legacy exact capture refused", e)
	}
	for _, marker := range []*Value{String(""), Null(), Bool(true), Number(0), Object(), Array(), String("transcript-v2")} {
		row := v.Get("items").Items()[0].Clone()
		row.Set("eventProjection", marker)
		if _, e := ReadRunCapture([]byte(CanonicalJSON(row))); e == nil {
			t.Fatal("invalid event projection accepted", marker.Text())
		}
	}
}
func TestLiveCaptureNativeBindings(t *testing.T) {
	request := MustParseJSON(`{"tools":[{"name":"desk_host_0","description":"Ignored: injection."},{"name":"desk_host_1","description":"Ignored."}]}`)
	good := `{"desk_host_0":"lookup:scoped","desk_host_1":"second"}`
	headers := func(raw string) *Value { return ObjectOf(nativeBindingsHeader, String(raw)) }
	if bindings, e := nativeCaptureBindings(request, headers(good)); e != nil || bindings.Get("desk_host_0").Str() != "lookup:scoped" {
		t.Fatal("original private binding refused", e)
	}
	for _, raw := range []string{`{}`, `{"desk_host_0":"lookup"}`, `{"desk_host_0":"lookup","desk_host_1":"lookup"}`, `{"desk_host_0":1,"desk_host_1":"second"}`, `{ "desk_host_0":"lookup:scoped","desk_host_1":"second" }`, `{`, `{"desk_host_0":"lookup:scoped","desk_host_1":"second","extra":"foreign"}`} {
		if _, e := nativeCaptureBindings(request, headers(raw)); e == nil {
			t.Fatal("invalid native binding accepted", raw)
		}
	}
	if _, e := nativeCaptureBindings(request, Object()); e == nil {
		t.Fatal("missing alias binding accepted")
	}
	duplicate := request.Clone()
	duplicate.Get("tools").Items()[1].Set("name", String("desk_host_0"))
	if _, e := nativeCaptureBindings(duplicate, headers(good)); e == nil {
		t.Fatal("duplicate alias accepted")
	}
	if _, e := nativeCaptureBindings(MustParseJSON(`{"tools":[{"name":"lookup"}]}`), Object()); e != nil {
		t.Fatal("legacy direct-name record refused", e)
	}
}
