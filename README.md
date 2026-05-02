# RL Toolkit

A lightweight, plugin-based framework for the Rocket League Stats API.

RL Toolkit consists of two components:
- **Server** (`rl-toolkit`) - A Go application that connects to Rocket League's Stats API and serves plugin data via HTTP
- **Overlay** (`rl-widget`) - A Rust/Tauri desktop widget that displays plugin overlays on top of your game

Both run natively on **Windows**, **Linux**, and **macOS**.

---

## What You'll Need Before Starting

- **Rocket League** installed and updated
- The `rl-toolkit` server running (either pre-built or compiled from source)
- *(Optional)* The `rl-widget` overlay app for desktop overlays

---

## Quick Start

### 1. Enable the Rocket League Stats API

Rocket League needs to be configured to send game data. Edit this file:

**Windows:**
```
C:\Program Files\Epic Games\RocketLeague\TAGame\Config\DefaultStatsAPI.ini
```

**Linux (Proton/Steam):**
```
~/.steam/steam/steamapps/common/rocketleague/TAGame/Config/DefaultStatsAPI.ini
```

Add or modify these lines:

```ini
[TAGame.MatchStatsExporter_TA]
PacketSendRate=60
Port=49123
```

**Save the file and restart Rocket League.**

### 2. Start the RL Toolkit Server

The server is the core of RL Toolkit. It receives game data and makes it available to plugins.

**Windows:**
```powershell
# If you have the pre-built binary
.\rl-toolkit.exe

# Or run from source (requires Go - see Building section below)
go run .
```

**Linux:**
```bash
# If you have the pre-built binary
./rl-toolkit

# Or run from source (requires Go - see Building section below)
go run .
```

When started, the server will:
- Connect to Rocket League on `localhost:49123`
- Start a web dashboard at `http://localhost:8080`
- Load all plugins from the `plugins/` folder

You should see output like:
```
2026/05/02 12:00:00 RL Toolkit listening on :8080
2026/05/02 12:00:00 Connected to RL Stats API at 127.0.0.1:49123
```

### 3. Open the Dashboard

Open `http://localhost:8080` in your browser. This shows:
- Connection status to Rocket League
- Loaded plugins and their status
- Links to individual plugin pages

---

## Using the Overlay

The overlay displays plugin information on top of Rocket League while you play. You have three options:

### Option 1: Desktop Widget (Recommended for Players)

The `rl-widget` is a transparent, click-through window that stays on top of your game.

**Important:** Rocket League must be in **borderless windowed** mode (Settings → Video). Overlays cannot appear above exclusive fullscreen.

**Linux:**
```bash
./overlay-app/src-tauri/target/release/rl-widget
# Or for a specific plugin:
./overlay-app/src-tauri/target/release/rl-widget --plugin=dejavu
```

**Windows:**
```powershell
.\overlay-app\src-tauri\target\release\rl-widget.exe
# Or for a specific plugin:
.\overlay-app\src-tauri\target\release\rl-widget.exe --plugin=dejavu
```

The widget will appear as a transparent overlay. You can click through it to play normally.

**Building the widget** requires Rust and Tauri. See the Building section below.

### Option 2: OBS Browser Source (Recommended for Streamers)

If you're streaming and don't need to see the overlay yourself:

1. In OBS, add a **Browser Source**
2. Set URL to `http://localhost:8080/overlay`
3. Set width/height to your monitor resolution (e.g., 1920x1080)
4. Enable **"Shutdown source when not visible"**

This bakes the overlay directly into your stream output.

### Option 3: Browser on Second Monitor

Open any plugin's overlay in a regular browser:

```
# Control page (shows settings)
http://localhost:8080/plugins/dejavu/overlay.html

# Transparent overlay view
http://localhost:8080/plugins/dejavu/overlay.html?overlay=1
```

Move the browser window to a second monitor. The overlay view removes all chrome and backgrounds.

---

## Plugin: Déjà Vu (Included)

Déjà Vu is a player tracker that comes with RL Toolkit. It shows:

- **Encounter count** - How many times you've played against each player
- **Player history** - Previous usernames and aliases
- **Platform** - Steam, Epic, PlayStation, Xbox
- **Live match stats** - Goals, assists, saves, shots, demos (real-time)
- **Match info** - Score and time remaining

**First-time setup:** When you first use Déjà Vu, you need to tell it who you are:

1. Join a match
2. Look for your name in the overlay
3. Click your name, OR
4. Manually enter your `PrimaryId` at the bottom (format: `Steam|12345|0`)

Your player data is saved in `data/dejavu.json` and persists between sessions.

---

## Building from Source

### Prerequisites

**Both platforms need:**
- [Go 1.22+](https://go.dev/dl/) (for the server)
- [Rust](https://rustup.rs) (for the overlay widget)

**Linux additional:**
```bash
# Arch/Manjaro
sudo pacman -S base-devel webkit2gtk-4.1 gtk-layer-shell pkg-config

# Ubuntu 24.04+
sudo apt install libwebkit2gtk-4.1-dev libgtk-layer-shell-dev libgtk-3-dev
```

**Windows additional:**
- [Visual Studio Build Tools 2022](https://visualstudio.microsoft.com/downloads/) with "Desktop development with C++"
- WebView2 runtime (pre-installed on Windows 11, may need [manual install](https://developer.microsoft.com/en-us/microsoft-edge/webview2/) on older Windows 10)

### Building the Server (rl-toolkit)

**Linux:**
```bash
go build -o rl-toolkit .
```

**Windows:**
```powershell
go build -o rl-toolkit.exe .
# Or cross-compile from Linux:
# GOOS=windows GOARCH=amd64 go build -o rl-toolkit.exe .
```

The server has no system dependencies and cross-compiles easily.

### Building the Overlay Widget (rl-widget)

**Linux:**
```bash
cd overlay-app/src-tauri
cargo build --release
# Output: overlay-app/src-tauri/target/release/rl-widget
```

**Windows:**
```powershell
cd overlay-app\src-tauri
cargo build --release
# Output: overlay-app\src-tauri\target\release\rl-widget.exe
```

**Note:** The widget must be built on its target operating system. Tauri's webview library links native OS libraries and cannot be cross-compiled.

---

## Command Line Options

```
  -rl-addr string    RL Stats API address (default "127.0.0.1:49123")
  -port int          HTTP server port (default 8080)
  -plugins string    Plugin directory path (default "plugins")
  -data string       Data directory path (default "data")
```

Example: Run on a different port with custom plugin location
```bash
./rl-toolkit -port 9000 -plugins ./my-plugins -data ./my-data
```

---

## Writing Plugins

Create a new plugin:

```bash
./rl-toolkit new my-plugin
```

This creates `plugins/my-plugin/` with a working template (`manifest.json` and `overlay.html`).

For full documentation on events, the widget API, persistence, and debugging, see **[docs/PLUGINS.md](docs/PLUGINS.md)**.

View all available events:
```bash
curl http://localhost:8080/api/events
```

---

## Troubleshooting

**"Connection refused" or server won't start**
- Make sure Rocket League is running with the Stats API enabled (see Step 1)
- Check if port 49123 is already in use: `netstat -an | grep 49123` (Linux) or `netstat -an | findstr 49123` (Windows)

**Overlay not appearing**
- Rocket League must be in borderless windowed mode, not exclusive fullscreen
- On Linux: make sure you're using a Wayland compositor or have the required GTK libraries installed

**Widget won't build on Linux**
- Install `webkit2gtk-4.1` and `gtk-layer-shell` for your distribution
- Make sure `pkg-config` is installed

**SmartScreen warning on Windows**
- The executables are unsigned. Click "More info" → "Run anyway"

---

## Plugin Ideas

What you can build with the Stats API:

- **Boost tracker** — Real-time boost meter with usage history
- **Session stats** — Aggregate stats across multiple matches
- **Goal analysis** — Shot placement heatmaps from goal data
- **Demo tracker** — Demolition stats with sound effects
- **Match timeline** — Visual timeline of goals, demos, and events
- **Speed tracker** — Ball and car speed graphs
- **Crossbar counter** — Track those near-misses
