package backend

import (
	"rl-toolkit/backend/internal/types"
)

// ShortcutRef and EnrichedPlayer alias internal/types so existing
// in-backend callers keep compiling. The resolver methods themselves
// (ResolveByShortcut, ResolveByPrimaryId) live on roster.Tracker in
// internal/roster.
type ShortcutRef = types.ShortcutRef
type EnrichedPlayer = types.EnrichedPlayer
