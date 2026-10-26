// Package usagewitness is internal admitted-callback evidence, never host metadata or dispatch authority.
package usagewitness

import "sync"

type Witness struct {
	mu             sync.Mutex
	owner, ticket  any
	live, admitted bool
}

func New(owner, ticket any) *Witness { return &Witness{owner: owner, ticket: ticket, live: true} }
func Observe(w *Witness) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.live {
		w.admitted = true
	}
}
func Close(w *Witness) {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.live = false
	w.mu.Unlock()
}
func Admitted(w *Witness, owner, ticket any) bool {
	if w == nil {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return !w.live && w.admitted && w.owner == owner && w.ticket == ticket
}
