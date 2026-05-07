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

  const repaint = () => {
    const body = document.body;
    if (!body) return;
    body.style.display = focusOK && phaseOK ? 'flex' : 'none';
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
