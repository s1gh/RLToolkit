// Compact "Recon HUD" overlay (?overlay=1).
// Renders a tag per non-self player; gold-accented for returning opponents.
// Auto-sizes — empty / small lobbies leave most of the iframe transparent.
window.DV = window.DV || {};

DV.overlay = (function () {
  const $ = DV.dom.$;
  // Same reason as the leaderboard view: render is wired to onTick (~60Hz)
  // but the overlay's content is purely a function of the roster +
  // encounter counts. Skipping no-op rebuilds prevents the staggered
  // fade-in animation from restarting every frame.
  let lastFp = '';

  function render() {
    const body = $('ov-body');
    if (!body) return;
    const m = RLT.match.current;

    if (!m) {
      if (lastFp === 'EMPTY') return;
      lastFp = 'EMPTY';
      body.innerHTML = '<div class="ov-empty">awaiting contact</div>';
      return;
    }

    const fp = m.players.map((p) => p.id + ':' + p.team + ':' + p.encounterCount + ':' + p.name).join('|');
    if (fp === lastFp) return;
    lastFp = fp;

    // i is a global stagger index across both teams so rows fade in sequentially.
    let i = 0;
    const team = (players, t) => {
      const list = players.filter((p) => !p.isMe);
      if (!list.length) return '';
      return '<div class="ov-team ' + t + '">' +
        '<div class="ov-tname">' + t + '</div>' +
        list.map((p) => {
          const idx = i++;
          const cls = ['ov-row'];
          if (p.encounterCount > 1) cls.push('returning');
          // Show only the most recent prior alias, if any. Aliases array
          // excludes the current name (SDK-side filter), so .slice(-1)[0]
          // is the previous handle — most useful "wait, that's so-and-so"
          // signal in the heat of a queue.
          const aliases = p.aliases.length
            ? '<div class="ov-r-aliases"><span class="aka">aka</span>' +
                RLT.ui.esc(p.aliases.slice(-1)[0]) +
              '</div>'
            : '';
          // Encounter count: bare number for first-meet (×1), explicit
          // ×N for repeats. The × is a glyph rather than a CSS pseudo so
          // it stays selectable/copy-pasteable and respects the same
          // animation timing as the number.
          const num = p.encounterCount > 1
            ? '×' + p.encounterCount
            : p.encounterCount;
          return '<div class="' + cls.join(' ') + '" style="--ov-i:' + idx + '">' +
            '<div class="ov-r-num">' + num + '</div>' +
            '<div class="ov-r-name">' + RLT.ui.esc(p.name) + aliases + '</div>' +
          '</div>';
        }).join('') +
      '</div>';
    };

    const html = team(m.blue, 'blue') + team(m.orange, 'orange');
    body.innerHTML = html || '<div class="ov-empty">solo session</div>';
  }

  return { render };
})();
