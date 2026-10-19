// Package certification is the Go runner of the certification suite. It drives a Go adapter
// against the same scripted model and recorded runtime the TypeScript certification uses
// (packages/harness/certification), served over HTTP on the loopback interface by
// packages/harness/scripts/scripted-server.mjs, so one suite certifies every adapter.
//
// Keyless and offline: the server is the repository's own Node script, started by this package and
// ended by its process id; nothing else is listened on or called.
package certification

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ServerScript is the shared server, from the repository root.
const ServerScript = "packages/harness/scripts/scripted-server.mjs"

// Server is one running scripted server.
type Server struct {
	URL    string
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stderr *lockedBuffer
	client *http.Client
	once   sync.Once
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// RepositoryRoot finds the repository root from the working directory upward: the directory that
// holds the shared server.
func RepositoryRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ServerScript)); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no %s above the working directory", ServerScript)
		}
		dir = parent
	}
}

// StartServer starts the shared server with the node on PATH (or $NODE) and waits for its address.
// The repository's dependencies must be installed (npm ci at the root).
func StartServer(ctx context.Context) (*Server, error) {
	root, err := RepositoryRoot()
	if err != nil {
		return nil, err
	}
	node := os.Getenv("NODE")
	if node == "" {
		node = "node"
	}
	// Without the trap handler V8 reserves no guard region for WebAssembly memory, so the server runs
	// under a cap on virtual memory (ulimit -v) as the suites here are run.
	cmd := exec.Command(node, "--disable-wasm-trap-handler", ServerScript)
	cmd.Dir = root
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr := &lockedBuffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s %s: %w", node, ServerScript, err)
	}
	server := &Server{cmd: cmd, stdin: stdin, stderr: stderr, client: &http.Client{
		// The host's own transport, taken before a leg seals http.DefaultTransport: the relay and the
		// runtime are the host's to reach, and nothing else is.
		Transport: http.DefaultTransport.(*http.Transport).Clone(),
		// A model API never redirects the client relaying to it; neither does this server.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
	type ready struct {
		url string
		err error
	}
	answer := make(chan ready, 1)
	go func() {
		line, err := bufio.NewReader(stdout).ReadString('\n')
		if err != nil {
			answer <- ready{err: fmt.Errorf("the scripted server said nothing: %w", err)}
			return
		}
		var said struct {
			Listening string `json:"listening"`
		}
		if err := json.Unmarshal([]byte(line), &said); err != nil || said.Listening == "" {
			answer <- ready{err: fmt.Errorf("the scripted server's first line is not its address: %q", line)}
			return
		}
		answer <- ready{url: said.Listening}
		_, _ = io.Copy(io.Discard, stdout)
	}()
	select {
	case got := <-answer:
		if got.err != nil {
			server.Close()
			return nil, fmt.Errorf("%w (node %s; run npm ci at the repository root): %s", got.err, node, stderr.String())
		}
		server.URL = got.url
		return server, nil
	case <-time.After(60 * time.Second):
		server.Close()
		return nil, fmt.Errorf("the scripted server did not start within 60s: %s", stderr.String())
	case <-ctx.Done():
		server.Close()
		return nil, ctx.Err()
	}
}

// Close ends the server: its stdin is closed, which ends it, and the process this package started
// is killed by its id if it has not ended within five seconds.
func (s *Server) Close() {
	s.once.Do(func() {
		_ = s.stdin.Close()
		done := make(chan struct{})
		go func() {
			_ = s.cmd.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = s.cmd.Process.Kill()
			<-done
		}
	})
}

// Stderr is what the server wrote to its standard error, for a failure's report.
func (s *Server) Stderr() string { return s.stderr.String() }

func (s *Server) do(ctx context.Context, method, path string, body any, into any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, s.URL+path, reader)
	if err != nil {
		return err
	}
	response, err := s.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	if response.StatusCode/100 != 2 {
		return fmt.Errorf("%s %s: %d %s", method, path, response.StatusCode, strings.TrimSpace(string(raw)))
	}
	if into == nil {
		return nil
	}
	return json.Unmarshal(raw, into)
}

// Endpoint is how a session's scripted model answers: scriptedModel's options.
type Endpoint struct {
	API        string `json:"api"`      // openai-compatible, anthropic or gemini
	AnswerAs   string `json:"answerAs"` // stream or whole
	Thinking   string `json:"thinking,omitempty"`
	Refuted    bool   `json:"refuted,omitempty"`
	Signatures string `json:"signatures,omitempty"`
	ToolCalls  string `json:"toolCalls,omitempty"`
}

// Scenario is certification/scenario.json, as far as the runner reads it.
type Scenario struct {
	Policy        string                     `json:"policy"`
	ScenarioTools []string                   `json:"scenarioTools"`
	Unknowns      []string                   `json:"unknowns"`
	Documents     map[string]json.RawMessage `json:"documents"`
}

// Scenario reads the scenario the server's scripted model plays.
func (s *Server) Scenario(ctx context.Context) (*Scenario, error) {
	var scenario Scenario
	if err := s.do(ctx, http.MethodGet, "/scenario", nil, &scenario); err != nil {
		return nil, err
	}
	return &scenario, nil
}

// NewSession stands up one scripted endpoint and one recorded runtime.
func (s *Server) NewSession(ctx context.Context, endpoint Endpoint) (*Session, error) {
	var created struct {
		ID    string `json:"id"`
		Relay string `json:"relay"`
		MCP   string `json:"mcp"`
	}
	if err := s.do(ctx, http.MethodPost, "/sessions", endpoint, &created); err != nil {
		return nil, err
	}
	if created.ID == "" || created.Relay == "" || created.MCP == "" {
		return nil, errors.New("the scripted server created a session without its paths")
	}
	return &Session{server: s, endpoint: endpoint, id: created.ID, relay: created.Relay, mcp: created.MCP}, nil
}
