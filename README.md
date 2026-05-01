# RL Toolkit

A lightweight, plugin-based framework for the Rocket League Stats API.  
Zero dependencies — runs as a single executable on Windows, Linux, or macOS.

---

## Quick Start

### 1. Enable the Rocket League Stats API

Edit the file at:

```
<Rocket League Install Dir>\TAGame\Config\DefaultStatsAPI.ini
```

Set these values:

```ini
[TAGame.MatchStatsExporter_TA]
PacketSendRate=60
Port=49123
```

Restart Rocket League after saving.

### 2. Run RL Toolkit

**Windows:** double-click `rl-toolkit.exe`  
**Linux:** `./rl-toolkit`

The toolkit will:
- Start a local web server on `http://localhost:8080`
- Auto-connect to the RL Stats API on `localhost:49123`
- Load all plugins from the `plugins/` directory

### 3. Open the Dashboard

Visit `http://localhost:8080` in your browser. You'll see loaded plugins and connection status.

---

## Overlay Display

The toolkit serves plugin overlays as web pages. There are three ways to
get them onto your screen:

### Method A: Desktop widget (recommended for players)

The toolkit ships with `rl-widget` — a transparent, frameless, always-on-top
window backed by [Tauri](https://tauri.app). On Linux it uses
`wlr-layer-shell` to sit above other windows with no compositor config; on
Windows / macOS it uses the platform's standard always-on-top + click-through
primitives.

```bash
./overlay-app/src-tauri/target/release/rl-widget --plugin=dejavu
```

One process per plugin, anchored from the manifest's `anchor` /
`offset_x` / `offset_y`. Click-through, so you can play through it.

> **Note:** RL needs to be in **borderless windowed** mode (the default).
> No compositor-level overlay can sit above an exclusively-fullscreen game.

Build instructions and per-OS prereqs are in **[BUILD.md](BUILD.md)**.

### Method B: OBS Browser Source (recommended for streamers)

1. In OBS, add a **Browser Source**
2. Set URL to `http://localhost:8080/overlay`
3. Set width/height to your monitor resolution
4. Enable **"Shutdown source when not visible"**

This composites every plugin's overlay at their configured screen
positions, baked into the broadcast.

### Method C: Direct browser

Each plugin's overlay page is a regular URL:

```
http://localhost:8080/plugins/dejavu/overlay.html              (control page)
http://localhost:8080/plugins/dejavu/overlay.html?overlay=1    (transparent overlay view)
```

Useful for: a second monitor, a tablet, or any browser-based overlay tool
(Rainmeter, eww, Übersicht) pointed at the URL.

---

## Included Plugin: Déjà Vu

Tracks every player you encounter and alerts you when you see a returning
player. Shows:

- Encounter count (highlighted badge for returning players)
- Previous usernames / aliases
- Platform (Steam, Epic, etc.)
- Live match stats (goals, assists, saves, shots, demos)
- Match score and time

**Setting your ID:** Click your own name in the overlay during a match, or
manually enter your `PrimaryId` (e.g. `Steam|12345|0`) in the input at the
bottom.

Data persists across sessions in `data/dejavu.json`.

---

## Writing Plugins

Scaffold a working plugin in one command:

```bash
./rl-toolkit new my-plugin
```

That creates `plugins/my-plugin/{manifest.json,overlay.html}` from a
working template. Refresh the dashboard and your plugin appears.

The full authoring guide — events, overlay vs. dashboard mode, the
desktop widget API, persistence, identity, encounters, debugging — lives
in **[docs/PLUGINS.md](docs/PLUGINS.md)**.

For a quick reference of every event you can subscribe to:

```bash
curl http://localhost:8080/api/events
```

---

## CLI Options

```
  -rl-addr string    RL Stats API address (default "127.0.0.1:49123")
  -port int          HTTP server port (default 8080)
  -plugins string    Plugin directory path (default "plugins")
  -data string       Data directory path (default "data")
```

---

## Building from Source

The project has two binaries — `rl-toolkit` (Go server) and `rl-widget`
(Rust + Tauri overlay). Per-OS prerequisites and full instructions live in
**[BUILD.md](BUILD.md)**. The short version:

```bash
# Toolkit (Go 1.22+, no system deps)
go build -o rl-toolkit .

# Widget (Linux: needs webkit2gtk-4.1 + gtk-layer-shell)
cd overlay-app/src-tauri && cargo build --release
```

The toolkit cross-compiles cleanly with `GOOS=windows`/`darwin`. The
widget needs a real Windows or macOS box for those targets — Tauri's
webview crate doesn't cross-compile.

---

## Plugin Ideas

A few things the Stats API makes possible:

- **Boost tracker** — real-time boost meter overlay with usage history
- **Session stats** — aggregate stats across multiple matches in a session
- **Goal analysis** — shot placement maps from GoalScored impact locations
- **Demo tracker** — who's demolishing who, with sound effects
- **Match timeline** — visual timeline of goals, demos, and stat events
- **Speed tracker** — ball and car speed graphs in real-time
- **Crossbar counter** — the ultimate tilt tracker
