import { urlParams, isOverlay } from './env.js';

// Overlay sizing + anchor honoring + body class toggle. Run once at
// boot.
//
// Only fires when the SDK is loaded as the overlay view (the script
// tag carries data-view="overlay"). The unified-overlay aggregator and
// the Tauri widget supervisor pass anchor/hide_when_unfocused/phases
// as URL params at mount time (parameterized runtime values the
// manifest can't provide on its own); we read them here.
//
// The exported gates (hideWhenUnfocused, phaseGate) are read later
// by visibility.js to wire the focus/phase repaint loop.

export let overlayHideWhenUnfocused = false;
export let overlayPhaseGate = null;

try {
  const params = urlParams;
  const inOverlay = isOverlay;
  const anchor = params.get('anchor') || 'top-left';
  overlayHideWhenUnfocused = inOverlay && params.has('hide_when_unfocused');
  const overlayGated = inOverlay && (params.has('hide_when_unfocused') || params.has('phases'));
  if (overlayGated) {
    const s = document.createElement('style');
    s.textContent = 'body{display:none!important}';
    s.id = '__rlt_gate_style';
    (document.head || document.documentElement).appendChild(s);
  }
  if (inOverlay && params.has('phases')) {
    const list = (params.get('phases') || '')
      .split(',')
      .map((s) => s.trim())
      .filter(Boolean);
    if (list.length) overlayPhaseGate = new Set(list);
  }
  if (inOverlay) {
    const vAlign = anchor.indexOf('bottom') >= 0 ? 'flex-end' : 'flex-start';
    const hAlign = anchor.indexOf('right') >= 0 ? 'flex-end' : 'flex-start';
    const apply = () => {
      const html = document.documentElement;
      const body = document.body;
      if (!body) return;
      body.classList.add('overlay-mode');
      html.style.margin = '0';
      html.style.padding = '0';
      html.style.height = '100%';
      body.style.margin = '0';
      body.style.padding = '0';
      body.style.minHeight = '100%';
      body.style.height = '100%';
      body.style.width = '100%';
      const gated = overlayHideWhenUnfocused || overlayPhaseGate !== null;
      body.style.display = gated ? 'none' : 'flex';
      body.style.flexDirection = 'column';
      body.style.alignItems = hAlign;
      body.style.justifyContent = vAlign;
      const gateStyle = document.getElementById('__rlt_gate_style');
      if (gateStyle) gateStyle.remove();
    };
    if (document.body) apply();
    else document.addEventListener('DOMContentLoaded', apply, { once: true });
  }
} catch (_) {
  // overlay-mode auto-sizing is best-effort. If URL parsing or style
  // assignment fails, the plugin's manual styles still apply.
}
