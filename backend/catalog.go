package backend

import "rl-toolkit/backend/internal/catalog"

// EventCatalogEntry / EventCatalog alias internal/catalog so existing
// in-backend callers (server.go's /api/events handler, wire_init.go's
// alias-table seeder) keep compiling.
type EventCatalogEntry = catalog.Entry

var EventCatalog = catalog.Entries
