package types

// VerifiedStatfeedNames is the registry of StatfeedEvent.EventName
// values the toolkit knows about. Anything NOT in this set is treated
// as "discovered" — the StatfeedEmitter publishes _UnknownStatfeed and
// (optionally) the discovery store records first/last-seen + count.
//
// This is broader than the SDK's `RLT.stats` object (which exposes
// only the 10 most-used names as ergonomic constants for plugin
// authors). The two don't have to match: the SDK set is plugin-facing
// API surface, this set is the "we already know about this name"
// filter for the discovery channel.
var VerifiedStatfeedNames = map[string]struct{}{
	// Promoted variants — each has its own _-prefixed synthetic event
	// alongside the catch-all _StatfeedEvent.
	"Shot":       {},
	"HatTrick":   {},
	"Save":       {},
	"EpicSave":   {},
	"Demolish":   {},
	"FlipReset":  {},
	"Assist":     {},
	"Center":     {},
	"Clear":      {},
	"BicycleHit": {},
	// Goal modifiers — collected onto _GoalScored.modifiers but the
	// catch-all _StatfeedEvent also ships them, so they belong here.
	"AerialGoal":     {},
	"BackwardsGoal":  {},
	"BicycleGoal":    {},
	"LongGoal":       {},
	"TurtleGoal":     {},
	"OvertimeGoal":   {},
	"PoolShot":       {},
	"HoopsSwishGoal": {},
	// Stat events without a dedicated synthetic envelope. Catch-all
	// _StatfeedEvent still fires; we list them here so RL emitting
	// them doesn't trip _UnknownStatfeed.
	"Goal":       {},
	"Win":        {},
	"MVP":        {},
	"Playmaker":  {},
	"Savior":     {},
	"LowFive":    {},
	"HighFive":   {},
	"FirstTouch": {},
	"Demolition": {},
	"OwnGoal":    {},
}
