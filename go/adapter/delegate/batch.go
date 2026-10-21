package delegate

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/turn"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
)

// planned is one task of a batch, ready to run.
type planned struct {
	request    contract.DelegatedTaskTaskRequest
	profile    *turn.TaskProfile
	budget     turn.TaskBudget
	references []*runcontext.Entry
	visible    map[string]bool
	thread     *runcontext.TaskThread
}

// delegateTool is delegate_tasks, executed in the parent's step: one batch at a time.
func (c *coordinator) delegateTool() runcontext.HostTool {
	declared := turn.DelegationTool(c.profiles)
	var mu sync.Mutex
	tool := runcontext.HostTool{Name: declared.Name, Description: declared.Description, InputSchema: declared.InputSchema, Interaction: "delegation"}
	tool.Execute = func(ctx context.Context, args json.RawMessage) (runcontext.ToolResult, error) {
		request, err := turn.ReadDelegationRequest(args)
		if err != nil {
			return runcontext.ToolResult{}, err
		}
		mu.Lock()
		defer mu.Unlock()
		tasks, err := c.runBatch(ctx, request)
		if err != nil {
			return runcontext.ToolResult{}, err
		}
		if err := ctx.Err(); err != nil {
			return runcontext.ToolResult{}, failure.Cancelled
		}
		answered := c.batchAnswer(tasks)
		c.batchMu.Lock()
		for _, held := range c.pending {
			_ = c.chat.KeepResult(held.key, held.kept)
		}
		c.pending = nil
		c.batchMu.Unlock()
		return answered, nil
	}
	return tool
}

// resolve is an entry of the parent's context by its id, or by the id the host gave a document;
// never another chat's.
func (c *coordinator) resolve(reference string) *runcontext.Entry {
	entries := c.chat.Entries()
	for _, entry := range entries {
		if entry.ID == reference {
			return entry
		}
	}
	prefix := c.chat.Scope() + ":" + reference + ":"
	var found *runcontext.Entry
	for _, entry := range entries {
		if strings.HasPrefix(entry.ID, prefix) && len(entry.ID) == len(prefix)+16 {
			found = entry
		}
	}
	return found
}

func (c *coordinator) plan(request contract.DelegatedTaskTaskRequest) (*planned, error) {
	var profile *turn.TaskProfile
	for at := range c.profiles {
		if c.profiles[at].ID == request.Profile {
			profile = &c.profiles[at]
		}
	}
	var thread *runcontext.TaskThread
	if request.Resume != nil {
		thread = c.chat.Task(*request.Resume)
		if thread == nil {
			return nil, failure.ToolInput("There is no finished task " + *request.Resume + " in this chat to resume. Resume a task this chat’s delegations completed, by the id its result gave.")
		}
		if thread.Profile != request.Profile {
			return nil, failure.ToolInput("Task " + *request.Resume + " ran as " + thread.Profile + "; a resumed task keeps its profile.")
		}
	}
	var references []*runcontext.Entry
	var missing []string
	for _, name := range request.References {
		entry := c.resolve(name)
		if entry == nil {
			quoted, _ := json.Marshal(name)
			missing = append(missing, string(quoted))
		}
		references = append(references, entry)
	}
	if len(missing) > 0 {
		return nil, failure.ToolInput("Not in your context: " + strings.Join(missing, ", ") + ". Pass a contextId from list_context or a result reference, or a document id.")
	}
	visible := map[string]bool{}
	if thread != nil {
		visible = thread.Visible
	}
	for _, entry := range references {
		c.chat.Reveal(visible, entry.ID)
	}
	budget := turn.ProfileBudget(turn.TaskProfile{})
	if profile != nil {
		budget = turn.ProfileBudget(*profile)
	}
	return &planned{request: request, profile: profile, budget: budget, references: references, visible: visible, thread: thread}, nil
}

// childModelName is the admitted role's model, falling back to the profile's legacy selection.
func (c *coordinator) childModelName(profile turn.TaskProfile) string {
	binding, _, _, err := c.childBinding(profile)
	if err != nil {
		return c.run.ModelName
	}
	if binding.Agent != nil {
		return binding.Agent.Model
	}
	return binding.ModelName
}

// reuseKey is the key an identical task is found by in this chat (agent#17).
func reuseKey(p *planned, model string, providers ...*contract.ProvenanceProvider) string {
	var ids []string
	for _, entry := range p.references {
		ids = append(ids, entry.ID)
	}
	sort.Strings(ids)
	if ids == nil {
		ids = []string{}
	}
	value := runcontext.ObjectOf("profile", runcontext.String(p.request.Profile), "model", runcontext.String(model),
		"objective", runcontext.String(p.request.Objective))
	held := runcontext.Array()
	for _, id := range ids {
		held.Append(runcontext.String(id))
	}
	value.Set("references", held)
	if len(providers) > 0 && providers[0] != nil {
		value.Set("provider", runcontext.ObjectOf("connection", runcontext.String(providers[0].Connection), "family", runcontext.String(providers[0].Family)))
	}
	return string(value.JSON())
}

// brief is what a child is sent: its profile's instructions, the rules, and its brief's line.
func (c *coordinator) brief(p *planned, profile turn.TaskProfile) string {
	brief := turn.Brief{Objective: p.request.Objective, TimeLimitSeconds: int64(p.budget.Milliseconds+500) / 1000}
	for _, entry := range p.references {
		brief.References = append(brief.References, contract.DelegatedTaskBriefReference{ID: entry.ID, Label: entry.Label, Kind: entry.Kind, Bytes: int64(entry.Bytes)})
	}
	if p.thread != nil {
		for _, exchange := range p.thread.Exchanges {
			var held contract.DelegatedTaskEarlierWork
			_ = json.Unmarshal(exchange.JSON(), &held)
			brief.EarlierWork = append(brief.EarlierWork, held)
		}
	}
	return turn.ChildPrompt(profile, brief)
}

// childRun is what one run of a child left.
type childRun struct {
	text, progress, failed, streamed string
	questions                        []*runcontext.Value
	paused                           bool
	err                              error
}

// childLink attaches a child's context to its grant, its tool budget and its meter, and hands its
// boundary the parent's source gate (ADR-0006): the child's source calls count against the turn and
// reach the parent's report, as its test calls do.
type childLink struct {
	limit   int
	calls   func(*contract.DelegatedTask) int64
	task    *contract.DelegatedTask
	budget  turn.TaskBudget
	m       *meter
	sources func() *runcontext.SourceGate
}

// ParentSources is the source gate of the run that delegated this one, or nil.
func (l childLink) ParentSources() *runcontext.SourceGate { return l.sources() }

func (l childLink) Attach(context *runcontext.SessionContext) {
	context.Execution.LimitTo(l.limit)
	context.Execution.ToolAllowance = func() *runcontext.ToolAllowance {
		return &runcontext.ToolAllowance{Used: int(l.calls(l.task)), Limit: int(l.budget.ToolCalls), Scope: "task"}
	}
	l.m.mu.Lock()
	l.m.contexts = append(l.m.contexts, context)
	l.m.mu.Unlock()
}

// runChild is one run of a child on the adapter, into run.
func (c *coordinator) runChild(ctx context.Context, p *planned, profile turn.TaskProfile, task *contract.DelegatedTask, objective, prompt string, limit int, offered bool, m *meter, run *childRun) error {
	var mu sync.Mutex
	emit := func(event contract.RunEvent) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case event.Message != nil:
			if run.text != "" {
				run.text += "\n\n"
			}
			run.text += event.Message.Text
			run.progress = ""
		case event.MessageProgress != nil:
			run.progress = event.MessageProgress.Text
			run.streamed = event.MessageProgress.Text
			id := task.ID
			c.deliver(contract.RunEvent{MessageProgress: &contract.RunEventMessageProgress{Type: "message_progress", Text: event.MessageProgress.Text, TaskID: &id}})
		case event.Error != nil:
			run.failed = event.Error.Message
			if event.Error.Failure != nil {
				if held, ok := failure.Read(*event.Error.Failure); ok {
					task.Failure = &held
				}
			}
		case event.BudgetPaused != nil:
			run.paused = true
		case event.Proposal != nil:
			run.questions = append(run.questions, c.proposalQuestion(p, task, event.Proposal))
		case event.Guardrail != nil && event.Guardrail.Tool == "proposal":
			run.questions = append(run.questions, openQuestion("proposal", "The child wrote a proposal, which this conversation does not take; it was not kept.", ""))
		case event.UserQuestion != nil:
			run.questions = append(run.questions, openQuestion("question", questionText(event.UserQuestion.Question), ""))
		}
	}
	calls, tools, err := c.run.Children(ctx, profile, emit)
	if err != nil {
		return err
	}
	binding, id, policy, err := c.childBinding(profile)
	if err != nil {
		return err
	}
	name := binding.ModelName
	if binding.Agent != nil {
		name = binding.Agent.Model
	}
	c.childMu.Lock()
	c.models[task.ID] = name
	c.childMu.Unlock()
	var hosts []runcontext.HostTool
	if offered {
		hosts = c.childHostTools(profile)
	} else {
		tools = nil
	}
	input := &runcontext.Input{Objective: objective, Chat: c.chat.ChildScope(p.visible), Link: childLink{limit: limit, calls: c.toolCalls, task: task, budget: p.budget, m: m, sources: c.parentSources}}
	if parent := c.run.Context; parent != nil {
		input.Configuration, input.Phase, input.ArtifactProposals, input.Memory, input.Runtime, input.Authorize, input.Connection =
			parent.Configuration, parent.Phase, parent.ArtifactProposals, parent.Memory, parent.Runtime, parent.Authorize, parent.Connection
	}
	no := false
	child := *c.run
	child.ModelName = name
	if c.run.Agent != nil {
		child.Model = nil
		child.Agent = c.childAgent(task, name, m, binding.Agent)
	} else {
		child.Model = c.childModel(task, p.budget, m, binding.Model)
	}
	// The child is admitted as its resolved role, not the parent's author. Its own
	// binding keeps the coordinator's accounting wrappers through adapter admission.
	child.Wire = binding.Wire
	child.Capabilities = binding.Capabilities
	if policy != nil {
		child.Policy, child.PolicyProject = policy, nil
		child.Thinking = policy["models"].(map[string]any)["author"].(map[string]any)["reasoning"].(string)
		child.TaskClass, child.Critic, child.CriticProject, child.CriticReasoning = "", "off", "", ""
		input.Connection = id
		child.PolicyConnections = nil
		var requests *int
		if concurrency, ok := policy["concurrency"].(map[string]any); ok {
			capacity := int(concurrency["requests"].(float64))
			requests = &capacity
		}
		child.Models = func() (runcontext.RunModels, error) {
			return runcontext.RunModels{Critic: name, Child: name, ConcurrentRequests: requests}, nil
		}
	}
	child.Gate = calls
	child.Tools = tools
	child.HostTools = c.wrapHostTools(hosts, task, &p.budget)
	child.Prompt = prompt
	child.TestPrompt = ""
	child.Purpose = ""
	child.AllowConversation = true
	child.Interactive = true
	child.Review = &no
	child.Cases = nil
	// ADR-0009: a child is handed no skills; it inherits none of the parent's loaded set.
	child.Skills = nil
	child.Context = input
	child.Children = nil
	child.BeforeRuntimeCall = func(context.Context, string) error { return c.claimTool(task, &p.budget) }
	child.Emit = emit
	return c.inner.Run(ctx, &child)
}

// childHostTools are the parent's host tools a child of profile is offered (childOffer): those with
// no interaction whose delegation member is a capability the profile lists.
func (c *coordinator) childHostTools(profile turn.TaskProfile) []runcontext.HostTool {
	var out []runcontext.HostTool
	for _, tool := range c.run.HostTools {
		if tool.Interaction != "" || tool.Delegation == "" {
			continue
		}
		for _, capability := range profile.Capabilities {
			if capability == tool.Delegation {
				out = append(out, tool)
				break
			}
		}
	}
	return out
}

// proposalQuestion keeps a document a child proposed in the chat's ledger for the parent to read
// (agent#16): never applied or shown for review.
func (c *coordinator) proposalQuestion(p *planned, task *contract.DelegatedTask, proposal *contract.RunEventProposal) *runcontext.Value {
	document, _ := runcontext.ParseJSON(mustJSON(proposal.Document))
	unknowns := runcontext.Array()
	for _, unknown := range proposal.Unknowns {
		unknowns.Append(runcontext.String(unknown))
	}
	kept, err := c.ledger.Retain(runcontext.Document{ID: "task-proposal-" + task.ID, Kind: "tool", Label: "Document proposed by delegated task “" + task.Title + "”",
		Value: runcontext.ObjectOf("document", document, "unknowns", unknowns)}, false)
	if err != nil || kept == nil {
		return openQuestion("proposal", "The child proposed a document. It was not applied or shown for review, and the retained-context bound left no room to keep it.", "")
	}
	c.chat.Reveal(p.visible, kept.ID)
	return openQuestion("proposal", "The child proposed a document. It was not applied or shown for review: read it with read_window at ref, and propose it yourself if it should be.", kept.ID)
}
