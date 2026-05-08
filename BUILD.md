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
design, you can't reach it with the mouse or alt-tab. Exit via the
**tray icon** (right-click → *Quit*). On minimal desktops that can't
host a tray, kill the `rl-widget` process.

---

## Windows

Two artifacts ship per release:

| Artifact                         | For                                                                                       | Auto-update              |
|----------------------------------|-------------------------------------------------------------------------------------------|--------------------------|
| `RLToolkit_<v>_x64-setup.exe`    | Mainstream — NSIS installer; lands in `%LOCALAPPDATA%\RLT-Launcher\`, Start Menu shortcut, uninstaller in *Apps & Features* | Yes (Tauri updater)      |
| `RLToolkit_<v>_x64-portable.zip` | Power users / no-admin / USB stick — unzip and run                                        | Notify-only (banner links to release page) |

User data lives at `%LOCALAPPDATA%\RLToolkit\` regardless of which
artifact you ship. The install dir and the data dir are deliberately
separate so updates and uninstalls never touch user state (settings,
identity, plugin databases, logs).

### Prereqs

1. **Rust** — install [rustup](https://rustup.rs). Default MSVC toolchain.
2. **Visual Studio Build Tools 2022** — [download](https://visualstudio.microsoft.com/downloads/#build-tools-for-visual-studio-2022).
   In the installer, pick *Desktop development with C++*. Provides
   `link.exe`, the MSVC C runtime, and the Windows SDK headers Tauri's
   `wry` crate needs.
3. **WebView2 runtime** — preinstalled on Windows 11 / recent Windows 10.
   To check:
   ```powershell
   Get-ItemProperty 'HKLM:\SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}' -ErrorAction SilentlyContinue
   ```
   If that returns nothing, install the [evergreen runtime](https://developer.microsoft.com/en-us/microsoft-edge/webview2/).
4. **Go** — for the `rl-toolkit` sidecar and the `gen-update-manifest` helper.
5. **Node.js (LTS)** — `make sdk` bundles the web SDK with esbuild. After cloning:
   ```powershell
   npm install
   ```
6. **Tauri CLI**:
   ```powershell
   cargo install tauri-cli --version "^2.0" --locked
   ```

### Signing key (one-time)

The Tauri updater verifies update integrity with an Ed25519 keypair.
Generate it once:

```powershell
cargo tauri signer generate -w "$HOME\Documents\rltoolkit-updater.key"
```

You'll be prompted for a password. Back up both the `.key` file and the
password — to a password manager **and** an offline location. **Loss of
the private key permanently breaks updates for already-shipped
versions.** A new key forces every existing installation to be
reinstalled by hand, since the old launcher will reject signatures from
the new key.

The matching public key is committed in
`overlay\src-tauri\tauri.launcher.json` under `plugins.updater.pubkey`.
Don't regenerate unless you accept the consequences.

> The signature is for **update integrity** only. It is NOT
> Authenticode and does not silence the SmartScreen warning on first
> run. Code signing requires a separate ~$300/yr cert and isn't
> planned for now.

### Building a release

Set the signing env vars in your PowerShell session:

```powershell
$env:TAURI_SIGNING_PRIVATE_KEY = Get-Content -Raw "$HOME\Documents\rltoolkit-updater.key"
$env:TAURI_SIGNING_PRIVATE_KEY_PASSWORD = "<password>"
```

Bump the version. The updater compares the running version against
`latest.json`, so it has to actually change.

- `overlay\src-tauri\Cargo.toml` — `version = "0.2.0"`
- `overlay\src-tauri\tauri.conf.json` — `"version": "0.2.0"`

(`scripts\build-windows-smoketest.ps1` has a `Set-Version` helper that
rewrites both files in lockstep — handy for smoke tests, but for real
releases keep it as a normal commit so the version bump shows up in
`git log`.)

Build:

```powershell
make release-windows VERSION=0.2.0 RELEASE_OWNER=<github-owner> RELEASE_NOTES="bug fixes"
```

Outputs in `release\windows\`:

- `RLToolkit_0.2.0_x64-setup.exe` — NSIS installer, signed
- `RLToolkit_0.2.0_x64-setup.exe.sig` — minisign signature
- `RLToolkit_0.2.0_x64-portable.zip` — `RLT-Launcher.exe` + `rl-toolkit.exe`
- `latest.json` — manifest the running launcher fetches to detect updates

### Publishing

Upload **all four** artifacts to a single GitHub release named `v0.2.0`,
and mark it as the latest release (the launcher's `endpoints` config
points at `releases/latest/download/latest.json`):

```powershell
gh release create v0.2.0 --title "v0.2.0" --notes "..." --latest `
  release\windows\RLToolkit_0.2.0_x64-setup.exe `
  release\windows\RLToolkit_0.2.0_x64-setup.exe.sig `
  release\windows\RLToolkit_0.2.0_x64-portable.zip `
  release\windows\latest.json
```

The asset filename in `latest.json` (`RLToolkit_<v>_x64-setup.exe`) must
match the URL on GitHub exactly, or installed launchers will hit a 404
on the download step. `release-windows` generates the URL from
`RELEASE_OWNER` + `VERSION`, so as long as you don't rename the
artifacts, the manifest stays consistent.

### Smoke testing the auto-update flow

`scripts\build-windows-smoketest.ps1` builds **both** an old (`0.1.0`)
and a new (`0.2.0`) installer in one pass, with version files reverted
back to `0.1.0` after the build. Use it to verify the full
"banner → download → relaunch on new version" loop end-to-end before a
real release. See the script's header comment for the workflow.

### Run (development)

For dev iteration without going through the installer:

```powershell
make launcher-portable
.\release\windows\RLT-Launcher.exe
```

The portable build bundles the sidecar next to the launcher and is
otherwise identical, minus the auto-updater (it's a separate Cargo
feature, gated to the installer build).

#### Plugin hot-reload (`rl-toolkit dev`)

The backend's localhost dev API — used by `rl-toolkit dev <plugin-path>`
to push live edits into the running overlay — is **off by default**.
Production runs don't need it; opt in only while iterating on a plugin:

```powershell
# Direct backend run:
.\rl-toolkit.exe -dev

# Via the launcher: set the env var before launching, the launcher
# propagates it to the sidecar as -dev:
$env:RLT_DEV = "1"
.\release\windows\RLT-Launcher.exe
```

Without `-dev`, `rl-toolkit dev <path>` will fail with a clear hint
about turning it on. `RLT_DEV` accepts `1`, `true`, `yes`, or `on`.

### Known caveats

- **SmartScreen on first run.** Unsigned NSIS still trips Microsoft
  Defender SmartScreen — *"unrecognized app from starting"*. Click
  *More info* → *Run anyway*. Mitigated only by Authenticode.
- **Exclusive fullscreen RL doesn't get overlaid.** No compositor-level
  overlay (Tauri, Discord, Steam, OBS Browser Source) appears over an
  exclusively-fullscreen DirectX game. Set RL to **borderless windowed**
  in *Settings → Video*. It's RL's default; only exclusive fullscreen
  breaks the overlay.
- **Anti-cheat.** RL Toolkit doesn't inject into the game process, hook
  input, or read game memory — it talks to RL's own Stats API over TCP,
  the same surface as Bakkesmod. EAC has no problem with it.

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
