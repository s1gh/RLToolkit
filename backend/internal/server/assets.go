package server

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
//go:embed web/dashboard.html web/overlay.html web/sdk.css web/overlay-editor.js
//go:embed web/fonts/*.woff2
//go:embed web/sdk/dist/sdk.js
var webFS embed.FS

// faviconSVG mirrors the dashboard's logo gradient so the browser tab
// and the dashboard's "RL" tile feel like the same brand.
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
// allocation+copy on every request.
//
// sdkJSBytes is rendered once at startup with the
// types.VerifiedStatfeedNames registry inlined where the source has
// the `/*__RLT_STATS__*/{}` placeholder. That gives plugins a
// synchronous `RLT.stats.DEMOLISH` while keeping the registry in one
// place (internal/types/statfeed.go).
var (
	sdkJSBytes      = renderSDKJS(mustReadEmbed("web/sdk/dist/sdk.js"))
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
// registry literal should land. Bundled SDK source has:
//
//	JSON.parse("__RLT_STATS_JSON__")
//
// We replace the inner double-quoted token (including quotes) with a
// JSON-encoded string of the same shape, so `JSON.parse(...)` at
// runtime yields the registry object.
//
// Why a string literal rather than the previous comment + empty-object
// placeholder? esbuild reformats comment+literal pairs, breaking
// byte-exact substitution. String literals come through bundlers
// verbatim.
var statsPlaceholder = []byte(`"__RLT_STATS_JSON__"`)

// renderSDKJS substitutes the types.VerifiedStatfeedNames registry
// into the SDK source at startup. Result is cached for the lifetime of
// the process (the registry is a compile-time constant; nothing
// changes at runtime). Panics if the placeholder isn't present — that
// would leave RLT.stats permanently empty, which is a build-time bug
// worth failing fast on.
func renderSDKJS(src []byte) []byte {
	if !bytes.Contains(src, statsPlaceholder) {
		panic("sdk.js missing " + string(statsPlaceholder) + " placeholder")
	}
	return bytes.Replace(src, statsPlaceholder, buildStatsJSONLiteral(), 1)
}

// buildStatsJSONLiteral renders types.VerifiedStatfeedNames as a
// JS-string-quoted JSON object: e.g. `'{"DEMOLISH":"Demolish",…}'`
// (single-quoted to avoid escaping the inner double quotes). The SDK
// passes this to `JSON.parse`, which yields the registry object.
// Sorted by key for deterministic byte output.
func buildStatsJSONLiteral() []byte {
	type entry struct{ key, val string }
	rows := make([]entry, 0, len(types.VerifiedStatfeedNames))
	for name := range types.VerifiedStatfeedNames {
		rows = append(rows, entry{key: pascalToScreamingSnake(name), val: name})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].key < rows[j].key })

	var b bytes.Buffer
	b.WriteByte('\'')
	b.WriteByte('{')
	for i, r := range rows {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('"')
		b.WriteString(r.key)
		b.WriteString(`":"`)
		b.WriteString(r.val)
		b.WriteByte('"')
	}
	b.WriteByte('}')
	b.WriteByte('\'')
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
			boundary := isUpper && prevIsLower
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
