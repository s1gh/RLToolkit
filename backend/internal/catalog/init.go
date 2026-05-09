package catalog

import (
	"rl-toolkit/backend/internal/wire"
	"strings"
)

// init seeds wire.Canonical's lookup table from the catalog. Without
// this, the source dispatcher's normalization step is a no-op.
func init() {
	m := make(map[string]string, len(Entries)+1)
	for _, e := range Entries {
		m[strings.ToLower(e.Name)] = e.Name
	}
	// RL build quirk: the wire ships `replaywillend` without the
	// `goal` prefix. Map it to GoalReplayWillEnd.
	m["replaywillend"] = "GoalReplayWillEnd"
	wire.RegisterAliases(m)
}
