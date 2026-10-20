package certification

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
)

// runtimeSix are the Runtime's six the gate serves to a scenario, and contextSix the six the
// boundary adds to every conversation.
var (
	runtimeSix = []string{"get_schema", "list_examples", "get_example", "validate", "experimental_evaluate", "experimental_test_cases"}
	contextSix = []string{"list_context", "read_context", "read_window", "search", "update_plan", "request_budget_increase"}
)

type scenarioFile struct {
	Request   string             `json:"request"`
	Memory    string             `json:"memory"`
	HostTools []ScenarioHostTool `json:"hostTools"`
	Turns     []struct {
		Say string `json:"say"`
	} `json:"turns"`
	RuntimeAnswers map[string]json.RawMessage `json:"runtimeAnswers"`
}

func lastSay(file scenarioFile) string { return file.Turns[len(file.Turns)-1].Say }

func messagesOf(events []contract.RunEvent) []string {
	var out []string
	for _, event := range events {
		if event.Message != nil {
			out = append(out, event.Message.Text)
		}
	}
	return out
}

func endsOf(events []contract.RunEvent) int {
	n := 0
	for _, event := range events {
		if event.End != nil {
			n++
		}
	}
	return n
}

// ChatHostTools is scenario 1: a read, a proposal that stops at the review card, a refused name.
var ChatHostTools = ScenarioCheck{
	Scenario: "chat-host-tools",
	Names:    []string{"1 offer", "1 refused", "1 proposal", "1 answer"},
	Run: func(ctx context.Context, server *Server, answerAs string, adapter Adapter) ([]Finding, error) {
		var file scenarioFile
		if err := ScenarioFile("chat-host-tools", &file); err != nil {
			return nil, err
		}
		ledger := &HostLedger{}
		hostTools := HostToolsOf(file.HostTools, ledger)
		host, err := openScenario(ctx, server, ScenarioEndpoint{Scenario: "chat-host-tools", AnswerAs: answerAs}, hostTools, true)
		if err != nil {
			return nil, err
		}
		ran, err := host.runOn(ctx, adapter, &Run{Prompt: file.Request, AllowConversation: true, HostTools: hostTools, Context: requestInput(file.Request)})
		if err != nil {
			return nil, err
		}
		var out []Finding
		f := &findings{check: "1 offer"}
		if len(ran.Requests) == 0 {
			f.fail("no request")
		} else {
			offered := offeredNames(ran.Requests[0])
			want := sortedCopy(append(append(append([]string{}, runtimeSix...), contextSix...), "read_policy", "propose_pack"))
			if !slices.Equal(sortedCopy(offered), want) || slices.Contains(offered, "save_pack") {
				f.fail("offered %v", offered)
			}
		}
		out = append(out, f.list...)
		f = &findings{check: "1 refused"}
		refused := guardrailsOf(ran.Events, "refused")
		if len(refused) != 1 || refused[0].Tool != "save_pack" {
			f.fail("the refusals were %s", comparisonJSON(refused))
		} else {
			flow := withoutCheckpoints(ran.Events)
			at := -1
			for i, event := range flow {
				if event.Guardrail == refused[0] {
					at = i
				}
			}
			if at < 1 || at+1 >= len(flow) || flow[at-1].ToolCall == nil || flow[at-1].ToolCall.Name != "save_pack" ||
				flow[at+1].ToolResult == nil || flow[at+1].ToolResult.Name != "save_pack" || !flow[at+1].ToolResult.IsError {
				f.fail("the refusal is not between save_pack's call and its failed result")
			}
		}
		if len(ran.Seen) > 0 {
			f.fail("the runtime saw %v", seenNames(ran.Seen))
		}
		told := false
		if len(ran.Requests) > 0 {
			var body struct {
				Messages []struct {
					Role       string          `json:"role"`
					ToolCallID string          `json:"tool_call_id"`
					Content    json.RawMessage `json:"content"`
				} `json:"messages"`
			}
			_ = json.Unmarshal(ran.Requests[len(ran.Requests)-1].Body, &body)
			for _, message := range body.Messages {
				if message.Role == "tool" && message.ToolCallID == "call_2" {
					told = strings.Contains(string(message.Content), "never calls a tool it was not offered")
				}
			}
		}
		if !told {
			f.fail("the model was not told the gate's refusal")
		}
		out = append(out, f.list...)
		f = &findings{check: "1 proposal"}
		if names := callNames(ran.Events); !slices.Equal(names, []string{"read_policy", "save_pack", "propose_pack"}) {
			f.fail("the calls were %v", names)
		}
		proposals := proposalsOf(ran.Events)
		if len(proposals) != 1 || !sameJSON([]byte(comparisonJSON(proposals[0].Document)), []byte(`{"id":"expense-receipts","title":"Expense receipts"}`)) ||
			!slices.Equal(proposals[0].Unknowns, []string{"Whether a receipt must be itemized."}) {
			f.fail("the proposals were %s", comparisonJSON(proposals))
		}
		if executed := ledger.executed(); len(executed) != 1 || executed["read_policy"] != 1 {
			f.fail("the host ran %v", executed)
		}
		if errs := errorsOf(ran.Events); len(errs) > 0 {
			f.fail("errors: %s", errs[0].Message)
		}
		out = append(out, f.list...)
		f = &findings{check: "1 answer"}
		if messages := messagesOf(ran.Events); !slices.Equal(messages, []string{lastSay(file)}) || endsOf(ran.Events) != 1 {
			f.fail("the messages were %q with %d ends", messages, endsOf(ran.Events))
		}
		return append(out, f.list...), nil
	},
}

// Memory is scenario 3: memory recalled once for the person's message and carried in its layer on
// every request of a run over three tool steps (ADR-0005), and the allowance, which takes no reserve.
var Memory = ScenarioCheck{
	Scenario: "memory",
	Names:    []string{"3 run", "3 memory", "3 reserve"},
	Run: func(ctx context.Context, server *Server, answerAs string, adapter Adapter) ([]Finding, error) {
		var file scenarioFile
		if err := ScenarioFile("memory", &file); err != nil {
			return nil, err
		}
		host, err := openScenario(ctx, server, ScenarioEndpoint{Scenario: "memory", AnswerAs: answerAs}, nil, true)
		if err != nil {
			return nil, err
		}
		input := requestInput(file.Request)
		on := true
		input.Memory = &on
		var asked []string
		recall := runcontext.HostFuncs{MemoryForFunc: func(_ context.Context, query string) (string, error) {
			asked = append(asked, query)
			return file.Memory, nil
		}}
		ran, err := host.runOn(ctx, adapter, &Run{Prompt: file.Request, AllowConversation: true, Context: input, Host: recall})
		if err != nil {
			return nil, err
		}
		var out []Finding
		f := &findings{check: "3 run"}
		three := []string{"get_schema", "list_examples", "get_example"}
		if !slices.Equal(callNames(ran.Events), three) || !slices.Equal(seenNames(ran.Seen), three) || len(ran.Requests) != 4 {
			f.fail("calls %v, seen %v, %d requests", callNames(ran.Events), seenNames(ran.Seen), len(ran.Requests))
		}
		if errs := errorsOf(ran.Events); len(errs) > 0 {
			f.fail("errors: %s", errs[0].Message)
		}
		out = append(out, f.list...)
		f = &findings{check: "3 memory"}
		if !slices.Equal(asked, []string{file.Request}) {
			f.fail("memoryFor was asked %q", asked)
		}
		block := runcontext.MemoryHeader + "\n" + file.Memory + "\n" + runcontext.HostPartsTable[2].End
		prefixes := map[string]bool{}
		for at, request := range ran.Requests {
			system, rest := systemSplit("openai-compatible", request.Body)
			place := strings.Index(system, block)
			if place < 0 || strings.Contains(rest, runcontext.MemoryHeader) {
				f.fail("request %d does not carry the memory in its system text alone", at+1)
				continue
			}
			prefixes[system[:place+len(block)]] = true
		}
		if len(prefixes) > 1 {
			f.fail("the stable prefix differs across the run's requests: %d different", len(prefixes))
		}
		out = append(out, f.list...)
		f = &findings{check: "3 reserve"}
		for _, finding := range reserveChecks() {
			f.fail("%s", finding)
		}
		return append(out, f.list...), nil
	},
}

// reserveChecks are the allowance's (reserveChecks): no reserve is taken for the brief or the
// memory, whatever the host says of memory. The same for every adapter, because the budget is the
// context's.
func reserveChecks() []string {
	var out []string
	instructions := "instructions"
	full := runcontext.InputBytes(runcontext.Defaults) - runcontext.UTF8Bytes(instructions) - runcontext.UTF8Bytes("[]") - 1536
	session := func(memory *bool) runcontext.Session {
		input := &runcontext.Input{Memory: memory}
		return runcontext.Session{Model: &runcontext.ModelSession{Family: "openai-compatible", Model: "scripted-model"}, Context: input}
	}
	check := func(memory *bool, fits, refused int) {
		held, err := runcontext.NewSessionContext(session(memory))
		if err != nil {
			out = append(out, err.Error())
			return
		}
		defer held.Close()
		if err := held.Check(instructions, strings.Repeat("p", fits), runcontext.Array()); err != nil {
			out = append(out, "a prompt within the allowance was refused")
		}
		if err := held.Check(instructions, strings.Repeat("p", refused), runcontext.Array()); err == nil {
			out = append(out, "a prompt past the allowance was admitted")
		}
	}
	on, off := true, false
	check(&on, full, full+1)
	check(nil, full, full+1)
	check(&off, full, full+1)
	return out
}

// PlanMode is scenario 4: the offer, the refused actions, and the plan.
var PlanMode = ScenarioCheck{
	Scenario: "plan-mode",
	Names:    []string{"4 offer", "4 reads", "4 refused", "4 plan"},
	Run: func(ctx context.Context, server *Server, answerAs string, adapter Adapter) ([]Finding, error) {
		var file scenarioFile
		if err := ScenarioFile("plan-mode", &file); err != nil {
			return nil, err
		}
		ledger := &HostLedger{}
		hostTools := HostToolsOf(file.HostTools, ledger)
		host, err := openScenario(ctx, server, ScenarioEndpoint{Scenario: "plan-mode", AnswerAs: answerAs}, hostTools, true)
		if err != nil {
			return nil, err
		}
		// The host enters Plan mode: the gate's own phase, which the adapter cannot lift.
		host.gate.Enter(gate.PlanPhase(host.offer))
		input := requestInput(file.Request)
		input.Phase = "plan"
		ran, err := host.runOn(ctx, adapter, &Run{Prompt: file.Request, AllowConversation: true, HostTools: hostTools, Context: input})
		if err != nil {
			return nil, err
		}
		var out []Finding
		var offered []string
		if len(ran.Requests) > 0 {
			offered = offeredNames(ran.Requests[0])
		}
		f := &findings{check: "4 offer"}
		want := sortedCopy(append(append(append([]string{}, gate.PlanRuntimeReads...), "read_policy", "propose_plan"), contextSix...))
		if !slices.Equal(sortedCopy(offered), want) {
			f.fail("offered %v", offered)
		}
		for _, action := range []string{"propose_pack", "save_recipe", "remember_note", gate.RehearsalTool, gate.TestTool} {
			if slices.Contains(offered, action) {
				f.fail("offered %s", action)
			}
		}
		out = append(out, f.list...)
		f = &findings{check: "4 reads"}
		var reads []string
		for _, name := range offered {
			if slices.Contains(runtimeSix, name) {
				reads = append(reads, name)
			}
		}
		if !slices.Equal(sortedCopy(reads), []string{"get_example", "get_schema", "list_examples", "validate"}) {
			f.fail("the Runtime reads offered were %v", reads)
		}
		out = append(out, f.list...)
		f = &findings{check: "4 refused"}
		flow := withoutCheckpoints(ran.Events)
		for _, name := range []string{gate.RehearsalTool, "save_recipe"} {
			at := -1
			for i, event := range flow {
				if event.ToolCall != nil && event.ToolCall.Name == name {
					at = i
					break
				}
			}
			if at < 0 || at+1 >= len(flow) || flow[at+1].ToolResult == nil || flow[at+1].ToolResult.Name != name || !flow[at+1].ToolResult.IsError ||
				!strings.Contains(flow[at+1].ToolResult.Text, "Plan mode does not permit this operation") {
				f.fail("%s was not refused by Plan mode next to its call", name)
			}
		}
		if len(ran.Seen) > 0 || ledger.executed()["save_recipe"] > 0 {
			f.fail("the runtime saw %v, the host ran %v", seenNames(ran.Seen), ledger.executed())
		}
		out = append(out, f.list...)
		f = &findings{check: "4 plan"}
		var plans []Received
		for _, received := range ledger.Received {
			if received.Tool == "propose_plan" {
				plans = append(plans, received)
			}
		}
		if len(plans) != 1 || !strings.Contains(string(plans[0].Arguments), `"objective":"Add a receipt rule"`) ||
			!strings.Contains(string(plans[0].Arguments), `"steps":["Read the receipts section","Draft the rule","Test the rule against the saved cases"]`) {
			f.fail("the plan received was %v", plans)
		}
		planned := false
		for _, event := range ran.Events {
			if event.ToolResult != nil && event.ToolResult.Name == "propose_plan" {
				planned = !event.ToolResult.IsError
			}
		}
		if !planned || len(proposalsOf(ran.Events)) > 0 || !slices.Equal(messagesOf(ran.Events), []string{lastSay(file)}) || len(errorsOf(ran.Events)) > 0 || endsOf(ran.Events) != 1 {
			f.fail("plan result %t, %d proposals, messages %q, %d errors, %d ends", planned, len(proposalsOf(ran.Events)), messagesOf(ran.Events), len(errorsOf(ran.Events)), endsOf(ran.Events))
		}
		return append(out, f.list...), nil
	},
}

// Scenarios are the certified scenarios, each run once on each answer shape.
var Scenarios = []ScenarioCheck{ChatHostTools, Memory, PlanMode, CorrectionLoop, Transcript, ToolSources}
