package runcontext

import (
	"bytes"
	"context"
	"net/http"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
)

// modelCompletion is live, one-use authority for one acknowledged model claim.
// Its pointer identity is registered by the writer; serialized fields cannot recreate it.
type modelCompletion struct {
	writer  *RunWriter
	binding RunBinding
	id      string
	request []byte
}

func beginDurableModel(ctx context.Context, binding *RunBinding, source, tool string, request []byte) (*modelCompletion, error) {
	return InvokeDurable(ctx, binding, "model", source, tool, request, true, func(raw []byte) (*modelCompletion, error) { return &modelCompletion{request: bytes.Clone(raw)}, nil }, nil, nil)
}

func finishDurableModel(ctx context.Context, binding *RunBinding, handle *modelCompletion, outcome error, verifiedRefusal bool) error {
	w := binding.Writer
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.fatal != nil {
		return w.fatal
	}
	if handle == nil || handle.writer != w || handle.binding != *binding || !w.models[handle] {
		w.fatal = &ContinuationIdentityChanged{}
		return w.fatal
	}
	if verifiedRefusal {
		f := failure.Normalize(outcome, "model", time.Now())
		if outcome == nil || f.Source != "model" || f.Outcome != "refused" || f.Status == nil || *f.Status < 400 || *f.Status >= 500 || *f.Status == 408 {
			w.fatal = &ContinuationIdentityChanged{}
			return w.fatal
		}
	}
	// Verification alone is not acknowledgement. Cancellation before the result
	// commit must not promote even a verified response to a durable result.
	if ctx.Err() != nil {
		verifiedRefusal = false
		if outcome == nil {
			outcome = ctx.Err()
		}
	}
	delete(w.models, handle)
	if w.stopped {
		return nil
	}
	if w.dispatchClosed.Load() && outcome == nil {
		outcome = failure.Cancelled
	}
	found := false
	for i, row := range w.book.Dispatches {
		if row.ID == handle.id && row.Kind == "model" && row.Scope == binding.Scope && row.Read && row.State == "claimed" {
			if outcome == nil || verifiedRefusal {
				w.book.Dispatches[i].State = "result"
			} else {
				w.book.Dispatches[i].State = "unknown"
				if w.budget != nil {
					w.budget.unknown(handle.id)
				}
			}
			if outcome != nil {
				f := failure.Normalize(outcome, "model", time.Now())
				w.book.Dispatches[i].Failure = &f
			}
			found = true
			break
		}
	}
	if !found {
		w.fatal = &ContinuationIdentityChanged{}
		return w.fatal
	}
	// A canceled dispatch context cannot acknowledge storage. Keep its already admitted
	// claim unknown in this writer; the host's bounded FinalizeStop persists it.
	if ctx.Err() != nil {
		w.dispatchClosed.Store(true)
		return nil
	}
	if _, err := w.saveLocked(ctx, *binding, nil, nil, nil, nil); err != nil {
		w.fatal = &StorageOutcomeUnknown{cause: err}
		return w.fatal
	}
	if outcome == nil || verifiedRefusal {
		w.ackCaptureLocked(handle.id)
		for _, row := range w.book.Dispatches {
			if row.Kind == "model" && row.Read && row.Scope == binding.Scope && row.State == "unknown" {
				delete(w.recovered, row.ID)
			}
		}
	} else {
		w.dispatchClosed.Store(true)
	}
	return nil
}

// InvokeDurableModel quarantines a durable response below executable SDK/tool boundaries.
// verify must consume the bounded original body and positively validate its terminal protocol;
// a nil return is never inferred from HTTP headers, EOF, a local close or engine advancement.
// Non-durable calls retain their existing streaming behavior.
func InvokeDurableModel(ctx context.Context, binding *RunBinding, source, tool string, request []byte, execute func([]byte) (*http.Response, error), verify func(*http.Response) error) (*http.Response, error) {
	if binding == nil {
		return execute(request)
	}
	handle, err := beginDurableModel(ctx, binding, source, tool, request)
	if err != nil {
		return nil, err
	}
	var response *http.Response
	verifiedRefusal := false
	var captured *modelCaptureBody
	if err = ctx.Err(); err == nil {
		if binding.Writer.Failed() {
			return nil, &ContinuationNotRecoverable{}
		}
		response, err = execute(handle.request)
		if err == nil {
			if binding.Writer.capture != nil && response != nil && response.Body != nil {
				captured = &modelCaptureBody{ReadCloser: response.Body}
				response.Body = captured
			}
			err = verify(response)
			if binding.Writer.capture != nil {
				binding.Writer.observeCaptureModel(handle, response, captured)
			}
			verifiedRefusal = err != nil && response != nil && response.StatusCode >= 400 && response.StatusCode < 500 && response.StatusCode != 408 && failure.Normalize(err, "model", time.Now()).Outcome == "refused"
		}
	}
	if saved := finishDurableModel(ctx, binding, handle, err, verifiedRefusal); saved != nil {
		if response != nil && response.Body != nil {
			response.Body.Close()
		}
		return nil, saved
	}
	if err != nil && !verifiedRefusal {
		if response != nil && response.Body != nil {
			response.Body.Close()
		}
		return nil, err
	}
	if err = finishUsageResponse(ctx, binding, handle, response); err != nil {
		if response != nil && response.Body != nil {
			response.Body.Close()
		}
		return nil, err
	}
	if ctx.Err() != nil || binding.Writer.Failed() {
		if response != nil && response.Body != nil {
			response.Body.Close()
		}
		return nil, failure.Cancelled
	}
	return response, nil
}
