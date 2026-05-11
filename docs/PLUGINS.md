# Writing RL Toolkit Plugins

A guide for building plugins that consume Rocket League events and render
overlays on top of the game.

---

## Table of contents

- [What a plugin is](#what-a-plugin-is)
  - [Render contexts](#render-contexts)
- [The 5-minute Hello World](#the-5-minute-hello-world)
- [Project layout](#project-layout)
- [Packaging and distribution](#packaging-and-distribution)
- [Manifest reference](#manifest-reference)
- [The `RLT` global](#the-rlt-global)
  - [Plugin lifecycle: `RLT.plugin.register`](#plugin-lifecycle-rltpluginregister)
  - [Identity: `RLT.me`](#identity-rltme)
  - [Match state: `RLT.match`](#match-state-rltmatch)
  - [Phase machine: `RLT.match.state`](#phase-machine-rltmatchstate)
  - [Encounter ledger: `RLT.encounters`](#encounter-ledger-rltencounters)
  - [Per-plugin storage: `RLT.store`](#per-plugin-storage-rltstore)
  - [UI helpers: `RLT.ui`](#ui-helpers-rltui)
  - [Stats registry: `RLT.stats`](#stats-registry-rltstats)
  - [Widget control: `RLT.widget`](#widget-control-rltwidget)
  - [Connection status: `RLT.status` / `RLT.statusStable`](#connection-status-rltstatus--rltstatusstable)
  - [Settings panel: `RLT.settings`](#settings-panel-rltsettings)
  - [Misc utilities: `RLT.util`](#misc-utilities-rltutil)
  - [Low-level event bus: `RLT.on` / `RLT.off`](#low-level-event-bus-rlton--rltoff)
- [Event reference](#event-reference)
  - [The `EnrichedPlayer` shape](#the-enrichedplayer-shape)
  - [Lifecycle events](#lifecycle-events)
  - [Match-state synthetics](#match-state-synthetics)
  - [Roster events](#roster-events)
  - [Goal & scoring events](#goal--scoring-events)
  - [Touch & ball events](#touch--ball-events)
  - [Statfeed events](#statfeed-events)
  - [Demo correlation](#demo-correlation)
  - [Tick-diff events](#tick-diff-events)
  - [Tick stream](#tick-stream)
  - [Identity & boot signals](#identity--boot-signals)
- [Phase gating](#phase-gating)
- [Settings panel](#settings-panel)
- [Player MMR (`/api/mmr`)](#player-mmr-apimmr)
- [Best practices](#best-practices)

---

## What a plugin is

A plugin is a self-contained directory under `plugins/` containing a
`manifest.json` and at least one HTML file. The toolkit's Go backend
serves the HTML to the Tauri overlay window (and the dashboard's
preview iframe). The HTML pulls in `/sdk.js`, which exposes
`window.RLT` — your full API surface for reading match state and
subscribing to events.

You do not run any backend code. Plugins are pure HTML + JS + CSS.

### Render contexts

A plugin can have up to **four views**, each its own HTML file
declared in the manifest:

| View           | Where it renders                                                                                     | Purpose                                                                                              | Required? |
| -------------- | ---------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------- | --------- |
| **Overlay**    | Tauri widget window floating on top of the game.                                                     | Compact, transparent, click-through HUD.                                                              | Yes       |
| **Dashboard**  | Full browser tab, opened from the dashboard's "Open" button.                                         | Rich UIs that don't fit a transparent overlay — history tables, charts, leaderboards, configuration. | Optional  |
| **Settings**   | Iframe modal inside the dashboard, opened from the "Settings" button.                                | Per-plugin configuration UI.                                                                          | Optional  |
| **Background** | Hidden iframe inside the launcher window. Loads at app startup, lives until the launcher closes.     | Always-on work that has to react to events independent of any visible UI surface.                     | Optional  |

Each view file declares which view it is via a `data-view` attribute
on the SDK script tag:

```html
<script src="/sdk.js" data-plugin="my-plugin" data-view="overlay"></script>
<script src="/sdk.js" data-plugin="my-plugin" data-view="dashboard"></script>
<script src="/sdk.js" data-plugin="my-plugin" data-view="settings"></script>
<script src="/sdk.js" data-plugin="my-plugin" data-view="background"></script>
```

The SDK exposes the current view as `RLT.view` and boolean shorthands:

```js
RLT.view              // 'overlay' | 'dashboard' | 'settings' | 'background'
RLT.isOverlay         // === 'overlay'
RLT.isDashboard       // === 'dashboard'
RLT.isSettingsView    // === 'settings'
RLT.isBackground      // === 'background'
```

#### When to use the background view

The overlay only loads while the Tauri overlay window is shown over
the game (or an OBS browser source is open). The dashboard only loads
while the user has that plugin's tab open. **Neither is reliable for
event-driven work.** If your plugin has to react to an event the
moment it fires — uploading a saved replay, playing a sound, posting
to a webhook — put that handler in a background view.

The background view has no UI (its iframe is `display:none`) and is
loaded by the launcher for every enabled plugin that declares
`background.file`. Lifetime is the launcher's lifetime; it survives
dashboard reloads, plugin tab navigation, and overlay visibility
changes.

`ballchasing-upload` and `crossbar` both use this pattern — settings
panel for config, overlay reduced to a stub, all real work in
`background.html`.

Why three files instead of one HTML branched at runtime? Each view has
distinct UI, distinct stylesheet needs, and different lifetimes. Three
files = three small concerns; one file = a giant `if (isOverlay)`
ladder that's hard to read. You're free to share JS modules across
them — the dashboard and overlay typically both pull in the same
`state.js`, and the settings view writes the config that the others
read.

Behaviors that vary by view:

- **`RLT.widget.*` calls** (resize, anchor, opacity, autoSize, …) only
  do anything inside the Tauri overlay window. In dashboard/settings
  contexts they no-op gracefully (resolve to `false`). Safe to call
  unconditionally; gate on `RLT.widget.isHosted()` if the no-op would
  hide a real bug.
- **`RLT.store` writes** are allowed from the overlay and settings
  views (which are user-controlled and trustworthy). The dashboard
  view is read-only by default; override with `allowWrites: true` on
  `register()` if you have interactive write UI in the dashboard.
- **`RLT.settings.close()`** only does anything in the settings view
  (it posts to the dashboard parent telling it to close the modal).
- **Cross-view state sync** is automatic via `RLT.store.onChange` —
  when the settings panel writes, the overlay and dashboard handlers
  fire immediately. See the example below.

---

## The 5-minute Hello World

Goal: log every goal scorer's name and the ball speed at the moment of
the goal. Show a running list of recent goals in a dashboard view. Add
a settings panel where the user can toggle whether to log own-goals
too — and have the change take effect immediately in both the overlay
and the dashboard.

Five files in `plugins/hello-world/`:

```
plugins/hello-world/
├── manifest.json
├── overlay.html        # the in-game HUD
├── dashboard.html      # full-page browser view
├── settings.html       # iframe settings panel
└── shared.js           # config helpers used by all three views
```

### 1. The manifest

**`plugins/hello-world/manifest.json`**

```json
{
  "name": "hello-world",
  "title": "Hello World",
  "version": "0.1.0",
  "author": "you",
  "description": "Logs every goal scorer + ball speed.",
  "overlay": {
    "file": "overlay.html",
    "width": 320,
    "height": 80,
    "anchor": "top-right"
  },
  "dashboard": { "file": "dashboard.html" },
  "settings":  { "file": "settings.html" }
}
```

The dashboard's per-plugin card will show:

- An **"Open Dashboard"** button that opens `dashboard.html` in a new tab.
- A **"Settings"** button that opens `settings.html` in a modal iframe.
- An enable/disable toggle for the overlay (the toolkit takes care of
  showing/hiding the Tauri widget for you).

### 2. Shared helpers

**`plugins/hello-world/shared.js`**

```js
// Loaded by all three views. Reading + writing config goes through
// here so the overlay and dashboard agree on defaults and the
// settings panel uses the same key. Exports onto window.HW.
(function () {
  'use strict';

  const STORE_KEY = 'config';
  const DEFAULTS = { logOwnGoals: false };

  async function readConfig() {
    const cfg = await RLT.store.get(STORE_KEY);
    return Object.assign({}, DEFAULTS, cfg || {});
  }

  async function writeConfig(patch) {
    const cfg = await readConfig();
    await RLT.store.set(STORE_KEY, Object.assign({}, cfg, patch));
  }

  window.HW = { readConfig, writeConfig };
})();
```

### 3. The overlay (runs in the Tauri widget)

**`plugins/hello-world/overlay.html`**

```html
<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <title>Hello World</title>
    <link rel="stylesheet" href="/sdk.css" />
    <script src="/sdk.js" data-plugin="hello-world" data-view="overlay"></script>
  </head>
  <body>
    <div id="last-goal">queue up — waiting for a goal</div>

    <script src="./shared.js"></script>
    <script>
      // Cache config locally. Hydrated lazily in init, then refreshed
      // automatically whenever the settings panel writes to the store
      // (RLT.store.onChange fires for any cross-view write to our
      // namespace). If a goal lands before the initial fetch resolves,
      // logOwnGoals is just false — the safe default.
      let logOwnGoals = false;
      async function refreshConfig() {
        ({ logOwnGoals } = await HW.readConfig());
      }

      RLT.plugin.register({
        init() {
          // Fire-and-forget initial fetch. init is sync — don't await.
          refreshConfig();

          // Re-read whenever any view writes to our store.config key
          // (typically the settings panel). The 'config' filter scopes
          // the subscription so we don't refetch on unrelated writes.
          RLT.store.onChange('config', refreshConfig);

          // RLT.widget.* calls no-op outside Tauri, so this is safe
          // to call unconditionally.
          RLT.widget.autoSize(true, { target: document.body });
        },

        events: {
          _GoalScored(goal) {
            if (goal.isOwnGoal && !logOwnGoals) return;
            const scorer = goal.scorer?.name ?? 'unknown';
            const speed = goal.goalSpeed ?? 'n/a';
            const tag = goal.isOwnGoal ? ' (own goal)' : '';
            document.getElementById('last-goal').textContent =
              `${scorer} scored at ${speed} km/h${tag}`;
          },
        },
      });
    </script>
  </body>
</html>
```

### 4. The dashboard (runs in a browser tab)

**`plugins/hello-world/dashboard.html`**

```html
<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <title>Hello World</title>
    <link rel="stylesheet" href="/sdk.css" />
    <script src="/sdk.js" data-plugin="hello-world" data-view="dashboard"></script>
    <style>
      body { font: 14px/1.4 var(--rlt-ui, system-ui); padding: 24px; max-width: 720px; }
      h1 { margin: 0 0 16px; }
      ul { padding: 0; list-style: none; }
      li { padding: 6px 0; border-bottom: 1px solid var(--rlt-line, #2a2e3a); }
      .empty { color: var(--rlt-txt-2, #a6abbf); font-style: italic; }
    </style>
  </head>
  <body>
    <h1>Hello World</h1>
    <p>Recent goals (this session):</p>
    <ul id="goals"><li class="empty">no goals yet</li></ul>

    <script src="./shared.js"></script>
    <script>
      const log = [];
      const ul = document.getElementById('goals');

      function render() {
        if (log.length === 0) {
          ul.innerHTML = '<li class="empty">no goals yet</li>';
          return;
        }
        ul.innerHTML = log
          .slice(-20)
          .reverse()
          .map((g) => `<li>${RLT.ui.esc(g)}</li>`)
          .join('');
      }

      let logOwnGoals = false;
      async function refreshConfig() {
        ({ logOwnGoals } = await HW.readConfig());
      }

      RLT.plugin.register({
        init() {
          refreshConfig();
          RLT.store.onChange('config', refreshConfig);
        },

        events: {
          _GoalScored(goal) {
            if (goal.isOwnGoal && !logOwnGoals) return;
            const scorer = goal.scorer?.name ?? 'unknown';
            const speed = goal.goalSpeed ?? 'n/a';
            const tag = goal.isOwnGoal ? ' (own goal)' : '';
            log.push(`${scorer} — ${speed} km/h${tag}`);
            render();
          },
        },
      });
    </script>
  </body>
</html>
```

### 5. The settings panel (runs in a dashboard iframe)

**`plugins/hello-world/settings.html`**

```html
<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <title>Hello World — Settings</title>
    <link rel="stylesheet" href="/sdk.css" />
    <script src="/sdk.js" data-plugin="hello-world" data-view="settings"></script>
  </head>
  <body style="padding: 24px; max-width: 480px; font: 14px/1.4 var(--rlt-ui, system-ui)">
    <h2>Hello World settings</h2>

    <label>
      <input type="checkbox" id="own-goals" /> Also log own goals
    </label>

    <p>
      <button id="save">Save</button>
      <button id="cancel">Close</button>
    </p>

    <script src="./shared.js"></script>
    <script>
      RLT.plugin.register({
        init() {
          const checkbox = document.getElementById('own-goals');

          // Hydrate the checkbox from the store. init is sync, so wrap
          // the await in an inline async IIFE.
          (async () => {
            const cfg = await HW.readConfig();
            checkbox.checked = cfg.logOwnGoals;
          })();

          document.getElementById('save').onclick = async () => {
            await HW.writeConfig({ logOwnGoals: checkbox.checked });
            // The overlay + dashboard both subscribed to
            // store.onChange, so this write broadcasts _StoreChanged
            // → their refreshConfig handlers fire → caches update. No
            // extra signaling needed.
            RLT.settings.close();
          };

          document.getElementById('cancel').onclick = () => RLT.settings.close();
        },
      });
    </script>
  </body>
</html>
```

> **Cross-view state sync** is powered by `RLT.store.onChange`. The
> backend broadcasts `_StoreChanged` on every successful write or
> delete; the SDK filters by your plugin's namespace and (optionally)
> a single key. The overlay and dashboard re-hydrate their cached
> config the moment the settings panel saves — no polling, no reload.

### What you'll see

Drop the directory under `plugins/` and restart the toolkit:

- **Tauri widget** opens `overlay.html`. The text on screen updates
  with each goal — `<scorer> scored at <speed> km/h`.
- **Dashboard card** for "Hello World" shows three buttons: enable
  toggle, "Open Dashboard", "Settings". Clicking "Open Dashboard"
  opens `dashboard.html` in a new tab; the goal list there fills up
  in real time.
- Clicking "Settings" opens `settings.html` in a modal. Tick "Also
  log own goals" → Save. From that moment, own goals appear in both
  the overlay and the dashboard log without reopening anything.

Why `_GoalScored` instead of `GoalScored`? The underscore prefix
denotes a *synthetic* event — produced by the backend after enriching
the raw RL event with resolved player references, ball speed, and goal
modifiers. You almost always want the synthetic version.

---

## Project layout

```
plugins/
└── my-plugin/
    ├── manifest.json     # required
    ├── overlay.html      # required (declared in manifest.overlay.file)
    ├── dashboard.html    # optional (declared in manifest.dashboard.file)
    ├── settings.html     # optional (declared in manifest.settings.file)
    ├── shared.js         # optional, loaded from any view
    ├── styles.css        # optional, loaded by your HTML
    └── …                 # split JS / CSS however you like
```

The backend serves the entire plugin directory at `/plugins/<name>/`.
Load any sibling file with relative URLs:

```html
<script src="./shared.js"></script>
<link rel="stylesheet" href="./styles.css" />
```

Load shared SDK assets with absolute URLs (they aren't in your plugin
folder):

```html
<link rel="stylesheet" href="/sdk.css" />
<script src="/sdk.js" data-plugin="my-plugin" data-view="overlay"></script>
```

The `data-view` attribute on the SDK script tag tells the SDK which
view it's in. Use `"overlay"` / `"dashboard"` / `"settings"` to match
the manifest. Without it, the SDK defaults to `"overlay"`.

---

## Packaging and distribution

A finished plugin ships as a single `.rltp` file — a ZIP archive whose
root contains `manifest.json` and the rest of the plugin's assets, with
no nested top-level folder. The canonical filename is
`<name>-<version>.rltp`, derived from the manifest.

The `rl-toolkit` binary has subcommands for working with `.rltp`
files:

```sh
rl-toolkit pack [-out <dir>] <plugin-folder>
# Zips a plugin source folder into <name>-<version>.rltp.

rl-toolkit install [-plugins <dir>] <file.rltp>
# Unzips a .rltp into the plugins directory (default: the per-OS
# RLToolkit data dir, e.g. ~/.local/share/RLToolkit/plugins on Linux,
# %LOCALAPPDATA%\RLToolkit\plugins on Windows, or
# ~/Library/Application Support/RLToolkit/plugins on macOS).

rl-toolkit uninstall [-plugins <dir>] <name>
# Removes an installed plugin folder.

rl-toolkit dev <plugin-folder>
# Hot-reload an unpackaged plugin folder against the running backend.
```

`rl-toolkit dev` is the daily-driver loop while authoring. The backend
must be running with the dev API enabled (see below):

```sh
rl-toolkit -dev                    # in terminal A — server with dev API on
rl-toolkit dev plugins/my-plugin   # in terminal B
```

The dev API is **off by default**; pass `-dev` to `rl-toolkit` (or
launch the launcher with `RLT_DEV=1` set) to turn it on. The CLI then
discovers the backend's localhost-only dev port via a `dev.port` file
inside the RLToolkit application directory:

- Linux: `$XDG_DATA_HOME/RLToolkit/dev.port` (or
  `~/.local/share/RLToolkit/dev.port`)
- macOS: `~/Library/Application Support/RLToolkit/dev.port`
- Windows: `%LOCALAPPDATA%\RLToolkit\dev.port`

The launcher and CLI agree on this location automatically; no flags
needed. `rl-toolkit dev` registers the folder, watches it for changes
(debounce ~150ms), and tells every view of the plugin (overlay,
dashboard tab, settings panel) to reload on each save — no manual
browser refresh needed. Ctrl-C unregisters and exits cleanly. The
installed plugin directory is never modified; the dev plugin lives
entirely in memory and shadows any installed plugin of the same name
until you Ctrl-C.

If `rl-toolkit dev` errors with `read dev.port: ... (is rl-toolkit
running with -dev, or the launcher with RLT_DEV=1?)`, the backend
isn't running with the dev API on. Restart it with `-dev`.

End users install third-party plugins two ways: the dashboard's
"Install plugin…" button (in the Plugins section header) opens a
native file picker for a `.rltp`, or `rl-toolkit install path/to/plugin.rltp`
from a terminal. Both run the same install path: validate the
manifest, replace the existing plugin folder if any, write the new
files. The picker uploads to the same `POST /api/sideload` endpoint
the CLI's HTTP path uses.

To remove a plugin, click the trash icon on its dashboard card (a
confirm modal protects against accidents) or run `rl-toolkit uninstall <name>`.
Uninstalling a plugin that's currently registered for `rl-toolkit dev`
hot-reload is refused — stop the dev session first.

---

## Manifest reference

| Field         | Type   | Required | Notes                                                                  |
| ------------- | ------ | -------- | ---------------------------------------------------------------------- |
| `name`        | string | yes      | Directory-name match. Used as the namespace for `RLT.store`.           |
| `version`     | string | yes      | Semver. Logged at register time.                                       |
| `overlay`     | object | yes      | The Tauri widget view. See `overlay` table below.                      |
| `title`       | string | no       | Display name in the dashboard. Defaults to `name` when omitted.        |
| `author`      | string | no       | Shown in the dashboard plugin list.                                    |
| `description` | string | no       | Shown in the dashboard plugin list.                                    |
| `dashboard`   | object | no       | `{ "file": "dashboard.html" }` — full-page browser view.               |
| `settings`    | object | no       | `{ "file": "settings.html" }` — iframe modal in the dashboard.         |
| `background`  | object | no       | `{ "file": "background.html" }` — hidden always-on iframe in the launcher. |
| `permissions` | object | no       | Sandbox relaxations. See `permissions` section below.                  |

### `overlay` object

The overlay carries Tauri-specific sizing and gating because that's
the only view that runs in a window of its own.

| Field                 | Type           | Required | Notes                                                                  |
| --------------------- | -------------- | -------- | ---------------------------------------------------------------------- |
| `file`                | string         | yes      | Path relative to the plugin dir. Usually `"overlay.html"`.             |
| `width`               | int            | no       | Initial window width in CSS pixels. Recommended; `0` falls back to platform default. |
| `height`              | int            | no       | Initial window height. Recommended; `0` falls back to platform default. |
| `anchor`              | string         | no       | One of `top-left`, `top-right`, `bottom-left`, `bottom-right`. Empty string falls back to `bottom-left`. |
| `offset_x`            | int            | no       | Pixels from the anchored edge horizontally.                            |
| `offset_y`            | int            | no       | Pixels from the anchored edge vertically.                              |
| `opacity`             | float (0–1)    | no       | Window opacity. A literal `0` is reset to `1.0` by the validator — pick a small non-zero value (e.g. `0.01`) for near-invisible. |
| `hide_when_unfocused` | boolean / null | no       | If `true`, overlay hides when the game window loses focus. `null`/absent uses the SDK default. |
| `show_during_phase`   | array          | no       | Phase names to show during. Hidden in all other phases. See [phase gating](#phase-gating). |
| `unmount_outside_phase` | boolean      | no       | When `true` (and `show_during_phase` is non-empty), the plugin's iframe is unmounted outside the listed phases — events stop being delivered, JS stops running. Default `false` (visual gate only). See [phase gating](#phase-gating). |

### `dashboard`, `settings`, and `background` objects

All three are simple `{ "file": "<name>.html" }` pointers today.
Object form (rather than a bare string) leaves room for per-view
options later without another schema break.

```json
"dashboard":  { "file": "dashboard.html" },
"settings":   { "file": "settings.html" },
"background": { "file": "background.html" }
```

The validator rejects any view whose `file` doesn't exist on disk
inside the plugin folder, or whose path tries to escape the folder.

### `permissions` object

Plugin iframes are served from the toolkit's own origin, so a
`fetch()` to any `/api/...` path works directly. Calls to external
APIs run into the browser's CORS policy: any third-party host that
doesn't return permissive `Access-Control-Allow-*` headers will
reject the preflight, and the call fails before it leaves. Most
third-party REST APIs don't.

The `permissions` block opts a plugin into a tightly-scoped backend
proxy that bypasses CORS for explicitly allowlisted origins:

```json
"permissions": {
  "connect": ["https://ballchasing.com"]
}
```

| Field     | Type     | Notes                                                                                |
| --------- | -------- | ------------------------------------------------------------------------------------ |
| `connect` | string[] | List of bare https origins the plugin is allowed to reach via the backend proxy.     |

Each `connect` entry must be a bare https origin: scheme + host
(optional `:port`), with no query or fragment, no userinfo, and at
most a `/` path. Invalid entries are dropped at manifest load time
and logged to the backend console.

To call an allowlisted origin from your plugin's JS, route the
request through the backend proxy:

```js
const target = "https://ballchasing.com/api/v2/upload?" + params.toString();
const proxied = "/api/plugin-fetch/my-plugin?url=" + encodeURIComponent(target);
const r = await fetch(proxied, {
  method: "POST",
  headers: { Authorization: apiKey },
  body: formData,
});
```

The proxy:

- Validates that the target URL is https and its origin matches one
  of the plugin's `permissions.connect` entries (403 otherwise).
- Builds a fresh upstream request with only the request method, URL,
  and body forwarded. Of the inbound headers, only `Authorization`,
  `Content-Type`, and `Accept` are copied through — everything else
  (custom headers, `Cookie`, `User-Agent`, `Host`, …) is dropped.
- Does NOT follow redirects — a 3xx from the upstream is returned
  to the plugin verbatim, preventing SSRF via 302 into private
  networks.
- Caps both the inbound request body and the outbound response body
  at 16 MiB.
- Returns the upstream's status code and body to the plugin's
  `fetch` promise. Of the upstream response headers, only
  `Content-Type` and `Retry-After` are exposed; other headers
  (e.g. `Set-Cookie`, `ETag`, rate-limit headers) are not visible
  to the plugin.

---

## The `RLT` global

`RLT` is a frozen singleton constructed once per page load. Members:

| Member                                | Description                                              |
| ------------------------------------- | -------------------------------------------------------- |
| `RLT.plugin.register(spec)`           | Register your plugin. Returns a handle.                  |
| `RLT.plugin.list()`                   | Snapshot of registered plugins on this page.             |
| `RLT.plugin.get(name)`                | Look up a registered plugin handle by name.              |
| `RLT.pluginName`                      | The `data-plugin` attribute (or URL-derived name).       |
| `RLT.pluginManifest()`                | Returns the parsed manifest (or `null` until loaded).    |
| `RLT.onManifest(fn)`                  | Subscribe to manifest-loaded; fires once.                |
| `RLT.version`                         | SDK protocol version (currently `1`).                    |
| `RLT.me` / `RLT.identity`             | The player's identity store. See [identity](#identity-rltme). |
| `RLT.match`                           | Live, enriched match state.                              |
| `RLT.encounters`                      | Cross-plugin player-encounter ledger.                    |
| `RLT.store`                           | Per-plugin K/V storage (namespaced by your plugin name). |
| `RLT.events`                          | Typed event subscription helpers.                        |
| `RLT.events.catalog`                  | The full 51-entry event catalog (read-only).             |
| `RLT.events.byCategory`               | `{tick, scoring, play, stat, lifecycle, replay, roster}` index. |
| `RLT.stats`                           | Statfeed `eventName` registry.                           |
| `RLT.ui`                              | Toasts, time formatting, platform icons.                 |
| `RLT.util`                            | Misc helpers (`rafBatcher`).                             |
| `RLT.widget`                          | Tauri widget control (resize, anchor, opacity).          |
| `RLT.focus`                           | Game-foreground change notifications.                    |
| `RLT.status()` / `RLT.onStatus(fn)`   | Raw bus connection state.                                |
| `RLT.statusStable()` / `RLT.onStatusStable(fn)` | Debounced version (smooths reconnect blips).   |
| `RLT.on(ev, fn)` / `RLT.off(ev, fn)` | Low-level bus subscribe / unsubscribe.                   |
| `RLT.view`                            | `'overlay'` / `'dashboard'` / `'settings'` — see [Render contexts](#render-contexts). |
| `RLT.isOverlay` / `RLT.isDashboard` / `RLT.isSettingsView` | Boolean shorthands for `RLT.view`.    |
| `RLT.settings.close()`                | Closes the settings panel (settings view only).          |

### Plugin lifecycle: `RLT.plugin.register`

```js
const handle = RLT.plugin.register({
  // Optional: gate handlers to specific phases. Default = always fire.
  // 'idle' is a back-compat alias for 'none'.
  whilePhase: ['live', 'replay', 'paused', 'countdown'],

  // Event handlers. Object keys are event names; values are functions.
  // Both raw and synthetic events work here. The handler receives the
  // event payload as its only argument.
  events: {
    _GoalScored(goal) { /* … */ },
    _Save(save) { /* … */ },
  },

  // Convenience subscriptions for the most common signals:
  onMatch(m)      { /* m is RLT.match.current; fires on identity change */ },
  onTick(m)       { /* fires every UpdateState (~30 Hz) */ },
  onRoster(m)     { /* fires when the player list changes */ },
  onIdentity(id)  { /* fires when the user's identity changes */ },
  onEncounters(map) { /* fires when the encounter ledger changes */ },
  onState(next, prev) { /* fires on every phase change */ },
  onMatchActive(active) { /* fires when matchActive flips */ },
  onFocusChange(active) { /* fires when game-window focus changes */ },

  // Lifecycle hooks (see "Lifecycle hooks" section below for details):
  init(handle)    { /* sync, runs at register time */ },
  ready(handle)   { /* runs once identity + encounters are loaded */ },
  dispose()       { /* runs when handle.dispose() is called */ },

  // If true, this plugin can write to its store from the dashboard
  // view too. Defaults to false (overlay/settings views can write;
  // dashboard view is read-only).
  allowWrites: false,
});

// The handle:
handle.name      // resolved plugin name
handle.version   // resolved version
handle.title
handle.author
handle.manifest  // parsed manifest object (or null until loaded)
handle.store     // per-plugin K/V store
handle.events    // array of subscribed event names
handle.disposed  // boolean
handle.dispose() // unsubscribe everything + invoke spec.dispose
```

`onState`, `onMatchActive`, and `onFocusChange` **bypass** `whilePhase`
gating — they're meta-signals you typically need regardless of phase.
Everything else respects `whilePhase` if set.

#### Lifecycle hooks: `init` vs `ready` vs `dispose`

Three places to put setup / teardown code. Pick the right one and the
SDK takes care of timing for you.

| Hook        | When it fires                                                                 | What's guaranteed to be available                                                                   | Use it for                                                                                          |
| ----------- | ----------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------- |
| **`init`**  | Synchronously, at `register()` time — before `register()` returns.            | The SDK itself (`RLT.bus`, `RLT.store`, `RLT.widget`, etc.). DOM is loaded. **Identity, encounters, and the manifest may NOT be hydrated yet.** | DOM wiring, `widget.autoSize`, `store.onChange` subscriptions, `events.on(...)` calls — anything that doesn't depend on knowing who the user is or which players have been seen before. |
| **`ready`** | Asynchronously, once both `RLT.me.isReady()` and `RLT.encounters.isReady()` return true. Always fires after `init`, on a microtask. | Everything `init` had + `RLT.me.id` is populated, `RLT.encounters.all()` is loaded. The plugin handle is fully constructed (so `handle.dispose()` is safe to call). | Anything that needs to know who the user is — initial roster scan, encounter-aware UI bootstrap, "did I beat this player before?" lookups. |
| **`dispose`** | When something calls `handle.dispose()` — typically a hot-reload, the page navigating away, or the plugin removing itself. | All the same context as the rest of your handlers, but every `events.on` / `match.onChange` / `store.onChange` subscription you registered through the SDK has *already* been torn down for you. | Releasing resources the SDK doesn't own: `setInterval` timers, raw `addEventListener` you wired manually, audio nodes, WebSocket connections. |

A few rules that shake out of the table:

- **You don't need `await` inside `register()`.** `register()` returns
  the handle synchronously; pass async functions as `init` / `ready`
  and the SDK awaits them in the right order.
- **Don't put async waits in `init`.** It's the synchronous-setup
  hook. If you need to fetch something on boot, kick the fetch off in
  `init` and resolve it in `ready` (or just put the whole thing in
  `ready` if it depends on identity anyway).
- **You usually don't need `dispose`.** The SDK auto-unsubscribes
  every subscription you registered through the spec
  (`events`, `onMatch`, `onTick`, `onRoster`, `onIdentity`,
  `onEncounters`, `onState`, `onMatchActive`, `onFocusChange`). Only
  reach for `dispose` when you set up something *outside* the SDK
  (timers, manual DOM listeners, audio).
- **Errors are caught and logged.** A throwing `init`, `ready`, or
  `dispose` won't crash the SDK; it'll log `[RLT] plugin "<name>"
  init/ready/dispose threw:` and continue. Plugins are isolated from
  each other.

### Identity: `RLT.me`

The "who am I" store. Backed by `data/identity.json` on the server.

```js
RLT.me.id            // e.g. "Steam|76561198000000000|0", or "" if not set
RLT.me.isReady()     // true once boot hydration completes
RLT.me.onChange(fn)  // subscribe to id changes
await RLT.me.set("Steam|76561198000000000|0")
await RLT.me.clear()
```

`RLT.identity` is an alias for `RLT.me`.

The `EnrichedPlayer` objects you receive in event payloads have an
`isMe: true` field stamped on them when their `id` matches `RLT.me.id`.

### Match state: `RLT.match`

The enriched, ready-to-render view of the current match.

```js
RLT.match.current      // null when no match exists
RLT.match.onChange(fn) // fires on roster/identity/encounter changes
RLT.match.onTick(fn)   // fires every UpdateState packet
RLT.match.onRoster(fn) // fires when the player list changes
RLT.match.subscribe()  // ensure UpdateState is subscribed even without a callback
```

`RLT.match.current` shape:

```js
{
  guid,            // match GUID, or "local" for menu-only state
  players: [EnrichedPlayer, ...],  // see EnrichedPlayer below
  blue:    [EnrichedPlayer, ...],  // players with team === 0
  orange:  [EnrichedPlayer, ...],  // players with team === 1
  me,              // the player matching RLT.me.id, or null
  game,            // raw RL Game object (PascalCase fields preserved)
  arena,           // human-readable arena name (Arena field, _P stripped)
  clockSeconds,    // current match clock in seconds, or null
  overtime,        // boolean
  replay,          // true during goal replay or history replay
  hasWinner,       // boolean
  winner,          // winner string from RL, or ""
  scoreBlue,       // integer
  scoreOrange,     // integer
  teams: [         // normalized team metadata
    { teamNum, name, score, colorPrimary, colorSecondary, raw }
  ],
  blueTeam,        // teams.find(t => t.teamNum === 0) or null
  orangeTeam,      // teams.find(t => t.teamNum === 1) or null
  replayInfo,      // { frame, elapsed } during replay, else null
  ball: {          // null when game.Ball is absent
    speed,         // ball speed (km/h) or null
    teamNum,       // last-touched team or null
    lastTouchTeam, // null when no team has touched yet (RL sentinel 255)
    raw,           // raw RL Ball object
  },
  target,          // spectator camera target if game.bHasTarget, else null
  raw,             // the full raw UpdateState payload
}
```

### Phase machine: `RLT.match.state`

```js
RLT.match.state.phase         // one of: none | lobby | countdown | live |
                              //         paused | replay | ended | podium
RLT.match.state.previous      // the phase before this one
RLT.match.state.matchActive   // true while a match is in flight
RLT.match.state.guid          // current match GUID
RLT.match.state.since         // ISO 8601 timestamp of phase start
RLT.match.state.onChange(fn)  // (newPhase, prevPhase) => void
RLT.match.state.onMatchActive(fn) // (active: boolean) => void

RLT.match.onState(fn)         // alias for state.onChange
```

### Encounter ledger: `RLT.encounters`

A persistent map of player IDs you've seen across past matches.
Auto-populated as matches play; shared across all plugins.

```js
RLT.encounters.get(id)    // { names, count, first_seen, last_seen, matches } or null
RLT.encounters.all()      // shallow copy of the full map
RLT.encounters.isBotId(id) // true for "Bot|…" IDs
RLT.encounters.isReady()  // true once the ledger has loaded from disk
RLT.encounters.onChange(fn)
```

`EnrichedPlayer` objects you get from events also carry a stamped
`encounter` field (when known) so you don't always need to look this up
manually.

### Per-plugin storage: `RLT.store`

K/V storage namespaced to your plugin. Backed by JSON on disk.

```js
await RLT.store.get(key)         // returns the value or null
await RLT.store.getAll()         // returns the full namespace as an object
await RLT.store.set(key, val)    // returns true on success
await RLT.store.delete(key)      // returns true on success

// Subscribe to writes that hit this namespace from any context. Fires
// for writes made by the overlay, the dashboard preview, the settings
// panel, or a direct HTTP POST to /api/data/<your-plugin>/<key>. The
// handler receives {key, op}, where op is 'set' or 'delete'.
const off = RLT.store.onChange((change) => {
  console.log(change.key, 'was', change.op);
});

// Filter to a single key:
RLT.store.onChange('config', (change) => { /* only fires for 'config' */ });

off();  // unsubscribe
```

Writes are only allowed from overlay or settings views by default. The
dashboard view is read-only unless your plugin passes `allowWrites:
true` to `register()`. Read-only writes log a warning and resolve to
`false`.

`onChange` is the canonical "react to settings changes" hook. The
backend broadcasts a `_StoreChanged` event on every successful write
or delete; the SDK filters by namespace before invoking your handler,
so you never see other plugins' writes.

### UI helpers: `RLT.ui`

| Method                           | Notes                                                   |
| -------------------------------- | ------------------------------------------------------- |
| `RLT.ui.platformIcon(platform)`  | SVG markup for `'steam'`, `'epic'`, `'ps*'`, `'xbox*'`, `'switch'`. |
| `RLT.ui.playerIcon(player)`      | Bot icon for bots, otherwise `platformIcon(player.platform)`. |
| `RLT.ui.esc(s)`                  | HTML-escape a string.                                    |
| `RLT.ui.escAttr(s)`              | Attribute-safe escape (keeps `<` and `>`).               |
| `RLT.ui.formatTime(secs, overtime)` | `m:ss`, prefixed with `+` in OT.                      |
| `RLT.ui.timeAgo(iso)`            | Human-readable relative time (`now`, `5m`, `2h`, `3d`).  |
| `RLT.ui.cssEsc(s)`               | `CSS.escape` polyfill.                                   |
| `RLT.ui.toast(msg, ms)`          | Show a transient toast in the overlay.                   |
| `RLT.ui.matchBadgeLabel()`       | One-word status: `offline` / `idle` / `lobby` / phase.   |
| `RLT.ui.bindStatusPill(elementId, onChange)` | Wire the debounced status to a DOM element. |

### Stats registry: `RLT.stats`

Server-injected registry of all known statfeed `eventName` values.
Use it to compare against `_StatfeedEvent.eventName` without
hard-coding strings.

```js
RLT.stats.DEMOLISH       // → "Demolish"
RLT.stats.SAVE           // → "Save"
RLT.stats.EPIC_SAVE      // → "EpicSave"
RLT.stats.GOAL           // → "Goal"
RLT.stats.HAT_TRICK      // → "HatTrick"
// … 28 entries total, mirroring backend/internal/types/statfeed.go

RLT.stats.known          // Set of all values for membership tests
```

### Widget control: `RLT.widget`

Tauri-hosted overlay only. Outside Tauri (dashboard tab, settings
iframe) every method is a no-op.

```js
RLT.widget.isHosted()                      // true inside Tauri
await RLT.widget.size(width, height)
await RLT.widget.anchor("top-left")        // or top-right / bottom-*
await RLT.widget.margin(x, y)
await RLT.widget.opacity(0.0..1.0)
await RLT.widget.visible(true|false)
RLT.widget.autoSize(true, { target, minWidth, maxWidth, minHeight, maxHeight })
RLT.widget.fitWidth({ target, maxWidth, extra })
```

Two return-shape conventions to know:

- `size`, `anchor`, `margin`, `opacity`, `visible` always return a
  **Promise** that resolves to `true` on success, `false` outside
  Tauri or on IPC failure. `await` them.
- `autoSize` and `fitWidth` return a **synchronous boolean** —
  `true` when the watcher is set up (inside Tauri), `false` outside.
  They kick off the resize loop in the background; there's nothing to
  await. `await` works anyway (you'd just be awaiting a bare boolean),
  so it's safe to call them with or without `await` if you'd rather
  keep call-site styling consistent with the rest of the API.

`autoSize` watches `target` (default `document.body`) with
ResizeObserver and resizes the window on change. `fitWidth` is the
write-only-grow variant — width can only increase, not shrink.

### Connection status: `RLT.status` / `RLT.statusStable`

```js
RLT.status()              // 'connected' | 'connecting' | 'disconnected'
RLT.onStatus(fn)          // raw status feed (every change)
RLT.statusStable()        // same values, but down transitions debounced 3s
RLT.onStatusStable(fn)    // smooth status feed (good for status pills)
RLT._reconnect()          // force-reconnect the SSE stream
```

### Settings panel: `RLT.settings`

Settings views run inside an iframe in the dashboard. Detect via
`RLT.isSettingsView`. To close yourself:

```js
if (RLT.isSettingsView) {
  RLT.settings.close();
}
```

### Misc utilities: `RLT.util`

```js
const draw = RLT.util.rafBatcher(() => repaintExpensiveUI());
draw();    // schedule once
draw();    // coalesces with the in-flight rAF
```

### Low-level event bus: `RLT.on` / `RLT.off`

Use `events: {…}` in `register()` for normal subscriptions. The bare
bus is for advanced cases (wildcard listeners, dynamic subscription
management):

```js
const off = RLT.on('_GoalScored', (payload) => { /* … */ });
off();                       // unsubscribe
RLT.on('*', (event, payload) => { /* fires for every event */ });
RLT.on('_status', (s) => { /* connection status changes */ });
```

`RLT.on` also auto-subscribes the backend to the named event — without
that, the SSE stream would filter it out.

---

## Event reference

There are 51 events in the catalog. They fall into two camps:

- **Raw events** — produced by Rocket League's stats API and forwarded
  by the toolkit. Names are PascalCase (e.g. `GoalScored`,
  `BallHit`). Their payload's `raw` field carries the original
  envelope; the typed wrapper exposes `matchGuid` + `raw`.
- **Synthetic events** (underscore-prefixed) — produced by the
  toolkit's correlator after enriching the raw event with resolved
  player references, ball-speed correlation, and statistics. **Almost
  always prefer the synthetic version** — it's strictly more useful.

Subscribe via `events: {…}` in `register()`, or via `RLT.events.onX(fn)`
helpers, or via `RLT.on(name, fn)`.

The full machine-readable catalog lives at `RLT.events.catalog`.

### The `EnrichedPlayer` shape

Every "player" reference in synthetic event payloads has this shape:

```js
{
  id,        // "Platform|UserId|SubId", e.g. "Steam|76561198000000000|0", or "Bot|<name>"
  name,      // display name
  team,      // 0 (blue) or 1 (orange)
  platform,  // "Steam" | "Epic" | "PS4" | "PS5" | "Xbox" | "Switch" | …
  isBot,     // boolean
  isMe,      // true if id === RLT.me.id (stamped by the SDK)
  encounter, // RLT.encounters record for this player, if known (stamped by the SDK)
}
```

`encounter` is `undefined` for first-time-seen players.

### Lifecycle events

Raw RL events. The synthetic counterpart for these is `_MatchState`
(see below) — you almost never need the raw ones unless you care about
RL's specific edge timing.

| Event               | When                                                    |
| ------------------- | ------------------------------------------------------- |
| `MatchCreated`      | All teams replicated; lobby ready.                      |
| `MatchInitialized`  | First countdown started.                                |
| `CountdownBegin`    | Round countdown began.                                  |
| `RoundStarted`      | Active gameplay started (countdown ended).              |
| `MatchPaused`       | Match paused by an admin.                               |
| `MatchUnpaused`     | Match resumed.                                          |
| `GoalReplayStart`   | Goal replay began.                                      |
| `GoalReplayWillEnd` | Ball exploded during replay (skipped replays don't fire this). |
| `GoalReplayEnd`     | Goal replay ended.                                      |
| `MatchEnded`        | Match decided.                                          |
| `PodiumStart`       | Game entered podium state.                              |
| `MatchDestroyed`    | Player left the match.                                  |
| `ReplayCreated`     | Match-history replay loaded (NOT goal replays).         |

Payload (typed wrapper):

```js
{ matchGuid: string|null, raw: <RL native object>|null }
```

### Match-state synthetics

#### `_MatchState`

The authoritative current phase. Fires on every transition.

```js
{
  matchActive: boolean,              // true while a match is in flight
  phase: 'none'|'lobby'|'countdown'|'live'|'paused'|'replay'|'ended'|'podium',
  previousPhase: <phase>,
  matchGuid: string,
  since: '2026-05-07T12:34:56.000+00:00', // ISO 8601
  phaseDurationSeconds: number,       // how long we've been in this phase
  trigger: 'MatchCreated'|'UpdateState'|'CountdownBegin'|'RoundStarted'|
           'MatchPaused'|'MatchUnpaused'|'MatchEnded'|'PodiumStart'|
           'MatchDestroyed'|'bReplayEdge'|'connectionLost'|
           'watchdogTimeout'|'initial'
}
```

This is the event that drives `RLT.match.state.phase` and the
`whilePhase` / `show_during_phase` gates.

#### `_OvertimeStarted`

Rising edge of `Game.bOvertime`. Fires exactly once per match when the
score is tied at the end of regulation and overtime begins.

```js
{
  matchGuid: string,
  scoreBlue: integer,
  scoreOrange: integer,
  tiedAt: integer,
  matchDurationSecondsBeforeOT: number | null,
}
```

### Roster events

#### `_RosterChanged`

Fires when the set of (player ID, team) pairs changes. Name-only and
platform-only changes do NOT fire it.

```js
{
  matchGuid: string,        // "" outside a match
  players: [
    { id, name, team, platform, isBot },
    ...
  ] | null,                 // null on the had-roster → empty transition
}
```

#### `_PlayerJoined` / `_PlayerLeft`

Roster-diff edges. Fires when a single player appears or disappears
between consecutive UpdateState ticks.

```js
{ matchGuid, player: EnrichedPlayer, phase: <current phase> }
```

### Goal & scoring events

#### `_GoalScored`

The headline event. Carries the resolved scorer, assister, last
ball-touch player, ball speed at goal, the goal location, and a
`modifiers` object with detected goal types.

```js
{
  matchGuid: string,
  scorer: EnrichedPlayer,
  assister: EnrichedPlayer | null,
  ballLastTouch: { player: EnrichedPlayer|null, speed: number|null } | null,
  goalSpeed: number | null,           // ball speed (km/h) at the moment of the goal
  goalTime: number | null,            // seconds since round start
  impactLocation: { X, Y, Z } | null, // where the ball crossed the goal line
  scoringTeam: 0 | 1 | null,
  concedingTeam: 0 | 1 | null,
  isOwnGoal: boolean,
  modifiers: {
    isAerialGoal:    boolean,
    isBackwardsGoal: boolean,
    isBicycleGoal:   boolean,
    isLongGoal:      boolean,
    isTurtleGoal:    boolean,
    isOvertimeGoal:  boolean,
    isPoolShot:      boolean,
    isHoopsSwishGoal: boolean,
    isHatTrickGoal:  boolean,
    isFlipResetGoal: boolean,
  }
}
```

All `modifiers` fields are always present (defaulted to `false` when
not detected).

#### `_OwnGoal`

Higher-confidence own-goal detection (score-delta verified rather than
inferred from `_GoalScored.isOwnGoal`).

```js
{
  matchGuid: string,
  deflector: EnrichedPlayer,            // the player whose touch deflected in
  scoringTeam: 0 | 1,
  concedingTeam: 0 | 1,
  scoreAfter: { blue: integer, orange: integer },
  correlatedGoalScorer: EnrichedPlayer | null,
}
```

#### `_FirstBlood`

First `_GoalScored` of the match. Fires exactly once per match.

```js
{
  matchGuid: string,
  scorer: EnrichedPlayer,
  scoringTeam: 0 | 1,
  concedingTeam: 0 | 1,
  secondsIntoMatch: number | null,
}
```

#### `_GoalReplayStarted`

Same payload shape as `_GoalScored`, fired on the `bReplay` rising
edge. Useful when you want a full goal payload at *replay* time
(e.g. to drive a replay overlay), not at scoring time.

#### `_FastestShotOfMatch`

Fires when the per-match maximum ball speed is exceeded.

```js
{
  matchGuid: string,
  speed: number,
  source: 'GoalScored' | 'BallHit',
  player: EnrichedPlayer | null,
}
```

#### `_TeamScoreChanged`

Fires alongside `_GoalScored` and `_OwnGoal`.

```js
{
  matchGuid: string,
  teamNum: 0 | 1,
  teamName: string,
  before: integer,
  after: integer,
  delta: integer,
}
```

### Touch & ball events

#### `_BallHit`

Every ball touch with resolved players + pre/post speed.

```js
{
  matchGuid: string,
  players: [EnrichedPlayer, ...],     // primary toucher first
  preHitSpeed: number | null,
  postHitSpeed: number | null,
  location: { X, Y, Z } | null,
}
```

#### `_CrossbarHit`

```js
{
  matchGuid: string,
  ballSpeed: number | null,
  impactForce: number | null,
  ballLocation: { X, Y, Z } | null,
  ballLastTouch: { player: EnrichedPlayer|null, speed: number|null } | null,
}
```

#### `_FirstTouch`

First `_BallHit` after each `RoundStarted`. Re-arms every round.

```js
{
  matchGuid: string,
  players: [EnrichedPlayer, ...],
  postHitSpeed: number | null,
  location: { X, Y, Z } | null,
  timeFromCountdownEndSeconds: number | null,
}
```

#### `_BallPossessionChanged`

Fires when `Game.Ball.TeamNum` changes. RL's "untouched" sentinel
(`255`) normalizes to `null`.

```js
{
  matchGuid: string,
  before: 0 | 1 | null,
  after: 0 | 1 | null,
  triggeredBy: {
    player: EnrichedPlayer | null,
    preHitSpeed: number | null,
    postHitSpeed: number | null,
  } | null,
}
```

### Statfeed events

`StatfeedEvent` is RL's native catch-all for "a player earned a stat."
Each statfeed type also has a dedicated synthetic event with extra
correlation.

#### `_StatfeedEvent`

```js
{
  matchGuid: string,
  eventName: string,                // e.g. "Demolish", "Save", "EpicSave"
                                    // — see RLT.stats for the registry
  type: string,
  mainTarget: EnrichedPlayer | null,
  secondaryTarget: EnrichedPlayer | null,
}
```

#### `_UnknownStatfeed`

Fires when a statfeed `eventName` arrives that's NOT in the verified
registry (`RLT.stats`). Useful for catching new stat types RL adds.

```js
{ matchGuid, eventName, mainTarget, secondaryTarget }
```

#### Promoted statfeeds (one event per statfeed type)

Each promoted event ships with the same `matchGuid` as `_StatfeedEvent`
plus extra correlated context. All fields beyond the base shape are
listed below; everything except `_PlayerDemolished` carries
`mainTarget` (and, when applicable, `secondaryTarget`).

| Event               | Extra fields                                                                |
| ------------------- | --------------------------------------------------------------------------- |
| `_PlayerDemolished` | `attacker`, `victim`, `isSelfDemo`, `isTeamDemo`, `attackerSpeed?`, `attackerWasSupersonic?` (no `mainTarget`/`secondaryTarget`) |
| `_FlipReset`        | `flipResetsThisMatch` (counter)                                             |
| `_HatTrick`         | `goalsThisMatch` (counter; the event is suppressed entirely until the player has 3 non-own-goal goals) |
| `_Save`             | `correlatedShot: EnrichedPlayer \| null` (within the last 15 statfeed events) |
| `_EpicSave`         | `correlatedShot: EnrichedPlayer \| null`                                    |
| `_Shot`             | `correlatedTouch: { player, preHitSpeed, postHitSpeed } \| null` (last 3 ball-hits) |
| `_Assist`           | `correlatedGoal: { scorer, scoringTeam, concedingTeam } \| null`            |
| `_Center`           | `correlatedTouch: { player, preHitSpeed, postHitSpeed } \| null`            |
| `_Clear`            | `correlatedTouch: { player, preHitSpeed, postHitSpeed } \| null`            |
| `_BicycleHit`       | `correlatedTouch: { player, preHitSpeed, postHitSpeed } \| null`            |

### Demo correlation

#### `_DemoChain`

Standalone synthetic (not a statfeed promotion). Fires when the same
attacker records ≥2 demos within a rolling 5-second window — useful
for "double demo" / "triple demo" recognition independent of the
per-demo `_PlayerDemolished` stream.

```js
{
  matchGuid: string,
  attacker: EnrichedPlayer,
  victims: [EnrichedPlayer, ...],
  count: integer,
  windowSeconds: number,
}
```

### Tick-diff events

These watch UpdateState frame-to-frame and emit on changes.

#### `_PlayerScoreChanged`

Per-player stat diff. Only fields that moved appear in `delta`.

```js
{
  matchGuid: string,
  player: EnrichedPlayer,
  delta: {
    score?:   integer,
    goals?:   integer,
    assists?: integer,
    saves?:   integer,
    shots?:   integer,
    touches?: integer,
    demos?:   integer,
  }
}
```

#### `_BoostPickup`

Fires when a player's boost rises (rising-edge only — respawns and
boost-resets are suppressed).

```js
{ matchGuid, player: EnrichedPlayer, boostBefore, boostAfter, delta }
```

#### `_MatchEnded`

The synthetic counterpart to raw `MatchEnded` — adds the resolved
winner team name and final scores.

```js
{
  matchGuid: string,
  winnerTeamNum: 0 | 1 | null,
  winnerName: string,
  scoreBlue: integer | null,
  scoreOrange: integer | null,
}
```

#### `_SavedReplay`

Fires when Rocket League finishes writing a `.replay` file to disk.
The Stats API doesn't expose this signal, so the backend watches the
Demos directory directly. Useful for plugins that want to copy,
upload, or analyze replays after a match.

```js
{
  matchGuid: string,    // file basename minus ".replay", uppercase as RL writes it
  fileName: string,     // e.g. "04CD01A14F982F367484E281FA8BE810.replay"
  path: string,         // absolute path on disk
  sizeBytes: integer,   // size at the moment the file was observed stable
  savedAt: string,      // ISO-8601 UTC, e.g. "2026-05-09T14:23:45.123Z"
}
```

`matchGuid` correlates with `_MatchEnded.matchGuid`, but **case-fold both
sides** before comparing — RL stores the GUID uppercase in the filename
and (depending on the source) lowercase or mixed-case in the Stats API
payload.

The watcher waits until the file size has been stable for ~1.5s before
firing, so plugins reading the file from `path` will see a complete
replay. Files already present when the backend starts are NOT
re-emitted — the event means "saved during this session."

The watched directory auto-detects on Linux (Steam Proton prefix) and
Windows (`%USERPROFILE%\Documents\My Games\Rocket League\TAGame\Demos`).
macOS and non-standard installs require setting a custom path via
`PUT /api/replay-watcher` with body `{"dir":"/abs/path"}`. A `null`
or absent `dir` clears the override and falls back to auto-detect.

`GET /api/replay-watcher` returns the watcher's current state:

```js
{
  configured: string,    // user override; "" when unset
  autoDetected: string,  // first existing per-OS candidate; "" when none matched
  effective: string,     // the dir the watcher is actually monitoring
  status: "active" | "inactive",
  statusReason: string,  // human-readable when inactive (e.g. "directory not found")
}
```

A companion event `_ReplayWatcherChanged` fires after every successful
PUT (even when the dir didn't actually change); payload is the same
shape as `GET /api/replay-watcher`.

### Tick stream

#### `UpdateState`

The raw RL match snapshot at PacketSendRate (configured in
`DefaultStatsAPI.ini`, typically 10–30 Hz). Heavy. Most plugins
should subscribe via `RLT.match.onTick(fn)` instead — that gets you
the *enriched* match object on every tick.

```js
{
  matchGuid: string|null,
  raw: { Game, Players, … }   // raw RL UpdateState
}
```

#### `ClockUpdatedSeconds`

Fires when the match clock changes by ≥1 second.

```js
{
  matchGuid: string|null,
  seconds: integer|null,
  overtime: boolean,
  raw: { … }
}
```

### Identity & boot signals

#### `_IdentityChanged`

Fires when the user's identity is set or cleared on the backend.

```js
{ primaryId: string, name: string }   // or null when cleared
```

You can also subscribe via `RLT.me.onChange(fn)`.

#### `_BootId`

Process-lifetime UUID. The first SSE frame on every connect — useful
for detecting backend restarts (compare against the previous boot ID
you saw).

The boot ID lives at the **top level** of the SSE envelope
(`{"Event":"_BootId","bootId":"..."}`), not inside a `Data` field, so
SDK event handlers receive an empty payload (`{}`). To read the
current boot ID, fetch `GET /api/boot-id`:

```js
const { bootId } = await fetch('/api/boot-id').then((r) => r.json());
```

`_BootId` is not bridged into `RLT.events`, so subscribe via the raw
bus (`RLT.on('_BootId', ...)`) if you want the connect-time signal.

#### `_status`

Bus connection status. Not really an "event" — it's the underlying
signal `RLT.status()` reads.

```js
'connected' | 'connecting' | 'disconnected'
```

Subscribe via `RLT.onStatus(fn)`.

---

## Phase gating

Three mechanisms, with very different cost and lifetime semantics.
Pick by what you actually need:

| Mechanism | Where | Visual effect | Plugin still runs? | Events still received? |
|-----------|-------|---------------|--------------------|------------------------|
| `show_during_phase` (manifest) | Author | `display: none` on `<body>` outside listed phases | Yes | Yes |
| `show_during_phase` + `unmount_outside_phase: true` (manifest) | Author | Iframe is removed from the DOM outside listed phases | No (iframe is gone) | No (bus subscription dies, shared SSE filter shrinks) |
| `whilePhase` on `RLT.plugin.register` (code) | Author | None — visibility unchanged | Yes | Yes (handler is a no-op outside phases) |
| Per-card enable toggle (dashboard / `/api/overlay/overrides`) | User | Iframe unmounted | No | No |

### `show_during_phase` (visual gate, default)

```json
"show_during_phase": ["countdown", "live", "paused", "replay"]
```

The aggregator forwards the array to the SDK as a URL flag; the SDK
toggles `body { display: 'flex' | 'none' }` on phase change. The
plugin's iframe stays mounted, the bus subscription stays open, and
your event handlers keep firing — you just can't see anything
outside the listed phases.

### `unmount_outside_phase` (hard kill, opt-in)

```json
"show_during_phase": ["replay"],
"unmount_outside_phase": true
```

Upgrades the gate: outside the listed phases, the aggregator
**unmounts the iframe entirely**. The plugin's SSE subscription
dies, the shared bus filter is recomputed without its events, and
its in-memory state is released. Re-mounted (cold-started) on the
next phase entry.

Use this when:
- The plugin holds expensive state (audio nodes, large buffers,
  long-lived timers) that you'd rather release between phases.
- You want a phase-bound plugin to be genuinely off — no event
  delivery, no JS running — outside its window.

Skip this when:
- You need cross-phase state (e.g. a session-long counter) — the
  cold-start on each re-mount loses your in-memory state.
- The plugin's `show_during_phase` covers most of a match anyway —
  the mount/unmount churn isn't worth the brief idle savings.

No effect if `show_during_phase` is empty or absent.

### `whilePhase` (handler-level)

```js
RLT.plugin.register({
  whilePhase: ['live', 'replay'],
  events: { _GoalScored: handler }, // only fires during live/replay
});
```

Wraps each handler in a phase check. Handlers outside the allowed
phases are silently skipped. The SDK still receives, parses, and
dispatches the event — only the user callback is gated. `onState`,
`onMatchActive`, and `onFocusChange` ignore `whilePhase` because
you usually want phase-change notifications regardless of the
current phase.

### Phase values

`none` (no match), `lobby`, `countdown`, `live`, `paused`, `replay`,
`ended`, `podium`. The string `idle` is a back-compat alias for
`none` accepted by `whilePhase`.

---

## Settings panel

To add a settings view, declare it in your manifest:

```json
"settings": { "file": "settings.html" }
```

…and create the file. The SDK script tag in it must declare
`data-view="settings"`:

```html
<script src="/sdk.js" data-plugin="my-plugin" data-view="settings"></script>
```

The dashboard renders a "Settings" button on your plugin's card. The
button opens `settings.html` in a modal iframe.

Inside the settings view:

```js
// Read/write your store (writes are always allowed from settings views).
const cfg = await RLT.store.getAll();
await RLT.store.set('volume', 0.8);

// Close yourself when the user is done.
document.querySelector('#close').onclick = () => RLT.settings.close();
```

The settings view shares the SDK with the overlay and dashboard, so
the moment you call `RLT.store.set(...)`, those views' `RLT.store.onChange`
handlers fire and they re-read fresh config. See [Per-plugin
storage](#per-plugin-storage-rltstore) for the `onChange` API.

---

## Player MMR (`/api/mmr`)

Plugins can fetch live Rocket League MMR for the current player or any
other player by platform + id. Data comes from tracker.gg. The backend
caches successful lookups in memory and on disk for 5 minutes per
`(platform, id)`, so repeated calls inside the window do not hit
tracker.gg again.

### Endpoints

- `GET /api/mmr` — returns MMR for the current `RLT.me`. Requires
  identity to be set on the dashboard.
- `GET /api/mmr/{platform}/{id}` — explicit lookup. `platform` is one
  of `steam`, `psn`, `xbl`, `switch` (case-insensitive). Epic Games is
  not supported; tracker.gg keys those by display name.

### Response (200)

```json
{
  "platform": "steam",
  "playerId": "76561197960287930",
  "playlists": {
    "1v1":    {"mmr": 785,  "tier": "Platinum III", "division": "Division III", "matches": 0},
    "2v2":    {"mmr": 1005, "tier": "Diamond III",  "division": "Division II",  "matches": 0},
    "3v3":    {"mmr": 1067, "tier": "Diamond III",  "division": "Division IV",  "matches": 10},
    "casual": {"mmr": 1405, "tier": "Unranked",     "division": "Division I",   "matches": 0}
  },
  "fetchedAt": "2026-05-09T17:18:00Z",
  "cached": false,
  "age": 0
}
```

Only the three core ranked playlists and Casual are returned. The
extra modes (Hoops, Rumble, Dropshot, Snowday) and any unknown segment
are dropped; plugins that need them should fetch tracker.gg directly
through `/api/plugin-fetch/`.

`cached` and `age` reflect cache state. A fresh upstream fetch returns
`cached: false, age: 0`. A repeat call within the 5-minute window
returns `cached: true` with `age` set to the number of seconds since
`fetchedAt`, so plugins can display a "stale by N seconds" indicator
without doing the math themselves.

### Errors

| Status | When | Body |
|---|---|---|
| 400 | Bad path or unsupported `platform` on the explicit route | `{"error":"bad request"}` |
| 404 | Player has no tracker.gg profile | `{"error":"player not found","platform":"…","playerId":"…"}` |
| 405 | Method not GET | `{"error":"method not allowed"}` |
| 409 | `/api/mmr` called with no identity set | `{"error":"identity not set"}` |
| 501 | Self route, identity uses an unsupported platform (Epic) | `{"error":"platform not supported","platform":"epic"}` |
| 502 | Cloudflare blocked us, or upstream returned an unexpected status | `{"error":"upstream blocked","upstreamStatus":403}` or `{"error":"upstream error"}` |
| 503 + `Retry-After` | Local rate limit or breaker is open | `{"error":"rate limited"}` or `{"error":"upstream temporarily unavailable"}` |

### Backend safety

- Successful lookups are cached for 5 minutes per `(platform, id)`, in
  memory and at `<dataDir>/tracker-mmr-cache.json`. The cache survives
  launcher restarts.
- Outbound calls are rate-limited to 1 request per second (burst of
  3). Cache hits bypass the rate limiter entirely.
- After 3 consecutive Cloudflare blocks (403/429), a circuit breaker
  opens for 5 minutes. Calls during that window return 503 immediately
  with a `Retry-After` header. Cache hits bypass the breaker too.
- The backend does not retry. Plugins decide their own retry policy.
- There is no force-refresh or cache-bypass parameter.

### Example (from a plugin)

```js
const res = await fetch('/api/mmr');
if (res.ok) {
  const { playlists, cached, age } = await res.json();
  console.log('2v2 MMR:', playlists['2v2']?.mmr, cached ? `(cached ${age}s ago)` : '(live)');
}
```

---

## Best practices

- **Prefer synthetic events.** They have resolved player refs, ball
  speeds, modifier flags, and correlation that the raw events don't.
  Subscribe to `_GoalScored`, not `GoalScored`.
- **Use `match.onTick` sparingly.** It fires at PacketSendRate
  (10–30 Hz). Coalesce expensive renders with `RLT.util.rafBatcher`.
- **Treat null fields as expected.** Many enrichments depend on
  correlation that can fail (e.g. `_Save.correlatedShot` is null when
  no recent opposing shot was found). Don't crash on `null`.
- **Don't hard-code statfeed names.** Use `RLT.stats.DEMOLISH` etc;
  the registry is server-injected, so a backend update keeps your
  plugin in sync automatically.
- **Gate visibility at the manifest level.** `show_during_phase` is
  declarative — one line in the manifest instead of a phase
  subscription in your code. Under the hood the SDK still toggles
  `body { display: none }` per phase change, so your event handlers
  keep firing; what you avoid is duplicating the subscription and the
  flash of content before the first phase resolves (the SDK injects a
  pre-paint hide for you).
- **Use `ready()` for initial setup that needs identity or
  encounters.** It fires after both stores have hydrated; before that,
  `RLT.me.id` may be empty even if the user has set their identity.
- **For sound-only plugins, use a tiny non-zero opacity** (e.g.
  `"opacity": 0.01`) plus a transparent body. The validator resets a
  literal `0` to `1.0`, so `0` won't hide the window.
- **Inspect real event payloads with `RLT.on('*', console.log)`.**
  Drop a wildcard listener in any plugin to print every event the
  bus delivers — useful when you're not sure what a synthetic event
  actually contains in practice. Remove it before shipping.
