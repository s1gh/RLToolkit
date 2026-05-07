package backend

import "rl-toolkit/internal/bus"

// names returns the Name field of every event in evts. Shared across
// the emit-processor tests as a compact way to render expected /
// actual sequences in failure messages.
func names(evts []bus.Event) []string {
	out := make([]string, len(evts))
	for i, e := range evts {
		out[i] = e.Name
	}
	return out
}
