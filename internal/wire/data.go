package wire

import "encoding/json"

// UnwrapInnerData pulls the inner Data string out of an envelope,
// accepting both PascalCase and lowercase top-level keys. Returns ""
// on missing or malformed envelope. RL ships UpdateState's Game/Players
// payload as a JSON-encoded *string* under Data, so callers that want
// to inspect the inner shape need to peel one layer of quoting first.
func UnwrapInnerData(raw []byte) string {
	var env struct {
		Data    string `json:"Data"`
		DataLow string `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return ""
	}
	if env.Data != "" {
		return env.Data
	}
	return env.DataLow
}

// PickStr returns a if non-empty, otherwise b. Used to pick between
// PascalCase and lowercase JSON values.
func PickStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// PickFloat returns a if non-nil, otherwise b.
func PickFloat(a, b *float64) *float64 {
	if a != nil {
		return a
	}
	return b
}
