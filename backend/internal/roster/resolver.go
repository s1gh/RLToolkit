package roster

import (
	"rl-toolkit/backend/internal/types"
)

// ResolveByShortcut maps a {Name, Shortcut, TeamNum} stub to a fully
// enriched player using the current live roster. Lookup is by Name —
// RL names are unique within a match. Shortcut is RL's per-match slot
// index (a number), kept on the enriched output for plugins that need
// it but not used for the roster join. Falls back to building a
// minimal enriched stub from the ref itself if no roster match is
// found.
func (t *Tracker) ResolveByShortcut(ref types.ShortcutRef) *types.EnrichedPlayer {
	if ref.Name == "" {
		return nil
	}

	t.mu.Lock()
	roster := t.lastRoster
	t.mu.Unlock()

	for _, p := range roster {
		if p.Name == ref.Name {
			return t.toEnriched(p)
		}
	}

	return &types.EnrichedPlayer{
		Name: ref.Name,
		Team: ref.TeamNum,
	}
}

// ResolveByPrimaryId looks up a player by PrimaryId in the current
// roster snapshot. Returns nil if id is empty.
func (t *Tracker) ResolveByPrimaryId(id string) *types.EnrichedPlayer {
	if id == "" {
		return nil
	}

	t.mu.Lock()
	roster := t.lastRoster
	t.mu.Unlock()

	for _, p := range roster {
		if p.ID == id {
			return t.toEnriched(p)
		}
	}
	out := &types.EnrichedPlayer{
		ID:       id,
		Platform: types.PlatformFromID(id),
		IsBot:    types.IsBotID(id),
	}
	t.stampIsMe(out)
	return out
}

func (t *Tracker) toEnriched(p Player) *types.EnrichedPlayer {
	out := &types.EnrichedPlayer{
		ID:       p.ID,
		Name:     p.Name,
		Team:     p.Team,
		Platform: p.Platform,
		IsBot:    types.IsBotID(p.ID),
	}
	t.stampIsMe(out)
	return out
}

func (t *Tracker) stampIsMe(p *types.EnrichedPlayer) {
	if t.identity == nil || p == nil || p.ID == "" {
		return
	}
	if myID := t.identity.MyPrimaryID(); myID != "" && myID == p.ID {
		p.IsMe = true
	}
}
