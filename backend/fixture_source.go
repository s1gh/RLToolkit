package backend

import (
	"rl-toolkit/backend/internal/source"
)

// FixtureSource aliases internal/source.Fixture for in-backend callers
// (tests, demos). The replayer itself lives in internal/source.
type FixtureSource = source.Fixture
