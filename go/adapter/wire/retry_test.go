package wire

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
)

// The one retry a refusal earns (the rows of engines/vercel/engine.test.ts that pin it, over
// engines/vercel/retry.ts): only a refusal the catalogue classes refused and names retryable, at
// most twice, never where the caller cannot afford another request, and never where the provider
// asks for longer than one turn should wait.

func refusal(status int, body string, headers map[string]string) *http.Response {
	held := http.Header{"Content-Type": []string{"application/json"}}
	for name, value := range headers {
		held.Set(name, value)
	}
	return &http.Response{StatusCode: status, Header: held, Body: io.NopCloser(strings.NewReader(body))}
}

const rateLimited = `{"error":{"type":"rate_limit_error","message":"slow down"}}`

// retried runs one wrapped call over the answers given, with no real waiting.
func retried(t *testing.T, options RetryOptions, answers ...*http.Response) (*http.Response, []time.Duration, []int, []RefusedAttempt) {
	t.Helper()
	var waits []time.Duration
	var attempts []int
	var refused []RefusedAttempt
	requests := 0
	call := func(ctx context.Context, suffix string, request ModelRequest) (*http.Response, error) {
		held := answers[min(requests, len(answers)-1)]
		requests++
		return held, nil
	}
	options.Wait = func(ctx context.Context, wait time.Duration) error {
		waits = append(waits, wait)
		return nil
	}
	onAttempt := options.OnAttempt
	options.OnAttempt = func(attempt int) {
		attempts = append(attempts, attempt)
		if onAttempt != nil {
			onAttempt(attempt)
		}
	}
	onRefused := options.OnRefused
	options.OnRefused = func(held RefusedAttempt) error {
		refused = append(refused, held)
		if onRefused != nil {
			return onRefused(held)
		}
		return nil
	}
	if options.Now == nil {
		options.Now = func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
	}
	answer, err := RetryingCall(call, options)(context.Background(), "chat/completions", ModelRequest{Body: []byte("{}")})
	if err != nil {
		t.Fatalf("the wrapped call: %v", err)
	}
	return answer, waits, attempts, refused
}

// TestARefusalIsRetriedOnce is "four requests remaining and one 429 retried: the run still ends
// with its proposal" — the refusal is retried, and each refused attempt reaches the record.
func TestARefusalIsRetriedOnce(t *testing.T) {
	answered := []*http.Response{refusal(429, rateLimited, nil), {StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("ok"))}}
	answer, waits, attempts, refused := retried(t, RetryOptions{}, answered...)
	if answer.StatusCode != 200 {
		t.Fatalf("the answer is %d", answer.StatusCode)
	}
	if len(attempts) != 2 || attempts[0] != 1 || attempts[1] != 2 {
		t.Errorf("the attempts said are %v", attempts)
	}
	if len(refused) != 1 || refused[0].Attempt != 1 || refused[0].Failure.Code != "rate-limited" || refused[0].Failure.Outcome != "refused" {
		t.Fatalf("the refused attempts are %+v", refused)
	}
	if refused[0].Wait != RetryBackoff[0] {
		t.Errorf("the wait before the retry is %v", refused[0].Wait)
	}
	if len(waits) != 1 || waits[0] != RetryBackoff[0] {
		t.Errorf("the waits are %v", waits)
	}
	// A successful answer is handed on as it came.
	body, _ := io.ReadAll(answer.Body)
	equalText(t, "the answer's body", string(body), "ok")
}

// TestAtMostTwoRetries is retry.ts's "at most twice": three attempts in all, and the backoff of
// each.
func TestAtMostTwoRetries(t *testing.T) {
	answer, waits, attempts, refused := retried(t, RetryOptions{}, refusal(429, rateLimited, nil))
	if answer.StatusCode != 429 {
		t.Fatalf("the answer is %d", answer.StatusCode)
	}
	if len(attempts) != 3 {
		t.Errorf("the attempts said are %v", attempts)
	}
	if len(waits) != 2 || waits[0] != RetryBackoff[0] || waits[1] != RetryBackoff[1] {
		t.Errorf("the waits are %v", waits)
	}
	if len(refused) != 3 || refused[2].Wait != 0 {
		t.Errorf("the refused attempts are %+v", refused)
	}
	// The refusal is handed back with its body still readable.
	body, _ := io.ReadAll(answer.Body)
	equalText(t, "the refusal's body", string(body), rateLimited)
}

// TestNoRetryTheCallerCannotAfford is "one request remaining and a 429: no retry takes it, and the
// refusal is reported".
func TestNoRetryTheCallerCannotAfford(t *testing.T) {
	answer, waits, attempts, refused := retried(t, RetryOptions{MayRetry: func() bool { return false }}, refusal(429, rateLimited, nil))
	if answer.StatusCode != 429 || len(attempts) != 1 || len(waits) != 0 {
		t.Errorf("the refusal was retried: %d, %v, %v", answer.StatusCode, attempts, waits)
	}
	if len(refused) != 1 || refused[0].Wait != 0 {
		t.Errorf("the refused attempt is %+v", refused)
	}
}

// TestNoRetryBeyondTheWaitLimit is "not at all where Retry-After asks for longer than one turn
// should wait — that refusal is reported with its retryAt".
func TestNoRetryBeyondTheWaitLimit(t *testing.T) {
	answer, waits, attempts, refused := retried(t, RetryOptions{}, refusal(429, rateLimited, map[string]string{"retry-after": "120"}))
	if answer.StatusCode != 429 || len(attempts) != 1 || len(waits) != 0 {
		t.Errorf("a long Retry-After was waited out: %d, %v, %v", answer.StatusCode, attempts, waits)
	}
	if len(refused) != 1 || refused[0].Failure.RetryAt == nil {
		t.Fatalf("the refused attempt is %+v", refused)
	}
	if refused[0].Wait != 0 {
		t.Errorf("a wait was recorded for a refusal nothing waits for: %v", refused[0].Wait)
	}
}

// TestTheLongerOfTheBackoffAndTheProvidersWait is "waiting the longer of the backoff and the
// provider's Retry-After".
func TestTheLongerOfTheBackoffAndTheProvidersWait(t *testing.T) {
	_, waits, _, _ := retried(t, RetryOptions{}, refusal(429, rateLimited, map[string]string{"retry-after": "5"}))
	if len(waits) != 2 || waits[0] < 4*time.Second || waits[0] > 5*time.Second {
		t.Errorf("the waits are %v", waits)
	}
}

// TestOnlyARetryableRefusal is retry.ts's first rule: never an unknown outcome, and only a refusal
// the catalogue names.
func TestOnlyARetryableRefusal(t *testing.T) {
	for _, held := range []struct {
		what   string
		status int
		body   string
	}{
		{"a refusal a person has to go and fix", 409, `{"error":"no key stored","code":"assistant-no-key"}`},
		{"a refusal that is not retryable", 400, `{"error":{"message":"Unsupported parameter: reasoning_effort"}}`},
		{"a 5xx, which may have run", 500, `{"error":{"message":"upstream"}}`},
		{"a 408, which may have run", 408, `{"error":{"message":"timeout"}}`},
	} {
		answer, waits, attempts, refused := retried(t, RetryOptions{}, refusal(held.status, held.body, nil))
		if answer.StatusCode != held.status || len(attempts) != 1 || len(waits) != 0 || len(refused) != 0 {
			t.Errorf("%s was retried: %v, %v, %v", held.what, attempts, waits, refused)
		}
	}
	// The overload code a 4xx carries is retryable, as the catalogue names it.
	_, _, attempts, _ := retried(t, RetryOptions{}, refusal(429, `{"type":"overloaded_error","message":"overloaded"}`, nil))
	if len(attempts) != 3 {
		t.Errorf("an overloaded endpoint was asked %v times", attempts)
	}
}

// TestEachCallStartsAtAttemptOne is "starts each call at attempt 1, so a later row never carries
// an earlier call's attempt".
func TestEachCallStartsAtAttemptOne(t *testing.T) {
	var attempts []int
	requests := 0
	call := func(ctx context.Context, suffix string, request ModelRequest) (*http.Response, error) {
		requests++
		if requests == 1 {
			return refusal(429, rateLimited, nil), nil
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("ok"))}, nil
	}
	wrapped := RetryingCall(call, RetryOptions{
		OnAttempt: func(attempt int) { attempts = append(attempts, attempt) },
		Wait:      func(context.Context, time.Duration) error { return nil },
	})
	ctx := context.Background()
	if _, err := wrapped(ctx, "chat/completions", ModelRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := wrapped(ctx, "chat/completions", ModelRequest{}); err != nil {
		t.Fatal(err)
	}
	// 1 and 2 for the first call, then 1 again: the second call is a new request's first attempt.
	if len(attempts) != 3 || attempts[0] != 1 || attempts[1] != 2 || attempts[2] != 1 {
		t.Errorf("the attempts said are %v", attempts)
	}
}

func TestUnavailableSwitchesOnceAndResetsTheRetryCount(t *testing.T) {
	var switched []string
	answer, _, attempts, _ := retried(t, RetryOptions{
		Fallback: func(reason string) (string, string, bool) {
			if reason != "unavailable" {
				t.Errorf("reason is %s", reason)
			}
			return "primary", "secondary", true
		},
		OnSwitch: func(from, to, reason string) error { switched = append(switched, from+">"+to+":"+reason); return nil },
	}, refusal(503, `{}`, nil), &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("ok"))})
	if answer.StatusCode != 200 || len(switched) != 1 || switched[0] != "primary>secondary:unavailable" {
		t.Fatalf("answer %d, switches %v", answer.StatusCode, switched)
	}
	if len(attempts) != 2 || attempts[0] != 1 || attempts[1] != 1 {
		t.Errorf("attempts are %v", attempts)
	}
}

func TestRefusalNeverSwitches(t *testing.T) {
	called := false
	answer, _, _, _ := retried(t, RetryOptions{Fallback: func(string) (string, string, bool) { called = true; return "primary", "secondary", true }}, refusal(403, `{}`, nil))
	if answer.StatusCode != 403 || called {
		t.Errorf("refusal answer %d, fallback called %v", answer.StatusCode, called)
	}
}

// TestAWaitTheRunEnds is waitFor: a wait the run's context ends, reported as the cancellation it
// is.
func TestAWaitTheRunEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := WaitFor(ctx, time.Hour); !failure.IsCancelled(err) {
		t.Errorf("a cancelled wait answered %v", err)
	}
}
