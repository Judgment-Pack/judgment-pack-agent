package runcontext

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Judgment-Pack/judgment-pack-agent/go/internal/usagewitness"
	"strings"
)

// UsageBoundRequest is the final encoded body after every tier/fallback rewrite.
// The host bounder returns conservative bounds, never predicted/provider-reported usage.
type UsageBoundRequest struct {
	Kind                 string
	Family               string
	Connection           string
	ConnectionRevision   string
	Model                string
	Request              []byte
	EncodedOutputMaximum *int64
}
type UsageBudgetInput struct {
	Store   UsageBudgetStore
	Scope   UsageBudgetScope
	Bounder func(context.Context, UsageBoundRequest) (UsageAmounts, error)
}
type UsageJournalEntry struct {
	Reservation UsageReservation    `json:"reservation"`
	Receipt     *UsageBudgetReceipt `json:"receipt,omitempty"`
	Settlement  *UsageBudgetReceipt `json:"settlement,omitempty"`
	State       string              `json:"state"`
	Observed    UsageAmounts        `json:"observed,omitempty"`
}
type UsageBudgetRefusal struct {
	ID            string `json:"id"`
	Execution     string `json:"execution"`
	Role          string `json:"role"`
	Task          string `json:"task,omitempty"`
	RequestDigest string `json:"requestDigest"`
	Name          string `json:"name"`
	Dimension     string `json:"dimension,omitempty"`
}
type UsageBudgetJournal struct {
	Version  int                  `json:"version"`
	Scope    UsageBudgetScope     `json:"scope"`
	Entries  []UsageJournalEntry  `json:"entries"`
	Refusals []UsageBudgetRefusal `json:"refusals"`
}
type UsageTotal struct {
	Known    int64 `json:"known"`
	Complete bool  `json:"complete"`
}
type UsageTotals map[string]UsageTotal
type usageDispatchFacts struct {
	DurableScope
	Source     string
	Tool       string
	Descriptor string
	UsageBoundRequest
}
type usageTicket struct {
	nativeWitness *usagewitness.Witness
	owner         *usageJournalOwner
	id            string
	key           string
	family        string
}
type usageJournalOwner struct {
	input   UsageBudgetInput
	root    string
	journal UsageBudgetJournal
	tickets map[*usageTicket]bool
}

func usageReceiptValid(value UsageBudgetReceipt, request UsageReservation) bool {
	if value.Resource != request.Scope.Resource || value.Scope != request.Scope.ID || value.Reservation != request.ID || value.ReservationDigest != durableDigest([]byte(usageCanonical(request))) || !usageID(value.Revision) || !usageAmounts(value.Observed, false) {
		return false
	}
	for key, n := range value.Observed {
		upper, ok := request.Upper[key]
		if !ok || n > upper {
			return false
		}
	}
	switch value.State {
	case "reserved", "never-admitted":
		return len(value.Observed) == 0
	case "partial", "settled":
		complete := true
		for key := range request.Upper {
			if _, ok := value.Observed[key]; !ok {
				complete = false
			}
		}
		return value.State == "settled" && complete || value.State == "partial" && !complete && len(value.Observed) > 0
	}
	return false
}
func validUsageJournal(journal UsageBudgetJournal) bool {
	if journal.Version != 1 || usageScope(journal.Scope, journal.Scope.Resource) != nil || journal.Entries == nil || len(journal.Entries) > 400 || journal.Refusals == nil || len(journal.Refusals) > 100 {
		return false
	}
	ids := map[string]bool{}
	for _, row := range journal.Entries {
		r := row.Reservation
		if usageRequest(r, journal.Scope.Resource) != nil || usageCanonical(r.Scope) != usageCanonical(journal.Scope) || ids[r.ID] {
			return false
		}
		ids[r.ID] = true
		if row.State != "reserved" && row.State != "claimed" && row.State != "reported" && row.State != "unknown" && row.State != "never-admitted" {
			return false
		}
		if row.Receipt != nil && (!usageReceiptValid(*row.Receipt, r) || row.Receipt.State != "reserved") || row.State != "unknown" && row.Receipt == nil || row.Settlement != nil && !usageReceiptValid(*row.Settlement, r) {
			return false
		}
		count := "modelRequests"
		if r.Kind == "agent" {
			count = "providerInvocations"
		}
		if row.State == "reported" && row.Observed == nil {
			return false
		}
		if row.Observed != nil && (row.Receipt == nil || row.State != "reported" && row.State != "unknown" || row.Observed[count] != 1) {
			return false
		}
		if r.Kind == "agent" {
			if _, present := row.Observed["modelRequests"]; present {
				return false
			}
		}
		if row.Settlement != nil && row.Settlement.State != "never-admitted" {
			for key, n := range row.Settlement.Observed {
				if value, present := row.Observed[key]; !present || value != n {
					return false
				}
			}
		}
		if row.Observed != nil {
			if !usageAmounts(row.Observed, false) {
				return false
			}
			for key, n := range row.Observed {
				if _, limited := r.Scope.Limits[key]; (limited || key == "modelRequests" || key == "providerInvocations") && n > r.Upper[key] {
					return false
				}
			}
		}
		if row.State == "never-admitted" && (row.Settlement == nil || row.Settlement.State != "never-admitted" || row.Observed != nil) {
			return false
		}
	}
	for _, row := range journal.Refusals {
		if !usageID(row.ID) || !usageID(row.Execution) || !usageID(row.Role) || row.Task != "" && !usageID(row.Task) || !recordDigestPattern.MatchString(row.RequestDigest) {
			return false
		}
		switch row.Name {
		case "UsageBudgetExceeded", "UsageBudgetUnknown", "UsageBudgetUnsupported", "UsageBudgetIdentityChanged", "UsageBudgetCapacity":
		default:
			return false
		}
		if row.Dimension != "" {
			known := false
			for _, key := range usageDimensions {
				known = known || key == row.Dimension
			}
			if !known {
				return false
			}
		}
	}
	return true
}
func readUsageJournal(value *Value) (*UsageBudgetJournal, error) {
	if value == nil {
		return nil, nil
	}
	var journal UsageBudgetJournal
	if json.Unmarshal(value.JSON(), &journal) != nil || !validUsageJournal(journal) {
		return nil, &UsageBudgetIdentityChanged{}
	}
	return &journal, nil
}
func newUsageJournalOwner(input *UsageBudgetInput, root string, retained *UsageBudgetJournal) (*usageJournalOwner, error) {
	if input == nil || input.Store == nil || usageScope(input.Scope, input.Store.Resource()) != nil {
		return nil, &UsageBudgetIdentityChanged{}
	}
	if retained != nil && (!validUsageJournal(*retained) || usageCanonical(retained.Scope) != usageCanonical(input.Scope)) {
		return nil, &UsageBudgetIdentityChanged{}
	}
	held := *input
	held.Scope = usageCopy(input.Scope)
	owner := &usageJournalOwner{input: held, root: root, tickets: map[*usageTicket]bool{}, journal: UsageBudgetJournal{Version: 1, Scope: usageCopy(held.Scope), Entries: []UsageJournalEntry{}, Refusals: []UsageBudgetRefusal{}}}
	if retained != nil {
		for _, row := range retained.Entries {
			if row.Reservation.Root != root {
				return nil, &UsageBudgetIdentityChanged{}
			}
		}
		owner.journal = usageCopy(*retained)
	}
	for i := range owner.journal.Entries {
		row := &owner.journal.Entries[i]
		if row.State == "reserved" || row.State == "claimed" {
			row.State = "unknown"
		}
	}
	return owner, nil
}
func usageFactsKey(f usageDispatchFacts) string {
	return usageCanonical(map[string]any{"execution": f.Execution, "role": f.Role, "task": f.Task, "source": f.Source, "tool": f.Tool, "descriptor": f.Descriptor, "kind": f.Kind})
}
func (o *usageJournalOwner) refusal(f usageDispatchFacts, cause error) error {
	if len(o.journal.Refusals) >= 100 {
		return &UsageBudgetCapacity{}
	}
	row := UsageBudgetRefusal{ID: newStorageIdentity(), Execution: f.Execution, Role: f.Role, Task: f.Task, RequestDigest: durableDigest([]byte(f.Descriptor)), Name: "UsageBudgetUnknown"}
	var exceeded *UsageBudgetExceeded
	var unsupported *UsageBudgetUnsupported
	var changed *UsageBudgetIdentityChanged
	var capacity *UsageBudgetCapacity
	switch {
	case errors.As(cause, &exceeded):
		row.Name = "UsageBudgetExceeded"
		row.Dimension = exceeded.Dimension
	case errors.As(cause, &unsupported):
		row.Name = "UsageBudgetUnsupported"
	case errors.As(cause, &changed):
		row.Name = "UsageBudgetIdentityChanged"
	case errors.As(cause, &capacity):
		row.Name = "UsageBudgetCapacity"
	}
	o.journal.Refusals = append(o.journal.Refusals, row)
	return nil
}
func (o *usageJournalOwner) reserve(ctx context.Context, id string, f usageDispatchFacts) (*usageTicket, error) {
	if !usageDescriptorMatches(f) {
		return nil, &UsageBudgetUnsupported{}
	}
	if !usageID(f.Connection) || !usageID(f.ConnectionRevision) || !usageID(f.Model) {
		return nil, &UsageBudgetUnsupported{}
	}
	for _, row := range o.journal.Entries {
		if row.State == "unknown" {
			return nil, &UsageBudgetUnknown{}
		}
	}
	if len(o.journal.Entries) >= 400 {
		return nil, &UsageBudgetCapacity{}
	}
	limits := o.input.Scope.Limits
	upper := UsageAmounts{"modelRequests": 0, "providerInvocations": 0}
	if f.Kind == "model" {
		upper["modelRequests"] = 1
	} else {
		upper["providerInvocations"] = 1
	}
	_, models := limits["modelRequests"]
	_, input := limits["inputTokens"]
	_, output := limits["outputTokens"]
	if f.Kind == "agent" && (models || input || output) {
		return nil, &UsageBudgetUnsupported{}
	}
	if input || output {
		if o.input.Bounder == nil || f.Family != "openai-compatible" && f.Family != "anthropic" && f.Family != "gemini" {
			return nil, &UsageBudgetUnsupported{}
		}
		var requiredMaximum int64
		bound := f.UsageBoundRequest
		bound.Request = append([]byte(nil), f.Request...)
		if output {
			maximum, err := encodedUsageOutputMaximum(f.Family, f.Request)
			if err != nil {
				return nil, err
			}
			if f.EncodedOutputMaximum != nil && *f.EncodedOutputMaximum != maximum {
				return nil, &UsageBudgetUnsupported{}
			}
			requiredMaximum = maximum
			bound.EncodedOutputMaximum = &maximum
		} else {
			bound.EncodedOutputMaximum = nil
		}
		bounds, err := o.input.Bounder(ctx, bound)
		if err != nil {
			return nil, err
		}
		bounds = usageCopy(bounds)
		if !usageAmounts(bounds, false) {
			return nil, &UsageBudgetUnsupported{}
		}
		for _, key := range []string{"inputTokens", "outputTokens"} {
			if _, limited := limits[key]; limited {
				n, known := bounds[key]
				if !known || key == "outputTokens" && n < requiredMaximum {
					return nil, &UsageBudgetUnsupported{}
				}
				upper[key] = n
			}
		}
	}
	r := UsageReservation{Scope: usageCopy(o.input.Scope), ID: id, Root: o.root, Execution: f.Execution, Role: f.Role, Task: f.Task, Kind: f.Kind, RequestDigest: durableDigest([]byte(f.Descriptor)), Connection: f.Connection, ConnectionRevision: f.ConnectionRevision, Model: f.Model, Upper: upper}
	if usageRequest(r, o.input.Scope.Resource) != nil {
		return nil, &UsageBudgetIdentityChanged{}
	}
	index := len(o.journal.Entries)
	o.journal.Entries = append(o.journal.Entries, UsageJournalEntry{Reservation: r, State: "unknown"})
	result, err := o.input.Store.Reserve(ctx, r)
	if err != nil {
		var exceeded *UsageBudgetExceeded
		var unsupported *UsageBudgetUnsupported
		var changed *UsageBudgetIdentityChanged
		var capacity *UsageBudgetCapacity
		if errors.As(err, &exceeded) || errors.As(err, &unsupported) || errors.As(err, &changed) || errors.As(err, &capacity) {
			o.journal.Entries = o.journal.Entries[:index]
		}
		return nil, err
	}
	if !result.Created || !usageReceiptValid(result.Receipt, r) || result.Receipt.State != "reserved" {
		return nil, &UsageBudgetUnknown{}
	}
	row := &o.journal.Entries[index]
	receipt := usageCopy(result.Receipt)
	row.Receipt = &receipt
	row.State = "reserved"
	ticket := &usageTicket{owner: o, id: id, key: usageFactsKey(f), family: f.Family}
	o.tickets[ticket] = true
	return ticket, nil
}
func (o *usageJournalOwner) consume(ticket *usageTicket, f usageDispatchFacts) (string, error) {
	if ticket == nil || ticket.owner != o || !o.tickets[ticket] || ticket.key != usageFactsKey(f) {
		return "", &UsageBudgetIdentityChanged{}
	}
	for i := range o.journal.Entries {
		row := &o.journal.Entries[i]
		if row.Reservation.ID == ticket.id {
			if row.State != "reserved" {
				break
			}
			delete(o.tickets, ticket)
			row.State = "claimed"
			return ticket.id, nil
		}
	}
	return "", &UsageBudgetIdentityChanged{}
}
func (o *usageJournalOwner) unknown(id string) {
	for i := range o.journal.Entries {
		row := &o.journal.Entries[i]
		if row.Reservation.ID == id && row.State == "claimed" {
			row.State = "unknown"
		}
	}
}
func (o *usageJournalOwner) observe(ctx context.Context, id string, observed UsageAmounts, evidence string) error {
	for i := range o.journal.Entries {
		row := &o.journal.Entries[i]
		if row.Reservation.ID != id {
			continue
		}
		if row.State != "claimed" || !usageAmounts(observed, false) || !recordDigestPattern.MatchString(evidence) {
			return &UsageBudgetIdentityChanged{}
		}
		for key, n := range observed {
			if _, limited := row.Reservation.Scope.Limits[key]; (limited || key == "modelRequests" || key == "providerInvocations") && n > row.Reservation.Upper[key] {
				return &UsageBudgetIdentityChanged{}
			}
		}
		count := "modelRequests"
		if row.Reservation.Kind == "agent" {
			count = "providerInvocations"
		}
		if observed[count] != 1 {
			return &UsageBudgetIdentityChanged{}
		}
		row.Observed = usageCopy(observed)
		row.State = "reported"
		limited := UsageAmounts{}
		for key, n := range observed {
			if _, held := row.Reservation.Upper[key]; held {
				limited[key] = n
			}
		}
		receipt, err := o.input.Store.Settle(ctx, UsageSettlement{Scope: o.input.Scope, ID: id, ReservationDigest: row.Receipt.ReservationDigest, CommitID: newStorageIdentity(), Evidence: evidence, Kind: "reported", Observed: limited})
		if err != nil {
			row.State = "unknown"
			return err
		}
		if !usageReceiptValid(receipt, row.Reservation) || receipt.State != "partial" && receipt.State != "settled" {
			row.State = "unknown"
			return &UsageBudgetUnknown{}
		}
		for key, n := range limited {
			if actual, known := receipt.Observed[key]; !known || actual != n {
				row.State = "unknown"
				return &UsageBudgetUnknown{}
			}
		}
		held := usageCopy(receipt)
		row.Settlement = &held
		return nil
	}
	return &UsageBudgetIdentityChanged{}
}

// UsageJournalTotals aggregates each root-owned physical attempt exactly once; childUsage is not re-added.
func UsageJournalTotals(journal UsageBudgetJournal) (UsageTotals, error) {
	if !validUsageJournal(journal) {
		return nil, &UsageBudgetIdentityChanged{}
	}
	totals := UsageTotals{}
	for _, key := range usageDimensions {
		totals[key] = UsageTotal{Known: 0, Complete: true}
	}
	for _, row := range journal.Entries {
		if row.State == "never-admitted" {
			continue
		}
		for _, key := range usageDimensions {
			total := totals[key]
			n, known := row.Observed[key]
			if !known {
				if !(key == "providerInvocations" && row.Reservation.Kind == "model") {
					total.Complete = false
				}
			} else {
				if n > usageMaxSafeInteger-total.Known {
					return nil, &UsageBudgetUnknown{}
				}
				total.Known += n
			}
			totals[key] = total
		}
	}
	return totals, nil
}
func usageExactCount(value any) (int64, bool) {
	token, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	n, valid := exactReportNumber(string(token))
	return int64(n), valid && n >= 0
}
func encodedUsageOutputMaximum(family string, request []byte) (int64, error) {
	value, err := descriptorJSON(request, false)
	body, ok := value.(map[string]any)
	if err != nil || !ok {
		return 0, &UsageBudgetUnsupported{}
	}
	fields := []any{}
	switch family {
	case "openai-compatible":
		for _, key := range []string{"max_tokens", "max_completion_tokens", "max_output_tokens"} {
			if n, present := body[key]; present {
				fields = append(fields, n)
			}
		}
	case "anthropic":
		fields = append(fields, body["max_tokens"])
	case "gemini":
		config, _ := body["generationConfig"].(map[string]any)
		fields = append(fields, config["maxOutputTokens"])
	default:
		return 0, &UsageBudgetUnsupported{}
	}
	if len(fields) == 0 {
		return 0, &UsageBudgetUnsupported{}
	}
	var maximum int64
	for _, value := range fields {
		n, valid := usageExactCount(value)
		if !valid || n < 1 {
			return 0, &UsageBudgetUnsupported{}
		}
		if n > maximum {
			maximum = n
		}
	}
	return maximum, nil
}

// usageResponseTokens is called only after bounded positive terminal quarantine, never on raw provider input.
func usageResponseTokens(family string, streamed bool, text string) (UsageAmounts, error) {
	out := UsageAmounts{}
	get := func(raw any, key string) any { row, _ := raw.(map[string]any); return row[key] }
	copyTokens := func(raw any, input, output string) error {
		row, ok := raw.(map[string]any)
		if !ok {
			return nil
		}
		for source, target := range map[string]string{input: "inputTokens", output: "outputTokens"} {
			if value, present := row[source]; present {
				n, valid := usageExactCount(value)
				if !valid {
					return &UsageBudgetUnknown{}
				}
				out[target] = n
			}
		}
		return nil
	}
	take := func(value any) error {
		switch family {
		case "openai-compatible":
			return copyTokens(get(value, "usage"), "prompt_tokens", "completion_tokens")
		case "anthropic":
			if err := copyTokens(get(get(value, "message"), "usage"), "input_tokens", "output_tokens"); err != nil {
				return err
			}
			return copyTokens(get(value, "usage"), "input_tokens", "output_tokens")
		case "gemini":
			return copyTokens(get(value, "usageMetadata"), "promptTokenCount", "candidatesTokenCount")
		default:
			return &UsageBudgetUnknown{}
		}
	}
	parse := func(raw string) error {
		if raw == "[DONE]" {
			return nil
		}
		value, err := descriptorJSON([]byte(raw), false)
		if err != nil {
			return &UsageBudgetUnknown{}
		}
		if rows, ok := value.([]any); ok {
			for _, row := range rows {
				if err := take(row); err != nil {
					return err
				}
			}
			return nil
		}
		return take(value)
	}
	if !streamed {
		if err := parse(text); err != nil {
			return nil, err
		}
		return out, nil
	}
	data := []string{}
	flush := func() error {
		if len(data) == 0 {
			return nil
		}
		raw := strings.Join(data, "\n")
		data = nil
		return parse(raw)
	}
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if line == "" {
			if err := flush(); err != nil {
				return nil, err
			}
		} else if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return out, nil
}

func usageDescriptorMatches(f usageDispatchFacts) bool {
	if f.Kind == "agent" {
		return f.Descriptor == string(f.Request)
	}
	descriptor, err := descriptorJSON([]byte(f.Descriptor), false)
	if err != nil {
		return false
	}
	object, ok := descriptor.(map[string]any)
	if !ok {
		return false
	}
	body, ok := object["body"].(string)
	return ok && body == string(f.Request)
}
func (o *usageJournalOwner) reconcile(ctx context.Context) error {
	for _, row := range o.journal.Entries {
		head, err := o.input.Store.ReadReservation(ctx, o.input.Scope, row.Reservation.ID)
		if err != nil {
			return err
		}
		if head == nil || usageCanonical(head.Request) != usageCanonical(row.Reservation) || !usageReceiptValid(head.Receipt, row.Reservation) || head.Receipt.State == "never-admitted" && row.State != "never-admitted" {
			return &UsageBudgetIdentityChanged{}
		}
		if row.Settlement != nil {
			for key, n := range row.Settlement.Observed {
				if actual, ok := head.Receipt.Observed[key]; !ok || actual != n {
					return &UsageBudgetIdentityChanged{}
				}
			}
		}
		for key, n := range head.Receipt.Observed {
			if previous, ok := row.Observed[key]; ok && previous != n {
				return &UsageBudgetIdentityChanged{}
			}
		}
	}
	return nil
}
