package certification

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/Judgment-Pack/judgment-pack-agent/go/relay"
)

// RelayPrefix is the Desk's relay mount (RELAY_PREFIX in packages/harness/session.ts).
const RelayPrefix = "/api/assistant/relay/v1"

// ModelRequestHeaders are the only headers a model request may carry, the chassis' outbound
// allow-list (relay.ModelRequestHeaders). A credential is not among them.
var ModelRequestHeaders = relay.ModelRequestHeaders

// sessionBearer is the certification's stand-in for the Desk session the relay route is gated by,
// and hostBearer the host's own bearer on the in-process call (the relay handler's guard).
const (
	sessionBearer = "certification-session"
	hostBearer    = "certification-host"
)

// ModelRequest is one model request as an adapter hands it to the host: the protocol's headers and
// the body, never an address and never a credential (relay.Request).
type ModelRequest = relay.Request

// ModelCall is the host's relay capability, session.model.call in the TypeScript package: a path
// suffix after the relay's mount, and a request (relay.Call). An adapter's only reach to a model
// is this.
type ModelCall = relay.Call

// RuntimeCall sends one JSON-RPC message to the runtime and returns its reply, or nil for a
// notification. It is the runtime's transport before any gate: the runner puts it behind the gate
// (go/gate) and an adapter is handed only the gated call.
type RuntimeCall func(ctx context.Context, message json.RawMessage) (json.RawMessage, error)

// Session is one scripted endpoint and one recorded runtime on the shared server.
type Session struct {
	server   *Server
	endpoint Endpoint
	id       string
	relay    string
	mcp      string

	mu     sync.Mutex
	bodies [][]byte
	seam   []SeamRequest
	call   ModelCall
}

// Bodies are the model requests' bodies the relay capability was handed, in order, as they crossed
// the in-process seam: what the TypeScript legs' fetch stub records, for the checks that read a
// request's own text.
func (s *Session) Bodies() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]byte(nil), s.bodies...)
}

// Seam are the requests the adapter handed the relay, as they crossed the in-process seam: each
// one's header names (lower-cased) and its address, for K1 at the seam.
func (s *Session) Seam() []SeamRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]SeamRequest(nil), s.seam...)
}

// SeamRequest is one request at the in-process seam.
type SeamRequest struct {
	Path        string
	HeaderNames []string
}

// Endpoint is how this session's model answers.
func (s *Session) Endpoint() Endpoint { return s.endpoint }

// ModelCall binds the relay capability for this session's wire: the in-process relay client
// (go/relay) in front of the relay handler's path, whose upstream is this session's scripted
// endpoint on the shared server. The handler presents the session bearer as the endpoint's
// credential, on every wire, which is what the scripted endpoint records as the TypeScript legs'
// Desk session (K1: authorization once, no other credential); it takes that value back out of every
// answer's headers (WithoutReflectedCredentials) as it would a provider key.
func (s *Session) ModelCall() ModelCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.call == nil {
		handler := relay.NewHandler(relay.Upstream{
			Base:      s.server.URL + "/s/" + s.id + RelayPrefix,
			Kind:      s.endpoint.API,
			Transport: s.server.client.Transport,
			Credential: func() (string, string, string) {
				return "Authorization", "Bearer " + sessionBearer, sessionBearer
			},
		}, hostBearer)
		client := &relay.Client{Handler: handler, Family: s.endpoint.API, Bearer: hostBearer, Seen: func(request *http.Request, body []byte) {
			names := make([]string, 0, len(request.Header))
			for name := range request.Header {
				names = append(names, strings.ToLower(name))
			}
			sort.Strings(names)
			s.mu.Lock()
			defer s.mu.Unlock()
			if request.Method != http.MethodGet {
				s.bodies = append(s.bodies, append([]byte(nil), body...))
			}
			s.seam = append(s.seam, SeamRequest{Path: request.URL.RequestURI(), HeaderNames: names})
		}}
		s.call = client.Call()
	}
	return s.call
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
	URL                 string            `json:"url"`
	HeaderNames         []string          `json:"headerNames"`
	API                 string            `json:"api"`
	StreamRequested     bool              `json:"streamRequested"`
	ToolNames           []string          `json:"toolNames"`
	MessageCount        int               `json:"messageCount"`
	Step                string            `json:"step"`
	Results             int               `json:"results"`
	BodyMembers         []string          `json:"bodyMembers"`
	ThinkingRequested   bool              `json:"thinkingRequested"`
	ThinkingParam       *ThinkingParam    `json:"thinkingParam"`
	Refutation          bool              `json:"refutation"`
	SignaturesCarried   []string          `json:"signaturesCarried"`
	SignaturesExpected  []string          `json:"signaturesExpected"`
	SignaturesMissing   []string          `json:"signaturesMissing"`
	SignaturesTruncated []string          `json:"signaturesTruncated"`
	SignaturesMalformed []string          `json:"signaturesMalformed"`
	ReasoningIn         []string          `json:"reasoningIn"`
	SchemaKeywords      []string          `json:"schemaKeywords"`
	Schemas             []json.RawMessage `json:"schemas"`
	ModelParts          []json.RawMessage `json:"modelParts"`
}

// ThinkingParam is the tier parameter a request carried, with its path, verbatim.
type ThinkingParam struct {
	Path  string          `json:"path"`
	Value json.RawMessage `json:"value"`
}

// EndpointRequest is one request as a scenario's endpoint read it (EndpointRequest in
// testing/scripted.ts): its number from 1 and its body.
type EndpointRequest struct {
	Number int             `json:"number"`
	Body   json.RawMessage `json:"body"`
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
	// Endpoint is a scenario session's requests as its endpoint read them.
	Endpoint []EndpointRequest `json:"endpoint"`
}

// Advance moves a scenario session of several responses or turns to its next.
func (s *Session) Advance(ctx context.Context) error {
	return s.server.do(ctx, http.MethodPost, "/s/"+s.id+"/advance", map[string]any{}, nil)
}

// Observations reads what this session saw so far.
func (s *Session) Observations(ctx context.Context) (*Observations, error) {
	var observed Observations
	if err := s.server.do(ctx, http.MethodGet, "/s/"+s.id+"/observations", nil, &observed); err != nil {
		return nil, err
	}
	return &observed, nil
}
