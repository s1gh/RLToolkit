package backend

import (
	"rl-toolkit/backend/internal/tick"
	"rl-toolkit/backend/internal/types"
)

// TickStore aliases internal/tick.Store. NewTickStore preserves the
// original constructor name. The cached snapshot types are also
// aliased here so emit/state code that names them keeps compiling.
type TickStore = tick.Store
type tickSnapshot = types.TickSnapshot
type tickPlayer = types.TickPlayer

func NewTickStore() *TickStore { return tick.New() }
