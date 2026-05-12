# Building RL Toolkit

Two binaries make up the project:

| Binary       | Source       | Stack          | Purpose                                |
|--------------|--------------|----------------|----------------------------------------|
| `rl-toolkit` | `./` (root)  | Go             | HTTP server, SSE bus, plugin host      |
| `rl-widget`  | `./overlay`  | Rust + Tauri 2 | Overlay window (unified or per-plugin) |

The toolkit cross-compiles cleanly from any host. The widget has to be
built on its target OS — Tauri's webview crate (`wry`) links native
platform libraries and isn't friendly to cross-compilation.

---

## Linux

This is the development host. Both binaries build natively here.

### Prerequisites (Arch / Cachy / Manjaro)

```bash
sudo pacman -S base-devel rustup go webkit2gtk-4.1 \
               gtk-layer-shell pkg-config nodejs npm
rustup default stable
npm install                # one-time: pulls esbuild + biome locally
```

Other distros: install equivalents of `webkit2gtk-4.1`,
`gtk-layer-shell`, and the GTK 3 dev headers. On Ubuntu 24.04+ that's
`libwebkit2gtk-4.1-dev`, `libgtk-layer-shell-dev`, and `libgtk-3-dev`.

Node is required because every `make` target depends on `make sdk`,
which bundles the web SDK with esbuild. If you only want the Go binary
and don't need a fresh SDK bundle, `go build .` works without Node —
but the resulting binary serves the last `sdk.js` left on disk.

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

`make release` builds the backend and launcher for the host OS into
`release/<host-os>/`. It does **not** cross-compile — the widget needs
the target OS's native libraries, so each platform's release artefacts
are produced on a matching host. See *Releasing* below for the full
multi-platform flow.

### Run

```bash
./rl-toolkit &                                       # background
./overlay/src-tauri/target/release/rl-widget         # unified: all enabled plugins, fullscreen
# or, single-plugin mode:
./overlay/src-tauri/target/release/rl-widget --plugin=dejavu
```

### Stopping the widget

The overlay window is click-through, undecorated, and skip-taskbar — by
design, you can't reach it with the mouse or alt-tab. Exit via the
**tray icon** (right-click → *Quit*). On minimal desktops without a
tray, kill the `rl-widget` process.

---

## Windows

Releases build on GitHub-hosted Windows runners (see *Releasing*), so
a local Windows toolchain is only needed for development iteration on
Windows-specific code. The release ships two Windows artefacts:

| Artefact                         | Audience                                  | Auto-update          |
|----------------------------------|-------------------------------------------|----------------------|
| `RLToolkit_<v>_x64-setup.exe`    | Mainstream — NSIS installer; lands in `%LOCALAPPDATA%\RLT-Launcher\`, Start Menu shortcut, uninstaller in *Apps & Features* | Yes (Tauri updater) |
| `RLToolkit_<v>_x64-portable.zip` | No-admin / USB-stick / power users — unzip and run | No                   |

User data lives at `%LOCALAPPDATA%\RLToolkit\` regardless of artefact.
Install location and data directory are deliberately separate so
updates and uninstalls never touch user state (settings, identity,
plugin databases, logs).

### Prerequisites (local dev only)

1. **Rust** — install [rustup](https://rustup.rs) with the default MSVC
   toolchain.
2. **Visual Studio Build Tools 2022** —
   [download](https://visualstudio.microsoft.com/downloads/#build-tools-for-visual-studio-2022).
   In the installer, pick *Desktop development with C++*. Provides
   `link.exe`, the MSVC C runtime, and the Windows SDK headers Tauri's
   `wry` crate needs.
3. **WebView2 runtime** — preinstalled on Windows 11 and recent
   Windows 10. To check:
   ```powershell
   Get-ItemProperty 'HKLM:\SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}' -ErrorAction SilentlyContinue
   ```
   If that returns nothing, install the
   [evergreen runtime](https://developer.microsoft.com/en-us/microsoft-edge/webview2/).
4. **Go** — for the `rl-toolkit` sidecar and the `gen-update-manifest`
   helper.
5. **Node.js (LTS)** — for `make sdk`. After cloning, run
   `npm install` once.
6. **Tauri CLI**:
   ```powershell
   cargo install tauri-cli --version "^2.0" --locked
   ```

### Run (development)

For dev iteration without going through the installer:

```powershell
make launcher-portable
.\release\windows\RLT-Launcher.exe
```

The portable build bundles the sidecar next to the launcher and is
otherwise identical to the installed version, minus the auto-updater
(it's a separate Cargo feature, gated to the installer build).

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

Without `-dev`, `rl-toolkit dev <path>` fails with a clear hint about
turning it on. `RLT_DEV` accepts `1`, `true`, `yes`, or `on`.

### Known caveats

- **SmartScreen on first run.** Unsigned NSIS still trips Microsoft
  Defender SmartScreen — *"unrecognized app from starting"*. Click
  *More info* → *Run anyway*. Mitigated only by Authenticode.
- **Exclusive fullscreen RL doesn't get overlaid.** No
  compositor-level overlay (Tauri, Discord, Steam, OBS Browser Source)
  appears over an exclusively-fullscreen DirectX game. Set RL to
  **borderless windowed** in *Settings → Video* — that's RL's default,
  so most users are fine without action.
- **Anti-cheat.** RL Toolkit doesn't inject into the game process,
  hook input, or read game memory — it talks to RL's own Stats API
  over TCP. EAC has no problem with it.

---

## Releasing

A release is one GitHub release tag with seven assets:

| Artefact                                | Built on              | Auto-update         |
|-----------------------------------------|-----------------------|---------------------|
| `RLToolkit_<v>_x64-setup.exe`           | CI (`windows-latest`) | Yes (NSIS)          |
| `RLToolkit_<v>_x64-setup.exe.sig`       | CI                    | —                   |
| `RLToolkit_<v>_x64-portable.zip`        | CI                    | No                  |
| `RLToolkit_<v>_x86_64.AppImage`         | CI (`ubuntu-24.04`)   | Yes (Tauri updater) |
| `RLToolkit_<v>_x86_64.AppImage.sig`     | CI                    | —                   |
| `RLToolkit_<v>_x86_64-portable.tar.gz`  | CI                    | No                  |
| `latest.json`                           | CI (last step)        | —                   |

Both platforms build on GitHub-hosted runners. No local Windows machine
required.

The AppImage runs on any glibc-2.39-or-newer distro (Ubuntu 24.04+,
Fedora 40+, current Arch / Cachy / Manjaro). Linux user data lives at
`~/.local/share/RLToolkit/` (or `$XDG_DATA_HOME/RLToolkit`), separate
from the install location, so updates and uninstalls never touch user
state.

The AppImage build host is `ubuntu-24.04`. We started on `ubuntu-22.04`
for maximum glibc compatibility but had to bump because its bundled
GTK (3.24.33) has Wayland surface/damage bugs that produce
vertical-line artifacts on transparent-window resize. 24.04 ships GTK
3.24.41 with the fixes. Users on glibc 2.35–2.38 systems use the
portable tarball instead.

The `release-linux` Make target also strips a few libs from the AppDir
before `appimagetool` repacks. `linuxdeploy-plugin-gtk` bundles the
host's `libwayland-client/cursor/egl/server` and `libepoxy`, but the
bundle has no Mesa — `libEGL` / `libGL` / `libgbm` always come from
the user's host. On hosts with newer wayland (Arch / Cachy at
wayland 1.25), host Mesa drives a Wayland EGL display through the
older bundled `libwayland-client` and fails with `EGL_BAD_PARAMETER`,
leaving the launcher window blank. Letting wayland-client resolve
from the host pairs it with host Mesa and the EGL init succeeds. The
wayland-* libs are NEEDED entries on `libgdk-3.so.0` regardless of
session type, so they're always present on any GTK 3 install.

The rest of the bundled GTK / Cairo / Pango / GLib stack stays in.
`libgstgl-1.0.so.0` also stays bundled because host gstreamer ABI
tends to drift the other way (missing `gst_video_is_dma_drm_caps`).

If the strip list ever needs updating (a future Ubuntu LTS bumps a
soname suffix), the list lives in `Makefile` under `release-linux`.

### One-time setup

#### Signing key

The Tauri updater verifies update integrity with an Ed25519 keypair.
Generate it once on any machine with the Tauri CLI installed:

```bash
cargo tauri signer generate -w ~/rltoolkit-updater.key
```

You'll be prompted for a password. Back up both the `.key` file and
the password — to a password manager **and** an offline location.
**Losing the private key permanently breaks updates for every shipped
version.** Generating a new key forces every existing installation to
be reinstalled by hand, since the old launcher rejects signatures from
the new key.

The matching public key is committed at
`overlay/src-tauri/tauri.launcher.json` under `plugins.updater.pubkey`.
Don't regenerate unless you accept the consequences.

> The signature covers **update integrity** only. It is not
> Authenticode and does not silence the SmartScreen warning on first
> run. Code signing requires a separate ~$300/yr cert and isn't
> planned for now.

#### GitHub Actions secrets

Add two **Repository secrets** at *Settings → Secrets and variables →
Actions* on github.com:

- `TAURI_SIGNING_PRIVATE_KEY` — the contents of the `.key` file
  generated above.
- `TAURI_SIGNING_PRIVATE_KEY_PASSWORD` — the matching password.

Both jobs (Windows and Linux) reuse the same Ed25519 keypair: a single
`latest.json` covers both platforms, and dual keys would mean dual
backups for no real isolation gain.

### Cutting a release

The unified workflow at `.github/workflows/release.yml` builds Windows
and Linux artefacts in parallel on GitHub-hosted runners, then a final
`publish` job assembles the cross-platform `latest.json` and uploads
everything to the draft release. End-to-end runtime is ~15 minutes from
a cold cache, ~8 minutes with warm caches.

#### 1. Bump the version on `main`

```bash
make bump VERSION=0.2.0
git diff overlay/src-tauri/
git commit -am "chore: bump to 0.2.0"
git push origin main
```

`make bump` rewrites the version in `overlay/src-tauri/Cargo.toml` and
`overlay/src-tauri/tauri.conf.json`. It validates strict semver
(`X.Y.Z`, no leading `v`, no pre-release suffix) because the Tauri
updater and `latest.json` both compare versions semver-style.

The bump must land on `main` before the workflow runs — the workflow
checks out `main` and would otherwise pick up the stale version.

#### 2. (Optional) Pre-create the draft release with notes

```bash
gh release create v0.2.0 --repo s1gh/RLToolkit --draft \
  --title "v0.2.0" --notes-file CHANGELOG-0.2.0.md
```

If you skip this, the workflow creates an empty draft. Either way,
`gen-update-manifest` reads the release body and embeds it as the
`notes` field in `latest.json`, so writing notes before the workflow
runs is the simplest path. You can also edit the draft after the
workflow completes and re-run only the `publish` job to refresh
`latest.json` — but pre-creating is less fiddly.

#### 3. Trigger the workflow

```bash
gh workflow run release.yml --repo s1gh/RLToolkit -f tag=v0.2.0
```

Or use the GitHub web UI: *Actions → release → Run workflow*, enter
the tag, click *Run workflow*.

The workflow has four jobs:

- `prepare` — strips the leading `v`, creates a draft release if one
  doesn't exist (preserves existing notes if it does).
- `build-windows` — `windows-latest` runner, `make release-windows`,
  uploads three files as a build artefact.
- `build-linux` — `ubuntu-24.04`, `make release-linux`, uploads three
  files as a build artefact.
- `publish` — downloads both artefact bundles, runs
  `gen-update-manifest` with both sigs, uploads all seven files to the
  draft release with `--clobber`.

`build-windows` and `build-linux` run in parallel.

#### 4. Verify and publish

```bash
# Should show 7 assets
gh release view v0.2.0 --repo s1gh/RLToolkit --json assets | jq -r '.assets[].name' | sort

# Inspect the manifest
gh release download v0.2.0 --repo s1gh/RLToolkit --pattern 'latest.json' -O - | jq .
```

The manifest must list both `windows-x86_64` and `linux-x86_64` under
`platforms`, with URLs matching the canonical asset names.

Then publish (or use the *Edit release* button in the browser):

```bash
gh release edit v0.2.0 --repo s1gh/RLToolkit --draft=false --latest
```

`--latest` is critical: the launcher's auto-update endpoint hits
`releases/latest/download/latest.json`, so the release must be flagged
latest for installed launchers to find the update.

### Re-running on failure

If a build job fails, push fixes to `main` and re-trigger the workflow
with the same tag. `gh release upload --clobber` overwrites partial
uploads cleanly. The draft stays a draft across retries.

If only `publish` failed (build jobs succeeded), GitHub's *Re-run
failed jobs* button on the run page reuses the existing build
artefacts instead of rebuilding — saves the 10+ minutes of compile
time.

### Verifying auto-update with a real install

Run a previous-version launcher (e.g., 0.1.0) on a clean machine.
Within ~10 seconds you should see "Update available: 0.2.0" in the
banner. Click *Update & restart*; after a few seconds the launcher
relaunches as 0.2.0, with the version footer in the bottom-right
corner reflecting the new version. User data at
`~/.local/share/RLToolkit/` (Linux) or `%LOCALAPPDATA%\RLToolkit\`
(Windows) is preserved across the upgrade.

### Common pitfalls

- **Forgetting the bump commit.** The workflow checks out `main`. If
  the bump isn't pushed, Tauri's bundler names artefacts after the
  old version and the workflow will still succeed but ship the wrong
  filenames. `make bump` plus the commit must land on `main` before
  triggering.
- **Tag input doesn't match the bumped version.** The workflow strips
  the leading `v` from the tag input and passes that to the build. If
  you bumped to `0.2.0` but typed `v0.2.1` as the tag, the build
  proceeds but the source-tree version-sync step rewrites Cargo.toml
  on the checkout to `0.2.1` — artefacts say `0.2.1`, `main` says
  `0.2.0`. Confusing. Stick to matching versions.
- **GitHub repo must be public** for the auto-updater to fetch
  `latest.json` over plain HTTPS. Private repos return 404 to
  unauthenticated callers.
- **Don't republish over a published release.** If something's wrong
  after publish, cut a new patch version (e.g., 0.2.1) instead of
  re-tagging — installed launchers cache `latest.json` briefly, and
  in-place rewrites can confuse the updater plugin.

---

## macOS

Not implemented. The Tauri code paths nominally cover macOS the same
way as Windows (`#[cfg(not(target_os = "linux"))]`), but no one has
built against the Apple toolchain yet. Contributions welcome.
