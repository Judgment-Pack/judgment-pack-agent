// Package relay is the model relay in process (ADR-0004, decision 2): the call a Go adapter makes
// to a model, which runs through the Desk's relay handler's own path rather than beside it.
//
// Two halves. Client is the adapter's only way out: a path suffix and a request, held to the
// chassis' rules (the suffix, the header allow-list, the host's own bearer) and served by an
// http.Handler in process, never over a socket, with only the answer's content headers and its
// bounded diagnostics handed back. Handler is that handler's path as this repository holds it, a
// line-for-line reading of the Desk's handleModelRelay (internal/desk/modelrelay.go): the suffix
// and query refused before anything is read, the body bound refused and never truncated, four
// slots and no queue, the body read whole before it is sent, the request header allow-list with
// one credential added, the overall deadline and the idle bound (before the first byte and between
// two reads), no redirect followed, and the answer's headers filtered (withoutReflectedCredentials,
// the refusal mark, Location, Refresh, trailers) before the client sees any of it. In this
// repository it targets the certification's scripted endpoint; the Desk binds its own handler
// (handleModelRelay) in Handler's place, so the client's path is the Desk's own.
//
// What the Desk's handler does that this one does not, because it is the Desk's configuration and
// not the path: the session guard is a bearer comparison here; the key store, the key's binding to
// its destination, the AI connection and profile checks, the model allow-list, the per-connection
// capacity, the server's memory preparation, the data-work lock and the log line.
package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// The bounds of one relayed request, the Desk's (modelrelay.go).
const (
	// Prefix is the relay's mount (relayPrefix). The v1 belongs to the route, not to any endpoint.
	Prefix = "/api/assistant/relay/v1/"
	// MaxBody bounds one relayed request body (maxRelayBody): refused past it, never truncated.
	MaxBody = 8 << 20
	// MaxSuffix bounds the path asked for (maxRelaySuffix).
	MaxSuffix = 256
	// MaxInFlight is how many relayed requests are carried at once (maxRelayInFlight): a bound,
	// not a queue.
	MaxInFlight = 4
	// MaxListingBody bounds the one answer the relay reads, a model listing (maxListingBody).
	MaxListingBody = 1 << 20
	// MinFingerprintable is the key length from which a key is looked for inside a longer value
	// (minFingerprintable in assistant.go); below it, only an exact value is.
	MinFingerprintable = 12
	// FinalWrite bounds the one write allowed after the overall deadline (relayFinalWrite).
	FinalWrite = 5 * time.Second
)

// The two time bounds every relayed request is held to (relayDeadline, relayIdle). A Handler whose
// own fields are zero uses these.
var (
	Deadline = 10 * time.Minute
	Idle     = 2 * time.Minute
)

// RefusalHeader marks a refusal the relay wrote itself (RefusalHeader in files.go): an endpoint can
// never wear it, because every upstream answer has it stripped.
const RefusalHeader = "X-Jpack-Desk-Refusal"

// The codes the relay's refusals carry (files.go).
const (
	CodeRelayPath     = "assistant-relay-path"
	CodeRelayBusy     = "assistant-relay-busy"
	CodeRelayUpstream = "assistant-relay-upstream"
	CodeListing       = "assistant-listing-refused"
	CodeTooLarge      = "too-large"
	CodeBadRequest    = "bad-request"
	CodeUnauthorized  = "unauthorized"
)

// RequestHeaders are the request headers that travel to the endpoint, and nothing else does
// (relayedRequestHeaders): an allow-list, so a credential header nobody has heard of does not
// travel because it is not on it. RequestHeaderPrefixes are the families carried whole.
var (
	RequestHeaders = []string{
		"Accept", "Accept-Encoding", "Accept-Language", "Content-Type", "Content-Length",
		"User-Agent", "Anthropic-Version", "Anthropic-Beta", "OpenAI-Beta", "OpenAI-Organization",
		"OpenAI-Project",
	}
	RequestHeaderPrefixes = []string{"X-Stainless-"}
)

// RequestHeader reports whether one request header travels (relayedRequestHeader).
func RequestHeader(name string) bool {
	for _, allowed := range RequestHeaders {
		if strings.EqualFold(name, allowed) {
			return true
		}
	}
	for _, prefix := range RequestHeaderPrefixes {
		if len(name) > len(prefix) && strings.EqualFold(name[:len(prefix)], prefix) {
			return true
		}
	}
	return false
}

// ReflectedCredentialHeaders are deleted from every relayed answer (reflectedCredentialHeaders).
var ReflectedCredentialHeaders = []string{
	"Authorization", "Proxy-Authorization", "WWW-Authenticate", "Proxy-Authenticate",
	"X-Api-Key", "Api-Key", "X-Goog-Api-Key", "Set-Cookie",
}

// PathMethods are the methods a colon may introduce in the last segment (relayPathMethods).
var PathMethods = []string{"generateContent", "streamGenerateContent", "countTokens"}

// StreamPair is the one query pair a relayed request may carry, on the Gemini wire only
// (relayStreamPair).
const StreamPair = "alt=sse"

// ListingSuffix is the path each wire's model listing is at (relayListingSuffix).
var ListingSuffix = map[string]string{
	"openai-compatible": "models",
	"anthropic":         "v1/models",
	"gemini":            "v1beta/models",
}

// SuffixProblem holds a relayed path to the relay's rule (relaySuffixProblem): one or more
// segments of letters, digits, ".", "_" and "-", no empty or dot segment, and a colon only
// before one of PathMethods in a non-empty last segment.
func SuffixProblem(suffix string) string {
	if suffix == "" {
		return "a relayed request must name at least one path segment after " + strings.TrimSuffix(Prefix, "/")
	}
	if len(suffix) > MaxSuffix {
		return fmt.Sprintf("a relayed path is at most %d bytes; this one is %d", MaxSuffix, len(suffix))
	}
	segments := strings.Split(suffix, "/")
	for index, segment := range segments {
		if segment == "" {
			return "a relayed path may not contain an empty segment"
		}
		if segment == "." || segment == ".." {
			return `a relayed path may not contain a "." or ".." segment`
		}
		if name, method, found := strings.Cut(segment, ":"); found {
			if index != len(segments)-1 || name == "" || !contains(PathMethods, method) {
				return "a colon in a relayed path may only introduce one of " + strings.Join(PathMethods, ", ") +
					", after a non-empty final segment"
			}
			segment = name
		}
		for _, r := range segment {
			if !pathRune(r) {
				return "a relayed path may contain only letters, digits, \".\", \"_\" and \"-\" " +
					"in each segment; a backslash, a percent sign or a space is refused rather " +
					"than encoded"
			}
		}
	}
	return ""
}

func pathRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-'
}

func contains(list []string, item string) bool {
	for _, held := range list {
		if held == item {
			return true
		}
	}
	return false
}

// QueryProblem refuses a relayed query, admitting only StreamPair, once (relayQueryProblem).
func QueryProblem(raw string) (extra, problem string) {
	if raw == "" {
		return "", ""
	}
	if strings.ContainsRune(raw, ';') {
		return "", "a relayed query may not contain a semicolon: it is a separator to some " +
			"servers and a value to others, and this desk will not send one it cannot " +
			"read the same way twice"
	}
	for _, parameter := range strings.Split(raw, "&") {
		name, _, _ := strings.Cut(parameter, "=")
		decoded, err := url.QueryUnescape(name)
		if err == nil && decoded == "alt" && parameter == StreamPair && extra == "" {
			extra = parameter
			continue
		}
		return "", "a relayed request carries no query of the page's own: nothing of it is " +
			"forwarded, because no comparison this desk can write is the one every server " +
			"downstream makes"
	}
	return extra, ""
}

// ExtraQueryPair is the pair a wire admits (relayExtraQueryPair): StreamPair on gemini, none
// elsewhere.
func ExtraQueryPair(kind string) string {
	if kind == "gemini" {
		return StreamPair
	}
	return ""
}

// Target is the address one relayed request goes to (relayTarget): the suffix on the configured
// URL's escaped path, the configured query first and the admitted pair after it.
func Target(base, suffix, extra string) (*url.URL, error) {
	parsed, err := url.Parse(base)
	if err != nil {
		return nil, err
	}
	appendPath(parsed, "/"+suffix)
	if extra != "" {
		if parsed.RawQuery == "" {
			parsed.RawQuery = extra
		} else {
			parsed.RawQuery += "&" + extra
		}
		parsed.ForceQuery = false
	}
	return parsed, nil
}

// appendPath puts a suffix on a URL's escaped path (appendPath in assistant.go).
func appendPath(u *url.URL, suffix string) {
	escaped := strings.TrimRight(u.EscapedPath(), "/") + suffix
	decoded, err := url.PathUnescape(escaped)
	if err != nil {
		u.Path = escaped
		u.RawPath = escaped
		return
	}
	u.Path = decoded
	u.RawPath = escaped
}

// WithoutReflectedCredentials takes the key back out of an answer's headers
// (withoutReflectedCredentials): by name, and any header whose value is the key, or holds it where
// the key is at least MinFingerprintable bytes. It reads headers only: a body is not read, and a
// derived form of the key (base64, hex, a part of it) is not caught.
func WithoutReflectedCredentials(header http.Header, key string) {
	for _, name := range ReflectedCredentialHeaders {
		header.Del(name)
	}
	if key == "" {
		return
	}
	long := len(key) >= MinFingerprintable
	for name, values := range header {
		for _, value := range values {
			if value == key || (long && strings.Contains(value, key)) {
				header.Del(name)
				break
			}
		}
	}
}

// WithoutTrailers takes the trailer keys out of a header map (withoutTrailers).
func WithoutTrailers(header http.Header) {
	header.Del("Trailer")
	for name := range header {
		if strings.HasPrefix(name, http.TrailerPrefix) {
			header.Del(name)
		}
	}
}

var (
	errListingCarriesKey = errors.New("listing carries the key")
	errListingTooLarge   = errors.New("listing past the bound")
	errListingNotJSON    = errors.New("listing is not one JSON value")
)

// ListingProblem is the scan of a model listing (listingProblem): exactly one JSON value, and no
// string or number in it is the key (or holds it, for a key of MinFingerprintable bytes or more).
func ListingProblem(body []byte, key string) error {
	long := key != "" && len(key) >= MinFingerprintable
	carries := func(value string) bool {
		return key != "" && (value == key || (long && strings.Contains(value, key)))
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	depth, values := 0, 0
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return errListingNotJSON
		}
		switch typed := token.(type) {
		case json.Delim:
			if typed == '[' || typed == '{' {
				if depth == 0 {
					values++
				}
				depth++
			} else {
				depth--
			}
		case string:
			if carries(typed) {
				return errListingCarriesKey
			}
			if depth == 0 {
				values++
			}
		case json.Number:
			if carries(string(typed)) {
				return errListingCarriesKey
			}
			if depth == 0 {
				values++
			}
		default:
			if depth == 0 {
				values++
			}
		}
	}
	if values != 1 || depth != 0 {
		return errListingNotJSON
	}
	return nil
}

// Upstream is the endpoint the host configured: its base address, its wire, the transport the
// relay sends on, the credential it presents and the key it takes back out of every answer.
type Upstream struct {
	Base string
	Kind string
	// Transport sends the relayed request. It is the host's; nil is http.DefaultTransport. A
	// RoundTripper follows no redirect.
	Transport http.RoundTripper
	// Credential is the one header the relay adds (credentialHeader in modelrelay.go), and the key
	// WithoutReflectedCredentials looks for in the answer. Empty name: none is added.
	Credential func() (name, value, key string)
}

// Handler is the relay handler's path (handleModelRelay), for one configured upstream.
type Handler struct {
	Upstream Upstream
	// Session is the bearer the host's own requests carry; a request without it is refused before
	// anything is read, as the Desk's guard refuses one.
	Session string
	// Deadline and Idle are the overall and idle bounds; zero is the package's.
	Deadline, Idle time.Duration
	slots          chan struct{}
}

// NewHandler is the handler for one upstream, with its four slots.
func NewHandler(upstream Upstream, session string) *Handler {
	return &Handler{Upstream: upstream, Session: session, slots: make(chan struct{}, MaxInFlight)}
}

func (h *Handler) deadline() time.Duration {
	if h.Deadline > 0 {
		return h.Deadline
	}
	return Deadline
}

func (h *Handler) idle() time.Duration {
	if h.Idle > 0 {
		return h.Idle
	}
	return Idle
}

// writeCoded writes one refusal the relay authors (writeJSONCoded): {"error", "code"}, marked with
// RefusalHeader carrying its code.
func writeCoded(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	data, _ := json.Marshal(map[string]string{"error": message, "code": code})
	w.Header().Set(RefusalHeader, code)
	w.WriteHeader(status)
	_, _ = w.Write(append(data, '\n'))
}

// beforeTheFirstByte bounds the wait for the upstream's response headers by the idle bound
// (beforeTheFirstByte).
type beforeTheFirstByte struct {
	inner  http.RoundTripper
	cancel context.CancelFunc
	idle   time.Duration
}

func (b beforeTheFirstByte) RoundTrip(r *http.Request) (*http.Response, error) {
	timer := time.AfterFunc(b.idle, b.cancel)
	defer timer.Stop()
	inner := b.inner
	if inner == nil {
		inner = http.DefaultTransport
	}
	return inner.RoundTrip(r)
}

// idleBody cancels a relayed request that has stopped moving (idleBody).
type idleBody struct {
	inner io.ReadCloser
	timer *time.Timer
	idle  time.Duration
}

func boundedByIdle(inner io.ReadCloser, cancel context.CancelFunc, idle time.Duration) io.ReadCloser {
	return &idleBody{inner: inner, idle: idle, timer: time.AfterFunc(idle, cancel)}
}

func (b *idleBody) Read(p []byte) (int, error) {
	n, err := b.inner.Read(p)
	if err == nil {
		b.timer.Reset(b.idle)
	}
	return n, err
}

func (b *idleBody) Close() error {
	b.timer.Stop()
	return b.inner.Close()
}

// deadlineWriter bounds every write to the caller by the idle bound, never past the overall
// deadline but for one final answer (deadlineWriter). SetWriteDeadline is best effort, as the
// Desk's is: a writer that cannot hold one is written to unbounded.
type deadlineWriter struct {
	http.ResponseWriter
	controller *http.ResponseController
	until      time.Time
	finalUntil time.Time
	idle       time.Duration
}

func (d *deadlineWriter) extend() {
	now := time.Now()
	next := now.Add(d.idle)
	if next.After(d.until) {
		next = d.until
	}
	if next.After(now) {
		_ = d.controller.SetWriteDeadline(next)
		return
	}
	if d.finalUntil.IsZero() {
		d.finalUntil = now.Add(FinalWrite)
	}
	_ = d.controller.SetWriteDeadline(d.finalUntil)
}

// WriteHeader swallows an informational response: a 1xx reaches the caller past every filter.
func (d *deadlineWriter) WriteHeader(status int) {
	if status < 100 || status >= 200 {
		d.extend()
		d.ResponseWriter.WriteHeader(status)
	}
}

func (d *deadlineWriter) Write(p []byte) (int, error) {
	d.extend()
	return d.ResponseWriter.Write(p)
}

func (d *deadlineWriter) Flush() {
	d.extend()
	_ = d.controller.Flush()
}

func (d *deadlineWriter) Unwrap() http.ResponseWriter { return d.ResponseWriter }

// ServeHTTP carries one model request to the configured upstream, in the Desk's order: every
// refusal first, and in each of them nothing is sent.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.Session == "" || r.Header.Get("Authorization") != "Bearer "+h.Session {
		writeCoded(w, http.StatusUnauthorized, CodeUnauthorized, "this desk did not admit the request's session")
		return
	}
	suffix := strings.TrimPrefix(r.URL.EscapedPath(), Prefix)
	if !strings.HasPrefix(r.URL.EscapedPath(), Prefix) {
		writeCoded(w, http.StatusBadRequest, CodeRelayPath, "a relayed request is under "+strings.TrimSuffix(Prefix, "/"))
		return
	}
	if reason := SuffixProblem(suffix); reason != "" {
		writeCoded(w, http.StatusBadRequest, CodeRelayPath, reason)
		return
	}
	extra, reason := QueryProblem(r.URL.RawQuery)
	if reason != "" {
		writeCoded(w, http.StatusBadRequest, CodeRelayPath, reason)
		return
	}
	if r.ContentLength > MaxBody {
		writeCoded(w, http.StatusRequestEntityTooLarge, CodeTooLarge, fmt.Sprintf("a relayed request body is at most %d bytes; nothing was sent", MaxBody))
		return
	}
	kind := h.Upstream.Kind
	listing := r.Method == http.MethodGet && suffix == ListingSuffix[kind]
	if extra != "" && extra != ExtraQueryPair(kind) {
		writeCoded(w, http.StatusBadRequest, CodeRelayPath, fmt.Sprintf("a relayed request to a %q endpoint carries no query parameter at all; nothing was sent", kind))
		return
	}
	name, value, key := "", "", ""
	if h.Upstream.Credential != nil {
		name, value, key = h.Upstream.Credential()
	}
	target, err := Target(h.Upstream.Base, suffix, extra)
	if err != nil {
		writeCoded(w, http.StatusConflict, "assistant-unconfigured", "the configured endpoint is not an address a request can be sent to")
		return
	}
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		writeCoded(w, http.StatusServiceUnavailable, CodeRelayBusy, fmt.Sprintf("this desk is already carrying %d requests to the endpoint; nothing was sent", MaxInFlight))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.deadline())
	defer cancel()
	r = r.WithContext(ctx)
	deadline := time.Now().Add(h.deadline())
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(deadline)
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBody))
	_ = controller.SetReadDeadline(time.Time{})
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeCoded(w, http.StatusRequestEntityTooLarge, CodeTooLarge, fmt.Sprintf("a relayed request body is at most %d bytes; nothing was sent", MaxBody))
			return
		}
		writeCoded(w, http.StatusBadRequest, CodeBadRequest, "the request body could not be read, and nothing was sent")
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r.TransferEncoding = nil
	idle := h.idle()
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			out := pr.Out
			out.URL = target
			out.Host = ""
			carried := make(http.Header, len(RequestHeaders)+1)
			for header, values := range out.Header {
				if RequestHeader(header) {
					carried[header] = values
				}
			}
			if name != "" {
				carried.Set(name, value)
			}
			if listing {
				carried.Del("Accept-Encoding")
			}
			out.Header = carried
		},
		Transport:     beforeTheFirstByte{inner: h.Upstream.Transport, cancel: cancel, idle: idle},
		FlushInterval: -1,
		ModifyResponse: func(response *http.Response) error {
			WithoutReflectedCredentials(response.Header, key)
			response.Header.Del(RefusalHeader)
			response.Header.Del("Location")
			response.Header.Del("Refresh")
			response.Trailer = nil
			response.Header.Del("Trailer")
			if listing {
				bounded := boundedByIdle(response.Body, cancel, idle)
				read, err := io.ReadAll(io.LimitReader(bounded, MaxListingBody+1))
				_ = bounded.Close()
				if err != nil {
					return err
				}
				if len(read) > MaxListingBody {
					return errListingTooLarge
				}
				if problem := ListingProblem(read, key); problem != nil {
					return problem
				}
				response.Body = io.NopCloser(bytes.NewReader(read))
				response.ContentLength = int64(len(read))
				response.Header.Set("Content-Length", strconv.Itoa(len(read)))
				response.Header.Del("Content-Encoding")
				return nil
			}
			response.Body = boundedByIdle(response.Body, cancel, idle)
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			switch {
			case errors.Is(err, errListingCarriesKey):
				writeCoded(w, http.StatusBadGateway, CodeListing, "the endpoint put the credential in its model listing; this desk will not list it")
			case errors.Is(err, errListingTooLarge):
				writeCoded(w, http.StatusBadGateway, CodeListing, fmt.Sprintf("the endpoint's model listing is past the %d bytes this desk reads, so none of it was listed", MaxListingBody))
			case errors.Is(err, errListingNotJSON):
				writeCoded(w, http.StatusBadGateway, CodeListing, "the endpoint's model listing is not one JSON document, so this desk could not read it and did not list it")
			default:
				writeCoded(w, http.StatusBadGateway, CodeRelayUpstream, "the configured endpoint could not be reached: "+transportDiagnostic(err))
			}
		},
	}
	proxy.ServeHTTP(&deadlineWriter{ResponseWriter: w, controller: controller, until: deadline, idle: idle}, r)
	WithoutTrailers(w.Header())
}

// transportDiagnostic is one word for a transport failure, never anything the endpoint wrote
// (transportDiagnostic in assistant.go: its timeout, DNS, refused and unexpected-status words; the
// TLS word is not reached by the transports this repository uses).
func transportDiagnostic(err error) string {
	var opError *net.OpError
	var dns *net.DNSError
	switch {
	case errors.Is(err, context.DeadlineExceeded) || os.IsTimeout(err) || errors.As(err, &opError) && opError.Timeout():
		return "timeout"
	case errors.As(err, &dns):
		return "dns"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "refused"
	}
	return "unexpected-status"
}
