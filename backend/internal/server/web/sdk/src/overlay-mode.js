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
    // Gate body via opacity + visibility, NOT display. display:none
    // collapses layout, which means the autoSize ResizeObserver sees
    // 0x0 while gated and (worse) the first paint after un-gating
    // shows a frame of partial content while incremental renders
    // catch up — that flicker is what users perceived as "wrong team
    // flashing". Keeping layout live but invisible lets autoSize keep
    // measuring (so the iframe is right-sized the moment we un-gate)
    // and the fade-in covers any per-event render churn.
    const s = document.createElement('style');
    s.textContent =
      'body{opacity:0!important;visibility:hidden!important;' +
      'transition:opacity 120ms ease-out!important}';
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
    // Manifest-driven auto axes. When an axis is "auto" the iframe is
    // sized by the SDK's autoSize loop measuring body's content, so
    // body must NOT pin itself to the iframe with width/height: 100%
    // on that axis — that would collapse body to whatever the iframe
    // currently is (initially nothing), feeding back zero into autoSize
    // and locking the widget tiny. Fixed axes keep the 100% stretch so
    // the manifest's dimensions describe the visible widget.
    const autoWidth = params.has('auto_width');
    const autoHeight = params.has('auto_height');
    const apply = () => {
      const html = document.documentElement;
      const body = document.body;
      if (!body) return;
      body.classList.add('overlay-mode');
      if (autoWidth) body.classList.add('rlt-auto-width');
      if (autoHeight) body.classList.add('rlt-auto-height');
      html.style.margin = '0';
      html.style.padding = '0';
      html.style.height = '100%';
      // Overlays are click-through and run inside an iframe sized to
      // the manifest's declared bounds. A scrollbar would visibly
      // appear over the game and isn't useful even if it could be
      // clicked. Clip any content overflow at the html level so a
      // too-tall fixed-size plugin renders flush with its declared
      // size and truncates rather than showing a chrome scrollbar.
      // Body-level overflow stays visible on auto axes so the autoSize
      // observer sees the content's true bounding rect.
      //
      // On auto axes, leave html overflow visible. Chromium/WebView2
      // clamps a descendant's scrollHeight/scrollWidth to the nearest
      // overflow:hidden ancestor — so html{overflow:hidden} plus
      // body{height:auto} reports body.scrollHeight pinned to the
      // current iframe viewport. The autoSize observer then sees no
      // delta when content grows and the iframe stops expanding past
      // its initial size. Auto-axis plugins don't need the scrollbar
      // suppression anyway because the iframe is being sized to fit.
      html.style.overflow = (autoWidth || autoHeight) ? 'visible' : 'hidden';
      body.style.margin = '0';
      body.style.padding = '0';
      if (autoHeight) {
        body.style.minHeight = '0';
        body.style.height = 'auto';
      } else {
        body.style.minHeight = '100%';
        body.style.height = '100%';
      }
      if (autoWidth) {
        body.style.width = 'max-content';
      } else {
        body.style.width = '100%';
      }
      body.style.overflow = autoWidth || autoHeight ? 'visible' : 'hidden';
      // Auto axes: use block layout so body sizes to its children's
      // natural intrinsic dimensions. A flex container with auto size
      // can shrink children below their content extent (default
      // flex-shrink: 1), which feeds a too-small height back into the
      // autoSize ResizeObserver and locks the iframe smaller than the
      // content. Anchor alignment is unnecessary in auto mode anyway:
      // the iframe IS the size of the content, there's no extra space
      // to justify within. Fixed-axis plugins keep flex so the existing
      // anchor-pin behavior is preserved.
      const useFlex = !autoWidth && !autoHeight;
      body.style.display = useFlex ? 'flex' : 'block';
      body.style.flexDirection = useFlex ? 'column' : '';
      body.style.alignItems = useFlex ? hAlign : '';
      body.style.justifyContent = useFlex ? vAlign : '';
      // Add the transition inline too (the !important gate-style
      // injected into <head> covers the initial gated state, but the
      // visibility module toggles opacity via inline style which has
      // higher precedence — so the transition needs to live there too
      // to actually animate).
      body.style.transition = 'opacity 120ms ease-out';
    };
    if (document.body) apply();
    else document.addEventListener('DOMContentLoaded', apply, { once: true });
  }
} catch (_) {
  // overlay-mode auto-sizing is best-effort. If URL parsing or style
  // assignment fails, the plugin's manual styles still apply.
}
