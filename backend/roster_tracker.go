package main

import (
	"bytes"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// RosterTracker watches UpdateState envelopes and emits a synthetic
// "_RosterChanged" event whenever the roster identity changes (player
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
type RosterTracker struct {
	bus *EventBus

	mu        sync.Mutex
	lastFp    string
	lastGUID  string
	lastRoster []rosterPlayer
}

// NewRosterTracker creates a tracker tied to a bus. No background
// goroutine — Feed runs synchronously from the RL client's dispatcher
// (same call site as LifecycleTracker.Feed), so changes publish
// inline with the UpdateState that triggered them.
func NewRosterTracker(bus *EventBus) *RosterTracker {
	return &RosterTracker{bus: bus}
}

// Feed inspects every dispatched packet. Non-UpdateState envelopes are
// cheap to skip (substring miss, no JSON decode); UpdateState envelopes
// pull their inner Data string out and run the fingerprint comparison.
//
// MatchDestroyed clears the tracker so the next match starts fresh.
// Without this a back-to-back rejoin into the same lobby (same guid,
// same roster) would silently no-op and a late-mounted plugin would
// see no _RosterChanged event.
func (t *RosterTracker) Feed(raw []byte) {
	head := raw
	if len(head) > 96 {
		head = head[:96]
	}
	for _, m := range updateStateMarkers {
		if bytes.Contains(head, m) {
			t.onUpdateState(raw)
			return
		}
	}
	// Fast-path miss: only one non-UpdateState event matters for our
	// state — MatchDestroyed clears the cache so a re-mount of the
	// same match guid still emits _RosterChanged.
	event := extractEventName(raw)
	if event == "MatchDestroyed" || event == "_ConnectionStatus" {
		t.mu.Lock()
		t.lastFp = ""
		t.lastGUID = ""
		t.lastRoster = nil
		t.mu.Unlock()
	}
}

// rosterPlayer is the per-player payload shipped on _RosterChanged.
// Same field set the SDK reads when it builds a match view from
// UpdateState, minus the fast-changing physics fields. Encounter
// counts, isMe, aliases — that's all client-side state the SDK adds
// after receiving this event.
type rosterPlayer struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Team     int    `json:"team"`
	Platform string `json:"platform,omitempty"`
}

type rosterEvent struct {
	Event     string         `json:"Event"`
	MatchGUID string         `json:"match_guid"`
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
// Same fields, JSON tags pointing at the lowercase keys. The
// LifecycleTracker has the same back-compat dance for its top-level
// envelope; we mirror it for the inner Data shape.
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
// so this is a double-decode boundary. Returns nil on parse failure
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
	// Normalize the player list to the canonical PascalCase shape so
	// downstream code sees one source of truth.
	players := d.Players
	if len(players) == 0 && len(d.PlayersLow) > 0 {
		players = make([]updateStatePlayer, len(d.PlayersLow))
		for i, p := range d.PlayersLow {
			players[i] = updateStatePlayer{PrimaryID: p.PrimaryID, Name: p.Name, TeamNum: p.TeamNum}
		}
	}

	// Build the fingerprint: guid + sorted (id, team) pairs. Sorting
	// makes the comparison order-independent — RL doesn't guarantee
	// stable ordering across ticks, and a reorder shouldn't trigger
	// a synthetic event.
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
		})
	}
	t.mu.Unlock()

	t.publish(guid, players)
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

func (t *RosterTracker) publish(guid string, players []updateStatePlayer) {
	out := make([]rosterPlayer, 0, len(players))
	for _, p := range players {
		out = append(out, rosterPlayer{
			ID:       p.PrimaryID,
			Name:     p.Name,
			Team:     p.TeamNum,
			Platform: platformFromID(p.PrimaryID),
		})
	}
	b, err := json.Marshal(rosterEvent{
		Event:     "_RosterChanged",
		MatchGUID: guid,
		Players:   out,
	})
	if err != nil {
		return
	}
	t.bus.Publish(b)
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
