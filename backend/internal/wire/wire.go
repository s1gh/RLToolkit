// Package wire decodes the RL Stats API envelope: pulling event
// names, MatchGuid values, and bReplay flags out of raw JSON without
// allocating the multi-KB inner Data payload.
//
// The canonical-name lookup table lives outside this package because
// it's derived from the event catalog (which is part of the backend
// package). Call RegisterAliases at init time to seed it; until that
// happens, Canonical falls back to returning the input unchanged.
package wire

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync/atomic"
)

var aliasTable atomic.Pointer[map[string]string]

// RegisterAliases installs the lowercase-to-PascalCase lookup table
// used by Canonical. Safe to call multiple times — last write wins.
// In practice the backend calls it exactly once during package init.
func RegisterAliases(m map[string]string) {
	cp := make(map[string]string, len(m))
	for k, v := range m {
		cp[strings.ToLower(k)] = v
	}
	aliasTable.Store(&cp)
}

// ExtractEventName pulls the event name from an RL-shaped JSON
// envelope. Returns "" if absent or malformed. Accepts both PascalCase
// ("Event") and lowercase ("event") keys; the value is normalized to
// canonical PascalCase via Canonical so downstream comparisons stay
// PascalCase. The Data field is skipped as a RawMessage to avoid
// allocating the multi-KB inner payload.
func ExtractEventName(raw []byte) string {
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
	return Canonical([]byte(name))
}

// Canonical maps an extracted name to its PascalCase form via the
// alias table. Unknown names fall through unchanged so newly-added RL
// events still flow.
func Canonical(b []byte) string {
	// Synthetic events ("_"-prefixed) are emitted in canonical form.
	if len(b) > 0 && b[0] == '_' {
		return string(b)
	}
	tablePtr := aliasTable.Load()
	if tablePtr == nil {
		return string(b)
	}
	table := *tablePtr
	if name, ok := table[string(b)]; ok {
		return name
	}
	lower := bytes.ToLower(b)
	if name, ok := table[string(lower)]; ok {
		return name
	}
	return string(b)
}

// bReplayTrueMarkers covers the casings/escape-forms of `"bReplay":true`
// we may see in an UpdateState payload. The flag lives inside the
// JSON-encoded Data string (quotes backslash-escaped) and casing varies
// by RL build. Allocation-free; runs on every UpdateState.
var bReplayTrueMarkers = [][]byte{
	[]byte(`\"bReplay\":true`),
	[]byte(`\"breplay\":true`),
	[]byte(`"bReplay":true`),
	[]byte(`"breplay":true`),
}

// ScanBReplay returns whether the UpdateState payload reports an
// active goal replay. Absence of any true-marker is treated as false.
func ScanBReplay(raw []byte) bool {
	for _, m := range bReplayTrueMarkers {
		if bytes.Contains(raw, m) {
			return true
		}
	}
	return false
}

// matchGuidMarkers covers the casings of `"MatchGuid":"…"`. Same
// escape/casing concerns as bReplayTrueMarkers; we try each in order
// and slice off whichever quote variant ends the value.
var matchGuidMarkers = [][]byte{
	[]byte(`\"MatchGuid\":\"`),
	[]byte(`\"matchguid\":\"`),
	[]byte(`"MatchGuid":"`),
	[]byte(`"matchguid":"`),
}

// ExtractMatchGUID pulls MatchGuid out of a payload without a full
// Unmarshal. Returns "" if absent or malformed.
func ExtractMatchGUID(raw []byte) string {
	for _, m := range matchGuidMarkers {
		i := bytes.Index(raw, m)
		if i < 0 {
			continue
		}
		rest := raw[i+len(m):]
		// Value ends at the next quote, escaped (\") if inside a
		// JSON-encoded string — take whichever terminator comes first.
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

// primaryIdMarkers covers the casings/escape-forms of the per-player
// `"PrimaryId":"…"` key inside UpdateState. One marker hit means one
// player on the roster. Allocation-free and tolerant of RL's varied
// escape conventions (Data may be a JSON-encoded string, in which
// case the inner quotes are backslash-escaped).
var primaryIdMarkers = [][]byte{
	[]byte(`\"PrimaryId\":\"`),
	[]byte(`\"primaryid\":\"`),
	[]byte(`"PrimaryId":"`),
	[]byte(`"primaryid":"`),
}

// CountPlayers returns the number of player entries in an UpdateState
// payload by counting `"PrimaryId"` occurrences. Each player block
// contains exactly one PrimaryId field, so the count maps 1:1 to
// roster size. Returns 0 if no marker matches (freeplay, lobby with
// no replication yet, or a malformed frame).
func CountPlayers(raw []byte) int {
	for _, m := range primaryIdMarkers {
		if bytes.Contains(raw, m) {
			return bytes.Count(raw, m)
		}
	}
	return 0
}

// GUIDFromData reads MatchGuid from a typed event's Data payload. RL
// ships Data as a JSON-encoded *string* containing JSON, hence the
// double decode.
func GUIDFromData(data json.RawMessage) string {
	if len(data) == 0 {
		return ""
	}
	var inner string
	if err := json.Unmarshal(data, &inner); err != nil {
		return ""
	}
	return ExtractMatchGUID([]byte(inner))
}
