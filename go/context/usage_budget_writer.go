package runcontext

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/Judgment-Pack/judgment-pack-agent/go/internal/usagewitness"
	"io"
	"net/http"
	"strings"

	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
)

type usageDispatchContextKey struct{}

func (w *RunWriter) attachUsage(ctx context.Context, input *UsageBudgetInput) error {
	var retained *UsageBudgetJournal
	if w.latest != nil && w.latest.UsageBudget != nil {
		raw, err := json.Marshal(w.latest.UsageBudget)
		if err != nil {
			return err
		}
		value, err := ParseJSON(raw)
		if err != nil {
			return err
		}
		retained, err = readUsageJournal(value)
		if err != nil {
			return err
		}
	}
	if input == nil {
		if retained != nil {
			return &UsageBudgetIdentityChanged{}
		}
		return nil
	}
	if w.latest != nil && retained == nil {
		return &UsageBudgetIdentityChanged{}
	}
	owner, err := newUsageJournalOwner(input, w.ID, retained)
	if err != nil {
		return err
	}
	if err := owner.reconcile(ctx); err != nil {
		return err
	}
	w.budget = owner
	return nil
}

// PrepareUsageRequest is the canonical physical boundary, after final wire rewriting and before
// ClaimRequest/InvokeDurable. The returned context carries one private original-writer ticket.
func (c *SessionContext) PrepareUsageRequest(ctx context.Context, role, kind, source, tool, connection, model string, descriptor, body []byte, family string) (context.Context, error) {
	binding := c.Persistence(role)
	if binding == nil {
		return ctx, nil
	}
	w := binding.Writer
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.budget == nil {
		return ctx, nil
	}
	if w.fatal != nil {
		return ctx, w.fatal
	}
	if w.dispatchClosed.Load() || ctx.Err() != nil {
		return ctx, failure.Cancelled
	}
	revision := ""
	if c.session.Context != nil {
		revision = c.session.Context.ConnectionRevisions[connection]
	}
	f := usageDispatchFacts{DurableScope: binding.Scope, Source: source, Tool: tool, Descriptor: string(descriptor), UsageBoundRequest: UsageBoundRequest{Kind: kind, Family: family, Connection: connection, ConnectionRevision: revision, Model: model, Request: append([]byte(nil), body...)}}
	ticket, err := w.budget.reserve(ctx, newStorageIdentity(), f)
	if err != nil {
		if refused := w.budget.refusal(f, err); refused != nil {
			w.fatal = refused
			return ctx, refused
		}
		if _, saved := w.saveLocked(ctx, *binding, nil, nil, nil, nil); saved != nil {
			return ctx, saved
		}
		w.fatal = err
		return ctx, err
	}
	if _, err = w.saveLocked(ctx, *binding, nil, nil, nil, nil); err != nil {
		return ctx, err
	}
	return context.WithValue(ctx, usageDispatchContextKey{}, ticket), nil
}
func finishUsageResponse(ctx context.Context, binding *RunBinding, handle *modelCompletion, response *http.Response) error {
	w := binding.Writer
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.budget == nil {
		return nil
	}
	observed := UsageAmounts{"modelRequests": 1}
	if response == nil || response.Body == nil {
		return &UsageBudgetUnknown{}
	}
	// Canonical verification already read/replaced this body, but this boundary retains its own byte cap.
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4*1024*1024+1))
	response.Body.Close()
	response.Body = io.NopCloser(bytes.NewReader(raw))
	if err != nil || len(raw) > 4*1024*1024 {
		return &UsageBudgetUnknown{}
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		ticket, _ := ctx.Value(usageDispatchContextKey{}).(*usageTicket)
		if ticket == nil || ticket.owner != w.budget {
			return &UsageBudgetIdentityChanged{}
		}
		// The kind/final request binding was consumed before dispatch; family is private live metadata.
		family := ticket.family
		tokens, err := usageResponseTokens(family, strings.Contains(response.Header.Get("Content-Type"), "text/event-stream"), string(raw))
		if err != nil {
			return err
		}
		for k, n := range tokens {
			observed[k] = n
		}
	}
	settlementErr := w.budget.observe(ctx, handle.id, observed, durableDigest([]byte(handle.id+":model-result")))
	if _, err = w.saveLocked(ctx, *binding, nil, nil, nil, nil); err != nil {
		return &StorageOutcomeUnknown{cause: err}
	}
	if settlementErr != nil {
		w.fatal = settlementErr
		return settlementErr
	}
	return nil
}

// UsageBudgetEnabled exposes no budget capability; request builders use it only to preserve legacy ordering when absent.
func (c *SessionContext) UsageBudgetEnabled() bool {
	binding := c.Persistence("author")
	if binding == nil {
		return false
	}
	binding.Writer.mu.Lock()
	defer binding.Writer.mu.Unlock()
	return binding.Writer.budget != nil
}

// NativeUsageWitness is an internal, live exact-claim token; external hosts cannot import its observation operations.
func NativeUsageWitness(ctx context.Context) *usagewitness.Witness {
	ticket, _ := ctx.Value(usageDispatchContextKey{}).(*usageTicket)
	if ticket == nil {
		return nil
	}
	return ticket.nativeWitness
}
