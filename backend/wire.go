package backend

import (
	"bytes"
	"encoding/json"
	"strings"
)

// extractEventName pulls the event name from an RL-shaped JSON
// envelope without a full Unmarshal. Returns "" if the field is
// absent or malformed.
//
// RL emits the envelope key + values in lowercase ("event":"goalscored")
// on this build of the Stats API; older docs use PascalCase
// ("Event":"GoalScored"). We accept both keys and normalize the value
// to canonical PascalCase via canonicalEventName so every downstream
// comparison stays PascalCase.
//
// Implementation: decode the envelope's two name keys via the JSON
// parser. The Data field is captured as a json.RawMessage and thrown
// away — no allocation for the multi-KB inner payload, and field
// order in the envelope no longer matters (a previous substring-scan
// implementation broke whenever Data appeared before Event).
func extractEventName(raw []byte) string {
	var env struct {
		EventPascal string `json:"Event"`
		EventLower  string `json:"event"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return ""
	}
	name := env.EventPascal
	if name == "" {
		name = env.EventLower
	}
	if name == "" {
		return ""
	}
	return canonicalEventName([]byte(name))
}

// canonicalEventName maps an extracted name to its PascalCase form. The
// fast path is a direct map lookup for known events (covers everything
// in catalog.go plus the synthetic _-prefixed events). Unknown names
// fall through unchanged so newly-added RL events still flow.
func canonicalEventName(b []byte) string {
	// Synthetic events from our own server (_Lifecycle, _ConnectionStatus)
	// never need normalization — we emit them in canonical form.
	if len(b) > 0 && b[0] == '_' {
		return string(b)
	}
	if name, ok := lowerToCanonical[string(b)]; ok {
		return name
	}
	// Common case on this RL build: input is already lowercase. Try the
	// lowered form before giving up so a name change in catalog.go
	// doesn't silently break things.
	lower := bytes.ToLower(b)
	if name, ok := lowerToCanonical[string(lower)]; ok {
		return name
	}
	return string(b)
}

// lowerToCanonical maps lowercased event names to their PascalCase form.
// Built from EventCatalog at init time so the two stay in sync, plus a
// few wire-level aliases where the RL Stats API uses a name that
// doesn't lowercase to the catalog form.
var lowerToCanonical = func() map[string]string {
	m := make(map[string]string, len(EventCatalog)+2)
	for _, e := range EventCatalog {
		m[strings.ToLower(e.Name)] = e.Name
	}
	// Observed on this RL build: the wire ships `replaywillend` (no
	// `goal` prefix). Treat it as the catalog's GoalReplayWillEnd so
	// plugins subscribing to the documented name still get events.
	m["replaywillend"] = "GoalReplayWillEnd"
	return m
}()

// bReplayTrueMarkers are the four casings/escape-forms of
// `"bReplay":true` we may see in an UpdateState payload. The flag
// lives inside the JSON-encoded Data string, so quotes appear
// backslash-escaped in the raw envelope bytes. Casing also varies
// (PascalCase vs lowercase) by RL build. This runs on every
// UpdateState so it must stay allocation-free — four bytes.Contains
// over a ~1KB payload is still sub-microsecond.
var bReplayTrueMarkers = [][]byte{
	[]byte(`\"bReplay\":true`),
	[]byte(`\"breplay\":true`),
	[]byte(`"bReplay":true`),
	[]byte(`"breplay":true`),
}

// scanBReplay returns whether the UpdateState payload reports an
// active goal replay. Absence of any true-marker is treated as false.
func scanBReplay(raw []byte) bool {
	for _, m := range bReplayTrueMarkers {
		if bytes.Contains(raw, m) {
			return true
		}
	}
	return false
}

// matchGuidMarkers cover the four casings of `"MatchGuid":"…"`. The
// field lives inside the JSON-encoded Data string, so quotes appear
// backslash-escaped in the raw envelope bytes. Casing also varies
// (PascalCase vs lowercase) by RL build. We try each marker in order
// and slice off whatever quote variant (escaped or plain) ends the
// value.
var matchGuidMarkers = [][]byte{
	[]byte(`\"MatchGuid\":\"`),
	[]byte(`\"matchguid\":\"`),
	[]byte(`"MatchGuid":"`),
	[]byte(`"matchguid":"`),
}

// extractMatchGUID pulls MatchGuid out of a payload without a full
// Unmarshal. Returns "" if absent or malformed.
func extractMatchGUID(raw []byte) string {
	for _, m := range matchGuidMarkers {
		i := bytes.Index(raw, m)
		if i < 0 {
			continue
		}
		rest := raw[i+len(m):]
		// Value ends at the next quote, which may be escaped (\") if
		// we're inside a JSON-encoded string. Look for whichever
		// terminator comes first.
		end := bytes.IndexByte(rest, '"')
		if esc := bytes.Index(rest, []byte(`\"`)); esc >= 0 && (end < 0 || esc < end) {
			end = esc
		}
		if end < 0 {
			return ""
		}
		return string(rest[:end])
	}
	return ""
}

// guidFromData reads MatchGuid from a typed event's Data payload. RL
// ships Data as a JSON-encoded *string* containing JSON, hence the
// double decode.
func guidFromData(data json.RawMessage) string {
	if len(data) == 0 {
		return ""
	}
	var inner string
	if err := json.Unmarshal(data, &inner); err != nil {
		return ""
	}
	return extractMatchGUID([]byte(inner))
}
