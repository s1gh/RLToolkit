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
    host.innerHTML = '<div class="teams-stack">' + teamBlock(m.blue, 'blue') + teamBlock(m.orange, 'orange') + '</div>';
  }

  function playerRow(p) {
    const cls = ['player-row'];
    if (p.isMe) cls.push('is-me');
    if (p.encounterCount > 1) cls.push('returning');

    const aliases = p.aliases.length
      ? '<div class="pr-aliases">aka ' + p.aliases.slice(-2).map(RLT.ui.esc).join(' · ') + '</div>'
      : '';
    const youTag = p.isMe ? '<span class="you-tag">YOU</span>' : '';
    const claim = (p.id && !p.isMe)
      ? '<button class="claim-btn" data-claim-id="' + RLT.ui.escAttr(p.id) + '">this is me</button>'
      : '<span></span>';

    const platform = p.platform
      ? '<span class="pr-platform">' + RLT.ui.esc(p.platform) + '</span>'
      : '';

    return '<div class="' + cls.join(' ') + '" data-pid="' + RLT.ui.escAttr(p.id) + '">' +
      '<div class="pr-badge" data-cell="enc">' + p.encounterCount + '</div>' +
      '<div class="pr-info">' +
        '<div class="pr-name">' + RLT.ui.esc(p.name) + youTag + '</div>' +
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
