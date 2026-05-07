package bus

import "encoding/json"

// Event is the unit of data flowing through the pipeline. Decoded
// envelope, raw payload kept for processors that want to skip
// per-field decoding.
type Event struct {
	// Name is the canonical PascalCase event name. Synthetic events
	// produced by processors carry the "_"-prefixed convention
	// (_MatchState, _GoalScored, ...).
	Name string
	// Data is the event-specific payload. For RL events, it's the
	// "Data" field of the envelope. For synthetic events, it's the
	// marshaled struct the emitting processor produced.
	Data json.RawMessage
	// Raw is the original wire bytes. Present for events read from a
	// Source; nil for synthetic events emitted by processors.
	Raw []byte
}
