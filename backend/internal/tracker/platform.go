// Package tracker fetches Rocket League MMR from api.tracker.gg.
// See docs/superpowers/specs/2026-05-09-tracker-mmr-design.md.
package tracker

import (
	"errors"
	"strings"
)

// ErrUnsupportedPlatform indicates the input identity is not one of the
// platforms we know how to look up on tracker.gg (Epic, in particular,
// is keyed by display name and not supported in v1).
var ErrUnsupportedPlatform = errors.New("tracker: unsupported platform")

// platformPrefixToSlug maps the leading segment of an EnrichedPlayer.ID
// (e.g. "Steam|76561…|0") to a tracker.gg URL slug. Keys are matched
// case-insensitively.
var platformPrefixToSlug = map[string]string{
	"steam":   "steam",
	"sony":    "psn",
	"ps4":     "psn",
	"ps5":     "psn",
	"xboxone": "xbl",
	"xbox":    "xbl",
	"nswitch": "switch",
	"switch":  "switch",
}

// ResolveSlug parses an "Platform|UserId|SubId" identity string into a
// tracker.gg slug + the user id segment. Returns ErrUnsupportedPlatform
// for unknown prefixes (notably Epic) or malformed input.
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
	case "steam", "psn", "xbl", "switch":
		return true
	}
	return false
}
