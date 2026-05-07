// Walk a synthetic-event payload and stamp `isMe` + `encounter` onto
// every EnrichedPlayer-shaped node we recognize. Called from the bus
// dispatcher for every `_`-prefixed envelope.
//
// Identity and encounters are passed in (rather than imported) to
// avoid a circular import — bus.js imports this module, identity.js
// imports bus.js, so a static import here would force esbuild to
// evaluate identity.js before bus.js is ready, blowing up identity's
// `bus.on(...)` at module init.
//
// index.js wires the dependencies via setStampDeps() once all modules
// have been evaluated, before any SSE event can fire.

let _identity = null;
let _encounters = null;

export function setStampDeps(identity, encounters) {
  _identity = identity;
  _encounters = encounters;
}

export function stampClientSideFields(node) {
  if (!node || typeof node !== 'object') return;
  if (Array.isArray(node)) {
    for (const item of node) stampClientSideFields(item);
    return;
  }
  if (
    typeof node.id === 'string' &&
    typeof node.name === 'string' &&
    typeof node.team === 'number' &&
    typeof node.isBot === 'boolean'
  ) {
    if (node.isMe === undefined || node.isMe === false) {
      node.isMe = _identity ? _identity._isMe(node.id) : false;
    }
    if (node.encounter === undefined || node.encounter === null) {
      const enc = _encounters && node.id ? _encounters.get(node.id) : null;
      if (enc) node.encounter = enc;
    }
  }
  for (const key of Object.keys(node)) {
    const v = node[key];
    if (v && typeof v === 'object') stampClientSideFields(v);
  }
}
