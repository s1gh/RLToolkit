(function () {
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
        // Tag the document so plugin CSS can target .overlay-mode for
        // transparent backgrounds, no padding, etc — same convention every
        // plugin used by hand before; now it's automatic.
        body.classList.add('overlay-mode');
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

  // Server-side event filter (?events=...). The SDK always needs the
  // lifecycle events (drives RLT.match.lifecycle and reset semantics).
  //
  // UpdateState is intentionally NOT in the always-required set — it's
  // the heaviest event by far (~1-3 KB at 60-120 Hz) and most plugins
  // don't need it. Plugins opt in by registering an UpdateState/onTick
  // handler, an onMatch handler, or by calling RLT.match.subscribe().
  //
  // Synthetic events ("_ConnectionStatus", "_Lifecycle") bypass the
  // server's filter entirely; we don't list them here.
  const requiredEvents = new Set([
    'MatchCreated', 'MatchInitialized',
    'CountdownBegin', 'RoundStarted',
    'MatchPaused', 'MatchUnpaused',
    'GoalReplayStart', 'GoalReplayWillEnd', 'GoalReplayEnd',
    'MatchEnded', 'PodiumStart', 'MatchDestroyed',
    'ReplayCreated',
  ]);
  const subscribedEvents = new Set(requiredEvents);

  // addEvent records that a plugin handler wants 'name' and reconnects
  // the EventSource if we already opened one with a stale filter.
  // Idempotent — re-registering a handler for the same event is free.
  function addEvent(name) {
    if (!name || subscribedEvents.has(name)) return;
    subscribedEvents.add(name);
    if (es) {
      // Already connected with the prior filter; reconnect so the
      // server starts delivering the new event. Cheap on localhost.
      try { es.close(); } catch (_) {}
      es = null;
      connect();
    }
  }

  function buildEventsURL() {
    const events = Array.from(subscribedEvents).join(',');
    return '/events?events=' + encodeURIComponent(events);
  }

  function connect() {
    if (es) return;
    es = new EventSource(buildEventsURL());
    es.onmessage = (e) => {
      let msg;
      try { msg = JSON.parse(e.data); } catch { return; }
      if (msg.Event === '_ConnectionStatus') {
        status = msg.Status;
        bus.emit('_status', status);
        return;
      }
      // Synthetic _Lifecycle: snapshot fields live at the top level
      // (match_active / phase / match_guid / since), not inside Data.
      // Hand the whole envelope to the lifecycle subscriber.
      if (msg.Event === '_Lifecycle') {
        bus.emit('_Lifecycle', msg);
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
      // Important distinction: a stored record with my_id === '' means
      // the user explicitly cleared their identity — respect it. Only
      // attempt the legacy migration when there is NO record yet at all.
      let cfg = await storeGet('_rlt', 'identity');
      if (cfg && typeof cfg.my_id === 'string') {
        myId = cfg.my_id;
      } else {
        // One-time migration from the legacy dejavu-only location.
        // Always write back (even if empty) so subsequent loads take
        // the fast path and never look at the legacy slot again.
        const legacy = await storeGet('dejavu', 'config');
        if (legacy && legacy.my_id) {
          myId = legacy.my_id;
        }
        await storeSet('_rlt', 'identity', { my_id: myId });
        // Drop the legacy slot so a future Clear can't be undone by it.
        if (legacy) await storeDelete('dejavu', 'config');
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
    let map = {};                 // PrimaryId -> { names, count, first_seen, last_seen, matches, wins, losses }
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

    // Returns true when the count was created or bumped (i.e. a new
    // player-match pair was observed). The per-tick top-up uses the
    // return value to decide whether to rebuild the cached match
    // snapshot — same-guid re-records are no-ops on the count.
    function record(id, name, guid) {
      if (!id) return false;
      const now = new Date().toISOString();
      if (!map[id]) {
        map[id] = { names: [name], count: 1, first_seen: now, last_seen: now, matches: [guid], wins: 0, losses: 0 };
        ev.emit('change', map);
        persistShared();
        return true;
      }
      const e = map[id];
      if (!e.matches) e.matches = [];
      if (e.matches.includes(guid)) {
        e.last_seen = now;
        if (!e.names.includes(name)) e.names.push(name);
        persistShared();
        return false;
      }
      e.count++;
      e.last_seen = now;
      if (!e.names.includes(name)) e.names.push(name);
      e.matches.push(guid);
      if (e.matches.length > 50) e.matches = e.matches.slice(-50);
      ev.emit('change', map);
      persistShared();
      return true;
    }

    // Apply a per-player W/L delta. Lazy-upgrades legacy records that
    // predate the wins/losses fields (treat absent as 0). Caller is
    // responsible for dedup — this method blindly increments.
    function recordOutcome(id, won) {
      if (!id) return;
      const e = map[id];
      if (!e) return; // outcome only meaningful for already-seen players
      if (typeof e.wins   !== 'number') e.wins   = 0;
      if (typeof e.losses !== 'number') e.losses = 0;
      if (won) e.wins++; else e.losses++;
      ev.emit('change', map);
      persistShared();
    }

    return {
      get(id) { return map[id] || null; },
      all() { return Object.assign({}, map); },
      isReady() { return loaded; },
      onChange(fn) { return ev.on('change', fn); },
      _record: record,
      _recordOutcome: recordOutcome,
    };
  })();

  // ─── Enriched match state ──────────────────────────────────
  // Computes a clean view from each UpdateState: blue/orange splits, isMe,
  // encounterCount, aliases, etc. Plugins should generally use this rather
  // than parsing UpdateState themselves.
  //
  // Encounter recording happens inside the UpdateState handler, gated on
  // lifecycle phase: only count players we actually played (countdown
  // through podium-adjacent phases), not lobby no-shows. The ledger
  // dedups per (player, guid) so the per-tick scan is idempotent and
  // late-joiners / rejoiners get picked up automatically.
  const match = (function () {
    const ev = emitter();
    let cur = null;       // null when no match
    let lastFingerprint = '';
    let lastFinalizedGuid = null;  // last guid we recorded W/L for (dedup)
    // Phases where the player is actually engaged in a match — kickoff
    // through final whistle. Lobby/menu phases are excluded so a player
    // who dodges before countdown doesn't get counted. Late-joiners and
    // rejoiners are picked up automatically because we re-scan the
    // roster on every tick while in these phases; the ledger's
    // per-(id, guid) dedup keeps this idempotent.
    const RECORDING_PHASES = new Set(['countdown', 'live', 'paused', 'replay']);

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
          carTouches: p.CarTouches | 0,
          boost: typeof p.Boost === 'number' ? p.Boost : null,
          speed: typeof p.Speed === 'number' ? p.Speed : null,
          // Booleans from the spectator-only block. Absent fields → false,
          // which is what plugins want as a default.
          boosting:     !!p.bBoosting,
          onGround:     !!p.bOnGround,
          onWall:       !!p.bOnWall,
          powersliding: !!p.bPowersliding,
          demolished:   !!p.bDemolished,
          supersonic:   !!p.bSupersonic,
          hasCar:       !!p.bHasCar,
          // Present only on the frame this player was demolished. Resolved
          // to the enriched player-ref shape in a second pass below, once
          // the full roster is built. Stash the raw stub here for now.
          attackerRaw: p.Attacker || null,
          attacker: null,
          encounterCount: enc ? enc.count : 1,
          aliases: enc ? enc.names.filter((n) => n !== name) : [],
          firstSeen: enc ? enc.first_seen : null,
          lastSeen: enc ? enc.last_seen : null,
          platform: id ? id.split('|')[0] : '?',
          raw: p,
        };
      });

      // Resolve attackers now that the roster exists. resolvePlayerIn
      // looks up the attacker against the just-built `players` array, so
      // attacker.player / attacker.isMe / attacker.encounter are all
      // populated when the attacker is in the same match.
      for (const p of players) {
        if (p.attackerRaw) {
          p.attacker = resolvePlayerIn(players, p.attackerRaw);
        }
        delete p.attackerRaw;
      }

      const blue   = players.filter((p) => p.team === 0);
      const orange = players.filter((p) => p.team === 1);
      const game   = d.Game || null;
      const me     = players.find((p) => p.isMe) || null;

      // Normalize Game.Teams into lowercase-keyed entries plus blue/orange
      // split shortcuts. ColorPrimary/ColorSecondary are raw hex strings
      // (no '#' prefix) — plugins prepend '#' when applying.
      const teams = game && Array.isArray(game.Teams)
        ? game.Teams
            .filter((t) => t && typeof t.TeamNum === 'number')
            .map((t) => ({
              teamNum: t.TeamNum,
              name: t.Name || '',
              score: t.Score | 0,
              colorPrimary: t.ColorPrimary || '',
              colorSecondary: t.ColorSecondary || '',
              raw: t,
            }))
            .sort((a, b) => a.teamNum - b.teamNum)
        : [];
      const blueTeam = teams.find((t) => t.teamNum === 0) || null;
      const orangeTeam = teams.find((t) => t.teamNum === 1) || null;

      // Conditional replay-only fields. The API emits Frame and Elapsed
      // only while a replay is active (goal replay or match-history
      // replay). Group them under a single nullable object so plugins
      // can gate with `if (m.replayInfo) { … }`.
      const hasFrame = game && typeof game.Frame === 'number';
      const hasElapsed = game && typeof game.Elapsed === 'number';
      const replayInfo = (hasFrame || hasElapsed) ? {
        frame:   hasFrame   ? game.Frame   : null,
        elapsed: hasElapsed ? game.Elapsed : null,
      } : null;

      return {
        guid,
        players, blue, orange, me,
        game,
        arena: game ? (game.Arena || '').replace(/_P$/, '').replace(/_/g, ' ') : '',
        clockSeconds: game ? (game.TimeSeconds | 0) : null,
        overtime: !!(game && game.bOvertime),
        // bReplay covers both goal replays and history replays — the
        // authoritative way to detect either, instead of inferring from
        // GoalReplayStart/End edges.
        replay:     !!(game && game.bReplay),
        hasWinner:  !!(game && game.bHasWinner),
        winner:     game ? (game.Winner || '') : '',
        scoreBlue:   game && game.Teams ? ((game.Teams.find((t) => t.TeamNum === 0) || game.Teams[0] || {}).Score | 0) : 0,
        scoreOrange: game && game.Teams ? ((game.Teams.find((t) => t.TeamNum === 1) || game.Teams[1] || {}).Score | 0) : 0,
        // Normalized team metadata: full array plus blue/orange shortcuts
        // (mirroring the existing match.blue / match.orange split).
        teams,
        blueTeam,
        orangeTeam,
        replayInfo,
        // Ball state. Lowercase keys + .raw mirror the resolved-player
        // pattern used elsewhere in the SDK. TeamNum 255 is the API's
        // "ball has not been touched yet" sentinel; lastTouchTeam
        // normalizes it to null so plugins don't have to remember.
        ball: (game && game.Ball) ? {
          speed: typeof game.Ball.Speed === 'number' ? game.Ball.Speed : null,
          teamNum: typeof game.Ball.TeamNum === 'number' ? game.Ball.TeamNum : null,
          lastTouchTeam: (typeof game.Ball.TeamNum === 'number' && game.Ball.TeamNum !== 255)
            ? game.Ball.TeamNum
            : null,
          raw: game.Ball,
        } : null,
        // Spectator camera target — only meaningful when bHasTarget.
        target: game && game.bHasTarget ? (game.Target || null) : null,
        raw: d,
      };
    }

    // Record everyone on the current roster against this match guid.
    // Only meaningful during recording phases — caller is responsible
    // for that gate. Returns true if any player triggered a count
    // create/bump, so the caller can rebuild `cur` so encounterCount
    // reflects the new ledger.
    function recordRoster() {
      if (!cur) return false;
      let bumped = false;
      const seen = new Set();
      for (const p of cur.raw.Players || []) {
        const id = p.PrimaryId || '';
        if (!id || seen.has(id)) continue;
        seen.add(id);
        if (encounters._record(id, p.Name || 'Unknown', cur.guid)) bumped = true;
      }
      return bumped;
    }

    bus.on('UpdateState', (d) => {
      if (!d) return;
      cur = build(d);
      ev.emit('tick', cur);

      // Record the roster only once we're past the lobby. The
      // ledger dedups per-(player, guid), so calling this every tick
      // while in a match is idempotent — it only does work when a
      // never-seen-this-match player is on the roster (kickoff,
      // late-joiners, rejoiners after a quit).
      if (RECORDING_PHASES.has(lifecycle.phase) && cur.players.length > 0) {
        if (recordRoster()) {
          cur = build(cur.raw);
          lastFingerprint = '';
        }
      }

      const fp = cur.guid + '|' + identity.id + '|' +
        cur.players.map((p) => p.id + ':' + p.team + ':' + (p.encounterCount > 1 ? 'r' : 'n')).join(',');
      if (fp !== lastFingerprint) {
        lastFingerprint = fp;
        ev.emit('change', cur);
      }
    });

    function clear() {
      lastFinalizedGuid = null;
      if (!cur) return;
      cur = null;
      lastFingerprint = '';
      ev.emit('change', null);
      ev.emit('tick', null);
    }

    // Commit W/L exactly once per match. Requires:
    //   - the user has claimed identity AND has a known team in the
    //     current roster (otherwise we can't determine outcome),
    //   - WinnerTeamNum is present on the event,
    //   - the current match guid hasn't been finalized already.
    bus.on('MatchEnded', (d) => {
      if (!d || !cur) return;
      if (lastFinalizedGuid === cur.guid) return;
      const winnerTeam = (typeof d.WinnerTeamNum === 'number') ? d.WinnerTeamNum : null;
      if (winnerTeam === null) return;
      const me = cur.players.find((p) => p.isMe);
      if (!me || typeof me.team !== 'number') return;
      const won = me.team === winnerTeam;
      const seen = new Set();
      for (const p of cur.players) {
        if (!p.id || seen.has(p.id)) continue;
        seen.add(p.id);
        encounters._recordOutcome(p.id, won);
      }
      lastFinalizedGuid = cur.guid;
    });

    bus.on('MatchDestroyed', clear);
    bus.on('_status', (s) => { if (s === 'disconnected') clear(); });

    // Re-fingerprint when identity changes so isMe is up to date.
    identity.onChange(() => { lastFingerprint = ''; if (cur) cur = build(cur.raw); ev.emit('change', cur); });

    return {
      get current() { return cur; },
      // onChange / onTick auto-subscribe to UpdateState since they're
      // useless without it. Plugins that read .current synchronously
      // outside a handler should call .subscribe() once at init time.
      onChange(fn) { addEvent('UpdateState'); return ev.on('change', fn); },
      onTick(fn)   { addEvent('UpdateState'); return ev.on('tick', fn);   },
      subscribe()  { addEvent('UpdateState'); },
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
  // Platform brand icons (Simple Icons paths, monocolor via fill=currentColor).
  // Keyed by lowercased platform string; values are just the path 'd' attribute.
  const PLATFORM_ICONS = {
    steam:    'M11.979 0C5.678 0 .511 4.86.022 11.037l6.432 2.658a3.4 3.4 0 0 1 1.912-.59q.094.001.188.006l2.861-4.142V8.91a4.53 4.53 0 0 1 4.524-4.524c2.494 0 4.524 2.031 4.524 4.527s-2.03 4.525-4.524 4.525h-.105l-4.076 2.911l.004.159a3.39 3.39 0 0 1-3.39 3.396a3.41 3.41 0 0 1-3.331-2.727L.436 15.27C1.862 20.307 6.486 24 11.979 24c6.627 0 11.999-5.373 11.999-12S18.605 0 11.979 0M7.54 18.21l-1.473-.61c.262.543.714.999 1.314 1.25a2.551 2.551 0 0 0 3.337-3.324a2.547 2.547 0 0 0-3.255-1.413l1.523.63a1.878 1.878 0 0 1-1.445 3.467zm11.415-9.303a3.02 3.02 0 0 0-3.015-3.015a3.015 3.015 0 1 0 3.015 3.015m-5.273-.005a2.264 2.264 0 1 1 4.531 0a2.267 2.267 0 0 1-2.266 2.265a2.264 2.264 0 0 1-2.265-2.265',
    epic:     'M3.537 0C2.165 0 1.66.506 1.66 1.879V18.44a4 4 0 0 0 .02.433c.031.3.037.59.316.92c.027.033.311.245.311.245c.153.075.258.13.43.2l8.335 3.491c.433.199.614.276.928.27h.002c.314.006.495-.071.928-.27l8.335-3.492c.172-.07.277-.124.43-.2c0 0 .284-.211.311-.243c.28-.33.285-.621.316-.92a4 4 0 0 0 .02-.434V1.879c0-1.373-.506-1.88-1.878-1.88zm13.366 3.11h.68c1.138 0 1.688.553 1.688 1.696v1.88h-1.374v-1.8c0-.369-.17-.54-.523-.54h-.235c-.367 0-.537.17-.537.539v5.81c0 .369.17.54.537.54h.262c.353 0 .523-.171.523-.54V8.619h1.373v2.143c0 1.144-.562 1.71-1.7 1.71h-.694c-1.138 0-1.7-.566-1.7-1.71V4.82c0-1.144.562-1.709 1.7-1.709zm-12.186.08h3.114v1.274H6.117v2.603h1.648v1.275H6.117v2.774h1.74v1.275h-3.14zm3.816 0h2.198c1.138 0 1.7.564 1.7 1.708v2.445c0 1.144-.562 1.71-1.7 1.71h-.799v3.338h-1.4zm4.53 0h1.4v9.201h-1.4zm-3.13 1.235v3.392h.575c.354 0 .523-.171.523-.54V4.965c0-.368-.17-.54-.523-.54z',
    playstation: 'M8.984 2.596v17.547l3.915 1.261V6.688c0-.69.304-1.151.794-.991c.636.18.76.814.76 1.505v5.875c2.441 1.193 4.362-.002 4.362-3.152c0-3.237-1.126-4.675-4.438-5.827c-1.307-.448-3.728-1.186-5.39-1.502zm4.656 16.241l6.296-2.275c.715-.258.826-.625.246-.818c-.586-.192-1.637-.139-2.357.123l-4.205 1.5V14.98l.24-.085s1.201-.42 2.913-.615c1.696-.18 3.785.03 5.437.661c1.848.601 2.04 1.472 1.576 2.072c-.465.6-1.622 1.036-1.622 1.036l-8.544 3.107V18.86zM1.807 18.6c-1.9-.545-2.214-1.668-1.352-2.32c.801-.586 2.16-1.052 2.16-1.052l5.615-2.013v2.313L4.205 17c-.705.271-.825.632-.239.826c.586.195 1.637.15 2.343-.12L8.247 17v2.074c-.12.03-.256.044-.39.073c-1.939.331-3.996.196-6.038-.479z',
    xbox:     'M4.102 21.033A11.95 11.95 0 0 0 12 24a11.96 11.96 0 0 0 7.902-2.967c1.877-1.912-4.316-8.709-7.902-11.417c-3.582 2.708-9.779 9.505-7.898 11.417m11.16-14.406c2.5 2.961 7.484 10.313 6.076 12.912A11.94 11.94 0 0 0 24 12.004a11.95 11.95 0 0 0-3.57-8.536s-.027-.022-.082-.042a.8.8 0 0 0-.281-.045c-.592 0-1.985.434-4.805 3.246M3.654 3.426c-.057.02-.082.041-.086.042A11.96 11.96 0 0 0 0 12.004c0 2.854.998 5.473 2.661 7.533c-1.401-2.605 3.579-9.951 6.08-12.91c-2.82-2.813-4.216-3.245-4.806-3.245a.7.7 0 0 0-.281.046zM12 3.551S9.055 1.828 6.755 1.746c-.903-.033-1.454.295-1.521.339C7.379.646 9.659 0 11.984 0H12c2.334 0 4.605.646 6.766 2.085c-.068-.046-.615-.372-1.52-.339C14.946 1.828 12 3.545 12 3.545z',
    switch:   'M14.176 24h3.674c3.376 0 6.15-2.774 6.15-6.15V6.15C24 2.775 21.226 0 17.85 0H14.1c-.074 0-.15.074-.15.15v23.7c-.001.076.075.15.226.15m4.574-13.199c1.351 0 2.399 1.125 2.399 2.398c0 1.352-1.125 2.4-2.399 2.4c-1.35 0-2.4-1.049-2.4-2.4c-.075-1.349 1.05-2.398 2.4-2.398M11.4 0H6.15C2.775 0 0 2.775 0 6.15v11.7C0 21.226 2.775 24 6.15 24h5.25c.074 0 .15-.074.15-.149V.15c.001-.076-.075-.15-.15-.15M9.676 22.051H6.15a4.194 4.194 0 0 1-4.201-4.201V6.15A4.194 4.194 0 0 1 6.15 1.949H9.6zM3.75 7.199c0 1.275.975 2.25 2.25 2.25s2.25-.975 2.25-2.25c0-1.273-.975-2.25-2.25-2.25s-2.25.977-2.25 2.25',
  };
  // Normalizes RL's PrimaryId platform prefix to an icon key. RL emits
  // values like 'Steam', 'Epic', 'PS4', 'XboxOne', 'Switch', 'Unknown'.
  function platformIconKey(platform) {
    if (!platform) return null;
    const p = String(platform).toLowerCase();
    if (p === 'steam')   return 'steam';
    if (p === 'epic')    return 'epic';
    if (p.startsWith('ps')) return 'playstation';     // PS4, PS5
    if (p.startsWith('xbox')) return 'xbox';          // XboxOne, Xbox
    if (p === 'switch' || p.includes('nintendo')) return 'switch';
    return null; // unknown / unmapped
  }

  const ui = {
    // Returns inline SVG markup for the platform's brand icon, or an empty
    // string when the platform isn't recognized. The caller's container
    // controls sizing; the SVG inherits color via fill=currentColor.
    platformIcon(platform) {
      const key = platformIconKey(platform);
      if (!key) return '';
      const d = PLATFORM_ICONS[key];
      const title = key.charAt(0).toUpperCase() + key.slice(1);
      return '<svg class="rlt-platform-icon" viewBox="0 0 24 24" aria-label="' + title + '" role="img">'
        + '<title>' + title + '</title>'
        + '<path fill="currentColor" d="' + d + '"/></svg>';
    },
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
  //     against the roster (matched by shortcut, then name) so the caller
  //     still gets isMe/encounter/full player object.

  // Resolve a {Name, Shortcut, TeamNum} or {Name, PrimaryId, ...} stub
  // against an explicit roster (the array of enriched player objects
  // produced by build()). Returns the same enriched shape regardless of
  // which input shape the API used. roster may be null/empty — the result
  // still carries name/shortcut/team from the stub itself.
  function resolvePlayerIn(roster, ref) {
    if (!ref) return null;
    let player = null;
    if (roster && roster.length) {
      if (ref.PrimaryId) {
        player = roster.find((p) => p.id === ref.PrimaryId) || null;
      }
      if (!player && typeof ref.Shortcut === 'number') {
        player = roster.find((p) => (p.raw && p.raw.Shortcut === ref.Shortcut)) || null;
      }
      if (!player && ref.Name) {
        player = roster.find((p) => p.name === ref.Name) || null;
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
      player,        // full enriched player from the roster, or null
      encounter: enc, // full encounter record from the ledger, or null
      raw: ref,
    };
  }

  // Backwards-compatible wrapper used by every existing caller (typed
  // event normalizers): resolves against the live match.current roster.
  function resolvePlayer(ref) {
    const cur = match.current;
    return resolvePlayerIn(cur ? cur.players : null, ref);
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
  //
  // GoalScored note: RL re-fires GoalScored at round-restart boundaries
  // (after the kickoff countdown) with an empty Scorer.Name and no
  // resolvable roster match. Without this guard, every such re-fire
  // overwrites the plugin's last-known scorer with the literal string
  // "Unknown". Drop the event when we can't identify a real scorer.
  bus.on('GoalScored', (d) => {
    if (!d) return;
    const scorer = resolvePlayer(d.Scorer);
    if (!scorer || (!scorer.player && (!d.Scorer || !d.Scorer.Name))) return;
    emitTyped('GoalScored', {
      matchGuid: d.MatchGuid || null,
      goalSpeed: d.GoalSpeed != null ? d.GoalSpeed : null,
      goalTime:  d.GoalTime  != null ? d.GoalTime  : null,
      impactLocation: d.ImpactLocation || null,
      scorer,
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

  // Statfeed: every player ref is a {Name, Shortcut, TeamNum} stub —
  // resolvePlayer enriches it against the live roster. Field names mirror
  // the official wire payload (MainTarget / SecondaryTarget) so the docs
  // are searchable both ways.
  bus.on('StatfeedEvent', (d) => {
    if (!d) return;
    emitTyped('StatfeedEvent', {
      matchGuid: d.MatchGuid || null,
      eventName: d.EventName || '',
      type:      d.Type      || '',
      mainTarget:      resolvePlayer(d.MainTarget),
      secondaryTarget: d.SecondaryTarget ? resolvePlayer(d.SecondaryTarget) : null,
      raw: d,
    });
  });

  bus.on('ClockUpdatedSeconds', (d) => {
    if (!d) return;
    emitTyped('ClockUpdatedSeconds', {
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
    return (fn) => {
      addEvent(name);
      return eventsBus.on(name, fn);
    };
  }
  const events = {
    on:  (name, fn) => {
      addEvent(name);
      return eventsBus.on(name, fn);
    },
    off: (name, fn) => eventsBus.off(name, fn),
    recent(name, n) {
      const arr = recentByType.get(name) || [];
      return n ? arr.slice(-n) : arr.slice();
    },

    // typed subscribers
    onGoalScored:        makeOn('GoalScored'),
    onBallHit:           makeOn('BallHit'),
    onCrossbarHit:       makeOn('CrossbarHit'),
    onStatfeedEvent:     makeOn('StatfeedEvent'),
    onClockUpdatedSeconds: makeOn('ClockUpdatedSeconds'),

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

  // ─── Lifecycle (driven by the server's _Lifecycle event) ──
  //
  // The toolkit publishes an authoritative gameplay snapshot via the
  // synthetic _Lifecycle SSE event. We mirror it locally so plugins
  // can poll the current phase, subscribe to transitions, or ask the
  // simpler "am I in a match" question without inferring from
  // individual RL events.
  //
  // Phase enum (matches server LifecyclePhase exactly):
  //   'none'      - not in a match (server's authoritative state for
  //                 disconnected, between matches, or RL silent for >5s)
  //   'created'   - MatchCreated fired but countdown hasn't started
  //   'countdown' - CountdownBegin fired, RoundStarted hasn't
  //   'live'      - active gameplay
  //   'paused'    - admin paused
  //   'replay'    - goal replay
  //   'ended'     - MatchEnded fired
  //   'podium'    - PodiumStart fired
  //
  // Backwards compat: the previous client-only machine called the
  // catch-all state 'idle'. Plugins that wrote whilePhase: ['idle']
  // keep working — we accept either name in shouldFire below.
  const lifecycle = (function () {
    const ev = emitter();
    const matchActiveEv = emitter();

    let phase = 'none';
    let prevPhase = 'none';
    let matchActive = false;
    let matchGUID = '';
    let since = null; // ISO string from the server

    function applySnapshot(snap) {
      const newPhase = String(snap.phase || 'none');
      const newActive = !!snap.match_active;
      const newGUID = String(snap.match_guid || '');
      const phaseChanged = newPhase !== phase;
      const activeChanged = newActive !== matchActive;

      if (phaseChanged) {
        prevPhase = phase;
        phase = newPhase;
      }
      matchActive = newActive;
      matchGUID = newGUID;
      since = snap.since || null;

      if (phaseChanged) ev.emit('change', phase, prevPhase);
      if (activeChanged) matchActiveEv.emit('change', matchActive);
    }

    // Wire the server's authoritative event. The bus emits these from
    // the EventSource onmessage path before the typed-event normalizer
    // runs, so we always see them inline with everything else.
    bus.on('_Lifecycle', (snap) => { if (snap) applySnapshot(snap); });

    return {
      get phase() { return phase; },
      get previous() { return prevPhase; },
      get matchActive() { return matchActive; },
      get guid() { return matchGUID; },
      get since() { return since; },
      onChange(fn) { return ev.on('change', fn); },
      onMatchActive(fn) { return matchActiveEv.on('change', fn); },
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
    { name: 'UpdateState',       category: 'tick',      shape: 'matchstate', livePhases: ['live','replay','paused','countdown'], desc: 'Match snapshot at PacketSendRate. Includes derived teams/blueTeam/orangeTeam, replayInfo, and resolved per-player attacker.' },

    // In-play events
    { name: 'GoalScored',        category: 'scoring',   shape: 'goal',       livePhases: ['live','replay'],     desc: 'Scorer + assister + last touch + impact.' },
    { name: 'BallHit',           category: 'play',      shape: 'ballhit',    livePhases: ['live'],              desc: 'Ball touched. Pre/post speed and location.' },
    { name: 'CrossbarHit',       category: 'play',      shape: 'crossbar',   livePhases: ['live'],              desc: 'Ball hit a crossbar.' },
    { name: 'StatfeedEvent',       category: 'stat',      shape: 'stat',       livePhases: ['live','replay'],     desc: 'Player earned a stat (demo, save, epic save, etc).' },
    { name: 'ClockUpdatedSeconds', category: 'play',      shape: 'clock',      livePhases: ['live','countdown'],  desc: 'Match clock changed by ≥1 second.' },

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

  // ─── Statfeed eventName registry ───────────────────────────
  // RL ships StatfeedEvent.eventName as raw asset names ('Demolish',
  // 'AerialGoal', 'FlipReset', …). The official Stats API docs do NOT
  // enumerate these — the list below is what's been observed in-game.
  // Use RLT.stats.* in plugin code instead of bare strings so typos
  // surface as undefined rather than silent no-ops.
  //
  // To discover new ones, run the debug plugin and watch for entries
  // here that aren't in this list — then add them.
  // `known` is attached BEFORE the freeze so plugins can do
  // stats.known.has(s.eventName) when filtering against the verified
  // set. (Adding it after Object.freeze would throw in strict mode.)
  const stats = {
    SHOT:        'Shot',         // type: "Shot on Goal"
    GOAL:        'Goal',         // also fires GoalScored
    AERIAL_GOAL: 'AerialGoal',
    LONG_GOAL:   'LongGoal',
    TURTLE_GOAL: 'TurtleGoal',
    HAT_TRICK:   'HatTrick',     // 3+ goals by same player
    SAVE:        'Save',
    DEMOLISH:    'Demolish',     // secondaryTarget = demolished player
    FLIP_RESET:  'FlipReset',
    WIN:         'Win',
  };
  stats.known = new Set(Object.values(stats));
  Object.freeze(stats);

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
      const cur = lifecycle.phase;
      // Back-compat alias: 'idle' was the previous name for what the
      // server now calls 'none'. Plugins that wrote whilePhase:['idle']
      // (or its inverse) should keep working.
      if (allow.indexOf(cur) !== -1) return true;
      if (cur === 'none' && allow.indexOf('idle') !== -1) return true;
      return false;
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

    function isolate(spec, fn) {
      // Like gate() but without the whilePhase filter — used for
      // transition observers (onLifecycle, onMatchActive) that need to
      // fire regardless of destination phase. Still wraps in try/catch
      // for error isolation.
      return function () {
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
          // Tell the server we want this event delivered. '*' is the
          // raw-bus catchall — it doesn't add anything to the filter
          // (the catchall fires on whatever the bus already gets).
          if (evName !== '*') addEvent(evName);
          const sub = (evName === '*')
            ? bus.on('*', gate(spec, handler))
            : eventsBus.on(evName, gate(spec, handler));
          unsubs.push(sub);
        }
      }

      // Convenience subscriptions.
      //
      // onLifecycle and onMatchActive intentionally bypass whilePhase
      // gating: they observe transitions *into* and *out of* phases, so
      // gating them by the destination phase would swallow exactly the
      // transitions the plugin cares about. (Without this, a plugin
      // with whilePhase: ['live'] would never see "match_active just
      // went false" because the destination phase is 'none'.)
      // isolate() gives them error/log handling without phase gating.
      if (typeof spec.onMatch       === 'function') unsubs.push(match.onChange(gate(spec, spec.onMatch)));
      if (typeof spec.onTick        === 'function') unsubs.push(match.onTick(gate(spec, spec.onTick)));
      if (typeof spec.onIdentity    === 'function') unsubs.push(identity.onChange(gate(spec, spec.onIdentity)));
      if (typeof spec.onEncounters  === 'function') unsubs.push(encounters.onChange(gate(spec, spec.onEncounters)));
      if (typeof spec.onLifecycle   === 'function') unsubs.push(lifecycle.onChange(isolate(spec, spec.onLifecycle)));
      if (typeof spec.onMatchActive === 'function') unsubs.push(lifecycle.onMatchActive(isolate(spec, spec.onMatchActive)));
      if (typeof spec.onFocusChange === 'function') unsubs.push(focus.onChange(isolate(spec, spec.onFocusChange)));

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

  // ─── Widget control (desktop-overlay only) ─────────────────
  // When the plugin's overlay is loaded inside the Tauri-backed rl-widget,
  // these methods reshape the host window at runtime: resize to fit
  // content, re-anchor, fade, hide between matches, etc.
  //
  // In an OBS browser source / regular browser tab there is no host
  // window to reshape — every call is a silent no-op that resolves to
  // false, so plugin code stays portable.
  //
  // The detection is conservative: we look for window.__TAURI_INTERNALS__
  // (Tauri 2's invoke bridge). If it isn't present, we don't assume any
  // host capability.
  const widget = (function () {
    const inTauri = typeof window !== 'undefined'
      && !!window.__TAURI_INTERNALS__
      && typeof window.__TAURI_INTERNALS__.invoke === 'function';
    // Stable storage for autoSize across repeated calls.
    const autoSizeState = { observer: null, flush: null, active: false };
    // Stable storage for fitWidth so retoggling reuses the high-water mark.
    const fitWidthState = { observer: null };

    function invoke(cmd, args) {
      if (!inTauri) return Promise.resolve(false);
      try {
        return window.__TAURI_INTERNALS__.invoke(cmd, args || {})
          .then(() => true)
          .catch((e) => {
            console.warn('[RLT.widget]', cmd, 'failed:', e);
            return false;
          });
      } catch (e) {
        console.warn('[RLT.widget]', cmd, 'threw:', e);
        return Promise.resolve(false);
      }
    }

    return {
      /** True only when running inside the desktop widget (Tauri). */
      isHosted() { return inTauri; },

      /** Resize the host window. width/height in CSS (logical) pixels. */
      size(width, height) {
        return invoke('widget_size', { width: width | 0, height: height | 0 });
      },

      /** Anchor to a corner: 'top-left' | 'top-right' | 'bottom-left' | 'bottom-right'. */
      anchor(corner) {
        return invoke('widget_anchor', { anchor: String(corner || 'bottom-left') });
      },

      /** Padding (in logical pixels) from each anchored edge. */
      margin(x, y) {
        return invoke('widget_margin', { x: x | 0, y: y | 0 });
      },

      /** Opacity 0..1, applied to the whole window. */
      opacity(o) {
        return invoke('widget_opacity', { opacity: Number(o) });
      },

      /** Show / hide the window. Use to keep the widget out of the way
       *  between matches without tearing down the process. */
      visible(v) {
        return invoke('widget_visible', { visible: !!v });
      },

      /** Auto-resize the host window to fit a measurement target. Pass true
       *  to start, false to stop. Uses ResizeObserver and debounces calls
       *  to widget_size to one animation frame.
       *
       *  Options:
       *    target    — DOM element OR CSS selector to measure. Defaults
       *                to document.body. Pass the actual content wrapper
       *                (e.g. '.ov') so the body's flex centering doesn't
       *                inflate the measurement to the iframe's full size.
       *    minWidth, minHeight, maxWidth, maxHeight — clamps in CSS px.
       *
       *  Idempotent: subsequent calls re-target rather than stack. */
      autoSize(enabled, opts) {
        if (!inTauri) return false;
        opts = opts || {};
        const minW = (opts.minWidth | 0) || 1;
        const minH = (opts.minHeight | 0) || 1;
        const maxW = (opts.maxWidth | 0) || 4096;
        const maxH = (opts.maxHeight | 0) || 4096;

        const resolveTarget = () => {
          if (opts.target instanceof Element) return opts.target;
          if (typeof opts.target === 'string') {
            return document.querySelector(opts.target) || document.body;
          }
          return document.body;
        };

        // Tear down any previous observer so options take effect.
        if (autoSizeState.observer) {
          autoSizeState.observer.disconnect();
          autoSizeState.observer = null;
        }

        if (!enabled) {
          autoSizeState.active = false;
          return true;
        }

        let pending = false;
        let lastW = -1, lastH = -1;
        const flush = () => {
          pending = false;
          if (!autoSizeState.active) return;
          const el = resolveTarget();
          if (!el) return;
          // getBoundingClientRect respects transforms and gives us fractional
          // pixels; scrollWidth/Height ignores subpixel layout. The widget
          // surface is integer-pixel so we ceil to avoid clipping the last
          // row by half a pixel during fade-in animations.
          const r = el.getBoundingClientRect();
          const w = Math.max(minW, Math.min(maxW, Math.ceil(r.width)));
          const h = Math.max(minH, Math.min(maxH, Math.ceil(r.height)));
          if (w === lastW && h === lastH) return;
          lastW = w; lastH = h;
          invoke('widget_size', { width: w, height: h });
        };

        const observer = new ResizeObserver(() => {
          if (pending) return;
          pending = true;
          requestAnimationFrame(flush);
        });
        autoSizeState.observer = observer;
        autoSizeState.flush = flush;
        autoSizeState.active = true;

        // ResizeObserver only fires on the elements we observe. Watch the
        // target and document.body — the latter catches reflows that don't
        // change the target's box but do change its layout (e.g. a parent
        // flex container resizing).
        observer.observe(resolveTarget());
        if (resolveTarget() !== document.body && document.body) {
          observer.observe(document.body);
        }
        // Kick once so the first paint sizes correctly even if no
        // observer event fires.
        requestAnimationFrame(flush);

        // ResizeObserver doesn't fire on transform / opacity animations, so
        // a fade-in or slide-in finishing changes nothing observable. We
        // hook animation/transition end and font load — all common sources
        // of post-first-paint layout shift — and re-flush.
        const onAnimEnd = () => requestAnimationFrame(flush);
        document.addEventListener('animationend', onAnimEnd, true);
        document.addEventListener('transitionend', onAnimEnd, true);
        if (document.fonts && document.fonts.ready) {
          document.fonts.ready.then(() => requestAnimationFrame(flush));
        }
        return true;
      },

      /** Grow the host window's width to fit a measurement target's natural
       *  content width. Width is monotonic — it only grows, never shrinks —
       *  so there's no feedback loop with body's max-width:100% chain.
       *  Height is left at the manifest value.
       *
       *  Use this for "long player name pushes the row past the manifest
       *  width" — the surface widens to fit that name and stays widened
       *  for the session. (Shrinking back when the long name leaves would
       *  re-introduce the autoSize feedback loop.)
       *
       *  Options:
       *    target   — element OR selector to measure (default: document.body).
       *    maxWidth — absolute cap so a pathological name (AAAAA...) can't
       *               widen the surface to 4K. Defaults to 800px.
       *    extra    — extra px to add beyond measured width (e.g. for
       *               glow/padding the layout box doesn't include).
       *               Defaults to 0.
       *
       *  Returns false outside Tauri (OBS / browser) — no host to resize. */
      fitWidth(opts) {
        if (!inTauri) return false;
        opts = opts || {};
        const maxW = (opts.maxWidth | 0) || 800;
        const extra = (opts.extra | 0) || 0;
        const resolveTarget = () => {
          if (opts.target instanceof Element) return opts.target;
          if (typeof opts.target === 'string') {
            return document.querySelector(opts.target) || document.body;
          }
          return document.body;
        };

        // Tear down any prior fitWidth observer so options can be retuned.
        if (fitWidthState.observer) {
          fitWidthState.observer.disconnect();
          fitWidthState.observer = null;
        }

        let pending = false;
        // We track the largest width we've ever asked the host for. Each
        // tick we compare the natural content width to that high-water
        // mark and only invoke widget_size if we've grown.
        let highWater = 0;
        const flush = () => {
          pending = false;
          const el = resolveTarget();
          if (!el) return;
          // scrollWidth is the unconstrained content width — ignores
          // max-width / overflow:hidden / nowrap clipping. Exactly the
          // measurement we need: "how wide does this *want* to be".
          const wanted = Math.min(maxW, el.scrollWidth + extra);
          if (wanted <= highWater) return;
          highWater = wanted;
          // Pass the manifest height through unchanged — we only resize
          // width here. (window.innerHeight reflects the current surface
          // height, which is the manifest value at startup and any
          // explicit resize since.)
          invoke('widget_size', { width: wanted, height: window.innerHeight });
        };

        const observer = new ResizeObserver(() => {
          if (pending) return;
          pending = true;
          requestAnimationFrame(flush);
        });
        fitWidthState.observer = observer;
        observer.observe(resolveTarget());
        // Body too — content reflows that don't change target's box but do
        // change wrapping behavior (e.g. an iframe-width change) come
        // through body.
        if (resolveTarget() !== document.body && document.body) {
          observer.observe(document.body);
        }
        // First-paint kick.
        requestAnimationFrame(flush);
        // Re-measure after fade-ins finish and after web fonts arrive
        // (fonts shift name widths once they replace the system fallback).
        const onAnimEnd = () => requestAnimationFrame(flush);
        document.addEventListener('animationend', onAnimEnd, true);
        document.addEventListener('transitionend', onAnimEnd, true);
        if (document.fonts && document.fonts.ready) {
          document.fonts.ready.then(() => requestAnimationFrame(flush));
        }
        return true;
      },
    };
  })();

  // ─── RLT.focus ─────────────────────────────────────────────
  //
  // Foreground-window detection. Powered by the rl-widget Rust process,
  // which polls the OS every 250ms and pushes focus-change signals into
  // the webview via webview.eval('window.postMessage({ __rlt_focus__:
  // true, active }, "*")'). We listen for those messages and re-emit via
  // the SDK's emitter shape so plugins can subscribe with onChange(fn)
  // the same way they subscribe to onMatchActive, onIdentity, etc.
  //
  // Why postMessage and not Tauri's event.listen: Tauri 2's user-facing
  // event API only exists in the @tauri-apps/api JS package, which this
  // plain-JS SDK doesn't bundle. window.__TAURI_INTERNALS__ exposes the
  // IPC invoke primitive but no event listener. postMessage is the
  // simplest cross-runtime channel — it works inside Tauri *and* in any
  // browser tab (where the listener just sits idle waiting for messages
  // that never come, since browser pages have no game-focus concept).
  const focus = (function () {
    const ev = emitter();

    if (typeof window !== 'undefined' && typeof window.addEventListener === 'function') {
      window.addEventListener('message', (msg) => {
        // Filter on the sentinel field so we ignore unrelated postMessage
        // traffic (Tauri internals, devtools, third-party scripts).
        if (!msg || !msg.data || msg.data.__rlt_focus__ !== true) return;
        ev.emit('change', !!msg.data.active);
      });
    }

    return {
      /** Subscribe to focus-change events. fn receives a boolean (true =
       *  game is foreground, false = not). Returns an unsub function. */
      onChange(fn) { return ev.on('change', fn); },
    };
  })();

  // ─── Public API ────────────────────────────────────────────
  window.RLT = {
    plugin: plugin,         // registration API; .name kept below for back-compat
    pluginName: pluginName, // explicit name accessor
    version: 1,

    // raw event bus — adding a handler for a specific event also opts
    // that event into the server-side filter so the EventSource starts
    // delivering it. Wildcards ('*') don't add anything; the catchall
    // fires on whatever the bus already gets.
    on: (ev, fn) => {
      if (ev && ev !== '*') addEvent(ev);
      return bus.on(ev, fn);
    },
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
    stats,
    widget,
    focus,
  };

  // Auto-connect on load. Plugins can call RLT._reconnect() to force.
  window.RLT._reconnect = function () { if (es) { es.close(); es = null; } connect(); };
  // Defer the first connect so synchronous RLT.plugin.register({...})
  // calls in the page's <script> tags land in the filter before we
  // open the EventSource. Microtask is enough — most plugin pages
  // call register() inline. Plugins that register later trigger a
  // reconnect via addEvent's stale-filter path.
  Promise.resolve().then(connect);
})();
