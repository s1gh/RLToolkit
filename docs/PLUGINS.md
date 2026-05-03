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
  - [Things worth knowing when handling events](#things-worth-knowing-when-handling-events)
  - [How event delivery is filtered](#how-event-delivery-is-filtered-and-why-your-plugin-gets-only-what-it-asked-for)
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
   teardown — and reads your plugin's name, version, and author from
   `manifest.json` so you don't repeat them here.

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
  events: {
    GoalScored(g)      { /* ... */ },
    BallHit(hit)       { /* ... */ },
    UpdateState(state) { /* ... */ },   // 60Hz match snapshot
    MatchEnded(end)    { /* ... */ },
    StatfeedEvent(s)   { /* ... */ },
    '*'(name, payload) { /* catchall */ },
  },
});
```

> **Plugin metadata comes from `manifest.json`.** Don't repeat `name`,
> `version`, `author`, or `title` in the `register()` call — the SDK
> reads them from the manifest the toolkit already serves at
> `/api/plugins`, so a single source of truth keeps both sides in sync.
> The handle returned by `register()` exposes the resolved values:
> `handle.name`, `handle.version`, `handle.author`, `handle.title`,
> plus the full `handle.manifest` object. You can also access the
> manifest globally via `RLT.pluginManifest()` (synchronous; returns
> `null` until the fetch resolves) or `RLT.onManifest(fn)` (fires
> exactly once when ready). Pass `name`/`version`/`author`/`title` on
> the spec only when you genuinely need to override the manifest —
> e.g. tests or dynamically registered plugins.
>
> **A valid `manifest.json` is required.** The toolkit's HTTP server
> blocks requests under `/plugins/<name>/...` when the named plugin
> has no manifest or the manifest fails to parse — the response is a
> plain 404 and the watch log shows `[plugins] Bad manifest in <name>:
> ...`. Fix the manifest and the next request goes through (no server
> restart needed). The SDK still works in *out-of-toolkit* hosting
> (e.g. you embed `/sdk.js` from a page on a different origin); in
> that case `handle.name` falls back to the `data-plugin` attribute
> or the URL path segment, and `version`/`author`/`title` are `null`.

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
| `StatfeedEvent`    | RL's stat feed fired (demo, save, epic save, hat trick, etc).            | `live` `replay`                        |
| `ClockUpdatedSeconds` | Match clock changed by ≥1 second.                                     | `live` `countdown`                     |
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
  replay:      false,                  // true during goal replays / history replays
  hasWinner:   false,                  // a team has officially won
  winner:      '',                     // winning team name (e.g. 'Blue') or ''
  scoreBlue:   2,
  scoreOrange: 1,
  ball:        { Speed, TeamNum },     // ball.Speed in UU/s, TeamNum=255 = untouched
  target:      { Name, Shortcut, TeamNum } | null,  // spectator camera target, when bHasTarget
  raw:         {...},                  // raw RL UpdateState envelope
}
```

##### `GoalScored`

```js
{
  matchGuid:      '550e8400-...',
  goalSpeed:      91.4,                // ball speed at goal-line crossing, km/h (see note below)
  goalTime:       12.3,                // length of the round (seconds) that just closed
  impactLocation: { X, Y, Z },         // where the ball crossed the goal line
  scorer:         Player,              // resolved against the live roster
  assister:       Player | null,
  ballLastTouch:  { player: Player, speed: 87 } | null,  // post-touch ball speed, km/h
  raw:            {...},
}
```

> **Speed units, the messy truth.** The official RL Stats API spec
> labels both `GoalSpeed` and `BallLastTouch.Speed` as "Unreal
> Units/second", but the spec's own example values (87.3 and 125) and
> every observed value match RL's in-game **km/h** display directly. A
> goal RL displays as 91 km/h arrives here as ~91. Treat these two
> fields as km/h. `BallHit` ball speeds (`preSpeed`, `postSpeed`) and
> player `speed` from `UpdateState` *are* genuinely in UU/s — convert
> with `× 0.036` for km/h.

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

##### `StatfeedEvent`

```js
{
  matchGuid:       '...',
  eventName:       'Demolish',            // raw asset name (see table below)
  type:            'Demolition',          // localized display label
  mainTarget:      Player,                // the player who earned the stat
  secondaryTarget: Player | null,         // e.g. for demos, the demolished player
  raw:             {...},
}
```

The official Stats API docs do not enumerate `eventName` values. The
following were verified in-game using the `debug` plugin:

| eventName | type (display label) | SDK constant |
|-----------|---------------------|--------------|
| `Shot` | Shot on Goal | `RLT.stats.SHOT` |
| `Goal` | Goal | `RLT.stats.GOAL` |
| `AerialGoal` | Aerial Goal | `RLT.stats.AERIAL_GOAL` |
| `LongGoal` | Long Goal | `RLT.stats.LONG_GOAL` |
| `TurtleGoal` | Turtle Goal | `RLT.stats.TURTLE_GOAL` |
| `HatTrick` | Hat Trick | `RLT.stats.HAT_TRICK` |
| `Save` | Save | `RLT.stats.SAVE` |
| `Demolish` | Demolition | `RLT.stats.DEMOLISH` |
| `FlipReset` | Flip Reset | `RLT.stats.FLIP_RESET` |
| `Win` | Win | `RLT.stats.WIN` |

Prefer the `RLT.stats.*` constants over bare strings in plugin code —
typos surface as `undefined` (handler never matches) instead of silent
no-ops. `RLT.stats.known` is a `Set` of all verified values, useful for
filtering unknown events:

```js
events: {
  StatfeedEvent(s) {
    if (s.eventName === RLT.stats.DEMOLISH) {
      // count demos
    }
  },
},
```

This list is not exhaustive — other values likely exist for stats that
did not occur during testing (e.g. `EpicSave`, `Assist`, `BicycleGoal`,
`OvertimeGoal`). Use the `debug` plugin to discover new ones, then add
them to `RLT.stats` in `sdk.go` and to this table.

##### `ClockUpdatedSeconds`

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
  isBot:          false,                         // CPU-controlled (RL ships every bot under one sentinel id)
  score:          487,
  goals:          1,
  assists:        2,
  saves:          0,
  shots:          5,
  demos:          1,
  touches:        21,                            // total ball touches
  carTouches:     5,                             // touches by car body (not ball)
  boost:          78,                            // 0..100, or null mid-respawn
  speed:          1820,                          // car velocity, UU/s (×0.036 → km/h), or null
  boosting:       false,
  onGround:       true,
  onWall:         false,
  powersliding:   false,
  demolished:     false,                         // true on the frame this car is destroyed
  supersonic:     false,                         // true at top speed (~2300 UU/s)
  hasCar:         true,                          // false during respawn
  attacker:       null,                          // {Name, Shortcut, TeamNum} when demolished
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

### Things worth knowing when handling events

Cross-cutting advice that bites every plugin author at least once.

**Phase-gate noisy events.** Most events fire during menus, podium, and
freeplay too. If you only care about real matches, set `whilePhase:
['live']` (or `['live', 'replay']` if you want goal-replay context).
Gameplay-only events like `BallHit`, `StatfeedEvent`, and `ClockUpdatedSeconds`
mostly belong behind a phase gate — without one your plugin counts
training-mode demos and lobby-warmup goals as if they were real.

**Player `id` can be empty.** Sub-event player refs ship as
`{Name, Shortcut, TeamNum}` only — no `PrimaryId`. The SDK falls back to
roster name/shortcut matching, but in private matches with bots, or
right after a player joins (before their roster entry replicates), you
may end up with `id === ''`. If you key a Map by `mainTarget.id`,
multiple bots collide on the empty string. Defensive code: key on
`p.id || p.name`. Applies to every resolved player: `mainTarget`,
`secondaryTarget`, `scorer`, `assister`, `ballLastTouch.player`,
`BallHit.players[i]`.

**`UpdateState` is the hot path.** It fires at PacketSendRate (~60Hz).
Anything you do in `onTick`/`UpdateState` runs sixty times a second.
DOM rebuilds belong behind a content fingerprint — diff first, paint
only when something actually changed. The dejavu plugin's
`views/overlay.js` shows the pattern: build a string from the relevant
fields (`m.players.map(p => id+':'+team+':'+name).join('|')`), bail
early if it matches `lastFp`, otherwise repaint and update `lastFp`.

**`MatchCreated` arrives before the roster fills.** The event fires
when teams are first replicated — players still trickle in over the
next second. If you want the final roster, listen for the first
`UpdateState` after `MatchCreated`, or use `RLT.match.onMatch` (which
the SDK debounces to structural changes only).

**`MatchEnded` fires before `PodiumStart`.** If you tear DOM down on
`MatchEnded`, you'll lose it before the user gets to read the final
score during podium. If you want to clear, do it on `MatchDestroyed`
(player has left the lobby) or `MatchCreated` (a new match started).

**Resetting per-match state.** The pattern most plugins want is "reset
my counters at the start of each match." `MatchCreated` is the right
hook — it fires once per lobby, no replays, no podium echoes. Don't
reset on `MatchInitialized` (it can fire multiple times for the same
match in some flows) or `RoundStarted` (fires per-round in OT).

**Goal-replay events are real events.** `GoalScored` fires once during
live play AND, in some RL builds, can fire again as the replay rewinds
into the goal moment. Use `RLT.events.recent('GoalScored')` if you need
to deduplicate; the dedup key is usually
`(matchGuid, scorer.id, goalTime)`. The SDK keeps a per-event ring
buffer of the last 50 payloads:

```js
RLT.events.recent('GoalScored');     // [{ at: 1714659321831, data: {...} }, ...]
RLT.events.recent('GoalScored', 5);  // last 5
```

Each entry is `{ at, data }` — `at` is `Date.now()` at the time the
event was emitted, `data` is the same typed payload your handler
receives. Events you haven't subscribed to never reach the buffer (the
SDK doesn't widen the SSE filter for `recent()` lookups).

**Persistence: per-plugin store vs shared encounter ledger.** Anything
specific to your plugin's logic goes in `RLT.store` (per-plugin
namespace). Anything that's about the *players themselves* — names,
encounters, win/loss against you — belongs on the shared ledger via
`RLT.encounters` so other plugins benefit. A demo tracker should put
its per-match map in `RLT.store`; a "have I demoed this person before"
counter should extend the encounter record.

**The raw envelope is always there.** Every typed payload ships the
original RL data under `.raw`. If the SDK didn't surface a field you
need, it's still reachable: `goal.raw.SomeFieldRLAdded`. Don't fork
the SDK to add a field — read it from `raw` in your plugin and, if
it's broadly useful, send a PR upstream.

### How event delivery is filtered (and why your plugin gets only what it asked for)

You don't need to think about this most of the time, but it's
useful to understand when something doesn't fire when you expected.

The toolkit's SSE stream supports per-subscriber filtering:

```
EventSource('/events?events=GoalScored,StatfeedEvent')
```

The SDK builds that URL automatically by tracking which events your
handlers request. When you write `events: { GoalScored }` or call
`RLT.events.onBallHit(fn)`, the SDK adds the event name to the
filter and (if already connected) reconnects the EventSource with
the new list. This means your plugin's webview only does
`JSON.parse` on events you actually subscribed to — important at
60Hz, more important at 120Hz.

The filter always includes the RL lifecycle events
(`MatchCreated`, `RoundStarted`, `MatchEnded`, etc.) — these drive
phase tracking and the reset semantics on `RLT.match.lifecycle`,
and they're rare (a handful per match) so they're free to subscribe
to.

`UpdateState` is **opt-in**, not always-on. It's the heaviest event
by far (~1-3 KB at 60-120 Hz, dominates total bandwidth) so plugins
only get it when they need it. The SDK auto-subscribes when:

  - You register an `UpdateState` event handler, OR
  - You register an `onTick` or `onMatch` handler, OR
  - You call `RLT.match.onChange(...)` or `RLT.match.onTick(...)`, OR
  - You call `RLT.match.subscribe()` explicitly.

Plugins that only react to events (`GoalScored`, `StatfeedEvent`,
`BallHit`, etc.) and never read `RLT.match.current` directly stay
off the tick stream and pay near-zero bandwidth.

**Side effect:** without `UpdateState`, `RLT.match.current` is `null`
and the enriched `.player` field on event payloads (e.g.
`g.scorer.player`) is also `null` — the event's own `.name`,
`.shortcut`, `.team` still work. If you need the full enriched
roster lookup, opt in via `RLT.match.subscribe()` at plugin init.

Synthetic events with a `_` prefix (`_ConnectionStatus`,
`_Lifecycle`) bypass the filter entirely on the server side —
they're framing signals every subscriber needs.

**Wildcard catchalls** (`'*'` in `spec.events` or `RLT.on('*', fn)`)
fire on whatever the bus *already* gets. They don't widen the
filter, so a wildcard handler in a plugin that only declared
`GoalScored` will still only see `GoalScored` (plus the SDK's
internal essentials and the synthetic events).

**To see the active filter** for an open page, check the EventSource
URL in DevTools → Network → EventStream tab.

### Lifecycle gating

The toolkit tracks two related but distinct signals server-side and
ships them as a single `_Lifecycle` snapshot:

  - **`match_active`** (bool) — am I in a match? The right question for
    "should the widget be on screen". True after `MatchCreated`, false
    after `MatchDestroyed`, the connection drops, OR no `UpdateState`
    arrives for 5 seconds (catches "user backed out to menu without RL
    emitting MatchDestroyed").
  - **`phase`** (enum) — what gameplay phase is happening right now?
    The right question for "should I count this goal".

The phase enum: `none`, `created`, `countdown`, `live`, `paused`,
`replay`, `ended`, `podium`. **Use `'none'`** in new plugins. Older
code wrote `'idle'` instead — `whilePhase: ['idle']` still matches
`'none'` for back-compat, but `lifecycle.phase` itself only ever
reports `'none'`. So `if (phase === 'idle') { … }` will not fire
in modern plugins; rewrite to `phase === 'none'`.

If you only want events while the match is actually playing, gate
with `whilePhase`:

```js
RLT.plugin.register({
  whilePhase: ['live', 'replay'],   // ignore menu / podium / paused
  events: {
    BallHit(h) { /* only fires during live play */ }
  }
});
```

Subscribe to phase transitions with `onLifecycle`, or to the simpler
match-active question with `onMatchActive`:

```js
RLT.plugin.register({
  onLifecycle(phase, prev) {
    console.log('phase changed:', prev, '→', phase);
  },
  onMatchActive(active) {
    // Fires once when match_active flips. Reliable across all the
    // "user left the match" paths — clean exit, RL crash, alt-tab
    // away from menu, etc.
    if (!active) clearMyState();
  }
});
```

For the current values without subscribing:

```js
RLT.match.lifecycle.phase        // 'live' / 'none' / etc.
RLT.match.lifecycle.matchActive  // bool
RLT.match.lifecycle.guid         // current match GUID, or ''
RLT.match.lifecycle.previous     // previous phase
```

Or query the server directly: `GET /api/lifecycle`.

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
| `onLifecycle(phase, prev)` | When the gameplay phase transitions.                  |
| `onMatchActive(active)` | When `match_active` flips — the simpler "in a match" signal. |
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

RLT.encounters.isBotId('Unknown|0|0'); // true — same check as Player.isBot,
                                       // useful when iterating the raw map
                                       // (Object.entries(encounters.all())).
```

Players in `m.players` already carry `encounterCount`, `aliases`, and `isBot`.
Prefer `p.isBot` over comparing `id` strings — the SDK abstracts RL's
sentinel format so plugins don't break if it changes.

---

## UI helpers

```js
RLT.ui.esc(str);            // HTML-escape a string for innerHTML
RLT.ui.platformIcon('Steam'); // SVG markup for a platform brand icon
RLT.ui.playerIcon(p);         // Like platformIcon, but returns a CPU
                              // icon for bots — prefer this for player
                              // rows so AI players don't render a blank
                              // icon slot.
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

The typed accessors on `RLT.events` work the same way:

```js
const unsubGoal = RLT.events.onGoalScored((g) => { /* ... */ });
unsubGoal();   // store and call this to detach

// Note: there are no off-by-name typed methods (no `offGoalScored`).
// The unsub function returned at subscribe time is the only way to
// detach a typed handler. RLT.events.off(name, fn) does work for
// generic detach by reference.
```

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
  `RLT.onStatus(s => ...)`. The toolkit's connection to Rocket League
  cycles every 30s of menu idle by design (TCP idle-timeout reconnect),
  so this signal will flip connected → connecting → connected several
  times per session even when nothing is wrong. For visible UI that
  surfaces "are we live?", prefer the **debounced** view:
  `RLT.statusStable()` and `RLT.onStatusStable(s => ...)` — same values,
  but brief reconnect cycles never cross the threshold to non-connected.
  Coming back to `connected` is instant in both views.
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

    // g.goalSpeed is already in km/h despite what RL's spec text says —
    // see the "Speed units, the messy truth" note above.

    // Name + version come from manifest.json automatically; we only
    // declare runtime behaviour here.
    RLT.plugin.register({
      whilePhase: ['live', 'replay'],

      events: {
        GoalScored(g) {
          speedEl.classList.remove('empty');
          speedEl.textContent = Math.round(g.goalSpeed || 0) + ' km/h';
          whoEl.textContent   = g.scorer.name;
        },
      },

      onLifecycle(phase) {
        if (phase === 'none') {
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

## Migration notes

### 2026-05-02 — Normalized shapes for `ball` and `player.attacker`

Two fields on `RLT.match.current` have been migrated from raw
wire-shape passthroughs (PascalCase keys) to the enriched
lowercase-key + `.raw` pattern used everywhere else in the SDK.

- **`match.current.ball`** — was `{ Speed, TeamNum }`; is now
  `{ speed, teamNum, lastTouchTeam, raw }`. `lastTouchTeam`
  normalizes the API's `TeamNum === 255` "ball has not been
  touched yet" sentinel to `null`.
  - `m.ball.Speed` → `m.ball.speed` (or `m.ball.raw.Speed`).
  - `m.ball.TeamNum === 255` → `m.ball.lastTouchTeam === null`.

- **`match.current.players[i].attacker`** — was the raw
  `{ Name, Shortcut, TeamNum }` stub; is now the same enriched
  shape used by typed event payloads: `{ name, shortcut, team,
  id, isMe, player, encounter, raw }`. The original wire stub
  is on `attacker.raw`.
  - `player.attacker.Name` → `player.attacker.name`
    (or `player.attacker.raw.Name`).

### New fields (additive, no migration needed)

- `match.current.teams` — array of `{ teamNum, name, score,
  colorPrimary, colorSecondary, raw }`. Hex colors are raw (no `#`).
- `match.current.blueTeam` / `match.current.orangeTeam` — split
  shortcuts mirroring the existing `match.blue` / `match.orange`
  player splits.
- `match.current.replayInfo` — `null` outside replays;
  `{ frame, elapsed }` while a replay is active.

## Overlay editor

Open `http://localhost:8080/overlay?edit=1` in a real desktop browser
(Chrome, Firefox) — separate from OBS — to drag-position your plugin
widgets directly. Drag the body of a widget to move it; drag the
bottom-right handle to resize. Click a widget to reveal a control panel
in the top-right corner with anchor, width, height, opacity, and a
"Reset to manifest" button. The top bar has a "Reset all" that clears
every override.

Edits save automatically on drop / commit to
`data/overlay-overrides.json`. The file is keyed by plugin name and
holds only the fields you've changed; everything else falls back to the
plugin's `manifest.json` `overlay` block. So plugin updates don't
clobber your layout, and the same plugin can ship to two streamers with
different positions on each side.

Drag offsets snap to an 8px grid; hold Shift while dragging to bypass
the snap. Switching anchor corners recomputes offsets so the widget
visually stays in place.

OBS browser sources never see `?edit=1` — you only put `/overlay` in
OBS — so the editor chrome can never accidentally render in your
stream.

For predictable results, edit at the same browser window size as your
OBS canvas (typically 1920×1080); the saved offsets are pixel values
relative to the visible viewport.
