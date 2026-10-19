package relay

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testKey = "sk-a-key-of-more-than-twelve-bytes"

func upstreamClient(t *testing.T, upstream http.HandlerFunc, kind string) (*Client, *Handler, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(upstream)
	t.Cleanup(server.Close)
	handler := NewHandler(Upstream{
		Base:      server.URL + "/v1",
		Kind:      kind,
		Transport: server.Client().Transport,
		Credential: func() (string, string, string) {
			return "x-api-key", testKey, testKey
		},
	}, "host-session")
	return &Client{Handler: handler, Family: kind, Bearer: "host-session"}, handler, server
}

func call(t *testing.T, client *Client, suffix string, headers map[string]string) (*http.Response, string) {
	t.Helper()
	response, err := client.Call()(context.Background(), suffix, Request{Headers: headers, Body: []byte(`{"model":"m"}`)})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	return response, string(body)
}

// The credential the relay adds reaches the endpoint once, and nothing the adapter named that is
// off the allow-list does; the answer reaches the adapter through the facade, with a key the
// endpoint reflects in any header taken out (the ADR's test that a reflected credential does not
// reach the adapter).
func TestAReflectedCredentialDoesNotReachTheAdapter(t *testing.T) {
	var seen http.Header
	client, _, _ := upstreamClient(t, func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		w.Header().Set("Authorization", "Bearer "+testKey)
		w.Header().Set("X-Api-Key", testKey)
		w.Header().Set("X-Echo", "you sent "+testKey)
		w.Header().Set("Set-Cookie", "a=b")
		w.Header().Set(RefusalHeader, "unauthorized")
		w.Header().Set("Location", "/api/session")
		w.Header().Set("Trailer", "X-Late")
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-Id", "req_1")
		_, _ = io.WriteString(w, `{"ok":true}`)
		w.Header().Set("X-Late", testKey)
	}, "anthropic")
	response, body := call(t, client, "v1/messages", map[string]string{
		"content-type": "application/json", "x-api-key": "the adapter's own", "cookie": "c", "x-auth-token": "t",
	})
	if body != `{"ok":true}` || response.StatusCode != 200 {
		t.Fatalf("the answer was %d %q", response.StatusCode, body)
	}
	if got := seen.Values("X-Api-Key"); len(got) != 1 || got[0] != testKey {
		t.Fatalf("the endpoint was sent x-api-key %v", got)
	}
	for _, name := range []string{"Cookie", "X-Auth-Token", "Authorization"} {
		if seen.Get(name) != "" {
			t.Errorf("the endpoint was sent %s", name)
		}
	}
	for name, values := range response.Header {
		for _, value := range values {
			if strings.Contains(value, testKey) {
				t.Errorf("the adapter was handed the key in %s", name)
			}
		}
	}
	for _, name := range []string{"Set-Cookie", RefusalHeader, "Location", "X-Echo", "Trailer"} {
		if response.Header.Get(name) != "" {
			t.Errorf("the adapter was handed %s", name)
		}
	}
	if response.Header.Get("X-Request-Id") != "req_1" || response.Header.Get("Content-Type") != "application/json" {
		t.Errorf("the facade dropped the content type or the request id: %v", response.Header)
	}
	// And the handler's own filter, read before the facade: the echo is gone by value there too.
	h := http.Header{"X-Echo": {"Bearer " + testKey}, "X-Short": {"abc"}}
	WithoutReflectedCredentials(h, testKey)
	if h.Get("X-Echo") != "" || h.Get("X-Short") != "abc" {
		t.Errorf("WithoutReflectedCredentials left %v", h)
	}
}

// No redirect is followed: the 3xx comes back as the answer, without its Location, and the address
// it named is never reached.
func TestNoRedirectIsFollowed(t *testing.T) {
	var reached atomic.Int32
	client, _, _ := upstreamClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/elsewhere" {
			reached.Add(1)
			return
		}
		w.Header().Set("Location", "/elsewhere")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}, "openai-compatible")
	response, _ := call(t, client, "chat/completions", nil)
	if response.StatusCode != http.StatusTemporaryRedirect || response.Header.Get("Location") != "" || reached.Load() != 0 {
		t.Fatalf("status %d, location %q, reached %d", response.StatusCode, response.Header.Get("Location"), reached.Load())
	}
}

// Four slots and no queue: a fifth request while four are in flight is refused with nothing sent.
func TestTheFourSlotsAreABoundNotAQueue(t *testing.T) {
	release := make(chan struct{})
	var arrived atomic.Int32
	client, _, _ := upstreamClient(t, func(w http.ResponseWriter, r *http.Request) {
		arrived.Add(1)
		<-release
	}, "openai-compatible")
	var wg sync.WaitGroup
	for range MaxInFlight {
		wg.Add(1)
		go func() {
			defer wg.Done()
			response, err := client.Call()(context.Background(), "chat/completions", Request{Body: []byte("{}")})
			if err == nil {
				_, _ = io.ReadAll(response.Body)
				response.Body.Close()
			}
		}()
	}
	deadline := time.Now().Add(5 * time.Second)
	for arrived.Load() < MaxInFlight && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	response, body := call(t, client, "chat/completions", nil)
	close(release)
	wg.Wait()
	if response.StatusCode != http.StatusServiceUnavailable || !strings.Contains(body, CodeRelayBusy) || arrived.Load() != MaxInFlight {
		t.Fatalf("the fifth was answered %d %s with %d arrived", response.StatusCode, body, arrived.Load())
	}
}

// A body past the bound is refused, never truncated, and nothing is sent.
func TestABodyPastTheBoundIsRefused(t *testing.T) {
	var arrived atomic.Int32
	client, _, _ := upstreamClient(t, func(http.ResponseWriter, *http.Request) { arrived.Add(1) }, "openai-compatible")
	response, err := client.Call()(context.Background(), "chat/completions", Request{Body: make([]byte, MaxBody+1)})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusRequestEntityTooLarge || !strings.Contains(string(body), CodeTooLarge) || arrived.Load() != 0 {
		t.Fatalf("%d %s, %d arrived", response.StatusCode, body, arrived.Load())
	}
}

// The idle bound holds before the first byte: an endpoint that answers nothing is cut at the idle
// bound, not the overall deadline.
func TestTheIdleBoundHoldsBeforeTheFirstByte(t *testing.T) {
	stop := make(chan struct{})
	defer close(stop)
	client, handler, _ := upstreamClient(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-stop:
		case <-r.Context().Done():
		}
	}, "openai-compatible")
	handler.Idle = 200 * time.Millisecond
	started := time.Now()
	response, body := call(t, client, "chat/completions", nil)
	// The Desk's word for a request its own idle bound cancelled is unexpected-status: the
	// cancellation is the handler's, not a transport timeout.
	if response.StatusCode != http.StatusBadGateway || !strings.Contains(body, CodeRelayUpstream) || time.Since(started) > 5*time.Second {
		t.Fatalf("%d %s after %s", response.StatusCode, body, time.Since(started))
	}
}

// The capability's own rules refuse before anything crosses: a dot segment, a query off the
// family's pair, a colon method off the list; and a handler that does not admit the session
// refuses as the Desk's guard does, which the client reports as authentication, not started.
func TestTheSuffixAndTheSessionAreHeld(t *testing.T) {
	var arrived atomic.Int32
	client, _, _ := upstreamClient(t, func(http.ResponseWriter, *http.Request) { arrived.Add(1) }, "openai-compatible")
	for _, suffix := range []string{"../chat", "chat/completions?key=x", "models/m:deleteEverything", "a//b", "a b"} {
		if _, err := client.Call()(context.Background(), suffix, Request{}); err == nil {
			t.Errorf("%q reached the handler", suffix)
		}
	}
	if CallSuffixProblem("v1beta/models/m:streamGenerateContent?alt=sse", "gemini") != "" {
		t.Error("the gemini stream pair was refused")
	}
	if CallSuffixProblem("v1beta/models/m:streamGenerateContent?alt=sse", "anthropic") == "" {
		t.Error("the stream pair was admitted off gemini")
	}
	stranger := &Client{Handler: client.Handler, Family: "openai-compatible", Bearer: "another-session"}
	if _, err := stranger.Call()(context.Background(), "chat/completions", Request{}); err == nil || !strings.Contains(err.Error(), "authentication") {
		t.Errorf("a stranger's session was %v", err)
	}
	if arrived.Load() != 0 {
		t.Errorf("%d requests reached the endpoint", arrived.Load())
	}
}

// An answer streams: the adapter reads the first event before the endpoint has written the last.
func TestAnAnswerStreams(t *testing.T) {
	second := make(chan struct{})
	client, _, _ := upstreamClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: one\n\n")
		w.(http.Flusher).Flush()
		<-second
		_, _ = io.WriteString(w, "data: two\n\n")
	}, "openai-compatible")
	response, err := client.Call()(context.Background(), "chat/completions", Request{Body: []byte("{}")})
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	first := make([]byte, len("data: one\n\n"))
	if _, err := io.ReadFull(response.Body, first); err != nil || string(first) != "data: one\n\n" {
		t.Fatalf("read %q, %v", first, err)
	}
	close(second)
	rest, _ := io.ReadAll(response.Body)
	if string(rest) != "data: two\n\n" {
		t.Fatalf("read %q", rest)
	}
}

// Four streamed answers stopped mid-stream, their bodies never closed, give their slots back when
// their requests' contexts end: the fifth request is answered 200, not busy.
func TestAStoppedStreamGivesBackItsSlot(t *testing.T) {
	var arrived atomic.Int32
	client, handler, _ := upstreamClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: one\n\n")
		w.(http.Flusher).Flush()
		if arrived.Add(1) > MaxInFlight {
			return
		}
		chunk := strings.Repeat("data: more\n\n", 64)
		for r.Context().Err() == nil {
			if _, err := io.WriteString(w, chunk); err != nil {
				return
			}
			w.(http.Flusher).Flush()
		}
	}, "openai-compatible")
	for range MaxInFlight {
		ctx, cancel := context.WithCancel(context.Background())
		response, err := client.Call()(ctx, "chat/completions", Request{Body: []byte("{}")})
		if err != nil {
			t.Fatal(err)
		}
		first := make([]byte, len("data: one\n\n"))
		if _, err := io.ReadFull(response.Body, first); err != nil {
			t.Fatal(err)
		}
		cancel()
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(handler.slots) > 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	response, body := call(t, client, "chat/completions", nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("the fifth was answered %d %s with %d slots held", response.StatusCode, body, len(handler.slots))
	}
}
