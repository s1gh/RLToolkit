# Building RL Toolkit

Two binaries make up RL Toolkit:

| Binary       | Source                | Stack            | Purpose                          |
|--------------|-----------------------|------------------|----------------------------------|
| `rl-toolkit` | `./` (root)           | Go               | HTTP server, SSE bus, plugin host |
| `rl-widget`  | `./overlay`       | Rust + Tauri 2   | Overlay window (unified or per-plugin) |

The toolkit cross-compiles cleanly from any host. The widget needs to be
built on its target OS — Tauri's webview crate (`wry`) links native
platform libraries and isn't friendly to cross-compilation.

---

## Linux

This is the development host. Both binaries build natively here.

### Prereqs (Arch / Cachy / Manjaro)

```bash
sudo pacman -S base-devel rustup go webkit2gtk-4.1 \
               gtk-layer-shell pkg-config nodejs npm
rustup default stable
npm install                # one-time: pulls esbuild + biome locally
```

Other distros: install equivalents of `webkit2gtk-4.1`, `gtk-layer-shell`,
and the GTK 3 dev headers. On Ubuntu 24.04+ that's
`libwebkit2gtk-4.1-dev`, `libgtk-layer-shell-dev`, and `libgtk-3-dev`.

`nodejs`/`npm` are required for any `make` target — the `make sdk` step
bundles the web SDK with esbuild, and every other target (`make backend`,
`make release`, etc.) depends on it. After cloning, run `npm install`
once to fetch esbuild (and biome, used by `make fmt` / `make lint`).
If you only want to build the Go binary directly (`go build .`) and skip
the bundled SDK, you can do that without Node — but the resulting
binary will serve a stale `sdk.js` from the last bundle on disk.

### Build

```bash
# Toolkit (~5 sec)
go build -o rl-toolkit .

# Widget (~1 min first time, then incremental)
cd overlay/src-tauri
cargo build --release
# → overlay/src-tauri/target/release/rl-widget
```

### Release bundle

`make release` runs both Go cross-compiles plus the host-OS widget build,
collecting outputs under `release/{linux,windows}/`. The widget for a
non-host OS still has to be built on that OS — wry can't cross-compile.

### Run

```bash
./rl-toolkit &                                          # background
./overlay/src-tauri/target/release/rl-widget        # unified: all enabled plugins, fullscreen
# or, single-plugin mode:
./overlay/src-tauri/target/release/rl-widget --plugin=dejavu
```

### Stopping the widget

The overlay window is click-through, undecorated, and skip-taskbar — by
design, you can't reach it with the mouse or alt-tab. Two exit paths:

- **Global hotkey** (default `Ctrl+Shift+Q`). Always available; if the
  default conflicts with another app, override with
  `--quit-hotkey=<COMBO>` (Tauri shortcut syntax — e.g. `Alt+F4`,
  `CmdOrCtrl+W`).
- **Tray icon** — right-click → *Quit*. Best-effort; some minimal
  desktops can't host a tray.

---

## Windows

### Prereqs

1. **Rust** — install [rustup](https://rustup.rs). Default MSVC toolchain.
2. **Visual Studio Build Tools 2022** — [download](https://visualstudio.microsoft.com/downloads/#build-tools-for-visual-studio-2022).
   In the installer pick *Desktop development with C++*. Provides
   `link.exe`, the MSVC C runtime, and the Windows SDK headers Tauri's
   `wry` crate needs.
3. **WebView2 runtime** — preinstalled on Windows 11 and recent Windows
   10. To check, run in PowerShell:
   ```powershell
   Get-ItemProperty 'HKLM:\SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}' -ErrorAction SilentlyContinue
   ```
   If that returns nothing, install the [evergreen runtime](https://developer.microsoft.com/en-us/microsoft-edge/webview2/).
4. **Go** (for the toolkit, optional — see "Building rl-toolkit" below).
5. **Node.js (LTS)** — required for any `make` target on Windows. The
   `make sdk` step bundles the web SDK with esbuild, and `make backend`
   / `make release` depend on it. Install from
   [nodejs.org](https://nodejs.org/) (LTS installer adds `node` and
   `npm` to PATH). Then in the repo root:
   ```powershell
   npm install
   ```
   to fetch the local esbuild + biome devDeps.

   If Node isn't installed, you can still build the toolkit binary
   directly with `go build -o rl-toolkit.exe .` — but that skips the
   SDK bundle step, so the binary will serve whatever `sdk.js` is
   currently on disk under `backend\internal\server\web\sdk\dist\`.
   Building from a fresh clone without Node will fail — there's no
   pre-bundled SDK in version control.
6. **Tauri CLI**:
   ```powershell
   cargo install tauri-cli --version "^2.0" --locked
   ```

### Build the widget

```powershell
cd overlay\src-tauri
cargo tauri build           # release: ~5–10 min, optimized
# or
cargo tauri build --debug   # debug:   ~2 min, faster iteration
```

Output:

```
overlay\src-tauri\target\release\rl-widget.exe                       # ~7 MB
overlay\src-tauri\target\release\bundle\msi\rl-widget_0.1.0_x64_*.msi  # installer
```

### Build the toolkit

You can either build natively on Windows:

```powershell
go build -o rl-toolkit.exe .
```

…or cross-compile from Linux (no extra setup, Go's stdlib only):

```bash
GOOS=windows GOARCH=amd64 go build -o rl-toolkit.exe .
```

Both produce identical binaries.

### Run

```powershell
.\rl-toolkit.exe                                  # in one terminal
.\overlay\src-tauri\target\release\rl-widget.exe                  # unified: all enabled plugins, fullscreen
# or, single-plugin mode:
.\overlay\src-tauri\target\release\rl-widget.exe --plugin=dejavu
```

### Known caveats on Windows

- **Exclusive fullscreen RL doesn't get overlaid.** No compositor-level
  overlay (Tauri, Discord, Steam, OBS Browser Source) appears over an
  exclusively-fullscreen DirectX game. Set RL to **borderless windowed**
  in Settings → Video. It's RL's default; only exclusive fullscreen
  breaks the overlay.
- **SmartScreen warning on first run.** The `.exe` is unsigned; Windows
  will show a "Microsoft Defender SmartScreen prevented an unrecognized
  app from starting" dialog. Click *More info* → *Run anyway*. Code
  signing requires a $300/yr cert and isn't planned for now.
- **Anti-cheat.** RL ships with Easy Anti-Cheat in some modes. RL Toolkit
  doesn't inject into the game process, hook input, or read game memory
  — it talks to RL's own Stats API over TCP, which is the same surface
  used by Bakkesmod and friends. EAC has no problem with it.

---

## macOS

Untested as of v0.1.0. The Tauri code paths exist
(`#[cfg(not(target_os = "linux"))]` covers macOS the same way as
Windows), but no one's compiled against the Apple toolchain yet.

To attempt: install Rust, Xcode Command Line Tools (`xcode-select
--install`), Node.js LTS (`brew install node` — required for `make`
targets to bundle the web SDK), and Go if you also want the toolkit
(`brew install go`). Then `npm install` in the repo root, then
`cargo tauri build` from `overlay/src-tauri`. Report what breaks.

---

## CI / multi-platform releases

Not set up yet. When we want signed installers per OS the path is
[`tauri-apps/tauri-action`](https://github.com/tauri-apps/tauri-action)
running on `windows-latest`, `macos-latest`, and `ubuntu-latest`
runners. Free for public repos. To enable, push the project to GitHub
and add `.github/workflows/release.yml` — happy to write that workflow
when we get there.
