# RL Toolkit

A plugin-based overlay framework for Rocket League. Reads live match data from RL's own Stats API, exposes it to plugins as a clean JavaScript SDK, and renders them as transparent click-through overlays on top of the game.

> **Status: alpha.** Shipping and self-updating, but APIs and storage formats may still change between versions.

![screenshot placeholder — overlay running on top of Rocket League](docs/assets/hero.png)

---

## What it is

RL Toolkit is two binaries:

- **`rl-toolkit`** — a Go backend that talks to Rocket League over TCP, normalises the event stream, and serves a plugin host + dashboard over HTTP.
- **`rl-widget`** — a Rust + Tauri 2 overlay window that renders plugin HTML on top of the game, click-through and undecorated.

Plugins are pure HTML + JS + CSS, dropped into a directory. No backend code, no compilation step. The SDK (`window.RLT`) gives them match state, lifecycle events, per-plugin storage, and a settings panel.

It does **not** inject into the game, hook input, or read game memory — it only consumes RL's own Stats API. EAC has no problem with it.

## Bundled plugins

| Plugin | What it does |
|---|---|
| **Déjà Vu** | Tracks players you've encountered before and shows encounter history. |
| **Session Tracker** | Wins, losses, and stats for the current launcher session. |
| **Demolitions** | Demolitions dealt — this match and all-time. |
| **Ballchasing Upload** | Auto-uploads saved replays to ballchasing.com. |
| **Crossbar Sound** | Plays a sound effect when the ball hits a crossbar. |
| **Hello World** | Reference plugin showing the overlay + dashboard + settings layout. |
| **SynthTracker** | Dev tool — subscribes to every synthetic event and shows live counts. |

## Install

Grab the latest release from the [releases page](https://github.com/s1gh/RLToolkit/releases/latest):

| Platform | Artefact | Notes |
|---|---|---|
| Windows | `RLToolkit_<v>_x64-setup.exe` | NSIS installer, auto-updates. |
| Windows | `RLToolkit_<v>_x64-portable.zip` | No-admin / USB-stick. No auto-update. |
| Linux   | `RLToolkit_<v>_x86_64.AppImage` | Any glibc 2.35+ distro. Auto-updates. |
| Linux   | `RLToolkit_<v>_x86_64-portable.tar.gz` | Tarball. No auto-update. |

In Rocket League, set *Settings → Video → Display Mode* to **Borderless**. Exclusive fullscreen blocks all compositor-level overlays (Tauri, Discord, Steam Overlay, OBS Browser Source) — this is a DirectX limitation, not specific to RL Toolkit.

## Build from source

See [`docs/BUILD.md`](docs/BUILD.md) for the full build, packaging, and release flow on Linux and Windows. The short version:

```bash
# Linux (Arch / Cachy / Manjaro)
sudo pacman -S base-devel rustup go webkit2gtk-4.1 gtk-layer-shell pkg-config nodejs npm
rustup default stable
npm install

go build -o rl-toolkit .                    # backend (~5 sec)
cd overlay/src-tauri && cargo build --release   # widget (~1 min first time)
```

Run them:

```bash
./rl-toolkit &
./overlay/src-tauri/target/release/rl-widget
```

## Writing a plugin

A plugin is a directory under `plugins/` with a `manifest.json` and one or more HTML files:

```
plugins/my-plugin/
├── manifest.json
├── overlay.html        # required — renders on top of the game
├── dashboard.html      # optional — full browser tab
└── settings.html       # optional — config modal
```

Minimal `overlay.html`:

```html
<!doctype html>
<html>
<head><script src="/sdk.js" data-plugin="my-plugin" data-view="overlay"></script></head>
<body>
  <div id="last-goal">queue up — waiting for a goal</div>
  <script>
    const el = document.getElementById('last-goal');
    RLT.plugin.register({
      events: {
        _GoalScored(goal) {
          const scorer = goal.scorer?.name ?? 'unknown';
          el.textContent = `${scorer} — ${goal.goalSpeed ?? 'n/a'} km/h`;
        },
      },
    });
  </script>
</body>
</html>
```

Full SDK reference, event catalogue, and packaging guide: [`docs/PLUGINS.md`](docs/PLUGINS.md).

## Repository layout

```
backend/        Go backend — HTTP server, SSE bus, plugin host
overlay/        Rust + Tauri 2 overlay window
plugins/        Bundled plugins (Déjà Vu, Session Tracker, etc.)
docs/           BUILD.md, PLUGINS.md
release/        Build outputs (gitignored)
```

## License

[MIT](LICENSE).
