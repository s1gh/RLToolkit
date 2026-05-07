import { emitter } from './util.js';
import { bus } from './bus.js';

// Identity (backend is the source of truth).
//
// The backend persists "who am I" at data/identity.json and broadcasts
// _IdentityChanged on every Set/Clear. The frontend identity module is
// a thin cache + write-through wrapper:
//   - boot:         GET /api/identity → seed myId
//   - set/clear:    PUT/DELETE /api/identity, await response, apply
//   - live updates: bus echo (_IdentityChanged from a different tab,
//                   or from the dashboard claim button)
//
// Why HTTP-on-boot instead of relying on the SSE seeded frame?
// The SSE connection opens lazily (only after the first addEvent /
// plugin register), and the seeded-frame path has been brittle across
// the various `?events=__none__` / hosted-bus / iframe-mode
// combinations. A direct fetch on construction sidesteps all of that —
// one round-trip, deterministic.
//
// _isMe(id) stays synchronous against the cache so player-stamping
// keeps working during a disconnect.

export const identity = (function () {
  const ev = emitter();
  let myId = '';
  let loaded = false;
  let nameHint = '';

  function applySnapshot(snap) {
    const next = (snap && typeof snap.primaryId === 'string') ? snap.primaryId : '';
    const nextName = (snap && typeof snap.name === 'string') ? snap.name : '';
    if (next === myId && nextName === nameHint && loaded) return;
    myId = next;
    nameHint = nextName;
    loaded = true;
    ev.emit('change', myId);
  }

  bus.on('_IdentityChanged', applySnapshot);

  (async function bootHydrate() {
    try {
      const r = await fetch('/api/identity');
      if (r.ok) {
        const body = await r.json().catch(() => null);
        applySnapshot(body);
      } else {
        applySnapshot(null);
      }
    } catch {
      applySnapshot(null);
    }
  })();

  return {
    get id() {
      return myId;
    },
    isReady() {
      return loaded;
    },
    async set(id) {
      const trimmed = (id || '').trim();
      if (!trimmed) return this.clear();
      const r = await fetch('/api/identity', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ primaryId: trimmed, name: nameHint || '' }),
      });
      if (!r.ok) throw new Error('set identity: ' + r.status);
      applySnapshot({ primaryId: trimmed, name: nameHint || '' });
    },
    async clear() {
      const r = await fetch('/api/identity', { method: 'DELETE' });
      if (!r.ok && r.status !== 204) throw new Error('clear identity: ' + r.status);
      applySnapshot(null);
    },
    onChange(fn) {
      return ev.on('change', fn);
    },
    _isMe(id) {
      return !!id && id === myId;
    },
  };
})();
