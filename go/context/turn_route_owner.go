package runcontext

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
	"github.com/Judgment-Pack/judgment-pack-agent/go/internal/historycapture"
	"github.com/Judgment-Pack/judgment-pack-agent/go/internal/providerrelease"
	"slices"
	"sort"
	"sync"
	"time"
)

type routeCapabilityNeeded struct{}

func (*routeCapabilityNeeded) Error() string { return "TurnRouteCapabilityNeeded" }
func (c *SessionContext) AssertRouteDispatch() error {
	if c.RoutePending() {
		return &routeCapabilityNeeded{}
	}
	return nil
}
func IsRouteCapabilityNeeded(err error) bool {
	var needed *routeCapabilityNeeded
	return errors.As(err, &needed)
}

type turnRouteOwner struct {
	mu                   sync.RWMutex
	c                    *SessionContext
	session              Session
	policy               RoutingPolicy
	catalogue            RouteCatalogue
	binding              any
	facts                RouteFacts
	route                *TurnRoute
	pending              string
	classifying          bool
	freshClassifier      bool
	activeNative         *nativeRouteAttempt
	nativeClaim          *nativeRouteClaim
	nativeReleaseBlocked bool
}

func (c *SessionContext) initializeTurnRoute(ctx context.Context, session Session) error {
	p, err := RoutingPolicyFromMembers(c.ResolvedPolicy)
	if err != nil {
		return err
	}
	if p == nil || session.ChildDefinition != nil || session.Context != nil && session.Context.Objective != "" {
		if c.Execution.State().TurnRoute != nil {
			return ErrTurnRouteIdentityChanged
		}
		return nil
	}
	if c.Persistence("author") == nil {
		return failure.Authored("configuration", "DurableStoreRequired: routing requires the original acknowledged writer.", "storage")
	}
	owner := &turnRouteOwner{c: c, session: session, policy: *p}
	native := session.Agent != nil
	author := c.RequestValues().Reasoning("author")
	allowed := []string{"off", "low", "on", "medium", "ultra", "high"}
	if native {
		allowed = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}
	}
	if xs, ok := c.ResolvedPolicy["models.author.discovered.reasoning.supported"].Value.([]any); ok {
		allowed = []string{}
		for _, x := range xs {
			if s, ok := x.(string); ok {
				allowed = append(allowed, s)
			}
		}
	}
	for _, tier := range p.Tiers {
		if !slices.Contains(allowed, tier) {
			return failure.Authored("configuration", "TurnRouteTierUnsupported: routing tier is outside admitted role allowance.", "context")
		}
	}
	cat := RouteCatalogue{Tools: []RouteTool{}, Skills: []string{}, Tiers: append([]string{}, p.Tiers...), Native: native}
	if author != "" {
		cat.Tier = &author
		if !slices.Contains(cat.Tiers, author) {
			cat.Tiers = append(cat.Tiers, author)
		}
	}
	offer := []any{}
	for _, tool := range session.Tools {
		if tool.Meta == nil {
			return ErrTurnRouteInvalid
		}
		interaction := ""
		if tool.Offer != nil {
			_ = json.Unmarshal(tool.Offer.AdditionalMembers["interaction"], &interaction)
		}
		cat.Tools = append(cat.Tools, RouteTool{Name: tool.Name, Operation: tool.Meta.Operation, Effect: tool.Meta.Effect, Interaction: interaction})
		offer = append(offer, tool.Offer)
	}
	for _, tool := range session.HostTools {
		if tool.Taxonomy == nil {
			return ErrTurnRouteInvalid
		}
		cat.Tools = append(cat.Tools, RouteTool{Name: tool.Name, Operation: tool.Taxonomy.Operation, Effect: tool.Taxonomy.Effect, Interaction: tool.Interaction})
		offer = append(offer, tool.Declaration())
		if tool.Name == "update_plan" {
			cat.Planning = true
		}
	}
	if c.Skills != nil {
		for _, skill := range c.Skills.byName {
			cat.Skills = append(cat.Skills, skill.ID)
		}
		sort.Strings(cat.Skills)
	}
	owner.catalogue = cat
	state := c.Execution.State()
	phase := "work"
	if session.Context != nil && session.Context.Phase != "" {
		phase = session.Context.Phase
	}
	owner.binding = map[string]any{"execution": state.ID, "request": state.RequestKey, "scope": state.Context.Scope, "phase": phase, "policy": p, "override": session.Route, "offer": RouteDigest(offer), "provider": c.Provider("author"), "members": c.ResolvedPolicy}
	facts := RouteFacts{Mode: phase}
	if session.Context != nil {
		facts.Attachments = len(session.Context.Documents) > 0
		facts.Artifact = session.Context.CurrentState != ""
	}
	text := session.Prompt
	if session.Context != nil && session.Context.CurrentRequest != nil {
		text = session.Context.CurrentRequest.Message
	}
	facts.TurnBytes = len([]byte(text))
	if len(text) > 1 && text[0] == '/' {
		for i := 1; i <= len(text); i++ {
			if i == len(text) || text[i] == ' ' || text[i] == '\n' || text[i] == '\t' {
				if routeName(text[1:i]) {
					facts.Slash = text[1:i]
				}
				break
			}
		}
	}
	if session.RouteFacts != nil {
		facts.Mode = session.RouteFacts.Mode
		facts.Attachments = session.RouteFacts.Attachments
		facts.Artifact = session.RouteFacts.Artifact
	}
	owner.facts = facts
	c.turnRoute = owner
	if prior := state.TurnRoute; prior != nil {
		restored, e := RestoreTurnRoute(prior, cat, owner.binding)
		if e != nil {
			return e
		}
		if native && restored.Widened {
			if e = c.Persistence("author").Writer.restoreNativeRoute(ctx, *c.Persistence("author"), restored); e != nil {
				return e
			}
		}
		owner.route = &restored
		if restored.Reason == "classifier-pending" {
			if state.Durable != nil {
				for _, d := range state.Durable.Get("dispatches").Items() {
					if d.Get("scope").Get("role").Str() == "classifier" && oneOf(d.Get("state"), "unknown", "claimed") {
						return failure.Authored("storage", "TurnRouteClassifierUnknown: retained outcome cannot authorize replay or default dispatch.", "storage")
					}
				}
			}
			restored.Reason = "classifier-interrupted"
			if restored.Classifier != nil {
				restored.Classifier.Status = "refused"
			}
			return owner.publish(ctx, restored, "route_decided")
		}
		return owner.preloadSkills()
	}
	route, e := DecideTurnRoute(*p, cat, facts, owner.binding, session.Route, nil)
	if e != nil {
		return e
	}
	needsClassifier := p.Mode == "auto" && p.Classifier != nil && session.Route == nil
	for _, r := range p.Rules {
		if MatchesRouteRule(r, facts) {
			needsClassifier = false
			break
		}
	}
	if needsClassifier {
		identity := RouteDigest(map[string]any{"provider": c.Provider("author"), "revision": c.connectionRevisions[c.Provider("author").Connection]})
		route.Classifier = &RouteClassifierRecord{Binding: identity, PromptDigest: RouteDigest("not-dispatched"), Status: "unsupported"}
		if !native && c.Execution.Remaining() > 1 {
			route.Reason = "classifier-pending"
			route.Classifier.Status = "pending"
			owner.freshClassifier = true
			return owner.publish(ctx, route, "")
		}
		route, e = DecideTurnRoute(*p, cat, facts, owner.binding, nil, route.Classifier)
		if e != nil {
			return e
		}
	}
	return owner.publish(ctx, route, "route_decided")
}
func (o *turnRouteOwner) publish(ctx context.Context, route TurnRoute, kind string) error {
	if !ValidTurnRoute(route) {
		return ErrTurnRouteInvalid
	}
	raw, _ := json.Marshal(route)
	var typed contract.TurnRoute
	if err := json.Unmarshal(raw, &typed); err != nil {
		return err
	}
	if err := o.c.Execution.MutateCheckpoint(CheckpointPatch{"TurnRoute": &typed, "Version": 3}); err != nil {
		return err
	}
	state, err := o.c.Snapshot(o.c.Execution.State().Status)
	if err != nil {
		return err
	}
	binding := o.c.Persistence("author")
	if binding == nil {
		return ErrTurnRouteIdentityChanged
	}
	if kind == "" {
		err = binding.Writer.Save(ctx, *binding, state)
	} else {
		var event contract.RunEvent
		if kind == "route_decided" {
			event.RouteDecided = &contract.RunEventRouteDecided{Type: kind, Route: typed}
		} else {
			event.RouteWidened = &contract.RunEventRouteWidened{Type: kind, Route: typed}
		}
		var output contract.RunEvent
		var proof *nativeRouteClaim
		if kind == "route_widened" && o.catalogue.Native {
			o.mu.RLock()
			proof = o.nativeClaim
			o.mu.RUnlock()
			if proof == nil {
				return &ProviderReleaseUnknown{}
			}
		}
		output, err = binding.Writer.CommitRoute(ctx, *binding, state, event, proof)
		if err == nil {
			if output.RouteWidened != nil {
				raw, _ := json.Marshal(output.RouteWidened.Route)
				if json.Unmarshal(raw, &route) != nil {
					return ErrTurnRouteIdentityChanged
				}
				if err = o.c.Execution.MutateCheckpoint(CheckpointPatch{"TurnRoute": &output.RouteWidened.Route}); err != nil {
					return err
				}
			}
			o.c.notices = append(o.c.notices, output)
		}
	}
	if err != nil {
		return err
	}
	o.mu.Lock()
	copy := usageCopy(route)
	o.route = &copy
	o.mu.Unlock()
	if kind == "route_decided" {
		return o.preloadSkills()
	}
	return nil
}
func (c *SessionContext) RouteTier(role string) string {
	if c.turnRoute == nil || role != "author" {
		return ""
	}
	o := c.turnRoute
	o.mu.RLock()
	defer o.mu.RUnlock()
	if o.classifying {
		return o.policy.Tiers[0]
	}
	if o.route != nil && o.route.Tier != nil {
		return *o.route.Tier
	}
	return ""
}
func (c *SessionContext) RouteOffers(name string) bool {
	if c.turnRoute == nil || c.CriticReading || c.Reformatting {
		return true
	}
	o := c.turnRoute
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.route == nil || o.classifying || RouteOffers(*o.route, o.catalogue, name)
}
func (c *SessionContext) RequireRouteCapability(name string, args json.RawMessage) error {
	if c.turnRoute == nil || c.CriticReading || c.Reformatting || c.Execution.ApprovalAsked() {
		return nil
	}
	o := c.turnRoute
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.pending != "" {
		return &routeCapabilityNeeded{}
	}
	if o.route == nil || o.classifying {
		return nil
	}
	capability := name
	knownSkill := false
	if name == "load_skill" && c.Skills != nil {
		var a struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(args, &a) == nil {
			if skill, ok := c.Skills.byName[a.Name]; ok {
				capability = skill.ID
				knownSkill = true
				if skill.Name == "asking-well" && skill.Kind == "method" && skill.Profile.Effect == "pure" && slices.Equal(skill.Profile.Operations, []string{"ask"}) && slices.ContainsFunc(o.catalogue.Tools, func(t RouteTool) bool {
					return t.Name == "ask_user" && t.Operation == "ask" && t.Effect == "pure" && t.Interaction == "question"
				}) {
					return nil
				}
			}
		}
	}
	withheldSkill := knownSkill && o.route.DecidedBy != "default" && !o.route.Widened && !slices.Contains(o.route.Skills, capability)
	if withheldSkill || !knownSkill && !RouteOffers(*o.route, o.catalogue, name) {
		if o.route.Widened || !knownSkill && !slices.ContainsFunc(o.catalogue.Tools, func(t RouteTool) bool { return t.Name == name }) {
			return nil
		}
		o.pending = capability
		return &routeCapabilityNeeded{}
	}
	return nil
}
func (c *SessionContext) RoutePending() bool {
	if c.turnRoute == nil {
		return false
	}
	o := c.turnRoute
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.pending != ""
}
func (c *SessionContext) WidenTurnRoute(ctx context.Context) error {
	if c.turnRoute == nil {
		return ErrTurnRouteWidenRefused
	}
	o := c.turnRoute
	o.mu.RLock()
	if o.route == nil || o.pending == "" || o.nativeReleaseBlocked || o.catalogue.Native && o.nativeClaim == nil {
		o.mu.RUnlock()
		return ErrTurnRouteWidenRefused
	}
	r, e := WidenedTurnRoute(*o.route, o.catalogue, o.pending)
	o.mu.RUnlock()
	if e != nil {
		return e
	}
	if err := o.publish(ctx, r, "route_widened"); err != nil {
		return err
	}
	o.mu.Lock()
	o.pending = ""
	o.mu.Unlock()
	return nil
}
func (c *SessionContext) RouteClassifying() bool {
	if c.turnRoute == nil {
		return false
	}
	o := c.turnRoute
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.classifying
}
func (c *SessionContext) RouteClassifierCeiling() int {
	if c.turnRoute == nil {
		return 0
	}
	o := c.turnRoute
	o.mu.RLock()
	defer o.mu.RUnlock()
	if o.classifying && o.policy.Classifier != nil {
		return o.policy.Classifier.MaxOutputTokens
	}
	return 0
}

// ResolveTurnRouteClassifier consumes only this fresh context's once-only classifier stage.
func (c *SessionContext) ResolveTurnRouteClassifier(ctx context.Context, execute func(context.Context, string, RouteClassifier) (RouteClassifierRecord, error)) error {
	if c.turnRoute == nil {
		return nil
	}
	o := c.turnRoute
	o.mu.Lock()
	if !o.freshClassifier {
		o.mu.Unlock()
		return nil
	}
	o.freshClassifier = false
	o.classifying = true
	config := usageCopy(*o.policy.Classifier)
	tier := o.policy.Tiers[0]
	o.mu.Unlock()
	bounded, cancel := context.WithTimeout(ctx, time.Duration(config.TimeoutMilliseconds)*time.Millisecond)
	defer cancel()
	record, err := execute(bounded, tier, config)
	o.mu.Lock()
	o.classifying = false
	o.mu.Unlock()
	if err != nil {
		return err
	}
	route, err := DecideTurnRoute(o.policy, o.catalogue, o.facts, o.binding, nil, &record)
	if err != nil {
		return err
	}
	return o.publish(ctx, route, "route_decided")
}

// All native attempt state is per invocation. No caller marker can construct a slot or claim.
type nativeRouteAttemptKey struct{}
type nativeRouteAttempt struct {
	mu            sync.Mutex
	owner         *turnRouteOwner
	writer        *RunWriter
	scope         DurableScope
	route         string
	open, entered bool
	claim         *nativeRouteClaim
}
type nativeRouteClaim struct {
	scope                                                   DurableScope
	id, requestDigest, request, source, tool, route, needed string
	live, released, consumed                                bool
}
type nativeRouteDisposition struct{ id, requestDigest, route string }

func (w *RunWriter) supersededNativeLocked(binding *RunBinding, row DurableDispatch) (bool, error) {
	if row.Kind != "agent" || row.Scope != binding.Scope || w.latest == nil || w.latest.TurnRoute == nil || !w.latest.TurnRoute.Widened {
		return false, nil
	}
	marker := w.latest.TurnRoute.WidenedInvocation
	proof, ok := w.nativeSuperseded[usageCanonical(binding.Scope)]
	if marker == nil || !ok || proof.route != usageCanonical(w.latest.TurnRoute) {
		return false, &ContinuationIdentityChanged{}
	}
	if row.ID != marker.DispatchID {
		return false, &HostOutcomeUnknown{}
	}
	if proof.id != row.ID || proof.requestDigest != row.RequestDigest || marker.RequestDigest != row.RequestDigest || durableDigest([]byte(row.Request)) != row.RequestDigest || row.State != "unknown" || !row.Read {
		return false, &ContinuationIdentityChanged{}
	}
	return true, nil
}

// InvokeNativeRoute uses the original writer and a private slot. The host receives only release capability.
func (c *SessionContext) InvokeNativeRoute(ctx context.Context, role, source, tool string, request []byte, run func(context.Context, []byte, *providerrelease.Capability) error, proofs ...*historycapture.NativeBindings) error {
	binding := c.Persistence(role)
	if binding != nil && binding.Writer.capture != nil {
		var aliases map[string]string
		if len(proofs) > 0 {
			var err error
			aliases, err = proofs[0].ForRequest(request)
			if err != nil {
				return err
			}
		}
		ctx = context.WithValue(ctx, nativeCaptureKey{}, &nativeCaptureSlot{writer: binding.Writer, scope: binding.Scope, bindings: aliases})
	}
	invoke := func(given context.Context, execute func([]byte) error) error {
		_, err := InvokeDurable(given, binding, "agent", source, tool, request, true, func(raw []byte) (struct{}, error) { return struct{}{}, execute(raw) }, func(struct{}) bool { return false }, nil)
		return err
	}
	o := c.turnRoute
	if o == nil || role != "author" || c.CriticReading || c.Reformatting {
		return invoke(ctx, func(raw []byte) error { return run(ctx, raw, nil) })
	}
	o.mu.Lock()
	if o.route == nil || o.route.Widened {
		o.mu.Unlock()
		return invoke(ctx, func(raw []byte) error { return run(ctx, raw, nil) })
	}
	if binding == nil || o.activeNative != nil || o.nativeReleaseBlocked {
		o.mu.Unlock()
		return &ContinuationIdentityChanged{}
	}
	attempt := &nativeRouteAttempt{owner: o, writer: binding.Writer, scope: binding.Scope, route: o.route.BindingDigest, open: true}
	o.activeNative = attempt
	o.mu.Unlock()
	defer func() {
		attempt.mu.Lock()
		attempt.open = false
		attempt.mu.Unlock()
		o.mu.Lock()
		if o.activeNative == attempt {
			o.activeNative = nil
		}
		o.mu.Unlock()
	}()
	scoped := context.WithValue(ctx, nativeRouteAttemptKey{}, attempt)
	release, finish := providerrelease.NewReleaseAttempt()
	finished := false
	defer func() {
		if !finished {
			finish()
		}
	}()
	return invoke(scoped, func(raw []byte) error {
		problem := run(scoped, raw, release)
		acknowledged := finish()
		finished = true
		o.mu.RLock()
		pending := o.pending
		o.mu.RUnlock()
		if pending != "" {
			if !acknowledged || ctx.Err() != nil || problem != nil && !IsRouteCapabilityNeeded(problem) {
				o.mu.Lock()
				o.nativeReleaseBlocked = true
				o.mu.Unlock()
				if problem != nil {
					return problem
				}
				return &ProviderReleaseUnknown{}
			}
			attempt.mu.Lock()
			claim := attempt.claim
			valid := attempt.open
			attempt.mu.Unlock()
			w := binding.Writer
			w.mu.Lock()
			if !valid || claim == nil || !w.nativeClaims[claim] || !claim.live || claim.released || claim.consumed || claim.scope != binding.Scope || claim.route != attempt.route || claim.request != string(raw) || durableDigest(raw) != claim.requestDigest {
				w.mu.Unlock()
				return &ContinuationIdentityChanged{}
			}
			claim.released = true
			claim.needed = pending
			w.mu.Unlock()
			o.mu.Lock()
			o.nativeClaim = claim
			o.mu.Unlock()
			return &routeCapabilityNeeded{}
		}
		return problem
	})
}

// NativeRouteCapability is callable only through this invocation's source-owned callback closure.
func (c *SessionContext) NativeRouteCapability(ctx context.Context, kind, name string) error {
	attempt, _ := ctx.Value(nativeRouteAttemptKey{}).(*nativeRouteAttempt)
	if attempt == nil || c.turnRoute == nil {
		return &ContinuationIdentityChanged{}
	}
	binding := c.Persistence("author")
	if binding == nil {
		return &ContinuationIdentityChanged{}
	}
	attempt.mu.Lock()
	valid := attempt.open && attempt.claim != nil && attempt.owner == c.turnRoute && attempt.writer == binding.Writer
	attempt.mu.Unlock()
	if !valid {
		return &ContinuationIdentityChanged{}
	}
	o := c.turnRoute
	o.mu.RLock()
	known := kind == "skill" && slices.Contains(o.catalogue.Skills, name) || (kind == "tool" || kind == "planning" && name == "update_plan") && slices.ContainsFunc(o.catalogue.Tools, func(t RouteTool) bool { return t.Name == name })
	o.mu.RUnlock()
	if !known {
		return &ContinuationIdentityChanged{}
	}
	if kind == "skill" {
		o.mu.Lock()
		if o.route != nil && !o.route.Widened && !slices.Contains(o.route.Skills, name) {
			o.pending = name
			o.mu.Unlock()
			return &routeCapabilityNeeded{}
		}
		o.mu.Unlock()
	} else if err := c.RequireRouteCapability(name, nil); err != nil {
		return err
	}
	return &ContinuationIdentityChanged{}
}

func (o *turnRouteOwner) preloadSkills() error {
	if o.c.Skills == nil || o.route == nil {
		return nil
	}
	for _, id := range o.route.Skills {
		for name, skill := range o.c.Skills.byName {
			if skill.ID == id {
				if _, err := o.c.Skills.load(name); err != nil {
					return err
				}
				break
			}
		}
	}
	return nil
}

// Restore only the immutable execution transaction. Metadata is intentionally outside this proof.
func (w *RunWriter) restoreNativeRoute(ctx context.Context, binding RunBinding, route TurnRoute) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	invalid := func() error { return &ContinuationIdentityChanged{} }
	store, ok := w.Store.(HistoryRunStore)
	marker := route.WidenedInvocation
	if binding.Writer != w || !ok || store.HistoryVersion() != 1 || !route.Widened || marker == nil || w.latest == nil || usageCanonical(w.latest.TurnRoute) != usageCanonical(route) {
		return invalid()
	}
	var old *DurableDispatch
	for i := range w.book.Dispatches {
		if w.book.Dispatches[i].ID == marker.DispatchID {
			old = &w.book.Dispatches[i]
			break
		}
	}
	if old == nil || old.Kind != "agent" || !old.Read || old.State != "unknown" || old.Scope != binding.Scope || old.RequestDigest != marker.RequestDigest || durableDigest([]byte(old.Request)) != marker.RequestDigest {
		return invalid()
	}
	readHead := func() (*RunHistoryHead, error) {
		head, e := store.ReadHistoryHead(ctx, w.ID)
		if e != nil {
			return nil, e
		}
		if head == nil {
			return nil, invalid()
		}
		raw, _ := historyJSONBytes(head)
		checked, e := ReadRunHistoryHead(raw)
		return &checked, e
	}
	head, err := readHead()
	if err != nil {
		return err
	}
	expected := HistoryObjectRef{Digest: HistoryBytesDigest(w.acknowledgedCheckpoint), Bytes: len(w.acknowledgedCheckpoint)}
	if head.RunID != w.ID || head.ExecutionRevision != w.revision || head.Checkpoint == nil || *head.Checkpoint != expected {
		return invalid()
	}
	total, sequence, eventCount, claimed := 0, 0, 0, 0
	previous := ""
	widened := false
	object := func(ref HistoryObjectRef) ([]byte, error) {
		total += ref.Bytes
		if !validHistoryRef(ref) || total > HistoryTotalBytes {
			return nil, invalid()
		}
		page, e := store.ReadHistoryObject(ctx, HistoryResource{Kind: "run", ID: w.ID}, head.Revision, ref.Digest, 0, ref.Bytes)
		if e != nil {
			return nil, e
		}
		if page.Digest != ref.Digest || page.Offset != 0 || page.Total != ref.Bytes || len(page.Value) != ref.Bytes || page.NextOffset != ref.Bytes || page.HasMore || HistoryBytesDigest(page.Value) != ref.Digest {
			return nil, invalid()
		}
		return page.Value, nil
	}
	checkpoint := func(ref HistoryObjectRef) (*contract.Checkpoint, error) {
		raw, e := object(ref)
		if e != nil {
			return nil, e
		}
		v, e := ParseJSON(raw)
		if e != nil {
			return nil, e
		}
		return ReadExecution(v)
	}
	matches := func(state *contract.Checkpoint, status string) bool {
		if state == nil || state.ID != w.ID || state.Durable == nil {
			return false
		}
		var book DurableBook
		raw, _ := json.Marshal(state.Durable)
		if json.Unmarshal(raw, &book) != nil {
			return false
		}
		for _, row := range book.Dispatches {
			if row.ID == old.ID {
				return row.State == status && row.Kind == "agent" && row.Read && row.Scope == old.Scope && row.Request == old.Request && row.RequestDigest == old.RequestDigest && row.Source == old.Source && row.Tool == old.Tool
			}
		}
		return false
	}
	for sequence < head.CommitCount {
		if sequence >= HistoryMaxRows {
			return invalid()
		}
		page, e := store.ReadHistoryPage(ctx, w.ID, head.Revision, sequence, 100)
		if e != nil {
			return e
		}
		if page.Revision != head.Revision || page.AfterSequence != sequence || len(page.Items) == 0 || len(page.Items) > 100 || page.Total != head.CommitCount || page.NextSequence != sequence+len(page.Items) || page.HasMore != (page.NextSequence < head.CommitCount) {
			return invalid()
		}
		for _, input := range page.Items {
			raw, _ := historyJSONBytes(input)
			row, e := ReadRunHistoryEntry(raw)
			if e != nil {
				return e
			}
			sequence++
			prior := ""
			if row.PreviousDigest != nil {
				prior = *row.PreviousDigest
			}
			if row.Sequence != sequence || prior != previous || row.EventStart != eventCount+1 {
				return invalid()
			}
			eventCount += row.EventCount
			if eventCount > HistoryMaxEvents {
				return invalid()
			}
			previous = row.Digest
			var state *contract.Checkpoint
			if claimed == 0 && row.Checkpoint != nil {
				state, e = checkpoint(*row.Checkpoint)
				if e != nil {
					return e
				}
				if matches(state, "claimed") && state.TurnRoute != nil && state.TurnRoute.BindingDigest == route.BindingDigest && !state.TurnRoute.Widened {
					claimed = sequence
				}
			}
			if !widened {
				for _, ref := range row.Events {
					raw, e := object(ref)
					if e != nil {
						return e
					}
					event, e := ReadStoredRunEvent(raw)
					if e != nil {
						return e
					}
					if event.Scope == binding.Scope && event.Event.RouteWidened != nil && usageCanonical(event.Event.RouteWidened.Route) == usageCanonical(route) {
						v, e := ParseJSON(raw)
						if e != nil {
							return e
						}
						if contract.CheckRecord("event", v.Get("event").JSON()) != nil {
							return invalid()
						}
						if claimed == 0 || claimed >= sequence || row.Checkpoint == nil {
							return invalid()
						}
						if state == nil {
							state, e = checkpoint(*row.Checkpoint)
							if e != nil {
								return e
							}
						}
						if !matches(state, "unknown") || usageCanonical(state.TurnRoute) != usageCanonical(route) {
							return invalid()
						}
						widened = true
					}
				}
			}
		}
	}
	again, err := readHead()
	if err != nil {
		return err
	}
	executionHead := func(h *RunHistoryHead) string {
		return usageCanonical([]any{h.RunID, h.ExecutionRevision, h.CommitCount, h.EventCount, h.Root, h.Checkpoint})
	}
	if !widened || previous != head.Root || eventCount != head.EventCount || executionHead(again) != executionHead(head) {
		return invalid()
	}
	if w.nativeSuperseded == nil {
		w.nativeSuperseded = map[string]nativeRouteDisposition{}
	}
	w.nativeSuperseded[usageCanonical(binding.Scope)] = nativeRouteDisposition{id: old.ID, requestDigest: old.RequestDigest, route: usageCanonical(route)}
	return nil
}
