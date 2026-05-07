package backend

import (
	"rl-toolkit/internal/wire"
	"strings"
)

// init seeds the wire package's name-canonicalization table from
// EventCatalog. wire.Canonical is otherwise a no-op fallback, so
// rl_source's normalization step depends on this running before any
// envelope is decoded.
//
// The two manual aliases cover wire-level RL build quirks that don't
// lowercase to the catalog form.
func init() {
	m := make(map[string]string, len(EventCatalog)+2)
	for _, e := range EventCatalog {
		m[strings.ToLower(e.Name)] = e.Name
	}
	// Observed on this RL build: the wire ships `replaywillend` (no
	// `goal` prefix). Treat it as the catalog's GoalReplayWillEnd so
	// plugins subscribing to the documented name still get events.
	m["replaywillend"] = "GoalReplayWillEnd"
	wire.RegisterAliases(m)
}
