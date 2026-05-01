package main

// sdkJS is the RL Toolkit Plugin SDK. Plugins load it via:
//
//	<script src="/sdk.js" data-plugin="my-plugin"></script>
//
// and then use the global `RLT` object to subscribe to events, read enriched
// match state, claim/observe identity, persist plugin data, and look up
// shared encounter history.
const sdkJS = `(function () {
  'use strict';

  if (window.RLT) return; // idempotent

  // ─── Determine plugin name ─────────────────────────────────
  // Plugins identify themselves with <script src="/sdk.js" data-plugin="name">.
  // Falls back to the path segment under /plugins/ when the attribute is
  // missing (so it Just Works in most cases).
  let pluginName = 'unknown';
  try {
    const cur = document.currentScript;
    if (cur && cur.dataset && cur.dataset.plugin) {
      pluginName = cur.dataset.plugin;
    } else {
      const m = location.pathname.match(/\/plugins\/([^/]+)\//);
      if (m) pluginName = m[1];
    }
  } catch (e) {}

  // ─── Overlay sizing + anchor honoring ──────────────────────
  // When the page is loaded inside the composite overlay's iframe, the
  // manifest's width/height defines the plugin's canvas. We force html/body
  // to fill that canvas and pin the plugin's content to the manifest's
  // anchor corner — so anchor:bottom-left + offset 0,0 actually means
  // "flush bottom-left of the iframe", regardless of how big the plugin's
  // content is. No plugin code required.
  try {
    const params = new URLSearchParams(location.search);
    const inOverlay = params.has('overlay');
    const anchor = params.get('anchor') || 'top-left';
    if (inOverlay) {
      const vAlign = anchor.indexOf('bottom') >= 0 ? 'flex-end' : 'flex-start';
      const hAlign = anchor.indexOf('right')  >= 0 ? 'flex-end' : 'flex-start';
      const apply = () => {
        const html = document.documentElement;
        const body = document.body;
        if (!body) return;
        // Fill the iframe completely.
        html.style.margin = '0';
        html.style.padding = '0';
        html.style.height = '100%';
        body.style.margin = '0';
        body.style.padding = '0';
        body.style.minHeight = '100%';
        body.style.height = '100%';
        body.style.width = '100%';
        // Pin content to the manifest's anchor corner.
        body.style.display = 'flex';
        body.style.flexDirection = 'column';
        body.style.alignItems = hAlign;
        body.style.justifyContent = vAlign;
      };
      if (document.body) apply();
      else document.addEventListener('DOMContentLoaded', apply, { once: true });
    }
  } catch (e) {}

  // ─── Tiny pub/sub ──────────────────────────────────────────
  function emitter() {
    const subs = new Map(); // event -> Set<fn>
    return {
      on(ev, fn) {
        if (!subs.has(ev)) subs.set(ev, new Set());
        subs.get(ev).add(fn);
        return () => this.off(ev, fn);
      },
      off(ev, fn) {
        const set = subs.get(ev);
        if (set) set.delete(fn);
      },
      emit(ev, ...args) {
        const set = subs.get(ev);
        if (set) for (const fn of set) {
          try { fn(...args); } catch (e) { console.error('[RLT]', ev, e); }
        }
        // wildcard
        const all = subs.get('*');
        if (all) for (const fn of all) {
          try { fn(ev, ...args); } catch (e) { console.error('[RLT] *', e); }
        }
      },
    };
  }

  // ─── Internal SSE bridge ───────────────────────────────────
  const bus = emitter();
  let status = 'disconnected';
  let es = null;

  function connect() {
    if (es) return;
    es = new EventSource('/events');
    es.onmessage = (e) => {
      let msg;
      try { msg = JSON.parse(e.data); } catch { return; }
      if (msg.Event === '_ConnectionStatus') {
        status = msg.Status;
        bus.emit('_status', status);
        return;
      }
      // Decode the inner JSON-encoded Data payload — RL ships it as a string.
      let data = msg.Data;
      if (typeof data === 'string') {
        try { data = JSON.parse(data); } catch { data = null; }
      }
      bus.emit(msg.Event, data, msg);
    };
    es.onerror = () => {
      // EventSource fires onerror for transient interruptions too — Firefox
      // logs a warning even though the browser auto-reconnects. Only signal
      // 'disconnected' to plugins if the connection is truly CLOSED, so we
      // don't churn plugin state on every blip.
      //   readyState: 0=CONNECTING, 1=OPEN, 2=CLOSED
      if (es && es.readyState === 2) {
        status = 'disconnected';
        bus.emit('_status', status);
      }
      // While CONNECTING (auto-reconnect in flight), stay quiet — the next
      // _ConnectionStatus message from the server will refresh status.
    };
  }

  // Close the SSE connection cleanly before the page unloads. Without this,
  // Firefox logs "The connection to /events was interrupted while the page
  // was loading" every time the user navigates away (e.g. opening a plugin
  // from the dashboard). Closing from JS first turns the abort into a
  // graceful disconnect — no warning.
  //
  // pagehide fires reliably on every navigation kind (bfcache, regular,
  // tab close); beforeunload doesn't fire on mobile / bfcache.
  window.addEventListener('pagehide', () => {
    if (es) {
      try { es.close(); } catch (_) {}
      es = null;
    }
  });

  // ─── Shared store wrappers ─────────────────────────────────
  // Per-plugin namespace: /api/data/<plugin>/<key>
  // Shared namespace:    /api/data/_rlt/<key>
  async function storeGet(ns, key) {
    try {
      const r = await fetch('/api/data/' + ns + (key ? '/' + key : ''));
      if (!r.ok) return null;
      return await r.json();
    } catch (e) { return null; }
  }
  async function storeSet(ns, key, val) {
    try {
      await fetch('/api/data/' + ns + '/' + key, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(val),
      });
      return true;
    } catch (e) { return false; }
  }
  async function storeDelete(ns, key) {
    try {
      await fetch('/api/data/' + ns + '/' + key, { method: 'DELETE' });
      return true;
    } catch (e) { return false; }
  }

  // Debounced writer to avoid hammering the disk on rapid changes.
  function debouncedWriter(ns, key, getValue, ms) {
    let timer = null;
    return function flush() {
      if (timer) clearTimeout(timer);
      timer = setTimeout(() => storeSet(ns, key, getValue()), ms);
    };
  }

  // ─── Identity (shared across all plugins) ──────────────────
  const identity = (function () {
    const ev = emitter();
    let myId = '';
    let loaded = false;

    async function load() {
      // New shared location:
      let cfg = await storeGet('_rlt', 'identity');
      if (cfg && cfg.my_id) {
        myId = cfg.my_id;
      } else {
        // One-time migration from the legacy dejavu-only location.
        const legacy = await storeGet('dejavu', 'config');
        if (legacy && legacy.my_id) {
          myId = legacy.my_id;
          await storeSet('_rlt', 'identity', { my_id: myId });
        }
      }
      loaded = true;
      ev.emit('change', myId);
    }
    load();

    return {
      get id() { return myId; },
      isReady() { return loaded; },
      async set(id) {
        myId = (id || '').trim();
        await storeSet('_rlt', 'identity', { my_id: myId });
        ev.emit('change', myId);
      },
      async clear() { return this.set(''); },
      onChange(fn) { return ev.on('change', fn); },
      // exposed so match-state can flag isMe correctly even before load resolves
      _isMe(id) { return id && id === myId; },
    };
  })();

  // ─── Encounter ledger (shared across all plugins) ──────────
  const encounters = (function () {
    const ev = emitter();
    let map = {};                 // PrimaryId -> { names, count, first_seen, last_seen, matches }
    let loaded = false;
    const persistShared = debouncedWriter('_rlt', 'encounters', () => map, 1500);

    async function load() {
      const fresh = await storeGet('_rlt', 'encounters');
      if (fresh) {
        map = fresh;
      } else {
        // One-time migration from dejavu/encounters.
        const legacy = await storeGet('dejavu', 'encounters');
        if (legacy) {
          map = legacy;
          // Cap count to distinct match GUIDs (fixes the over-counting bug
          // some users have in their existing data).
          for (const id of Object.keys(map)) {
            const e = map[id];
            const truth = Math.max(1, ((e && e.matches) || []).length);
            if ((e.count || 0) !== truth) e.count = truth;
          }
          await storeSet('_rlt', 'encounters', map);
        }
      }
      loaded = true;
      ev.emit('change', map);
    }
    load();

    function record(id, name, guid) {
      if (!id) return;
      const now = new Date().toISOString();
      if (!map[id]) {
        map[id] = { names: [name], count: 1, first_seen: now, last_seen: now, matches: [guid] };
        ev.emit('change', map);
        persistShared();
        return;
      }
      const e = map[id];
      if (!e.matches) e.matches = [];
      if (e.matches.includes(guid)) {
        e.last_seen = now;
        if (!e.names.includes(name)) e.names.push(name);
        persistShared();
        return;
      }
      e.count++;
      e.last_seen = now;
      if (!e.names.includes(name)) e.names.push(name);
      e.matches.push(guid);
      if (e.matches.length > 50) e.matches = e.matches.slice(-50);
      ev.emit('change', map);
      persistShared();
    }

    return {
      get(id) { return map[id] || null; },
      all() { return Object.assign({}, map); },
      isReady() { return loaded; },
      onChange(fn) { return ev.on('change', fn); },
      _record: record,
    };
  })();

  // ─── Enriched match state ──────────────────────────────────
  // Computes a clean view from each UpdateState: blue/orange splits, isMe,
  // encounterCount, aliases, etc. Plugins should generally use this rather
  // than parsing UpdateState themselves.
  //
  // Encounter recording is decoupled from build() — registerCurrentMatch()
  // runs once per match transition (driven off RL's lifecycle events), not
  // every tick. This is robust across match-end → new-match transitions
  // where build() might otherwise miss the GUID change for a frame.
  const match = (function () {
    const ev = emitter();
    let cur = null;       // null when no match
    let lastFingerprint = '';
    let registeredGuid = null; // last guid we recorded encounters for

    function build(d) {
      const guid = d.MatchGuid || 'local';

      const players = (d.Players || []).map((p) => {
        const id = p.PrimaryId || '';
        const name = p.Name || 'Unknown';
        const enc = id ? encounters.get(id) : null;
        return {
          id, name,
          team: p.TeamNum,
          isMe: identity._isMe(id),
          score: p.Score | 0,
          goals: p.Goals | 0,
          assists: p.Assists | 0,
          saves: p.Saves | 0,
          shots: p.Shots | 0,
          demos: p.Demos | 0,
          touches: p.Touches | 0,
          boost: typeof p.Boost === 'number' ? p.Boost : null,
          speed: typeof p.Speed === 'number' ? p.Speed : null,
          onGround: !!p.bOnGround,
          hasCar: !!p.bHasCar,
          encounterCount: enc ? enc.count : 1,
          aliases: enc ? enc.names.filter((n) => n !== name) : [],
          firstSeen: enc ? enc.first_seen : null,
          lastSeen: enc ? enc.last_seen : null,
          platform: id ? id.split('|')[0] : '?',
          raw: p,
        };
      });

      const blue   = players.filter((p) => p.team === 0);
      const orange = players.filter((p) => p.team === 1);
      const game   = d.Game || null;
      const me     = players.find((p) => p.isMe) || null;

      return {
        guid,
        players, blue, orange, me,
        game,
        arena: game ? (game.Arena || '').replace(/_P$/, '').replace(/_/g, ' ') : '',
        clockSeconds: game ? (game.TimeSeconds | 0) : null,
        overtime: !!(game && game.bOvertime),
        scoreBlue:   game && game.Teams ? ((game.Teams.find((t) => t.TeamNum === 0) || game.Teams[0] || {}).Score | 0) : 0,
        scoreOrange: game && game.Teams ? ((game.Teams.find((t) => t.TeamNum === 1) || game.Teams[1] || {}).Score | 0) : 0,
        ball: game && game.Ball ? game.Ball : null,
        raw: d,
      };
    }

    // Record every player in the current match exactly once per match.
    // The Go-side reconnects on MatchDestroyed, so each new match arrives on
    // a fresh TCP connection with a fresh guid — we don't have to defend
    // against half-emptied lobby states or stale podium UpdateStates here.
    function registerCurrentMatch() {
      if (!cur) return;
      if (cur.guid === registeredGuid) return;
      const recorded = [];
      const seen = new Set();
      for (const p of cur.raw.Players || []) {
        const id = p.PrimaryId || '';
        if (!id || seen.has(id)) continue;
        seen.add(id);
        encounters._record(id, p.Name || 'Unknown', cur.guid);
        recorded.push(p.Name);
      }
      if (recorded.length === 0) return; // wait for the roster to populate
      registeredGuid = cur.guid;
      // Rebuild so encounterCount/aliases reflect the new ledger.
      cur = build(cur.raw);
      lastFingerprint = '';
      ev.emit('change', cur);
    }

    bus.on('UpdateState', (d) => {
      if (!d) return;
      cur = build(d);
      ev.emit('tick', cur);

      if (cur.guid !== registeredGuid && cur.players.length > 0) {
        registerCurrentMatch();
      }

      const fp = cur.guid + '|' + identity.id + '|' +
        cur.players.map((p) => p.id + ':' + p.team + ':' + (p.encounterCount > 1 ? 'r' : 'n')).join(',');
      if (fp !== lastFingerprint) {
        lastFingerprint = fp;
        ev.emit('change', cur);
      }
    });

    function clear() {
      registeredGuid = null;
      if (!cur) return;
      cur = null;
      lastFingerprint = '';
      ev.emit('change', null);
      ev.emit('tick', null);
    }

    bus.on('MatchDestroyed', clear);
    bus.on('_status', (s) => { if (s === 'disconnected') clear(); });

    // Re-fingerprint when identity changes so isMe is up to date.
    identity.onChange(() => { lastFingerprint = ''; if (cur) cur = build(cur.raw); ev.emit('change', cur); });

    return {
      get current() { return cur; },
      onChange(fn) { return ev.on('change', fn); }, // structural changes only
      onTick(fn)   { return ev.on('tick', fn);   }, // every UpdateState
    };
  })();

  // ─── Per-plugin store ──────────────────────────────────────
  const store = {
    get(key)        { return storeGet(pluginName, key); },
    getAll()        { return storeGet(pluginName, ''); },
    set(key, val)   { return storeSet(pluginName, key, val); },
    delete(key)     { return storeDelete(pluginName, key); },
  };

  // ─── UI helpers ────────────────────────────────────────────
  const ui = {
    esc(s) {
      return String(s == null ? '' : s).replace(/[&<>"']/g, (c) => ({
        '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
      }[c]));
    },
    escAttr(s) {
      return String(s == null ? '' : s).replace(/[&"']/g, (c) => ({
        '&': '&amp;', '"': '&quot;', "'": '&#39;',
      }[c]));
    },
    formatTime(secs, overtime) {
      if (secs == null) return '0:00';
      const m = Math.floor(secs / 60);
      const s = Math.floor(secs % 60);
      return (overtime ? '+' : '') + m + ':' + (s < 10 ? '0' : '') + s;
    },
    timeAgo(iso) {
      if (!iso) return '—';
      const diff = Date.now() - new Date(iso).getTime();
      const mins = Math.floor(diff / 60000);
      if (mins < 1) return 'now';
      if (mins < 60) return mins + 'm';
      const hrs = Math.floor(mins / 60);
      if (hrs < 24) return hrs + 'h';
      const days = Math.floor(hrs / 24);
      if (days < 7) return days + 'd';
      const weeks = Math.floor(days / 7);
      if (weeks < 5) return weeks + 'w';
      return Math.floor(days / 30) + 'mo';
    },
    cssEsc(s) {
      if (window.CSS && CSS.escape) return CSS.escape(s);
      return String(s == null ? '' : s).replace(/["\\]/g, '\\$&');
    },
    toast(msg, ms) {
      let t = document.getElementById('__rlt_toast');
      if (!t) {
        t = document.createElement('div');
        t.id = '__rlt_toast';
        t.style.cssText =
          'position:fixed;bottom:28px;left:50%;transform:translateX(-50%) translateY(20px);' +
          'background:linear-gradient(135deg,#22d3ee,#a78bfa);color:#0a0c14;' +
          'padding:12px 22px;border-radius:8px;font:700 13px Inter,system-ui,sans-serif;' +
          'letter-spacing:.04em;text-transform:uppercase;opacity:0;pointer-events:none;' +
          'transition:opacity .25s,transform .25s;z-index:99999;' +
          'box-shadow:0 0 30px rgba(34,211,238,.4),0 0 50px rgba(167,139,250,.4);';
        document.body.appendChild(t);
      }
      t.textContent = msg;
      requestAnimationFrame(() => {
        t.style.opacity = '1';
        t.style.transform = 'translateX(-50%) translateY(0)';
      });
      clearTimeout(t._timer);
      t._timer = setTimeout(() => {
        t.style.opacity = '0';
        t.style.transform = 'translateX(-50%) translateY(20px)';
      }, ms || 2000);
    },
  };

  // ─── Typed events layer ────────────────────────────────────
  // Wraps the raw bus with strongly-named handlers. Each callback receives
  // a payload where players are resolved against the live match roster and
  // the encounter ledger, so plugins don't need to do lookups themselves.
  //
  // Player references in RL events come in two shapes:
  //   - In UpdateState / its derivatives: { Name, PrimaryId, TeamNum, ... }
  //   - In sub-events (BallHit, GoalScored, …): { Name, Shortcut, TeamNum }
  //     i.e. NO PrimaryId — only a name + spectator shortcut. We resolve those
  //     against the current match.players list (matched by shortcut, then
  //     name) so the caller still gets isMe/encounter/full player object.
  function resolvePlayer(ref) {
    if (!ref) return null;
    const cur = match.current;
    let player = null;
    if (cur) {
      if (ref.PrimaryId) {
        player = cur.players.find((p) => p.id === ref.PrimaryId) || null;
      }
      if (!player && typeof ref.Shortcut === 'number') {
        player = cur.players.find((p) => (p.raw && p.raw.Shortcut === ref.Shortcut)) || null;
      }
      if (!player && ref.Name) {
        player = cur.players.find((p) => p.name === ref.Name) || null;
      }
    }
    const id = (player && player.id) || ref.PrimaryId || '';
    const enc = id ? encounters.get(id) : null;
    return {
      name: ref.Name || (player && player.name) || 'Unknown',
      shortcut: typeof ref.Shortcut === 'number' ? ref.Shortcut : null,
      team: typeof ref.TeamNum === 'number' ? ref.TeamNum : (player ? player.team : null),
      id,
      isMe: identity._isMe(id),
      player,        // full enriched player from match.players, or null
      encounter: enc, // full encounter record from the ledger, or null
      raw: ref,
    };
  }

  // Recent-events ring buffer so plugins booting mid-match can show context.
  const recentByType = new Map(); // ev -> array of { at, data, envelope }
  const RECENT_LIMIT = 50;
  function recordRecent(ev, data, envelope) {
    let arr = recentByType.get(ev);
    if (!arr) { arr = []; recentByType.set(ev, arr); }
    arr.push({ at: Date.now(), data, envelope });
    if (arr.length > RECENT_LIMIT) arr.splice(0, arr.length - RECENT_LIMIT);
  }

  // Typed bus — sits in front of the raw one. Subscribers fire AFTER the
  // raw bus has run so match.current is already up to date for the tick
  // when sub-events fire on the same frame as an UpdateState.
  const eventsBus = emitter();

  function emitTyped(name, payload) {
    recordRecent(name, payload);
    eventsBus.emit(name, payload);
  }

  // Per-event normalizers — each is a tiny function so the README-fields
  // map 1:1 to JS property names, and players come pre-resolved.
  bus.on('GoalScored', (d) => {
    if (!d) return;
    emitTyped('GoalScored', {
      matchGuid: d.MatchGuid || null,
      goalSpeed: d.GoalSpeed != null ? d.GoalSpeed : null,
      goalTime:  d.GoalTime  != null ? d.GoalTime  : null,
      impactLocation: d.ImpactLocation || null,
      scorer:   resolvePlayer(d.Scorer),
      assister: d.Assister ? resolvePlayer(d.Assister) : null,
      ballLastTouch: d.BallLastTouch ? {
        player: resolvePlayer(d.BallLastTouch.Player),
        speed: d.BallLastTouch.Speed != null ? d.BallLastTouch.Speed : null,
      } : null,
      raw: d,
    });
  });

  bus.on('BallHit', (d) => {
    if (!d) return;
    emitTyped('BallHit', {
      matchGuid: d.MatchGuid || null,
      players: (d.Players || []).map(resolvePlayer),
      preSpeed:  d.Ball ? (d.Ball.PreHitSpeed != null ? d.Ball.PreHitSpeed : null) : null,
      postSpeed: d.Ball ? (d.Ball.PostHitSpeed != null ? d.Ball.PostHitSpeed : null) : null,
      location:  d.Ball ? (d.Ball.Location || null) : null,
      raw: d,
    });
  });

  bus.on('CrossbarHit', (d) => {
    if (!d) return;
    emitTyped('CrossbarHit', {
      matchGuid: d.MatchGuid || null,
      ballSpeed:   d.BallSpeed   != null ? d.BallSpeed   : null,
      impactForce: d.ImpactForce != null ? d.ImpactForce : null,
      ballLocation: d.BallLocation || null,
      ballLastTouch: d.BallLastTouch ? {
        player: resolvePlayer(d.BallLastTouch.Player),
        speed: d.BallLastTouch.Speed != null ? d.BallLastTouch.Speed : null,
      } : null,
      raw: d,
    });
  });

  bus.on('StatfeedEvent', (d) => {
    if (!d) return;
    emitTyped('Statfeed', {
      matchGuid: d.MatchGuid || null,
      eventName: d.EventName || '',
      type:      d.Type      || '',
      target:    resolvePlayer(d.MainTarget),
      victim:    d.SecondaryTarget ? resolvePlayer(d.SecondaryTarget) : null,
      raw: d,
    });
  });

  bus.on('ClockUpdatedSeconds', (d) => {
    if (!d) return;
    emitTyped('ClockUpdated', {
      matchGuid: d.MatchGuid || null,
      seconds:   d.TimeSeconds != null ? d.TimeSeconds : null,
      overtime:  !!d.bOvertime,
      raw: d,
    });
  });

  bus.on('MatchEnded', (d) => {
    if (!d) return;
    emitTyped('MatchEnded', {
      matchGuid: d.MatchGuid || null,
      winnerTeam: typeof d.WinnerTeamNum === 'number' ? d.WinnerTeamNum : null,
      raw: d,
    });
  });

  // Plain pass-through events: nothing to resolve, but we still surface them
  // through the typed bus so plugins have one consistent API.
  ['MatchCreated','MatchInitialized','MatchDestroyed',
   'MatchPaused','MatchUnpaused',
   'CountdownBegin','RoundStarted',
   'GoalReplayStart','GoalReplayWillEnd','GoalReplayEnd',
   'PodiumStart','ReplayCreated'
  ].forEach((name) => {
    bus.on(name, (d) => {
      emitTyped(name, {
        matchGuid: (d && d.MatchGuid) || null,
        raw: d || null,
      });
    });
  });

  // Helper that wires RLT.events.onName(fn) and exposes a typed history.
  function makeOn(name) {
    return (fn) => eventsBus.on(name, fn);
  }
  const events = {
    on:  (name, fn) => eventsBus.on(name, fn),
    off: (name, fn) => eventsBus.off(name, fn),
    recent(name, n) {
      const arr = recentByType.get(name) || [];
      return n ? arr.slice(-n) : arr.slice();
    },

    // typed subscribers
    onGoalScored:        makeOn('GoalScored'),
    onBallHit:           makeOn('BallHit'),
    onCrossbarHit:       makeOn('CrossbarHit'),
    onStatfeed:          makeOn('Statfeed'),
    onClockUpdated:      makeOn('ClockUpdated'),

    onMatchCreated:      makeOn('MatchCreated'),
    onMatchInitialized:  makeOn('MatchInitialized'),
    onMatchDestroyed:    makeOn('MatchDestroyed'),
    onMatchEnded:        makeOn('MatchEnded'),
    onMatchPaused:       makeOn('MatchPaused'),
    onMatchUnpaused:     makeOn('MatchUnpaused'),

    onCountdownBegin:    makeOn('CountdownBegin'),
    onRoundStarted:      makeOn('RoundStarted'),

    onGoalReplayStart:   makeOn('GoalReplayStart'),
    onGoalReplayWillEnd: makeOn('GoalReplayWillEnd'),
    onGoalReplayEnd:     makeOn('GoalReplayEnd'),

    onPodiumStart:       makeOn('PodiumStart'),
    onReplayCreated:     makeOn('ReplayCreated'),
  };

  // ─── Lifecycle state machine ───────────────────────────────
  // RL doesn't ship a "what's happening right now" enum. Plugins want one.
  // Derived states:
  //   'idle'      - no match (or RL disconnected)
  //   'created'   - MatchCreated fired but countdown hasn't started
  //   'countdown' - CountdownBegin fired, RoundStarted hasn't
  //   'live'      - active gameplay
  //   'paused'    - admin paused
  //   'replay'    - goal replay or history replay active
  //   'ended'     - MatchEnded fired
  //   'podium'    - PodiumStart fired
  const lifecycle = (function () {
    const ev = emitter();
    let phase = 'idle';
    let prevPhase = 'idle';

    function set(next) {
      if (next === phase) return;
      prevPhase = phase;
      phase = next;
      ev.emit('change', phase, prevPhase);
    }

    bus.on('_status', (s) => { if (s !== 'connected') set('idle'); });

    bus.on('MatchCreated',     () => set('created'));
    bus.on('MatchInitialized', () => set('countdown'));
    bus.on('CountdownBegin',   () => set('countdown'));
    bus.on('RoundStarted',     () => set('live'));
    bus.on('MatchPaused',      () => set('paused'));
    bus.on('MatchUnpaused',    () => set(prevPhase === 'paused' ? 'live' : prevPhase));
    bus.on('GoalReplayStart',  () => set('replay'));
    bus.on('GoalReplayEnd',    () => set('live'));
    bus.on('MatchEnded',       () => set('ended'));
    bus.on('PodiumStart',      () => set('podium'));
    bus.on('MatchDestroyed',   () => set('idle'));

    // Fall-back: if we get UpdateStates without ever hearing a setup event
    // (plugin booted mid-match), best-effort to 'live'.
    bus.on('UpdateState', (d) => {
      if (phase === 'idle' && d && d.Players && d.Players.length) {
        if (d.Game && d.Game.bReplay) set('replay');
        else set('live');
      }
    });

    return {
      get phase() { return phase; },
      get previous() { return prevPhase; },
      onChange(fn) { return ev.on('change', fn); },
    };
  })();

  // Expose lifecycle on the existing match object for discoverability.
  match.lifecycle = lifecycle;
  match.onLifecycle = (fn) => lifecycle.onChange(fn);

  // ─── Event catalog ─────────────────────────────────────────
  // Self-documenting registry of every event the SDK emits, what shape its
  // payload has, and whether it fires during live gameplay only or anywhere.
  // Plugin authors can console.log(RLT.events.catalog) to discover what's
  // available without reading source.
  events.catalog = [
    // Per-tick stream
    { name: 'UpdateState',       category: 'tick',      shape: 'matchstate', livePhases: ['live','replay','paused','countdown'], desc: 'Match snapshot at PacketSendRate (raw envelope payload).' },

    // In-play events
    { name: 'GoalScored',        category: 'scoring',   shape: 'goal',       livePhases: ['live','replay'],     desc: 'Scorer + assister + last touch + impact.' },
    { name: 'BallHit',           category: 'play',      shape: 'ballhit',    livePhases: ['live'],              desc: 'Ball touched. Pre/post speed and location.' },
    { name: 'CrossbarHit',       category: 'play',      shape: 'crossbar',   livePhases: ['live'],              desc: 'Ball hit a crossbar.' },
    { name: 'Statfeed',          category: 'stat',      shape: 'stat',       livePhases: ['live','replay'],     desc: 'Player earned a stat (demo, save, epic save, etc).' },
    { name: 'ClockUpdated',      category: 'play',      shape: 'clock',      livePhases: ['live','countdown'],  desc: 'Match clock changed by ≥1 second.' },

    // Lifecycle
    { name: 'MatchCreated',      category: 'lifecycle', shape: 'match',      livePhases: '*',                   desc: 'All teams replicated; lobby ready.' },
    { name: 'MatchInitialized',  category: 'lifecycle', shape: 'match',      livePhases: '*',                   desc: 'First countdown started.' },
    { name: 'CountdownBegin',    category: 'lifecycle', shape: 'match',      livePhases: '*',                   desc: 'Round countdown began.' },
    { name: 'RoundStarted',      category: 'lifecycle', shape: 'match',      livePhases: '*',                   desc: 'Active gameplay started (countdown ended).' },
    { name: 'MatchPaused',       category: 'lifecycle', shape: 'match',      livePhases: '*',                   desc: 'Match paused by an admin.' },
    { name: 'MatchUnpaused',     category: 'lifecycle', shape: 'match',      livePhases: '*',                   desc: 'Match resumed.' },
    { name: 'GoalReplayStart',   category: 'replay',    shape: 'match',      livePhases: '*',                   desc: 'Goal replay began.' },
    { name: 'GoalReplayWillEnd', category: 'replay',    shape: 'match',      livePhases: '*',                   desc: 'Ball exploded during replay (fires only if not skipped).' },
    { name: 'GoalReplayEnd',     category: 'replay',    shape: 'match',      livePhases: '*',                   desc: 'Goal replay ended.' },
    { name: 'MatchEnded',        category: 'lifecycle', shape: 'matchend',   livePhases: '*',                   desc: 'Match decided. Has WinnerTeamNum.' },
    { name: 'PodiumStart',       category: 'lifecycle', shape: 'match',      livePhases: '*',                   desc: 'Game entered podium state.' },
    { name: 'MatchDestroyed',    category: 'lifecycle', shape: 'match',      livePhases: '*',                   desc: 'Player left the match.' },
    { name: 'ReplayCreated',     category: 'lifecycle', shape: 'match',      livePhases: '*',                   desc: 'Match-history replay loaded (NOT goal replays).' },
  ];

  // Frozen views by category for the common "give me everything in group X" need.
  events.byCategory = events.catalog.reduce((acc, e) => {
    (acc[e.category] = acc[e.category] || []).push(e.name);
    return acc;
  }, {});

  // ─── Plugin registration API ───────────────────────────────
  // Declarative way for plugins to wire themselves up. Built on top of the
  // imperative APIs above — the raw bus is still available for one-offs.
  //
  // Usage:
  //   const me = RLT.plugin.register({
  //     name: 'boost-meter',
  //     version: '1.0.0',
  //     init()  { /* setup */ },
  //     ready() { /* once identity + encounters loaded */ },
  //     events: {
  //       GoalScored(g)  { ... },         // typed payload (g.scorer.player, etc)
  //       UpdateState(d) { ... },         // raw match update
  //       '*'(name, p)   { ... },         // catchall
  //     },
  //     whilePhase: ['live', 'replay'],   // optional gate; default = any phase
  //     onMatch(m)     { ... },           // structural match changes
  //     onTick(m)      { ... },           // every UpdateState
  //     onIdentity(id) { ... },
  //     onEncounters(map) { ... },
  //     dispose() { /* cleanup */ },
  //   });
  //   me.dispose();    // tear down all subscriptions, run dispose hook
  const plugin = (function () {
    const registry = []; // array of plugin records

    function shouldFire(spec) {
      if (!spec.whilePhase) return true;
      if (spec.whilePhase === '*') return true;
      const allow = Array.isArray(spec.whilePhase) ? spec.whilePhase : [spec.whilePhase];
      return allow.indexOf(lifecycle.phase) !== -1;
    }

    function gate(spec, fn) {
      // Wraps a handler with phase gating + error isolation, so a single
      // plugin throwing doesn't take down others.
      return function () {
        if (!shouldFire(spec)) return;
        try { return fn.apply(null, arguments); }
        catch (e) { console.error('[RLT] plugin "' + spec.name + '" handler threw:', e); }
      };
    }

    function register(spec) {
      spec = spec || {};
      const name = spec.name || pluginName;
      const unsubs = [];
      let disposed = false;

      // Per-plugin scoped store. Falls back to the page-level pluginName if
      // the spec didn't override it (most plugins won't).
      const pluginStore = {
        get(key)      { return storeGet(name, key); },
        getAll()      { return storeGet(name, ''); },
        set(key, val) { return storeSet(name, key, val); },
        delete(key)   { return storeDelete(name, key); },
      };

      // Wire event handlers — keys are event names from the catalog.
      if (spec.events) {
        for (const evName of Object.keys(spec.events)) {
          const handler = spec.events[evName];
          if (typeof handler !== 'function') continue;
          // '*' uses the raw bus catchall; named events go through the typed bus.
          const sub = (evName === '*')
            ? bus.on('*', gate(spec, handler))
            : eventsBus.on(evName, gate(spec, handler));
          unsubs.push(sub);
        }
      }

      // Convenience subscriptions.
      if (typeof spec.onMatch      === 'function') unsubs.push(match.onChange(gate(spec, spec.onMatch)));
      if (typeof spec.onTick       === 'function') unsubs.push(match.onTick(gate(spec, spec.onTick)));
      if (typeof spec.onIdentity   === 'function') unsubs.push(identity.onChange(gate(spec, spec.onIdentity)));
      if (typeof spec.onEncounters === 'function') unsubs.push(encounters.onChange(gate(spec, spec.onEncounters)));
      if (typeof spec.onLifecycle  === 'function') unsubs.push(lifecycle.onChange(gate(spec, spec.onLifecycle)));

      const handle = {
        name,
        version: spec.version || null,
        author:  spec.author  || null,
        get disposed() { return disposed; },
        store: pluginStore,
        events: Object.keys(spec.events || {}),
        spec,
        dispose() {
          if (disposed) return;
          disposed = true;
          for (const u of unsubs) { try { u(); } catch {} }
          unsubs.length = 0;
          if (typeof spec.dispose === 'function') {
            try { spec.dispose(); }
            catch (e) { console.error('[RLT] plugin "' + name + '" dispose threw:', e); }
          }
          const i = registry.indexOf(handle);
          if (i >= 0) registry.splice(i, 1);
        },
      };
      registry.push(handle);

      // init synchronously, ready when identity + encounters have loaded.
      if (typeof spec.init === 'function') {
        try { spec.init(handle); }
        catch (e) { console.error('[RLT] plugin "' + name + '" init threw:', e); }
      }
      const fireReady = () => {
        if (typeof spec.ready === 'function') {
          try { spec.ready(handle); }
          catch (e) { console.error('[RLT] plugin "' + name + '" ready threw:', e); }
        }
      };
      if (identity.isReady() && encounters.isReady()) {
        fireReady();
      } else {
        // poll cheaply (loaded flips once and stays)
        const t = setInterval(() => {
          if (identity.isReady() && encounters.isReady()) {
            clearInterval(t);
            if (!disposed) fireReady();
          }
        }, 50);
      }

      console.debug('[RLT] plugin registered:', name, spec.version || '(no version)');
      return handle;
    }

    return {
      register,
      list() {
        return registry.map((h) => ({
          name: h.name, version: h.version, author: h.author,
          events: h.events.slice(),
          disposed: h.disposed,
        }));
      },
      get(name) { return registry.find((h) => h.name === name) || null; },
    };
  })();

  // ─── Public API ────────────────────────────────────────────
  window.RLT = {
    plugin: plugin,         // registration API; .name kept below for back-compat
    pluginName: pluginName, // explicit name accessor
    version: 1,

    // raw event bus
    on: (ev, fn) => bus.on(ev, fn),
    off: (ev, fn) => bus.off(ev, fn),

    // connection
    status: () => status,
    onStatus: (fn) => bus.on('_status', fn),

    // domain APIs
    match,
    me: identity,
    encounters,
    store,
    ui,
    events,
  };

  // Auto-connect on load. Plugins can call RLT._reconnect() to force.
  window.RLT._reconnect = function () { if (es) { es.close(); es = null; } connect(); };
  connect();
})();
`

// sdkCSS exposes shared design tokens so plugins can opt into the toolkit's
// look without pasting the same 200 lines of CSS into every overlay.
//
// Plugins reference them via var(--rlt-cyan), var(--rlt-display), etc.
const sdkCSS = `:root{
  /* surfaces */
  --rlt-bg-0:#0a0c14; --rlt-bg-1:#0f1320; --rlt-bg-2:#161b2c; --rlt-bg-3:#1d2238;
  --rlt-line:#232a44; --rlt-line-2:#2f375a;

  /* text */
  --rlt-txt:#e6e9f5; --rlt-txt-2:#a9b0cf; --rlt-txt-3:#6c739a;

  /* accents */
  --rlt-cyan:#22d3ee;       --rlt-cyan-glow:rgba(34,211,238,0.35);
  --rlt-violet:#a78bfa;     --rlt-violet-glow:rgba(167,139,250,0.35);
  --rlt-magenta:#f472b6;
  --rlt-gold:#fbbf24;
  --rlt-green:#22c55e;

  /* team colors */
  --rlt-blue:#3b82f6;       --rlt-blue-glow:rgba(59,130,246,0.4);
  --rlt-orange:#fb7c3c;     --rlt-orange-glow:rgba(251,124,60,0.4);

  /* typography (load fonts at the top of your overlay) */
  --rlt-display:'Saira Condensed',system-ui,sans-serif;
  --rlt-ui:'Inter',system-ui,sans-serif;
  --rlt-mono:'JetBrains Mono',ui-monospace,monospace;

  /* shape */
  --rlt-radius:10px;
  --rlt-radius-lg:14px;
}

/* Convenience classes plugins can drop in. */
.rlt-card{
  background:linear-gradient(180deg,var(--rlt-bg-1),var(--rlt-bg-2));
  border:1px solid var(--rlt-line);
  border-radius:var(--rlt-radius-lg);
  box-shadow:0 8px 30px rgba(0,0,0,0.3);
  color:var(--rlt-txt);
}
.rlt-pill{
  display:inline-flex;align-items:center;gap:8px;
  padding:6px 12px;
  background:var(--rlt-bg-3);border:1px solid var(--rlt-line);
  border-radius:99px;
  font-family:var(--rlt-ui);font-size:11px;font-weight:600;
  color:var(--rlt-txt-2);
  text-transform:uppercase;letter-spacing:0.08em;
}
.rlt-btn{
  font-family:var(--rlt-ui);font-size:12px;font-weight:600;
  padding:9px 14px;border-radius:7px;
  border:1px solid var(--rlt-line);
  background:var(--rlt-bg-3);color:var(--rlt-txt);
  cursor:pointer;transition:all .15s ease;
  letter-spacing:0.02em;text-transform:uppercase;
}
.rlt-btn:hover{
  border-color:var(--rlt-cyan);color:var(--rlt-cyan);
  box-shadow:0 0 16px var(--rlt-cyan-glow);
}
.rlt-btn-primary{
  background:linear-gradient(135deg,var(--rlt-cyan),var(--rlt-violet));
  color:var(--rlt-bg-0);border-color:transparent;
}
.rlt-btn-primary:hover{
  box-shadow:0 0 20px var(--rlt-cyan-glow), 0 0 28px var(--rlt-violet-glow);
  color:var(--rlt-bg-0);
}
`
