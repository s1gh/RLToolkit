/**
 * RL Toolkit — Plugin SDK
 *
 * Single-file IIFE that exposes a global `window.RLT` object to plugin
 * pages. Drop it in with one tag:
 *
 *   <script src="/sdk.js" data-plugin="my-plugin"></script>
 *
 * The SDK opens an SSE connection to the toolkit server, normalizes
 * Rocket League events into typed payloads, tracks per-player encounter
 * history, exposes a per-plugin key/value store, and (when running
 * inside the desktop widget) lets plugins reshape the host window.
 *
 * Plugin-author docs: docs/PLUGINS.md.
 * This file is the source of truth for the public surface — keep the
 * two in sync when you change either.
 *
 * The whole file runs in strict mode. Plugin code does NOT inherit
 * strict mode unless the plugin opts in via its own `'use strict'`.
 *
 * Map of the file (jump targets):
 *   - Plugin name discovery .................. ~line  43
 *   - Overlay-mode auto-sizing ................ ~line  60
 *   - Tiny pub/sub emitter .................... ~line 100
 *   - SSE bridge + auto-subscription filter ... ~line 125
 *   - Persistent K/V store wrappers ........... ~line 225
 *   - Identity (shared, "who is me") .......... ~line 260
 *   - Encounter ledger (shared) ............... ~line 305
 *   - Enriched match state .................... ~line 390
 *   - Per-plugin namespaced store ............. ~line 635
 *   - UI helpers (icons, esc, toast) .......... ~line 645
 *   - Typed event layer + payload normalizers . ~line 740
 *   - Lifecycle (driven by _Lifecycle SSE) .... ~line 960
 *   - Event catalog (self-documenting) ........ ~line 1030
 *   - Statfeed eventName registry ............. ~line 1070
 *   - Plugin registration API ................. ~line 1095
 *   - Widget control (Tauri host only) ........ ~line 1265
 *   - Game-foreground focus detection ......... ~line 1430
 *   - Public API surface (window.RLT) ......... ~line 1465
 *
 * @version 1
 */
(function () {
  // sdk.js is loaded as a classic <script>, not an ES module — the
  // IIFE-scoped strict mode is intentional and not redundant.
  // biome-ignore lint/suspicious/noRedundantUseStrict: classic script
  'use strict';

  if (window.RLT) return; // idempotent — second include is a no-op

  // ─── Determine plugin name ─────────────────────────────────
  // Plugins identify themselves with <script src="/sdk.js" data-plugin="name">.
  // Falls back to the path segment under /plugins/ when the attribute is
  // missing (so it Just Works in most cases).
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
    // noop: leave pluginName as 'unknown'. document.currentScript is
    // null when the SDK is loaded as a module or via dynamic import,
    // and location.pathname is unreadable in some sandboxed contexts.
  }

  // ─── Manifest discovery ────────────────────────────────────
  // Fetch the plugin's manifest at startup so register() can default
  // name/version/author from the single source of truth (the manifest
  // file the toolkit's server already reads). Plugin code can then
  // call `RLT.plugin.register({ init, events, ... })` with no metadata
  // duplication.
  //
  // The fetch is async; register() may run before it resolves. In that
  // case the handle's metadata fields start with whatever spec/fallback
  // values were known synchronously, then get patched in-place once the
  // manifest arrives. Plugin handlers don't depend on these fields, so
  // the patch-after-the-fact is invisible.
  let pluginManifest = null;
  let manifestLoaded = false; // distinguishes "still fetching" from "fetched, no entry"
  const manifestSubs = new Set();
  function finalizeManifest(m) {
    pluginManifest = m;
    manifestLoaded = true;
    if (!m && pluginName !== 'unknown') {
      // The server-side requireManifest middleware rejects requests for
      // any plugin without a valid manifest, so reaching this branch
      // typically means the plugin's HTML is hosted *outside* the
      // toolkit (e.g. served from another origin while still importing
      // /sdk.js). The runtime keeps working with fallback metadata,
      // but the toolkit's discoverability surface (dashboard list,
      // unified overlay) won't see it.
      console.warn(
        '[RLT] no manifest matched plugin name "' +
          pluginName +
          '" via /api/plugins. The SDK will use fallback metadata. ' +
          'If this plugin is hosted by the toolkit, ensure plugins/' +
          pluginName +
          '/manifest.json exists and parses.',
      );
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
  // When the page is loaded inside the composite overlay's iframe, the
  // manifest's width/height defines the plugin's canvas. We force html/body
  // to fill that canvas and pin the plugin's content to the manifest's
  // anchor corner — so anchor:bottom-left + offset 0,0 actually means
  // "flush bottom-left of the iframe", regardless of how big the plugin's
  // content is. No plugin code required.
  //
  // hide_when_unfocused / show_during_phase (URL flags forwarded by the
  // overlay host from the manifest): when present, default-hide the
  // entire body until the host signals BOTH that RL is foreground AND
  // that the current lifecycle phase is in the allowed set. The two
  // gates AND together — a plugin opted into both must clear both to
  // appear. Implemented at the SDK level so plugins don't hand-roll
  // the same init/onFocusChange/onPhase dance every time.
  //
  // Editor preview never sets these flags — the user is editing in the
  // dashboard tab where RL isn't focused and there's no live phase, so
  // hiding every widget there would make the editor unusable.
  let overlayHideWhenUnfocused = false;
  let overlayPhaseGate = null; // null = no gate; Set<string> = whitelist
  const __rltUrlParams = new URLSearchParams(location.search);
  const __rltIsSettingsView = __rltUrlParams.has('settings');
  try {
    const params = __rltUrlParams;
    const inOverlay = params.has('overlay');
    const anchor = params.get('anchor') || 'top-left';
    overlayHideWhenUnfocused = inOverlay && params.has('hide_when_unfocused');
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
        // Pin content to the manifest's anchor corner. If either gate
        // is on (hide-when-unfocused or show-during-phase), start with
        // display:none so nothing paints before the gates clear; the
        // combined subscriber below restores 'flex' once both pass.
        // Otherwise apply 'flex' immediately (default behavior).
        const gated = overlayHideWhenUnfocused || overlayPhaseGate !== null;
        body.style.display = gated ? 'none' : 'flex';
        body.style.flexDirection = 'column';
        body.style.alignItems = hAlign;
        body.style.justifyContent = vAlign;
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

  // ─── SSE bridge + server-side event filter (auto-subscription) ──
  //
  // The toolkit's SSE endpoint takes a comma-separated event filter:
  //   GET /events?events=GoalScored,StatfeedEvent
  // and only delivers events on that list. Plugins almost never set
  // it directly — instead, the SDK tracks which event names plugin
  // handlers have asked for and rebuilds the filter URL on the fly.
  // When `addEvent(name)` is called after a connection is already
  // open, we close and reopen the EventSource so the server starts
  // delivering the new event.
  //
  // Five entry points opt into an event:
  //   - register({ events: { Foo(...) } })  → addEvent('Foo')
  //   - RLT.events.on('Foo', fn)            → addEvent('Foo')
  //   - RLT.events.onFoo(fn)                → addEvent('Foo') via makeOn
  //   - RLT.match.onTick / onChange / subscribe → addEvent('UpdateState')
  //   - RLT.on('Foo', fn)                   → addEvent('Foo') (raw bus)
  //
  // `requiredEvents` is the always-on baseline — lifecycle events
  // that drive `RLT.match.lifecycle` and reset semantics. They're
  // rare (a handful per match) so they're free to subscribe to.
  //
  // `UpdateState` is intentionally NOT in the baseline. It's the
  // heaviest event by far (~1-3 KB at 60-120 Hz, dominates total
  // bandwidth) and most plugins don't need it. Plugins that only
  // react to discrete events stay off the tick stream.
  //
  // Synthetic events (`_ConnectionStatus`, `_Lifecycle`) bypass the
  // filter entirely on the server — they're framing signals every
  // subscriber needs. Don't list them here.
  const bus = emitter();
  let status = 'disconnected';
  let es = null;

  // Hosted-bus mode: when the unified overlay aggregator (/overlay) is
  // hosting us in an iframe, it opens a single shared EventSource and
  // fans every parsed envelope to all child iframes via postMessage.
  // We skip our own EventSource entirely in that case — saves N TCP
  // connections, N JSON parses per event, and N SDK-wide reconnect
  // storms when the toolkit briefly drops.
  //
  // Detection is synchronous via a URL flag the parent adds to every
  // iframe src (`&__rlt_hosted=1`). We can't poll postMessage at module
  // load and a `parent !== window` check would false-positive on OBS
  // browser sources that have no shared bus. The flag is a clean
  // explicit handshake.
  const hostedBus = __rltUrlParams.has('__rlt_hosted');
  // Tell the parent which events we want. Parent unions across all
  // iframes and updates its single EventSource filter accordingly.
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

  // Idempotent — re-registering a handler for the same event is free.
  function addEvent(name) {
    if (!name || subscribedEvents.has(name)) return;
    subscribedEvents.add(name);
    if (hostedBus) {
      // Hosted mode: tell the parent we want this event. The parent
      // unions across all iframes and updates its single EventSource
      // filter accordingly. No reconnect on our side — we don't own
      // the socket.
      postToHost({ __rlt_bus_addEvent__: true, name });
      return;
    }
    if (es) {
      // Already connected with the prior filter; reconnect so the
      // server starts delivering the new event. Cheap on localhost.
      try {
        es.close();
      } catch (_) {
        /* noop: already-closed sockets throw */
      }
      es = null;
      connect();
    }
  }

  function buildEventsURL() {
    const events = Array.from(subscribedEvents).join(',');
    return '/events?events=' + encodeURIComponent(events);
  }

  // Dispatch a parsed envelope to the bus. Shared between the direct
  // EventSource path and the hosted-bus path — both paths see the same
  // wire shape (PascalCase or lowercase keys, synthetic _-prefixed
  // events, JSON-encoded Data strings) and need identical handling.
  //
  // In hosted-bus mode the parent forwards every event regardless of
  // what each iframe asked for (the parent's server-side filter is the
  // union across all iframes), so we filter client-side here against
  // our own subscribedEvents set to keep plugin handlers from firing
  // for events they didn't opt into.
  function dispatchEnvelope(msg) {
    if (!msg) return;
    // The RL Stats API has shipped both PascalCase ("Event"/"Data"/"Status")
    // and all-lowercase ("event"/"data"/"status") envelopes across versions.
    // Accept either at this boundary so the rest of the SDK stays PascalCase.
    // Synthetic envelopes from our own server (_ConnectionStatus, _Lifecycle)
    // are always PascalCase, so they fall out of the same checks.
    const event = msg.Event ?? msg.event;
    const eventStatus = msg.Status ?? msg.status;
    if (event === '_ConnectionStatus') {
      status = eventStatus;
      bus.emit('_status', status);
      return;
    }
    // Synthetic _Lifecycle: snapshot fields live at the top level
    // (match_active / phase / match_guid / since), not inside Data.
    // Hand the whole envelope to the lifecycle subscriber.
    if (event === '_Lifecycle') {
      bus.emit('_Lifecycle', msg);
      return;
    }
    // Synthetic _RosterChanged: top-level match_guid + players,
    // not inside Data. Hand the whole envelope through.
    if (event === '_RosterChanged') {
      bus.emit('_RosterChanged', msg);
      return;
    }
    // Other synthetic _-prefixed events (e.g. _StatfeedEvent, _BallHit,
    // _CrossbarHit, _MatchEnded, _GoalScored) ship pre-enriched: their
    // payload lives directly on the top-level envelope, not inside a
    // JSON-encoded Data string. Hand the whole envelope to the bus so
    // typed handlers receive the enriched shape verbatim. The server
    // emits these alongside their raw counterparts, never instead of —
    // direct-mode plugins still see GoalScored, BallHit, etc.
    if (typeof event === 'string' && event.length > 0 && event[0] === '_') {
      bus.emit(event, msg);
      return;
    }
    // In hosted mode the parent broadcasts events the union of all
    // iframes' filters — drop any we didn't personally opt into so
    // typed handlers don't fire for unwanted events. Synthetic
    // events (_ prefix) are handled above; this filter only applies
    // to RL-side events.
    if (hostedBus && event && !subscribedEvents.has(event)) return;
    // Decode the inner JSON-encoded Data payload — RL ships it as a string.
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
    if (hostedBus) return; // host owns the socket; nothing to do
    // Guard reentry: if we already have a healthy (or still-connecting)
    // EventSource, leave it alone. Only proceed if there's no socket or
    // the prior one is CLOSED — that catches the race between onerror
    // firing and the addEvent path also calling connect().
    //   readyState: 0=CONNECTING, 1=OPEN, 2=CLOSED
    // Already have a healthy (or still-connecting) socket? Nothing to do.
    if (es !== null && es.readyState !== 2) return;
    es = new EventSource(buildEventsURL());
    es.onmessage = (e) => {
      // Any message proves the link is alive — cancel a pending
      // disconnect-watchdog if one was armed by a recent onerror.
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
      // EventSource fires onerror for transient interruptions too — Firefox
      // logs a warning even though the browser auto-reconnects. Only signal
      // 'disconnected' to plugins if the connection is truly CLOSED, so we
      // don't churn plugin state on every blip.
      //   readyState: 0=CONNECTING, 1=OPEN, 2=CLOSED
      if (es !== null && es.readyState === 2) {
        status = 'disconnected';
        bus.emit('_status', status);
        return;
      }
      // While CONNECTING (auto-reconnect in flight), most browsers stay in
      // this state forever when the server is unreachable — they don't
      // transition to CLOSED. Without a watchdog, the SDK would never
      // emit 'disconnected' for a stopped server. Arm a timer; if the
      // connection comes back (onmessage fires, see clearReconnectWatchdog
      // below) we cancel it. If it doesn't, we declare the link dead.
      armReconnectWatchdog();
    };
  }

  // Watchdog: when EventSource is stuck in CONNECTING after an error, we
  // give the browser a window to recover. If a real message (or
  // server-sent _ConnectionStatus) arrives, the watchdog is cancelled
  // and status reflects whatever the server said. Otherwise we mark the
  // link disconnected so plugin UI doesn't lie about being live.
  //
  // The window is 2× sseHeartbeat (server pings every 15s) so a single
  // missed ping doesn't trip the watchdog — only a sustained silence.
  const RECONNECT_WATCHDOG_MS = 30000;
  let reconnectWatchdog = null;
  function armReconnectWatchdog() {
    if (reconnectWatchdog) return;
    reconnectWatchdog = setTimeout(() => {
      reconnectWatchdog = null;
      // Only emit if we haven't recovered in the meantime. Recovery is
      // detected by a real message arriving (clearReconnectWatchdog
      // is called from the onmessage path).
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
      try {
        es.close();
      } catch (_) {
        /* noop: already-closed sockets throw */
      }
      es = null;
    }
    clearReconnectWatchdog();
    // Tear down widget-watcher observers + their document listeners so
    // bfcache restores don't end up with stale closures referencing a
    // disposed flush() function.
    teardownWatchers();
  });

  // ─── Shared store wrappers ─────────────────────────────────
  // Per-plugin namespace: /api/data/<plugin>/<key>
  // Shared namespace:    /api/data/_rlt/<key>
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

  // Debounced writer to avoid hammering the disk on rapid changes.
  function debouncedWriter(ns, key, getValue, ms) {
    let timer = null;
    return function flush() {
      if (timer) clearTimeout(timer);
      timer = setTimeout(() => storeSet(ns, key, getValue()), ms);
    };
  }

  // Build a namespaced K/V store. Used both for the page-level
  // `RLT.store` (scoped to the host plugin) and for the per-plugin
  // `handle.store` returned by `register()`. Same shape, different
  // namespace — the only thing that varies is which folder the data
  // lands in on disk (data/<ns>.json).
  function makeNamespacedStore(ns) {
    return {
      get(key) {
        return storeGet(ns, key);
      },
      getAll() {
        return storeGet(ns, '');
      },
      set(key, val) {
        return storeSet(ns, key, val);
      },
      delete(key) {
        return storeDelete(ns, key);
      },
    };
  }

  // ─── Bot detection ─────────────────────────────────────────
  // RL ships every CPU-controlled player under the same sentinel
  // PrimaryId. We don't try to fingerprint individual bots — they share
  // one record in the ledger and one row in any roster scan. Plugins
  // should treat `Player.isBot` as the canonical check; the literal
  // string below is internal and may change if RL ever revises its
  // wire format.
  const BOT_PRIMARY_ID = 'Unknown|0|0';
  function isBotId(id) {
    return id === BOT_PRIMARY_ID;
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
      const cfg = await storeGet('_rlt', 'identity');
      if (cfg && typeof cfg.my_id === 'string') {
        myId = cfg.my_id;
      } else {
        // One-time migration from the legacy dejavu-only location.
        // Always write back (even if empty) so subsequent loads take
        // the fast path and never look at the legacy slot again.
        const legacy = await storeGet('dejavu', 'config');
        if (legacy?.my_id) {
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
      // exposed so match-state can flag isMe correctly even before load resolves.
      // Returns a real boolean (not the empty string) so plugin code that
      // does `player.isMe === true` behaves predictably.
      _isMe(id) {
        return !!id && id === myId;
      },
    };
  })();

  // ─── Encounter ledger (shared across all plugins) ──────────
  const encounters = (function () {
    const ev = emitter();
    let map = {}; // PrimaryId -> { names, count, first_seen, last_seen, matches }
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

    // Returns true when the count was created or bumped (i.e. a new
    // player-match pair was observed). The per-tick top-up uses the
    // return value to decide whether to rebuild the cached match
    // snapshot — same-guid re-records are no-ops on the count.
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
      // Whether a ledger key (or any RL PrimaryId) refers to the
      // aggregate-bot record. Use this when iterating the raw ledger
      // — for live-roster players, `Player.isBot` is shorter and the
      // canonical check.
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
    let cur = null; // null when no match
    let lastFingerprint = '';
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

    // Build a match view from a _RosterChanged payload. Same shape as
    // build(d) above so plugins reading match.current see one
    // consistent player object regardless of which event triggered the
    // build. Per-tick fields (score, boost, demos, …) default to 0 /
    // null because the roster payload doesn't carry them; if a plugin
    // subscribes to both onRoster and onTick/onMatch the next
    // UpdateState will overwrite with the full physics state.
    function buildFromRoster(guid, list) {
      const players = list.map((p) => {
        const id = p.id || '';
        const name = p.name || 'Unknown';
        const enc = id ? encounters.get(id) : null;
        return {
          id,
          name,
          team: p.team | 0,
          isMe: identity._isMe(id),
          isBot: isBotId(id),
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
          platform: p.platform || (id ? id.split('|')[0] : '?'),
          // No raw RL frame available on a roster-only build. Stash a
          // minimal stub mimicking the RL Players shape so recordRoster
          // (which reads cur.raw.Players for ledger writes) keeps
          // working when only roster events are flowing. Without this,
          // a plugin running roster-only (e.g. dejavu) wouldn't bump
          // the encounter ledger on its first match.
          raw: { PrimaryId: id, Name: name, TeamNum: p.team | 0 },
        };
      });
      return {
        guid,
        players,
        blue: players.filter((p) => p.team === 0),
        orange: players.filter((p) => p.team === 1),
        // Roster events don't carry game/ball/target. Plugins that need
        // those should opt into UpdateState explicitly via onTick /
        // onMatch, which will land on the next tick and overwrite cur.
        game: null,
        ball: null,
        target: null,
        raw: { Players: players.map((p) => p.raw) },
      };
    }

    // Same fingerprint shape `change` uses below — id/team/r-or-n.
    // Keeps the 'change' event behaviour identical between the
    // UpdateState path and the _RosterChanged path so plugins that
    // wrote onMatch see no difference whichever event fired first.
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

    // _RosterChanged is the toolkit's synthetic event for "roster
    // identity moved" — fires on player join/leave/team-switch and
    // on match-guid change. Lighter-weight than UpdateState (a few
    // events per match instead of 60-120Hz), so plugins that only
    // need to know who's on the field can subscribe via match.onRoster
    // and skip UpdateState entirely.
    //
    // We build a match view from the roster payload alone — score,
    // boost, demos and other per-tick stats default to 0/null. If
    // the same plugin is also subscribed to UpdateState the next tick
    // overwrites cur with the richer view. Plugins that want full
    // per-tick state should use onTick / onMatch (which transitively
    // subscribe to UpdateState).
    bus.on('_RosterChanged', (env) => {
      if (!env) return;
      // The synthetic envelope arrives with top-level fields, not the
      // standard {Event, Data} shape — see backend/roster_tracker.go.
      const guid = env.match_guid || env.MatchGUID || 'local';
      const list = Array.isArray(env.players) ? env.players : [];
      cur = buildFromRoster(guid, list);

      // Record the new roster against the ledger, gated on phase
      // (same convention as the UpdateState path). Without this, a
      // plugin running roster-only (no UpdateState subscription)
      // would never bump encounter counts. The ledger dedups
      // per-(player, guid) so this stays idempotent across ticks.
      if (RECORDING_PHASES.has(lifecycle.phase) && cur.players.length > 0) {
        if (recordRoster()) {
          // Re-build so encounterCount on each player reflects the
          // freshly-bumped ledger entry.
          cur = buildFromRoster(guid, list);
          lastFingerprint = '';
        }
      }

      ev.emit('roster', cur);
      // Also emit 'change' so plugins that wrote `onMatch` still get
      // the late-joiner update via the same event they subscribed to.
      // Same fingerprint shape as the UpdateState path so a plugin
      // listening to both 'roster' and 'change' won't see spurious
      // double-fires.
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

      // Fan UpdateState out to the typed events bus so plugins that
      // register `events: { UpdateState(state) { … } }` receive the
      // same enriched payload that onTick delivers. Done after the
      // late-joiner re-build above so subscribers always see counts
      // consistent with the ledger. recordRecent is reached via
      // emitTyped, so the ring buffer also gets the fresh payload.
      emitTyped('UpdateState', cur);

      const fp =
        cur.guid +
        '|' +
        identity.id +
        '|' +
        cur.players
          .map((p) => p.id + ':' + p.team + ':' + (p.encounterCount > 1 ? 'r' : 'n'))
          .join(',');
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

    return {
      get current() {
        return cur;
      },
      // onChange / onTick auto-subscribe to UpdateState since they're
      // useless without it. Plugins that read .current synchronously
      // outside a handler should call .subscribe() once at init time.
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
      // onRoster delivers the same player-list view as onMatch but
      // without subscribing to UpdateState. Fires only on roster
      // identity changes (join, leave, team-switch, match guid flip)
      // — typically a handful of times per match instead of 60-120Hz.
      // Use this when your plugin reads roster identity (id, name,
      // team, platform, encounterCount) and doesn't care about
      // per-tick physics state. Per-tick fields on the player view
      // (score, boost, demos…) are zero/null on a roster-only build.
      //
      // Synthetic _-prefixed events bypass the server-side filter
      // anyway, so we don't call addEvent here — the wire delivers
      // _RosterChanged regardless.
      onRoster(fn) {
        return ev.on('roster', fn);
      },
    };
  })();

  // ─── Per-plugin store ──────────────────────────────────────
  // Scoped to the host plugin (the one whose page loaded sdk.js).
  // Plugins registered via RLT.plugin.register() get their own store
  // on the returned handle, scoped to spec.name — see register().
  const store = makeNamespacedStore(pluginName);

  // ─── UI helpers ────────────────────────────────────────────
  // Platform brand icons (Simple Icons paths, monocolor via fill=currentColor).
  // Keyed by lowercased platform string; values are just the path 'd' attribute.
  const PLATFORM_ICONS = {
    steam:
      'M11.979 0C5.678 0 .511 4.86.022 11.037l6.432 2.658a3.4 3.4 0 0 1 1.912-.59q.094.001.188.006l2.861-4.142V8.91a4.53 4.53 0 0 1 4.524-4.524c2.494 0 4.524 2.031 4.524 4.527s-2.03 4.525-4.524 4.525h-.105l-4.076 2.911l.004.159a3.39 3.39 0 0 1-3.39 3.396a3.41 3.41 0 0 1-3.331-2.727L.436 15.27C1.862 20.307 6.486 24 11.979 24c6.627 0 11.999-5.373 11.999-12S18.605 0 11.979 0M7.54 18.21l-1.473-.61c.262.543.714.999 1.314 1.25a2.551 2.551 0 0 0 3.337-3.324a2.547 2.547 0 0 0-3.255-1.413l1.523.63a1.878 1.878 0 0 1-1.445 3.467zm11.415-9.303a3.02 3.02 0 0 0-3.015-3.015a3.015 3.015 0 1 0 3.015 3.015m-5.273-.005a2.264 2.264 0 1 1 4.531 0a2.267 2.267 0 0 1-2.266 2.265a2.264 2.264 0 0 1-2.265-2.265',
    epic: 'M3.537 0C2.165 0 1.66.506 1.66 1.879V18.44a4 4 0 0 0 .02.433c.031.3.037.59.316.92c.027.033.311.245.311.245c.153.075.258.13.43.2l8.335 3.491c.433.199.614.276.928.27h.002c.314.006.495-.071.928-.27l8.335-3.492c.172-.07.277-.124.43-.2c0 0 .284-.211.311-.243c.28-.33.285-.621.316-.92a4 4 0 0 0 .02-.434V1.879c0-1.373-.506-1.88-1.878-1.88zm13.366 3.11h.68c1.138 0 1.688.553 1.688 1.696v1.88h-1.374v-1.8c0-.369-.17-.54-.523-.54h-.235c-.367 0-.537.17-.537.539v5.81c0 .369.17.54.537.54h.262c.353 0 .523-.171.523-.54V8.619h1.373v2.143c0 1.144-.562 1.71-1.7 1.71h-.694c-1.138 0-1.7-.566-1.7-1.71V4.82c0-1.144.562-1.709 1.7-1.709zm-12.186.08h3.114v1.274H6.117v2.603h1.648v1.275H6.117v2.774h1.74v1.275h-3.14zm3.816 0h2.198c1.138 0 1.7.564 1.7 1.708v2.445c0 1.144-.562 1.71-1.7 1.71h-.799v3.338h-1.4zm4.53 0h1.4v9.201h-1.4zm-3.13 1.235v3.392h.575c.354 0 .523-.171.523-.54V4.965c0-.368-.17-.54-.523-.54z',
    playstation:
      'M8.984 2.596v17.547l3.915 1.261V6.688c0-.69.304-1.151.794-.991c.636.18.76.814.76 1.505v5.875c2.441 1.193 4.362-.002 4.362-3.152c0-3.237-1.126-4.675-4.438-5.827c-1.307-.448-3.728-1.186-5.39-1.502zm4.656 16.241l6.296-2.275c.715-.258.826-.625.246-.818c-.586-.192-1.637-.139-2.357.123l-4.205 1.5V14.98l.24-.085s1.201-.42 2.913-.615c1.696-.18 3.785.03 5.437.661c1.848.601 2.04 1.472 1.576 2.072c-.465.6-1.622 1.036-1.622 1.036l-8.544 3.107V18.86zM1.807 18.6c-1.9-.545-2.214-1.668-1.352-2.32c.801-.586 2.16-1.052 2.16-1.052l5.615-2.013v2.313L4.205 17c-.705.271-.825.632-.239.826c.586.195 1.637.15 2.343-.12L8.247 17v2.074c-.12.03-.256.044-.39.073c-1.939.331-3.996.196-6.038-.479z',
    xbox: 'M4.102 21.033A11.95 11.95 0 0 0 12 24a11.96 11.96 0 0 0 7.902-2.967c1.877-1.912-4.316-8.709-7.902-11.417c-3.582 2.708-9.779 9.505-7.898 11.417m11.16-14.406c2.5 2.961 7.484 10.313 6.076 12.912A11.94 11.94 0 0 0 24 12.004a11.95 11.95 0 0 0-3.57-8.536s-.027-.022-.082-.042a.8.8 0 0 0-.281-.045c-.592 0-1.985.434-4.805 3.246M3.654 3.426c-.057.02-.082.041-.086.042A11.96 11.96 0 0 0 0 12.004c0 2.854.998 5.473 2.661 7.533c-1.401-2.605 3.579-9.951 6.08-12.91c-2.82-2.813-4.216-3.245-4.806-3.245a.7.7 0 0 0-.281.046zM12 3.551S9.055 1.828 6.755 1.746c-.903-.033-1.454.295-1.521.339C7.379.646 9.659 0 11.984 0H12c2.334 0 4.605.646 6.766 2.085c-.068-.046-.615-.372-1.52-.339C14.946 1.828 12 3.545 12 3.545z',
    switch:
      'M14.176 24h3.674c3.376 0 6.15-2.774 6.15-6.15V6.15C24 2.775 21.226 0 17.85 0H14.1c-.074 0-.15.074-.15.15v23.7c-.001.076.075.15.226.15m4.574-13.199c1.351 0 2.399 1.125 2.399 2.398c0 1.352-1.125 2.4-2.399 2.4c-1.35 0-2.4-1.049-2.4-2.4c-.075-1.349 1.05-2.398 2.4-2.398M11.4 0H6.15C2.775 0 0 2.775 0 6.15v11.7C0 21.226 2.775 24 6.15 24h5.25c.074 0 .15-.074.15-.149V.15c.001-.076-.075-.15-.15-.15M9.676 22.051H6.15a4.194 4.194 0 0 1-4.201-4.201V6.15A4.194 4.194 0 0 1 6.15 1.949H9.6zM3.75 7.199c0 1.275.975 2.25 2.25 2.25s2.25-.975 2.25-2.25c0-1.273-.975-2.25-2.25-2.25s-2.25.977-2.25 2.25',
    // CPU / chip — used by RLT.ui.playerIcon for AI players. Square chip
    // outline with corner pins so it reads as "computer-controlled" at the
    // small sizes plugin overlays use (12–24px). Filled inner core gives
    // it presence next to the brand icons without overpowering them.
    bot: 'M9 3v2H7a2 2 0 0 0-2 2v2H3v2h2v2H3v2h2v2a2 2 0 0 0 2 2h2v2h2v-2h2v2h2v-2h2a2 2 0 0 0 2-2v-2h2v-2h-2v-2h2V9h-2V7a2 2 0 0 0-2-2h-2V3h-2v2h-2V3h-2v2H11V3H9zm-2 4h10v10H7V7zm2 2v6h6V9H9z',
  };
  // Normalizes RL's PrimaryId platform prefix to an icon key. RL emits
  // values like 'Steam', 'Epic', 'PS4', 'XboxOne', 'Switch', 'Unknown'.
  function platformIconKey(platform) {
    if (!platform) return null;
    const p = String(platform).toLowerCase();
    if (p === 'steam') return 'steam';
    if (p === 'epic') return 'epic';
    if (p.startsWith('ps')) return 'playstation'; // PS4, PS5
    if (p.startsWith('xbox')) return 'xbox'; // XboxOne, Xbox
    if (p === 'switch' || p.includes('nintendo')) return 'switch';
    return null; // unknown / unmapped
  }

  // Render an icon from PLATFORM_ICONS by its key. Returns inline SVG
  // markup; the caller's container controls sizing and the SVG inherits
  // color via fill=currentColor. Empty string when the key isn't known.
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
    // Brand icon for a platform string ('Steam', 'Epic', 'PS4', 'XboxOne',
    // 'Switch', …). Returns '' when the platform isn't recognized.
    // For player rows, prefer ui.playerIcon(player) — it knows about bots.
    platformIcon(platform) {
      const key = platformIconKey(platform);
      return key ? renderIcon(key) : '';
    },

    // Returns the right icon for a Player object: a CPU/chip glyph for
    // bots, otherwise the brand icon for their platform. Single call site
    // for plugin code that wants "the icon that represents this player's
    // origin" without branching on isBot manually.
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
    // Safe-escape an arbitrary string for use inside a CSS selector value
    // (e.g. attribute selectors like `[data-pid="..."]`). Falls back to a
    // minimal manual escape on older browsers without CSS.escape.
    cssEsc(s) {
      if (window.CSS?.escape) return CSS.escape(s);
      return String(s == null ? '' : s).replace(/["\\]/g, '\\$&');
    },
    // Brief banner at the bottom-center. Reuses one DOM node across calls.
    // All visual styling lives in sdk.css under .rlt-toast / .rlt-toast--show
    // so plugins can override the look without forking the SDK.
    toast(msg, ms) {
      let t = document.getElementById('__rlt_toast');
      if (!t) {
        t = document.createElement('div');
        t.id = '__rlt_toast';
        t.className = 'rlt-toast';
        document.body.appendChild(t);
      }
      t.textContent = msg;
      // Two rAFs: the first commits the freshly-appended node so the
      // transition has a starting state, the second flips the class so
      // the show animation actually plays (single rAF skips the frame
      // on first call after creation).
      requestAnimationFrame(() => {
        requestAnimationFrame(() => t.classList.add('rlt-toast--show'));
      });
      clearTimeout(t._timer);
      t._timer = setTimeout(() => t.classList.remove('rlt-toast--show'), ms || 2000);
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

  // Backwards-compatible wrapper used by every existing caller (typed
  // event normalizers): resolves against the live match.current roster.
  function resolvePlayer(ref) {
    const cur = match.current;
    return resolvePlayerIn(cur ? cur.players : null, ref);
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
    if (!scorer || (!scorer.player && !d.Scorer?.Name)) return;
    emitTyped('GoalScored', {
      matchGuid: d.MatchGuid || null,
      goalSpeed: d.GoalSpeed != null ? d.GoalSpeed : null,
      goalTime: d.GoalTime != null ? d.GoalTime : null,
      impactLocation: d.ImpactLocation || null,
      scorer,
      assister: d.Assister ? resolvePlayer(d.Assister) : null,
      ballLastTouch: d.BallLastTouch
        ? {
            player: resolvePlayer(d.BallLastTouch.Player),
            speed: d.BallLastTouch.Speed != null ? d.BallLastTouch.Speed : null,
          }
        : null,
      raw: d,
    });
  });

  bus.on('BallHit', (d) => {
    if (!d) return;
    emitTyped('BallHit', {
      matchGuid: d.MatchGuid || null,
      players: (d.Players || []).map(resolvePlayer),
      preSpeed: d.Ball ? (d.Ball.PreHitSpeed != null ? d.Ball.PreHitSpeed : null) : null,
      postSpeed: d.Ball ? (d.Ball.PostHitSpeed != null ? d.Ball.PostHitSpeed : null) : null,
      location: d.Ball ? d.Ball.Location || null : null,
      raw: d,
    });
  });

  bus.on('CrossbarHit', (d) => {
    if (!d) return;
    emitTyped('CrossbarHit', {
      matchGuid: d.MatchGuid || null,
      ballSpeed: d.BallSpeed != null ? d.BallSpeed : null,
      impactForce: d.ImpactForce != null ? d.ImpactForce : null,
      ballLocation: d.BallLocation || null,
      ballLastTouch: d.BallLastTouch
        ? {
            player: resolvePlayer(d.BallLastTouch.Player),
            speed: d.BallLastTouch.Speed != null ? d.BallLastTouch.Speed : null,
          }
        : null,
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
      type: d.Type || '',
      mainTarget: resolvePlayer(d.MainTarget),
      secondaryTarget: d.SecondaryTarget ? resolvePlayer(d.SecondaryTarget) : null,
      raw: d,
    });
  });

  bus.on('ClockUpdatedSeconds', (d) => {
    if (!d) return;
    emitTyped('ClockUpdatedSeconds', {
      matchGuid: d.MatchGuid || null,
      seconds: d.TimeSeconds != null ? d.TimeSeconds : null,
      overtime: !!d.bOvertime,
      raw: d,
    });
  });

  // The other MatchEnded handler (inside the `match` IIFE, above) records
  // per-encounter W/L and only reads from the bus — it's independent of
  // this typed-event normalizer. Both handlers fire because the bus
  // iterates every subscriber for an event; ordering between them is
  // not guaranteed and shouldn't matter (different concerns, no shared
  // state beyond `match.current` which is set before either runs).
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
  ].forEach((name) => {
    bus.on(name, (d) => {
      emitTyped(name, {
        matchGuid: d?.MatchGuid || null,
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
    on: (name, fn) => {
      addEvent(name);
      return eventsBus.on(name, fn);
    },
    off: (name, fn) => eventsBus.off(name, fn),
    recent(name, n) {
      const arr = recentByType.get(name) || [];
      return n ? arr.slice(-n) : arr.slice();
    },

    // typed subscribers
    onUpdateState: makeOn('UpdateState'),
    onGoalScored: makeOn('GoalScored'),
    onBallHit: makeOn('BallHit'),
    onCrossbarHit: makeOn('CrossbarHit'),
    onStatfeedEvent: makeOn('StatfeedEvent'),
    onClockUpdatedSeconds: makeOn('ClockUpdatedSeconds'),

    onMatchCreated: makeOn('MatchCreated'),
    onMatchInitialized: makeOn('MatchInitialized'),
    onMatchDestroyed: makeOn('MatchDestroyed'),
    onMatchEnded: makeOn('MatchEnded'),
    onMatchPaused: makeOn('MatchPaused'),
    onMatchUnpaused: makeOn('MatchUnpaused'),

    onCountdownBegin: makeOn('CountdownBegin'),
    onRoundStarted: makeOn('RoundStarted'),

    onGoalReplayStart: makeOn('GoalReplayStart'),
    onGoalReplayWillEnd: makeOn('GoalReplayWillEnd'),
    onGoalReplayEnd: makeOn('GoalReplayEnd'),

    onPodiumStart: makeOn('PodiumStart'),
    onReplayCreated: makeOn('ReplayCreated'),
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
    bus.on('_Lifecycle', (snap) => {
      if (snap) applySnapshot(snap);
    });

    // SSE disconnect invalidates our snapshot — without this, plugins
    // polling `lifecycle.phase` after a connection drop would see a
    // stale "live" value indefinitely. On reconnect the server's first
    // _Lifecycle replaces this with the authoritative state.
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
  // Self-documenting registry of every event the SDK emits, what shape its
  // payload has, and whether it fires during live gameplay only or anywhere.
  // Plugin authors can console.log(RLT.events.catalog) to discover what's
  // available without reading source.
  events.catalog = [
    // Per-tick stream
    {
      name: 'UpdateState',
      category: 'tick',
      shape: 'matchstate',
      livePhases: ['live', 'replay', 'paused', 'countdown'],
      desc: 'Match snapshot at PacketSendRate. Includes derived teams/blueTeam/orangeTeam, replayInfo, and resolved per-player attacker.',
      stability: 'stable',
      since: '1.0',
    },

    // In-play events
    {
      name: 'GoalScored',
      category: 'scoring',
      shape: 'goal',
      livePhases: ['live', 'replay'],
      desc: 'Scorer + assister + last touch + impact.',
      stability: 'stable',
      since: '1.0',
    },
    {
      name: 'BallHit',
      category: 'play',
      shape: 'ballhit',
      livePhases: ['live'],
      desc: 'Ball touched. Pre/post speed and location.',
      stability: 'stable',
      since: '1.0',
    },
    {
      name: 'CrossbarHit',
      category: 'play',
      shape: 'crossbar',
      livePhases: ['live'],
      desc: 'Ball hit a crossbar.',
      stability: 'stable',
      since: '1.0',
    },
    {
      name: 'StatfeedEvent',
      category: 'stat',
      shape: 'statfeed',
      livePhases: ['live', 'replay'],
      desc: 'Player earned a stat (demo, save, epic save, etc).',
      stability: 'stable',
      since: '1.0',
    },
    {
      name: 'ClockUpdatedSeconds',
      category: 'play',
      shape: 'clock',
      livePhases: ['live', 'countdown'],
      desc: 'Match clock changed by ≥1 second.',
      stability: 'stable',
      since: '1.0',
    },

    // Lifecycle
    {
      name: 'MatchCreated',
      category: 'lifecycle',
      shape: 'match',
      livePhases: '*',
      desc: 'All teams replicated; lobby ready.',
      stability: 'stable',
      since: '1.0',
    },
    {
      name: 'MatchInitialized',
      category: 'lifecycle',
      shape: 'match',
      livePhases: '*',
      desc: 'First countdown started.',
      stability: 'stable',
      since: '1.0',
    },
    {
      name: 'CountdownBegin',
      category: 'lifecycle',
      shape: 'match',
      livePhases: '*',
      desc: 'Round countdown began.',
      stability: 'stable',
      since: '1.0',
    },
    {
      name: 'RoundStarted',
      category: 'lifecycle',
      shape: 'match',
      livePhases: '*',
      desc: 'Active gameplay started (countdown ended).',
      stability: 'stable',
      since: '1.0',
    },
    {
      name: 'MatchPaused',
      category: 'lifecycle',
      shape: 'match',
      livePhases: '*',
      desc: 'Match paused by an admin.',
      stability: 'stable',
      since: '1.0',
    },
    {
      name: 'MatchUnpaused',
      category: 'lifecycle',
      shape: 'match',
      livePhases: '*',
      desc: 'Match resumed.',
      stability: 'stable',
      since: '1.0',
    },
    {
      name: 'GoalReplayStart',
      category: 'replay',
      shape: 'match',
      livePhases: '*',
      desc: 'Goal replay began.',
      stability: 'stable',
      since: '1.0',
    },
    {
      name: 'GoalReplayWillEnd',
      category: 'replay',
      shape: 'match',
      livePhases: '*',
      desc: 'Ball exploded during replay (fires only if not skipped).',
      stability: 'stable',
      since: '1.0',
    },
    {
      name: 'GoalReplayEnd',
      category: 'replay',
      shape: 'match',
      livePhases: '*',
      desc: 'Goal replay ended.',
      stability: 'stable',
      since: '1.0',
    },
    {
      name: 'MatchEnded',
      category: 'lifecycle',
      shape: 'matchend',
      livePhases: '*',
      desc: 'Match decided. Has WinnerTeamNum.',
      stability: 'stable',
      since: '1.0',
    },
    {
      name: 'PodiumStart',
      category: 'lifecycle',
      shape: 'match',
      livePhases: '*',
      desc: 'Game entered podium state.',
      stability: 'stable',
      since: '1.0',
    },
    {
      name: 'MatchDestroyed',
      category: 'lifecycle',
      shape: 'match',
      livePhases: '*',
      desc: 'Player left the match.',
      stability: 'stable',
      since: '1.0',
    },
    {
      name: 'ReplayCreated',
      category: 'lifecycle',
      shape: 'match',
      livePhases: '*',
      desc: 'Match-history replay loaded (NOT goal replays).',
      stability: 'stable',
      since: '1.0',
    },

    // Synthetic events (toolkit-emitted, not from RL). Player references
    // are pre-resolved against the live roster so subscribers don't
    // need to look anything up themselves.
    {
      name: '_StatfeedEvent',
      category: 'stat',
      shape: 'stat-enriched',
      livePhases: ['live', 'replay'],
      desc: 'StatfeedEvent with MainTarget/SecondaryTarget pre-resolved.',
      stability: 'provisional',
      since: '1.1',
    },
    {
      name: '_BallHit',
      category: 'play',
      shape: 'ballhit-enriched',
      livePhases: ['live'],
      desc: 'BallHit with Players[] pre-resolved.',
      stability: 'provisional',
      since: '1.1',
    },
    {
      name: '_CrossbarHit',
      category: 'play',
      shape: 'crossbar-enriched',
      livePhases: ['live'],
      desc: 'CrossbarHit with BallLastTouch.Player pre-resolved.',
      stability: 'provisional',
      since: '1.1',
    },
    {
      name: '_MatchEnded',
      category: 'lifecycle',
      shape: 'matchend-enriched',
      livePhases: '*',
      desc: 'MatchEnded with winnerName + scoreBlue/scoreOrange resolved.',
      stability: 'provisional',
      since: '1.1',
    },
    {
      name: '_GoalScored',
      category: 'scoring',
      shape: 'goal-enriched',
      livePhases: ['live', 'replay'],
      desc: 'GoalScored with players resolved + scoringTeam/concedingTeam/isOwnGoal flags + same-frame modifiers (aerial/long/turtle/hatTrick/etc.).',
      stability: 'provisional',
      since: '1.1',
    },
    {
      name: '_OwnGoal',
      category: 'scoring',
      shape: 'owngoal',
      livePhases: ['live', 'replay'],
      desc: 'Score-delta verified own goal: emitted when a team scores +1 and the most recent ball touch was by the opposing team. Phase-gated to live/replay.',
      stability: 'provisional',
      since: '1.1',
    },
    // UpdateState-diff events (Phase 4). Backend compares current vs
    // previous tick and emits the change directly — plugins skip
    // UpdateState entirely.
    {
      name: '_PlayerJoined',
      category: 'roster',
      shape: 'player-event',
      livePhases: '*',
      desc: 'Roster diff: player appeared this tick.',
      stability: 'provisional',
      since: '1.1',
    },
    {
      name: '_PlayerLeft',
      category: 'roster',
      shape: 'player-event',
      livePhases: '*',
      desc: 'Roster diff: player disappeared this tick.',
      stability: 'provisional',
      since: '1.1',
    },
    {
      name: '_PlayerScoreChanged',
      category: 'stat',
      shape: 'score-delta',
      livePhases: '*',
      desc: 'Per-player stat diff. Only fields that moved appear in `delta`.',
      stability: 'provisional',
      since: '1.1',
    },
    {
      name: '_BoostPickup',
      category: 'play',
      shape: 'boost-pickup',
      livePhases: ['live'],
      desc: 'Player Boost rose between ticks (not a respawn). Spectator-only for opponents.',
      stability: 'provisional',
      since: '1.1',
    },
    {
      name: '_BallPossessionChanged',
      category: 'play',
      shape: 'possession',
      livePhases: ['live'],
      desc: 'Game.Ball.TeamNum changed. before/after nullable — 255 → null.',
      stability: 'provisional',
      since: '1.1',
    },
    {
      name: '_TeamScoreChanged',
      category: 'scoring',
      shape: 'team-score-delta',
      livePhases: ['live', 'replay'],
      desc: 'A team Score moved. Pair with _OwnGoal for own-goal verification.',
      stability: 'provisional',
      since: '1.1',
    },
  ];

  // Frozen views by category for the common "give me everything in group X" need.
  events.byCategory = events.catalog.reduce((acc, e) => {
    if (!acc[e.category]) acc[e.category] = [];
    acc[e.category].push(e.name);
    return acc;
  }, {});

  // Lock the catalog so plugins can't mutate the source of truth at runtime.
  // (Plugin code that forks the SDK and adds entries should do so here.)
  events.catalog.forEach(Object.freeze);
  Object.freeze(events.catalog);
  Object.freeze(events.byCategory);

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
    SHOT: 'Shot', // type: "Shot on Goal"
    GOAL: 'Goal', // also fires GoalScored
    AERIAL_GOAL: 'AerialGoal',
    LONG_GOAL: 'LongGoal',
    TURTLE_GOAL: 'TurtleGoal',
    HAT_TRICK: 'HatTrick', // 3+ goals by same player
    SAVE: 'Save',
    DEMOLISH: 'Demolish', // secondaryTarget = demolished player
    FLIP_RESET: 'FlipReset',
    WIN: 'Win',
  };
  stats.known = new Set(Object.values(stats));
  Object.freeze(stats);

  // ─── Plugin registration API ───────────────────────────────
  // Declarative way for plugins to wire themselves up. Built on top of the
  // imperative APIs above — the raw bus is still available for one-offs.
  //
  // Usage:
  //   const me = RLT.plugin.register({
  //     // Lifecycle hooks
  //     init()  { /* sync setup, runs at register time */ },
  //     ready() { /* once identity + encounter ledger have loaded */ },
  //     dispose() { /* cleanup, runs when handle.dispose() is called */ },
  //
  //     // Typed event handlers — keys are names from the event catalog.
  //     // Each handler receives the typed payload (see catalog for shapes).
  //     // The SDK auto-subscribes on the SSE filter; no extra opt-in needed.
  //     events: {
  //       GoalScored(g)    { ... },       // typed payload (g.scorer.player, etc)
  //       UpdateState(s)   { ... },       // enriched 60Hz state (same as onTick)
  //       '*'(name, p)     { ... },       // catchall on the raw bus
  //     },
  //     whilePhase: ['live', 'replay'],   // optional gate for events + on*; '*' = always
  //
  //     // Convenience subscribers (gated by whilePhase, except where noted).
  //     onTick(state)            { ... }, // every UpdateState (~60Hz)
  //     onMatch(state)           { ... }, // structural match changes (uses UpdateState)
  //     onRoster(state)          { ... }, // roster id changes only (no UpdateState)
  //     onIdentity(id)           { ... }, // user re-claimed identity
  //     onEncounters(map)        { ... }, // encounter ledger updated
  //     onLifecycle(phase, prev) { ... }, // phase transition (bypasses whilePhase)
  //     onMatchActive(active)    { ... }, // match_active flipped (bypasses whilePhase)
  //     onFocusChange(active)    { ... }, // game-window focus (bypasses whilePhase)
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

    // Wrap a plugin handler with error isolation (a single plugin throw
    // never escapes into the dispatcher), and optionally with whilePhase
    // gating. Pass `{ phaseGated: false }` for transition observers
    // (onLifecycle, onMatchActive) that need to fire regardless of the
    // destination phase — without that escape hatch, a plugin with
    // whilePhase: ['live'] would never see "match_active just went
    // false" because the destination phase is 'none'.
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

    /**
     * Register a plugin against the SDK. Wires up event handlers, lifecycle
     * gating, error isolation, and a per-plugin storage scope. Returns a
     * handle for introspection and explicit teardown.
     *
     * The SDK auto-subscribes to whatever events your handlers ask for —
     * declaring `events: { GoalScored }` causes the SDK to widen the
     * server-side filter and start delivering GoalScored, with no extra
     * API call. UpdateState is the only opt-in heavyweight: register an
     * `onTick`, `onMatch`, `events.UpdateState`, or call
     * `RLT.match.subscribe()` to get it.
     *
     * Every handler is wrapped in try/catch — one plugin throwing never
     * affects another. Event handlers (the `events` map plus `onMatch`,
     * `onTick`, `onIdentity`, `onEncounters`) are also gated by
     * `whilePhase` if set; transition observers (`onLifecycle`,
     * `onMatchActive`, `onFocusChange`) bypass the gate by design so
     * they can observe transitions out of any phase.
     *
     * Plugin metadata (name, version, author, title) is read from the
     * plugin's `manifest.json` automatically — there's no need to
     * duplicate it here. The fields below are **overrides** for the rare
     * case (testing, dynamic plugins) when you want to differ from the
     * manifest. Production plugins should pass only the runtime fields
     * (`init`, `events`, `whilePhase`, `onMatch`, …).
     *
     * @param {object}   spec
     * @param {string}   [spec.name]       Override the manifest's `name`.
     *                                     Default: manifest.name → script
     *                                     `data-plugin` attr → URL path
     *                                     segment under /plugins/. Used
     *                                     as the storage namespace.
     * @param {string}   [spec.version]    Override the manifest's `version`.
     * @param {string}   [spec.author]     Override the manifest's `author`.
     * @param {string}   [spec.title]      Override the manifest's `title`.
     * @param {string|string[]} [spec.whilePhase]
     *                                     Phase gate for `events`, `onMatch`,
     *                                     `onTick`, `onIdentity`, `onEncounters`.
     *                                     One of: 'none', 'created', 'countdown',
     *                                     'live', 'paused', 'replay', 'ended',
     *                                     'podium', or '*' (always). 'idle' is
     *                                     accepted as a back-compat alias for
     *                                     'none'. Default: '*'.
     *
     * @param {object}   [spec.events]     Map of EventName → handler(payload).
     *                                     Use `'*'` for a catchall on the raw
     *                                     bus (does not widen the SSE filter).
     *
     * @param {(handle: object) => void} [spec.init]    Called synchronously on register.
     *                                                  DOM setup goes here.
     * @param {(handle: object) => void} [spec.ready]   Called once after identity
     *                                                  and the encounter ledger
     *                                                  have finished loading.
     * @param {(s: object) => void}      [spec.onTick]      Every UpdateState (~60Hz).
     * @param {(s: object) => void}      [spec.onMatch]     Structural match changes only.
     *                                                  Pulls UpdateState off the wire.
     * @param {(s: object) => void}      [spec.onRoster]    Roster identity changed
     *                                                  (join/leave/team-switch). Light-
     *                                                  weight: a few events per match,
     *                                                  no UpdateState subscription.
     *                                                  Per-tick fields on the player
     *                                                  view are zero/null on this path.
     * @param {(id: string) => void}     [spec.onIdentity]  User changed which player is "me".
     * @param {(map: object) => void}    [spec.onEncounters] Encounter ledger updated.
     * @param {(phase: string, prev: string) => void} [spec.onLifecycle]
     *                                                  Phase transition. Bypasses whilePhase.
     * @param {(active: boolean) => void} [spec.onMatchActive]
     *                                                  match_active flipped. Bypasses whilePhase.
     * @param {(active: boolean) => void} [spec.onFocusChange]
     *                                                  Game window focus changed (Tauri only).
     *                                                  Bypasses whilePhase.
     * @param {() => void}               [spec.dispose]     Called by handle.dispose().
     *
     * @returns {{
     *   name: string,
     *   version: ?string,
     *   author: ?string,
     *   title: ?string,
     *   manifest: ?object,
     *   disposed: boolean,
     *   store: { get, getAll, set, delete },
     *   events: string[],
     *   spec: object,
     *   dispose: () => void
     * }} A handle every plugin gets back. `name`/`version`/`author`/`title`
     *    and `manifest` resolve once /api/plugins responds — they're patched
     *    in-place when the manifest fetch lands. `disposed` is a getter,
     *    flips true after dispose() runs. `events` lists the event names
     *    declared on `spec.events` (for introspection by other plugins).
     */
    function register(spec) {
      spec = spec || {};
      // Resolution order for metadata: explicit spec value → manifest →
      // pluginName fallback. The manifest may not have loaded yet (the
      // /api/plugins fetch is async); when it lands later we patch the
      // handle below. None of these fields gate any behaviour, so the
      // delayed patch is invisible to plugin handlers.
      const name = spec.name || pluginManifest?.name || pluginName;
      const unsubs = [];
      let disposed = false;

      // Per-plugin scoped store. Defaults to the page-level pluginName
      // (most plugins won't override `spec.name`). Same shape and
      // semantics as the top-level RLT.store.
      const pluginStore = makeNamespacedStore(name);

      // Wire event handlers — keys are event names from the catalog.
      if (spec.events) {
        for (const evName of Object.keys(spec.events)) {
          const handler = spec.events[evName];
          if (typeof handler !== 'function') continue;
          // Tell the server we want this event delivered. '*' is the
          // raw-bus catchall — it doesn't add anything to the filter
          // (the catchall fires on whatever the bus already gets).
          if (evName !== '*') addEvent(evName);
          const sub =
            evName === '*'
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

      // Patch metadata fields once the manifest fetch resolves, if the
      // plugin didn't override them in spec. Skipped when the manifest
      // is already loaded — the synchronous defaults above already
      // captured everything.
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

      // init synchronously, ready when identity + encounters have loaded.
      if (typeof spec.init === 'function') {
        try {
          spec.init(handle);
        } catch (e) {
          console.error('[RLT] plugin "' + name + '" init threw:', e);
        }
      }
      // Run spec.ready() exactly once, after both the identity record
      // and the encounter ledger have finished loading. Both stores
      // emit 'change' on load completion; subscribe to each, and fire
      // when both report ready. The unsubs are tracked so a dispose()
      // before ready never fires the callback against a torn-down
      // plugin.
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
        fireReady();
      } else {
        // Each store emits 'change' once on initial load. Either may
        // already be ready; check inside fireReady() before firing.
        unsubs.push(identity.onChange(fireReady));
        unsubs.push(encounters.onChange(fireReady));
      }

      // Logged with the resolved version (spec → manifest → unknown).
      // For plugins that rely on the async manifest fetch, the line at
      // register-time may show "(no version)"; the handle gets patched
      // when the manifest arrives. The Go server logs the manifest
      // version separately at file-load time, so the truth is always
      // visible somewhere.
      console.debug('[RLT] plugin registered:', handle.name, handle.version || '(no version)');
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

  // ─── Internal: shared sizing helpers used by widget.autoSize/fitWidth ──
  //
  // Both methods need the same target-resolution and observer setup. They
  // differ only in the body of `flush()` (autoSize tracks both dimensions
  // with min/max clamps; fitWidth grows the width monotonically against a
  // high-water mark). Factoring the boilerplate out makes each method's
  // measurement policy fit on a screen.

  // Resolve `target` to a DOM Element. Accepts an Element directly, a CSS
  // selector string, or undefined (→ document.body). Re-resolved every
  // call so a deferred-rendered target is picked up automatically once it
  // mounts.
  function resolveTarget(target) {
    if (target instanceof Element) return target;
    if (typeof target === 'string') {
      return document.querySelector(target) || document.body;
    }
    return document.body;
  }

  // Tracks every active widget-watcher so pagehide can tear them all down
  // in one shot. Each entry is { observer, listeners: [{type, fn}] }.
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

  // Wire up a ResizeObserver + the standard "size-might-have-changed but
  // observer can't see it" signals (animationend, transitionend, font
  // load). Coalesces all of them through one rAF-debounced `flush`.
  //
  // Returns the watcher record so the caller can stash it for explicit
  // cleanup. The watcher is also added to `activeWatchers` so the global
  // pagehide handler tears it down on navigation.
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
    // Body too — reflows that don't change the target's own box but do
    // change wrapping (e.g. parent flex container resizes, iframe width
    // changes) reach us through document.body.
    if (getTarget() !== document.body && document.body) {
      observer.observe(document.body);
    }

    // Kick once: the first paint should size correctly even if no
    // observer event fires (target measured the same as last time).
    schedule();

    // ResizeObserver doesn't fire on opacity / transform changes, so a
    // fade-in finishing alters nothing observable. Web fonts arriving
    // (Inter, Saira Condensed) shift name widths once they replace the
    // system fallback. Hook both signals.
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
    const inTauri =
      typeof window !== 'undefined' &&
      !!window.__TAURI_INTERNALS__ &&
      typeof window.__TAURI_INTERNALS__.invoke === 'function';

    // One watcher per method. Restart calls dispose the previous watcher
    // so options take effect, instead of stacking observers.
    let autoSizeWatcher = null;
    let fitWidthWatcher = null;
    // fitWidth tracks the largest width we've ever asked the host for.
    // We only invoke widget_size when content wants to grow past that
    // mark — never on shrink — to avoid feedback loops with `max-width:
    // 100%` chains.
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
      /** True only when running inside the desktop widget (Tauri). */
      isHosted() {
        return inTauri;
      },

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

      /**
       * Auto-resize the host window to fit a measurement target.
       *
       * Watches the target with a `ResizeObserver` (plus animation/
       * transition/font-load signals) and pushes new sizes to the host
       * once per animation frame. Idempotent: a second call replaces
       * the previous watcher rather than stacking. Returns false outside
       * Tauri.
       *
       * @param {boolean} enabled                 true to start, false to stop.
       * @param {object}  [opts]
       * @param {Element|string} [opts.target]    Element or CSS selector. Default: document.body.
       *                                          Pass the content wrapper (e.g. '.ov') — body's
       *                                          flex centering otherwise inflates the measurement.
       * @param {number}  [opts.minWidth=1]
       * @param {number}  [opts.minHeight=1]
       * @param {number}  [opts.maxWidth=4096]
       * @param {number}  [opts.maxHeight=4096]
       * @returns {boolean}
       */
      autoSize(enabled, opts) {
        if (!inTauri) return false;
        opts = opts || {};
        const minW = opts.minWidth | 0 || 1;
        const minH = opts.minHeight | 0 || 1;
        const maxW = opts.maxWidth | 0 || 4096;
        const maxH = opts.maxHeight | 0 || 4096;

        // Tear down any previous watcher so new opts take effect.
        stopSizeWatcher(autoSizeWatcher);
        autoSizeWatcher = null;
        if (!enabled) return true;

        let lastW = -1,
          lastH = -1;
        const flush = () => {
          const el = resolveTarget(opts.target);
          if (!el) return;
          // getBoundingClientRect respects transforms and gives us fractional
          // pixels; scrollWidth/Height ignores subpixel layout. The widget
          // surface is integer-pixel so we ceil to avoid clipping the last
          // row by half a pixel during fade-in animations.
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

      /**
       * Grow the host window's width to fit the target's natural content.
       *
       * Width is monotonic — only grows, never shrinks — so there's no
       * feedback loop with `max-width: 100%` chains. Height is left at
       * the manifest value (we pass `window.innerHeight` through, which
       * reflects the manifest height at startup and any explicit resize
       * since). Returns false outside Tauri.
       *
       * Use case: "long player name pushes a row past the manifest
       * width" — the surface widens once and stays widened for the
       * session.
       *
       * @param {object} [opts]
       * @param {Element|string} [opts.target]    Element or CSS selector. Default: document.body.
       * @param {number} [opts.maxWidth=800]      Hard cap so a pathological name can't blow up the surface.
       * @param {number} [opts.extra=0]           Extra px added beyond measured width (e.g. for glow padding the layout box doesn't include).
       * @returns {boolean}
       */
      fitWidth(opts) {
        if (!inTauri) return false;
        opts = opts || {};
        const maxW = opts.maxWidth | 0 || 800;
        const extra = opts.extra | 0 || 0;

        // Restart cleanly with new opts; high-water mark is preserved on
        // module-level `fitWidthHighWater` so retoggling doesn't shrink.
        stopSizeWatcher(fitWidthWatcher);
        fitWidthWatcher = null;

        const flush = () => {
          const el = resolveTarget(opts.target);
          if (!el) return;
          // scrollWidth is the unconstrained content width — ignores
          // max-width / overflow:hidden / nowrap clipping. Exactly the
          // measurement we need: "how wide does this *want* to be".
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
        if (msg?.data?.__rlt_focus__ !== true) return;
        ev.emit('change', !!msg.data.active);
      });
    }

    return {
      /** Subscribe to focus-change events. fn receives a boolean (true =
       *  game is foreground, false = not). Returns an unsub function. */
      onChange(fn) {
        return ev.on('change', fn);
      },
    };
  })();

  // ─── Overlay visibility gates: SDK-side body toggle ─────────
  // When the manifest opts into hide_when_unfocused and/or
  // show_during_phase, the SDK already started with body {display:none}
  // in the bootstrap above. Here we wire both signals to toggle inline
  // display whenever either gate's state changes, with the combined
  // predicate `focusOK && phaseOK`. Plugins get this for free — no
  // init() default-hide, no onFocusChange or phase handlers needed.
  //
  // We restore display to 'flex' (the value the overlay bootstrap would
  // have set without gating) so anchor-corner pinning keeps working.
  //
  // The host's focus watcher is edge-triggered (only emits on
  // transitions). focus starts at "unknown" — we treat it as false
  // until the first emit, so a plugin that mounts before RL gains focus
  // stays hidden until the first onFocusChange(true).
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
    // Initial paint — handles the case where lifecycle.phase already
    // matches at bootstrap time (e.g. plugin loaded mid-match).
    repaint();
  }

  // ─── Stable connection status ──────────────────────────────
  //
  // The toolkit's connection to Rocket League cycles every 30s during
  // menu idle by design (TCP idle-timeout reconnect). The raw `_status`
  // signal flips connected → connecting → connected on each cycle,
  // which is correct but visually noisy when surfaced as-is in plugin
  // UI. `statusStable` debounces the path to non-connected states so
  // the brief cycle never crosses the threshold; coming back to
  // connected is instant.
  //
  // Plugins that need the raw signal (e.g. an indicator that flashes
  // intentionally on every blip) keep using RLT.onStatus / RLT.status().
  // Plugins that surface "are we live?" should prefer the stable view.
  const STATUS_DOWN_DEBOUNCE_MS = 3000;
  const statusStableState = (function () {
    const ev = emitter();
    let stable = status; // mirrors the raw status until a debounce defers it
    let pending = null; // pending downgrade timer

    bus.on('_status', (s) => {
      if (s === 'connected') {
        // Cancel any pending downgrade and reflect the good state now.
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
      // Already in a non-connected state? Update label immediately so
      // a connecting → disconnected progression is still visible.
      if (stable !== 'connected') {
        if (stable !== s) {
          stable = s;
          ev.emit('change', stable);
        }
        return;
      }
      // Currently stable on 'connected'; defer the downgrade. Latest
      // raw status wins if more arrive before the timer fires.
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

  // ─── Settings panel bridge ─────────────────────────────────
  // When a plugin's settings view runs inside the dashboard's modal
  // iframe, calling RLT.settings.close() posts a message the dashboard
  // listens for and dismisses the modal. Outside an iframe this is a
  // no-op — calling it from a plugin's overlay or control view does
  // nothing.
  const settingsApi = {
    close() {
      if (window.parent && window.parent !== window) {
        try {
          window.parent.postMessage({ type: 'rlt:settings:close' }, location.origin);
        } catch {
          // Cross-origin parent or postMessage rejection — ignore.
        }
      }
    },
  };

  // ─── Public API ────────────────────────────────────────────
  window.RLT = {
    plugin: plugin, // registration API; .name kept below for back-compat
    pluginName: pluginName, // explicit name accessor
    // Plugin manifest, fetched once at startup from /api/plugins.
    // Synchronous read returns null until the fetch resolves (typically
    // a few ms after page load). Plugins that need to react when it
    // arrives use `onManifest(fn)`; fn fires once with the manifest
    // object (or null if the fetch failed / no matching entry).
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
    // Debounced view of `status` — flicker-free across the toolkit's
    // 30s self-reconnect cycles. Prefer this for visible UI that says
    // "are we live?". The raw `status`/`onStatus` above stays available
    // for plugins that want every transition.
    statusStable: () => statusStableState.get(),
    onStatusStable: (fn) => statusStableState.onChange(fn),

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
    isSettingsView: __rltIsSettingsView,
    settings: settingsApi,
  };

  // Escape hatch: force-reconnect the SSE stream. Useful when the
  // browser's auto-reconnect is stuck after a long sleep/wake cycle.
  window.RLT._reconnect = function () {
    if (es) {
      try {
        es.close();
      } catch {
        /* noop: already-closed sockets throw */
      }
      es = null;
    }
    connect();
  };

  // Freeze the public surface so plugin code can't accidentally shadow
  // `RLT.match`, `RLT.events`, etc. (Sub-objects deliberately stay
  // mutable: e.g. `RLT.events.recent` reads from a Map that's updated
  // as events arrive; the catalog and byCategory views are frozen
  // independently above.)
  Object.freeze(window.RLT);

  // Defer the first connect so synchronous RLT.plugin.register({...})
  // calls in the page's <script> tags land in the filter before we
  // open the EventSource. Microtask is enough — most plugin pages
  // call register() inline. Plugins that register later trigger a
  // reconnect via addEvent's stale-filter path.
  if (hostedBus) {
    // Hosted mode: listen for envelopes the parent forwards from its
    // single shared EventSource. The parent also re-posts the last
    // _Lifecycle and _ConnectionStatus on iframe load so a late-
    // mounting plugin gets a complete initial state.
    window.addEventListener('message', (e) => {
      const data = e?.data;
      if (!data || data.__rlt_bus__ !== true || !data.msg) return;
      // Different origin would be a bug — both pages are served by the
      // same toolkit. Defensive check: only accept from our parent.
      if (e.source !== window.parent) return;
      dispatchEnvelope(data.msg);
    });
    // Tell the parent which events we already care about (the required
    // baseline plus anything the page's inline register() calls have
    // added by the time this microtask runs). Same deferral idea as
    // the direct-mode path.
    Promise.resolve().then(() => {
      const list = Array.from(subscribedEvents);
      postToHost({ __rlt_bus_hello__: true, events: list });
    });
  } else {
    Promise.resolve().then(connect);
  }
})();
