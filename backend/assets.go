package main

import "embed"

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
var (
	sdkJSBytes      = mustReadEmbed("web/sdk.js")
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
