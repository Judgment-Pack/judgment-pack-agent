package wire

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
)

// The one automatic retry this adapter makes of a model request: a refusal
// (engines/vercel/retry.ts).
//
// One rule: only a refusal the failure catalogue classes `refused` — a 4xx other than 408, which
// did nothing and charged nothing — and only one named in the catalogue (the 429 rate-limited
// class, and a 4xx carrying the overload code). Never an unknown outcome: a 5xx, 408, 504 or a
// transport failure may have run. Never after any response byte: the retry is decided on a
// response that was not ok. At most twice, waiting the longer of the backoff and the provider's
// Retry-After, and not at all where Retry-After asks for longer than one turn should wait. Each
// attempt is a model request, so it is counted and checkpointed like any other, and each refused
// one is handed to OnRefused for the completion record.

// ModelRetries is MODEL_RETRIES: retries after the first attempt, three attempts in all.
const ModelRetries = 2

// RetryBackoff is RETRY_BACKOFF_MS: the wait before retry one and retry two, where the provider
// asks for less.
var RetryBackoff = []time.Duration{time.Second, 2 * time.Second}

// RetryWaitLimit is RETRY_WAIT_LIMIT_MS: the longest Retry-After this adapter waits out inside a
// turn.
const RetryWaitLimit = 30 * time.Second

// RefusedAttempt is one refused attempt, as the completion record keeps it (RefusedAttempt).
type RefusedAttempt struct {
	// Attempt is which attempt at this request was refused, from 1.
	Attempt int
	Failure contract.Failure
	// Wait is the wait before the next attempt; zero where none follows.
	Wait time.Duration
}

// WaitFor is waitFor: a wait the run's context ends, holding no timer once it has.
func WaitFor(ctx context.Context, wait time.Duration) error {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return failure.Cancelled
	case <-timer.C:
		return nil
	}
}

// RetryOptions are retryingCall's hooks. The TypeScript's own signal is the call's context here.
type RetryOptions struct {
	// OnRefused is told each refused attempt, for the completion record.
	OnRefused func(refused RefusedAttempt) error
	// OnAttempt is said before every attempt, from 1, so a record is never left holding an earlier
	// call's number.
	OnAttempt func(attempt int)
	// MayRetry is whether a retry may take another request now; a refusal is reported where it may
	// not. nil is yes.
	MayRetry func() bool
	// Wait and Now stand in for the clock in a test; nil is WaitFor and time.Now.
	Wait func(ctx context.Context, wait time.Duration) error
	Now  func() time.Time
}

// delayFor is the wait before retry `attempt`, or false where the provider asks for too long.
func delayFor(refused contract.Failure, attempt int, now time.Time) (time.Duration, bool) {
	after := time.Duration(0)
	if refused.RetryAt != nil {
		if at, err := time.Parse(time.RFC3339Nano, *refused.RetryAt); err == nil {
			after = at.Sub(now)
		}
	}
	if after > RetryWaitLimit {
		return 0, false
	}
	backoff := RetryBackoff[len(RetryBackoff)-1]
	if attempt-1 < len(RetryBackoff) {
		backoff = RetryBackoff[attempt-1]
	}
	if after > backoff {
		return after, true
	}
	return backoff, true
}

// RetryingCall is retryingCall: the model capability wrapped in the one retry a refusal earns.
//
// The TypeScript reads the refusal's body from a clone of the response, so the SDK still reads the
// original where it is handed on; Go has no clone, so a refused body is read whole and put back on
// the response that is returned. Only a response that was not ok is read this way, so no streamed
// answer is buffered.
func RetryingCall(call ModelCall, options RetryOptions) ModelCall {
	wait := options.Wait
	if wait == nil {
		wait = WaitFor
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return func(ctx context.Context, suffix string, request ModelRequest) (*http.Response, error) {
		for attempt := 1; ; attempt++ {
			if options.OnAttempt != nil {
				options.OnAttempt(attempt)
			}
			answered, err := call(ctx, suffix, request)
			if err != nil {
				return nil, err
			}
			if answered.StatusCode >= 200 && answered.StatusCode <= 299 {
				return answered, nil
			}
			// The refusal's own envelope, read and put back, so the caller still reads it where the
			// answer is handed on. Classified by status, headers and the codes the catalogue
			// aliases; its prose is never kept.
			body := ""
			if answered.Body != nil {
				read, readErr := io.ReadAll(answered.Body)
				answered.Body.Close()
				if readErr != nil && ctx.Err() != nil {
					return nil, failure.Cancelled
				}
				body = string(read)
				answered.Body = io.NopCloser(bytes.NewReader(read))
			}
			refused := failure.Normalize(&RelayRefusal{Status: answered.StatusCode, Body: body, Headers: answered.Header}, "model", now())
			if !failure.CanRetryRefusal(refused, failure.RefusalCode(body)) {
				return answered, nil
			}
			held := RefusedAttempt{Attempt: attempt, Failure: refused}
			mayRetry := options.MayRetry == nil || options.MayRetry()
			delay, within := time.Duration(0), false
			if attempt <= ModelRetries && mayRetry {
				delay, within = delayFor(refused, attempt, now())
				if within {
					held.Wait = delay
				}
			}
			if options.OnRefused != nil {
				if err := options.OnRefused(held); err != nil {
					return nil, err
				}
			}
			if !within {
				return answered, nil
			}
			if answered.Body != nil {
				answered.Body.Close()
			}
			if err := wait(ctx, delay); err != nil {
				return nil, err
			}
		}
	}
}
