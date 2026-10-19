package gate

import (
	"errors"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

// Approvals, as approval.json states them. No approval crosses the engine slot: a run proposes
// and ends, and the host acts on the proposal afterwards under the chat's approval state. That
// state is a mode (manual, Ask me; auto, Auto within limits) and at most one grant, which only an
// explicit decision of the person mints (the Desk server's assistantApprovalTransaction); this
// package has no way to mint one, and no tool on the ceiling is an approval action. What a Go host
// reads here is the state's rule: the mode it shows and whether a grant admits one commit.

// ApprovalActions are the host actions a grant may be for (approval.json $defs/action): the Desk
// server's dispatcher admits these four and refuses any other.
var ApprovalActions = []string{"policy-update", "pack-update", "portable-research", "dialect-publish"}

// Resource is the resource a commit would change, as the host resolves it now.
type Resource struct {
	Path     string
	Name     string
	Revision string
}

// ErrApproval is the refusal of a commit no grant admits.
var ErrApproval = errors.New("Review and allow this action before saving automatically.")

// ErrApprovalAction is the refusal of an action outside the four.
var ErrApprovalAction = errors.New("This action requires explicit review.")

// ErrApprovalStale is the refusal of a commit whose resource changed after the grant.
var ErrApprovalStale = errors.New("The resource or its checks changed. Review a new allowance.")

// grantState is the grant's state as of now: a ready grant past its expiry reads as expired.
func grantState(grant *contract.ApprovalGrant, now time.Time) string {
	if grant.State != "ready" {
		return grant.State
	}
	expires, err := time.Parse(time.RFC3339, grant.ExpiresAt)
	if err != nil || !now.Before(expires) {
		return "expired"
	}
	return "ready"
}

// ApprovalMode is the mode the host shows: auto only where a grant is ready now, else manual.
func ApprovalMode(approval contract.Approval, now time.Time) string {
	if approval.Grant != nil && grantState(approval.Grant, now) == "ready" {
		return "auto"
	}
	return "manual"
}

// Commit says whether the approval state admits committing one action now, and the grant's state
// after: used where it admits (a grant is consumed before the commit runs, and never replayed);
// expired where it ran out; stale where the resource's revision is no longer the grant's. A
// commit is admitted only for one of the four actions, by the grant's own id, for the grant's
// action, path, name and target, on the revision the grant is bound to and the resource still
// has, before the grant expires.
func Commit(approval contract.Approval, request contract.ApprovalRequest, current Resource, now time.Time) (string, error) {
	action := deref(request.Action)
	if !contains(ApprovalActions, action) {
		return stateOf(approval.Grant, now), ErrApprovalAction
	}
	grant := approval.Grant
	if grant == nil {
		return "", ErrApproval
	}
	state := grantState(grant, now)
	if grant.ID != deref(request.ID) || grant.Action != action || grant.Path != current.Path || grant.Name != current.Name ||
		deref(grant.Target) != deref(request.Target) || state != "ready" {
		return state, ErrApproval
	}
	if deref(request.Revision) != grant.Revision || current.Revision != grant.Revision {
		return "stale", ErrApprovalStale
	}
	return "used", nil
}

func stateOf(grant *contract.ApprovalGrant, now time.Time) string {
	if grant == nil {
		return ""
	}
	return grantState(grant, now)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
