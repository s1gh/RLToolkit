package main

import "embed"

// webFS holds the dashboard + composite overlay HTML, served straight
// from the binary so we don't depend on the cwd at runtime.
//
//go:embed web/dashboard.html web/overlay.html
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
// allocation+copy on every request.
var (
	sdkJSBytes      = []byte(sdkJS)
	sdkCSSBytes     = []byte(sdkCSS)
	faviconSVGBytes = []byte(faviconSVG)
	dashboardHTML   = mustReadEmbed("web/dashboard.html")
	overlayHTML     = mustReadEmbed("web/overlay.html")
)

func mustReadEmbed(path string) []byte {
	data, err := webFS.ReadFile(path)
	if err != nil {
		panic("embedded asset missing: " + path)
	}
	return data
}
