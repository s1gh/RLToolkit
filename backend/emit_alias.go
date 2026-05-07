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

func NewFastestShotEmitter() *FastestShotEmitter { return emit.NewFastestShot() }
func NewFirstBloodEmitter() *FirstBloodEmitter   { return emit.NewFirstBlood() }

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
