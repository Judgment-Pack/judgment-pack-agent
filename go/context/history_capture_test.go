package runcontext

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func captureFixture(t *testing.T) (capturePort, map[string][]byte, *Value) {
	t.Helper()
	raw, e := os.ReadFile("../../contract/fixtures/history/capture.json")
	if e != nil {
		t.Fatal(e)
	}
	v, e := ParseJSON(raw)
	if e != nil {
		t.Fatal(e)
	}
	s := v.Get("source")
	var head RunHistoryHead
	var commits []RunHistoryEntry
	var metadata []HistoryMetadataEntry
	json.Unmarshal(s.Get("head").JSON(), &head)
	json.Unmarshal(s.Get("commits").JSON(), &commits)
	json.Unmarshal(s.Get("metadata").JSON(), &metadata)
	objects := map[string][]byte{}
	for _, m := range s.Get("objects").o {
		b, e := base64.StdEncoding.DecodeString(m.val.Str())
		if e != nil {
			t.Fatal(e)
		}
		objects[m.key] = b
	}
	return capturePort{head, commits, metadata, func(r HistoryObjectRef, _ ...string) ([]byte, error) {
		b, ok := objects[r.Digest]
		if !ok {
			return nil, &ReplayUnavailable{}
		}
		return append([]byte(nil), b...), nil
	}}, objects, v
}
func TestHistoryCaptureHistoricalMetadataWithoutOriginalBindings(t *testing.T) {
	port, _, v := captureFixture(t)
	if _, e := ReadCapturedCorpus(port); e == nil {
		t.Fatal("historical native capture without original binding evidence became replayable")
	}
	expectedBytes := []byte(CanonicalJSON(v.Get("capture")))
	capture, e := ReadRunCapture(expectedBytes)
	if e != nil {
		t.Fatal(e)
	}
	actualBytes, e := canonicalBytes(capture)
	if e != nil || !bytes.Equal(actualBytes, expectedBytes) {
		t.Fatal("capture canonical byte roundtrip", e)
	}
}
func TestHistoryCaptureRefusesMissingOrContradictoryClosure(t *testing.T) {
	for _, fault := range []string{"missing", "duplicate", "order", "unknown", "digest", "files", "metadata", "request-source", "native-tools", "manifest-extra", "response-shape"} {
		t.Run(fault, func(t *testing.T) {
			p, objects, _ := captureFixture(t)
			last := &p.commits[1]
			ref := *p.metadata[0].Record.Capture
			change := func(ref HistoryObjectRef, fn func(*Value)) HistoryObjectRef {
				v := MustParseJSON(string(objects[ref.Digest]))
				fn(v)
				raw := []byte(CanonicalJSON(v))
				r := HistoryObjectRef{Digest: HistoryBytesDigest(raw), Bytes: len(raw)}
				objects[r.Digest] = raw
				return r
			}
			replaceFile := func(name string, fn func(*Value)) {
				cap := MustParseJSON(string(objects[ref.Digest]))
				for _, file := range cap.Get("files").Items() {
					if file.Get("name").Str() != name {
						continue
					}
					manifest := MustParseJSON(string(objects[archiveRef(file.Get("manifest")).Digest]))
					full := []byte{}
					for _, part := range manifest.Get("parts").Items() {
						full = append(full, objects[archiveRef(part).Digest]...)
					}
					value := MustParseJSON(string(full))
					fn(value)
					raw := []byte(CanonicalJSON(value))
					part := HistoryObjectRef{Digest: HistoryBytesDigest(raw), Bytes: len(raw)}
					objects[part.Digest] = raw
					m := ObjectOf("version", Number(1), "bytes", Number(float64(len(raw))), "digest", String(part.Digest), "parts", Array(historyRefJSON(part)))
					raw = []byte(CanonicalJSON(m))
					mr := HistoryObjectRef{Digest: HistoryBytesDigest(raw), Bytes: len(raw)}
					objects[mr.Digest] = raw
					file.Set("manifest", historyRefJSON(mr))
					break
				}
				raw := []byte(CanonicalJSON(cap))
				r := HistoryObjectRef{Digest: HistoryBytesDigest(raw), Bytes: len(raw)}
				objects[r.Digest] = raw
				p.metadata[0].Record.Capture = &r
			}
			switch fault {
			case "native-tools":
				replaceFile("requests.json", func(v *Value) {
					v.Items()[1].Get("response").Set("body", String(`{"steps":[{"tool":{"id":"invisible","name":"lookup","arguments":{}}}]}`))
				})
			case "manifest-extra":
				replaceFile("manifest.json", func(v *Value) { v.Set("extra", Bool(true)) })
			case "response-shape":
				replaceFile("requests.json", func(v *Value) { v.Items()[0].Get("response").Set("headers", Array()) })
			case "metadata":
				p.head.Metadata.Count = 2
			case "request-source":
				r := change(last.Observations[0], func(v *Value) { v.Set("source", String("foreign")) })
				last.Observations[0] = r
			case "missing":
				last.Observations = last.Observations[:1]
			case "duplicate":
				last.Observations = append(last.Observations, last.Observations[1])
			case "files":
				v := MustParseJSON(string(objects[ref.Digest]))
				r := archiveRef(v.Get("files").Items()[0].Get("manifest"))
				delete(objects, r.Digest)
			case "order":
				r := change(ref, func(v *Value) {
					rows := v.Get("requests")
					rows.a[0], rows.a[1] = rows.a[1], rows.a[0]
					for i, r := range rows.a {
						r.Set("captureIndex", Number(float64(i)))
					}
				})
				p.metadata[0].Record.Capture = &r
			case "unknown":
				r := change(*last.Checkpoint, func(v *Value) { v.Get("durable").Get("dispatches").Items()[0].Set("state", String("unknown")) })
				last.Checkpoint = &r
				p.head.Checkpoint = &r
				replaceFile("checkpoint.json", func(v *Value) { v.Get("durable").Get("dispatches").Items()[0].Set("state", String("unknown")) })
				updated := change(*p.metadata[0].Record.Capture, func(v *Value) { v.Set("checkpoint", historyRefJSON(r)) })
				p.metadata[0].Record.Capture = &updated
			case "digest":
				r := change(ref, func(v *Value) {
					v.Get("requests").Items()[0].Set("requestDigest", String("sha256:"+strings.Repeat("0", 64)))
				})
				p.metadata[0].Record.Capture = &r
			}
			var previous *string
			for i := range p.commits {
				c := &p.commits[i]
				c.PreviousDigest = previous
				v := MustParseJSON(historyInputText(*c))
				c.Digest = HistoryBytesDigest([]byte(CanonicalJSON(recordWithoutDigest(v))))
				sum := c.Digest
				previous = &sum
			}
			p.head.Root = *previous
			row := &p.metadata[0]
			v := MustParseJSON(historyInputText(*row))
			row.Digest = HistoryBytesDigest([]byte(CanonicalJSON(recordWithoutDigest(v))))
			p.head.Metadata.Root = &row.Digest
			if _, e := ReadCapturedCorpus(p); e == nil {
				t.Fatal("invalid native capture accepted", fault)
			}
		})
	}
}

func TestHistoryCaptureNilViewIsUnavailable(t *testing.T) {
	if _, e := ReadCapturedCorpus((*RetainedRunHistory)(nil)); e == nil {
		t.Fatal("nil capture view admitted")
	}
	if _, e := ReadArchiveCapturedCorpus(nil); e == nil {
		t.Fatal("nil archive capture admitted")
	}
}
