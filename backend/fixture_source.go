package backend

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"rl-toolkit/internal/bus"
	"rl-toolkit/internal/wire"
)

// FixtureSource replays a JSONL file as a stream of Events. One JSON
// envelope per line. Used by tests to drive the pipeline with
// hand-crafted match recordings.
type FixtureSource struct {
	Path string
}

func (s *FixtureSource) Events(ctx context.Context) <-chan bus.Event {
	ch := make(chan bus.Event, 64)
	go func() {
		defer close(ch)
		f, err := os.Open(s.Path)
		if err != nil {
			return
		}
		defer f.Close()
		scanner := bufio.NewScanner(f)
		// Default token size is 64KB which is plenty for a single
		// envelope, but UpdateState payloads can be large; bump to 1MB
		// just to be safe.
		buf := make([]byte, 0, 64*1024)
		scanner.Buffer(buf, 1024*1024)
		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}
			raw := make([]byte, len(line))
			copy(raw, line)
			name := wire.ExtractEventName(raw)
			var env struct {
				Data      json.RawMessage `json:"Data,omitempty"`
				DataLower json.RawMessage `json:"data,omitempty"`
			}
			_ = json.Unmarshal(raw, &env)
			data := env.Data
			if len(data) == 0 {
				data = env.DataLower
			}
			select {
			case <-ctx.Done():
				return
			case ch <- bus.Event{Name: name, Data: data, Raw: raw}:
			}
		}
	}()
	return ch
}
