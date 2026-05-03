// "Most met" leaderboard — top 10 humans by encounter count.
//
// Self and the aggregate-bot record are excluded; both still live in the
// ledger but they're not interesting on a "who do I keep running into"
// list. Rendering is fingerprint-gated so we only repaint when the
// underlying ledger actually changed (the SDK calls onTick at ~60Hz, but
// the leaderboard content only changes on `record`/`change`).
window.DV = window.DV || {};

DV.leaderboard = (function () {
  const $ = DV.dom.$;
  const { LEADERBOARD_TOP_N: TOP_N, LEADERBOARD_HIGHLIGHT_N: HIGHLIGHT_N } = DV.constants;
  let lastFp = '';

  function render() {
    const host = $('lb-host');
    const cnt = $('lb-count');
    if (!host) return;
    const map = RLT.encounters.all();

    const rows = Object.entries(map)
      .filter(([id]) => id !== RLT.me.id && !RLT.encounters.isBotId(id))
      .map(([id, e]) => ({
        id,
        name: e.names[e.names.length - 1] || 'Unknown',
        aliases: e.names.slice(0, -1), // every prior name; current excluded
        count: e.count || 1,
        last: e.last_seen,
        platform: id.split('|')[0],
      }))
      .sort((a, b) => b.count - a.count || new Date(b.last) - new Date(a.last))
      .slice(0, TOP_N);

    // Include claim state in the fingerprint so claim/unclaim transitions
    // force a rebuild (claim-button visibility changes per row).
    const unclaimed = !RLT.me.id;
    const fp =
      (unclaimed ? 'U|' : 'C|') +
      rows
        .map(
          (p) =>
            p.id + ':' + p.count + ':' + p.name + ':' + (p.last || '') + ':' + p.aliases.length,
        )
        .join('|');
    if (fp === lastFp) return;
    lastFp = fp;

    // Host-level class adds the claim-button column only when relevant.
    host.classList.toggle('lb-host-claimable', unclaimed);
    if (cnt) cnt.textContent = rows.length;

    if (rows.length === 0) {
      host.innerHTML =
        '<div class="card-empty" style="padding:32px 16px">play a few matches and your most-met opponents will show up here</div>';
      return;
    }

    // The top HIGHLIGHT_N humans with count > 1 get the gold left-bar.
    // This is the all-time "you keep running into these people" cue and
    // is intentionally distinct from the live-match standout glow — see
    // views/match.js + views/overlay.js for that one.
    let highlights = 0;
    for (const p of rows) {
      if (p.count > 1 && highlights < HIGHLIGHT_N) {
        p.highlight = true;
        highlights++;
      }
    }

    host.innerHTML = rows.map(renderRow.bind(null, unclaimed)).join('');
  }

  function renderRow(unclaimed, p) {
    const aliases = p.aliases.length
      ? '<div class="lb-aliases">also ' + p.aliases.slice(-2).map(RLT.ui.esc).join(' · ') + '</div>'
      : '';

    const cls = ['lb-row'];
    if (p.highlight) cls.push('returning');

    const icon = RLT.ui.platformIcon(p.platform);
    const platformTitle = RLT.ui.escAttr(p.platform || 'Unknown');
    const platform = icon
      ? '<div class="lb-platform" title="' + platformTitle + '">' + icon + '</div>'
      : '<div class="lb-platform lb-platform-empty" title="' + platformTitle + '"></div>';

    // Claim button when identity is unclaimed. Wired through the global
    // data-claim-id handler in views/identity.js — no extra binding here.
    const claim = unclaimed
      ? '<button class="claim-btn lb-claim" data-claim-id="' +
        RLT.ui.escAttr(p.id) +
        '" title="Claim as me">+</button>'
      : '';

    return (
      '<div class="' +
      cls.join(' ') +
      '">' +
      '<div class="lb-count">' +
      p.count +
      '<span class="x">×</span></div>' +
      '<div class="lb-info">' +
      '<div class="lb-name">' +
      RLT.ui.esc(p.name) +
      '</div>' +
      aliases +
      '</div>' +
      platform +
      '<div class="lb-time">' +
      RLT.ui.timeAgo(p.last) +
      '</div>' +
      claim +
      '</div>'
    );
  }

  return { render };
})();
