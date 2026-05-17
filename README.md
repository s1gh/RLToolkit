<div align="center">

# RL Toolkit

### A plugin-based overlay platform for Rocket League

[![Latest release](https://img.shields.io/github/v/release/s1gh/RLToolkit?include_prereleases&style=for-the-badge&label=release&color=fb7c3c)](https://github.com/s1gh/RLToolkit/releases/latest)
[![Platforms](https://img.shields.io/badge/platforms-Windows%20%7C%20Linux-22d3ee?style=for-the-badge)](#install)
[![License: MIT](https://img.shields.io/badge/license-MIT-a78bfa?style=for-the-badge)](LICENSE)

Install the launcher, enable the plugins you want, and they render as transparent overlays on top of the game or as Browser Sources in OBS.

<br/>

![RL Toolkit launcher with the plugin list and OBS browser-source URLs](docs/assets/hero.png)

</div>

---

## What it is

You install one app: the **RL Toolkit launcher**. It bundles everything: the backend that talks to Rocket League's Stats API, the dashboard UI you see above, and the overlay window that floats on top of the game. From the dashboard you toggle plugins on and off, open per-plugin dashboards in their own tabs, configure them, and grab Browser Source URLs for OBS or Streamlabs.

It does not inject into the game, hook input, or read game memory. It only consumes RL's own Stats API over TCP, so EAC has no problem with it.

## Plugins

Every plugin (first-party and third-party) lives in the **plugin catalog** published on the [GitHub releases page](https://github.com/s1gh/RLToolkit/releases) under the `plugins-latest` tag. To install one, download the `.rltp` from that release and drop it onto the launcher's **Install plugin…** button. Once a plugin is installed, the launcher polls the catalog in the background; when a newer version ships, an **Update all** button appears next to the plugin list and applies the upgrade in place. No manual re-download for updates.

The current catalog includes:

| Plugin | What it does |
|---|---|
| **Déjà Vu** | Tracks players you've encountered before and shows encounter history. |
| **Session Tracker** | Wins, losses, and stats for the current launcher session. |
| **Demolitions** | Demolitions dealt this match and all-time. |
| **Ballchasing Upload** | Auto-uploads saved replays to ballchasing.com. |
| **Crossbar Sound** | Plays a sound effect when the ball hits a crossbar. |
| **Minigames** | In-match challenges with XP, levels, and streaks. |
| **Teammate Boost** | Live boost gauge for each of your teammates. |

The **Install plugin…** button also accepts hand-built `.rltp` packages — useful for testing plugins you're writing or sharing one privately before it lands in the catalog.

## Install

Grab the latest release from the [releases page](https://github.com/s1gh/RLToolkit/releases/latest):

| Platform | Artefact | Notes |
|---|---|---|
| Windows | `RLToolkit_<v>_x64-setup.exe` | NSIS installer, auto-updates. |
| Windows | `RLToolkit_<v>_x64-portable.zip` | No-admin or USB-stick. No auto-update. |
| Linux   | `RLToolkit_<v>_x86_64.AppImage` | glibc 2.39+ (Ubuntu 24.04+, Fedora 40+, current Arch). Auto-updates. |
| Linux   | `RLToolkit_<v>_x86_64-portable.tar.gz` | Tarball. No auto-update. |

In Rocket League, set *Settings → Video → Display Mode* to **Borderless**. Exclusive fullscreen blocks all compositor-level overlays (Tauri, Discord, Steam Overlay, OBS Browser Source). It's a fullscreen-rendering limitation that affects every overlay tool, not specific to RL Toolkit.

On Linux you'll also need WebKit2GTK 4.1 if your distro doesn't ship it by default. Debian / Ubuntu: `sudo apt install libwebkit2gtk-4.1-0`. Arch / Cachy / Manjaro: `sudo pacman -S webkit2gtk-4.1`. Fedora: `sudo dnf install webkit2gtk4.1`.

## Using it

1. Launch RL Toolkit. The launcher window opens with the plugin list.
2. Toggle the plugins you want on. They show up in the overlay immediately.
3. Click **Open** next to a plugin to see its full dashboard (history, stats, settings) in a browser tab.
4. Start Rocket League in **Borderless** mode. The overlay paints on top.

**Editing the overlay layout while in-game.** Press **Ctrl+Shift+E** at any time (or click **Edit overlay** in the launcher) to enter edit mode. Drag widgets to reposition or resize them, hit the shortcut again to commit. The shortcut works while Rocket League has focus, so you can lay out the overlay against the live game without alt-tabbing.

## OBS / Streamlabs

The launcher exposes two URLs for browser-source capture:

- **Overlay:** `http://localhost:49200/overlay`. All enabled plugins, transparent background, ready to drop into OBS as a Browser Source.
- **Overlay editor:** `http://localhost:49200/overlay?edit=1`. Same view, but you can drag widgets to reposition and resize them. Layout persists.

The launcher's *Browser sources* panel has copy buttons for both. Default port is `49200`.

## For developers

Plugins are folders with a `manifest.json` and one or more HTML files. No build step, no compilation. See [`docs/PLUGINS.md`](docs/PLUGINS.md) for the SDK reference, and [`docs/BUILD.md`](docs/BUILD.md) for building the launcher and backend from source.

---

<div align="center">

**RL Toolkit** is in active development. APIs and storage formats may change between releases until 1.0.

[Releases](https://github.com/s1gh/RLToolkit/releases) · [Plugin catalog](https://github.com/s1gh/RLToolkit/releases/tag/plugins-latest) · [Issues](https://github.com/s1gh/RLToolkit/issues) · [License](LICENSE)

</div>
