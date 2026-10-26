package adapter_test

import (
	"context"
	"encoding/json"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/eino"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/relay"
	"net/http"
	"testing"
)

type alteredCaptureView struct {
	source   *runcontext.RetainedRunHistory
	head     runcontext.RunHistoryHead
	metadata []runcontext.HistoryMetadataEntry
	objects  map[string][]byte
}

func (v alteredCaptureView) Head() runcontext.RunHistoryHead             { return v.head }
func (v alteredCaptureView) Commits() []runcontext.RunHistoryEntry       { return v.source.Commits() }
func (v alteredCaptureView) Metadata() []runcontext.HistoryMetadataEntry { return v.metadata }
func (v alteredCaptureView) ReadObject(ref runcontext.HistoryObjectRef) ([]byte, error) {
	if raw, ok := v.objects[ref.Digest]; ok {
		return append([]byte(nil), raw...), nil
	}
	return v.source.ReadObject(ref)
}
func captureFixtureCanonical(v any) []byte {
	raw, e := json.Marshal(v)
	if e != nil {
		panic(e)
	}
	return []byte(runcontext.CanonicalJSON(runcontext.MustParseJSON(string(raw))))
}
func alterOriginalCapture(t *testing.T, source *runcontext.RetainedRunHistory, fault string) alteredCaptureView {
	t.Helper()
	v := alteredCaptureView{source: source, head: source.Head(), metadata: source.Metadata(), objects: map[string][]byte{}}
	var metadata []runcontext.HistoryMetadataEntry
	_ = json.Unmarshal(captureFixtureCanonical(v.metadata), &metadata)
	v.metadata = metadata
	row := &v.metadata[0]
	raw, e := v.ReadObject(*row.Record.Capture)
	if e != nil {
		t.Fatal(e)
	}
	var capture map[string]any
	_ = json.Unmarshal(raw, &capture)
	file := "requests.json"
	if fault == "extra-event" || fault == "missing-event" || fault == "reordered-events" {
		file = "events.json"
	}
	if fault == "projection-absent" {
		delete(capture, "eventProjection")
	} else if fault == "projection-invalid" {
		capture["eventProjection"] = "transcript-v2"
	} else {
		for _, item := range capture["files"].([]any) {
			entry := item.(map[string]any)
			if entry["name"] != file {
				continue
			}
			var ref runcontext.HistoryObjectRef
			_ = json.Unmarshal(captureFixtureCanonical(entry["manifest"]), &ref)
			encoded, e := v.ReadObject(ref)
			if e != nil {
				t.Fatal(e)
			}
			m, e := runcontext.ReadHistoryChunkManifest(encoded)
			if e != nil {
				t.Fatal(e)
			}
			full := []byte{}
			for _, part := range m.Parts {
				raw, e := v.ReadObject(part)
				if e != nil {
					t.Fatal(e)
				}
				full = append(full, raw...)
			}
			var rows []any
			_ = json.Unmarshal(full, &rows)
			switch fault {
			case "extra-event":
				rows = append(rows, map[string]any{"version": 1, "type": "message", "text": "Extra."})
			case "missing-event":
				rows = rows[1:]
			case "reordered-events":
				rows[0], rows[1] = rows[1], rows[0]
			case "request-digest":
				request := rows[0].(map[string]any)["request"].(map[string]any)
				request["prompt"] = request["prompt"].(string) + " altered"
			default:
				headers := rows[0].(map[string]any)["response"].(map[string]any)["headers"].(map[string]any)
				if fault == "alias-absent" {
					delete(headers, "x-jp-native-tool-bindings")
				} else {
					var mapping map[string]string
					_ = json.Unmarshal([]byte(headers["x-jp-native-tool-bindings"].(string)), &mapping)
					for key := range mapping {
						if fault == "alias-tampered" {
							mapping[key] = "different-source"
						} else {
							delete(mapping, key)
						}
						break
					}
					headers["x-jp-native-tool-bindings"] = string(captureFixtureCanonical(mapping))
				}
			}
			manifest, parts, e := runcontext.ChunkHistoryValue(captureFixtureCanonical(rows))
			if e != nil {
				t.Fatal(e)
			}
			for _, o := range append(parts, manifest) {
				v.objects[o.Digest] = o.Value
			}
			entry["manifest"] = manifest.HistoryObjectRef
		}
	}
	object, e := runcontext.NewHistoryObject(captureFixtureCanonical(capture))
	if e != nil {
		t.Fatal(e)
	}
	v.objects[object.Digest] = object.Value
	row.Record.Capture = &object.HistoryObjectRef
	var body map[string]any
	_ = json.Unmarshal(captureFixtureCanonical(*row), &body)
	delete(body, "digest")
	row.Digest = runcontext.HistoryBytesDigest(captureFixtureCanonical(body))
	headRaw := captureFixtureCanonical(v.head)
	_ = json.Unmarshal(headRaw, &v.head)
	v.head.Metadata.Root = &row.Digest
	return v
}
func TestOriginalProjectedCaptureTamperRefusals(t *testing.T) {
	_, source := originalNativeToolCapture(t)
	for _, fault := range []string{"extra-event", "missing-event", "reordered-events", "projection-absent", "projection-invalid", "alias-absent", "alias-tampered", "alias-partial", "request-digest"} {
		t.Run(fault, func(t *testing.T) {
			v := alterOriginalCapture(t, source, fault)
			if _, e := runcontext.ReadCapturedCorpusView(v); e == nil {
				t.Fatal("altered original projected capture accepted", fault)
			}
		})
	}
}
func TestOriginalHTTPModelCannotSupplyNativeBinding(t *testing.T) {
	store := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
	sink := &originalCaptureSink{}
	run := originalCaptureRun(store, sink)
	run.Context.Objective = ""
	run.Model = func(context.Context, string, relay.Request) (*http.Response, error) {
		response := usageReply()
		response.Header.Set("x-jp-native-tool-bindings", "{}")
		return response, nil
	}
	if e := eino.New().Run(context.Background(), run); e != nil {
		t.Fatal(e)
	}
	if sink.calls != 0 {
		t.Fatal("HTTP native binding spoof reached sink")
	}
	head, e := store.ReadHistoryHead(context.Background(), "original-live")
	if e != nil || head.Metadata != nil {
		t.Fatal("HTTP native binding spoof published capture", e)
	}
}
