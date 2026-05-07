package backend

import (
	"bytes"
	"embed"
	"rl-toolkit/backend/internal/types"
	"sort"
	"strings"
)

// webFS holds the dashboard, overlay, and SDK assets, served straight
// from the binary so we don't depend on the cwd at runtime.
//
// Fonts are bundled too: the overlay used to fetch Saira Condensed,
// Inter, and JetBrains Mono from fonts.googleapis.com on every launch,
// which costs a network round-trip and triggers a post-load reflow as
// the web font swaps in for the system fallback. Self-hosting kills
// both costs.
//
//go:embed web/dashboard.html web/overlay.html web/sdk.js web/sdk.css web/overlay-editor.js
//go:embed web/fonts/*.woff2
var webFS embed.FS

// faviconSVG mirrors the dashboard's logo gradient so the browser tab and
// the dashboard's "RL" tile feel like the same brand.
const faviconSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32">
  <defs>
    <linearGradient id="g" x1="0" y1="0" x2="32" y2="32" gradientUnits="userSpaceOnUse">
      <stop offset="0" stop-color="#22d3ee"/>
      <stop offset="1" stop-color="#a78bfa"/>
    </linearGradient>
  </defs>
  <rect width="32" height="32" rx="8" fill="url(#g)"/>
  <text x="50%" y="55%" text-anchor="middle" dominant-baseline="middle"
        font-family="system-ui,sans-serif" font-weight="800" font-size="14"
        fill="#0a0c14" letter-spacing="-0.5">RL</text>
</svg>`

// Precomputed byte slices for assets we serve verbatim. Avoids a fresh
// allocation+copy on every request. The sdkJSBytes / sdkCSSBytes names
// are preserved so server.go's handlers don't need to change.
//
// sdkJSBytes is rendered once at startup with the types.VerifiedStatfeedNames
// registry inlined where the source has the `/*__RLT_STATS__*/{}`
// placeholder. That gives plugins a synchronous `RLT.stats.DEMOLISH`
// while keeping the registry in one place (statfeed_discoveries.go).
var (
	sdkJSBytes      = renderSDKJS(mustReadEmbed("web/sdk.js"))
	sdkCSSBytes     = mustReadEmbed("web/sdk.css")
	faviconSVGBytes = []byte(faviconSVG)
	dashboardHTML   = mustReadEmbed("web/dashboard.html")
	overlayHTML     = mustReadEmbed("web/overlay.html")
	overlayEditorJS = mustReadEmbed("web/overlay-editor.js")
)

func mustReadEmbed(path string) []byte {
	data, err := webFS.ReadFile(path)
	if err != nil {
		panic("embedded asset missing: " + path)
	}
	return data
}

// statsPlaceholder is the marker the SDK source carries where the
// registry literal should land. Kept distinctive (a JS comment +
// empty-object literal) so it's a no-op if substitution somehow
// fails — the SDK still parses, plugins just see RLT.stats == {}.
var statsPlaceholder = []byte("/*__RLT_STATS__*/ {}")

// renderSDKJS substitutes the types.VerifiedStatfeedNames registry into the
// SDK source at startup. Result is cached for the lifetime of the
// process (the registry is a compile-time constant; nothing changes
// at runtime). Panics if the placeholder isn't present — that would
// leave RLT.stats permanently empty, which is a build-time bug worth
// failing fast on.
func renderSDKJS(src []byte) []byte {
	if !bytes.Contains(src, statsPlaceholder) {
		panic("sdk.js missing " + string(statsPlaceholder) + " placeholder")
	}
	return bytes.Replace(src, statsPlaceholder, buildStatsLiteral(), 1)
}

// buildStatsLiteral renders types.VerifiedStatfeedNames as a JS object
// literal with SCREAMING_SNAKE keys mapping to the original
// PascalCase names. Sorted by key for deterministic byte output.
func buildStatsLiteral() []byte {
	type entry struct{ key, val string }
	rows := make([]entry, 0, len(types.VerifiedStatfeedNames))
	for name := range types.VerifiedStatfeedNames {
		rows = append(rows, entry{key: pascalToScreamingSnake(name), val: name})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].key < rows[j].key })

	var b bytes.Buffer
	b.WriteByte('{')
	for i, r := range rows {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(r.key)
		b.WriteString(`:"`)
		b.WriteString(r.val)
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.Bytes()
}

// pascalToScreamingSnake converts "AerialGoal" → "AERIAL_GOAL",
// "Demolish" → "DEMOLISH", "HoopsSwishGoal" → "HOOPS_SWISH_GOAL",
// "MVP" → "MVP". The rule: insert an underscore between a lowercase
// letter and an uppercase letter (the boundary between words), and
// between an acronym and a following capitalized word (e.g. "MVPGoal"
// → "MVP_GOAL"). Pure runs of uppercase ("MVP") stay intact.
func pascalToScreamingSnake(s string) string {
	runes := []rune(s)
	var b strings.Builder
	b.Grow(len(s) + 4)
	for i, r := range runes {
		if i > 0 {
			prev := runes[i-1]
			isUpper := r >= 'A' && r <= 'Z'
			prevIsLower := prev >= 'a' && prev <= 'z'
			// "aerialG" → "aerial_G": lowercase followed by uppercase.
			boundary := isUpper && prevIsLower
			// "MVPGoal" → "MVP_Goal": acronym ending where the next
			// uppercase starts a new capitalized word (i.e. is followed
			// by a lowercase letter). Without this we'd emit "MVPGOAL".
			if isUpper && !prevIsLower && i+1 < len(runes) {
				next := runes[i+1]
				if next >= 'a' && next <= 'z' {
					boundary = true
				}
			}
			if boundary {
				b.WriteByte('_')
			}
		}
		b.WriteRune(r)
	}
	return strings.ToUpper(b.String())
}
