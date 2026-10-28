package runcontext

import (
	"context"
	"crypto/rand"
	"encoding/json"

	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
)

const UsageBudgetRecordBytes = 64 * 1024
const UsageBudgetScopes = 64
const UsageBudgetReservations = 4096
const usageMaxSafeInteger int64 = 9007199254740991

var usageDimensions = []string{"modelRequests", "providerInvocations", "inputTokens", "outputTokens"}

// UsageAmounts contains exact reported dimensions OR conservative bounds, never an estimate.
// Its surrounding field says which. Omitted provider figures never mean zero.
type UsageAmounts map[string]int64
type UsageBudgetScope struct {
	Resource string       `json:"resource"`
	ID       string       `json:"id"`
	Revision string       `json:"revision"`
	Limits   UsageAmounts `json:"limits"`
}
type UsageReservation struct {
	Scope              UsageBudgetScope `json:"scope"`
	ID                 string           `json:"id"`
	Root               string           `json:"root"`
	Execution          string           `json:"execution"`
	Role               string           `json:"role"`
	Task               string           `json:"task,omitempty"`
	Kind               string           `json:"kind"`
	RequestDigest      string           `json:"requestDigest"`
	Connection         string           `json:"connection"`
	ConnectionRevision string           `json:"connectionRevision"`
	Model              string           `json:"model"`
	Upper              UsageAmounts     `json:"upper"`
}
type UsageSettlement struct {
	Scope             UsageBudgetScope `json:"scope"`
	ID                string           `json:"id"`
	ReservationDigest string           `json:"reservationDigest"`
	CommitID          string           `json:"commitId"`
	Evidence          string           `json:"evidence"`
	Kind              string           `json:"kind"`
	Observed          UsageAmounts     `json:"observed,omitempty"`
}
type UsageBudgetReceipt struct {
	Resource          string       `json:"resource"`
	Scope             string       `json:"scope"`
	Reservation       string       `json:"reservation"`
	ReservationDigest string       `json:"reservationDigest"`
	Revision          string       `json:"revision"`
	State             string       `json:"state"`
	Observed          UsageAmounts `json:"observed"`
}
type UsageReserveResult struct {
	Receipt UsageBudgetReceipt `json:"receipt"`
	// Created is live operation metadata, never serialized dispatch authority.
	Created bool `json:"created"`
}
type UsageReservationHead struct {
	Request UsageReservation   `json:"request"`
	Receipt UsageBudgetReceipt `json:"receipt"`
}

// UsageBudgetStore is a host-owned atomic budget resource shared by all relevant roots/chats.
// Its transactions are separate from RunStore CAS; unknown acknowledgments never refund.
type UsageBudgetStore interface {
	Resource() string
	Reserve(context.Context, UsageReservation) (UsageReserveResult, error)
	Settle(context.Context, UsageSettlement) (UsageBudgetReceipt, error)
	ReadReservation(context.Context, UsageBudgetScope, string) (*UsageReservationHead, error)
}

func usageFailure(code, source, outcome, detail string) error {
	f := failure.New(code, source, outcome)
	f.Detail = &detail
	return failure.NewError(f)
}

type UsageBudgetExceeded struct{ Dimension string }

func (e *UsageBudgetExceeded) Error() string {
	return "UsageBudgetExceeded: the " + e.Dimension + " budget cannot admit this physical request."
}
func (e *UsageBudgetExceeded) Unwrap() error {
	return usageFailure("request-limit", "context", "not-started", e.Error())
}

type UsageBudgetUnknown struct{}

func (*UsageBudgetUnknown) Error() string {
	return "UsageBudgetUnknown: committed scope usage or a reservation acknowledgment is unknown; no automatic replay or refund."
}
func (e *UsageBudgetUnknown) Unwrap() error {
	f := failure.New("storage", "storage", "unknown")
	detail := e.Error()
	f.Detail = &detail
	return failure.NewError(f)
}

type UsageBudgetUnsupported struct{}

func (*UsageBudgetUnsupported) Error() string {
	return "UsageBudgetUnsupported: this route cannot prove the requested physical usage upper bound."
}
func (e *UsageBudgetUnsupported) Unwrap() error {
	return usageFailure("configuration", "context", "not-started", e.Error())
}

type UsageBudgetIdentityChanged struct{}

func (*UsageBudgetIdentityChanged) Error() string {
	return "UsageBudgetIdentityChanged: the retained budget resource, policy or reservation differs."
}
func (e *UsageBudgetIdentityChanged) Unwrap() error {
	return usageFailure("stale-state", "context", "not-started", e.Error())
}

type UsageBudgetCapacity struct{}

func (*UsageBudgetCapacity) Error() string {
	return "UsageBudgetCapacity: unresolved or idempotent budget records reached the bounded resource capacity."
}
func (e *UsageBudgetCapacity) Unwrap() error {
	return usageFailure("storage", "storage", "not-started", e.Error())
}
func usageID(value string) bool { return len(value) <= 100 && validStorageID(value) }
func usageAmounts(value UsageAmounts, nonempty bool) bool {
	if value == nil || nonempty && len(value) == 0 {
		return false
	}
	for key, number := range value {
		known := false
		for _, dimension := range usageDimensions {
			if key == dimension {
				known = true
			}
		}
		if !known || number < 0 || number > usageMaxSafeInteger {
			return false
		}
	}
	return true
}
func usageCanonical(value any) string {
	raw, _ := json.Marshal(value)
	held, err := ParseJSON(raw)
	if err != nil {
		return ""
	}
	return CanonicalJSON(held)
}
func usageCopy[T any](value T) T {
	raw, _ := json.Marshal(value)
	var held T
	_ = json.Unmarshal(raw, &held)
	return held
}
func usageScope(value UsageBudgetScope, resource string) error {
	if !usageID(value.Resource) || !usageID(value.ID) || !usageID(value.Revision) || !usageAmounts(value.Limits, true) {
		return &StorageInputError{}
	}
	if value.Resource != resource {
		return &UsageBudgetIdentityChanged{}
	}
	return nil
}
func usageRequest(value UsageReservation, resource string) error {
	if err := usageScope(value.Scope, resource); err != nil {
		return err
	}
	for _, id := range []string{value.ID, value.Root, value.Execution, value.Role, value.Connection, value.ConnectionRevision, value.Model} {
		if !usageID(id) {
			return &StorageInputError{}
		}
	}
	if value.Task != "" && !usageID(value.Task) || value.Kind != "model" && value.Kind != "agent" || !recordDigestPattern.MatchString(value.RequestDigest) || !usageAmounts(value.Upper, false) {
		return &StorageInputError{}
	}
	for key := range value.Scope.Limits {
		if _, ok := value.Upper[key]; !ok {
			return &UsageBudgetUnsupported{}
		}
	}
	if value.Kind == "model" {
		if n, ok := value.Upper["modelRequests"]; ok && n != 1 {
			return &StorageInputError{}
		}
		if n, ok := value.Upper["providerInvocations"]; ok && n != 0 {
			return &StorageInputError{}
		}
	} else {
		if n, ok := value.Upper["providerInvocations"]; ok && n != 1 {
			return &StorageInputError{}
		}
		if _, ok := value.Scope.Limits["modelRequests"]; ok {
			return &UsageBudgetUnsupported{}
		}
	}
	if len(usageCanonical(value)) > UsageBudgetRecordBytes {
		return &StorageInputError{}
	}
	return nil
}
func usageSettlement(value UsageSettlement, resource string) error {
	if err := usageScope(value.Scope, resource); err != nil {
		return err
	}
	if !usageID(value.ID) || !usageID(value.CommitID) || !recordDigestPattern.MatchString(value.ReservationDigest) || !recordDigestPattern.MatchString(value.Evidence) {
		return &StorageInputError{}
	}
	if value.Kind == "reported" {
		if !usageAmounts(value.Observed, true) {
			return &StorageInputError{}
		}
	} else if value.Kind != "never-admitted" || value.Observed != nil {
		return &StorageInputError{}
	}
	if len(usageCanonical(value)) > UsageBudgetRecordBytes {
		return &StorageInputError{}
	}
	return nil
}

type heldUsageSettlement struct {
	input   string
	receipt UsageBudgetReceipt
}
type heldUsageReservation struct {
	reserved    UsageBudgetReceipt
	request     UsageReservation
	receipt     UsageBudgetReceipt
	settlements map[string]heldUsageSettlement
}
type heldUsageScope struct {
	scope        UsageBudgetScope
	baseline     UsageAmounts
	reservations map[string]*heldUsageReservation
}
type UsageBudgetSeed struct {
	Scope     UsageBudgetScope
	Committed UsageAmounts
}
type MemoryUsageBudgetOptions struct {
	Seeds []UsageBudgetSeed
	// Fault injection only; no recursive calls. AfterChange can lose an acknowledgment.
	BeforeChange func() error
	AfterChange  func() error
}

// MemoryUsageBudgetStore is explicit host-owned ephemeral state, without a hidden owner registry.
// Share one capability per budget resource. Seeded missing configured usage remains unknown.
type MemoryUsageBudgetStore struct {
	resource string
	gate     chan struct{}
	scopes   map[string]*heldUsageScope
	count    int
	options  MemoryUsageBudgetOptions
}

func NewMemoryUsageBudgetStore(resource string, options MemoryUsageBudgetOptions) (*MemoryUsageBudgetStore, error) {
	if !usageID(resource) {
		return nil, &StorageInputError{}
	}
	if len(options.Seeds) > UsageBudgetScopes {
		return nil, &UsageBudgetCapacity{}
	}
	store := &MemoryUsageBudgetStore{resource: resource, gate: make(chan struct{}, 1), scopes: map[string]*heldUsageScope{}, options: options}
	store.gate <- struct{}{}
	for _, seed := range options.Seeds {
		if err := usageScope(seed.Scope, resource); err != nil {
			return nil, err
		}
		if !usageAmounts(seed.Committed, false) {
			return nil, &StorageInputError{}
		}
		if _, exists := store.scopes[seed.Scope.ID]; exists {
			return nil, &UsageBudgetIdentityChanged{}
		}
		store.scopes[seed.Scope.ID] = &heldUsageScope{scope: usageCopy(seed.Scope), baseline: usageCopy(seed.Committed), reservations: map[string]*heldUsageReservation{}}
	}
	return store, nil
}
func (s *MemoryUsageBudgetStore) Resource() string { return s.resource }
func (s *MemoryUsageBudgetStore) lock(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return failure.Cancelled
	case <-s.gate:
		if ctx.Err() != nil {
			s.gate <- struct{}{}
			return failure.Cancelled
		}
		return nil
	}
}
func (s *MemoryUsageBudgetStore) unlock() { s.gate <- struct{}{} }
func (s *MemoryUsageBudgetStore) held(scope UsageBudgetScope) (*heldUsageScope, error) {
	held := s.scopes[scope.ID]
	if held != nil && usageCanonical(held.scope) != usageCanonical(scope) {
		return nil, &UsageBudgetIdentityChanged{}
	}
	return held, nil
}
func (s *MemoryUsageBudgetStore) Reserve(ctx context.Context, input UsageReservation) (UsageReserveResult, error) {
	var zero UsageReserveResult
	if err := usageRequest(input, s.resource); err != nil {
		return zero, err
	}
	frozen := usageCopy(input)
	if err := s.lock(ctx); err != nil {
		return zero, err
	}
	defer s.unlock()
	held, err := s.held(frozen.Scope)
	if err != nil {
		return zero, err
	}
	if held != nil {
		if old := held.reservations[frozen.ID]; old != nil {
			if usageCanonical(old.request) != usageCanonical(frozen) {
				return zero, &StorageCommitMismatch{}
			}
			return UsageReserveResult{Receipt: usageCopy(old.reserved), Created: false}, nil
		}
	}
	if s.count >= UsageBudgetReservations || held == nil && len(s.scopes) >= UsageBudgetScopes {
		return zero, &UsageBudgetCapacity{}
	}
	if held == nil {
		baseline := UsageAmounts{}
		for key := range frozen.Scope.Limits {
			baseline[key] = 0
		}
		held = &heldUsageScope{scope: usageCopy(frozen.Scope), baseline: baseline, reservations: map[string]*heldUsageReservation{}}
	}
	for _, dimension := range usageDimensions {
		limit, limited := frozen.Scope.Limits[dimension]
		if !limited {
			continue
		}
		consumed, known := held.baseline[dimension]
		if !known {
			return zero, &UsageBudgetUnknown{}
		}
		for _, row := range held.reservations {
			if row.receipt.State == "never-admitted" {
				continue
			}
			amount, ok := row.receipt.Observed[dimension]
			if !ok {
				amount, ok = row.request.Upper[dimension]
			}
			if !ok || amount > usageMaxSafeInteger-consumed {
				return zero, &UsageBudgetUnknown{}
			}
			consumed += amount
		}
		upper := frozen.Upper[dimension]
		if upper > usageMaxSafeInteger-consumed || consumed+upper > limit {
			return zero, &UsageBudgetExceeded{Dimension: dimension}
		}
	}
	if s.options.BeforeChange != nil {
		if err = s.options.BeforeChange(); err != nil {
			return zero, err
		}
	}
	if ctx.Err() != nil {
		return zero, failure.Cancelled
	}
	receipt := UsageBudgetReceipt{Resource: s.resource, Scope: frozen.Scope.ID, Reservation: frozen.ID, ReservationDigest: durableDigest([]byte(usageCanonical(frozen))), Revision: rand.Text(), State: "reserved", Observed: UsageAmounts{}}
	held.reservations[frozen.ID] = &heldUsageReservation{request: frozen, reserved: usageCopy(receipt), receipt: receipt, settlements: map[string]heldUsageSettlement{}}
	s.scopes[frozen.Scope.ID] = held
	s.count++
	if s.options.AfterChange != nil {
		if err = s.options.AfterChange(); err != nil {
			return zero, err
		}
	}
	return UsageReserveResult{Receipt: usageCopy(receipt), Created: true}, nil
}
func (s *MemoryUsageBudgetStore) Settle(ctx context.Context, input UsageSettlement) (UsageBudgetReceipt, error) {
	var zero UsageBudgetReceipt
	if err := usageSettlement(input, s.resource); err != nil {
		return zero, err
	}
	frozen := usageCopy(input)
	if err := s.lock(ctx); err != nil {
		return zero, err
	}
	defer s.unlock()
	held, err := s.held(frozen.Scope)
	if err != nil {
		return zero, err
	}
	var row *heldUsageReservation
	if held != nil {
		row = held.reservations[frozen.ID]
	}
	if row == nil || row.receipt.ReservationDigest != frozen.ReservationDigest {
		return zero, &UsageBudgetIdentityChanged{}
	}
	content := usageCanonical(frozen)
	if prior, ok := row.settlements[frozen.CommitID]; ok {
		if prior.input != content {
			return zero, &StorageCommitMismatch{}
		}
		return usageCopy(prior.receipt), nil
	}
	if len(row.settlements) >= 5 {
		return zero, &UsageBudgetCapacity{}
	}
	if row.receipt.State == "never-admitted" {
		return zero, &UsageBudgetIdentityChanged{}
	}
	observed := usageCopy(row.receipt.Observed)
	state := "never-admitted"
	if frozen.Kind == "never-admitted" {
		if len(observed) != 0 {
			return zero, &UsageBudgetIdentityChanged{}
		}
	} else {
		counted := "modelRequests"
		if row.request.Kind == "agent" {
			counted = "providerInvocations"
		}
		if value, known := frozen.Observed[counted]; known && value != 1 {
			return zero, &UsageBudgetIdentityChanged{}
		}
		for dimension, value := range frozen.Observed {
			upper, ok := row.request.Upper[dimension]
			prior, known := observed[dimension]
			if !ok || value > upper || known && prior != value {
				return zero, &UsageBudgetIdentityChanged{}
			}
			observed[dimension] = value
		}
		state = "settled"
		for key := range row.request.Upper {
			if _, ok := observed[key]; !ok {
				state = "partial"
			}
		}
	}
	if s.options.BeforeChange != nil {
		if err = s.options.BeforeChange(); err != nil {
			return zero, err
		}
	}
	if ctx.Err() != nil {
		return zero, failure.Cancelled
	}
	receipt := row.receipt
	receipt.Revision = rand.Text()
	receipt.State = state
	receipt.Observed = observed
	row.receipt = receipt
	row.settlements[frozen.CommitID] = heldUsageSettlement{input: content, receipt: usageCopy(receipt)}
	if s.options.AfterChange != nil {
		if err = s.options.AfterChange(); err != nil {
			return zero, err
		}
	}
	return usageCopy(receipt), nil
}
func (s *MemoryUsageBudgetStore) ReadReservation(ctx context.Context, scope UsageBudgetScope, id string) (*UsageReservationHead, error) {
	if err := usageScope(scope, s.resource); err != nil {
		return nil, err
	}
	if !usageID(id) {
		return nil, &StorageInputError{}
	}
	if err := s.lock(ctx); err != nil {
		return nil, err
	}
	defer s.unlock()
	held, err := s.held(scope)
	if err != nil {
		return nil, err
	}
	if held == nil {
		return nil, nil
	}
	row := held.reservations[id]
	if row == nil {
		return nil, nil
	}
	out := usageCopy(UsageReservationHead{Request: row.request, Receipt: row.receipt})
	return &out, nil
}
