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

The toolkit serves plugin overlays as web pages. There are several ways to
display them on top of your game:

### Method A: OBS Browser Source (recommended for streamers)

1. In OBS, add a **Browser Source**
2. Set URL to `http://localhost:8080/overlay`
3. Set width/height to your monitor resolution
4. Enable **"Shutdown source when not visible"**

This composites all plugin overlays at their configured screen positions.

### Method B: Browser on a second monitor

Open `http://localhost:8080/plugins/dejavu/overlay.html` in a browser on
your second monitor.

### Method C: Windows overlay (transparent topmost window)

Run the included PowerShell script (requires Edge WebView2, pre-installed
on Windows 10/11):

```powershell
powershell -ExecutionPolicy Bypass -File overlay.ps1
```

Or use any tool that can display a transparent, always-on-top browser window
pointed at `http://localhost:8080/overlay`.

### Method D: Individual plugin overlays

Each plugin can be opened directly:

```
http://localhost:8080/plugins/dejavu/overlay.html          (with background)
http://localhost:8080/plugins/dejavu/overlay.html?overlay=1 (transparent)
```

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

Plugins are folders inside `plugins/` containing a `manifest.json` and an
`overlay.html`:

```
plugins/
└── my-plugin/
    ├── manifest.json
    └── overlay.html
```

### manifest.json

```json
{
  "name": "my-plugin",
  "title": "My Plugin",
  "version": "1.0.0",
  "author": "you",
  "overlay": {
    "file": "overlay.html",
    "width": 300,
    "height": 200,
    "anchor": "bottom-left",
    "offset_x": 16,
    "offset_y": 16,
    "opacity": 0.9,
    "click_through": true
  }
}
```

`anchor` can be `top-left`, `top-right`, `bottom-left`, or `bottom-right`.

### Event Stream

Connect to the live event stream via Server-Sent Events:

```javascript
const es = new EventSource('/events');
es.onmessage = (e) => {
  const msg = JSON.parse(e.data);
  // msg.Event = "UpdateState" | "GoalScored" | "BallHit" | ...
  // msg.Data  = event payload (see RL Stats API docs)
};
```

### Data Persistence

Store and retrieve plugin data via the REST API:

```javascript
// Save
await fetch('/api/data/my-plugin/some-key', {
  method: 'POST',
  headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify({ hello: 'world' })
});

// Load
const resp = await fetch('/api/data/my-plugin/some-key');
const data = await resp.json();

// Load all keys
const all = await fetch('/api/data/my-plugin');

// Delete
await fetch('/api/data/my-plugin/some-key', { method: 'DELETE' });
```

Data is stored in `data/{plugin-name}.json`.

### Available Events

| Event | Description |
|---|---|
| `UpdateState` | Full match state (players, scores, ball, teams) at configured tick rate |
| `GoalScored` | Goal with scorer, assister, speed, impact location |
| `BallHit` | Ball hit with pre/post speed and location |
| `StatfeedEvent` | Player earned a stat (demo, save, epic save, etc.) |
| `CrossbarHit` | Ball hit the crossbar |
| `MatchCreated` | Match lobby created |
| `MatchInitialized` | First countdown started |
| `RoundStarted` | Countdown finished, play begins |
| `MatchEnded` | Winner decided |
| `MatchDestroyed` | Left the match |
| `MatchPaused` / `MatchUnpaused` | Match paused/resumed |
| `GoalReplayStart` / `GoalReplayEnd` | Goal replay lifecycle |
| `ClockUpdatedSeconds` | Game clock changed |
| `_ConnectionStatus` | Framework event: RL API connection state changed |

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

Requires Go 1.22+:

```bash
# Native build
go build -o rl-toolkit .

# Cross-compile for Windows
GOOS=windows GOARCH=amd64 go build -o rl-toolkit.exe .

# Cross-compile for macOS
GOOS=darwin GOARCH=arm64 go build -o rl-toolkit-mac .
```

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
