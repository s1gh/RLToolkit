package main

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
func extractEventName(raw []byte) string {
	head := raw
	if len(head) > 96 {
		head = head[:96]
	}
	for _, marker := range eventKeyMarkers {
		i := bytes.Index(head, marker)
		if i < 0 {
			continue
		}
		rest := raw[i+len(marker):]
		end := bytes.IndexByte(rest, '"')
		if end < 0 {
			return ""
		}
		return canonicalEventName(rest[:end])
	}
	return ""
}

var eventKeyMarkers = [][]byte{
	[]byte(`"Event":"`),
	[]byte(`"event":"`),
}

// canonicalEventName maps an extracted name to its PascalCase form.
func canonicalEventName(b []byte) string {
	if len(b) > 0 && b[0] == '_' {
		return string(b)
	}
	if name, ok := lowerToCanonical[string(b)]; ok {
		return name
	}
	lower := bytes.ToLower(b)
	if name, ok := lowerToCanonical[string(lower)]; ok {
		return name
	}
	return string(b)
}

var lowerToCanonical = func() map[string]string {
	m := make(map[string]string, len(EventCatalog)+2)
	for _, e := range EventCatalog {
		m[strings.ToLower(e.Name)] = e.Name
	}
	m["replaywillend"] = "GoalReplayWillEnd"
	return m
}()

// updateStateMarkers detect an UpdateState envelope by substring
// without a full unmarshal. Used on the lifecycle hot path.
var updateStateMarkers = [][]byte{
	[]byte(`"Event":"UpdateState"`),
	[]byte(`"event":"updatestate"`),
}

// bReplayTrueMarkers are the four casings/escape-forms of
// `"bReplay":true` we may see in an UpdateState payload. The flag
// lives inside the JSON-encoded Data string, so quotes appear
// backslash-escaped in the raw envelope bytes.
var bReplayTrueMarkers = [][]byte{
	[]byte(`\"bReplay\":true`),
	[]byte(`\"breplay\":true`),
	[]byte(`"bReplay":true`),
	[]byte(`"breplay":true`),
}

// scanBReplay returns whether the UpdateState payload reports an
// active goal replay.
func scanBReplay(raw []byte) bool {
	for _, m := range bReplayTrueMarkers {
		if bytes.Contains(raw, m) {
			return true
		}
	}
	return false
}

// matchGuidMarkers cover the four casings of `"MatchGuid":"…"`.
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
