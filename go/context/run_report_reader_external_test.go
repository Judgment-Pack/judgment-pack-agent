package runcontext_test

import (
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"

	rc "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

func retainedFixture(t *testing.T, name string) *rc.Value {
	t.Helper()
	raw, err := os.ReadFile("../../contract/vectors/" + name)
	if err != nil {
		t.Fatal(err)
	}
	value, err := rc.ParseJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
func invalidRetained(t *testing.T, value *rc.Value) {
	t.Helper()
	_, err := rc.ReadRunReport(value)
	var invalid *rc.RunReportInvalid
	if !errors.As(err, &invalid) {
		t.Fatalf("expected fixed RunReportInvalid, got %v", err)
	}
	_, err = rc.ParseRunReport(value.JSON())
	if !errors.As(err, &invalid) {
		t.Fatalf("expected byte RunReportInvalid, got %v", err)
	}
	if err.Error() != "RunReportInvalid: the retained run report is invalid or unsupported." {
		t.Fatal("unbounded error")
	}
}
func TestRetainedRunReportShared(t *testing.T) {
	all := append(retainedFixture(t, "report/reports.json").Get("items").Items(), retainedFixture(t, "report/retained.json").Get("items").Items()...)
	for _, row := range all {
		before := rc.CanonicalJSON(row)
		parsed, err := rc.ParseRunReport([]byte(before))
		if err != nil {
			t.Fatalf("%s: %v", row.Get("id").Str(), err)
		}
		text, err := rc.CanonicalRunReport(parsed)
		if err != nil || text != before {
			t.Fatalf("byte roundtrip differs: %v", err)
		}
		held, err := rc.ReadRunReport(row)
		if err != nil {
			t.Fatal(err)
		}
		held.Set("explanation", rc.String("mutated copy"))
		if rc.CanonicalJSON(row) != before {
			t.Fatal("reader changed retained input")
		}
	}
	minimal, _ := rc.ReadRunReport(all[0])
	if minimal.Has("usage") {
		t.Fatal("invented historical usage")
	}
	empty, _ := rc.ReadRunReport(all[2])
	if !empty.Has("usage") || empty.Get("usage").Get("rows").Len() != 0 {
		t.Fatal("lost explicit empty usage")
	}
}
func TestRetainedRunReportSharedInvalid(t *testing.T) {
	fixture := retainedFixture(t, "report/retained.json")
	rich := retainedFixture(t, "report/reports.json").Get("items").Items()[1]
	for _, row := range fixture.Get("invalid").Items() {
		t.Run(row.Get("name").Str(), func(t *testing.T) {
			value := rich.Clone()
			if base := row.Get("base"); base != nil {
				value = fixture.Get("items").Items()[int(base.Num())].Clone()
			}
			path := row.Get("path").Items()
			target := value
			for _, key := range path[:len(path)-1] {
				name := key.Str()
				if key.Kind() == rc.KindNumber {
					name = strconv.Itoa(int(key.Num()))
				}
				target = target.Get(name)
			}
			last := path[len(path)-1]
			key := last.Str()
			if last.Kind() == rc.KindNumber {
				key = strconv.Itoa(int(last.Num()))
			}
			if row.Get("remove").Truth() {
				replacement := rc.Object()
				for _, name := range target.Keys() {
					if name != key {
						replacement.Set(name, target.Get(name))
					}
				}
				if len(path) == 1 {
					value = replacement
				} else {
					parent := value
					for _, bit := range path[:len(path)-2] {
						name := bit.Str()
						if bit.Kind() == rc.KindNumber {
							name = strconv.Itoa(int(bit.Num()))
						}
						parent = parent.Get(name)
					}
					bit := path[len(path)-2]
					if bit.Kind() == rc.KindNumber {
						parent.Items()[int(bit.Num())] = replacement
					} else {
						parent.Set(bit.Str(), replacement)
					}
				}
			} else {
				target.Set(key, row.Get("value").Clone())
			}
			invalidRetained(t, value)
		})
	}
}
func TestRetainedRunReportRealPlanProjections(t *testing.T) {
	for _, file := range []string{"plan/checkpoints.json", "plan/extended-checkpoints.json"} {
		for _, fixture := range retainedFixture(t, file).Get("items").Items() {
			value := fixture.Clone().Set("status", rc.String("stopped"))
			var cp contract.Checkpoint
			if err := json.Unmarshal(value.JSON(), &cp); err != nil {
				t.Fatal(err)
			}
			projected, err := rc.ProjectRunReport(cp)
			if err != nil {
				t.Fatal(err)
			}
			before, _ := rc.CanonicalRunReport(projected)
			actual, err := rc.ParseRunReport([]byte(before))
			if err != nil {
				t.Fatal(err)
			}
			if rc.CanonicalJSON(actual.Get("plan")) != rc.CanonicalJSON(value.Get("plan")) || rc.CanonicalJSON(actual.Get("planState")) != rc.CanonicalJSON(value.Get("planState")) {
				t.Fatal("changed handwritten Plan/evidence history")
			}
		}
	}
}
func TestRetainedRunReportByteParsing(t *testing.T) {
	good := rc.CanonicalJSON(retainedFixture(t, "report/retained.json").Get("items").Items()[1])
	var invalid *rc.RunReportInvalid
	for _, spelling := range []string{"9007199254740991.1", "9007199254740992", "9.0071992547409911e15", "1e999", "1e-999", "-1", "NaN", "Infinity"} {
		_, err := rc.ParseRunReport([]byte(strings.Replace(good, "9007199254740991", spelling, 1)))
		if !errors.As(err, &invalid) {
			t.Fatalf("rounded %s: %v", spelling, err)
		}
	}
	for _, raw := range []string{"", "{", "null", "[]", "{}", `{"version":null}`, `{"version":"1"}`, `{"version":true}`, "true", "1", `"report"`, good + "garbage", strings.Replace(good, `"version":1`, `"__proto__":{},"version":1`, 1), strings.Replace(good, `"version":1`, `"version":1,"version":1`, 1), strings.Replace(good, `"inputTokens":9007199254740991`, `"inputTokens":1,"inputTokens":2`, 1), strings.Replace(good, "Καλημέρα", `\ud800`, 1), strings.Replace(good, "Καλημέρα", `\udc00`, 1), "\ufeff" + good, string([]byte{255}), string([]byte{0xed, 0xa0, 0x80})} {
		_, err := rc.ParseRunReport([]byte(raw))
		if !errors.As(err, &invalid) {
			t.Fatalf("malformed bytes accepted: %v", err)
		}
	}
	for _, spelling := range []string{"90071992547409910e-1", "9007199254740991.0", "9.007199254740991e15"} {
		held, err := rc.ParseRunReport([]byte(strings.Replace(good, "9007199254740991", spelling, 1)))
		if err != nil || rc.CanonicalJSON(held) != good {
			t.Fatalf("exact number refused/changed: %v", err)
		}
	}
	zero, zeroErr := rc.ParseRunReport([]byte(strings.Replace(good, `"outputTokens":0`, `"outputTokens":0e999999999999999999999999999`, 1)))
	if zeroErr != nil || rc.CanonicalJSON(zero) != good {
		t.Fatal("exact zero exponent changed", zeroErr)
	}
	held, err := rc.ParseRunReport([]byte(strings.Replace(good, "🌿", `\ud83c\udf3f`, 1)))
	if err != nil || rc.CanonicalJSON(held) != good {
		t.Fatal("valid surrogate pair changed", err)
	}
}
func TestRetainedRunReportBounds(t *testing.T) {
	row := retainedFixture(t, "report/reports.json").Get("items").Items()[0]
	minimal := rc.CanonicalJSON(row)
	padded := minimal + strings.Repeat(" ", rc.MaxRunReportBytes-len(minimal))
	if _, err := rc.ParseRunReport([]byte(padded)); err != nil {
		t.Fatal("exact input byte ceiling refused", err)
	}
	var overflow *rc.RunReportOverflow
	for _, raw := range []string{padded + " ", strings.Repeat("[", 66) + "0" + strings.Repeat("]", 66)} {
		if _, err := rc.ParseRunReport([]byte(raw)); !errors.As(err, &overflow) {
			t.Fatalf("overflow accepted: %v", err)
		}
	}
	if _, err := rc.ReadRunReport(row.Clone().Set("explanation", rc.String(strings.Repeat("x", rc.MaxRunReportBytes)))); !errors.As(err, &overflow) {
		t.Fatalf("object overflow accepted: %v", err)
	}
	deep := rc.Number(0)
	for range 66 {
		deep = rc.Array(deep)
	}
	if _, err := rc.ReadRunReport(deep); !errors.As(err, &overflow) {
		t.Fatal("object depth overflow accepted", err)
	}
	cycle := row.Clone()
	cycle.Set("extra", cycle)
	if _, err := rc.ReadRunReport(cycle); err == nil {
		t.Fatal("cycle accepted")
	}
	rows := rc.Array()
	for range 128 {
		rows.Append(rc.ObjectOf("source", rc.String("m"), "connection", rc.String("c"), "role", rc.String("a"), "requests", rc.Number(0), "invocations", rc.Number(0), "milliseconds", rc.Number(0), "bytes", rc.Number(0)))
	}
	children := rc.Array()
	for i := range 250 {
		children.Append(rc.ObjectOf("id", rc.String("child-"+strconv.Itoa(i)), "usage", rc.ObjectOf("rows", rows)))
	}
	large := row.Clone().Set("childUsage", children)
	if len(large.JSON()) >= rc.MaxRunReportBytes {
		t.Fatal("large node test setup exceeds bytes")
	}
	if _, err := rc.ReadRunReport(large); err != nil {
		t.Fatal("valid report with >200000 nodes refused", err)
	}
}

func TestRunReportMalformedRootsRefuseWithoutPanic(t *testing.T) {
	for _, raw := range []string{"null", "[]", "{}", `{"version":null}`, `{"version":"1"}`, `{"version":true}`, "true", "1", `"report"`} {
		for _, entry := range []string{"bytes", "value"} {
			t.Run(entry+"/"+raw, func(t *testing.T) {
				defer func() {
					if recovered := recover(); recovered != nil {
						t.Fatalf("malformed report root panicked instead of refusing: %v", recovered)
					}
				}()
				var value *rc.Value
				var err error
				if entry == "bytes" {
					value, err = rc.ParseRunReport([]byte(raw))
				} else {
					value, err = rc.ReadRunReport(rc.MustParseJSON(raw))
				}
				var invalid *rc.RunReportInvalid
				if value != nil || !errors.As(err, &invalid) {
					t.Fatalf("malformed report root returned a value or wrong refusal: %v", err)
				}
			})
		}
	}
}
