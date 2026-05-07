package backend

import (
	"encoding/json"
	"time"
)

// crossbarDebounceWindow suppresses repeat _CrossbarHit emissions
// when RL fires a burst (ball rolling along the goal frame). 500ms is
// long enough to absorb the burst, short enough that two genuinely
// distinct hits in a single play still both fire.
const crossbarDebounceWindow = 500 * time.Millisecond

// CrossbarEmitter republishes RL's CrossbarHit as _CrossbarHit with
// the BallLastTouch player resolved against the live roster, plus a
// debounce: RL fires bursts when the ball rolls along the goal frame,
// and we want one event per real impact.
//
// Phase gate: skipped during goal replays (RL still fires CrossbarHit
// on the cinematic camera bouncing the ball off the frame). Reads the
// replay flag from the shared TickStore.
//
// No mutex: emit processors run single-threaded inside Pipeline.Run.
type CrossbarEmitter struct {
	roster *RosterTracker
	ticks  *TickStore

	lastHit time.Time
}

func NewCrossbarEmitter(roster *RosterTracker, ticks *TickStore) *CrossbarEmitter {
	return &CrossbarEmitter{roster: roster, ticks: ticks}
}

func (e *CrossbarEmitter) Process(evt Event) []Event {
	if evt.Name != "CrossbarHit" {
		return nil
	}
	if e.ticks != nil && e.ticks.InReplay() {
		return nil
	}
	now := time.Now()
	if !e.lastHit.IsZero() && now.Sub(e.lastHit) < crossbarDebounceWindow {
		// Update the timestamp on drops so a long roll keeps suppressing.
		e.lastHit = now
		return nil
	}
	e.lastHit = now

	inner := unwrapInnerData(evt.Raw)
	if inner == "" {
		return nil
	}
	var d crossbarHitData
	if err := json.Unmarshal([]byte(inner), &d); err != nil {
		return nil
	}
	guid := pickStr(d.MatchGUID, d.MatchGUIDLow)
	speed := pickFloat(d.BallSpeed, d.BallSpeedLow)
	force := pickFloat(d.ImpactForce, d.ImpactForceLow)
	loc := d.BallLocation
	if loc == nil {
		loc = d.BallLocationLow
	}
	lastTouch := d.BallLastTouch
	if lastTouch == nil {
		lastTouch = d.BallLastTouchLow
	}

	out := struct {
		MatchGUID     string                 `json:"matchGuid,omitempty"`
		BallSpeed     *float64               `json:"ballSpeed,omitempty"`
		ImpactForce   *float64               `json:"impactForce,omitempty"`
		BallLocation  *vec3                  `json:"ballLocation,omitempty"`
		BallLastTouch *enrichedBallLastTouch `json:"ballLastTouch,omitempty"`
	}{
		MatchGUID:    guid,
		BallSpeed:    speed,
		ImpactForce:  force,
		BallLocation: loc,
	}
	if lastTouch != nil {
		ref := lastTouch.Player
		if ref == nil {
			ref = lastTouch.PlayerLow
		}
		sp := pickFloat(lastTouch.Speed, lastTouch.SpeedLow)
		var enrichedRef *EnrichedPlayer
		if ref != nil {
			enrichedRef = e.roster.ResolveByShortcut(*ref)
		}
		if enrichedRef != nil || sp != nil {
			out.BallLastTouch = &enrichedBallLastTouch{
				Player: enrichedRef,
				Speed:  sp,
			}
		}
	}
	body, err := json.Marshal(out)
	if err != nil {
		return nil
	}
	return []Event{{Name: "_CrossbarHit", Data: body}}
}
