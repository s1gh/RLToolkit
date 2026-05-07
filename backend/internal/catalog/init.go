package catalog

import (
	"rl-toolkit/backend/internal/wire"
	"strings"
)

// init seeds the wire package's name-canonicalization table from the
// catalog. wire.Canonical is otherwise a no-op fallback, so the
// source/dispatcher's normalization step depends on this running
// before any envelope is decoded.
//
// The manual alias covers a wire-level RL build quirk that doesn't
// lowercase to the catalog form.
func init() {
	m := make(map[string]string, len(Entries)+1)
	for _, e := range Entries {
		m[strings.ToLower(e.Name)] = e.Name
	}
	// Observed on this RL build: the wire ships `replaywillend` (no
	// `goal` prefix). Treat it as the catalog's GoalReplayWillEnd so
	// plugins subscribing to the documented name still get events.
	m["replaywillend"] = "GoalReplayWillEnd"
	wire.RegisterAliases(m)
}
