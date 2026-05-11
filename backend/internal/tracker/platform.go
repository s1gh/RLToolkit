// Package tracker fetches Rocket League MMR from api.tracker.gg.
// See docs/superpowers/specs/2026-05-09-tracker-mmr-design.md.
package tracker

import (
	"errors"
	"strings"
)

// ErrUnsupportedPlatform indicates the input identity is not one of the
// platforms we know how to look up on tracker.gg.
var ErrUnsupportedPlatform = errors.New("tracker: unsupported platform")

// platformPrefixToSlug maps the leading segment of an EnrichedPlayer.ID
// (e.g. "Steam|76561…|0") to a tracker.gg URL slug. Keys are matched
// case-insensitively. Epic is keyed by display name on tracker.gg
// rather than account ID, but the second segment of an Epic identity
// already carries the display name so the mapping is straight
// passthrough.
var platformPrefixToSlug = map[string]string{
	"steam":   "steam",
	"sony":    "psn",
	"ps4":     "psn",
	"ps5":     "psn",
	"xboxone": "xbl",
	"xbox":    "xbl",
	"nswitch": "switch",
	"switch":  "switch",
	"epic":    "epic",
}

// ResolveSlug parses an "Platform|UserId|SubId" identity string into a
// tracker.gg slug + the user id segment. Returns ErrUnsupportedPlatform
// for unknown prefixes or malformed input.
func ResolveSlug(input string) (slug, id string, err error) {
	if input == "" {
		return "", "", ErrUnsupportedPlatform
	}
	parts := strings.Split(input, "|")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", ErrUnsupportedPlatform
	}
	slug, ok := platformPrefixToSlug[strings.ToLower(parts[0])]
	if !ok {
		return "", "", ErrUnsupportedPlatform
	}
	return slug, parts[1], nil
}

// ValidExplicitSlug reports whether s is a slug we accept on the
// explicit /api/mmr/{platform}/{id} route, comparing case-insensitively.
func ValidExplicitSlug(s string) bool {
	switch strings.ToLower(s) {
	case "steam", "psn", "xbl", "switch", "epic":
		return true
	}
	return false
}
