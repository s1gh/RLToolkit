package backend

import (
	"encoding/json"
	"rl-toolkit/internal/bus"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// RosterTracker watches UpdateState envelopes and emits a synthetic
// _RosterChanged event whenever the roster identity changes (player
// join, leave, team switch, or guid flip — a fresh match). Plugins
// that only care about who's on the field (dejavu, anything similar)
// can subscribe to _RosterChanged and skip UpdateState entirely,
// which is the heaviest event by far (~1-3 KB at 60-120 Hz).
//
// What counts as a "change": the roster fingerprint is the match guid
// plus the sorted (PrimaryId, TeamNum) pairs. Score, position, boost,
// demos, etc. don't move it — those are physics state that lives on
// UpdateState. Player name and platform are NOT in the fingerprint
// either: they're stable for the duration of a match, but a typo
// correction or a late-resolved platform string would otherwise flap
// the event needlessly. The full normalized roster (id/team/name/
// platform per player) is shipped in the payload so consumers don't
// need to call match.build() themselves.
//
// Implements both StateProcessor (Observe — keeps the lastRoster
// snapshot fresh so ResolveByShortcut callers from other emitters
// get current data) and EmitProcessor (Process — returns
// _RosterChanged when the roster identity moved). Registration order
// matters: state processors run before emit processors, so the
// snapshot is current by the time anyone calls ResolveByShortcut
// inside Process.
type RosterTracker struct {
	mu         sync.Mutex
	lastFp     string
	lastGUID   string
	lastRoster []rosterPlayer

	// pending holds the next _RosterChanged payload to emit. Set by
	// Observe when the fingerprint moved; consumed by Process. Nil
	// when there's nothing new.
	pendingMu      sync.Mutex
	pendingPayload *rosterEvent

	// identity is the optional backend "who am I" store. When set,
	// ResolveByShortcut / ResolveByPrimaryId / rosterPlayerToEnriched
	// stamp EnrichedPlayer.IsMe when the player's PrimaryID matches
	// the stored identity. Nil leaves IsMe=false (the SDK still has
	// its own client-side stamping fallback).
	identity *IdentityStore
}

// AttachIdentity wires the IdentityStore so the resolver can stamp
// IsMe when a player matches the stored identity. Optional — without
// it, IsMe stays false and the SDK takes over stamping client-side.
func (r *RosterTracker) AttachIdentity(s *IdentityStore) { r.identity = s }

// NewRosterTracker creates a tracker. The bus arg is kept so existing
// call sites keep compiling, but the tracker no longer needs it —
// the pipeline broadcasts via the EmitProcessor return value.
func NewRosterTracker(_ *bus.Bus) *RosterTracker {
	return &RosterTracker{}
}

// Observe updates the roster snapshot from UpdateState. Clears the
// snapshot on MatchDestroyed and connection-loss so a fresh
// rejoin into the same lobby publishes _RosterChanged for late
// subscribers.
func (t *RosterTracker) Observe(evt bus.Event) {
	switch evt.Name {
	case "UpdateState":
		t.onUpdateState(evt.Raw)
	case "MatchDestroyed", "_ConnectionStatus":
		t.mu.Lock()
		t.lastFp = ""
		t.lastGUID = ""
		t.lastRoster = nil
		t.mu.Unlock()
		t.queueEmission("", nil)
	}
}

// Process returns _RosterChanged when Observe staged one. Pipeline
// registration order should put RosterTracker before any consumer
// that reads ResolveByShortcut/RosterSnapshot inside its own Process.
func (t *RosterTracker) Process(evt bus.Event) []bus.Event {
	t.pendingMu.Lock()
	pending := t.pendingPayload
	t.pendingPayload = nil
	t.pendingMu.Unlock()
	if pending == nil {
		return nil
	}
	body, err := json.Marshal(struct {
		MatchGUID string         `json:"matchGuid,omitempty"`
		Players   []rosterPlayer `json:"players"`
	}{
		MatchGUID: pending.MatchGUID,
		Players:   pending.Players,
	})
	if err != nil {
		return nil
	}
	return []bus.Event{{Name: "_RosterChanged", Data: body}}
}

func (t *RosterTracker) queueEmission(guid string, players []rosterPlayer) {
	t.pendingMu.Lock()
	t.pendingPayload = &rosterEvent{
		Event: "_RosterChanged",
		MatchGUID: guid,
		Players:   append([]rosterPlayer(nil), players...),
	}
	t.pendingMu.Unlock()
}

// rosterPlayer is the per-player payload shipped on _RosterChanged.
// Same shape as EnrichedPlayer (player_resolver.go) so synthetic events
// look identical wherever a player appears — _PlayerDemolished.attacker,
// _GoalScored.scorer, _RosterChanged.players[i] all share fields. The
// SDK's stampClientSideFields walker stamps isMe/encounter on every
// EnrichedPlayer it finds in a synthetic payload, so we leave those
// blank server-side (they're inherently per-user state).
type rosterPlayer struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Team     int    `json:"team"`
	Platform string `json:"platform,omitempty"`
	IsBot    bool   `json:"isBot"`
}

type rosterEvent struct {
	Event     string         `json:"Event"`
	MatchGUID string         `json:"matchGuid,omitempty"`
	Players   []rosterPlayer `json:"players"`
}

// updateStatePlayer mirrors the wire shape inside the JSON-encoded
// Data string. We only decode the fields the fingerprint and payload
// need; the full UpdateState carries 20+ per-player fields that the
// roster tracker doesn't care about, so a partial unmarshal saves a
// chunk of allocation per tick.
type updateStatePlayer struct {
	PrimaryID string `json:"PrimaryId"`
	Name      string `json:"Name"`
	TeamNum   int    `json:"TeamNum"`
}

// Lowercase wire variant — RL has shipped both casings across builds.
// Same fields, JSON tags pointing at the lowercase keys.
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

// envelopeData pulls the inner Data string out of an UpdateState
// envelope. RL ships Data as a JSON-encoded string containing JSON,
// so this is a double-decode boundary. Returns "" on parse failure
// — the caller treats that as "no roster change", which is the safe
// no-op default.
type updateStateEnvelope struct {
	Data    string `json:"Data"`
	DataLow string `json:"data"`
}

func (t *RosterTracker) onUpdateState(raw []byte) {
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
		players[i].PrimaryID = canonicalizeBotId(players[i].PrimaryID, players[i].Name)
	}
	fp := fingerprint(guid, players)

	t.mu.Lock()
	if fp == t.lastFp {
		t.mu.Unlock()
		return
	}
	t.lastFp = fp
	t.lastGUID = guid
	t.lastRoster = make([]rosterPlayer, 0, len(players))
	for _, p := range players {
		t.lastRoster = append(t.lastRoster, rosterPlayer{
			ID:       p.PrimaryID,
			Name:     p.Name,
			Team:     p.TeamNum,
			Platform: platformFromID(p.PrimaryID),
			IsBot:    isBotId(p.PrimaryID),
		})
	}
	snapshot := append([]rosterPlayer(nil), t.lastRoster...)
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
func (t *RosterTracker) Snapshot() []byte {
	t.mu.Lock()
	if len(t.lastRoster) == 0 {
		t.mu.Unlock()
		return nil
	}
	guid := t.lastGUID
	out := make([]rosterPlayer, len(t.lastRoster))
	copy(out, t.lastRoster)
	t.mu.Unlock()

	body, err := json.Marshal(struct {
		MatchGUID string         `json:"matchGuid,omitempty"`
		Players   []rosterPlayer `json:"players"`
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

// platformFromID extracts the leading "Steam" / "Epic" / etc segment
// from a PrimaryId of the shape "Platform|UserId|SubId". Mirrors the
// SDK's `id.split('|')[0]` so plugins receive the same value whether
// they read it from match.current.players or from a _RosterChanged
// payload.
func platformFromID(id string) string {
	if id == "" {
		return ""
	}
	i := strings.IndexByte(id, '|')
	if i <= 0 {
		return ""
	}
	return id[:i]
}
