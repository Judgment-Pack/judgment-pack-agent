package certification

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// RelayPrefix is the Desk's relay mount (RELAY_PREFIX in packages/harness/session.ts).
const RelayPrefix = "/api/assistant/relay/v1"

// ModelRequestHeaders are the only headers a model request may carry, the chassis' outbound
// allow-list (MODEL_REQUEST_HEADERS in packages/harness/session.ts). A credential is not among them.
var ModelRequestHeaders = []string{
	"accept", "accept-language", "content-type", "anthropic-version", "anthropic-beta",
	"openai-beta", "openai-organization", "openai-project",
}

// sessionBearer is the certification's stand-in for the Desk session the relay route is gated by.
const sessionBearer = "certification-session"

// ModelRequest is one model request as an adapter hands it to the host: the protocol's headers and
// the body, never an address and never a credential.
type ModelRequest struct {
	Method  string // POST unless GET
	Headers map[string]string
	Body    []byte
}

// ModelCall is the host's relay capability, session.model.call in the TypeScript package: a path
// suffix after the relay's mount, and a request. The host builds the address and writes its own
// session as the bearer; the relay attaches the provider's key on the far side. An adapter's only
// reach to a model is this.
type ModelCall func(ctx context.Context, suffix string, request ModelRequest) (*http.Response, error)

// RuntimeCall sends one JSON-RPC message to the runtime and returns its reply, or nil for a
// notification. It is the runtime's transport, as the TypeScript session's transport is, before
// any gate.
type RuntimeCall func(ctx context.Context, message json.RawMessage) (json.RawMessage, error)

// Session is one scripted endpoint and one recorded runtime on the shared server.
type Session struct {
	server   *Server
	endpoint Endpoint
	id       string
	relay    string
	mcp      string
}

// Endpoint is how this session's model answers.
func (s *Session) Endpoint() Endpoint { return s.endpoint }

// suffixProblem holds a suffix to the chassis' rule (suffixProblem in session.ts), in short: one
// or more non-empty, non-dot segments, at most 256 bytes, and no query but alt=sse on Gemini.
func suffixProblem(suffix, api string) string {
	if suffix == "" || len(suffix) > 256 {
		return "a model call's path is one to 256 bytes"
	}
	path, query, hasQuery := strings.Cut(strings.TrimPrefix(suffix, "/"), "?")
	if hasQuery && (api != "gemini" || query != "alt=sse") {
		return "a model call's query may only be alt=sse, on the Gemini wire"
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "a model call's path may not carry an empty or dot segment"
		}
	}
	return ""
}

// ModelCall binds the relay capability for this session's wire.
func (s *Session) ModelCall() ModelCall {
	return func(ctx context.Context, suffix string, request ModelRequest) (*http.Response, error) {
		if problem := suffixProblem(suffix, s.endpoint.API); problem != "" {
			return nil, fmt.Errorf("%s", problem)
		}
		method := request.Method
		if method == "" {
			method = http.MethodPost
		}
		var body io.Reader
		if method != http.MethodGet {
			body = bytes.NewReader(request.Body)
		}
		address := s.server.URL + "/s/" + s.id + RelayPrefix + "/" + strings.TrimPrefix(suffix, "/")
		outbound, err := http.NewRequestWithContext(ctx, method, address, body)
		if err != nil {
			return nil, err
		}
		for name, value := range request.Headers {
			for _, allowed := range ModelRequestHeaders {
				if strings.EqualFold(name, allowed) {
					outbound.Header.Set(allowed, value)
				}
			}
		}
		outbound.Header.Set("Authorization", "Bearer "+sessionBearer)
		return s.server.client.Do(outbound)
	}
}

// RuntimeCall binds the recorded runtime's transport.
func (s *Session) RuntimeCall() RuntimeCall {
	return func(ctx context.Context, message json.RawMessage) (json.RawMessage, error) {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.server.URL+s.mcp, bytes.NewReader(message))
		if err != nil {
			return nil, err
		}
		request.Header.Set("Content-Type", "application/json")
		response, err := s.server.client.Do(request)
		if err != nil {
			return nil, err
		}
		defer response.Body.Close()
		raw, err := io.ReadAll(response.Body)
		if err != nil {
			return nil, err
		}
		switch response.StatusCode {
		case http.StatusAccepted:
			return nil, nil
		case http.StatusOK:
			return raw, nil
		}
		return nil, fmt.Errorf("the recorded runtime answered %d: %s", response.StatusCode, raw)
	}
}

// RecordedRequest is one model request as the scripted model recorded it (RecordedRequest in
// certification/scriptedModel.ts), as far as the Go legs read it.
type RecordedRequest struct {
	URL               string   `json:"url"`
	HeaderNames       []string `json:"headerNames"`
	API               string   `json:"api"`
	StreamRequested   bool     `json:"streamRequested"`
	ToolNames         []string `json:"toolNames"`
	MessageCount      int      `json:"messageCount"`
	Step              string   `json:"step"`
	Results           int      `json:"results"`
	BodyMembers       []string `json:"bodyMembers"`
	ThinkingRequested bool     `json:"thinkingRequested"`
	Refutation        bool     `json:"refutation"`
}

// ServerObservation is one runtime call as the recorded runtime saw it.
type ServerObservation struct {
	Name    string         `json:"name"`
	Args    map[string]any `json:"args"`
	Refusal string         `json:"refusal"`
}

// Observations is what the session's endpoint and runtime saw, in arrival order.
type Observations struct {
	Requests []RecordedRequest   `json:"requests"`
	Seen     []ServerObservation `json:"seen"`
}

// Observations reads what this session saw so far.
func (s *Session) Observations(ctx context.Context) (*Observations, error) {
	var observed Observations
	if err := s.server.do(ctx, http.MethodGet, "/s/"+s.id+"/observations", nil, &observed); err != nil {
		return nil, err
	}
	return &observed, nil
}
