package failure

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

func repositoryFile(t *testing.T, parts ...string) []byte {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		path := filepath.Join(append([]string{dir}, parts...)...)
		if raw, err := os.ReadFile(path); err == nil {
			return raw
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no %s above the working directory", filepath.Join(parts...))
		}
		dir = parent
	}
}

// The catalogue in Go is contract/desk/catalog.json: every class, message, recovery and alias, in
// its order.
func TestTheCatalogueIsTheDesksFile(t *testing.T) {
	var file struct {
		Version  int               `json:"version"`
		Failures map[string]Entry  `json:"failures"`
		Aliases  map[string]string `json:"aliases"`
	}
	raw := repositoryFile(t, "contract", "desk", "catalog.json")
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	if file.Version != CatalogVersion {
		t.Errorf("version %d, not %d", CatalogVersion, file.Version)
	}
	if len(file.Failures) != len(Catalog) || len(Codes) != len(Catalog) {
		t.Fatalf("%d classes in the file, %d here", len(file.Failures), len(Catalog))
	}
	for code, entry := range file.Failures {
		if Catalog[code] != entry {
			t.Errorf("%s: %+v, not %+v", code, Catalog[code], entry)
		}
	}
	if fmt.Sprint(Aliases) != fmt.Sprint(file.Aliases) {
		t.Errorf("the aliases differ")
	}
	var ordered struct {
		Failures json.RawMessage `json:"failures"`
	}
	_ = json.Unmarshal(raw, &ordered)
	decoder := json.NewDecoder(bytes.NewReader(ordered.Failures))
	var keys []string
	if _, err := decoder.Token(); err != nil {
		t.Fatal(err)
	}
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			t.Fatal(err)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, key.(string))
	}
	if !slices.Equal(keys, Codes) {
		t.Errorf("the order is %v, not %v", Codes, keys)
	}
}

type refusal struct {
	status int
	body   string
	header http.Header
}

func (r refusal) Error() string               { return "refused" }
func (r refusal) StatusCode() int             { return r.status }
func (r refusal) ResponseBody() string        { return r.body }
func (r refusal) ResponseHeader() http.Header { return r.header }

// Normalize classifies by typed codes and statuses, as normalizeFailure does.
func TestNormalizeClassifiesAsTheTypeScriptReaderDoes(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	for _, row := range []struct {
		cause   error
		code    string
		outcome string
		status  int64
	}{
		{refusal{status: 429}, "rate-limited", "refused", 429},
		{refusal{status: 400, body: `{"error":{"type":"overloaded_error"}}`}, "unavailable", "refused", 400},
		{refusal{status: 500}, "unavailable", "unknown", 500},
		{refusal{status: 408}, "timeout", "unknown", 408},
		{refusal{status: 401}, "authentication", "refused", 401},
		{refusal{status: 400, body: `{"error":{"code":"context_length_exceeded"}}`}, "context-limit", "refused", 400},
		{refusal{status: 503, body: `{"code":"busy"}`}, "busy", "not-started", 503},
		{fmt.Errorf("wrapped: %w", refusal{status: 403}), "permission", "refused", 403},
		{Cancelled, "cancelled", "unknown", 0},
		{errors.New("plain"), "unknown", "unknown", 0},
		{Authored("configuration", "a host sentence", "desk"), "configuration", "unknown", 0},
	} {
		got := Normalize(row.cause, "model", now)
		status := int64(0)
		if got.Status != nil {
			status = *got.Status
		}
		if got.Code != row.code || got.Outcome != row.outcome || status != row.status {
			t.Errorf("%v: %s %s %d, not %s %s %d", row.cause, got.Code, got.Outcome, status, row.code, row.outcome, row.status)
		}
	}
	got := Normalize(refusal{status: 429, header: http.Header{"Retry-After": {"2"}, "X-Request-Id": {"req_1"}}}, "model", now)
	if got.RetryAt == nil || *got.RetryAt != "2026-10-19T12:00:02.000Z" || got.RequestID == nil || *got.RequestID != "req_1" {
		t.Errorf("the metadata is %v %v", got.RetryAt, got.RequestID)
	}
	if !CanRetryRefusal(Normalize(refusal{status: 429}, "model", now), "") {
		t.Error("a 429 is not retried")
	}
	if CanRetryRefusal(Normalize(refusal{status: 500}, "model", now), "") {
		t.Error("a 500 is retried")
	}
	overloaded := refusal{status: 400, body: `{"error":{"type":"overloaded_error"}}`}
	if !CanRetryRefusal(Normalize(overloaded, "model", now), RefusalCode(overloaded.body)) {
		t.Error("an overload refusal is not retried")
	}
}

// Every failure vector's error event says what the catalogue says for its class.
func TestTheFailureVectorsCarryTheCataloguesSentences(t *testing.T) {
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "contract", "vectors", "failures")); err == nil {
			break
		}
		dir = filepath.Dir(dir)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "contract", "vectors", "failures"))
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, entry := range entries {
		raw, err := os.ReadFile(filepath.Join(dir, "contract", "vectors", "failures", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var vector struct {
			Items []contract.RunEvent `json:"items"`
		}
		if err := json.Unmarshal(raw, &vector); err != nil {
			t.Fatalf("%s: %v", entry.Name(), err)
		}
		for _, event := range vector.Items {
			if event.Error == nil || event.Error.Failure == nil {
				continue
			}
			f, ok := Read(*event.Error.Failure)
			if !ok {
				t.Errorf("%s: the failure is not one the reader admits", entry.Name())
				continue
			}
			if Message(f) != event.Error.Message {
				t.Errorf("%s: the message is %q, the catalogue's %q", entry.Name(), event.Error.Message, Message(f))
			}
			checked++
		}
	}
	if checked < 15 {
		t.Fatalf("only %d error events checked", checked)
	}
}
