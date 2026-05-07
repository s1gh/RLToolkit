package backend

import "rl-toolkit/internal/overrides"

// OverridesStore + OverlayOverride are aliased over internal/overrides
// so existing main/server call-sites and JSON struct types stay
// unchanged. NewOverridesStore is preserved as a thin shim.
type OverridesStore = overrides.Store
type OverlayOverride = overrides.Override

func NewOverridesStore(dir string) (*OverridesStore, error) {
	return overrides.New(dir)
}
