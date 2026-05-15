import { state } from './state.js';
import { focus } from './focus.js';
import { overlayHideWhenUnfocused, overlayPhaseGate } from './overlay-mode.js';

// Overlay visibility gates: hide-when-unfocused and phase-gated
// rendering. Side-effect-only on import — wires bus subscriptions when
// the URL params requested gating, otherwise no-op.

if (overlayHideWhenUnfocused || overlayPhaseGate !== null) {
  let focusOK = !overlayHideWhenUnfocused; // not gating? always pass
  const phasePass = (p) => !overlayPhaseGate || overlayPhaseGate.has(p);
  let phaseOK = phasePass(state.phase);

  // Toggle visibility via opacity + visibility instead of display:none.
  // display:none collapses layout, which forces autoSize to remeasure
  // from scratch on un-gate AND surfaces incremental render churn as a
  // visible flicker (e.g. dejavu showing one team before the next
  // tick fills in the other). Keeping layout live but invisible lets
  // autoSize keep working in the background so the iframe is already
  // the right size when we fade in, and the 120ms fade absorbs any
  // per-event render churn that would otherwise be perceptible.
  // visibility:hidden in addition to opacity:0 ensures gated content
  // doesn't intercept clicks (overlays are click-through anyway, but
  // belt-and-braces).
  const repaint = () => {
    const body = document.body;
    if (!body) return;
    const show = focusOK && phaseOK;
    // Strip the !important boot-time gate style BEFORE writing inline
    // opacity. The gate uses !important so inline styles can't override
    // it; clearing it first lets the inline opacity:1 win and triggers
    // the CSS transition cleanly. Order matters here.
    if (show) {
      const gateStyle = document.getElementById('__rlt_gate_style');
      if (gateStyle) gateStyle.remove();
    }
    body.style.opacity = show ? '1' : '0';
    body.style.visibility = show ? 'visible' : 'hidden';
  };

  if (overlayHideWhenUnfocused) {
    focus.onChange((active) => {
      focusOK = !!active;
      repaint();
    });
  }
  if (overlayPhaseGate !== null) {
    state.onChange((newPhase) => {
      phaseOK = phasePass(newPhase);
      repaint();
    });
  }
  repaint();
}
