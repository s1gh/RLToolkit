// Live-match player list. The plugin's reason for existing: at a glance,
// see which of these 5 other people are returning, and which one (if any)
// you really know.
//
// Render strategy: the row scaffolding only depends on roster identity +
// teams + identity claim, so we rebuild it via a `scaffoldKey` and otherwise
// patch only the encounter count cell on every tick. That keeps DOM churn
// low at the SDK's 60Hz onTick rate.
window.DV = window.DV || {};

DV.match = (function () {
  const { $, setCell, scaffoldKey } = DV.dom;
  const classifyReturners = DV.classifyReturners;
  let lastKey = '';

  function render() {
    const host = $('match-host');
    if (!host) return;
    const m = RLT.match.current;

    // The standout id is part of the cache key so a mid-match reshuffle
    // (a returning player overtakes the previous top) repaints cleanly
    // instead of leaving the gold treatment on the old row.
    const standoutId = m
      ? classifyReturners(m.players.filter((p) => !p.isMe && !p.isBot)).standoutId
      : null;
    const key = scaffoldKey(m, RLT.me.id) + '|s=' + (standoutId || '-');

    if (key !== lastKey) {
      lastKey = key;
      buildScaffold(host, m, standoutId);
    }
    patchValues(host, m);
    updateBadge(m);
  }

  // The pill in the card header — reflects connection + match state.
  // Stays terse: we'd rather show one of {offline, idle, lobby, live, …}
  // than try to express every transition.
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

  // Forces a scaffold rebuild on the next render. Used after identity
  // changes so the YOU tag and claim-button affordances re-resolve.
  function invalidate() { lastKey = ''; }

  function buildScaffold(host, m, standoutId) {
    if (!m) {
      host.innerHTML = emptyState('queue up — waiting for a match');
      return;
    }

    const teamHTML = (players, team) => {
      if (!players.length) return '';
      return '<div class="team-block ' + team + '">'
        + '<div class="team-block-head"><span class="tdot"></span><span class="tname">' + team + '</span></div>'
        + players.map((p) => playerRow(p, p.id === standoutId)).join('')
        + '</div>';
    };
    const inner = teamHTML(m.blue, 'blue') + teamHTML(m.orange, 'orange');

    if (!inner) {
      // Match exists but the roster hasn't replicated yet (lobby load).
      host.innerHTML = emptyState('waiting for players…');
      return;
    }
    host.innerHTML = '<div class="teams-stack">' + inner + '</div>';
  }

  function emptyState(msg) {
    return '<div class="card-empty"><div class="em-icon">◇</div>' + msg + '</div>';
  }

  function playerRow(p, isStandout) {
    const cls = ['player-row'];
    if (p.isMe)  cls.push('is-me');
    if (p.isBot) cls.push('is-bot');
    // The plain "DÉJÀ VU" tag is for every other returner. Self never
    // gets it (you'd see it every game) and bots never get it (the
    // ledger holds one aggregate record across every bot you've played,
    // so the count is meaningless as a recognition signal).
    if (!p.isMe && !p.isBot && p.encounterCount > 1) cls.push('returning');
    if (isStandout) cls.push('returning-top');

    // Bot aliases come from the shared bot record — they're other bots'
    // names, not useful "aka" context for this specific bot.
    const aliases = (!p.isBot && p.aliases.length)
      ? '<div class="pr-aliases">aka ' + p.aliases.slice(-2).map(RLT.ui.esc).join(' · ') + '</div>'
      : '';

    const youTag = p.isMe  ? '<span class="you-tag">YOU</span>' : '';
    const botTag = p.isBot ? '<span class="bot-tag">BOT</span>' : '';

    // Claim button: only visible while identity is unclaimed and only on
    // human, non-self rows. Once claimed the buttons vanish — the YOU
    // tag is the affordance from then on.
    const claim = (!RLT.me.id && p.id && !p.isMe && !p.isBot)
      ? '<button class="claim-btn" data-claim-id="' + RLT.ui.escAttr(p.id) + '" title="This is me">+</button>'
      : '<span></span>';

    // playerIcon picks the right glyph: brand icon for humans, CPU icon
    // for bots. Empty placeholder only fires when neither is available —
    // a human with an unknown platform string.
    const icon = RLT.ui.playerIcon(p);
    const platformTitle = RLT.ui.escAttr(p.isBot ? 'Bot' : (p.platform || 'Unknown'));
    const platform = icon
      ? '<span class="pr-platform" title="' + platformTitle + '">' + icon + '</span>'
      : '<span class="pr-platform pr-platform-empty" title="' + platformTitle + '"></span>';

    // Badge content: encounter count for everyone else, "ME" for self
    // (the count would be "matches you've played yourself" — meaningless).
    // No data-cell="enc" on self rows so the per-tick patcher leaves the
    // "ME" label alone.
    const badge = p.isMe
      ? '<div class="pr-badge">ME</div>'
      : '<div class="pr-badge" data-cell="enc">' + p.encounterCount + '</div>';

    return '<div class="' + cls.join(' ') + '" data-pid="' + RLT.ui.escAttr(p.id) + '">' +
      badge +
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
      // Self row's badge says "ME" (no data-cell), so there's nothing to
      // patch. Skipping explicitly also documents the intent at this site.
      if (p.isMe) continue;
      const row = host.querySelector('[data-pid="' + RLT.ui.cssEsc(p.id) + '"]');
      if (!row) continue;
      setCell(row, '[data-cell="enc"]', String(p.encounterCount));
    }
  }

  return { render, invalidate };
})();
