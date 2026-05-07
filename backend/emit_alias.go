package backend

import (
	"rl-toolkit/internal/bus"
	"rl-toolkit/internal/emit"
)

// FastestShotEmitter / FirstBloodEmitter alias the emitters extracted
// to internal/emit. The original constructor names (NewFastestShotEmitter,
// NewFirstBloodEmitter) are preserved as forwarders so main.go's
// pipe.AddEmit calls keep working unchanged.
type FastestShotEmitter = emit.FastestShot
type FirstBloodEmitter = emit.FirstBlood
type DemosEmitter = emit.Demos
type CrossbarEmitter = emit.Crossbar

func NewFastestShotEmitter() *FastestShotEmitter { return emit.NewFastestShot() }
func NewFirstBloodEmitter() *FirstBloodEmitter   { return emit.NewFirstBlood() }

// NewDemosEmitter forwards into emit.NewDemos. TickStore satisfies
// emit.TickReader structurally (PlayerScalars has the same signature).
func NewDemosEmitter(ticks *TickStore) *DemosEmitter {
	return emit.NewDemos(ticks)
}

// NewCrossbarEmitter forwards into emit.NewCrossbar. RosterTracker
// satisfies emit.RosterResolver and TickStore satisfies emit.ReplayGate
// structurally; an explicit nil ticks (used by some tests) flows
// through unchanged.
func NewCrossbarEmitter(roster *RosterTracker, ticks *TickStore) *CrossbarEmitter {
	if ticks == nil {
		return emit.NewCrossbar(roster, nil)
	}
	return emit.NewCrossbar(roster, ticks)
}

// payloadBytes is preserved here for the remaining in-backend emitters
// (currently emit_demos.go). The internal/emit package has its own
// copy so it stays free of any backend dependency. Both will collapse
// once every emitter has migrated.
func payloadBytes(evt bus.Event) []byte {
	if len(evt.Data) > 0 {
		return evt.Data
	}
	return evt.Raw
}
