package tracker

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// playlistNameToKey is the stable mapping documented in the spec. Any
// upstream playlist name not in this table is dropped silently. We
// only expose the three core ranked playlists plus Casual; the extra
// modes (Hoops, Rumble, Dropshot, Snowday) are intentionally dropped.
var playlistNameToKey = map[string]string{
	"Ranked Duel 1v1":     "1v1",
	"Ranked Doubles 2v2":  "2v2",
	"Ranked Standard 3v3": "3v3",
	"Casual":              "casual",
}

// upstreamProfile mirrors the subset of the tracker.gg response we care
// about. Anything outside `data.segments` is ignored.
type upstreamProfile struct {
	Data struct {
		Segments []upstreamSegment `json:"segments"`
	} `json:"data"`
}

type upstreamSegment struct {
	Type     string `json:"type"`
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Stats struct {
		Rating struct {
			Value int `json:"value"`
		} `json:"rating"`
		Tier struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		} `json:"tier"`
		Division struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		} `json:"division"`
		MatchesPlayed struct {
			Value int `json:"value"`
		} `json:"matchesPlayed"`
	} `json:"stats"`
}

// parseProfile turns a tracker.gg JSON body into a *Result. It rejects
// responses without a `data.segments` key (treated as upstream malformed)
// but accepts an empty segments array, which is a valid state for a new
// account that has no ranked history yet.
func parseProfile(body []byte, platform, id string, now time.Time) (*Result, error) {
	var up upstreamProfile
	if err := json.Unmarshal(body, &up); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if up.Data.Segments == nil {
		return nil, errors.New("missing data.segments")
	}
	out := &Result{
		Platform:  platform,
		PlayerID:  id,
		Playlists: map[string]Rating{},
		FetchedAt: now,
	}
	for _, seg := range up.Data.Segments {
		if seg.Type != "playlist" {
			continue
		}
		key, ok := playlistNameToKey[seg.Metadata.Name]
		if !ok {
			continue
		}
		out.Playlists[key] = Rating{
			MMR:      seg.Stats.Rating.Value,
			Tier:     seg.Stats.Tier.Metadata.Name,
			Division: seg.Stats.Division.Metadata.Name,
			Matches:  seg.Stats.MatchesPlayed.Value,
		}
	}
	return out, nil
}
