# Writing RL Toolkit Plugins

A plugin is a folder under `plugins/` that ships one HTML file and a small
manifest. The toolkit serves the HTML, exposes a global `RLT` object with
the SDK, and streams Rocket League events to it. There is no build step
and no framework to learn — if you can write a script tag, you can write
a plugin.

This guide covers everything: scaffolding, events, overlays, widgets,
dashboards, persistence, and debugging.

---

## Table of contents

- [Quick start](#quick-start)
- [Anatomy of a plugin](#anatomy-of-a-plugin)
  - [`manifest.json`](#manifestjson)
  - [`overlay.html`](#overlayhtml)
- [Subscribing to events](#subscribing-to-events)
  - [The `events` map](#the-events-map)
  - [The full event catalog](#the-full-event-catalog)
  - [Lifecycle gating](#lifecycle-gating)
  - [Convenience subscriptions](#convenience-subscriptions)
  - [Enriched match state](#enriched-match-state)
- [Overlay vs. dashboard mode](#overlay-vs-dashboard-mode)
  - [How the overlay is composed](#how-the-overlay-is-composed)
- [Building a dashboard view](#building-a-dashboard-view)
- [Widget mode (desktop)](#widget-mode-desktop)
  - [`autoSize` — track content height *and* width](#autosize--track-content-height-and-width)
  - [`fitWidth` — grow only, never shrink](#fitwidth--grow-only-never-shrink)
- [Persisting data](#persisting-data)
- [Identity and encounters (shared)](#identity-and-encounters-shared)
  - [Who is "me"?](#who-is-me)
  - [Encounter ledger](#encounter-ledger)
- [UI helpers](#ui-helpers)
- [Manual subscription (escape hatch)](#manual-subscription-escape-hatch)
- [Disposing a plugin](#disposing-a-plugin)
- [Debugging](#debugging)
- [A complete plugin](#a-complete-plugin)

---

## Quick start

From the toolkit directory:

```bash
./rl-toolkit new my-plugin
```

This drops a working plugin in `plugins/my-plugin/`. Start the server,
open `http://localhost:8080`, and your plugin appears in the list. Click
the overlay link, score a goal in RL, and you'll see the scorer's name
update.

The whole plugin is two files:

```
plugins/my-plugin/
├── manifest.json    # how the toolkit displays your plugin
└── overlay.html     # the page itself, with your code
```

That's it. No bundler, no `node_modules`, no SDK install. Edit
`overlay.html` and refresh the page.

---

## Anatomy of a plugin

### `manifest.json`

Tells the toolkit what to call your plugin and how to render its overlay.

```json
{
  "name": "my-plugin",
  "title": "My Plugin",
  "version": "0.1.0",
  "author": "you",
  "description": "Shows the last goal scorer",
  "overlay": {
    "file": "overlay.html",
    "width": 320,
    "height": 120,
    "anchor": "top-right",
    "offset_x": 16,
    "offset_y": 16,
    "opacity": 1.0,
    "click_through": true
  }
}
```

| Field             | Required | Purpose                                                                                                |
| ----------------- | -------- | ------------------------------------------------------------------------------------------------------ |
| `name`            | yes      | Folder-safe identifier (alphanumerics, `_`, `-`). Used as the namespace for storage.                   |
| `title`           | yes      | Human-readable name shown in the dashboard.                                                            |
| `version`         | yes      | Semantic version. Changing it triggers a "loaded" log line.                                            |
| `author`          | yes      | Your handle.                                                                                           |
| `description`     | no       | Shown on the dashboard.                                                                                |
| `overlay.file`    | yes      | HTML file to load (relative to the plugin folder).                                                     |
| `overlay.width`   | yes      | Default canvas width in CSS pixels.                                                                    |
| `overlay.height`  | yes      | Default canvas height in CSS pixels.                                                                   |
| `overlay.anchor`  | yes      | One of `top-left`, `top-right`, `bottom-left`, `bottom-right`. Where the overlay pins inside its host. |
| `overlay.offset_x`| yes      | Padding in pixels from the anchored edges.                                                             |
| `overlay.offset_y`| yes      | Padding in pixels from the anchored edges.                                                             |
| `overlay.opacity` | yes      | `0.0` – `1.0`. (A value of `0.0` is treated as `1.0` — set a small ε for invisible.)                   |
| `overlay.click_through` | yes | If `true`, mouse events fall through the overlay to the game underneath.                            |

### `overlay.html`

The minimal viable plugin:

```html
<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <link rel="stylesheet" href="/sdk.css">
  <script src="/sdk.js" data-plugin="my-plugin"></script>
</head>
<body>
  <div id="who">waiting…</div>

  <script>
    RLT.plugin.register({
      events: {
        GoalScored(g) {
          document.getElementById('who').textContent = g.scorer.name;
        }
      }
    });
  </script>
</body>
</html>
```

Two things to notice:

1. **`<script src="/sdk.js" data-plugin="my-plugin">`** loads the SDK and tells it which plugin namespace to scope storage to. The `data-plugin` attribute should match the manifest `name`.
2. **`RLT.plugin.register({...})`** is how you wire up handlers. The SDK
   takes care of subscriptions, error isolation, lifecycle gating, and
   teardown.

You **don't** need to subscribe to the SSE stream yourself. You can — it's
just `new EventSource('/events')` — but the typed `RLT` API is friendlier
and better-documented.

---

## Subscribing to events

### The `events` map

The most common pattern: pass an object whose keys are event names from
the catalog and whose values are functions.

```js
RLT.plugin.register({
  name: 'my-plugin',
  version: '0.1.0',

  events: {
    GoalScored(g)      { /* ... */ },
    BallHit(hit)       { /* ... */ },
    UpdateState(state) { /* ... */ },   // 60Hz match snapshot
    MatchEnded(end)    { /* ... */ },
    Statfeed(stat)     { /* ... */ },
    '*'(name, payload) { /* catchall */ },
  },
});
```

Handlers are wrapped in `try/catch` automatically — a thrown error in
your plugin won't take down anyone else's.

### The full event catalog

The list also lives at runtime — `curl http://localhost:8080/api/events`
or `console.log(RLT.events.catalog)` in the browser console. The reference
below is the source of truth for what payload your handler will receive.

| Event              | When it fires                                                            | Live phases                            |
| ------------------ | ------------------------------------------------------------------------ | -------------------------------------- |
| `UpdateState`      | Match snapshot at PacketSendRate (~60Hz). The full per-tick world state. | `live` `replay` `paused` `countdown`   |
| `GoalScored`       | A goal was scored. Includes scorer, assister, last touch, ball impact.   | `live` `replay`                        |
| `BallHit`          | Any car touched the ball. Pre/post speed and location.                   | `live`                                 |
| `CrossbarHit`      | The ball hit a crossbar. Includes ball speed and impact force.           | `live`                                 |
| `Statfeed`         | RL's stat feed fired (demo, save, epic save, hat trick, etc).            | `live` `replay`                        |
| `ClockUpdated`     | Match clock changed by ≥1 second.                                        | `live` `countdown`                     |
| `MatchCreated`     | All teams replicated; lobby is ready.                                    | any                                    |
| `MatchInitialized` | First countdown started.                                                 | any                                    |
| `CountdownBegin`   | A round countdown began (start of round, post-goal restart).             | any                                    |
| `RoundStarted`     | Active gameplay started — countdown ended.                               | any                                    |
| `MatchPaused`      | An admin paused the match.                                               | any                                    |
| `MatchUnpaused`    | The match resumed.                                                       | any                                    |
| `GoalReplayStart`  | A goal replay began.                                                     | any                                    |
| `GoalReplayWillEnd`| The ball "exploded" during the replay — fires only if the replay isn't skipped. | any                             |
| `GoalReplayEnd`    | The goal replay ended.                                                   | any                                    |
| `MatchEnded`       | The match was decided. Has `WinnerTeamNum`.                              | any                                    |
| `PodiumStart`      | The game entered podium state.                                           | any                                    |
| `MatchDestroyed`   | The player left the match (or RL tore down the lobby).                   | any                                    |
| `ReplayCreated`    | A match-history replay loaded (NOT goal replays).                        | any                                    |

#### Payload shapes

Every typed payload includes the original RL envelope under `.raw` if you
need access to a field the SDK didn't surface. `matchGuid` (when
present) lets you correlate events that fired in the same match.

##### `UpdateState` / `onTick(state)` / `onMatch(state)`

The richest payload — a fully enriched view of the current match.

```js
{
  guid:        '550e8400-...',         // match GUID, or 'local'
  players:     [Player],               // see "Player object" below
  blue:        [Player],               // team 0 only
  orange:      [Player],               // team 1 only
  me:          Player | null,          // the player flagged isMe, or null
  game:        {...},                  // raw d.Game (arena, ball, teams, …)
  arena:       'Mannfield',            // pretty arena name
  clockSeconds: 287,                   // remaining clock (int seconds)
  overtime:    false,
  scoreBlue:   2,
  scoreOrange: 1,
  ball:        {Location, Velocity},   // null when no match
  raw:         {...},                  // raw RL UpdateState envelope
}
```

##### `GoalScored`

```js
{
  matchGuid:      '550e8400-...',
  goalSpeed:      210.4,               // kph (RL-units; convert if you want km/h)
  goalTime:       12.3,                // seconds since round start
  impactLocation: { X, Y, Z },         // where the ball crossed the goal line
  scorer:         Player,              // resolved against the live roster
  assister:       Player | null,
  ballLastTouch:  { player: Player, speed: 6321 } | null,
  raw:            {...},
}
```

##### `BallHit`

```js
{
  matchGuid: '...',
  players:   [Player],                 // every player whose car touched the ball this frame
  preSpeed:  4321,                     // ball speed before contact (RL units)
  postSpeed: 5984,                     // ball speed after contact
  location:  { X, Y, Z },
  raw:       {...},
}
```

##### `CrossbarHit`

```js
{
  matchGuid:    '...',
  ballSpeed:    7012,
  impactForce:  840,
  ballLocation: { X, Y, Z },
  ballLastTouch: { player: Player, speed: 7012 } | null,
  raw:          {...},
}
```

##### `Statfeed`

```js
{
  matchGuid: '...',
  eventName: 'Demolition',             // or 'Save', 'EpicSave', 'HatTrick', 'AerialGoal', …
  type:      'Demolition',             // RL's category — usually mirrors eventName
  target:    Player,                   // the player who got the stat
  victim:    Player | null,            // for demos, the player who got demolished
  raw:       {...},
}
```

##### `ClockUpdated`

```js
{
  matchGuid: '...',
  seconds:   286,                      // current clock in seconds
  overtime:  false,
  raw:       {...},
}
```

##### `MatchEnded`

```js
{
  matchGuid:  '...',
  winnerTeam: 0 | 1 | null,            // 0 = blue, 1 = orange
  raw:        {...},
}
```

##### Lifecycle pass-throughs

These events have no extra payload — they're notifications:

```js
// MatchCreated, MatchInitialized, CountdownBegin, RoundStarted,
// MatchPaused, MatchUnpaused, GoalReplayStart, GoalReplayWillEnd,
// GoalReplayEnd, MatchDestroyed, PodiumStart, ReplayCreated
{
  matchGuid: '...' | null,
  raw:       {...} | null,
}
```

#### The Player object

Surfaced wherever a player appears (UpdateState rosters, goal scorers,
ball-hit participants, statfeed targets/victims). The SDK enriches the
raw RL fields with derived data so you don't re-implement lookups.

```js
{
  id:             'Steam|76561198000000000|0',  // PrimaryId; '' if RL didn't ship one
  name:           'Squishy Muffinz',
  team:           0,                             // 0 = blue, 1 = orange
  isMe:           false,                         // matches the user's claimed identity
  score:          487,
  goals:          1,
  assists:        2,
  saves:          0,
  shots:          5,
  demos:          1,
  touches:        21,
  boost:          78,                            // 0..100, or null mid-respawn
  speed:          1820,                          // car velocity (RL units), or null
  onGround:       true,
  hasCar:         true,
  encounterCount: 4,                             // # of distinct matches you've shared
  aliases:        ['SquishyMuffinz', 'sm_old'],  // prior names, current name excluded
  firstSeen:      '2025-09-12T19:04:11Z',        // ISO timestamp; null if first-meet
  lastSeen:       '2026-04-30T22:18:03Z',
  platform:       'Steam',                       // or 'Epic', 'PS4', 'PS5', 'XboxOne', 'Switch', '?'
  raw:            {...},                         // original RL Player struct
}
```

In **resolved** events (`GoalScored.scorer`, `BallHit.players[i]`, etc.)
the SDK reconciles the raw RL payload (which often only carries name +
shortcut + team) against the live roster, so you get the same enriched
shape regardless of which event surfaced the player. If the player isn't
in the current roster, the resolution falls back to the raw fields with
`player: null` and `encounter: null`.

### Lifecycle gating

If you only want events while the match is actually playing, add a
`whilePhase` filter:

```js
RLT.plugin.register({
  whilePhase: ['live', 'replay'],   // ignore menu / podium / paused
  events: {
    BallHit(h) { /* only fires during live play */ }
  }
});
```

The phase machine recognises: `idle`, `created`, `countdown`, `live`,
`paused`, `replay`, `ended`, `podium`. Subscribe to phase transitions
explicitly with `onLifecycle`:

```js
RLT.plugin.register({
  onLifecycle(phase, prev) {
    console.log('phase changed:', prev, '→', phase);
  }
});
```

### Convenience subscriptions

Several common patterns get their own top-level callbacks so you don't
have to filter `UpdateState` yourself:

| Callback        | Fires                                                           |
| --------------- | --------------------------------------------------------------- |
| `init(handle)`  | Once, synchronously at register. Setup DOM here.                |
| `ready(handle)` | Once, after identity + encounter ledger have finished loading.  |
| `onTick(state)` | Every `UpdateState` (60Hz). Hot path — keep it cheap.           |
| `onMatch(state)`| Only when the match's *structure* changes (roster/score/team).  |
| `onIdentity(id)`| When the user changes which player is "me".                    |
| `onEncounters(map)` | When the encounter ledger updates.                          |
| `dispose()`     | When `handle.dispose()` is called — clean up here.              |

```js
RLT.plugin.register({
  init() {
    this.scoreEl = document.getElementById('score');
  },
  onTick(m) {
    if (!m) return;
    this.scoreEl.textContent = `${m.scoreBlue} – ${m.scoreOrange}`;
  },
});
```

### Enriched match state

Inside `onTick(m)` (and `match.current`), the SDK gives you a cleaner
view than raw `UpdateState`:

```js
m = {
  guid:        'abc123',
  players:     [...],   // each: id, name, team, isMe, score, goals, assists,
                        //       saves, shots, demos, touches, boost, speed,
                        //       encounterCount, aliases, firstSeen, lastSeen
  blue:        [...],   // players where team === 0
  orange:      [...],   // players where team === 1
  me:          {...} | null,
  scoreBlue:   2,
  scoreOrange: 1,
  arena:       'mannfield night',
  clockSeconds: 142,
  overtime:    false,
  ball:        {...},
  raw:         {...},   // the original UpdateState if you need it
}
```

Always prefer this over parsing `UpdateState` directly — it deduplicates
the work everyone else's plugin is doing.

---

## Overlay vs. dashboard mode

Every plugin page is loaded in two contexts:

| URL                                         | Mode                              |
| ------------------------------------------- | --------------------------------- |
| `/plugins/my-plugin/overlay.html`           | **Dashboard / standalone** mode.  |
| `/plugins/my-plugin/overlay.html?overlay=1` | **Overlay** mode (used by /overlay, OBS, the desktop widget). |

The `?overlay=1` query parameter is the signal. The SDK detects it
automatically and:

- adds a `body.overlay-mode` class so your CSS can switch styles
- pins the body to the manifest's `anchor` corner
- forces html/body to fill the iframe so anchoring is exact

You don't have to write any of that yourself. Just style for the two
modes:

```css
body { background: var(--rlt-bg-0); padding: 16px; }
body.overlay-mode { background: transparent; padding: 0; }
```

If you want a different layout in overlay mode (e.g. condensed view, no
title bar), branch on the class:

```css
.title { display: block; }
body.overlay-mode .title { display: none; }
```

Or, if you need to know the mode in JS:

```js
const isOverlay = document.body.classList.contains('overlay-mode');
```

### How the overlay is composed

The toolkit ships a single overlay page at `/overlay`. It loads every
plugin's HTML in a positioned `<iframe>`, sized to the manifest's
`width`/`height` and pinned to its `anchor` corner with
`offset_x`/`offset_y` padding. Plugin overlays composite together; you
don't have to coordinate with anyone else.

In OBS, add a Browser Source pointing at:

```
http://localhost:8080/overlay
```

The page is transparent by default — the source goes straight on top of
your gameplay capture.

---

## Building a dashboard view

Most plugins are happy with one mode. If you want a **richer page in the
dashboard** that shows controls or history alongside the live overlay,
just put both in the same file and branch on the mode:

```html
<body>
  <!-- dashboard chrome (hidden in overlay mode) -->
  <main class="dashboard">
    <h1>My Plugin</h1>
    <button id="reset">Reset</button>
    <div id="history"></div>
  </main>

  <!-- compact overlay view -->
  <div class="overlay-card">
    <div id="who">—</div>
  </div>

  <style>
    body.overlay-mode .dashboard { display: none; }
    body:not(.overlay-mode) .overlay-card { display: none; }
  </style>
</body>
```

The same `RLT.plugin.register` call drives both. You don't need separate
HTML files unless you want to. (See `plugins/dejavu/` for a real
two-mode plugin.)

If you prefer separate files, set `overlay.file` in the manifest to the
overlay-only page; the dashboard view is automatically `overlay.html` if
you load it without `?overlay=1`.

---

## Widget mode (desktop)

The toolkit ships a Tauri-backed desktop app, **rl-widget**, that hosts
plugin overlays as transparent always-on-top windows. When your plugin
runs inside it, you can reshape the host window from JS:

```js
RLT.widget.size(420, 200);             // resize
RLT.widget.anchor('bottom-right');     // pin to a corner
RLT.widget.margin(24, 24);             // padding from the edges
RLT.widget.opacity(0.85);              // fade the window
RLT.widget.visible(false);             // hide between matches
```

Outside the widget (OBS, plain browser tab) every call is a silent no-op
that resolves to `false`. Plugin code stays portable:

```js
if (RLT.widget.isHosted()) {
  RLT.widget.size(800, 240);
}
```

Two helpers handle the common "match the window to my content" case:

### `autoSize` — track content height *and* width

```js
RLT.widget.autoSize(true, {
  target:    '.card',     // element or selector to measure
  minWidth:  240, minHeight: 80,
  maxWidth:  800, maxHeight: 600,
});
```

Uses `ResizeObserver` to watch the target and resize the host window
once per animation frame. Pass `false` to stop. Subsequent calls
re-target rather than stack.

### `fitWidth` — grow only, never shrink

Useful for "long player name pushes the row past the manifest width".
Width is monotonic — the window widens to fit but never narrows back —
which avoids feedback loops with `max-width: 100%` chains.

```js
RLT.widget.fitWidth({
  target:   '.row',
  maxWidth: 800,    // hard cap so AAAAAAAAAAA can't take over the screen
  extra:    16,     // pixels added beyond measured width
});
```

---

## Persisting data

Every plugin gets a per-namespace key/value store. Values are arbitrary
JSON.

```js
await RLT.store.set('config', { showAssists: true });
const cfg = await RLT.store.get('config');
const all = await RLT.store.getAll();    // every key for this plugin
await RLT.store.delete('config');
```

Or, inside `register({...})`, the same store is on `this.store`:

```js
RLT.plugin.register({
  async ready() {
    const cfg = (await this.store.get('config')) || { showAssists: true };
    // ...
  }
});
```

Data is persisted to `data/<plugin-name>.json`. The store is shared
across overlay/dashboard/widget instances of the same plugin —
`RLT.store.set` from the dashboard is visible to the overlay
immediately (after a refresh).

If you need to share data with *other* plugins, write to the shared
namespace via `fetch` directly:

```js
await fetch('/api/data/_rlt/something', {
  method: 'POST',
  headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify(payload),
});
```

---

## Identity and encounters (shared)

Two pieces of state are maintained by the toolkit and shared across
plugins.

### Who is "me"?

`RLT.me` exposes the current user's `PrimaryId` so plugins can highlight
the local player without each one asking the user to claim themselves
again.

```js
RLT.me.id;                 // 'Steam|76561...|0' or '' if unclaimed
RLT.me.isReady();          // false until storage finishes loading
RLT.me.set('Steam|76561...|0');
RLT.me.clear();
RLT.me.onChange((id) => {});
```

In `onTick(m)` you also get `m.me` (the player object) and `p.isMe`
on every player.

### Encounter ledger

Every player you've ever shared a match with is tracked: aliases,
encounter count, first/last seen, win/loss against them.

```js
RLT.encounters.get('Steam|76561...|0');
// { names: ['MrFreeze', 'mrf'], count: 7,
//   first_seen: '...', last_seen: '...',
//   matches: [...], wins: 4, losses: 3 }

RLT.encounters.all();                  // every record
RLT.encounters.onChange((map) => {});  // ledger updated
```

Players in `m.players` already carry `encounterCount` and `aliases`.

---

## UI helpers

```js
RLT.ui.esc(str);            // HTML-escape a string for innerHTML
RLT.ui.platformIcon(id);    // SVG <path d=...> for Steam/Epic/PSN/Xbox/Switch
```

Design tokens are exposed via `/sdk.css` — use them so your plugin
matches the toolkit's look:

```css
.card {
  background: var(--rlt-bg-1);
  border:     1px solid var(--rlt-line);
  color:      var(--rlt-txt);
  font-family: var(--rlt-display);
}
.value { color: var(--rlt-cyan); }
```

Check `/sdk.css` for the full token list.

---

## Manual subscription (escape hatch)

If `register({ events: ... })` doesn't fit, the raw bus is still
available:

```js
const unsub = RLT.on('GoalScored', (payload) => { /* ... */ });
unsub();   // tear down

RLT.on('*', (eventName, payload) => { /* every event */ });
```

You'll lose error isolation and phase gating, but you can use this for
one-off integrations that don't fit the declarative model.

---

## Disposing a plugin

`register` returns a handle:

```js
const me = RLT.plugin.register({...});
me.dispose();          // unsubscribe everything, run dispose hook
me.disposed;           // true
```

Most plugins never dispose — they live for the page's lifetime. Disposal
matters mainly for hot-reloading during development or for plugins that
register additional plugins dynamically.

---

## Debugging

- **Connection state:** `RLT.status()` returns `'connected'` |
  `'connecting'` | `'disconnected'`. Subscribe with
  `RLT.onStatus(s => ...)`.
- **List registered plugins:** `RLT.plugin.list()` in the console.
- **Force reconnect:** `RLT._reconnect()` if the SSE stream gets stuck.
- **Inspect every event:** drop a `'*'(name, p)` handler in your `events`
  map and `console.log` it.
- **Server logs:** the toolkit logs to stdout — connection state, plugin
  load/unload, dropped slow subscribers (see the freeze-prevention
  notes in the source).

If RL itself freezes when you reload your overlay, your handler is
probably blocking the SSE write. The bus drops slow subscribers after
~1 second of buffer pressure, so you'll see a `dropped N slow
subscriber(s)` line in the server log. Check whether your handler
allocates per-frame or does sync I/O.

---

## A complete plugin

Here's a working "last goal speed" overlay in one file:

```html
<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <title>Last Goal Speed</title>
  <link rel="stylesheet" href="/sdk.css">
  <script src="/sdk.js" data-plugin="last-goal-speed"></script>
  <style>
    body { font-family: var(--rlt-display); padding: 16px;
           color: var(--rlt-txt); background: var(--rlt-bg-0); margin: 0; }
    body.overlay-mode { background: transparent; padding: 0; }
    .card { background: var(--rlt-bg-1); border: 1px solid var(--rlt-line);
            border-radius: 12px; padding: 14px 18px; }
    .label { font-size: 11px; letter-spacing: .18em; text-transform: uppercase;
             color: var(--rlt-txt-3); }
    .value { font-size: 32px; color: var(--rlt-cyan); }
    .who   { font-size: 13px; color: var(--rlt-txt-2); margin-top: 4px; }
    .empty { font-style: italic; color: var(--rlt-txt-3); }
  </style>
</head>
<body>
  <div class="card">
    <div class="label">Last Goal</div>
    <div id="speed" class="value empty">—</div>
    <div id="who" class="who"></div>
  </div>

  <script>
  (function () {
    const speedEl = document.getElementById('speed');
    const whoEl   = document.getElementById('who');

    // RL ships goal speed in Unreal Units / sec. ≈ 0.0364 → kph.
    const uuToKph = (uu) => Math.round(uu * 0.0364);

    RLT.plugin.register({
      name:    'last-goal-speed',
      version: '0.1.0',
      whilePhase: ['live', 'replay'],

      events: {
        GoalScored(g) {
          speedEl.classList.remove('empty');
          speedEl.textContent = uuToKph(g.goalSpeed || 0) + ' kph';
          whoEl.textContent   = g.scorer.name;
        },
      },

      onLifecycle(phase) {
        if (phase === 'idle') {
          speedEl.textContent = '—';
          speedEl.classList.add('empty');
          whoEl.textContent = '';
        }
      },
    });
  })();
  </script>
</body>
</html>
```

That's the whole plugin. Drop it under
`plugins/last-goal-speed/overlay.html` with a manifest (run `rl-toolkit
new last-goal-speed` and edit), and you're done.
