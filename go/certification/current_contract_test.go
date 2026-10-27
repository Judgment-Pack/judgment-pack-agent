package certification_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/certification"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
)

// This is the actual shared HTTP replay, with the explicit synthetic declarations.
// The historical recording is read as evidence and never rewritten to add metadata.
func TestCurrentReplayDeclarationsPreserveHistoricalRecording(t *testing.T) {
	root, err := certification.RepositoryRoot()
	if err != nil {
		t.Fatal(err)
	}
	read := func(name string, value any) []byte {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(root, "packages/harness/certification", name))
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(raw, value); err != nil {
			t.Fatal(err)
		}
		return raw
	}
	var historical struct {
		Tools []map[string]any `json:"tools"`
	}
	original := read("runtime.json", &historical)
	var current struct {
		Kind                      string
		HistoricalRecordingSHA256 string
		Tools                     map[string]any
	}
	read("runtime-current-contract.json", &current)
	digest := sha256.Sum256(original)
	if current.Kind != "handwritten-current-contract-test-declarations" || hex.EncodeToString(digest[:]) != current.HistoricalRecordingSHA256 {
		t.Fatal("synthetic declarations lost their historical byte pin")
	}
	for _, tool := range historical.Tools {
		if _, present := tool["_meta"]; present {
			t.Fatal("historical tool acquired synthetic metadata")
		}
	}
	ctx := context30s(t)
	session, err := server.NewSession(ctx, certification.Endpoint{API: "openai-compatible", AnswerAs: "whole"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := session.RuntimeCall()(ctx, json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Result struct{ Tools []map[string]any }
	}
	if err = json.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Result.Tools) != len(historical.Tools) {
		t.Fatal("synthetic replay changed historical tool count")
	}
	for at, tool := range response.Result.Tools {
		if !reflect.DeepEqual(tool["_meta"], current.Tools[tool["name"].(string)]) {
			t.Fatal("served metadata differs from explicit synthetic declaration")
		}
		delete(tool, "_meta")
		if !reflect.DeepEqual(tool, historical.Tools[at]) {
			t.Fatal("synthetic replay changed a historical descriptor")
		}
	}
	connection := gate.New(gate.Options{Runtime: gate.Call(session.RuntimeCall()), Granted: gate.Ceiling})
	offer, err := connection.Open(ctx, nil)
	if err != nil || len(offer.Tools) != len(historical.Tools) {
		t.Fatal("actual connection refused current declarations", err)
	}
	for at, tool := range offer.Tools {
		if tool.Name != historical.Tools[at]["name"] {
			t.Fatal("connection changed declaration order")
		}
	}
}

func TestCurrentReplayRefusesNonRehearsalEvaluation(t *testing.T) {
	ctx := context30s(t)
	session, err := server.NewSession(ctx, certification.Endpoint{API: "openai-compatible", AnswerAs: "whole"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := session.RuntimeCall()(ctx, json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"experimental_evaluate","arguments":{"pack":"{}","rehearsal":false}}}`))
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Error *struct {
			Code    int
			Message string
		}
	}
	if err = json.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	if response.Error == nil || response.Error.Code != -32602 || response.Error.Message != "the evaluation would have been recorded" {
		t.Fatalf("non-rehearsal evaluation was not refused: %s", raw)
	}
	observed, err := session.Observations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []certification.ServerObservation{{Name: "experimental_evaluate", Args: map[string]any{"pack": "{}", "rehearsal": false}, Refusal: "experimental_evaluate reached the runtime without rehearsal: true"}}
	if !reflect.DeepEqual(observed.Seen, want) || len(observed.Requests) != 0 {
		t.Fatalf("wrong replay refusal observations: %+v", observed)
	}
}

func TestCurrentReplayMetadataRefusedThroughConnection(t *testing.T) {
	for _, kind := range []string{"missing", "malformed"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context30s(t)
			session, err := server.NewSession(ctx, certification.Endpoint{API: "openai-compatible", AnswerAs: "whole"})
			if err != nil {
				t.Fatal(err)
			}
			underlying := session.RuntimeCall()
			changed := false
			// Only corrupt the declared test metadata after the real replay answers
			// tools/list. All handshake and dispatch remain on the actual connection.
			transport := func(ctx context.Context, frame json.RawMessage) (json.RawMessage, error) {
				raw, err := underlying(ctx, frame)
				if err != nil {
					return nil, err
				}
				var request struct{ Method string }
				if err = json.Unmarshal(frame, &request); err != nil {
					return nil, err
				}
				if request.Method != "tools/list" {
					return raw, nil
				}
				var answer map[string]json.RawMessage
				if err = json.Unmarshal(raw, &answer); err != nil {
					return nil, err
				}
				var result struct {
					Tools []map[string]json.RawMessage `json:"tools"`
				}
				if err = json.Unmarshal(answer["result"], &result); err != nil {
					return nil, err
				}
				if len(result.Tools) == 0 {
					t.Fatal("real replay served no metadata fixture")
				}
				if kind == "missing" {
					delete(result.Tools[0], "_meta")
				} else {
					result.Tools[0]["_meta"] = json.RawMessage(`{"operation":"read","effect":"read","undeclared":true}`)
				}
				changed = true
				answer["result"], err = json.Marshal(result)
				if err != nil {
					return nil, err
				}
				return json.Marshal(answer)
			}
			connection := gate.New(gate.Options{Runtime: transport, Granted: gate.Ceiling})
			offer, err := connection.Open(ctx, nil)
			if !changed || err == nil || len(offer.Tools) != 0 {
				t.Fatal("invalid metadata entered the actual connection offer", err)
			}
			if kind == "missing" && !strings.Contains(err.Error(), "taxonomy refused") {
				t.Fatal("missing metadata failed outside taxonomy admission", err)
			}
			if kind == "malformed" && !strings.Contains(err.Error(), "undeclared") {
				t.Fatal("malformed metadata failed outside the closed declaration reader", err)
			}
			observed, err := session.Observations(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(observed.Seen) != 0 || len(observed.Requests) != 0 {
				t.Fatal("invalid metadata reached dispatch", observed)
			}
		})
	}
}
