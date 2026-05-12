package server

import (
	"bytes"
	"embed"
	"rl-toolkit/backend/internal/types"
	"sort"
	"strings"
)

// webFS holds the dashboard, overlay, SDK, and font assets, served
// straight from the binary so runtime doesn't depend on cwd. Fonts
// are self-hosted to avoid the network round-trip and font-swap reflow.
//
//go:embed web/dashboard.html web/overlay.html web/sdk.css web/overlay-editor.js web/overlay-live-edit.js web/overlay-size.js
//go:embed web/fonts/*.woff2
//go:embed web/sdk/dist/sdk.js
var webFS embed.FS

// faviconSVG mirrors the dashboard's logo gradient.
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

// Precomputed byte slices for assets served verbatim. sdkJSBytes is
// rendered once at startup with types.VerifiedStatfeedNames inlined
// where sdk.js carries a placeholder, so plugins get a synchronous
// `RLT.stats.DEMOLISH` while the registry stays in one place
// (internal/types/statfeed.go).
var (
	sdkJSBytes      = renderSDKJS(mustReadEmbed("web/sdk/dist/sdk.js"))
	sdkCSSBytes     = mustReadEmbed("web/sdk.css")
	faviconSVGBytes = []byte(faviconSVG)
	dashboardHTML   = mustReadEmbed("web/dashboard.html")
	overlayHTML     = mustReadEmbed("web/overlay.html")
	overlayEditorJS   = mustReadEmbed("web/overlay-editor.js")
	overlayLiveEditJS = mustReadEmbed("web/overlay-live-edit.js")
	overlaySizeJS     = mustReadEmbed("web/overlay-size.js")
)

func mustReadEmbed(path string) []byte {
	data, err := webFS.ReadFile(path)
	if err != nil {
		panic("embedded asset missing: " + path)
	}
	return data
}

// statsPlaceholder marks where the registry literal lands in the
// bundled SDK source: `JSON.parse("__RLT_STATS_JSON__")`. We swap the
// quoted token for a JSON-encoded registry string so JSON.parse yields
// the object at runtime. A string literal (rather than a comment +
// empty object) survives bundler reformatting.
var statsPlaceholder = []byte(`"__RLT_STATS_JSON__"`)

// renderSDKJS substitutes types.VerifiedStatfeedNames into the SDK
// source at startup. Panics on a missing placeholder — that would
// leave RLT.stats permanently empty, a build-time bug worth failing
// fast on.
func renderSDKJS(src []byte) []byte {
	if !bytes.Contains(src, statsPlaceholder) {
		panic("sdk.js missing " + string(statsPlaceholder) + " placeholder")
	}
	return bytes.Replace(src, statsPlaceholder, buildStatsJSONLiteral(), 1)
}

// buildStatsJSONLiteral renders types.VerifiedStatfeedNames as a
// single-quoted JSON object literal (e.g. `'{"DEMOLISH":"Demolish",…}'`)
// for the SDK to JSON.parse. Sorted by key for deterministic output.
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

// pascalToScreamingSnake converts PascalCase to SCREAMING_SNAKE_CASE.
// Inserts underscores between lowercase→uppercase transitions and
// between an acronym and a following capitalized word (MVPGoal →
// MVP_GOAL). Pure uppercase runs (MVP) stay intact.
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
