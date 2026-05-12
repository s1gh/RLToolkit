package plugins

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Dimension is a manifest size value that can be either a fixed pixel
// count or the literal string "auto" (content-driven sizing). The host
// reads Auto to decide whether to enable content-fit on the iframe; Px
// supplies the initial/fallback size for clients that don't honor auto.
type Dimension struct {
	Auto bool
	Px   int
}

func (d *Dimension) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		*d = Dimension{}
		return nil
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return err
		}
		if s != "auto" {
			return fmt.Errorf("invalid dimension %q: only \"auto\" is accepted as a string value", s)
		}
		*d = Dimension{Auto: true}
		return nil
	}
	var n int
	if err := json.Unmarshal(trimmed, &n); err != nil {
		return fmt.Errorf("invalid dimension: %w", err)
	}
	if n < 0 {
		return fmt.Errorf("invalid dimension: %d is negative", n)
	}
	*d = Dimension{Px: n}
	return nil
}

func (d Dimension) MarshalJSON() ([]byte, error) {
	if d.Auto {
		return []byte(`"auto"`), nil
	}
	return json.Marshal(d.Px)
}
