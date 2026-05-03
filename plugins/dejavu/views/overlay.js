// Compact "recon HUD" rendered when the page is loaded with ?overlay=1.
// One row per other player; the standout (if any) gets the gold pulse —
// everyone else with encounterCount > 1 just gets the DÉJÀ VU tag.
//
// Render strategy: structural diff, not innerHTML rebuild. During match
// init the roster lands on the wire in two passes — the first carries
// raw player names with default encounterCount=1, the second carries
// the ledger-resolved counts and aliases. A naive `body.innerHTML = …`
// blows away every row between passes, restarting the `ov-row-in`
// fade-in animation on rows that are already mounted; that reads as a
// flicker. Instead we keep persistent row elements keyed by player id
// and only mutate the parts that changed. New players animate in;
// existing rows update in place, no animation restart.
window.DV = window.DV || {};

DV.overlay = (function () {
  const $ = DV.dom.$;
  const classifyReturners = DV.classifyReturners;

  // Per-row fingerprint — covers every value the row's content/classes
  // depend on. Used to skip in-place mutation when nothing about a row
  // has actually changed.
  function rowFp(p, isStandout) {
    return (
      p.id +
      '|' +
      p.team +
      '|' +
      p.encounterCount +
      '|' +
      p.name +
      '|' +
      p.platform +
      '|' +
      (p.aliases.length ? p.aliases.slice(-1)[0] : '') +
      '|' +
      (isStandout ? '1' : '0') +
      '|' +
      (p.isBot ? '1' : '0')
    );
  }

  // Build the inner HTML of a row (count cell + platform glyph + name).
  // Class list is set on the wrapper element separately so we can toggle
  // returning/returning-top without rewriting the inner DOM.
  function rowInnerHTML(p) {
    const aliases =
      !p.isBot && p.aliases.length
        ? '<div class="ov-r-aliases"><span class="aka">aka</span>' +
          RLT.ui.esc(p.aliases.slice(-1)[0]) +
          '</div>'
        : '';
    const num = p.encounterCount > 1 ? '×' + p.encounterCount : p.encounterCount;
    const icon = RLT.ui.playerIcon(p);
    const platformTitle = RLT.ui.escAttr(p.isBot ? 'Bot' : p.platform || 'Unknown');
    const platform = icon
      ? '<div class="ov-r-platform" title="' + platformTitle + '">' + icon + '</div>'
      : '<div class="ov-r-platform ov-r-platform-empty" title="' + platformTitle + '"></div>';
    return (
      '<div class="ov-r-num">' +
      num +
      '</div>' +
      platform +
      '<div class="ov-r-name">' +
      RLT.ui.esc(p.name) +
      aliases +
      '</div>'
    );
  }

  // Apply class list for a row in place. Toggles `returning` /
  // `returning-top` / `is-bot` based on the latest player view; doesn't
  // touch the row's children, so the inner DOM survives untouched.
  function applyRowClasses(el, p, isStandout) {
    el.classList.toggle('is-bot', !!p.isBot);
    el.classList.toggle('returning', !p.isBot && p.encounterCount > 1);
    el.classList.toggle('returning-top', !!isStandout);
  }

  // Stable per-row key. Humans key by their PrimaryId; bots all share
  // the sentinel BOT id (RL ships every CPU-controlled player under
  // the same PrimaryId — see isBotId in the SDK), so we tack on the
  // player's roster index to keep them distinct. The roster order is
  // stable across ticks within a match, so the index-keyed bot rows
  // get reused frame-to-frame just like human rows.
  function rowKey(p, rosterIdx) {
    return p.isBot ? p.id + '#' + rosterIdx : p.id;
  }

  // Mounted state: per-team containers and the row elements inside them.
  // Keyed by rowKey(p, idx) so we can find a row to update without
  // scanning. Reset to null whenever the body innerHTML is replaced
  // wholesale (no match → match, all-empty → solo, etc.).
  let mounted = null; // { blueWrap, orangeWrap, rows: Map<key, { el, fp, team }> }
  let lastBodyState = ''; // 'empty' | 'awaiting' | 'rows' | ''
  // Coarse roster fingerprint covering everything render() touches —
  // when this matches, the per-row diff walk would be a pure no-op so
  // we can skip it. render() runs at 60Hz via onTick; most ticks during
  // an active match flip nothing roster-shaped, so the early exit is
  // the common case.
  let lastRosterFp = '';

  function unmountAll() {
    mounted = null;
    lastBodyState = '';
    lastRosterFp = '';
  }

  function ensureMounted(body) {
    if (mounted) return mounted;
    body.innerHTML =
      '<div class="ov-team blue" hidden><div class="ov-tname">blue</div></div>' +
      '<div class="ov-team orange" hidden><div class="ov-tname">orange</div></div>';
    mounted = {
      blueWrap: body.querySelector('.ov-team.blue'),
      orangeWrap: body.querySelector('.ov-team.orange'),
      rows: new Map(),
    };
    lastBodyState = 'rows';
    return mounted;
  }

  function render() {
    const body = $('ov-body');
    if (!body) return;
    const m = RLT.match.current;

    if (!m) {
      if (lastBodyState === 'awaiting') return;
      unmountAll();
      body.innerHTML = '<div class="ov-empty">awaiting contact</div>';
      lastBodyState = 'awaiting';
      return;
    }

    // Standout candidates: other humans only. Bots and self are excluded
    // upstream (bots: meaningless recognition signal; self: filtered per
    // team below). standoutId is null when no one clearly stands out.
    const candidates = m.players.filter((p) => !p.isMe && !p.isBot);
    const { standoutId } = classifyReturners(candidates);

    // Cheap fast-path: if nothing render() cares about has changed since
    // last frame, bail before any DOM walk. The fingerprint covers every
    // input that drives row content, ordering, and the standout class.
    // Includes p.name so duplicate-id bots stay distinct in the fp.
    const fp =
      (standoutId || '-') +
      '!' +
      m.players
        .map(
          (p) =>
            p.id +
            ':' +
            p.team +
            ':' +
            (p.isMe ? '1' : '0') +
            ':' +
            (p.isBot ? '1' : '0') +
            ':' +
            p.encounterCount +
            ':' +
            p.name +
            ':' +
            p.platform +
            ':' +
            (p.aliases.length ? p.aliases.slice(-1)[0] : ''),
        )
        .join('|');
    if (fp === lastRosterFp && lastBodyState === 'rows') return;
    lastRosterFp = fp;

    // Build per-team lists, preserving each player's roster index so we
    // can derive a stable row key (bots share PrimaryId — index breaks
    // the tie). The original per-team filter dropped indices on the
    // floor; we keep them in `[player, idx]` pairs through the diff.
    const blueList = [];
    const orangeList = [];
    for (let idx = 0; idx < m.players.length; idx++) {
      const p = m.players[idx];
      if (p.isMe) continue;
      if (p.team === 0) blueList.push([p, idx]);
      else if (p.team === 1) orangeList.push([p, idx]);
    }

    if (!blueList.length && !orangeList.length) {
      if (lastBodyState === 'empty') return;
      unmountAll();
      body.innerHTML = '<div class="ov-empty">solo session</div>';
      lastBodyState = 'empty';
      return;
    }

    const state = ensureMounted(body);

    // Diff per team. wantedKeys collects every key we kept this pass;
    // anything in mounted.rows that isn't in it gets unmounted at the
    // end. Stagger index is global across teams so rows fade in in
    // rendering order (blue first, then orange).
    const wantedKeys = new Set();
    let staggerIdx = 0;

    function syncTeam(list, team, wrap) {
      if (!list.length) {
        wrap.hidden = true;
        return;
      }
      wrap.hidden = false;
      // The .ov-tname header is always children[0]; rows follow. Walk the
      // desired list and either move/update an existing row or create a
      // new one in the right slot.
      let cursor = wrap.firstElementChild ? wrap.firstElementChild.nextElementSibling : null;
      for (const [p, rosterIdx] of list) {
        const key = rowKey(p, rosterIdx);
        wantedKeys.add(key);
        // Bots are excluded from standout (the classifier already filters
        // them out), so their isStandout is always false here. Comparing
        // by id alone would falsely flag every bot when standoutId
        // happens to equal the BOT sentinel — guard with !p.isBot.
        const isStandout = !p.isBot && p.id === standoutId;
        const fp = rowFp(p, isStandout);
        let rec = state.rows.get(key);

        if (rec && rec.team !== team) {
          // Player switched teams (rare, but handle: player rejoining
          // after a quit, or team-balance shuffle). Detach from old slot,
          // we'll reinsert below.
          rec.el.remove();
          rec.team = team;
        }

        if (!rec) {
          // New row — fade-in animation runs once.
          const el = document.createElement('div');
          el.className = 'ov-row';
          el.style.setProperty('--ov-i', staggerIdx);
          el.innerHTML = rowInnerHTML(p);
          applyRowClasses(el, p, isStandout);
          rec = { el, fp, team };
          state.rows.set(key, rec);
          if (cursor) {
            wrap.insertBefore(el, cursor);
          } else {
            wrap.appendChild(el);
          }
          // cursor stays where it was — el was inserted BEFORE it, and
          // the next iteration should still compare against the same
          // existing row.
        } else {
          // Existing row — patch only what changed. No animation restart.
          if (rec.fp !== fp) {
            rec.el.innerHTML = rowInnerHTML(p);
            applyRowClasses(rec.el, p, isStandout);
            rec.fp = fp;
          }
          // Make sure it's positioned correctly. If the current cursor
          // doesn't match this row, move it into place.
          if (rec.el !== cursor) {
            wrap.insertBefore(rec.el, cursor);
          } else {
            cursor = cursor.nextElementSibling;
          }
        }
        staggerIdx++;
      }
      // Drop any leftover children past the desired tail (rows that
      // belonged to this team but are no longer wanted).
      while (cursor) {
        const next = cursor.nextElementSibling;
        // Only remove .ov-row leftovers (never the .ov-tname header).
        if (cursor.classList.contains('ov-row')) cursor.remove();
        cursor = next;
      }
    }

    syncTeam(blueList, 'blue', state.blueWrap);
    syncTeam(orangeList, 'orange', state.orangeWrap);

    // Reap rows whose keys aren't in this pass (player left the match,
    // or roster shrank so an index-based bot key no longer maps).
    for (const [key, rec] of state.rows) {
      if (!wantedKeys.has(key)) {
        rec.el.remove();
        state.rows.delete(key);
      }
    }
  }

  return { render };
})();
