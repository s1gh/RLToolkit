// Live-match player list. The plugin's reason for existing: at a glance,
// see which of these 5 other people are returning.
//
// Diff-patch render: rebuild scaffolding only when roster/teams/identity
// change. Otherwise patch only the encounter count cell.
window.DV = window.DV || {};

DV.match = (function () {
  const { $, setCell, scaffoldKey } = DV.dom;
  let lastKey = '';

  function render() {
    const host = $('match-host');
    if (!host) return;
    const m = RLT.match.current;

    const key = scaffoldKey(m, RLT.me.id);
    if (key !== lastKey) {
      lastKey = key;
      buildScaffold(host, m);
    }
    patchValues(host, m);
    updateBadge(m);
  }

  // The "offline | live | lobby | …" pill in the card header. Reflects
  // whether we're connected to RL, whether a match exists, and whether
  // its roster has populated yet.
  function updateBadge(m) {
    const badge = $('match-badge');
    if (!badge) return;
    let label = 'offline';
    if (RLT.status() === 'connected') {
      if (!m) label = 'idle';
      else if (!m.players || m.players.length === 0) label = 'lobby';
      else label = RLT.match.lifecycle?.phase || 'live';
    }
    if (badge.textContent !== label) badge.textContent = label;
  }

  function invalidate() { lastKey = ''; }

  function buildScaffold(host, m) {
    if (!m) {
      host.innerHTML = '<div class="card-empty"><div class="em-icon">◇</div>queue up — waiting for a match</div>';
      return;
    }
    const teamBlock = (players, team) => {
      if (!players.length) return '';
      return '<div class="team-block ' + team + '">'
        + '<div class="team-block-head"><span class="tdot"></span><span class="tname">' + team + '</span></div>'
        + players.map(playerRow).join('')
        + '</div>';
    };
    const inner = teamBlock(m.blue, 'blue') + teamBlock(m.orange, 'orange');
    // Match exists but no players have populated yet — common while sitting
    // in a lobby waiting for opponents to load. Show an empty state instead
    // of a blank panel.
    if (!inner) {
      host.innerHTML = '<div class="card-empty"><div class="em-icon">◇</div>waiting for players…</div>';
      return;
    }
    host.innerHTML = '<div class="teams-stack">' + inner + '</div>';
  }

  // RL ships every bot under a single sentinel id, so flag them visually
  // and skip irrelevant chrome (claim button, alias spam from other bots).
  const BOT_ID = 'Unknown|0|0';

  function playerRow(p) {
    const isBot = p.id === BOT_ID;
    const cls = ['player-row'];
    if (p.isMe) cls.push('is-me');
    if (isBot) cls.push('is-bot');
    if (!isBot && p.encounterCount > 1) cls.push('returning');

    // Aliases come from the shared bot record, so they're other bot names —
    // not useful as "aka" context for this specific bot.
    const aliases = (!isBot && p.aliases.length)
      ? '<div class="pr-aliases">aka ' + p.aliases.slice(-2).map(RLT.ui.esc).join(' · ') + '</div>'
      : '';
    const youTag = p.isMe ? '<span class="you-tag">YOU</span>' : '';
    const botTag = isBot ? '<span class="bot-tag">BOT</span>' : '';
    // Bots can't be "you", so don't offer the claim affordance.
    const claim = (p.id && !p.isMe && !isBot)
      ? '<button class="claim-btn" data-claim-id="' + RLT.ui.escAttr(p.id) + '">this is me</button>'
      : '<span></span>';

    const icon = RLT.ui.platformIcon(p.platform);
    const platform = icon
      ? '<span class="pr-platform" title="' + RLT.ui.escAttr(p.platform) + '">' + icon + '</span>'
      : '<span class="pr-platform pr-platform-empty" title="' + RLT.ui.escAttr(p.platform || 'Unknown') + '"></span>';

    return '<div class="' + cls.join(' ') + '" data-pid="' + RLT.ui.escAttr(p.id) + '">' +
      '<div class="pr-badge" data-cell="enc">' + p.encounterCount + '</div>' +
      '<div class="pr-info">' +
        '<div class="pr-name">' + RLT.ui.esc(p.name) + youTag + botTag + '</div>' +
        aliases +
      '</div>' +
      platform +
      claim +
    '</div>';
  }

  function patchValues(host, m) {
    if (!m) return;
    for (const p of m.players) {
      const row = host.querySelector('[data-pid="' + RLT.ui.cssEsc(p.id) + '"]');
      if (!row) continue;
      setCell(row, '[data-cell="enc"]', String(p.encounterCount));
    }
  }

  return { render, invalidate };
})();
