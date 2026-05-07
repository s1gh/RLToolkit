package backend

import (
	"rl-toolkit/backend/internal/bus"
	"rl-toolkit/backend/internal/identity"
	"rl-toolkit/backend/internal/roster"
)

// RosterTracker is the backend-package alias for roster.Tracker. The
// real implementation lives in internal/roster; this shim keeps
// existing call sites (main.go, server.go, the in-package tests)
// compiling while the rest of the migration completes.
type RosterTracker = roster.Tracker

// rosterPlayer is the per-player payload shipped on _RosterChanged.
// Aliased to roster.Player so backend tests that reference the type
// (test_helpers, bus_test) keep compiling.
type rosterPlayer = roster.Player

// rosterEvent is the synthetic event payload shape. Kept as an alias
// so the in-backend roster tests continue to typecheck.
type rosterEvent = struct {
	Event     string         `json:"Event"`
	MatchGUID string         `json:"matchGuid,omitempty"`
	Players   []roster.Player `json:"players"`
}

// NewRosterTracker mirrors the historical signature (taking a *bus.Bus
// the tracker no longer needs) so call sites don't have to change.
func NewRosterTracker(_ *bus.Bus) *RosterTracker { return roster.New() }

// identityAdapter bridges *identity.Store to roster.IdentityLookup —
// the consumer-side interface declares MyPrimaryID(), the store has
// Get() returning a richer *Identity. The adapter does the unwrap.
type identityAdapter struct{ s *identity.Store }

func (a identityAdapter) MyPrimaryID() string {
	if a.s == nil {
		return ""
	}
	id := a.s.Get()
	if id == nil {
		return ""
	}
	return id.PrimaryID
}

// AttachIdentityStore wires the persistent IdentityStore to a
// RosterTracker via the adapter. Defined here (not on the alias) so
// callers don't need to know about identityAdapter.
func AttachIdentityStore(t *RosterTracker, s *IdentityStore) {
	t.AttachIdentity(identityAdapter{s: s})
}

// rewriteUpdateStateBotIds forwards to internal/roster — preserved so
// rl_source.go's call site keeps compiling.
var rewriteUpdateStateBotIds = roster.RewriteUpdateStateBotIds
