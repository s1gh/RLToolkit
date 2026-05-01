// "Most met" leaderboard — top 10 opponents/teammates by encounter count.
// You are excluded from your own leaderboard. W/L is the user's record
// across all encounters with that player.
window.DV = window.DV || {};

DV.leaderboard = (function () {
  const $ = DV.dom.$;
  const TOP_N = 10;
  // RL ships every bot under one sentinel id, so the bot record is an
  // aggregate over every CPU we've played. Display it as "Bots" with a tag.
  const BOT_ID = 'Unknown|0|0';
  // Render is wired to onTick (~60Hz), but the leaderboard's content only
  // changes when the encounter ledger does. Skipping no-op rebuilds keeps
  // :hover state stable.
  let lastFp = '';

  function wlCell(wins, losses) {
    const w = wins   | 0;
    const l = losses | 0;
    if (w === 0 && l === 0) return '<div class="lb-wl empty">—</div>';
    return '<div class="lb-wl">'
      + '<span class="w">' + w + '</span>'
      + '<span class="sep">-</span>'
      + '<span class="l">' + l + '</span>'
      + '</div>';
  }

  function render() {
    const host = $('lb-host'); const cnt = $('lb-count');
    if (!host) return;
    const map = RLT.encounters.all();

    const all = Object.entries(map)
      // Exclude self and the bot aggregate — leaderboard tracks humans.
      // Bot data still persists in the ledger, just hidden from this view.
      .filter(([id]) => id !== RLT.me.id && id !== BOT_ID)
      .map(([id, e]) => {
        const isBot = id === BOT_ID;
        // Both human and bot rows show the most-recent name in the name
        // slot. Bots additionally surface every prior bot identity in the
        // alias line ("incl. Bandit · Hound · …"), so the BOT tag plus the
        // aliases make it obvious it's the aggregate AI bucket.
        return {
          id,
          isBot,
          name: e.names[e.names.length - 1] || (isBot ? 'Bot' : 'Unknown'),
          aliases: isBot ? (e.names ? e.names.slice(0, -1) : []) : e.names.slice(0, -1),
          count: e.count || 1,
          wins:   e.wins   | 0,
          losses: e.losses | 0,
          last: e.last_seen,
          platform: id.split('|')[0],
        };
      })
      .sort((a, b) => (b.count - a.count) || (new Date(b.last) - new Date(a.last)))
      .slice(0, TOP_N);

    // Include claim state in the fingerprint so claiming/unclaiming
    // forces a rebuild (claim button visibility changes per row).
    const unclaimed = !RLT.me.id;
    const fp = (unclaimed ? 'U|' : 'C|') + all.map((p) =>
      p.id + ':' + p.count + ':' + p.wins + ':' + p.losses + ':' + p.name + ':' + (p.last || '') + ':' + p.aliases.length
    ).join('|');
    if (fp === lastFp) return;
    lastFp = fp;

    // Toggle host-level class so the row grid gains an extra column for
    // the claim buttons only when they're actually rendered.
    host.classList.toggle('lb-host-claimable', unclaimed);

    if (cnt) cnt.textContent = all.length;

    if (all.length === 0) {
      host.innerHTML = '<div class="card-empty" style="padding:32px 16px">play a few matches and your most-met opponents will show up here</div>';
      return;
    }

    // Mark the top 5 non-bot rows with count > 1 as "returning" so they
    // get the gold left-accent. Bots are excluded from the highlight: the
    // accent is meant to flag humans you've actually played repeatedly,
    // not the aggregate AI bucket.
    let returningRank = 0;
    const RETURNING_TOP_N = 5;
    for (const p of all) {
      if (!p.isBot && p.count > 1 && returningRank < RETURNING_TOP_N) {
        p._isReturningTop = true;
        returningRank++;
      }
    }

    host.innerHTML = all.map((p) => {
      // Bot row shows the full roster of bot names (e.g. "Bandit · Hound · …");
      // human rows show prior aliases (last 2). Same slot, different content.
      const aliasLabel = p.isBot ? 'incl.' : 'also';
      const aliasItems = p.isBot ? p.aliases.slice(0, 4) : p.aliases.slice(-2);
      const aliases = aliasItems.length
        ? '<div class="lb-aliases">' + aliasLabel + ' ' + aliasItems.map(RLT.ui.esc).join(' · ') + '</div>'
        : '';
      const botTag = p.isBot ? '<span class="bot-tag">BOT</span>' : '';
      const rowCls = ['lb-row'];
      if (p._isReturningTop) rowCls.push('returning');
      if (p.isBot) rowCls.push('is-bot');

      const platform = (function () {
        const icon = RLT.ui.platformIcon(p.platform);
        const title = RLT.ui.escAttr(p.platform || 'Unknown');
        return icon
          ? '<div class="lb-platform" title="' + title + '">' + icon + '</div>'
          : '<div class="lb-platform lb-platform-empty" title="' + title + '"></div>';
      })();

      // Claim button: only when identity is unclaimed and the row isn't
      // a bot. Uses the same data-claim-id attribute the global handler
      // in identity.js listens for, so no extra wiring needed.
      const claim = (unclaimed && !p.isBot)
        ? '<button class="claim-btn lb-claim" data-claim-id="' + RLT.ui.escAttr(p.id) + '" title="Claim as me">+</button>'
        : '';

      return '<div class="' + rowCls.join(' ') + '">' +
        '<div class="lb-count">' + p.count + '<span class="x">×</span></div>' +
        '<div class="lb-info">' +
          '<div class="lb-name">' + RLT.ui.esc(p.name) + botTag + '</div>' +
          aliases +
        '</div>' +
        platform +
        wlCell(p.wins, p.losses) +
        '<div class="lb-time">' + RLT.ui.timeAgo(p.last) + '</div>' +
        claim +
      '</div>';
    }).join('');
  }

  return { render };
})();
