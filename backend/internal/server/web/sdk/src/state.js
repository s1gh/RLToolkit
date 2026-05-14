import { emitter } from './util.js';
import { bus } from './bus.js';

// Match state phase machine echo.
// Phases: none | lobby | countdown | live | paused | replay | ended | podium

export const state = (function () {
  const ev = emitter();
  const matchActiveEv = emitter();

  let phase = 'none';
  let prevPhase = 'none';
  let matchActive = false;
  let matchGUID = '';
  let since = null;
  let isFreeplay = false;

  function applySnapshot(snap) {
    const newPhase = String(snap.phase || 'none');
    const newActive = !!snap.matchActive;
    const newGUID = String(snap.matchGuid || '');
    const phaseChanged = newPhase !== phase;
    const activeChanged = newActive !== matchActive;

    if (phaseChanged) {
      prevPhase = phase;
      phase = newPhase;
    }
    matchActive = newActive;
    matchGUID = newGUID;
    since = snap.since || null;
    isFreeplay = !!snap.isFreeplay;

    if (phaseChanged) ev.emit('change', phase, prevPhase);
    if (activeChanged) matchActiveEv.emit('change', matchActive);
  }

  bus.on('_MatchState', (snap) => {
    if (snap) applySnapshot(snap);
  });

  bus.on('_status', (s) => {
    if (s !== 'disconnected') return;
    applySnapshot({ phase: 'none', matchActive: false, matchGuid: '', since: null });
  });

  return {
    get phase() {
      return phase;
    },
    get previous() {
      return prevPhase;
    },
    get matchActive() {
      return matchActive;
    },
    get guid() {
      return matchGUID;
    },
    get since() {
      return since;
    },
    get isFreeplay() {
      return isFreeplay;
    },
    onChange(fn) {
      return ev.on('change', fn);
    },
    onMatchActive(fn) {
      return matchActiveEv.on('change', fn);
    },
  };
})();
