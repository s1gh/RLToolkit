package backend

import "rl-toolkit/internal/identity"

// IdentityStore + Identity alias internal/identity so server, main,
// roster_tracker, and player_resolver keep their existing references.
type IdentityStore = identity.Store
type Identity = identity.Identity

func NewIdentityStore(dataDir string) (*IdentityStore, error) {
	return identity.New(dataDir)
}
