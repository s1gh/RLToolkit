package backend

import "rl-toolkit/backend/internal/datastore"

// DataStore is an alias over internal/datastore.Store. NewDataStore
// preserves the original constructor name so main.go's wiring stays
// unchanged.
type DataStore = datastore.Store

func NewDataStore(dir string) (*DataStore, error) {
	return datastore.New(dir)
}
