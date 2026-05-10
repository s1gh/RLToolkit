// Déjà Vu — overlay-only plugin. Tracks players you've encountered
// before and renders the current lobby as a compact recon HUD.
//
// Loaded as a classic <script>, not an ES module.
// biome-ignore lint/suspicious/noRedundantUseStrict: classic script
'use strict';

(function () {
  const STANDOUT_RATIO = 1.5;
  const STANDOUT_ABSOLUTE = 5;

  // Picks the single standout returner. Pure. Caller is responsible for
  // filtering out self and bots before passing players in.
  //   - No returners → null.
  //   - Exactly one returner → standout only if count ≥ STANDOUT_ABSOLUTE.
  //   - Multiple returners → standout only if top.count ≥ runnerUp.count * RATIO.
  // The absolute threshold is intentionally NOT a free pass in the
  // multi-returner branch: when everyone in the lobby has played you many
  // times, nobody is "the" standout.
  function classifyStandout(candidates) {
    const returners = candidates
      .filter((p) => p.encounterCount > 1)
      .sort((a, b) => b.encounterCount - a.encounterCount);
    if (returners.length === 0) return null;
    const top = returners[0];
    if (returners.length === 1) {
      return top.encounterCount >= STANDOUT_ABSOLUTE ? top.id : null;
    }
    const runnerUp = returners[1];
    return top.encounterCount >= runnerUp.encounterCount * STANDOUT_RATIO ? top.id : null;
  }

  function playerRow(p, standoutId) {
    const classes = ['ov-row'];
    if (p.isMe) classes.push('is-me');
    if (p.isBot) classes.push('is-bot');
    if (!p.isMe && !p.isBot && p.encounterCount > 1) classes.push('returning');
    if (!p.isMe && !p.isBot && p.id === standoutId) classes.push('returning-top');

    // Self shows 'ME' in the count cell; everyone else shows ×N where
    // N is the SDK's encounter count for that id. Always prefixed
    // with × — a bare '1' in a right-aligned column reads as empty
    // next to the denser '×3', '×4' characters on returner rows.
    // Bots share one aggregate encounter record in the SDK ledger, so
    // every bot in the lobby shows the same all-time bot-encounter
    // count; that's the honest reading of the data.
    const count = p.isMe ? 'ME' : '×' + p.encounterCount;

    const icon = RLT.ui.playerIcon(p) || '';
    const aliases =
      !p.isBot && p.aliases && p.aliases.length > 0
        ? '<div class="ov-r-aliases"><span class="aka">aka</span> ' +
          RLT.ui.esc(p.aliases[p.aliases.length - 1]) +
          '</div>'
        : '';

    return (
      '<div class="' + classes.join(' ') + '">' +
      '<div class="ov-r-num">' + count + '</div>' +
      '<div class="ov-r-platform">' + icon + '</div>' +
      '<div class="ov-r-name">' + RLT.ui.esc(p.name) + aliases + '</div>' +
      '</div>'
    );
  }

  function teamBlock(team, players, standoutId) {
    if (players.length === 0) return '';
    return (
      '<div class="ov-team ' + team + '">' +
      '<div class="ov-tname">' + team + '</div>' +
      players.map((p) => playerRow(p, standoutId)).join('') +
      '</div>'
    );
  }

  function render() {
    const body = document.getElementById('ov-body');
    if (!body) return;
    const m = RLT.match.current;
    if (!m) {
      body.innerHTML = '<div class="ov-empty">awaiting contact</div>';
      return;
    }
    const candidates = m.players.filter((p) => !p.isMe && !p.isBot);
    const standoutId = classifyStandout(candidates);
    const blue = m.players.filter((p) => p.team === 0);
    const orange = m.players.filter((p) => p.team === 1);
    body.innerHTML =
      teamBlock('blue', blue, standoutId) + teamBlock('orange', orange, standoutId);
  }

  RLT.plugin.register({
    init() {
      if (RLT.widget.isHosted()) {
        RLT.widget.fitWidth({ target: '.ov', maxWidth: 600, extra: 8 });
      }
    },
    ready() {
      render();
    },
    // Render trigger map:
    //   _PlayerJoined / _PlayerLeft  — roster join/leave (per-player events
    //                                  derived from UpdateState diffs).
    //   MatchCreated / MatchDestroyed — empty-state ↔ rows transitions.
    //   onEncounters                 — ledger writes (count just incremented
    //                                  for someone in the current lobby).
    events: {
      _PlayerJoined: render,
      _PlayerLeft: render,
      MatchCreated: render,
      MatchDestroyed: render,
    },
    onEncounters: render,
  });
})();
