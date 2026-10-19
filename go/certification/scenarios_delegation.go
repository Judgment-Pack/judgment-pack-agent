package certification

import (
	"context"
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/turn"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
)

// children is the host's Children capability for a scenario: a child's gated session is a gate of
// its own over the same runtime, its offer the session's, in the child's phase within the phase the
// host's gate is in (gate.ChildPhase), its guardrails to the child's events.
func (h *scenarioHost) children() adapter.Children {
	return func(ctx context.Context, profile contract.DelegatedTaskProfile, emit func(contract.RunEvent)) (*gate.Calls, []contract.ToolOfferRuntimeTool, error) {
		child := gate.New(gate.Options{Runtime: gate.Call(h.session.RuntimeCall()), Granted: gate.Ceiling, Emit: emit, TestCalls: h.gate.TestCalls()})
		child.SetOffer(h.offer)
		phase := gate.ChildPhase(profile, h.offer, h.gate.Phase())
		child.Enter(phase)
		return child.Calls(), phase.Offer(h.offer).Tools, nil
	}
}

type delegationFile struct {
	Documents []struct {
		ID    string          `json:"id"`
		Kind  string          `json:"kind"`
		Label string          `json:"label"`
		Value json.RawMessage `json:"value"`
	} `json:"documents"`
	HostTools []ScenarioHostTool `json:"hostTools"`
	Responses []struct {
		Request string `json:"request"`
	} `json:"responses"`
}

// The words a child's brief starts with, by which a child's requests are told from its parent's.
const childBrief = "Complete this read-only task"

func isChildRequest(request EndpointRequest) bool {
	for _, text := range openingTexts(request) {
		if strings.Contains(text, childBrief) {
			return true
		}
	}
	return false
}

type briefRead struct {
	Objective  string `json:"objective"`
	References []struct {
		ID    string `json:"id"`
		Label string `json:"label"`
	} `json:"references"`
	EarlierWork []struct {
		Objective string `json:"objective"`
		Result    string `json:"result"`
	} `json:"earlierWork"`
}

// briefIn is the brief a child was sent: the JSON line its prompt ends with.
func briefIn(prompt string) (briefRead, string) {
	var brief briefRead
	at := strings.LastIndex(prompt, "\n{\"objective\"")
	if at < 0 {
		return brief, ""
	}
	line := strings.SplitN(prompt[at+1:], "\n", 2)[0]
	_ = json.Unmarshal([]byte(line), &brief)
	return brief, line
}

func childBriefOf(request EndpointRequest) (briefRead, string) {
	for _, text := range openingTexts(request) {
		if strings.Contains(text, childBrief) {
			return briefIn(text)
		}
	}
	return briefRead{}, ""
}

// tasksIn are the tasks a delegate call answered, read out of the framed text that carries them.
func tasksIn(text string) []struct {
	ID    string `json:"id"`
	Title string `json:"title"`
} {
	type task = struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	var found []task
	for _, block := range strings.Split(text, "BEGIN RETRIEVED DATA ")[1:] {
		open := strings.Index(block, "\n")
		end := strings.LastIndex(block, "\nEND RETRIEVED DATA ")
		if open < 0 || !strings.Contains(block[:open], `"delegate_tasks"`) {
			continue
		}
		body := block[open+1:]
		if end > open {
			body = block[open+1 : end]
		}
		var inner struct {
			StructuredContent struct {
				Tasks []task `json:"tasks"`
			} `json:"structuredContent"`
			Tasks []task `json:"tasks"`
		}
		if json.Unmarshal([]byte(body), &inner) != nil {
			continue
		}
		if len(inner.StructuredContent.Tasks) > 0 {
			found = append(found, inner.StructuredContent.Tasks...)
		} else {
			found = append(found, inner.Tasks...)
		}
	}
	return found
}

// toolContents are the tool messages' texts a request carried.
func toolContents(request EndpointRequest) []string {
	var body struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	_ = json.Unmarshal(request.Body, &body)
	var out []string
	for _, message := range body.Messages {
		if message.Role == "tool" {
			var text string
			if json.Unmarshal(message.Content, &text) != nil {
				text = string(message.Content)
			}
			out = append(out, text)
		}
	}
	return out
}

func tasksOf(events []contract.RunEvent) []contract.DelegatedTask {
	order := []string{}
	last := map[string]contract.DelegatedTask{}
	for _, event := range events {
		if event.DelegatedTask != nil {
			task := event.DelegatedTask.Task
			if _, ok := last[task.ID]; !ok {
				order = append(order, task.ID)
			}
			last[task.ID] = task
		}
	}
	var out []contract.DelegatedTask
	for _, id := range order {
		out = append(out, last[id])
	}
	return out
}

func lastCheckpoint(events []contract.RunEvent) *contract.Checkpoint {
	var held *contract.Checkpoint
	for _, event := range events {
		if event.ExecutionCheckpoint != nil {
			state := event.ExecutionCheckpoint.Checkpoint
			held = &state
		}
	}
	return held
}

var policyEntry = regexp.MustCompile(`^(.+):policy:[0-9a-f]{16}$`)

func tokensAre(tokens *contract.ProvenanceTokenFigures, input, output int64) bool {
	return tokens != nil && tokens.InputTokens != nil && tokens.OutputTokens != nil && *tokens.InputTokens == input && *tokens.OutputTokens == output
}

// Delegation is scenario 2: a batch of two children, their findings, and a resumed child.
func Delegation(with func(adapter.Adapter) adapter.Adapter) ScenarioCheck {
	return ScenarioCheck{
		Scenario: "delegation",
		Names:    []string{"2 runs", "2 findings", "2 offers", "2 references", "2 reports", "2 figures", "2 resume"},
		Run: func(ctx context.Context, server *Server, answerAs string, inner Adapter) ([]Finding, error) {
			var file delegationFile
			if err := ScenarioFile("delegation", &file); err != nil {
				return nil, err
			}
			ledger := &HostLedger{}
			hostTools := HostToolsOf(file.HostTools, ledger)
			declared := append([]runcontext.HostTool{}, hostTools...)
			delegate := turn.DelegationTool(turn.TaskProfiles)
			declared = append(declared, runcontext.HostTool{Name: delegate.Name, Description: delegate.Description, InputSchema: delegate.InputSchema, Interaction: "delegation"})
			host, err := openScenario(ctx, server, ScenarioEndpoint{Scenario: "delegation", AnswerAs: answerAs}, declared, true)
			if err != nil {
				return nil, err
			}
			var mu sync.Mutex
			parents, children := 0, 0
			counted := adapter.Func(func(ctx context.Context, run *adapter.Run) error {
				offered := false
				for _, tool := range run.HostTools {
					offered = offered || tool.Name == turn.DelegateTool
				}
				mu.Lock()
				if offered {
					parents++
				} else {
					children++
				}
				mu.Unlock()
				return inner.Run(ctx, run)
			})
			wrapped := with(counted)
			chat := runcontext.NewChatContext("")
			var documents []runcontext.Document
			for _, document := range file.Documents {
				value, _ := runcontext.ParseJSON(document.Value)
				documents = append(documents, runcontext.Document{ID: document.ID, Kind: document.Kind, Label: document.Label, Value: value})
			}
			var ran []*ScenarioRun
			sent := 0
			for at, response := range file.Responses {
				if at > 0 {
					if err := host.session.Advance(ctx); err != nil {
						return nil, err
					}
				}
				input := requestInput(response.Request)
				input.Chat = chat
				input.Documents = documents
				one, err := host.runOn(ctx, wrapped, &Run{Prompt: response.Request, AllowConversation: true, Interactive: true, HostTools: hostTools, Context: input, Children: host.children()})
				if err != nil {
					return nil, err
				}
				one.Requests = one.Requests[sent:]
				sent += len(one.Requests)
				ran = append(ran, one)
			}
			var tasks []contract.DelegatedTask
			for _, one := range ran {
				tasks = append(tasks, tasksOf(one.Events)...)
			}
			byTitle := func(title string) *contract.DelegatedTask {
				for at := range tasks {
					if tasks[at].Title == title {
						return &tasks[at]
					}
				}
				return nil
			}
			var out []Finding
			f := &findings{check: "2 runs"}
			if parents != 2 || children != 3 {
				f.fail("%d parent runs and %d child runs, not 2 and 3", parents, children)
			}
			for at, one := range ran {
				own := 0
				for _, request := range one.Requests {
					if !isChildRequest(request) {
						own++
					}
				}
				if own <= 1 {
					f.fail("response %d: the parent made %d requests", at+1, own)
				}
			}
			out = append(out, f.list...)
			f = &findings{check: "2 findings"}
			var parentRequests []EndpointRequest
			for _, request := range ran[0].Requests {
				if !isChildRequest(request) {
					parentRequests = append(parentRequests, request)
				}
			}
			result := ""
			if len(parentRequests) > 0 {
				for _, text := range toolContents(parentRequests[len(parentRequests)-1]) {
					if strings.Contains(text, `from tool "delegate_tasks"`) {
						result = text
					}
				}
			}
			for _, text := range []string{"The mileage section names no rate and no unit.", "The 2026 rate is 0.58 per mile."} {
				if !strings.Contains(result, text) {
					f.fail("the delegate call's result does not carry %q", text)
				}
			}
			var titles []string
			for _, task := range tasksIn(result) {
				titles = append(titles, task.Title)
			}
			if !slices.Equal(titles, []string{"Review section 4", "Research the rate"}) {
				f.fail("the result's tasks were %v", titles)
			}
			out = append(out, f.list...)
			f = &findings{check: "2 offers"}
			offers := map[string][]string{}
			for _, request := range ran[0].Requests {
				if isChildRequest(request) {
					brief, _ := childBriefOf(request)
					offers[brief.Objective] = sortedCopy(offeredNames(request))
				}
			}
			if want := sortedCopy(append([]string{"read_policy"}, contextSix...)); !slices.Equal(offers["Review the mileage section of the policy for gaps."], want) {
				f.fail("the review child was offered %v", offers["Review the mileage section of the policy for gaps."])
			}
			if want := sortedCopy(append([]string{"read_policy", "read_rates"}, contextSix...)); !slices.Equal(offers["Find the published mileage rate for 2026."], want) {
				f.fail("the research child was offered %v", offers["Find the published mileage rate for 2026."])
			}
			for _, names := range offers {
				for _, closed := range append([]string{turn.DelegateTool}, runtimeSix...) {
					if slices.Contains(names, closed) {
						f.fail("a child was offered %s", closed)
					}
				}
			}
			if len(ran[0].Requests) == 0 || !slices.Contains(offeredNames(ran[0].Requests[0]), turn.DelegateTool) {
				f.fail("the parent was not offered delegate_tasks")
			}
			out = append(out, f.list...)
			f = &findings{check: "2 references"}
			review, research, resumed := byTitle("Review section 4"), byTitle("Research the rate"), byTitle("Rate unit")
			if review == nil || research == nil || resumed == nil {
				f.fail("the tasks were %d, not the three", len(tasks))
				return append(out, f.list...), nil
			}
			if len(review.References) != 1 || !strings.HasPrefix(review.References[0], chat.Scope()+":policy:") || !policyEntry.MatchString(review.References[0]) || !chat.HasEntry(review.References[0]) {
				f.fail("the review task's references were %v", review.References)
			}
			var reviewBrief briefRead
			var reviewLine string
			read := ""
			for _, request := range ran[0].Requests {
				if isChildRequest(request) && strings.Contains(string(request.Body), "Review the mileage section") && reviewLine == "" {
					reviewBrief, reviewLine = childBriefOf(request)
				}
				for _, text := range toolContents(request) {
					if strings.Contains(text, `from tool "read_window"`) {
						read = text
					}
				}
			}
			if len(reviewBrief.References) != 1 || len(review.References) != 1 || reviewBrief.References[0].ID != review.References[0] || reviewBrief.References[0].Label != "Expense policy" {
				f.fail("the review brief's references were %v", reviewBrief.References)
			}
			if !strings.Contains(read, "Parking is paid up to 20 USD a day.") || strings.Contains(reviewLine, "Parking is paid") {
				f.fail("the child did not read the reference's text, or the brief copied it")
			}
			if research.References != nil {
				f.fail("the research task has references")
			}
			out = append(out, f.list...)
			f = &findings{check: "2 reports"}
			kinds := map[string]string{"review": "review", "research": "research"}
			for _, task := range []*contract.DelegatedTask{review, research, resumed} {
				encoded, _ := json.Marshal(task)
				if task.State != "complete" || !turn.ValidDelegatedTask(encoded) || task.Report == nil {
					f.fail("%s: state %s, valid %t", task.Title, task.State, turn.ValidDelegatedTask(encoded))
					continue
				}
				report, _ := json.Marshal(task.Report)
				if _, problem := turn.ReadReport(kinds[task.Profile], report); problem != "" {
					f.fail("%s: the report is not its profile's shape: %s", task.Title, problem)
				}
			}
			reviewReport, _ := json.Marshal(review.Report)
			researchReport, _ := json.Marshal(research.Report)
			if !strings.Contains(string(reviewReport), `"severity":"gap"`) || strings.Count(string(reviewReport), `"severity":"gap"`) != 2 {
				f.fail("the review findings' severities are not gap and gap")
			}
			if !strings.Contains(string(researchReport), `"citations":["read_rates"]`) {
				f.fail("the research finding does not cite read_rates")
			}
			out = append(out, f.list...)
			f = &findings{check: "2 figures"}
			for _, task := range []*contract.DelegatedTask{review, research, resumed} {
				if task.Model == nil || *task.Model != "scripted-model" || task.DurationMs == nil || task.FirstByteMs == nil {
					f.fail("%s: model %v, duration %v, first byte %v", task.Title, task.Model, task.DurationMs, task.FirstByteMs)
				}
			}
			checkpoint := lastCheckpoint(ran[0].Events)
			if checkpoint == nil || checkpoint.Delegation == nil || len(checkpoint.Delegation.Batches) != 1 {
				f.fail("the last checkpoint carries no delegation figures of one batch")
			} else {
				figures := checkpoint.Delegation
				batch := figures.Batches[0]
				var profiles []string
				for _, child := range batch.Children {
					profiles = append(profiles, child.Profile)
				}
				if !slices.Equal(profiles, []string{"review", "research"}) || batch.Total.ToolCalls != 1 {
					f.fail("the batch's children were %v with %d tool calls", profiles, batch.Total.ToolCalls)
				}
				if !tokensAre(review.Tokens, 680, 60) || !tokensAre(research.Tokens, 710, 58) {
					f.fail("the children's tokens were %s and %s", comparisonJSON(review.Tokens), comparisonJSON(research.Tokens))
				}
				if batch.Total.ModelRequests != 4 || !tokensAre(batch.Total.Tokens, 1390, 118) {
					f.fail("the batch's total was %s", comparisonJSON(batch.Total))
				}
				if figures.Parent.Model != "scripted-model" || figures.Parent.ModelRequests != 3 || !tokensAre(figures.Parent.Tokens, 2030, 158) {
					f.fail("the parent's figures were %s", comparisonJSON(figures.Parent))
				}
			}
			if second := lastCheckpoint(ran[1].Events); second == nil || second.Delegation == nil || second.Delegation.Parent.ModelRequests != 2 || !tokensAre(second.Delegation.Parent.Tokens, 1600, 80) {
				f.fail("the second response's parent figures were wrong")
			}
			if messages := messagesOf(ran[0].Events); len(messages) == 0 || !strings.Contains(messages[len(messages)-1], "0.58 per mile") {
				f.fail("the answer does not carry the figure")
			}
			out = append(out, f.list...)
			f = &findings{check: "2 resume"}
			if resumed.Resumes == nil || *resumed.Resumes != research.ID {
				f.fail("Rate unit does not resume the research task")
			}
			var brief briefRead
			for _, request := range ran[1].Requests {
				if isChildRequest(request) {
					brief, _ = childBriefOf(request)
					break
				}
			}
			if brief.Objective != "Is the published rate per mile or per kilometre?" || len(brief.EarlierWork) != 1 ||
				brief.EarlierWork[0].Objective != "Find the published mileage rate for 2026." || brief.EarlierWork[0].Result != "The published 2026 rate is 0.58 per mile." {
				f.fail("the resumed brief was %s", comparisonJSON(brief))
			}
			if messages := messagesOf(ran[1].Events); len(messages) == 0 || !strings.Contains(messages[len(messages)-1], "per mile, not per kilometre") {
				f.fail("the second answer does not follow")
			}
			for _, one := range ran {
				if errs := errorsOf(one.Events); len(errs) > 0 {
					f.fail("errors: %s", errs[0].Message)
				}
			}
			return append(out, f.list...), nil
		},
	}
}
