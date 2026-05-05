/**
 * RL Toolkit — Plugin SDK (v1)
 *
 *   <script src="/sdk.js" data-plugin="my-plugin"></script>
 *
 * Exposes `window.RLT`. Plugin-author docs: docs/PLUGINS.md.
 */
(function () {
  // sdk.js is loaded as a classic <script>, not an ES module — the
  // IIFE-scoped strict mode is intentional and not redundant.
  // biome-ignore lint/suspicious/noRedundantUseStrict: classic script
  'use strict';

  if (window.RLT) return; // idempotent — second include is a no-op

  // ─── Plugin name discovery ─────────────────────────────────
  let pluginName = 'unknown';
  try {
    const cur = document.currentScript;
    if (cur?.dataset?.plugin) {
      pluginName = cur.dataset.plugin;
    } else {
      const m = location.pathname.match(/\/plugins\/([^/]+)\//);
      if (m) pluginName = m[1];
    }
  } catch (_) {
    /* noop */
  }

  // ─── Manifest discovery ────────────────────────────────────
  let pluginManifest = null;
  let manifestLoaded = false;
  const manifestSubs = new Set();
  function finalizeManifest(m) {
    pluginManifest = m;
    manifestLoaded = true;
    if (!m && pluginName !== 'unknown') {
      console.warn('[RLT] no manifest for "' + pluginName + '" — using fallback metadata.');
    }
    for (const fn of manifestSubs) {
      try {
        fn(pluginManifest);
      } catch (e) {
        console.error('[RLT] onManifest threw:', e);
      }
    }
    manifestSubs.clear();
  }
  const manifestPromise = fetch('/api/plugins')
    .then((r) => (r.ok ? r.json() : []))
    .then((list) => {
      finalizeManifest((list || []).find((p) => p?.name === pluginName) || null);
      return pluginManifest;
    })
    .catch((e) => {
      console.warn('[RLT] manifest fetch failed:', e);
      finalizeManifest(null);
      return null;
    });

  // ─── Overlay sizing + anchor honoring ──────────────────────
  let overlayHideWhenUnfocused = false;
  let overlayPhaseGate = null;
  const __rltUrlParams = new URLSearchParams(location.search);
  const __rltIsSettingsView = __rltUrlParams.has('settings');
  const __rltIsOverlay = __rltUrlParams.has('overlay');
  try {
    const params = __rltUrlParams;
    const inOverlay = __rltIsOverlay;
    const anchor = params.get('anchor') || 'top-left';
    overlayHideWhenUnfocused = inOverlay && params.has('hide_when_unfocused');
    const overlayGated = inOverlay && (params.has('hide_when_unfocused') || params.has('phases'));
    if (overlayGated) {
      const s = document.createElement('style');
      s.textContent = 'body{display:none!important}';
      s.id = '__rlt_gate_style';
      (document.head || document.documentElement).appendChild(s);
    }
    if (inOverlay && params.has('phases')) {
      const list = (params.get('phases') || '')
        .split(',')
        .map((s) => s.trim())
        .filter(Boolean);
      if (list.length) overlayPhaseGate = new Set(list);
    }
    if (inOverlay) {
      const vAlign = anchor.indexOf('bottom') >= 0 ? 'flex-end' : 'flex-start';
      const hAlign = anchor.indexOf('right') >= 0 ? 'flex-end' : 'flex-start';
      const apply = () => {
        const html = document.documentElement;
        const body = document.body;
        if (!body) return;
        body.classList.add('overlay-mode');
        html.style.margin = '0';
        html.style.padding = '0';
        html.style.height = '100%';
        body.style.margin = '0';
        body.style.padding = '0';
        body.style.minHeight = '100%';
        body.style.height = '100%';
        body.style.width = '100%';
        const gated = overlayHideWhenUnfocused || overlayPhaseGate !== null;
        body.style.display = gated ? 'none' : 'flex';
        body.style.flexDirection = 'column';
        body.style.alignItems = hAlign;
        body.style.justifyContent = vAlign;
        const gateStyle = document.getElementById('__rlt_gate_style');
        if (gateStyle) gateStyle.remove();
      };
      if (document.body) apply();
      else document.addEventListener('DOMContentLoaded', apply, { once: true });
    }
  } catch (_) {
    // noop: overlay-mode auto-sizing is best-effort. If URL parsing or
    // style assignment fails, the plugin's manual styles still apply.
  }

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
        if (set)
          for (const fn of set) {
            try {
              fn(...args);
            } catch (e) {
              console.error('[RLT]', ev, e);
            }
          }
        // wildcard
        const all = subs.get('*');
        if (all)
          for (const fn of all) {
            try {
              fn(ev, ...args);
            } catch (e) {
              console.error('[RLT] *', e);
            }
          }
      },
    };
  }

  // ─── SSE bridge + auto-subscription filter ──────────────────
  const bus = emitter();
  let status = 'disconnected';
  let es = null;

  // Hosted-bus mode: parent iframe fans SSE events via postMessage.
  const hostedBus = __rltUrlParams.has('__rlt_hosted');
  function postToHost(msg) {
    if (!hostedBus) return;
    try {
      window.parent.postMessage(msg, '*');
    } catch (_) {
      /* noop: cross-origin / detached parent */
    }
  }

  const requiredEvents = new Set([
    'MatchCreated',
    'MatchInitialized',
    'CountdownBegin',
    'RoundStarted',
    'MatchPaused',
    'MatchUnpaused',
    'GoalReplayStart',
    'GoalReplayWillEnd',
    'GoalReplayEnd',
    'MatchEnded',
    'PodiumStart',
    'MatchDestroyed',
    'ReplayCreated',
  ]);
  const subscribedEvents = new Set(requiredEvents);

  function addEvent(name) {
    if (!name || subscribedEvents.has(name)) return;
    subscribedEvents.add(name);
    if (hostedBus) {
      postToHost({ __rlt_bus_addEvent__: true, name });
      return;
    }
    if (es) {
      try {
        es.close();
      } catch (_) {
        /* noop */
      }
      es = null;
      connect();
    }
  }

  function buildEventsURL() {
    const events = Array.from(subscribedEvents).join(',');
    return '/events?events=' + encodeURIComponent(events);
  }

  // Stamp `isMe` + `encounter` onto every EnrichedPlayer-shaped object.
  function stampClientSideFields(node) {
    if (!node || typeof node !== 'object') return;
    if (Array.isArray(node)) {
      for (const item of node) stampClientSideFields(item);
      return;
    }
    if (
      typeof node.id === 'string' &&
      typeof node.name === 'string' &&
      typeof node.team === 'number' &&
      typeof node.isBot === 'boolean'
    ) {
      if (node.isMe === undefined || node.isMe === false) {
        node.isMe = identity._isMe(node.id);
      }
      if (node.encounter === undefined || node.encounter === null) {
        const enc = node.id ? encounters.get(node.id) : null;
        if (enc) node.encounter = enc;
      }
    }
    for (const key of Object.keys(node)) {
      const v = node[key];
      if (v && typeof v === 'object') stampClientSideFields(v);
    }
  }

  function dispatchEnvelope(msg) {
    if (!msg) return;
    const event = msg.Event ?? msg.event;
    const eventStatus = msg.Status ?? msg.status;
    if (event === '_ConnectionStatus') {
      status = eventStatus;
      bus.emit('_status', status);
      return;
    }
    if (event === '_Lifecycle') {
      bus.emit('_Lifecycle', msg);
      return;
    }
    if (event === '_RosterChanged') {
      stampClientSideFields(msg);
      bus.emit('_RosterChanged', msg);
      return;
    }
    // Other synthetic _-prefixed events ship pre-enriched at top level.
    if (typeof event === 'string' && event.length > 0 && event[0] === '_') {
      stampClientSideFields(msg);
      bus.emit(event, msg);
      return;
    }
    if (hostedBus && event && !subscribedEvents.has(event)) return;
    let data = msg.Data ?? msg.data;
    if (typeof data === 'string') {
      try {
        data = JSON.parse(data);
      } catch {
        data = null;
      }
    }
    bus.emit(event, data, msg);
  }

  function connect() {
    if (hostedBus) return;
    if (es !== null && es.readyState !== 2) return;
    es = new EventSource(buildEventsURL());
    es.onmessage = (e) => {
      clearReconnectWatchdog();
      let msg;
      try {
        msg = JSON.parse(e.data);
      } catch {
        return;
      }
      dispatchEnvelope(msg);
    };
    es.onerror = () => {
      if (es !== null && es.readyState === 2) {
        status = 'disconnected';
        bus.emit('_status', status);
        return;
      }
      armReconnectWatchdog();
    };
  }

  // 2× sseHeartbeat — only declare dead after sustained silence.
  const RECONNECT_WATCHDOG_MS = 30000;
  let reconnectWatchdog = null;
  function armReconnectWatchdog() {
    if (reconnectWatchdog) return;
    reconnectWatchdog = setTimeout(() => {
      reconnectWatchdog = null;
      if (es !== null && es.readyState !== 1 && status !== 'disconnected') {
        status = 'disconnected';
        bus.emit('_status', status);
      }
    }, RECONNECT_WATCHDOG_MS);
  }
  function clearReconnectWatchdog() {
    if (!reconnectWatchdog) return;
    clearTimeout(reconnectWatchdog);
    reconnectWatchdog = null;
  }

  window.addEventListener('pagehide', () => {
    if (es) {
      try {
        es.close();
      } catch (_) {
        /* noop: already-closed sockets throw */
      }
      es = null;
    }
    clearReconnectWatchdog();
    teardownWatchers();
  });

  // ─── K/V store wrappers ─────────────────────────────────────
  async function storeGet(ns, key) {
    try {
      const r = await fetch('/api/data/' + ns + (key ? '/' + key : ''));
      if (!r.ok) return null;
      return await r.json();
    } catch {
      return null;
    }
  }
  async function storeSet(ns, key, val) {
    try {
      await fetch('/api/data/' + ns + '/' + key, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(val),
      });
      return true;
    } catch {
      return false;
    }
  }
  async function storeDelete(ns, key) {
    try {
      await fetch('/api/data/' + ns + '/' + key, { method: 'DELETE' });
      return true;
    } catch {
      return false;
    }
  }

  function debouncedWriter(ns, key, getValue, ms) {
    let timer = null;
    return function flush() {
      if (timer) clearTimeout(timer);
      timer = setTimeout(() => storeSet(ns, key, getValue()), ms);
    };
  }

  let warnedReadOnlyStores = new Set();
  function makeNamespacedStore(ns, opts) {
    const allowWrites = !!(opts && opts.allowWrites) || __rltIsOverlay || __rltIsSettingsView;
    function readOnlyNoOp(action) {
      const key = ns + ':' + action;
      if (!warnedReadOnlyStores.has(key)) {
        warnedReadOnlyStores.add(key);
        console.warn('[RLT] store.' + action + ' ignored (read-only instance: ' + ns + ').');
      }
      return Promise.resolve(false);
    }
    return {
      get(key) {
        return storeGet(ns, key);
      },
      getAll() {
        return storeGet(ns, '');
      },
      set(key, val) {
        if (!allowWrites) return readOnlyNoOp('set');
        return storeSet(ns, key, val);
      },
      delete(key) {
        if (!allowWrites) return readOnlyNoOp('delete');
        return storeDelete(ns, key);
      },
    };
  }

  // ─── Bot detection ─────────────────────────────────────────
  function isBotId(id) {
    return typeof id === 'string' && id.startsWith('Bot|');
  }

  // ─── Identity (shared across all plugins) ──────────────────
  const identity = (function () {
    const ev = emitter();
    let myId = '';
    let loaded = false;

    async function load() {
      const cfg = await storeGet('_rlt', 'identity');
      if (cfg && typeof cfg.my_id === 'string') {
        myId = cfg.my_id;
      } else {
        // One-time migration from legacy dejavu location.
        const legacy = await storeGet('dejavu', 'config');
        if (legacy?.my_id) myId = legacy.my_id;
        await storeSet('_rlt', 'identity', { my_id: myId });
        if (legacy) await storeDelete('dejavu', 'config');
      }
      loaded = true;
      ev.emit('change', myId);
    }
    load();

    return {
      get id() {
        return myId;
      },
      isReady() {
        return loaded;
      },
      async set(id) {
        myId = (id || '').trim();
        await storeSet('_rlt', 'identity', { my_id: myId });
        ev.emit('change', myId);
      },
      async clear() {
        return this.set('');
      },
      onChange(fn) {
        return ev.on('change', fn);
      },
      _isMe(id) {
        return !!id && id === myId;
      },
    };
  })();

  // ─── Encounter ledger (shared across all plugins) ──────────
  const encounters = (function () {
    const ev = emitter();
    let map = {};
    let loaded = false;
    const persistShared = debouncedWriter('_rlt', 'encounters', () => map, 1500);

    async function load() {
      const fresh = await storeGet('_rlt', 'encounters');
      if (fresh) {
        map = fresh;
      } else {
        const legacy = await storeGet('dejavu', 'encounters');
        if (legacy) {
          map = legacy;
          for (const id of Object.keys(map)) {
            const e = map[id];
            const truth = Math.max(1, (e?.matches || []).length);
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
      if (!id) return false;
      const now = new Date().toISOString();
      if (!map[id]) {
        map[id] = { names: [name], count: 1, first_seen: now, last_seen: now, matches: [guid] };
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

    return {
      get(id) {
        return map[id] || null;
      },
      all() {
        return Object.assign({}, map);
      },
      isBotId,
      isReady() {
        return loaded;
      },
      onChange(fn) {
        return ev.on('change', fn);
      },
      _record: record,
    };
  })();

  // ─── Enriched match state ──────────────────────────────────
  const match = (function () {
    const ev = emitter();
    let cur = null;
    let lastFingerprint = '';
    const RECORDING_PHASES = new Set(['countdown', 'live', 'paused', 'replay']);

    function build(d) {
      const guid = d.MatchGuid || 'local';

      const players = (d.Players || []).map((p) => {
        const id = p.PrimaryId || '';
        const name = p.Name || 'Unknown';
        const enc = id ? encounters.get(id) : null;
        return {
          id,
          name,
          team: p.TeamNum,
          isMe: identity._isMe(id),
          isBot: isBotId(id),
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
          boosting: !!p.bBoosting,
          onGround: !!p.bOnGround,
          onWall: !!p.bOnWall,
          powersliding: !!p.bPowersliding,
          demolished: !!p.bDemolished,
          supersonic: !!p.bSupersonic,
          hasCar: !!p.bHasCar,
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

      const blue = players.filter((p) => p.team === 0);
      const orange = players.filter((p) => p.team === 1);
      const game = d.Game || null;
      const me = players.find((p) => p.isMe) || null;

      // Normalize Game.Teams into lowercase-keyed entries plus blue/orange
      // split shortcuts. ColorPrimary/ColorSecondary are raw hex strings
      // (no '#' prefix) — plugins prepend '#' when applying.
      const teams = Array.isArray(game?.Teams)
        ? game.Teams.filter((t) => t && typeof t.TeamNum === 'number')
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
      const replayInfo =
        hasFrame || hasElapsed
          ? {
              frame: hasFrame ? game.Frame : null,
              elapsed: hasElapsed ? game.Elapsed : null,
            }
          : null;

      return {
        guid,
        players,
        blue,
        orange,
        me,
        game,
        arena: game ? (game.Arena || '').replace(/_P$/, '').replace(/_/g, ' ') : '',
        clockSeconds: game ? game.TimeSeconds | 0 : null,
        overtime: !!game?.bOvertime,
        // bReplay covers both goal replays and history replays — the
        // authoritative way to detect either, instead of inferring from
        // GoalReplayStart/End edges.
        replay: !!game?.bReplay,
        hasWinner: !!game?.bHasWinner,
        winner: game?.Winner || '',
        scoreBlue: (game?.Teams?.find((t) => t.TeamNum === 0) || game?.Teams?.[0])?.Score | 0,
        scoreOrange: (game?.Teams?.find((t) => t.TeamNum === 1) || game?.Teams?.[1])?.Score | 0,
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
        ball: game?.Ball
          ? {
              speed: typeof game.Ball.Speed === 'number' ? game.Ball.Speed : null,
              teamNum: typeof game.Ball.TeamNum === 'number' ? game.Ball.TeamNum : null,
              lastTouchTeam:
                typeof game.Ball.TeamNum === 'number' && game.Ball.TeamNum !== 255
                  ? game.Ball.TeamNum
                  : null,
              raw: game.Ball,
            }
          : null,
        // Spectator camera target — only meaningful when bHasTarget.
        target: game?.bHasTarget ? game.Target || null : null,
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

    function buildFromRoster(guid, list) {
      const players = list.map((p) => {
        const id = p.id || '';
        const name = p.name || 'Unknown';
        const enc = id ? encounters.get(id) : null;
        return {
          id,
          name,
          team: p.team | 0,
          isMe: p.isMe === true,
          isBot: p.isBot === true,
          score: 0,
          goals: 0,
          assists: 0,
          saves: 0,
          shots: 0,
          demos: 0,
          touches: 0,
          carTouches: 0,
          boost: null,
          speed: null,
          boosting: false,
          onGround: false,
          onWall: false,
          powersliding: false,
          demolished: false,
          supersonic: false,
          hasCar: false,
          attacker: null,
          encounterCount: enc ? enc.count : 1,
          aliases: enc ? enc.names.filter((n) => n !== name) : [],
          firstSeen: enc ? enc.first_seen : null,
          lastSeen: enc ? enc.last_seen : null,
          platform: p.platform || '',
          raw: { PrimaryId: id, Name: name, TeamNum: p.team | 0 },
        };
      });
      return {
        guid,
        players,
        blue: players.filter((p) => p.team === 0),
        orange: players.filter((p) => p.team === 1),
        game: null,
        ball: null,
        target: null,
        raw: { Players: players.map((p) => p.raw) },
      };
    }

    function rosterFingerprintOf(view) {
      if (!view) return '';
      return (
        view.guid +
        '|' +
        identity.id +
        '|' +
        view.players
          .map((p) => p.id + ':' + p.team + ':' + (p.encounterCount > 1 ? 'r' : 'n'))
          .join(',')
      );
    }

    bus.on('_RosterChanged', (env) => {
      if (!env) return;
      const guid = env.matchGuid || env.match_guid || env.MatchGUID || 'local';
      const list = Array.isArray(env.players) ? env.players : [];
      cur = buildFromRoster(guid, list);

      if (RECORDING_PHASES.has(lifecycle.phase) && cur.players.length > 0) {
        if (recordRoster()) {
          cur = buildFromRoster(guid, list);
          lastFingerprint = '';
        }
      }

      ev.emit('roster', cur);
      const fp = rosterFingerprintOf(cur);
      if (fp !== lastFingerprint) {
        lastFingerprint = fp;
        ev.emit('change', cur);
      }
    });

    bus.on('UpdateState', (d) => {
      if (!d) return;
      cur = build(d);
      ev.emit('tick', cur);

      if (RECORDING_PHASES.has(lifecycle.phase) && cur.players.length > 0) {
        if (recordRoster()) {
          cur = build(cur.raw);
          lastFingerprint = '';
        }
      }

      emitTyped('UpdateState', cur);

      const fp = rosterFingerprintOf(cur);
      if (fp !== lastFingerprint) {
        lastFingerprint = fp;
        ev.emit('change', cur);
      }
    });

    function clear() {
      if (!cur) return;
      cur = null;
      lastFingerprint = '';
      ev.emit('change', null);
      ev.emit('tick', null);
    }

    bus.on('MatchDestroyed', clear);
    bus.on('_status', (s) => {
      if (s === 'disconnected') clear();
    });

    // Re-fingerprint when identity changes so isMe is up to date.
    identity.onChange(() => {
      lastFingerprint = '';
      if (cur) cur = build(cur.raw);
      ev.emit('change', cur);
    });

    // Re-stamp encounter fields when the ledger changes so plugins
    // that only subscribe to onRoster (and therefore never rebuild
    // cur from a fresh UpdateState tick) see up-to-date counts.
    encounters.onChange(() => {
      if (!cur) return;
      let changed = false;
      for (const p of cur.players) {
        const enc = p.id ? encounters.get(p.id) : null;
        const count = enc ? enc.count : 1;
        if (p.encounterCount !== count) {
          p.encounterCount = count;
          p.aliases = enc ? enc.names.filter((n) => n !== p.name) : [];
          p.firstSeen = enc ? enc.first_seen : null;
          p.lastSeen = enc ? enc.last_seen : null;
          changed = true;
        }
      }
      if (changed) {
        lastFingerprint = '';
        ev.emit('roster', cur);
        ev.emit('change', cur);
      }
    });

    // Catch-up recording: RL sends the first UpdateState (which
    // triggers _RosterChanged) before CountdownBegin, so the roster
    // often lands while lifecycle.phase is still "created" — outside
    // RECORDING_PHASES. When the phase later transitions into a
    // recording phase, re-run recordRoster so the encounter ledger
    // reflects the match. The encounters.onChange handler above
    // re-stamps cur.players when the ledger changes, so we don't
    // need to rebuild cur here. Read the phase from the raw
    // _Lifecycle envelope because the lifecycle module processes the
    // same event and may not have updated lifecycle.phase yet.
    bus.on('_Lifecycle', (snap) => {
      if (!snap) return;
      const p = String(snap.phase || 'none');
      if (!RECORDING_PHASES.has(p)) return;
      if (!cur || cur.players.length === 0) return;
      recordRoster();
    });

    return {
      get current() {
        return cur;
      },
      onChange(fn) {
        addEvent('UpdateState');
        return ev.on('change', fn);
      },
      onTick(fn) {
        addEvent('UpdateState');
        return ev.on('tick', fn);
      },
      subscribe() {
        addEvent('UpdateState');
      },
      onRoster(fn) {
        return ev.on('roster', fn);
      },
    };
  })();

  // ─── Per-plugin store ──────────────────────────────────────
  const store = makeNamespacedStore(pluginName);

  // ─── UI helpers ────────────────────────────────────────────
  const PLATFORM_ICONS = {
    steam:
      'M11.979 0C5.678 0 .511 4.86.022 11.037l6.432 2.658a3.4 3.4 0 0 1 1.912-.59q.094.001.188.006l2.861-4.142V8.91a4.53 4.53 0 0 1 4.524-4.524c2.494 0 4.524 2.031 4.524 4.527s-2.03 4.525-4.524 4.525h-.105l-4.076 2.911l.004.159a3.39 3.39 0 0 1-3.39 3.396a3.41 3.41 0 0 1-3.331-2.727L.436 15.27C1.862 20.307 6.486 24 11.979 24c6.627 0 11.999-5.373 11.999-12S18.605 0 11.979 0M7.54 18.21l-1.473-.61c.262.543.714.999 1.314 1.25a2.551 2.551 0 0 0 3.337-3.324a2.547 2.547 0 0 0-3.255-1.413l1.523.63a1.878 1.878 0 0 1-1.445 3.467zm11.415-9.303a3.02 3.02 0 0 0-3.015-3.015a3.015 3.015 0 1 0 3.015 3.015m-5.273-.005a2.264 2.264 0 1 1 4.531 0a2.267 2.267 0 0 1-2.266 2.265a2.264 2.264 0 0 1-2.265-2.265',
    epic: 'M3.537 0C2.165 0 1.66.506 1.66 1.879V18.44a4 4 0 0 0 .02.433c.031.3.037.59.316.92c.027.033.311.245.311.245c.153.075.258.13.43.2l8.335 3.491c.433.199.614.276.928.27h.002c.314.006.495-.071.928-.27l8.335-3.492c.172-.07.277-.124.43-.2c0 0 .284-.211.311-.243c.28-.33.285-.621.316-.92a4 4 0 0 0 .02-.434V1.879c0-1.373-.506-1.88-1.878-1.88zm13.366 3.11h.68c1.138 0 1.688.553 1.688 1.696v1.88h-1.374v-1.8c0-.369-.17-.54-.523-.54h-.235c-.367 0-.537.17-.537.539v5.81c0 .369.17.54.537.54h.262c.353 0 .523-.171.523-.54V8.619h1.373v2.143c0 1.144-.562 1.71-1.7 1.71h-.694c-1.138 0-1.7-.566-1.7-1.71V4.82c0-1.144.562-1.709 1.7-1.709zm-12.186.08h3.114v1.274H6.117v2.603h1.648v1.275H6.117v2.774h1.74v1.275h-3.14zm3.816 0h2.198c1.138 0 1.7.564 1.7 1.708v2.445c0 1.144-.562 1.71-1.7 1.71h-.799v3.338h-1.4zm4.53 0h1.4v9.201h-1.4zm-3.13 1.235v3.392h.575c.354 0 .523-.171.523-.54V4.965c0-.368-.17-.54-.523-.54z',
    playstation:
      'M8.984 2.596v17.547l3.915 1.261V6.688c0-.69.304-1.151.794-.991c.636.18.76.814.76 1.505v5.875c2.441 1.193 4.362-.002 4.362-3.152c0-3.237-1.126-4.675-4.438-5.827c-1.307-.448-3.728-1.186-5.39-1.502zm4.656 16.241l6.296-2.275c.715-.258.826-.625.246-.818c-.586-.192-1.637-.139-2.357.123l-4.205 1.5V14.98l.24-.085s1.201-.42 2.913-.615c1.696-.18 3.785.03 5.437.661c1.848.601 2.04 1.472 1.576 2.072c-.465.6-1.622 1.036-1.622 1.036l-8.544 3.107V18.86zM1.807 18.6c-1.9-.545-2.214-1.668-1.352-2.32c.801-.586 2.16-1.052 2.16-1.052l5.615-2.013v2.313L4.205 17c-.705.271-.825.632-.239.826c.586.195 1.637.15 2.343-.12L8.247 17v2.074c-.12.03-.256.044-.39.073c-1.939.331-3.996.196-6.038-.479z',
    xbox: 'M4.102 21.033A11.95 11.95 0 0 0 12 24a11.96 11.96 0 0 0 7.902-2.967c1.877-1.912-4.316-8.709-7.902-11.417c-3.582 2.708-9.779 9.505-7.898 11.417m11.16-14.406c2.5 2.961 7.484 10.313 6.076 12.912A11.94 11.94 0 0 0 24 12.004a11.95 11.95 0 0 0-3.57-8.536s-.027-.022-.082-.042a.8.8 0 0 0-.281-.045c-.592 0-1.985.434-4.805 3.246M3.654 3.426c-.057.02-.082.041-.086.042A11.96 11.96 0 0 0 0 12.004c0 2.854.998 5.473 2.661 7.533c-1.401-2.605 3.579-9.951 6.08-12.91c-2.82-2.813-4.216-3.245-4.806-3.245a.7.7 0 0 0-.281.046zM12 3.551S9.055 1.828 6.755 1.746c-.903-.033-1.454.295-1.521.339C7.379.646 9.659 0 11.984 0H12c2.334 0 4.605.646 6.766 2.085c-.068-.046-.615-.372-1.52-.339C14.946 1.828 12 3.545 12 3.545z',
    switch:
      'M14.176 24h3.674c3.376 0 6.15-2.774 6.15-6.15V6.15C24 2.775 21.226 0 17.85 0H14.1c-.074 0-.15.074-.15.15v23.7c-.001.076.075.15.226.15m4.574-13.199c1.351 0 2.399 1.125 2.399 2.398c0 1.352-1.125 2.4-2.399 2.4c-1.35 0-2.4-1.049-2.4-2.4c-.075-1.349 1.05-2.398 2.4-2.398M11.4 0H6.15C2.775 0 0 2.775 0 6.15v11.7C0 21.226 2.775 24 6.15 24h5.25c.074 0 .15-.074.15-.149V.15c.001-.076-.075-.15-.15-.15M9.676 22.051H6.15a4.194 4.194 0 0 1-4.201-4.201V6.15A4.194 4.194 0 0 1 6.15 1.949H9.6zM3.75 7.199c0 1.275.975 2.25 2.25 2.25s2.25-.975 2.25-2.25c0-1.273-.975-2.25-2.25-2.25s-2.25.977-2.25 2.25',
    bot: 'M9 3v2H7a2 2 0 0 0-2 2v2H3v2h2v2H3v2h2v2a2 2 0 0 0 2 2h2v2h2v-2h2v2h2v-2h2a2 2 0 0 0 2-2v-2h2v-2h-2v-2h2V9h-2V7a2 2 0 0 0-2-2h-2V3h-2v2h-2V3h-2v2H11V3H9zm-2 4h10v10H7V7zm2 2v6h6V9H9z',
  };
  function platformIconKey(platform) {
    if (!platform) return null;
    const p = String(platform).toLowerCase();
    if (p === 'steam') return 'steam';
    if (p === 'epic') return 'epic';
    if (p.startsWith('ps')) return 'playstation';
    if (p.startsWith('xbox')) return 'xbox';
    if (p === 'switch' || p.includes('nintendo')) return 'switch';
    return null;
  }

  function renderIcon(key) {
    const d = PLATFORM_ICONS[key];
    if (!d) return '';
    const title = key === 'bot' ? 'Bot' : key.charAt(0).toUpperCase() + key.slice(1);
    return (
      '<svg class="rlt-platform-icon" viewBox="0 0 24 24" aria-label="' +
      title +
      '" role="img">' +
      '<title>' +
      title +
      '</title>' +
      '<path fill="currentColor" d="' +
      d +
      '"/></svg>'
    );
  }

  const ui = {
    platformIcon(platform) {
      const key = platformIconKey(platform);
      return key ? renderIcon(key) : '';
    },
    playerIcon(p) {
      if (!p) return '';
      if (p.isBot) return renderIcon('bot');
      return p.platform ? this.platformIcon(p.platform) : '';
    },
    esc(s) {
      return String(s == null ? '' : s).replace(
        /[&<>"']/g,
        (c) =>
          ({
            '&': '&amp;',
            '<': '&lt;',
            '>': '&gt;',
            '"': '&quot;',
            "'": '&#39;',
          })[c],
      );
    },
    escAttr(s) {
      return String(s == null ? '' : s).replace(
        /[&"']/g,
        (c) =>
          ({
            '&': '&amp;',
            '"': '&quot;',
            "'": '&#39;',
          })[c],
      );
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
      if (window.CSS?.escape) return CSS.escape(s);
      return String(s == null ? '' : s).replace(/["\\]/g, '\\$&');
    },
    toast(msg, ms) {
      let t = document.getElementById('__rlt_toast');
      if (!t) {
        t = document.createElement('div');
        t.id = '__rlt_toast';
        t.className = 'rlt-toast';
        document.body.appendChild(t);
      }
      t.textContent = msg;
      requestAnimationFrame(() => {
        requestAnimationFrame(() => t.classList.add('rlt-toast--show'));
      });
      clearTimeout(t._timer);
      t._timer = setTimeout(() => t.classList.remove('rlt-toast--show'), ms || 2000);
    },
    matchBadgeLabel() {
      if (status !== 'connected') return 'offline';
      const m = match.current;
      if (!m) return 'idle';
      if (!m.players || m.players.length === 0) return 'lobby';
      return match.lifecycle?.phase || 'live';
    },
    bindStatusPill(elementId, onChange) {
      const paint = (s) => {
        const el = document.getElementById(elementId);
        if (!el) return;
        el.dataset.status = s;
        el.textContent = s === 'connected' ? 'live' : s;
        if (onChange) onChange(s);
      };
      paint(statusStableState.get());
      return statusStableState.onChange(paint);
    },
  };

  // ─── Typed events layer ────────────────────────────────────
  function resolvePlayerIn(roster, ref) {
    if (!ref) return null;
    let player = null;
    if (roster?.length) {
      if (ref.PrimaryId) {
        player = roster.find((p) => p.id === ref.PrimaryId) || null;
      }
      if (!player && typeof ref.Shortcut === 'number') {
        player = roster.find((p) => p.raw?.Shortcut === ref.Shortcut) || null;
      }
      if (!player && ref.Name) {
        player = roster.find((p) => p.name === ref.Name) || null;
      }
    }
    const id = player?.id || ref.PrimaryId || '';
    const enc = id ? encounters.get(id) : null;
    return {
      name: ref.Name || player?.name || 'Unknown',
      shortcut: typeof ref.Shortcut === 'number' ? ref.Shortcut : null,
      team: typeof ref.TeamNum === 'number' ? ref.TeamNum : player ? player.team : null,
      id,
      isMe: identity._isMe(id),
      isBot: isBotId(id),
      player, // full enriched player from the roster, or null
      encounter: enc, // full encounter record from the ledger, or null
      raw: ref,
    };
  }

  // Recent-events ring buffer so plugins booting mid-match can show context.
  // Useful for dedup ("did I already see this GoalScored?") and for "show
  // the last N events" debug panels. See RLT.events.recent(name, n).
  const recentByType = new Map(); // ev -> array of { at, data }
  const RECENT_LIMIT = 50;
  function recordRecent(ev, data) {
    let arr = recentByType.get(ev);
    if (!arr) {
      arr = [];
      recentByType.set(ev, arr);
    }
    arr.push({ at: Date.now(), data });
    if (arr.length > RECENT_LIMIT) arr.splice(0, arr.length - RECENT_LIMIT);
  }

  const eventsBus = emitter();

  function emitTyped(name, payload) {
    recordRecent(name, payload);
    eventsBus.emit(name, payload);
  }

  bus.on('ClockUpdatedSeconds', (d) => {
    if (!d) return;
    emitTyped('ClockUpdatedSeconds', {
      matchGuid: d.MatchGuid || null,
      seconds: d.TimeSeconds != null ? d.TimeSeconds : null,
      overtime: !!d.bOvertime,
      raw: d,
    });
  });

  // Pass-through events: wrap raw payload for consistent typed API.
  [
    'MatchCreated',
    'MatchInitialized',
    'MatchDestroyed',
    'MatchPaused',
    'MatchUnpaused',
    'CountdownBegin',
    'RoundStarted',
    'GoalReplayStart',
    'GoalReplayWillEnd',
    'GoalReplayEnd',
    'PodiumStart',
    'ReplayCreated',
    'GoalScored',
    'BallHit',
    'CrossbarHit',
    'StatfeedEvent',
    'MatchEnded',
  ].forEach((name) => {
    bus.on(name, (d) => {
      emitTyped(name, {
        matchGuid: d?.MatchGuid || null,
        raw: d || null,
      });
    });
  });

  // Bridge synthetics from raw bus → eventsBus for register()-style handlers.
  [
    '_StatfeedEvent',
    '_PlayerDemolished',
    '_UnknownStatfeed',
    '_BallHit',
    '_CrossbarHit',
    '_MatchEnded',
    '_GoalScored',
    '_OwnGoal',
    '_FlipReset',
    '_HatTrick',
    '_Save',
    '_EpicSave',
    '_Shot',
    '_Assist',
    '_Center',
    '_Clear',
    '_BicycleHit',
    '_FirstTouch',
    '_FirstBlood',
    '_OvertimeStarted',
    '_GoalReplayContext',
    '_MatchSummary',
    '_MatchMVP',
    '_LifecyclePhaseChanged',
    // UpdateState-diff synthetics — same bridge contract as the rest:
    // raw bus delivers them, register({ events }) handlers listen on
    // eventsBus, so they need a forward here or they silently no-op.
    '_PlayerJoined',
    '_PlayerLeft',
    '_PlayerScoreChanged',
    '_BoostPickup',
    '_BallPossessionChanged',
    '_TeamScoreChanged',
    '_DemoChain',
    '_FastestShotOfMatch',
    '_KickoffConverted',
  ].forEach((name) => {
    bus.on(name, (payload) => emitTyped(name, payload));
  });

  function makeOn(name) {
    return (fn) => {
      addEvent(name);
      return eventsBus.on(name, fn);
    };
  }
  const events = {
    on: (name, fn) => {
      addEvent(name);
      return eventsBus.on(name, fn);
    },
    off: (name, fn) => eventsBus.off(name, fn),
    recent(name, n) {
      const arr = recentByType.get(name) || [];
      return n ? arr.slice(-n) : arr.slice();
    },

    onUpdateState: makeOn('UpdateState'),
    onClockUpdatedSeconds: makeOn('ClockUpdatedSeconds'),
    onMatchCreated: makeOn('MatchCreated'),
    onMatchInitialized: makeOn('MatchInitialized'),
    onMatchDestroyed: makeOn('MatchDestroyed'),
    onMatchPaused: makeOn('MatchPaused'),
    onMatchUnpaused: makeOn('MatchUnpaused'),

    onCountdownBegin: makeOn('CountdownBegin'),
    onRoundStarted: makeOn('RoundStarted'),

    onGoalReplayStart: makeOn('GoalReplayStart'),
    onGoalReplayWillEnd: makeOn('GoalReplayWillEnd'),
    onGoalReplayEnd: makeOn('GoalReplayEnd'),

    onPodiumStart: makeOn('PodiumStart'),
    onReplayCreated: makeOn('ReplayCreated'),

    onEnrichedStatfeedEvent: makeOn('_StatfeedEvent'),
    onEnrichedBallHit: makeOn('_BallHit'),
    onEnrichedCrossbarHit: makeOn('_CrossbarHit'),
    onEnrichedMatchEnded: makeOn('_MatchEnded'),
    onEnrichedGoalScored: makeOn('_GoalScored'),
    onOwnGoal: makeOn('_OwnGoal'),
    onPlayerDemolished: makeOn('_PlayerDemolished'),
    onFlipReset: makeOn('_FlipReset'),
    onHatTrick: makeOn('_HatTrick'),
    onSave: makeOn('_Save'),
    onEpicSave: makeOn('_EpicSave'),
    onShot: makeOn('_Shot'),
    onAssist: makeOn('_Assist'),
    onCenter: makeOn('_Center'),
    onClear: makeOn('_Clear'),
    onBicycleHit: makeOn('_BicycleHit'),
    onPlayerJoined: makeOn('_PlayerJoined'),
    onPlayerLeft: makeOn('_PlayerLeft'),
    onPlayerScoreChanged: makeOn('_PlayerScoreChanged'),
    onBoostPickup: makeOn('_BoostPickup'),
    onBallPossessionChanged: makeOn('_BallPossessionChanged'),
    onTeamScoreChanged: makeOn('_TeamScoreChanged'),
    onFirstTouch: makeOn('_FirstTouch'),
    onFirstBlood: makeOn('_FirstBlood'),
    onOvertimeStarted: makeOn('_OvertimeStarted'),
    onDemoChain: makeOn('_DemoChain'),
    onFastestShotOfMatch: makeOn('_FastestShotOfMatch'),
    onKickoffConverted: makeOn('_KickoffConverted'),
    onLifecyclePhaseChanged: makeOn('_LifecyclePhaseChanged'),
    onGoalReplayContext: makeOn('_GoalReplayContext'),
    onMatchSummary: makeOn('_MatchSummary'),
    onUnknownStatfeed: makeOn('_UnknownStatfeed'),
  };

  // ─── Lifecycle ─────────────────────────────────────────────
  // Phases: none | created | countdown | live | paused | replay | ended | podium
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

    bus.on('_Lifecycle', (snap) => {
      if (snap) applySnapshot(snap);
    });

    bus.on('_status', (s) => {
      if (s !== 'disconnected') return;
      applySnapshot({ phase: 'none', match_active: false, match_guid: '', since: null });
    });

    return {
      get phase() {
        return phase;
      },
      get previous() {
        return prevPhase;
      },
      get matchActive() {
        return matchActive;
      },
      get guid() {
        return matchGUID;
      },
      get since() {
        return since;
      },
      onChange(fn) {
        return ev.on('change', fn);
      },
      onMatchActive(fn) {
        return matchActiveEv.on('change', fn);
      },
    };
  })();

  // Expose lifecycle on the existing match object for discoverability.
  match.lifecycle = lifecycle;
  match.onLifecycle = (fn) => lifecycle.onChange(fn);

  // ─── Event catalog ─────────────────────────────────────────
  // Compact table: [name, category, shape, livePhases, stability, since, desc, subscriptionGroup?]
  // Expanded into objects at runtime for the public API.
  const LR = ['live', 'replay'];
  const LRPC = ['live', 'replay', 'paused', 'countdown'];
  const catalogData = [
    [
      'UpdateState',
      'tick',
      'matchstate',
      LRPC,
      'stable',
      '1.0',
      'Match snapshot at PacketSendRate.',
    ],
    [
      'GoalScored',
      'scoring',
      'goal',
      LR,
      'stable',
      '1.0',
      'Scorer + assister + last touch + impact.',
    ],
    [
      'BallHit',
      'play',
      'ballhit',
      ['live'],
      'stable',
      '1.0',
      'Ball touched. Pre/post speed and location.',
    ],
    ['CrossbarHit', 'play', 'crossbar', ['live'], 'stable', '1.0', 'Ball hit a crossbar.'],
    [
      'StatfeedEvent',
      'stat',
      'statfeed',
      LR,
      'stable',
      '1.0',
      'Player earned a stat (demo, save, epic save, etc).',
    ],
    [
      'ClockUpdatedSeconds',
      'play',
      'clock',
      ['live', 'countdown'],
      'stable',
      '1.0',
      'Match clock changed by ≥1 second.',
    ],
    [
      'MatchCreated',
      'lifecycle',
      'match',
      '*',
      'stable',
      '1.0',
      'All teams replicated; lobby ready.',
    ],
    ['MatchInitialized', 'lifecycle', 'match', '*', 'stable', '1.0', 'First countdown started.'],
    ['CountdownBegin', 'lifecycle', 'match', '*', 'stable', '1.0', 'Round countdown began.'],
    [
      'RoundStarted',
      'lifecycle',
      'match',
      '*',
      'stable',
      '1.0',
      'Active gameplay started (countdown ended).',
    ],
    ['MatchPaused', 'lifecycle', 'match', '*', 'stable', '1.0', 'Match paused by an admin.'],
    ['MatchUnpaused', 'lifecycle', 'match', '*', 'stable', '1.0', 'Match resumed.'],
    ['GoalReplayStart', 'replay', 'match', '*', 'stable', '1.0', 'Goal replay began.'],
    [
      'GoalReplayWillEnd',
      'replay',
      'match',
      '*',
      'stable',
      '1.0',
      'Ball exploded during replay (fires only if not skipped).',
    ],
    ['GoalReplayEnd', 'replay', 'match', '*', 'stable', '1.0', 'Goal replay ended.'],
    [
      'MatchEnded',
      'lifecycle',
      'matchend',
      '*',
      'stable',
      '1.0',
      'Match decided. Has WinnerTeamNum.',
    ],
    ['PodiumStart', 'lifecycle', 'match', '*', 'stable', '1.0', 'Game entered podium state.'],
    ['MatchDestroyed', 'lifecycle', 'match', '*', 'stable', '1.0', 'Player left the match.'],
    [
      'ReplayCreated',
      'lifecycle',
      'match',
      '*',
      'stable',
      '1.0',
      'Match-history replay loaded (NOT goal replays).',
    ],
    [
      '_StatfeedEvent',
      'stat',
      'stat-enriched',
      LR,
      'provisional',
      '1.1',
      'StatfeedEvent with MainTarget/SecondaryTarget pre-resolved.',
      'statfeed:catchall',
    ],
    [
      '_BallHit',
      'play',
      'ballhit-enriched',
      ['live'],
      'provisional',
      '1.1',
      'BallHit with Players[] pre-resolved.',
    ],
    [
      '_CrossbarHit',
      'play',
      'crossbar-enriched',
      ['live'],
      'provisional',
      '1.1',
      'CrossbarHit with BallLastTouch.Player pre-resolved.',
    ],
    [
      '_MatchEnded',
      'lifecycle',
      'matchend-enriched',
      '*',
      'provisional',
      '1.1',
      'MatchEnded with winnerName + scoreBlue/scoreOrange resolved.',
    ],
    [
      '_GoalScored',
      'scoring',
      'goal-enriched',
      LR,
      'provisional',
      '1.1',
      'GoalScored with players resolved + scoringTeam/concedingTeam/isOwnGoal + modifiers.',
      'scoring',
    ],
    [
      '_OwnGoal',
      'scoring',
      'owngoal',
      LR,
      'provisional',
      '1.1',
      'Score-delta verified own goal. Higher confidence than _GoalScored.isOwnGoal.',
      'scoring',
    ],
    [
      '_PlayerDemolished',
      'stat',
      'demolish',
      LR,
      'provisional',
      '1.1',
      'Demolish with attacker/victim resolved + isSelfDemo/isTeamDemo flags.',
      'statfeed:promoted',
    ],
    [
      '_FlipReset',
      'stat',
      'stat-simple',
      LR,
      'provisional',
      '1.1',
      'FlipReset with MainTarget resolved + flipResetsThisMatch counter.',
      'statfeed:promoted',
    ],
    [
      '_HatTrick',
      'stat',
      'stat-simple',
      LR,
      'provisional',
      '1.1',
      'HatTrick with MainTarget resolved + goalsThisMatch counter.',
      'statfeed:promoted',
    ],
    [
      '_Save',
      'stat',
      'save',
      LR,
      'provisional',
      '1.1',
      'Save with MainTarget resolved + correlatedShot.',
      'statfeed:promoted',
    ],
    [
      '_EpicSave',
      'stat',
      'save',
      LR,
      'provisional',
      '1.1',
      'EpicSave variant. Mutually exclusive with _Save on the wire.',
      'statfeed:promoted',
    ],
    [
      '_Shot',
      'stat',
      'shot',
      LR,
      'provisional',
      '1.1',
      'Shot with MainTarget resolved + correlatedTouch.',
      'statfeed:promoted',
    ],
    [
      '_Assist',
      'stat',
      'assist',
      LR,
      'provisional',
      '1.1',
      'Assist with MainTarget resolved + correlatedGoal.',
      'statfeed:promoted',
    ],
    [
      '_Center',
      'stat',
      'stat-touch',
      LR,
      'provisional',
      '1.1',
      'Center with MainTarget resolved + correlatedTouch.',
      'statfeed:promoted',
    ],
    [
      '_Clear',
      'stat',
      'stat-touch',
      LR,
      'provisional',
      '1.1',
      'Clear with MainTarget resolved + correlatedTouch.',
      'statfeed:promoted',
    ],
    [
      '_BicycleHit',
      'stat',
      'stat-touch',
      LR,
      'provisional',
      '1.1',
      'BicycleHit with MainTarget resolved + correlatedTouch.',
      'statfeed:promoted',
    ],
    [
      '_PlayerJoined',
      'roster',
      'player-event',
      '*',
      'provisional',
      '1.1',
      'Roster diff: player appeared this tick.',
    ],
    [
      '_PlayerLeft',
      'roster',
      'player-event',
      '*',
      'provisional',
      '1.1',
      'Roster diff: player disappeared this tick.',
    ],
    [
      '_PlayerScoreChanged',
      'stat',
      'score-delta',
      '*',
      'provisional',
      '1.1',
      'Per-player stat diff. Only fields that moved appear in delta.',
    ],
    [
      '_BoostPickup',
      'play',
      'boost-pickup',
      ['live'],
      'provisional',
      '1.1',
      'Player Boost rose between ticks (not a respawn).',
    ],
    [
      '_BallPossessionChanged',
      'play',
      'possession',
      ['live'],
      'provisional',
      '1.1',
      'Game.Ball.TeamNum changed. before/after nullable.',
    ],
    [
      '_TeamScoreChanged',
      'scoring',
      'team-score-delta',
      LR,
      'provisional',
      '1.1',
      'A team Score moved. Fires alongside _GoalScored and _OwnGoal.',
      'scoring',
    ],
    [
      '_FirstTouch',
      'play',
      'first-touch',
      ['live'],
      'provisional',
      '1.1',
      'First BallHit after each RoundStarted. Re-arms every round.',
    ],
    [
      '_FirstBlood',
      'scoring',
      'first-blood',
      LR,
      'provisional',
      '1.1',
      'First _GoalScored of the match.',
    ],
    [
      '_OvertimeStarted',
      'lifecycle',
      'overtime-started',
      ['live'],
      'provisional',
      '1.1',
      'Rising edge of Game.bOvertime. Once per match.',
    ],
    [
      '_DemoChain',
      'stat',
      'demo-chain',
      LR,
      'provisional',
      '1.2',
      'Same attacker ≥2 demos within rolling 5s window.',
    ],
    [
      '_FastestShotOfMatch',
      'scoring',
      'fastest-shot',
      LR,
      'provisional',
      '1.2',
      'Per-match max ball speed surpassed.',
    ],
    [
      '_KickoffConverted',
      'scoring',
      'kickoff-converted',
      ['live'],
      'provisional',
      '1.2',
      'First _Shot or _GoalScored within ~10s of RoundStarted.',
    ],
    [
      '_LifecyclePhaseChanged',
      'lifecycle',
      'phase-transition',
      '*',
      'stable',
      '1.1',
      'Phase machine edge: from/to/duration/trigger.',
    ],
    [
      '_GoalReplayContext',
      'replay',
      'goal-replay-context',
      '*',
      'provisional',
      '1.1',
      'Full _GoalScored payload on the bReplay rising edge.',
    ],
    [
      '_MatchSummary',
      'lifecycle',
      'match-summary',
      '*',
      'provisional',
      '1.1',
      'Final scores, winner, MVP, full per-player stats after MatchEnded.',
    ],
    [
      '_MatchMVP',
      'lifecycle',
      'match-mvp',
      '*',
      'provisional',
      '1.1',
      'MVP Statfeed, independent of _MatchSummary timing.',
    ],
    [
      '_UnknownStatfeed',
      'stat',
      'unknown-statfeed',
      '*',
      'stable',
      '1.1',
      'Statfeed.EventName not in the verified registry.',
    ],
  ];
  events.catalog = catalogData.map((row) => {
    const entry = {
      name: row[0],
      category: row[1],
      shape: row[2],
      livePhases: row[3],
      stability: row[4],
      since: row[5],
      desc: row[6],
    };
    if (row[7]) entry.subscriptionGroup = row[7];
    return Object.freeze(entry);
  });
  events.byCategory = events.catalog.reduce((acc, e) => {
    if (!acc[e.category]) acc[e.category] = [];
    acc[e.category].push(e.name);
    return acc;
  }, {});
  Object.freeze(events.catalog);
  Object.freeze(events.byCategory);

  // ─── Statfeed eventName registry (server-inlined) ──────────
  const stats = /*__RLT_STATS__*/ {};
  stats.known = new Set(Object.values(stats));
  Object.freeze(stats);

  // ─── Plugin registration API ───────────────────────────────
  const plugin = (function () {
    const registry = []; // array of plugin records

    function shouldFire(spec) {
      if (!spec.whilePhase) return true;
      if (spec.whilePhase === '*') return true;
      const allow = Array.isArray(spec.whilePhase) ? spec.whilePhase : [spec.whilePhase];
      const cur = lifecycle.phase;
      if (allow.indexOf(cur) !== -1) return true;
      // 'idle' is a back-compat alias for 'none'.
      if (cur === 'none' && allow.indexOf('idle') !== -1) return true;
      return false;
    }

    function wrap(spec, fn, opts) {
      const phaseGated = !opts || opts.phaseGated !== false;
      return (...args) => {
        if (phaseGated && !shouldFire(spec)) return;
        try {
          return fn(...args);
        } catch (e) {
          console.error('[RLT] plugin "' + spec.name + '" handler threw:', e);
        }
      };
    }
    const gate = (spec, fn) => wrap(spec, fn);
    const isolate = (spec, fn) => wrap(spec, fn, { phaseGated: false });

    function register(spec) {
      spec = spec || {};
      const name = spec.name || pluginManifest?.name || pluginName;
      const unsubs = [];
      let disposed = false;
      const pluginStore = makeNamespacedStore(name, { allowWrites: !!spec.allowWrites });

      if (spec.events) {
        for (const evName of Object.keys(spec.events)) {
          const handler = spec.events[evName];
          if (typeof handler !== 'function') continue;
          if (evName !== '*') addEvent(evName);
          const sub =
            evName === '*'
              ? bus.on('*', gate(spec, handler))
              : eventsBus.on(evName, gate(spec, handler));
          unsubs.push(sub);
        }
      }

      // onLifecycle/onMatchActive/onFocusChange bypass whilePhase gating.
      if (typeof spec.onMatch === 'function') unsubs.push(match.onChange(gate(spec, spec.onMatch)));
      if (typeof spec.onTick === 'function') unsubs.push(match.onTick(gate(spec, spec.onTick)));
      if (typeof spec.onRoster === 'function')
        unsubs.push(match.onRoster(gate(spec, spec.onRoster)));
      if (typeof spec.onIdentity === 'function')
        unsubs.push(identity.onChange(gate(spec, spec.onIdentity)));
      if (typeof spec.onEncounters === 'function')
        unsubs.push(encounters.onChange(gate(spec, spec.onEncounters)));
      if (typeof spec.onLifecycle === 'function')
        unsubs.push(lifecycle.onChange(isolate(spec, spec.onLifecycle)));
      if (typeof spec.onMatchActive === 'function')
        unsubs.push(lifecycle.onMatchActive(isolate(spec, spec.onMatchActive)));
      if (typeof spec.onFocusChange === 'function')
        unsubs.push(focus.onChange(isolate(spec, spec.onFocusChange)));

      const handle = {
        name,
        version: spec.version || pluginManifest?.version || null,
        author: spec.author || pluginManifest?.author || null,
        title: spec.title || pluginManifest?.title || null,
        manifest: pluginManifest,
        get disposed() {
          return disposed;
        },
        store: pluginStore,
        events: Object.keys(spec.events || {}),
        spec,
        dispose() {
          if (disposed) return;
          disposed = true;
          for (const u of unsubs) {
            try {
              u();
            } catch {}
          }
          unsubs.length = 0;
          if (typeof spec.dispose === 'function') {
            try {
              spec.dispose();
            } catch (e) {
              console.error('[RLT] plugin "' + name + '" dispose threw:', e);
            }
          }
          const i = registry.indexOf(handle);
          if (i >= 0) registry.splice(i, 1);
        },
      };
      registry.push(handle);

      if (!manifestLoaded) {
        manifestPromise.then((m) => {
          if (!m || disposed) return;
          handle.manifest = m;
          if (!spec.name) handle.name = m.name || handle.name;
          if (!spec.version) handle.version = m.version || handle.version;
          if (!spec.author) handle.author = m.author || handle.author;
          if (!spec.title) handle.title = m.title || handle.title;
        });
      }

      if (typeof spec.init === 'function') {
        try {
          spec.init(handle);
        } catch (e) {
          console.error('[RLT] plugin "' + name + '" init threw:', e);
        }
      }
      let readyFired = false;
      const fireReady = () => {
        if (readyFired || disposed) return;
        if (!(identity.isReady() && encounters.isReady())) return;
        readyFired = true;
        if (typeof spec.ready === 'function') {
          try {
            spec.ready(handle);
          } catch (e) {
            console.error('[RLT] plugin "' + name + '" ready threw:', e);
          }
        }
      };
      if (identity.isReady() && encounters.isReady()) {
        // Defer to microtask to avoid TDZ on `const handle = register(…)`.
        Promise.resolve().then(fireReady);
      } else {
        unsubs.push(identity.onChange(fireReady));
        unsubs.push(encounters.onChange(fireReady));
      }

      const logRegistered = () =>
        console.debug('[RLT] plugin registered:', handle.name, handle.version || '(no version)');
      if (manifestLoaded || handle.version) {
        logRegistered();
      } else {
        manifestPromise.then(logRegistered);
      }
      return handle;
    }

    return {
      register,
      list() {
        return registry.map((h) => ({
          name: h.name,
          version: h.version,
          author: h.author,
          events: h.events.slice(),
          disposed: h.disposed,
        }));
      },
      get(name) {
        return registry.find((h) => h.name === name) || null;
      },
    };
  })();

  // ─── Widget sizing helpers ──────────────────────────────────
  function resolveTarget(target) {
    if (target instanceof Element) return target;
    if (typeof target === 'string') {
      return document.querySelector(target) || document.body;
    }
    return document.body;
  }

  const activeWatchers = new Set();

  function teardownWatchers() {
    for (const w of activeWatchers) {
      try {
        w.observer.disconnect();
      } catch (_) {
        /* noop: already disposed */
      }
      for (const { type, fn } of w.listeners) {
        try {
          document.removeEventListener(type, fn, true);
        } catch (_) {
          /* noop */
        }
      }
    }
    activeWatchers.clear();
  }

  function startSizeWatcher(getTarget, flush) {
    let pending = false;
    const schedule = () => {
      if (pending) return;
      pending = true;
      requestAnimationFrame(() => {
        pending = false;
        flush();
      });
    };

    const observer = new ResizeObserver(schedule);
    observer.observe(getTarget());
    if (getTarget() !== document.body && document.body) {
      observer.observe(document.body);
    }

    schedule();

    const listeners = [
      { type: 'animationend', fn: schedule },
      { type: 'transitionend', fn: schedule },
    ];
    for (const { type, fn } of listeners) {
      document.addEventListener(type, fn, true);
    }
    if (document.fonts?.ready) {
      document.fonts.ready.then(schedule);
    }

    const watcher = { observer, listeners };
    activeWatchers.add(watcher);
    return watcher;
  }

  function stopSizeWatcher(watcher) {
    if (!watcher) return;
    try {
      watcher.observer.disconnect();
    } catch (_) {
      /* noop */
    }
    for (const { type, fn } of watcher.listeners) {
      try {
        document.removeEventListener(type, fn, true);
      } catch (_) {
        /* noop */
      }
    }
    activeWatchers.delete(watcher);
  }

  // ─── Widget control (Tauri desktop-overlay only) ───────────
  const widget = (function () {
    const inTauri =
      typeof window !== 'undefined' &&
      !!window.__TAURI_INTERNALS__ &&
      typeof window.__TAURI_INTERNALS__.invoke === 'function';

    let autoSizeWatcher = null;
    let fitWidthWatcher = null;
    let fitWidthHighWater = 0;

    function invoke(cmd, args) {
      if (!inTauri) return Promise.resolve(false);
      try {
        return window.__TAURI_INTERNALS__
          .invoke(cmd, args || {})
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
      isHosted() {
        return inTauri;
      },
      size(width, height) {
        return invoke('widget_size', { width: width | 0, height: height | 0 });
      },
      anchor(corner) {
        return invoke('widget_anchor', { anchor: String(corner || 'bottom-left') });
      },
      margin(x, y) {
        return invoke('widget_margin', { x: x | 0, y: y | 0 });
      },
      opacity(o) {
        return invoke('widget_opacity', { opacity: Number(o) });
      },
      visible(v) {
        return invoke('widget_visible', { visible: !!v });
      },
      autoSize(enabled, opts) {
        if (!inTauri) return false;
        opts = opts || {};
        const minW = opts.minWidth | 0 || 1;
        const minH = opts.minHeight | 0 || 1;
        const maxW = opts.maxWidth | 0 || 4096;
        const maxH = opts.maxHeight | 0 || 4096;

        stopSizeWatcher(autoSizeWatcher);
        autoSizeWatcher = null;
        if (!enabled) return true;

        let lastW = -1,
          lastH = -1;
        const flush = () => {
          const el = resolveTarget(opts.target);
          if (!el) return;
          const r = el.getBoundingClientRect();
          const w = Math.max(minW, Math.min(maxW, Math.ceil(r.width)));
          const h = Math.max(minH, Math.min(maxH, Math.ceil(r.height)));
          if (w === lastW && h === lastH) return;
          lastW = w;
          lastH = h;
          invoke('widget_size', { width: w, height: h });
        };

        autoSizeWatcher = startSizeWatcher(() => resolveTarget(opts.target), flush);
        return true;
      },

      fitWidth(opts) {
        if (!inTauri) return false;
        opts = opts || {};
        const maxW = opts.maxWidth | 0 || 800;
        const extra = opts.extra | 0 || 0;

        stopSizeWatcher(fitWidthWatcher);
        fitWidthWatcher = null;

        const flush = () => {
          const el = resolveTarget(opts.target);
          if (!el) return;
          const wanted = Math.min(maxW, el.scrollWidth + extra);
          if (wanted <= fitWidthHighWater) return;
          fitWidthHighWater = wanted;
          invoke('widget_size', { width: wanted, height: window.innerHeight });
        };

        fitWidthWatcher = startSizeWatcher(() => resolveTarget(opts.target), flush);
        return true;
      },
    };
  })();

  // ─── Game-foreground focus detection ────────────────────────
  const focus = (function () {
    const ev = emitter();
    if (typeof window !== 'undefined' && typeof window.addEventListener === 'function') {
      window.addEventListener('message', (msg) => {
        if (msg?.data?.__rlt_focus__ !== true) return;
        ev.emit('change', !!msg.data.active);
      });
    }
    return {
      onChange(fn) {
        return ev.on('change', fn);
      },
    };
  })();

  // ─── Overlay visibility gates ───────────────────────────────
  if (overlayHideWhenUnfocused || overlayPhaseGate !== null) {
    let focusOK = !overlayHideWhenUnfocused; // not gating? always pass
    const phasePass = (p) => !overlayPhaseGate || overlayPhaseGate.has(p);
    let phaseOK = phasePass(lifecycle.phase);

    const repaint = () => {
      const body = document.body;
      if (!body) return;
      body.style.display = focusOK && phaseOK ? 'flex' : 'none';
    };

    if (overlayHideWhenUnfocused) {
      focus.onChange((active) => {
        focusOK = !!active;
        repaint();
      });
    }
    if (overlayPhaseGate !== null) {
      lifecycle.onChange((newPhase) => {
        phaseOK = phasePass(newPhase);
        repaint();
      });
    }
    repaint();
  }

  // ─── Stable connection status (debounced) ──────────────────
  const STATUS_DOWN_DEBOUNCE_MS = 3000;
  const statusStableState = (function () {
    const ev = emitter();
    let stable = status;
    let pending = null;

    bus.on('_status', (s) => {
      if (s === 'connected') {
        if (pending) {
          clearTimeout(pending);
          pending = null;
        }
        if (stable !== s) {
          stable = s;
          ev.emit('change', stable);
        }
        return;
      }
      if (stable !== 'connected') {
        if (stable !== s) {
          stable = s;
          ev.emit('change', stable);
        }
        return;
      }
      if (pending) clearTimeout(pending);
      pending = setTimeout(() => {
        pending = null;
        if (stable !== s) {
          stable = s;
          ev.emit('change', stable);
        }
      }, STATUS_DOWN_DEBOUNCE_MS);
    });

    return {
      get() {
        return stable;
      },
      onChange(fn) {
        return ev.on('change', fn);
      },
    };
  })();

  // ─── Utility helpers ────────────────────────────────────────
  const util = {
    rafBatcher(fn) {
      let scheduled = false;
      return function scheduleRender() {
        if (scheduled) return;
        scheduled = true;
        requestAnimationFrame(() => {
          scheduled = false;
          fn();
        });
      };
    },
  };

  // ─── Settings panel bridge ─────────────────────────────────
  const settingsApi = {
    close() {
      if (window.parent && window.parent !== window) {
        try {
          window.parent.postMessage({ type: 'rlt:settings:close' }, location.origin);
        } catch {
          /* noop */
        }
      }
    },
  };

  // ─── Public API ────────────────────────────────────────────
  window.RLT = {
    plugin: plugin,
    pluginName: pluginName,
    pluginManifest: () => pluginManifest,
    onManifest: (fn) => {
      if (manifestLoaded) {
        try {
          fn(pluginManifest);
        } catch (e) {
          console.error('[RLT] onManifest threw:', e);
        }
        return () => {};
      }
      manifestSubs.add(fn);
      return () => manifestSubs.delete(fn);
    },
    version: 1,

    on: (ev, fn) => {
      if (ev && ev !== '*') addEvent(ev);
      return bus.on(ev, fn);
    },
    off: (ev, fn) => bus.off(ev, fn),
    status: () => status,
    onStatus: (fn) => bus.on('_status', fn),
    statusStable: () => statusStableState.get(),
    onStatusStable: (fn) => statusStableState.onChange(fn),
    match,
    me: identity,
    encounters,
    store,
    ui,
    util,
    events,
    stats,
    widget,
    focus,
    isSettingsView: __rltIsSettingsView,
    settings: settingsApi,
  };

  window.RLT._reconnect = function () {
    if (es) {
      try {
        es.close();
      } catch {
        /* noop */
      }
      es = null;
    }
    connect();
  };

  Object.freeze(window.RLT);

  if (hostedBus) {
    window.addEventListener('message', (e) => {
      const data = e?.data;
      if (!data || data.__rlt_bus__ !== true || !data.msg) return;
      if (e.source !== window.parent) return;
      dispatchEnvelope(data.msg);
    });
    Promise.resolve().then(() => {
      postToHost({ __rlt_bus_hello__: true, events: Array.from(subscribedEvents) });
    });
  } else {
    Promise.resolve().then(connect);
  }
})();
