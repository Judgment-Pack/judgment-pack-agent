package wire

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func TestDurableModelSharedTerminalEvidence(t *testing.T) {
	raw, err := os.ReadFile("../../../contract/fixtures/storage/model-terminals.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures struct {
		Cases []struct {
			Name     string
			Family   EndpointKind
			Streamed bool
			Body     string
			Accepted bool
		}
	}
	if err = json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, row := range fixtures.Cases {
		t.Run(row.Name, func(t *testing.T) {
			if accepted := verifyDurableTerminal(context.Background(), row.Family, row.Streamed, []byte(row.Body)) == nil; accepted != row.Accepted {
				t.Fatalf("terminal admission=%v expected=%v", accepted, row.Accepted)
			}
			response := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(row.Body))}
			if row.Streamed {
				response.Header.Set("Content-Type", "text/event-stream")
			}
			err := QuarantineDurableResponse(context.Background(), row.Family, response)
			if (err == nil) != row.Accepted {
				t.Fatal("original response quarantine disagrees with terminal evidence")
			}
			if err == nil {
				replayed, _ := io.ReadAll(response.Body)
				if !bytes.Equal(replayed, []byte(row.Body)) {
					t.Fatal("response replay changed bytes")
				}
			}
		})
	}
}
func TestDurableModelQuarantineBoundsAndCancellation(t *testing.T) {
	oversized := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", DurableModelBytes+1)))}
	if QuarantineDurableResponse(context.Background(), OpenAICompatible, oversized) == nil {
		t.Fatal("oversized response accepted")
	}
	reader, writer := io.Pipe()
	defer writer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	response := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: reader}
	done := make(chan error, 1)
	go func() { done <- QuarantineDurableResponse(ctx, OpenAICompatible, response) }()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled never-ending body accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("root cancellation did not end quarantine")
	}
}
