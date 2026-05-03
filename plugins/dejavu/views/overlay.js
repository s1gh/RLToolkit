// Compact "recon HUD" rendered when the page is loaded with ?overlay=1.
// One row per other player; the standout (if any) gets the gold pulse —
// everyone else with encounterCount > 1 just gets the DÉJÀ VU tag.
window.DV = window.DV || {};

DV.overlay = (function () {
  const $ = DV.dom.$;
  const classifyReturners = DV.classifyReturners;

  // The render path runs on every onTick (~60Hz), but the visible content
  // is purely a function of roster + counts. Skipping no-op rebuilds keeps
  // the staggered fade-in from restarting every frame.
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

    // Standout candidates: other humans only. Bots and self are excluded
    // upstream (bots: meaningless recognition signal; self: filtered per
    // team below). standoutId is null when no one clearly stands out.
    const candidates = m.players.filter((p) => !p.isMe && !p.isBot);
    const { standoutId } = classifyReturners(candidates);

    // Fingerprint includes the standout id so a re-classification (e.g.
    // a 7-encounter player joins late and overtakes the previous top)
    // forces a repaint.
    const fp =
      (standoutId || '-') +
      '|' +
      m.players
        .map((p) => p.id + ':' + p.team + ':' + p.encounterCount + ':' + p.name + ':' + p.platform)
        .join('|');
    if (fp === lastFp) return;
    lastFp = fp;

    // Global stagger index across both teams so rows fade in sequentially.
    let i = 0;
    const teamHTML = (players, team) => {
      const list = players.filter((p) => !p.isMe);
      if (!list.length) return '';
      return (
        '<div class="ov-team ' +
        team +
        '">' +
        '<div class="ov-tname">' +
        team +
        '</div>' +
        list.map((p) => row(p, i++, p.id === standoutId)).join('') +
        '</div>'
      );
    };

    const html = teamHTML(m.blue, 'blue') + teamHTML(m.orange, 'orange');
    body.innerHTML = html || '<div class="ov-empty">solo session</div>';
  }

  function row(p, idx, isStandout) {
    const cls = ['ov-row'];
    if (p.isBot) cls.push('is-bot');
    if (!p.isBot && p.encounterCount > 1) cls.push('returning');
    if (isStandout) cls.push('returning-top');

    // Most-recent prior alias (current name excluded SDK-side). Surfaces
    // the "wait, that's so-and-so" signal in the heat of a queue.
    const aliases =
      !p.isBot && p.aliases.length
        ? '<div class="ov-r-aliases"><span class="aka">aka</span>' +
          RLT.ui.esc(p.aliases.slice(-1)[0]) +
          '</div>'
        : '';

    // Encounter count: bare number for first-meet, "×N" for repeats. The
    // × is a real glyph (not a CSS pseudo) so it stays selectable and
    // shares the row's animation timing.
    const num = p.encounterCount > 1 ? '×' + p.encounterCount : p.encounterCount;

    // playerIcon resolves to the CPU/chip glyph for bots and the brand
    // icon for humans. The empty placeholder still exists for the rare
    // case of a human with no platform string — keeps the column width
    // stable across rows so the layout doesn't jitter.
    const icon = RLT.ui.playerIcon(p);
    const platformTitle = RLT.ui.escAttr(p.isBot ? 'Bot' : p.platform || 'Unknown');
    const platform = icon
      ? '<div class="ov-r-platform" title="' + platformTitle + '">' + icon + '</div>'
      : '<div class="ov-r-platform ov-r-platform-empty" title="' + platformTitle + '"></div>';

    return (
      '<div class="' +
      cls.join(' ') +
      '" style="--ov-i:' +
      idx +
      '">' +
      '<div class="ov-r-num">' +
      num +
      '</div>' +
      platform +
      '<div class="ov-r-name">' +
      RLT.ui.esc(p.name) +
      aliases +
      '</div>' +
      '</div>'
    );
  }

  return { render };
})();
