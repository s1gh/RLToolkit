// Helpers for the overlay aggregator's content-driven sizing.
//
// A manifest dimension is either an integer pixel count or the literal
// string "auto". The string travels in JSON straight from the Go side
// (see backend/internal/plugins/dimension.go) — this module is the only
// place the JS host has to know about the sentinel. Pixel values render
// as `<n>px`; auto dimensions are left blank in CSS so the iframe's
// intrinsic size or a postMessage-driven update can take over.

export function isAuto(v) {
  return v === 'auto';
}

// clamp applies optional min/max bounds. Either bound being 0 means
// "unset" — manifests omit min_/max_ and Go defaults the field to 0.
// With both unset the value passes through; with only one set the other
// side is unbounded.
export function clamp(value, min, max) {
  let out = value;
  if (max > 0 && out > max) out = max;
  if (min > 0 && out < min) out = min;
  return out;
}

// applyDimension writes <n>px when v is numeric, clears the inline style
// when v is "auto", and silently ignores anything else (defensive — a
// malformed manifest reaches us as the parsed JSON value, not a string
// we can validate). axis is 'width' or 'height'.
export function applyDimension(el, axis, v) {
  if (!el || (axis !== 'width' && axis !== 'height')) return;
  if (isAuto(v)) {
    el.style[axis] = '';
    return;
  }
  if (typeof v === 'number' && Number.isFinite(v)) {
    el.style[axis] = v + 'px';
    return;
  }
  if (typeof v === 'string') {
    const n = parseInt(v, 10);
    if (Number.isFinite(n)) el.style[axis] = n + 'px';
  }
}
