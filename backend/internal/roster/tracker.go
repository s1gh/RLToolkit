// Package roster owns the live-roster tracker. It watches UpdateState
// envelopes, fingerprints (matchGuid + (PrimaryId, TeamNum) pairs) the
// roster, and emits a synthetic _RosterChanged event whenever the
// fingerprint moves. It also exposes ResolveByShortcut /
// ResolveByPrimaryId so emit processors can enrich a wire-side
// player stub against the live roster.
package roster

import (
	"encoding/json"
	"rl-toolkit/backend/internal/bus"
	"rl-toolkit/backend/internal/types"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// IdentityLookup is the consumer-side view of an IdentityStore: just
// enough to stamp EnrichedPlayer.IsMe when a roster member matches the
// stored "who am I" PrimaryID. Optional — without it, IsMe stays false
// and the SDK takes over stamping client-side. Implementations return
// "" when no identity is set.
type IdentityLookup interface {
	MyPrimaryID() string
}

// Tracker watches UpdateState envelopes and emits _RosterChanged
// whenever the roster identity changes (join, leave, team switch, or
// guid flip). Plugins that only care about who's on the field can
// subscribe to _RosterChanged and skip UpdateState — the heaviest
// event by far (~1-3 KB per packet at the user's PacketSendRate).
//
// The fingerprint is matchGuid + sorted (PrimaryId, TeamNum) pairs.
// Score, boost, position etc. don't move it — those are physics state
// on UpdateState. Name and platform are excluded too: a typo
// correction or late-resolved platform string would otherwise flap
// the event needlessly. The full normalized roster ships in the
// payload so consumers don't have to rebuild it.
//
// Implements both StateProcessor (Observe keeps lastRoster fresh for
// ResolveByShortcut) and EmitProcessor (Process returns the staged
// _RosterChanged). State runs before emit in the pipeline, so the
// snapshot is current by the time Process callers reach for it.
type Tracker struct {
	mu         sync.Mutex
	lastFp     string
	lastGUID   string
	lastRoster []Player

	pendingMu      sync.Mutex
	pendingPayload *rosterEvent

	identity IdentityLookup
}

// New creates a Tracker.
func New() *Tracker { return &Tracker{} }

// AttachIdentity wires an IdentityLookup so the resolver can stamp IsMe
// when a player matches the stored identity.
func (t *Tracker) AttachIdentity(s IdentityLookup) { t.identity = s }

// SetRoster replaces the cached roster snapshot. Used by tests.
func (t *Tracker) SetRoster(p []Player) {
	t.mu.Lock()
	t.lastRoster = append([]Player(nil), p...)
	t.mu.Unlock()
}

// Player is the per-player payload shipped on _RosterChanged. Same
// shape as types.EnrichedPlayer so synthetic events look identical
// wherever a player appears — _PlayerDemolished.attacker,
// _GoalScored.scorer, _RosterChanged.players[i] all share fields.
type Player struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Team     int    `json:"team"`
	Platform string `json:"platform,omitempty"`
	IsBot    bool   `json:"isBot"`
}

type rosterEvent struct {
	Event     string   `json:"Event"`
	MatchGUID string   `json:"matchGuid,omitempty"`
	Players   []Player `json:"players"`
}

// updateStatePlayer mirrors the wire shape inside the JSON-encoded
// Data string. We only decode the fields the fingerprint and payload
// need; the full UpdateState carries 20+ per-player fields that the
// roster tracker doesn't care about.
type updateStatePlayer struct {
	PrimaryID string `json:"PrimaryId"`
	Name      string `json:"Name"`
	TeamNum   int    `json:"TeamNum"`
}

type updateStatePlayerLower struct {
	PrimaryID string `json:"primaryid"`
	Name      string `json:"name"`
	TeamNum   int    `json:"teamnum"`
}

type updateStateData struct {
	MatchGUID    string                   `json:"MatchGuid"`
	MatchGUIDLow string                   `json:"matchguid"`
	Players      []updateStatePlayer      `json:"Players"`
	PlayersLow   []updateStatePlayerLower `json:"players"`
}

type updateStateEnvelope struct {
	Data    string `json:"Data"`
	DataLow string `json:"data"`
}

// Observe updates the roster snapshot from UpdateState. Clears the
// snapshot on MatchDestroyed and connection-loss so a fresh rejoin
// into the same lobby publishes _RosterChanged for late subscribers.
func (t *Tracker) Observe(evt bus.Event) {
	switch evt.Name {
	case "UpdateState":
		t.onUpdateState(evt.Raw)
	case "MatchDestroyed", "_ConnectionStatus":
		t.mu.Lock()
		hadRoster := len(t.lastRoster) > 0
		t.lastFp = ""
		t.lastGUID = ""
		t.lastRoster = nil
		t.mu.Unlock()
		// Only emit on the actual transition (had-roster → none) so
		// boot-time / idle-reconnect with an already-empty roster stays quiet.
		if hadRoster {
			t.queueEmission("", nil)
		}
	}
}

// Process returns _RosterChanged when Observe staged one.
func (t *Tracker) Process(evt bus.Event) []bus.Event {
	t.pendingMu.Lock()
	pending := t.pendingPayload
	t.pendingPayload = nil
	t.pendingMu.Unlock()
	if pending == nil {
		return nil
	}
	body, err := json.Marshal(struct {
		MatchGUID string   `json:"matchGuid,omitempty"`
		Players   []Player `json:"players"`
	}{
		MatchGUID: pending.MatchGUID,
		Players:   pending.Players,
	})
	if err != nil {
		return nil
	}
	return []bus.Event{{Name: "_RosterChanged", Data: body}}
}

func (t *Tracker) queueEmission(guid string, players []Player) {
	t.pendingMu.Lock()
	t.pendingPayload = &rosterEvent{
		Event:     "_RosterChanged",
		MatchGUID: guid,
		Players:   append([]Player(nil), players...),
	}
	t.pendingMu.Unlock()
}

func (t *Tracker) onUpdateState(raw []byte) {
	var env updateStateEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return
	}
	inner := env.Data
	if inner == "" {
		inner = env.DataLow
	}
	if inner == "" {
		return
	}
	var d updateStateData
	if err := json.Unmarshal([]byte(inner), &d); err != nil {
		return
	}
	guid := d.MatchGUID
	if guid == "" {
		guid = d.MatchGUIDLow
	}
	players := d.Players
	if len(players) == 0 && len(d.PlayersLow) > 0 {
		players = make([]updateStatePlayer, len(d.PlayersLow))
		for i, p := range d.PlayersLow {
			players[i] = updateStatePlayer{PrimaryID: p.PrimaryID, Name: p.Name, TeamNum: p.TeamNum}
		}
	}
	for i := range players {
		players[i].PrimaryID = types.CanonicalizeBotID(players[i].PrimaryID, players[i].Name)
	}
	fp := fingerprint(guid, players)

	t.mu.Lock()
	if fp == t.lastFp {
		t.mu.Unlock()
		return
	}
	t.lastFp = fp
	t.lastGUID = guid
	t.lastRoster = make([]Player, 0, len(players))
	for _, p := range players {
		t.lastRoster = append(t.lastRoster, Player{
			ID:       p.PrimaryID,
			Name:     p.Name,
			Team:     p.TeamNum,
			Platform: types.PlatformFromID(p.PrimaryID),
			IsBot:    types.IsBotID(p.PrimaryID),
		})
	}
	snapshot := append([]Player(nil), t.lastRoster...)
	t.mu.Unlock()

	t.queueEmission(guid, snapshot)
}

func fingerprint(guid string, players []updateStatePlayer) string {
	type kv struct {
		id   string
		team int
	}
	keys := make([]kv, 0, len(players))
	for _, p := range players {
		keys = append(keys, kv{id: p.PrimaryID, team: p.TeamNum})
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].id != keys[j].id {
			return keys[i].id < keys[j].id
		}
		return keys[i].team < keys[j].team
	})
	var b strings.Builder
	b.Grow(len(guid) + len(keys)*32)
	b.WriteString(guid)
	for _, k := range keys {
		b.WriteByte('|')
		b.WriteString(k.id)
		b.WriteByte(':')
		b.WriteString(strconv.Itoa(k.team))
	}
	return b.String()
}

// Snapshot returns the most recently published _RosterChanged envelope
// as raw JSON in the new wire shape ({Event, Data}), or nil if the
// tracker hasn't seen a roster yet. Used by the SSE handler to replay
// the current roster to a freshly-connected subscriber.
func (t *Tracker) Snapshot() []byte {
	t.mu.Lock()
	if len(t.lastRoster) == 0 {
		t.mu.Unlock()
		return nil
	}
	guid := t.lastGUID
	out := make([]Player, len(t.lastRoster))
	copy(out, t.lastRoster)
	t.mu.Unlock()

	body, err := json.Marshal(struct {
		MatchGUID string   `json:"matchGuid,omitempty"`
		Players   []Player `json:"players"`
	}{
		MatchGUID: guid,
		Players:   out,
	})
	if err != nil {
		return nil
	}
	envelope, err := json.Marshal(struct {
		Event string          `json:"Event"`
		Data  json.RawMessage `json:"Data"`
	}{Event: "_RosterChanged", Data: body})
	if err != nil {
		return nil
	}
	return envelope
}

// RosterSnapshot returns a copy of the current roster cache.
func (t *Tracker) RosterSnapshot() []Player {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]Player, len(t.lastRoster))
	copy(out, t.lastRoster)
	return out
}
