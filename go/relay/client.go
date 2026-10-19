package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"

	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
)

// Request is one model request as an adapter hands it to the host (ModelRequest in engine.ts): the
// protocol's headers and the body, never an address and never a credential. Method is POST unless
// GET.
type Request struct {
	Method  string
	Headers map[string]string
	Body    []byte
}

// Call is the relay capability (ModelCall in engine.ts): a path suffix after the relay's mount, and
// a request. An adapter's only reach to a model is this.
type Call func(ctx context.Context, suffix string, request Request) (*http.Response, error)

// ModelRequestHeaders are the only headers a model request may carry (MODEL_REQUEST_HEADERS in
// session.ts, the chassis' outbound allow-list): a credential is not among them.
var ModelRequestHeaders = []string{
	"accept", "accept-language", "content-type", "anthropic-version", "anthropic-beta",
	"openai-beta", "openai-organization", "openai-project",
}

// AnswerHeaders are the answer's content headers that reach the adapter, and DiagnosticHeaders the
// bounded diagnostics beside them (MODEL_ANSWER_HEADERS and MODEL_DIAGNOSTIC_HEADERS in session.ts).
var (
	AnswerHeaders     = []string{"content-type", "content-length"}
	DiagnosticHeaders = []string{"request-id", "x-request-id", "retry-after"}
)

var (
	segmentRune = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	diagnostic  = regexp.MustCompile(`^[-a-zA-Z0-9_]{1,128}$`)
	retryAfter  = regexp.MustCompile(`^\d+(?:\.\d+)?$`)
)

// CallSuffixProblem holds a suffix to the capability's rule before anything is sent (suffixProblem
// in session.ts): one or more segments of letters, digits, ".", "_" and "-"; a colon only before
// one of PathMethods in a non-empty last segment; at most MaxSuffix bytes; and no query but
// StreamPair, once, on a gemini endpoint. The family is the host's, never the adapter's.
func CallSuffixProblem(suffix, family string) string {
	if suffix == "" {
		return "a model call must name at least one path segment after " + strings.TrimSuffix(Prefix, "/")
	}
	if utf16Len(suffix) > MaxSuffix {
		return fmt.Sprintf("a model call's path is at most %d bytes; this one is %d", MaxSuffix, utf16Len(suffix))
	}
	parts := strings.Split(suffix, "?")
	path, rest := parts[0], parts[1:]
	if len(rest) > 0 {
		admitted := ExtraQueryPair(family)
		if len(rest) > 1 || rest[0] != admitted || admitted == "" {
			shown := admitted
			if shown == "" {
				shown = "none"
			}
			return "a model call may carry no query of its own; the only pair a " + family + " endpoint admits is " + shown + ", once"
		}
	}
	if path == "" {
		return "a model call must name at least one path segment after " + strings.TrimSuffix(Prefix, "/")
	}
	segments := strings.Split(path, "/")
	for index, segment := range segments {
		if segment == "" {
			return "a model call's path may not carry an empty segment"
		}
		if segment == "." || segment == ".." {
			return "a model call's path may not carry a dot segment"
		}
		if colon := strings.Index(segment, ":"); colon != -1 {
			name, method := segment[:colon], segment[colon+1:]
			if index != len(segments)-1 || name == "" || !contains(PathMethods, method) {
				return "a colon in a model call's path may only introduce one of " + strings.Join(PathMethods, ", ") +
					", after a non-empty final segment"
			}
			segment = name
		}
		if !segmentRune.MatchString(segment) {
			quoted, _ := json.Marshal(segment)
			return `a model call's path segments are letters, digits, ".", "_" and "-"; ` + string(quoted) + " is not one"
		}
	}
	return ""
}

func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// Client is the in-process relay client. Handler is the relay handler's path: the Desk binds its
// own handleModelRelay; this repository binds Handler. Family is the configured endpoint's wire,
// which decides the one query pair a call may carry. Bearer is the host's own session, written on
// every call; the adapter never names it.
type Client struct {
	Handler http.Handler
	Family  string
	Bearer  string
	// Seen, where set, is told each request as the handler is handed it (the certification reads
	// it: what crossed the seam).
	Seen func(*http.Request, []byte)
}

// Call is the capability bound to this client.
func (c *Client) Call() Call { return c.call }

func (c *Client) call(ctx context.Context, suffix string, request Request) (*http.Response, error) {
	if problem := CallSuffixProblem(suffix, c.Family); problem != "" {
		return nil, fmt.Errorf("%s", problem)
	}
	if err := ctx.Err(); err != nil {
		return nil, failure.Cancelled
	}
	method := request.Method
	if method == "" {
		method = http.MethodPost
	}
	if method != http.MethodPost && method != http.MethodGet {
		return nil, fmt.Errorf("a model call is a POST or a GET, not %s", method)
	}
	path, pair, _ := strings.Cut(suffix, "?")
	target := "http://desk.invalid" + Prefix + path
	if pair != "" {
		target += "?" + pair
	}
	var body io.Reader = http.NoBody
	var sent []byte
	if method != http.MethodGet {
		sent = append([]byte(nil), request.Body...)
		body = bytes.NewReader(sent)
	}
	inbound, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, err
	}
	inbound.ContentLength = int64(len(sent))
	inbound.RemoteAddr = "127.0.0.1:0"
	for name, value := range request.Headers {
		if contains(ModelRequestHeaders, strings.ToLower(name)) {
			inbound.Header.Set(name, value)
		}
	}
	inbound.Header.Set("Authorization", "Bearer "+c.Bearer)
	if c.Seen != nil {
		c.Seen(inbound, sent)
	}
	answered, err := serve(ctx, c.Handler, inbound)
	if err != nil {
		if ctx.Err() != nil {
			return nil, failure.Cancelled
		}
		return nil, failure.Err("connection-lost", "model", "unknown")
	}
	if answered.StatusCode == http.StatusUnauthorized && answered.Header.Get(RefusalHeader) == CodeUnauthorized {
		_ = answered.Body.Close()
		return nil, failure.Err("authentication", "desk", "not-started")
	}
	return facade(answered), nil
}

// facade is the answer the adapter gets (facade in session.ts): the status, the content headers and
// the bounded diagnostics, and the body's bytes; no address, no redirect, no other header.
func facade(answered *http.Response) *http.Response {
	carried := http.Header{}
	for name, values := range answered.Header {
		lower := strings.ToLower(name)
		for _, value := range values {
			switch {
			case contains(AnswerHeaders, lower):
				carried.Add(name, value)
			case contains(DiagnosticHeaders, lower) && len(value) <= 128:
				valid := diagnostic.MatchString(value)
				if lower == "retry-after" {
					_, err := http.ParseTime(value)
					valid = retryAfter.MatchString(value) || err == nil
				}
				if valid {
					carried.Add(name, value)
				}
			}
		}
	}
	return &http.Response{
		Status:        answered.Status,
		StatusCode:    answered.StatusCode,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        carried,
		Body:          answered.Body,
		ContentLength: answered.ContentLength,
	}
}

// serve runs one request through a handler in process and hands back its answer as soon as the
// handler writes its status, the body streaming from what the handler writes after.
func serve(ctx context.Context, handler http.Handler, request *http.Request) (*http.Response, error) {
	reader, writer := io.Pipe()
	w := &pipeWriter{header: http.Header{}, pipe: writer, ready: make(chan *http.Response, 1), body: reader}
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				w.WriteHeader(http.StatusBadGateway)
				_ = writer.CloseWithError(fmt.Errorf("the relay handler failed"))
				return
			}
			w.WriteHeader(http.StatusOK)
			_ = writer.Close()
		}()
		handler.ServeHTTP(w, request)
	}()
	select {
	case response := <-w.ready:
		return response, nil
	case <-ctx.Done():
		_ = reader.CloseWithError(ctx.Err())
		return nil, ctx.Err()
	}
}

// pipeWriter is the handler's ResponseWriter in process: its status and headers become the answer
// the client returns, and what it writes is the answer's body, read as it is written.
type pipeWriter struct {
	mu     sync.Mutex
	header http.Header
	pipe   *io.PipeWriter
	body   *io.PipeReader
	ready  chan *http.Response
	sent   bool
}

func (w *pipeWriter) Header() http.Header { return w.header }

func (w *pipeWriter) WriteHeader(status int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.sent || status < 200 {
		return
	}
	w.sent = true
	length := int64(-1)
	w.ready <- &http.Response{
		Status:        fmt.Sprintf("%d %s", status, http.StatusText(status)),
		StatusCode:    status,
		Header:        w.header.Clone(),
		Body:          w.body,
		ContentLength: length,
	}
}

func (w *pipeWriter) Write(p []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	return w.pipe.Write(p)
}

// Flush is offered so the reverse proxy streams; every write already reaches the reader.
func (w *pipeWriter) Flush() {}
