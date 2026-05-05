# RL Toolkit Plugins — Reference

A plugin is a folder under `plugins/` that ships one HTML file and a small
manifest. The toolkit serves the HTML, exposes a global `RLT` object, and
streams Rocket League events to it.

```
plugins/myplugin/
  manifest.json    # required
  overlay.html     # required (entry point)
  ...              # any extra css/js you reference from overlay.html
```

No build step, no framework. If you can write a `<script>` tag, you can
write a plugin.

---

## Table of contents

- [Quick start](#quick-start)
- [`manifest.json`](#manifestjson)
- [The `RLT` global](#the-rlt-global)
- [`RLT.plugin.register({...})`](#rltpluginregister)
- [Events](#events)
  - [Two surfaces: spec-compatible vs enriched](#two-surfaces-spec-compatible-vs-enriched)
  - [Spec-compatible events (Psyonix wire shape)](#spec-compatible-events-psyonix-wire-shape)
  - [Enriched synthetic events](#enriched-synthetic-events)
  - [The `Player` object](#the-player-object)
- [Match state](#match-state)
- [Identity & encounter ledger](#identity--encounter-ledger)
- [Persistence](#persistence)
- [Overlay vs dashboard mode](#overlay-vs-dashboard-mode)
- [Widget mode (desktop)](#widget-mode-desktop)
- [UI helpers](#ui-helpers)
- [Settings panel](#settings-panel)
- [Debugging](#debugging)
- [Complete plugin example](#complete-plugin-example)

---

## Quick start

```html
<!-- plugins/hello/overlay.html -->
<!doctype html>
<html>
  <head>
    <link rel="stylesheet" href="/sdk.css" />
    <script src="/sdk.js" data-plugin="hello"></script>
  </head>
  <body>
    <div id="msg">waiting for a goal…</div>
    <script>
      RLT.plugin.register({
        events: {
          _GoalScored(p) {
            document.getElementById('msg').textContent =
              p.scorer.name + ' scored! (team ' + p.scoringTeam + ')';
          },
        },
      });
    </script>
  </body>
</html>
```

```json
// plugins/hello/manifest.json
{
  "name": "hello",
  "title": "Hello",
  "version": "0.1.0",
  "author": "you",
  "description": "Says hi when someone scores.",
  "overlay": {
    "file": "overlay.html",
    "width": 320,
    "height": 80,
    "anchor": "top-right"
  }
}
```

That's a complete plugin. Drop the folder into `plugins/`, restart the
backend, and it loads.

---

## `manifest.json`

| Field | Required | Notes |
|---|---|---|
| `name` | ✅ | URL-safe id; matches the folder name. |
| `title` | ✅ | Human-readable name shown in the dashboard. |
| `version` | ✅ | SemVer string. |
| `author` | ✅ | Your name. |
| `description` | ✅ | One-line summary. |
| `overlay.file` | for overlays | Path inside the plugin folder, usually `overlay.html`. |
| `overlay.width` / `height` | for overlays | Pixel size. |
| `overlay.anchor` | for overlays | One of `top-left`, `top-right`, `bottom-left`, `bottom-right`, `center`. |
| `overlay.offset_x` / `offset_y` | optional | Pixel offset from the anchor. |
| `overlay.opacity` | optional | `0`–`1`. Default `1`. |
| `overlay.click_through` | optional | `true` makes the overlay ignore mouse input. |
| `overlay.hide_when_unfocused` | optional | `true` hides the overlay when Rocket League isn't focused. |
| `overlay.show_during_phase` | optional | Array of phases the overlay should appear in. See [phases](#match-state). |

The dashboard uses the same HTML file by default — see
[Overlay vs dashboard mode](#overlay-vs-dashboard-mode).

---

## The `RLT` global

Loading `/sdk.js` exposes one object:

| Path | What it is |
|---|---|
| `RLT.plugin` | Plugin lifecycle: `register`, `list`, etc. |
| `RLT.match` | Live match view (per-tick or per-roster-change). |
| `RLT.me` | The user's claimed identity (id, name, team). |
| `RLT.encounters` | Per-(player, match) ledger across all matches. |
| `RLT.events` | Typed event subscriptions + recent-event ring buffer. |
| `RLT.store` | Per-plugin key/value persistence (IndexedDB). |
| `RLT.ui` | DOM helpers (icons, escape, formatTime, toast). |
| `RLT.widget` | Desktop-widget controls (autosize, fitWidth). |
| `RLT.focus` | Game-focus signal (`onChange(active)`). |
| `RLT.settings` | Settings panel API (open/close from the dashboard card). |
| `RLT.stats` | String constants for known Statfeed `EventName` values. |
| `RLT.on` / `RLT.off` | Raw event bus (escape hatch). |
| `RLT.status()` / `RLT.statusStable()` | Backend connection state. |
| `RLT.pluginManifest()` / `RLT.onManifest(fn)` | Read your own manifest. |

The full surface is frozen — `RLT` itself can't be reassigned, but the
sub-objects stay live.

---

## `RLT.plugin.register({...})`

Declarative entry point. Call it once per plugin.

```js
RLT.plugin.register({
  // Metadata (defaults from manifest.json).
  name:    'myplugin',
  title:   'My Plugin',
  version: '0.1.0',

  // Phase gating: only fire handlers when the match is in one of these phases.
  // Omit (or '*') for "always". See "Match state" for the phase enum.
  whilePhase: ['live', 'replay', 'countdown', 'paused'],

  // Synchronous setup at register time.
  init(handle) { /* DOM wiring */ },
  // Async setup after identity + encounter ledger have loaded.
  ready(handle) { /* first paint, etc. */ },
  // Cleanup; called by handle.dispose().
  dispose() { /* … */ },

  // Event subscriptions (see "Events").
  events: {
    _GoalScored(p) { /* … */ },
    RoundStarted(d) { /* … */ },
    // Wildcard, fires for everything currently delivered:
    '*'(name, payload) { /* … */ },
  },

  // Convenience subscriptions (sugar over RLT.match / RLT.me / etc.).
  onTick(state)        { /* every UpdateState (~60Hz). Auto-subscribes. */ },
  onMatch(state)       { /* structural match changes only. */ },
  onRoster(state)      { /* roster join/leave/team-switch only. */ },
  onIdentity(id)       { /* user changed who is "me". */ },
  onEncounters(map)    { /* encounter ledger updated. */ },
  onLifecycle(p, prev) { /* gameplay phase transition. */ },
  onMatchActive(active){ /* "are we in a match" boolean flipped. */ },
  onFocusChange(active){ /* RL window gained/lost focus. */ },
});
```

`register()` returns a `handle`:

- `handle.store` — per-plugin scoped persistence (same shape as `RLT.store`).
- `handle.events` — array of event names you subscribed to.
- `handle.dispose()` — unsubscribe everything and call your `dispose`.
- `handle.name`, `handle.version`, `handle.author`, `handle.title`,
  `handle.manifest`.

---

## Events

### Two surfaces: spec-compatible vs enriched

The toolkit speaks Rocket League's official Stats API — every documented
event name (`GoalScored`, `BallHit`, `CrossbarHit`, `StatfeedEvent`,
`MatchEnded`, all 13 lifecycle events) fires under its original name. The
toolkit also publishes its own **synthetic** `_-prefixed` events with
extra work done server-side: player references resolved against the live
roster, same-frame correlations attached, per-tick diffs computed.

| Style | When to use |
|---|---|
| **Spec-compatible**: `events: { GoalScored(d) { d.raw.Scorer.Name } }` | You're following the [Psyonix Stats API docs](https://docs.psyonix.com/) and want the literal wire shape. |
| **Enriched**: `events: { _GoalScored(p) { p.scorer.name } }` | You want the toolkit to do the boring work for you — roster lookups, same-frame modifier attachment, score-delta own-goal detection, etc. |

Both work. Pick whichever fits your plugin.

### Spec-compatible events (Psyonix wire shape)

Every official event name fires as `{ matchGuid, raw }`, where `raw` is
the Psyonix wire payload exactly as documented at
<https://docs.psyonix.com/>. **Exception:** `ClockUpdatedSeconds` is
SDK-normalized to `{ matchGuid, seconds, overtime, raw }` for convenience.

| Event | Live phases |
|---|---|
| `UpdateState` | `live` `replay` `paused` `countdown` |
| `BallHit` | `live` |
| `ClockUpdatedSeconds` | `live` `countdown` |
| `CountdownBegin` | any |
| `CrossbarHit` | `live` |
| `GoalReplayEnd` / `GoalReplayStart` / `GoalReplayWillEnd` | any |
| `GoalScored` | `live` `replay` |
| `MatchCreated` / `MatchInitialized` / `MatchDestroyed` | any |
| `MatchEnded` | any |
| `MatchPaused` / `MatchUnpaused` | any |
| `PodiumStart` | any |
| `ReplayCreated` | any |
| `RoundStarted` | any |
| `StatfeedEvent` | `live` `replay` |

```js
events: {
  GoalScored(d) {
    // d.raw is the literal Stats API payload
    console.log(d.raw.Scorer.Name + ' scored at ' + d.raw.GoalSpeed + ' UU/s');
  },
}
```

### Enriched synthetic events

The 29 synthetic events do roster resolution, correlation, and diff
computation server-side. `Player` references arrive as full
[`Player`](#the-player-object) objects, not `{Name, Shortcut, TeamNum}`
stubs.

Browse the live registry: `curl http://localhost:8080/api/events` or read
`RLT.events.catalog` in the browser console.

#### Stability tiers

- **stable** — payload shape frozen for the major version.
- **provisional** — shape may refine in `1.x` minors as real-RL
  verification confirms field semantics. Keys won't disappear; treat
  new flags as additive.

#### Pre-resolved enrichment (mirrors raw RL events)

| Event | Stability | What it adds over the raw event |
|---|---|---|
| `_StatfeedEvent` | provisional | `mainTarget` / `secondaryTarget` resolved. |
| `_BallHit` | provisional | `players[]` resolved. |
| `_CrossbarHit` | provisional | `ballLastTouch.player` resolved. |
| `_MatchEnded` | provisional | `winnerName`, `scoreBlue`, `scoreOrange` from the cached final tick. |
| `_GoalScored` | provisional | `scorer`/`assister`/`ballLastTouch.player` resolved + `scoringTeam`, `concedingTeam`, `isOwnGoal`, `modifiers`. |

```js
// _GoalScored payload
{
  Event: '_GoalScored',
  matchGuid: '...',
  scorer:        Player,
  assister:      Player | null,
  ballLastTouch: { player: Player, speed } | null,
  goalSpeed:     91.4,                  // km/h
  goalTime:      12.3,                  // length of the round (s)
  impactLocation:{ X, Y, Z },
  scoringTeam:   0,
  concedingTeam: 1,
  isOwnGoal:     false,                  // same-frame heuristic; see _OwnGoal
  modifiers: {                           // omitted if no flags fired
    isAerialGoal:    true,
    isLongGoal:      true,
    isBackwardsGoal: true,
    isBicycleGoal:   true,
    isTurtleGoal:    true,
    isOvertimeGoal:  true,
    isPoolShot:      true,
    isHoopsSwishGoal:true,
    isHatTrickGoal:  true,
  },
}
```

#### Verified own goal

| Event | Stability | Notes |
|---|---|---|
| `_OwnGoal` | provisional | Score-delta verified. Fires when a team scores +1 and the most recent ball touch was by the opposing team. Phase-gated to `live` / `replay`. |

```js
{
  Event: '_OwnGoal',
  matchGuid: '...',
  deflector: Player,                     // opposing-team player who last touched
  scoringTeam: 0,
  concedingTeam: 1,
  scoreAfter: { blue: 1, orange: 0 },
  correlatedGoalScorer: Player | undefined,
}
```

> Why both `_OwnGoal` and `_GoalScored.isOwnGoal`? The flag on
> `_GoalScored` is a same-frame heuristic. `_OwnGoal` is verified
> against the next tick's `Game.Teams[].Score` delta and gated to
> live/replay so forfeit and mercy-rule score-ups don't false-positive.

#### Statfeed promotions

Each verified Statfeed variant gets its own enriched envelope. Names
not in the registry produce `_UnknownStatfeed` (see below).

| Event | Stability | Carries |
|---|---|---|
| `_PlayerDemolished` | provisional | `attacker`, `victim`, `isSelfDemo`, `isTeamDemo` |
| `_FlipReset` | provisional | `mainTarget` |
| `_HatTrick` | provisional | `mainTarget`, `goalsThisMatch` |
| `_Save` | provisional | `mainTarget`, `correlatedShot` (last opposing Shot) |
| `_EpicSave` | provisional | Same as `_Save`. Mutually exclusive on the wire. |
| `_Shot` | provisional | `mainTarget`, `correlatedTouch` (same-frame BallHit) |
| `_Assist` | provisional | `mainTarget`, `correlatedGoal` |
| `_Center` / `_Clear` / `_BicycleHit` | provisional | `mainTarget`, `correlatedTouch` |

```js
// _PlayerDemolished
{
  Event: '_PlayerDemolished',
  matchGuid: '...',
  attacker:   Player,
  victim:     Player,
  isSelfDemo: false,        // omitted when false
  isTeamDemo: false,        // omitted when false
}
```

#### UpdateState diffs

The backend compares each tick to the previous one and emits the change.
Subscribe to these instead of parsing `UpdateState` yourself.

| Event | Stability | Notes |
|---|---|---|
| `_PlayerJoined` | provisional | New `id` appeared this tick. |
| `_PlayerLeft` | provisional | `id` disappeared. |
| `_PlayerScoreChanged` | provisional | `delta` map only includes fields that moved. |
| `_BoostPickup` | provisional | Boost increased between ticks (not respawn). Spectator-only for opponents. |
| `_BallPossessionChanged` | provisional | `Game.Ball.TeamNum` changed. `255` → `null`. |
| `_TeamScoreChanged` | provisional | A team's `Score` moved. Pair with `_OwnGoal` to disambiguate. |

```js
// _PlayerScoreChanged
{
  Event: '_PlayerScoreChanged',
  matchGuid: '...',
  player: Player,
  delta: {                   // only fields that moved
    score:   30,
    goals:   1,
    touches: 2,
  },
}
```

#### Match milestones

Once-per-occurrence. Per-match flags reset on `MatchCreated` / `MatchDestroyed`.

| Event | Stability | Notes |
|---|---|---|
| `_FirstTouch` | provisional | First `BallHit` after each `RoundStarted` (every kickoff). |
| `_FirstBlood` | provisional | First `_GoalScored` of the match. |
| `_OvertimeStarted` | provisional | Rising edge of `Game.bOvertime`. |

#### Lifecycle + summary

| Event | Stability | Notes |
|---|---|---|
| `_LifecyclePhaseChanged` | **stable** | `from`, `to`, `phaseDurationSeconds`, `trigger`. Framing-bypass — every plugin receives this regardless of filter. |
| `_GoalReplayContext` | provisional | Fires on `GoalReplayStart` with the most recent cached `_GoalScored` (so plugins know which goal the replay is for). |
| `_MatchSummary` | provisional | Fires ~2s after `MatchEnded` (or earlier on `PodiumStart` / `MatchDestroyed`). Final scores, winner, MVP if it arrived, full per-player stats. `trigger` field tells you which path fired it. |
| `_BootId` | **stable** | First-frame on every SSE connection. Carries `bootId` (16 hex chars), the process-lifetime ID. Plugins use it to detect a backend (launcher) restart. Framing-bypass. |

```js
// _MatchSummary
{
  Event: '_MatchSummary',
  matchGuid: '...',
  winnerTeamNum: 0,
  winnerName:    'Blue Team',
  scoreBlue:     3,
  scoreOrange:   2,
  mvp:           Player | null,
  players: [
    { player: Player, score, goals, assists, saves, shots, demos },
    // …
  ],
  trigger: 'PodiumStart' | 'settleTimeout' | 'MatchDestroyed',
}
```

```js
// _BootId payload
{
  Event:  '_BootId',
  bootId: 'abc123def456...',  // 16 lowercase hex chars
}
```

The same value is available synchronously over HTTP: `GET /api/boot-id`
returns `{"bootId":"..."}`. Use the HTTP endpoint as a fallback when
the SSE first-frame might be missed (direct-mode browser sources that
reload, etc.).

#### Roster + discoverability

| Event | Stability | Notes |
|---|---|---|
| `_RosterChanged` | **stable** | Roster identity moved (join, leave, team-switch, match-guid flip). Players ship as full `Player` objects. Framing-bypass. |
| `_UnknownStatfeed` | **stable** | Statfeed `EventName` not in the verified registry. Persisted to `data/statfeed-discoveries.json` — see [`/api/statfeed-discoveries`](http://localhost:8080/api/statfeed-discoveries). |

### The `Player` object

The shape used wherever a player appears in a synthetic event payload
(`scorer`, `attacker`, `victim`, `mainTarget`, `players[i]`, etc.).

```js
{
  id:       'Steam|76561198000000000|0',  // PrimaryId; '' if RL didn't ship one
  name:     'Squishy Muffinz',
  team:     0,                             // 0 = blue, 1 = orange
  platform: 'Steam',                       // 'Steam', 'Epic', 'PS4', 'XboxOne', 'Switch'
  isBot:    false,
  isMe:     false,                         // matches RLT.me.id
  encounter: { count, names, first_seen, last_seen } | null,
}
```

`isMe` and `encounter` are stamped by the SDK on receipt — they're
client-side state (depend on the user's claimed identity and the
per-user encounter ledger), so the backend leaves them blank.

`RLT.match.current.players[i]` carries the same fields plus per-tick
physics state (`score`, `boost`, `demos`, `boosting`, `onGround`, etc.)
and the `attacker` field if the player is currently demolished.

---

## Match state

`RLT.match.current` is a fully enriched view of the live match,
rebuilt every `UpdateState` tick.

```js
{
  guid:        '550e8400-...',  // match GUID, or 'local'
  players:     [Player],         // see above
  blue:        [Player],         // team 0 only
  orange:      [Player],         // team 1 only
  me:          Player | null,    // the player flagged isMe
  game:        {...},             // raw d.Game (arena, ball, teams, …)
  arena:       'Mannfield',
  clockSeconds: 287,
  overtime:    false,
  replay:      false,             // true during goal/history replays
  hasWinner:   false,
  winner:      '',
  scoreBlue:   2,
  scoreOrange: 1,
  ball:        { Speed, TeamNum },
  target:      { Name, Shortcut, TeamNum } | null,
  raw:         {...},             // raw RL UpdateState envelope
}
```

Subscribe styles:

| Method | Fires | Auto-subscribes to |
|---|---|---|
| `RLT.match.onTick(fn)` | every `UpdateState` (~60Hz) | `UpdateState` |
| `RLT.match.onChange(fn)` | structural changes only (roster fingerprint moved) | `UpdateState` |
| `RLT.match.onRoster(fn)` | roster identity changes only | nothing — driven by `_RosterChanged` (framing-bypass) |
| `RLT.match.subscribe()` | nothing — just opts into `UpdateState` | `UpdateState` |

```js
RLT.match.onChange((state) => {
  console.log('match changed:', state.scoreBlue, '-', state.scoreOrange);
});
```

`RLT.match.lifecycle` exposes the toolkit's own gameplay-phase machine:

```js
RLT.match.lifecycle.phase            // 'none' | 'lobby' | 'countdown' | 'live' | 'replay' | 'paused' | 'podium'
RLT.match.lifecycle.matchActive      // true once we've left 'none'/'lobby'
RLT.match.lifecycle.onChange(fn)     // (phase, prev) => void
RLT.match.lifecycle.onMatchActive(fn) // (active) => void
```

`whilePhase` on `register()` gates your handlers against this enum.

---

## Identity & encounter ledger

### Who is "me"?

`RLT.me` is the user's claimed identity. The toolkit doesn't know who's
sitting at the keyboard; the user clicks "this is me" on a player row in
the dashboard, and that PrimaryId becomes their identity.

```js
RLT.me.id              // 'Steam|76561…|0' or null
RLT.me.name            // last seen display name
RLT.me.claim(id, name) // programmatically set
RLT.me.clear()         // forget
RLT.me.onChange(fn)    // (id) => void
```

Player objects carry `isMe: true` when their `id === RLT.me.id`. Use it
to highlight the current user's row, count "your" demos, etc.

### Encounter ledger

`RLT.encounters` is a per-(player, match) ledger persisted in the
browser's IndexedDB. It records every player you've shared a match with.
Used by the dejavu plugin and by `Player.encounter` on synthetic-event
payloads.

```js
RLT.encounters.get(id)  // { count, names: [], first_seen, last_seen } | null
RLT.encounters.all()     // map of id → record
RLT.encounters.onChange(fn)  // () => void
```

The ledger is bumped automatically when a roster lands during a recording
phase (anything past lobby). Plugins don't need to call anything.

---

## Persistence

`RLT.store` is a per-plugin key/value store backed by IndexedDB. Scoped
to the host plugin (the one that loaded `sdk.js`).

```js
await RLT.store.set('totals', { alice: 3, bob: 1 });
const totals = await RLT.store.get('totals');
const all    = await RLT.store.getAll();   // { key: value, … }
await RLT.store.delete('totals');
await RLT.store.clear();
```

Writes are debounced — rapid `set` calls coalesce into one disk flush.
Values must be JSON-serializable.

`handle.store` returned from `register()` is the same store, scoped to
your plugin's name. Prefer it inside `register()` callbacks.

---

## Overlay vs dashboard mode

The same `overlay.html` runs in two contexts:

- **Overlay mode** (`?overlay=1` in the URL) — transparent, sized to
  `manifest.overlay.width`/`height`, anchored per the manifest.
  `body.overlay-mode` class is set automatically.
- **Dashboard mode** (no `?overlay=1`) — full page, opaque, hosted in
  the dashboard tab.

Branch with the URL flag:

```js
const isOverlay = new URLSearchParams(location.search).has('overlay');
if (isOverlay) renderOverlay();
else           renderDashboard();
```

Or use one DOM tree and apply different CSS based on `body.overlay-mode`.

The launcher composes a single transparent **aggregator window**
containing every active overlay as an iframe. The aggregator owns the
SSE connection and forwards events to each iframe; this means hosted
overlays share one network connection regardless of how many plugins
are active. Direct-mode access (opening `overlay.html?overlay=1`
directly in a browser, useful for OBS browser sources) still works —
each direct-mode page opens its own SSE connection.

---

## Widget mode (desktop)

When the user opens an overlay as a desktop widget (separate floating
window), `RLT.widget.isHosted()` returns `true`.

```js
if (RLT.widget.isHosted()) {
  // Track content height + width.
  RLT.widget.autoSize(true, { maxWidth: 600, maxHeight: 800 });
  // Or grow-only width:
  RLT.widget.fitWidth({ target: '.ov', maxWidth: 600, extra: 8 });
}
```

`fitWidth` is monotonic — it only widens, never shrinks — useful for
content like a leaderboard row that should accommodate the longest
player name without flickering between sizes.

---

## UI helpers

`RLT.ui` is a thin DOM utility belt:

```js
RLT.ui.platformIcon('Steam')   // <svg>…</svg>
RLT.ui.playerIcon(player)      // platform icon, or bot glyph if isBot
RLT.ui.esc(str)                 // HTML-escape
RLT.ui.escAttr(str)             // attribute-safe escape
RLT.ui.cssEsc(str)              // CSS-selector-safe escape
RLT.ui.formatTime(287, false)   // "4:47"
RLT.ui.formatTime(12, true)     // "+0:12" (overtime prefix)
RLT.ui.timeAgo('2026-01-01T00:00:00Z')  // "2mo"
RLT.ui.toast('saved', 1500)     // brief banner at the bottom-center
```

`/sdk.css` ships sensible defaults for the toast, fonts (Saira
Condensed, Inter, JetBrains Mono), and CSS variables (`--rlt-bg-0`,
`--rlt-txt`, `--rlt-cyan`, etc.) you can use in your own styles.

---

## Settings panel

If your plugin has a settings page, mount it under `?settings=1` in the
same `overlay.html` and use `RLT.settings`:

```js
if (RLT.isSettingsView) {
  // render settings UI
  document.getElementById('done').onclick = () => RLT.settings.close();
}
```

The dashboard renders a "Settings" button on each plugin card when the
manifest declares `"settings": true` (or when `RLT.settings.open()` is
callable from the dashboard view).

---

## Debugging

- **Browser console.** Every plugin runs in its own iframe. Inspect with
  the browser devtools; per-plugin logs are prefixed in network /
  console panels.
- **`RLT.events.recent(name, n)`** — ring buffer of the last 50 events
  per name. `RLT.events.recent('_GoalScored', 5)` returns the last 5.
- **`RLT.events.catalog`** — frozen registry of every documented event,
  with `stability` and `since` per entry.
- **The `debug` plugin** — bundled. Live `_StatfeedEvent` log + a
  discoveries pane fed by `_UnknownStatfeed` so you can see RL emitting
  names that aren't in the verified registry yet.
- **`/api/events`** — same catalog as `RLT.events.catalog`, served by
  the backend.
- **`/api/statfeed-discoveries`** — persistent registry of unknown
  Statfeed names.
- **`/api/metrics`** — bus telemetry: subscriber count, deliveries,
  evictions, latency percentiles.

---

## Complete plugin example

A goal-counter overlay that highlights the current user's goals.

```html
<!-- plugins/goalcount/overlay.html -->
<!doctype html>
<html>
  <head>
    <link rel="stylesheet" href="/sdk.css" />
    <script src="/sdk.js" data-plugin="goalcount"></script>
    <style>
      body { font-family: var(--rlt-display); color: var(--rlt-txt); margin: 0; }
      body.overlay-mode { background: transparent; }
      .row { display: flex; justify-content: space-between; padding: 4px 12px; }
      .row.me { color: var(--rlt-cyan); font-weight: bold; }
      .count { font-variant-numeric: tabular-nums; }
    </style>
  </head>
  <body>
    <div id="root"></div>
    <script>
      RLT.plugin.register({
        whilePhase: ['live', 'replay', 'countdown', 'paused'],

        events: {
          _GoalScored(p) {
            // No work needed — we re-render from match.current below.
            // Just demonstrate that the enriched payload arrives ready.
            console.log(p.scorer.name, 'team', p.scoringTeam, 'isMe?', p.scorer.isMe);
          },
        },

        onTick(state) {
          const root = document.getElementById('root');
          root.innerHTML = state.players
            .slice()
            .sort((a, b) => b.goals - a.goals)
            .map(
              (p) =>
                `<div class="row${p.isMe ? ' me' : ''}">` +
                  `<span>${RLT.ui.esc(p.name)}</span>` +
                  `<span class="count">${p.goals}</span>` +
                `</div>`,
            )
            .join('');
        },
      });
    </script>
  </body>
</html>
```

```json
// plugins/goalcount/manifest.json
{
  "name": "goalcount",
  "title": "Goal Count",
  "version": "0.1.0",
  "author": "you",
  "description": "Live goal leaderboard.",
  "overlay": {
    "file": "overlay.html",
    "width": 280,
    "height": 200,
    "anchor": "top-left",
    "hide_when_unfocused": true,
    "show_during_phase": ["live", "replay", "countdown", "paused"]
  }
}
```

That's the full anatomy. Drop the folder under `plugins/`, restart the
backend, and the dashboard picks it up.
