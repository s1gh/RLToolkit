// Tiny DOM helpers shared across views.
window.DV = window.DV || {};

DV.dom = {
  $(id) { return document.getElementById(id); },

  // Update textContent only when it changed — avoids needless reflows.
  setCell(root, sel, val) {
    const el = root.querySelector(sel);
    if (el && el.textContent !== val) el.textContent = val;
  },

  // Stable structural fingerprint of the current match — used by the diff
  // patcher in views/match.js to decide when to rebuild row scaffolding
  // vs. just patching numbers.
  scaffoldKey(m, myId) {
    if (!m) return 'EMPTY';
    return m.guid + '|' + myId + '|' +
      m.players.map((p) => p.id + ':' + p.team + ':' + (p.encounterCount > 1 ? 'r' : 'n')).join(',');
  },
};
