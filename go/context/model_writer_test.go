package runcontext

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/wire"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestDurableModelHandleIsClaimBoundAndOneUse(t *testing.T) {
	for _, mode := range []string{"wrong-writer", "wrong-scope", "forged-copy", "duplicate", "stale-resume"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			store := NewMemoryRunStore(MemoryRunStoreOptions{})
			writer, binding := writerFixture(t, store, "model-handle", false)
			handle, err := beginDurableModel(ctx, binding, "source", "fixture", []byte(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			if writer.Checkpoint().Durable.Dispatches[0].State != "claimed" {
				t.Fatal("begin promoted claim")
			}
			target := binding
			given := handle
			switch mode {
			case "wrong-writer":
				_, target = writerFixture(t, NewMemoryRunStore(MemoryRunStoreOptions{}), "other-writer", false)
			case "wrong-scope":
				copy := *binding
				copy.Scope.Role = "critic"
				target = &copy
			case "forged-copy":
				copy := *handle
				given = &copy
			case "duplicate":
				if err = finishDurableModel(ctx, binding, handle, nil, false); err != nil {
					t.Fatal(err)
				}
			case "stale-resume":
				_, target = writerFixture(t, store, "model-handle", true)
			}
			if err = finishDurableModel(ctx, target, given, nil, false); err == nil {
				t.Fatal("invalid completion authority accepted")
			}
			if !target.Writer.Failed() {
				t.Fatal("invalid completion did not stop writer")
			}
		})
	}
}
func TestDurableModelFinishAcknowledgementKeepsUnknownHostBarrier(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	writer, binding := writerFixture(t, store, "pending-model-host", false)
	calls := 0
	_, err := InvokeDurable(ctx, binding, "host", "source", "write", []byte(`{}`), false, func([]byte) (ToolResult, error) { calls++; return ToolResult{}, errors.New("unknown host outcome") }, nil, nil)
	if err == nil {
		t.Fatal("host fixture did not fail")
	}
	handle, err := beginDurableModel(ctx, binding, "source", "fixture", []byte(`{"body":"exact"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err = finishDurableModel(ctx, binding, handle, errors.New("incomplete model"), false); err != nil {
		t.Fatal(err)
	}
	_, resumed := writerFixture(t, store, writer.ID, true)
	_, err = InvokeDurable(ctx, resumed, "host", "source", "write", []byte(`{}`), false, func([]byte) (ToolResult, error) { calls++; return ToolResult{}, nil }, nil, nil)
	var unknown *HostOutcomeUnknown
	if !errors.As(err, &unknown) || calls != 1 {
		t.Fatal("model recovery cleared unresolved non-read host barrier")
	}
}

func TestDurableModelRestoreDistinguishesVerifiedRefusalFromUnknown(t *testing.T) {
	for _, mode := range []string{"verified-refusal", "pending", "unverified-refusal", "malformed-refusal"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			store := NewMemoryRunStore(MemoryRunStoreOptions{})
			writer, binding := writerFixture(t, store, "model-restore", false)
			original := []byte(`{"request":"old"}`)
			later := []byte(`{"request":"later"}`)
			if mode == "verified-refusal" || mode == "malformed-refusal" {
				body := `{"error":{"code":"rate_limit_exceeded"}}`
				if mode == "malformed-refusal" {
					body = `{"error":`
				}
				response, err := InvokeDurableModel(ctx, binding, "source", "fixture", original, func([]byte) (*http.Response, error) {
					return &http.Response{StatusCode: 429, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
				}, func(response *http.Response) error {
					return wire.QuarantineDurableResponse(ctx, wire.OpenAICompatible, response)
				})
				if mode == "verified-refusal" {
					if err != nil || response == nil {
						t.Fatal("verified refusal not delivered after acknowledgement")
					}
				} else if err == nil {
					t.Fatal("malformed refusal accepted")
				}
			} else {
				handle, err := beginDurableModel(ctx, binding, "source", "fixture", original)
				if err != nil {
					t.Fatal(err)
				}
				if mode == "unverified-refusal" {
					f := failure.New("rate-limited", "model", "refused")
					status := int64(429)
					f.Status = &status
					if err = finishDurableModel(ctx, binding, handle, failure.NewError(f), false); err != nil {
						t.Fatal(err)
					}
				}
			}
			_, resumed := writerFixture(t, store, writer.ID, true)
			prepared, err := PrepareDurableRequest(resumed, "model", "source", "fixture", later)
			if err != nil {
				t.Fatal(err)
			}
			cp := resumed.Writer.Checkpoint()
			row := cp.Durable.Dispatches[0]
			if mode == "verified-refusal" {
				if string(prepared) != string(later) || RecoveringDurableRequest(resumed, "model") || row.State != "result" || row.Failure == nil || row.Failure.Outcome != "refused" {
					t.Fatal("known refusal substituted old request or lost failure evidence")
				}
			} else if string(prepared) != string(original) || !RecoveringDurableRequest(resumed, "model") {
				t.Fatal("unknown model stopped retaining original identity")
			}
		})
	}
}

func TestDurableModelUnresolvedNonReadModelNeverReplays(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	writer, binding := writerFixture(t, store, "nonread-model", false)
	_, err := InvokeDurable(ctx, binding, "model", "source", "fixture", []byte(`{}`), false, func([]byte) (int, error) { return 0, errors.New("unresolved non-read model") }, nil, nil)
	if err == nil {
		t.Fatal("fixture did not fail")
	}
	_, next := writerFixture(t, store, writer.ID, true)
	calls := 0
	_, err = InvokeDurableModel(ctx, next, "source", "fixture", []byte(`{}`), func([]byte) (*http.Response, error) { calls++; return nil, nil }, func(*http.Response) error { return nil })
	var unknown *HostOutcomeUnknown
	if !errors.As(err, &unknown) || calls != 0 {
		t.Fatal("unresolved non-read model dispatched again")
	}
}

func TestDurableModelRefusalRequiresExactOutcomeAcknowledgement(t *testing.T) {
	for _, mode := range []string{"missing", "stale", "wrong", "lost"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			memory := NewMemoryRunStore(MemoryRunStoreOptions{})
			armed := true
			store := writerReceiptStore{RunStore: memory, write: func(ctx context.Context, in RunCommit) (RunReceipt, error) {
				receipt, err := memory.Commit(ctx, in)
				if err != nil {
					return receipt, err
				}
				var cp struct{ Durable DurableBook }
				if err = json.Unmarshal(in.Checkpoint, &cp); err != nil {
					return receipt, err
				}
				if armed && len(cp.Durable.Dispatches) > 0 && cp.Durable.Dispatches[0].State == "result" {
					armed = false
					switch mode {
					case "missing":
						return RunReceipt{}, nil
					case "stale":
						receipt.Revision = in.ExpectedRevision
					case "wrong":
						receipt.CommitID = "other-commit"
					case "lost":
						return RunReceipt{}, errors.New("private acknowledgement detail")
					}
				}
				return receipt, nil
			}}
			writer, binding := writerFixture(t, store, "refusal-ack", false)
			calls := 0
			execute := func([]byte) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: 429, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"rate_limit_exceeded"}}`))}, nil
			}
			verify := func(response *http.Response) error {
				return wire.QuarantineDurableResponse(ctx, wire.OpenAICompatible, response)
			}
			response, err := InvokeDurableModel(ctx, binding, "source", "fixture", []byte(`{}`), execute, verify)
			var unknown *StorageOutcomeUnknown
			if response != nil || !errors.As(err, &unknown) || !writer.Failed() {
				t.Fatal("unacknowledged refusal escaped quarantine")
			}
			if writer.Checkpoint().Durable.Dispatches[0].State != "claimed" {
				t.Fatal("unacknowledged result became local completion authority")
			}
			_, again := InvokeDurableModel(ctx, binding, "source", "fixture", []byte(`{}`), execute, verify)
			if !errors.As(again, &unknown) || calls != 1 {
				t.Fatal("unacknowledged refusal permitted retry")
			}
		})
	}
}

func TestDurableModelGenericReturnIsNotTerminalEvidence(t *testing.T) {
	for _, mode := range []string{"headers", "iterator-return"} {
		t.Run(mode, func(t *testing.T) {
			writer, binding := writerFixture(t, NewMemoryRunStore(MemoryRunStoreOptions{}), "generic-return", false)
			_, err := InvokeDurable(context.Background(), binding, "model", "source", "fixture", []byte(`{}`), true, func([]byte) (any, error) {
				if mode == "headers" {
					return &http.Response{StatusCode: 200}, nil
				}
				return struct{ Done bool }{true}, nil
			}, nil, nil)
			if err == nil || writer.Checkpoint().Durable.Dispatches[0].State != "unknown" {
				t.Fatal("ordinary host/iterator return promoted a model claim")
			}
		})
	}
}
