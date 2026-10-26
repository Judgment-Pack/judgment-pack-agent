package adapter_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	native "github.com/Judgment-Pack/judgment-pack-agent/go/adapter/agent"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/eino"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
	"github.com/Judgment-Pack/judgment-pack-agent/go/relay"
)

// Opt-in Linux producer integration. Expectations are handwritten from the normative
// intake pack: incomplete intake selects clarify-return. No successful MCP reply is mocked.
const publishedPackSHA = "5bdf53e5c283b5a7c7a04373cfa66a488b021c1e098254424c0de7a963ea8aca"
const publishedFacts = `{"request":{"type":"one-time-extract","completeness":"incomplete","appropriateness":"pass","embargoedInformationToUnauthorizedRecipients":false}}`
const publishedEvidence = `{"intake-form":"present","sponsor-endorsement":"present"}`
const publishedConfig = "{\"configVersion\":\"3\",\"audit\":{\"dir\":\"audit\"},\"packs\":{}}\n"

func publishedHash(raw []byte) string           { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func publishedRead(path string) ([]byte, error) { return os.ReadFile(path) }
func publishedAbsolute(t *testing.T, key string) string {
	t.Helper()
	value := os.Getenv(key)
	if !filepath.IsAbs(value) {
		t.Fatalf("absolute %s is required", key)
	}
	return filepath.Clean(value)
}
func publishedSave(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}

// The test transport permits one outstanding request, at most 256 frames and 16 MiB
// total output, with a 4 MiB frame bound. The gate supplies actual public MCP frames.
type publishedRPC struct {
	cmd                                           *exec.Cmd
	stdin                                         io.WriteCloser
	stdout                                        io.ReadCloser
	lines                                         chan json.RawMessage
	readDone                                      chan struct{}
	waitDone                                      chan struct{}
	stop                                          chan struct{}
	readErr                                       error
	waitErr                                       error
	stderr                                        publishedStderr
	mu                                            sync.Mutex
	violations                                    []string
	frames                                        []json.RawMessage
	replies                                       []json.RawMessage
	bytes                                         int
	pid                                           int
	start                                         string
	binary, binarySHA, project, config, temporary string
	order                                         *[]string
	exact                                         json.RawMessage
}
type publishedStderr struct {
	mu       sync.Mutex
	data     []byte
	overflow bool
}

func (s *publishedStderr) Write(raw []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.data)+len(raw) > 65536 {
		s.overflow = true
		return 0, errors.New("Runtime stderr exceeds evidence bound")
	}
	s.data = append(s.data, raw...)
	return len(raw), nil
}
func publishedStartTime(pid int) (string, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", err
	}
	// comm may contain spaces and parentheses; the final ')' precedes stat field 3.
	end := strings.LastIndex(string(raw), ") ")
	if end < 0 {
		return "", errors.New("malformed process stat")
	}
	fields := strings.Fields(string(raw)[end+2:])
	if len(fields) < 20 {
		return "", errors.New("short process stat")
	}
	if _, err = strconv.ParseUint(fields[19], 10, 64); err != nil {
		return "", err
	}
	return fields[19], nil
}
func startPublishedRPC(t *testing.T, project, binary, binarySHA, temporary string, order *[]string, exact json.RawMessage) *publishedRPC {
	t.Helper()
	p := &publishedRPC{binary: binary, binarySHA: binarySHA, project: project, config: filepath.Join(project, "jpack.json"), temporary: temporary, order: order, exact: exact, lines: make(chan json.RawMessage, 1), readDone: make(chan struct{}), waitDone: make(chan struct{}), stop: make(chan struct{})}
	p.cmd = exec.Command(binary, "mcp")
	p.cmd.Dir = project
	p.cmd.Env = []string{"JPACK_CONFIG=" + p.config, "TMPDIR=" + temporary}
	p.cmd.Stderr = &p.stderr
	var err error
	p.stdin, err = p.cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	// Own both ends explicitly: Cmd.Wait must not close a StdoutPipe before
	// the bounded reader has consumed its final bytes.
	stdout, output, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	p.stdout = stdout
	p.cmd.Stdout = output
	if err = p.cmd.Start(); err != nil {
		output.Close()
		stdout.Close()
		t.Fatal(err)
	}
	if err = output.Close(); err != nil {
		t.Error(err)
	}
	p.pid = p.cmd.Process.Pid
	// Register teardown immediately, including failed startup identity checks.
	t.Cleanup(func() { p.close(t) })
	go func() { p.waitErr = p.cmd.Wait(); close(p.waitDone) }()
	p.start, err = publishedStartTime(p.pid)
	go func() {
		defer close(p.readDone)
		defer close(p.lines)
		scan := bufio.NewScanner(p.stdout)
		scan.Buffer(make([]byte, 4096), 4*1024*1024)
		count, total := 0, 0
		for scan.Scan() {
			raw := append(json.RawMessage(nil), scan.Bytes()...)
			count++
			total += len(raw)
			if count > 256 || total > 16*1024*1024 {
				p.readErr = errors.New("Runtime output exceeds evidence bound")
				return
			}
			var envelope map[string]json.RawMessage
			if json.Unmarshal(raw, &envelope) != nil || string(envelope["jsonrpc"]) != `"2.0"` {
				p.readErr = errors.New("malformed Runtime JSON-RPC output")
				return
			}
			select {
			case p.lines <- raw:
			case <-p.stop:
				return
			}
		}
		p.readErr = scan.Err()
	}()
	if err != nil {
		t.Fatal(err)
	}
	if err = p.observe(); err != nil {
		t.Fatal(err)
	}
	return p
}
func (p *publishedRPC) observe() error {
	select {
	case <-p.waitDone:
		return errors.New("owned Runtime exited")
	default:
	}
	start, err := publishedStartTime(p.pid)
	if err != nil || start != p.start {
		return fmt.Errorf("Runtime PID/start changed: %v", err)
	}
	proc := fmt.Sprintf("/proc/%d", p.pid)
	for _, pair := range [][2]string{{"exe", p.binary}, {"cwd", p.project}} {
		got, err := os.Readlink(filepath.Join(proc, pair[0]))
		if err != nil || got != pair[1] {
			return fmt.Errorf("Runtime %s differs: %q %v", pair[0], got, err)
		}
	}
	raw, err := publishedRead(filepath.Join(proc, "exe"))
	if err != nil || publishedHash(raw) != p.binarySHA {
		return errors.New("Runtime executable digest differs")
	}
	raw, err = publishedRead(filepath.Join(proc, "environ"))
	if err != nil {
		return err
	}
	env := strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00")
	sort.Strings(env)
	want := []string{"JPACK_CONFIG=" + p.config, "TMPDIR=" + p.temporary}
	sort.Strings(want)
	if !reflect.DeepEqual(env, want) {
		return errors.New("Runtime environment differs")
	}
	raw, err = publishedRead(filepath.Join(proc, "status"))
	if err != nil {
		return err
	}
	found := false
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "Uid:") {
			fields := strings.Fields(line)
			if len(fields) != 5 {
				return errors.New("malformed Runtime uid")
			}
			for _, uid := range fields[1:] {
				if uid != strconv.Itoa(os.Getuid()) {
					return errors.New("Runtime uid differs")
				}
			}
			found = true
		}
	}
	if !found {
		return errors.New("missing Runtime uid")
	}
	raw, err = publishedRead(p.config)
	if err != nil || string(raw) != publishedConfig {
		return errors.New("Runtime config differs")
	}
	return nil
}
func (p *publishedRPC) call(ctx context.Context, frame json.RawMessage) (json.RawMessage, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var request struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if json.Unmarshal(frame, &request) != nil || len(frame) > 4*1024*1024 || len(p.frames) >= 256 {
		return nil, errors.New("invalid or excessive outbound frame")
	}
	if request.Method == "tools/call" {
		if !sameJSON(request.Params, p.exact) {
			return p.violation("physical call differs from handwritten canonical expectation")
		}
		if !hasPublishedOrder(*p.order, "asked-ack") || !hasPublishedOrder(*p.order, "approved-ack") {
			return p.violation("physical call preceded separate acknowledged approval")
		}
		if p.calls() != 0 {
			return p.violation("physical Runtime action redispatched")
		}
		*p.order = append(*p.order, "physical-dispatch")
	}
	p.frames = append(p.frames, append(json.RawMessage(nil), frame...))
	input, ok := p.stdin.(*os.File)
	if !ok {
		return nil, errors.New("Runtime stdin is not a deadline-capable owned pipe")
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return nil, errors.New("bounded Runtime context deadline required")
	}
	if err := input.SetWriteDeadline(deadline); err != nil {
		return nil, err
	}
	if _, err := p.stdin.Write(append(append([]byte(nil), frame...), '\n')); err != nil {
		return nil, err
	}
	if len(request.ID) == 0 {
		return nil, nil
	}
	for i := 0; i < 64; i++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case reply, ok := <-p.lines:
			if !ok {
				<-p.readDone
				if p.readErr != nil {
					return nil, p.readErr
				}
				return nil, errors.New("Runtime stdout closed before reply")
			}
			p.bytes += len(reply)
			if p.bytes > 16*1024*1024 {
				return nil, errors.New("reply evidence bound exceeded")
			}
			p.replies = append(p.replies, reply)
			var answer struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
				Result json.RawMessage `json:"result"`
				Error  json.RawMessage `json:"error"`
			}
			if json.Unmarshal(reply, &answer) != nil {
				return nil, errors.New("unreadable Runtime reply")
			}
			if len(answer.ID) == 0 && answer.Method != "" {
				continue
			}
			if answer.Method != "" || !bytes.Equal(answer.ID, request.ID) || (len(answer.Result) == 0) == (len(answer.Error) == 0) {
				return nil, errors.New("unexpected or ambiguous Runtime reply")
			}
			return reply, nil
		}
	}
	return nil, errors.New("too many Runtime notifications")
}
func (p *publishedRPC) violation(detail string) (json.RawMessage, error) {
	p.violations = append(p.violations, detail)
	return nil, errors.New(detail)
}
func (p *publishedRPC) calls() int {
	n := 0
	for _, raw := range p.frames {
		var f struct {
			Method string `json:"method"`
		}
		_ = json.Unmarshal(raw, &f)
		if f.Method == "tools/call" {
			n++
		}
	}
	return n
}
func hasPublishedOrder(order []string, want string) bool {
	for _, entry := range order {
		if entry == want {
			return true
		}
	}
	return false
}
func (p *publishedRPC) close(t *testing.T) {
	t.Helper()
	for _, detail := range p.violations {
		t.Error(detail)
	}
	_ = p.stdin.Close()
	select {
	case <-p.waitDone:
	case <-time.After(2 * time.Second):
		// Signal only the original live process after checking held start/exe identity.
		if err := p.observe(); err != nil {
			t.Errorf("cannot verify owned Runtime for teardown: %v", err)
		} else {
			select {
			case <-p.waitDone:
			default:
				if err = p.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
					t.Errorf("owned Runtime kill: %v", err)
				}
			}
		}
		select {
		case <-p.waitDone:
		case <-time.After(2 * time.Second):
			t.Error("owned Runtime wait did not finish")
		}
	}
	readerJoined := false
	select {
	case <-p.readDone:
		readerJoined = true
	case <-time.After(time.Second):
		t.Error("Runtime stdout did not finish after process wait; evidence may be incomplete")
	}
	close(p.stop)
	_ = p.stdout.Close()
	if !readerJoined {
		select {
		case <-p.readDone:
			readerJoined = true
		case <-time.After(time.Second):
			t.Error("Runtime reader did not join")
		}
	}
	if readerJoined {
		for trailing := range p.lines {
			t.Error("unconsumed Runtime output")
			p.replies = append(p.replies, trailing)
		}
	}
	select {
	case <-p.waitDone:
	default:
		return
	}
	_, procErr := os.Stat(fmt.Sprintf("/proc/%d", p.pid))
	processClosed := errors.Is(procErr, os.ErrNotExist)
	if err := procErr; !processClosed {
		t.Errorf("owned Runtime PID remains: %v", err)
	}
	p.stderr.mu.Lock()
	stderr := string(p.stderr.data)
	overflow := p.stderr.overflow
	p.stderr.mu.Unlock()
	if overflow {
		t.Error("stderr evidence truncated")
	}
	if p.readErr != nil && !errors.Is(p.readErr, os.ErrClosed) {
		t.Errorf("Runtime stdout incomplete: %v", p.readErr)
	}
	publishedSave(t, filepath.Join(p.project, "process-closed.json"), map[string]any{"pid": p.pid, "startTime": p.start, "closed": processClosed, "readerJoined": readerJoined, "waitError": fmt.Sprint(p.waitErr), "stderr": stderr, "scope": "only held process; no descendant containment claim; no file deletion"})
}

type publishedStore struct {
	runcontext.RunStore
	mode    string
	order   *[]string
	fault   bool
	commits []map[string]any
}

func (s *publishedStore) Commit(ctx context.Context, in runcontext.RunCommit) (runcontext.RunReceipt, error) {
	var cp contract.Checkpoint
	if len(in.Checkpoint) > 0 {
		if err := json.Unmarshal(in.Checkpoint, &cp); err != nil {
			return runcontext.RunReceipt{}, err
		}
	}
	state := ""
	if cp.Approval != nil {
		state = cp.Approval.State
	}
	result := false
	if cp.Durable != nil {
		for _, d := range cp.Durable.Dispatches {
			if d.Kind == "runtime" && d.State == "result" {
				result = true
			}
		}
	}
	// Unknown result faults before publication, preserving the original dispatched claim.
	if s.mode == "result-ack" && result {
		s.fault = true
		*s.order = append(*s.order, "lost-result-ack")
		return runcontext.RunReceipt{}, errors.New("bounded lost result acknowledgement")
	}
	receipt, err := s.RunStore.Commit(ctx, in)
	if err != nil {
		return receipt, err
	}
	s.commits = append(s.commits, map[string]any{"commitID": in.CommitID, "revision": receipt.Revision, "checkpoint": json.RawMessage(in.Checkpoint), "events": in.Events})
	if s.mode == state+"-ack" {
		s.fault = true
		*s.order = append(*s.order, "lost-"+state+"-ack")
		return runcontext.RunReceipt{}, errors.New("bounded lost approval acknowledgement")
	}
	if state == "asked" || state == "approved" {
		*s.order = append(*s.order, state+"-ack")
	}
	return receipt, nil
}
func runPublished(t *testing.T, ctx context.Context, g *gate.Gate, store runcontext.RunStore, engine string, resume bool, decision *contract.CheckpointApprovalDecision, raw json.RawMessage, link *runtimeActionLink) ([]contract.RunEvent, error) {
	t.Helper()
	events := []contract.RunEvent{}
	run := &adapter.Run{Prompt: "Rehearse the synthetic intake decision.", Wire: "openai-compatible", ModelName: "deterministic-published-runtime-probe", Thinking: "off", Critic: "off", Gate: g.Calls(), Tools: g.Offer().Tools, AllowConversation: true, Context: &runcontext.Input{CurrentRequest: &runcontext.CurrentRequest{Message: "Rehearse the synthetic intake decision.", Input: "fixed synthetic input"}, RunStore: &runcontext.DurableRunInput{Store: store, ID: "runtime-action-run", Resume: resume}, Approval: decision, Link: link}, Emit: func(e contract.RunEvent) { events = append(events, e) }}
	run.EnrollContext = func(_ context.Context, offer adapter.ContextOffer) error {
		return g.EnrollContext(offer.ID, offer.HostTools)
	}
	implementation := eino.New()
	if engine == "native" {
		implementation = native.New()
		run.Agent = &adapter.Agent{Model: "deterministic-published-runtime-probe", Run: func(ctx context.Context, request adapter.AgentRequest, cb adapter.AgentCallbacks) error {
			if cb.ProviderRelease != nil {
				defer func() {
					if !cb.ProviderRelease.Acknowledge(cb.ProviderRelease.Invocation) {
						t.Error("native release acknowledgement refused")
					}
				}()
			}
			if resume {
				return cb.Event(ctx, adapter.AgentEvent{Type: "message", ID: "final", Text: "Synthetic rehearsal completed.", Phase: "final"})
			}
			for _, tool := range request.Tools {
				if strings.HasPrefix(tool.Description, "experimental_evaluate: ") {
					_, err := cb.Tool(ctx, adapter.AgentToolCall{ID: "native-call", Name: tool.Name, Arguments: raw})
					return err
				}
			}
			return errors.New("real Runtime evaluator was not offered")
		}}
	} else {
		requests := 0
		run.Model = func(context.Context, string, relay.Request) (*http.Response, error) {
			requests++
			if resume || requests > 1 {
				return enrollmentFinal(), nil
			}
			chunk := map[string]any{"id": "fixture", "object": "chat.completion.chunk", "model": "deterministic-published-runtime-probe", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "published-call", "type": "function", "function": map[string]any{"name": "experimental_evaluate", "arguments": string(raw)}}}}, "finish_reason": "tool_calls"}}}
			body, err := json.Marshal(chunk)
			if err != nil {
				return nil, err
			}
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: " + string(body) + "\n\ndata: [DONE]\n\n"))}, nil
		}
	}
	err := implementation.Run(ctx, run)
	return events, err
}
func TestRuntimeActionPublishedRuntime(t *testing.T) {
	if os.Getenv("JP_RUNTIME_BINARY") == "" {
		t.Skip("set exact published Runtime artifact and session-owned probe paths")
	}
	if runtime.GOOS != "linux" {
		t.Skip("exact process identity probe requires Linux procfs")
	}
	binary := publishedAbsolute(t, "JP_RUNTIME_BINARY")
	binarySHA := os.Getenv("JP_RUNTIME_BINARY_SHA256")
	if len(binarySHA) != 64 {
		t.Fatal("exact Runtime SHA256 required")
	}
	body, err := publishedRead(binary)
	if err != nil || publishedHash(body) != binarySHA {
		t.Fatal("Runtime artifact digest differs", err)
	}
	pack, err := publishedRead(publishedAbsolute(t, "JP_RUNTIME_PACK"))
	if err != nil || publishedHash(pack) != publishedPackSHA {
		t.Fatal("normative pack digest differs", err)
	}
	root := publishedAbsolute(t, "JP_RUNTIME_PROBE_ROOT")
	temporary := publishedAbsolute(t, "TMPDIR")
	for _, engine := range []string{"eino", "native"} {
		for _, mode := range []string{"confirmed", "refused", "asked-ack", "approved-ack", "result-ack"} {
			t.Run(engine+"/"+mode, func(t *testing.T) {
				project, err := os.MkdirTemp(root, engine+"-"+mode+"-")
				if err != nil {
					t.Fatal(err)
				}
				if err = os.Mkdir(filepath.Join(project, "audit"), 0700); err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(filepath.Join(project, "jpack.json"), []byte(publishedConfig), 0600); err != nil {
					t.Fatal(err)
				}
				raw, _ := json.Marshal(map[string]any{"pack": string(pack), "facts": publishedFacts, "evidence": publishedEvidence, "rehearsal": false})
				canonical, _ := json.Marshal(map[string]any{"pack": string(pack), "facts": publishedFacts, "evidence": publishedEvidence, "rehearsal": true})
				exact, _ := json.Marshal(map[string]any{"name": "experimental_evaluate", "arguments": json.RawMessage(canonical)})
				order := []string{}
				p := startPublishedRPC(t, project, binary, binarySHA, temporary, &order, exact)
				owner := gate.RuntimeExecutionOwner{ArtifactDigest: "sha256:" + binarySHA, ProcessID: fmt.Sprintf("pid-%d-start-%s", p.pid, p.start), ProjectID: publishedHash([]byte(project)), ProjectLabel: "Bounded synthetic intake project", SessionID: fmt.Sprintf("stdio-%d-%s", p.pid, p.start), PrincipalDigest: "sha256:" + publishedHash([]byte(fmt.Sprintf("uid:%d", os.Getuid()))), PrincipalLabel: "Local probe owner", ScopeDigest: "sha256:" + publishedHash([]byte(publishedConfig)), ScopeLabel: "Synthetic rehearsal in held project"}
				observations := 0
				port := &gate.RuntimeActionPort{Owner: owner, Current: func(context.Context) (gate.RuntimeExecutionOwner, error) {
					observations++
					if err := p.observe(); err != nil {
						return gate.RuntimeExecutionOwner{}, err
					}
					return owner, nil
				}}
				notices := []contract.RunEvent{}
				g := gate.New(gate.Options{Granted: []string{"experimental_evaluate"}, RuntimeActions: port, Runtime: p.call, Emit: func(e contract.RunEvent) { notices = append(notices, e) }})
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				offer, err := g.Open(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				if len(offer.Tools) != 1 || offer.Tools[0].Meta.Operation != "evaluate" || offer.Tools[0].Meta.Effect != "act" || offer.Tools[0].Meta.Experimental == nil || !*offer.Tools[0].Meta.Experimental {
					t.Fatal("published Runtime taxonomy differs")
				}
				memory := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
				store := &publishedStore{RunStore: memory, mode: mode, order: &order}
				link := &runtimeActionLink{}
				all := [][]contract.RunEvent{}
				runErrors := []string{}
				first, firstErr := runPublished(t, ctx, g, store, engine, false, nil, raw, link)
				all = append(all, first)
				runErrors = append(runErrors, fmt.Sprint(firstErr))
				if p.calls() != 0 {
					t.Fatal("physical Runtime call preceded person decision")
				}
				if mode == "asked-ack" {
					if !store.fault {
						t.Fatal("asked acknowledgement fault not reached")
					}
					if link.context == nil || link.context.Execution.State().Approval != nil {
						t.Fatal("unacknowledged pause retained asked state")
					}
					for _, event := range first {
						if event.ApprovalRequested != nil {
							t.Fatal("unacknowledged pause exposed actionable request")
						}
					}
				} else {
					if firstErr != nil {
						t.Fatal(firstErr)
					}
					asked := runtimeActionAsked(t, first)
					if asked.Runtime == nil || asked.Runtime.Owner != owner || !sameJSON(mustRuntimeJSON(asked.Args), canonical) {
						t.Fatal("ask did not bind exact actual owner and rewritten arguments")
					}
					head := runtimeActionHead(t, memory)
					if head.Get("version").Num() != 3 || head.Get("status").Str() != "paused" || head.Get("approval").Get("state").Str() != "asked" {
						t.Fatal("original writer did not acknowledge paused asked checkpoint")
					}
					decision := &contract.CheckpointApprovalDecision{CallID: asked.CallID, Approved: mode != "refused"}
					second, secondErr := runPublished(t, ctx, g, store, engine, true, decision, raw, link)
					all = append(all, second)
					runErrors = append(runErrors, fmt.Sprint(secondErr))
					want := 1
					if mode == "refused" || mode == "approved-ack" {
						want = 0
					}
					if p.calls() != want {
						t.Fatalf("physical dispatch count=%d want=%d", p.calls(), want)
					}
					if mode == "confirmed" && secondErr != nil {
						t.Fatal(secondErr)
					}
					if (mode == "approved-ack" || mode == "result-ack") && !store.fault {
						t.Fatal("intended acknowledgement fault not reached")
					}
					if want == 1 {
						found := false
						for _, reply := range p.replies {
							var result struct {
								Result struct {
									IsError    bool `json:"isError"`
									Structured struct {
										Disposition struct {
											Kind      string `json:"kind"`
											OutcomeID string `json:"outcomeId"`
										} `json:"disposition"`
									} `json:"structuredContent"`
								} `json:"result"`
							}
							if json.Unmarshal(reply, &result) == nil && result.Result.Structured.Disposition.OutcomeID == "clarify-return" {
								if result.Result.IsError || result.Result.Structured.Disposition.Kind != "outcome" {
									t.Fatal("actual Runtime evaluation failed")
								}
								found = true
							}
						}
						if !found {
							t.Fatal("actual Runtime did not return handwritten clarify-return outcome")
						}
						saved := runtimeActionHead(t, memory)
						dispatches := 0
						for _, d := range saved.Get("durable").Get("dispatches").Items() {
							if d.Get("kind").Str() == "runtime" {
								dispatches++
								if d.Get("read").Truth() {
									t.Fatal("physical action persisted as replayable read")
								}
								if mode == "confirmed" && d.Get("state").Str() != "result" {
									t.Fatal("actual result missing original writer acknowledgement")
								}
							}
						}
						if dispatches != 1 {
							t.Fatal("original writer exact physical dispatch claim missing")
						}
					}
					// A new exported run without a decision may recover results/unknown state only.
					store.mode = "restored"
					third, thirdErr := runPublished(t, ctx, g, store, engine, true, nil, raw, link)
					all = append(all, third)
					runErrors = append(runErrors, fmt.Sprint(thirdErr))
					if p.calls() != want {
						t.Fatal("resumed unknown or completed action redispatched")
					}
				}
				entries, err := os.ReadDir(filepath.Join(project, "audit"))
				if err != nil || len(entries) != 0 {
					t.Fatal("rehearsal changed real audit directory", err)
				}
				if observations == 0 {
					t.Fatal("actual current owner was never observed")
				}
				publishedSave(t, filepath.Join(project, "evidence.json"), map[string]any{"engine": engine, "mode": mode, "binary": binary, "binarySHA256": binarySHA, "packSHA256": publishedPackSHA, "pid": p.pid, "startTime": p.start, "owner": owner, "command": []string{binary, "mcp"}, "cwd": project, "env": p.cmd.Env, "config": publishedConfig, "order": order, "frames": p.frames, "transportViolations": p.violations, "replies": p.replies, "events": all, "runErrors": runErrors, "commits": store.commits, "notices": notices, "currentOwnerObservations": observations, "auditEntries": len(entries), "provider": "deterministic callback; actual published Runtime transport"})
			})
		}
	}
}
