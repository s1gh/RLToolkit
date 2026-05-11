# Plugin updates from inside the launcher

## Goal

Let users update installed plugins from inside the launcher's plugin
grid. Each plugin card shows a pill when a newer version is available;
clicking the pill updates that plugin in place. A section-level
"Update all (N)" button updates every plugin with a pending update in
one sweep. Running overlay / dashboard / background iframes for an
updated plugin reload in place; no launcher restart is required.

The mechanism is independent of launcher releases: plugins ship on
their own cadence through a separate catalog and a separate GitHub
release.

## Non-goals

- Plugin discovery / "browse and install new plugins from a catalog."
  Only updates of already-installed plugins are in scope. The same
  catalog can power discovery later; that's a follow-up project.
- Third-party / per-plugin update URLs. v1 has one curated catalog
  controlled by the toolkit maintainers. Adding a third-party source
  later is additive.
- Cryptographic signing of `.rltp` artifacts. SHA-256 from the
  catalog provides integrity; we trust the GitHub releases host the
  same way the existing launcher updater does.
- A "skip this version" / "remind me later" affordance. Pills appear
  whenever a newer version exists; the only ways to clear one are to
  install the update or to disable the plugin.

## Architecture

Three independently testable units, plus an HTTP API and a publishing
workflow.

### Catalog format (`plugins.json`)

Hosted as an asset on a long-lived GitHub release tagged
`plugins-latest`, alongside the `.rltp` artifacts it references.
Stable URL:

```
https://github.com/<owner>/RLToolkit/releases/download/plugins-latest/plugins.json
```

Schema:

```json
{
  "schema": 1,
  "generated_at": "2026-05-11T14:00:00Z",
  "plugins": [
    {
      "name": "demos2",
      "version": "1.1.0",
      "title": "Demolitions 2",
      "author": "rl-toolkit",
      "description": "Tracks how many players you've demolished, in this match and across all time",
      "url": "https://github.com/<owner>/RLToolkit/releases/download/plugins-latest/demos2-1.1.0.rltp",
      "sha256": "9f3a...c4",
      "size_bytes": 48213,
      "min_launcher_version": "0.3.0"
    }
  ]
}
```

Field notes:

- `schema: 1` lets a future launcher reject an unknown future format
  cleanly instead of half-parsing it.
- `min_launcher_version` is optional. Absent means no floor. When
  present and the running launcher is older, the entry is filtered
  out of the in-memory catalog at load time and never surfaces in
  the diff.
- `title`, `author`, `description` are catalog-side copies of the
  manifest fields, advisory only. The canonical post-install values
  always come from the unpacked `manifest.json`.
- `sha256` is required. Lowercase hex. Entries with an invalid
  sha256 are dropped at load time with a one-line log.

### `backend/internal/catalog` (new)

Pure I/O and parsing. No install side effects. Public surface:

```go
type Entry struct {
    Name, Version, Title, Author, Description, URL, SHA256 string
    SizeBytes          int64
    MinLauncherVersion string
}

type Update struct {
    Name              string
    InstalledVersion  string
    LatestVersion     string
    Entry             Entry
}

type Manager struct { /* url, http client, launcher version, plugins ref, cache */ }

func New(url string, launcherVersion string, pm *plugins.Manager) *Manager
func (m *Manager) Refresh(ctx context.Context) error
func (m *Manager) Updates() []Update
func (m *Manager) Find(name string) (Entry, bool)
func (m *Manager) LastChecked() time.Time
func (m *Manager) LastError() error
```

Behavior:

- `Refresh` does an HTTP GET with a 10s timeout, validates
  `schema == 1`, drops entries whose `min_launcher_version` exceeds
  the running version, drops entries with an invalid sha256 or URL.
  Dropped entries are logged once each, the same shape as the
  existing `sanitizeConnectPermissions` log line.
- `Updates` compares versions using semver. Catalog version > installed
  version means an update. Dev-registered plugins (names in
  `plugins.Manager.dev`) are excluded — they don't update from the
  catalog. We add a `DevNames() []string` accessor on the plugins
  Manager for this.
- Failures (network, parse, bad schema) make `Updates` return an
  empty slice rather than partial results. `LastError` exposes the
  most recent failure for the UI's banner.
- The catalog is held in memory only. No on-disk cache; re-fetching
  a small JSON on startup is cheap and avoids stale-state bugs.

### `backend/internal/install` (existing, extended)

A single new entry point:

```go
func InstallFromURL(ctx context.Context, url, expectedSHA256, pluginsDir string) (name string, err error)
```

Streams the download to a temp file in `os.TempDir`, computes
sha256 while writing, returns an error before unpacking if the hash
doesn't match. On match, delegates to the existing `Install(tmpPath,
pluginsDir)`. The temp file is removed in `defer`. A 50 MB hard cap
is enforced with an `io.LimitReader` so a malformed catalog can't
OOM the launcher.

The existing `.rltp` unpacking code path stays single-sourced.

### HTTP endpoints

Mounted on the existing backend HTTP server, following the same
registration pattern as `/api/plugins`.

- `GET /api/plugins/updates`

  Returns the current in-memory diff. Does NOT trigger a refresh.

  ```json
  {
    "updates": [
      {
        "name": "demos2",
        "installed_version": "1.0.0",
        "latest_version": "1.1.0",
        "size_bytes": 48213
      }
    ],
    "last_checked_at": "2026-05-11T14:02:11Z",
    "last_error": null
  }
  ```

  `last_error` is the message of the most recent `Refresh` failure
  or `null`. `last_checked_at` is RFC3339 or `null` if never fetched.

- `POST /api/plugins/refresh-catalog`

  Triggers `catalog.Refresh()` and returns the same body shape as
  `/api/plugins/updates`. Used at startup and by the manual refresh
  affordance.

- `POST /api/plugins/install-update`

  Body: `{"name":"demos2"}`. Looks up the entry, calls
  `InstallFromURL`, on success invalidates the plugins manifest
  cache for that name and broadcasts a `_PluginUpdated` bus event.

  Response: `{"name":"demos2","installed_version":"1.1.0"}`.

  Errors: 4xx for unknown name, missing catalog, name not in catalog;
  5xx for download / unpack failures. Body `{"error": "<message>"}`.

"Update all" is implemented client-side as a sequence of
`install-update` calls. Sequential, not parallel: avoids concurrent
zip extractions on the plugins dir. Partial failure leaves the
already-updated plugins updated.

### Live reload of running iframes

The existing `_DevPluginReload` mechanism already invalidates a
manifest cache entry and broadcasts a name; the launcher's plugin
host reloads the matching iframe.

We extend that pattern:

- `plugins.Manager` gains `NotifyUpdated(name string)`. Mirrors
  `NotifyReload` but invalidates the installed-plugin cache entry
  (`delete(pm.cache, folderName)`) rather than the dev cache, and
  broadcasts `_PluginUpdated` with `{name, installed_version}`.
- The install handler calls `NotifyUpdated` on success.
- `overlay/src/launcher.js`'s existing `_DevPluginReload` listener is
  extended to also handle `_PluginUpdated` with the same reload
  action against overlay / dashboard / background iframes for that
  plugin.
- OBS browser-source consumers receive the same SSE event and reload
  too, by virtue of consuming the same stream.

Saved state inside an iframe is the plugin's responsibility, same as
the dev reload contract.

### Dashboard UI (`backend/internal/server/web/dashboard.html`)

Card-level pill:

- Rendered in the `card-sub` row beside `v1.0.0` and `@author`.
- HTML: `<button class="update-pill" data-role="update-one" data-name="<plugin>">↑ Update to 1.1.0</button>`.
- Distinct accent color (warm tier — orange/yellow) so it pulls the
  eye without screaming.
- Hover/focus tooltip: "Click to update."
- While an install is in flight: disabled, shows a spinner glyph +
  "Updating…".
- On success: pill is removed by the `_PluginUpdated` re-render path
  (the version number on the card updates first, then the diff drops
  the entry).

Section header:

- An `Update all (N)` button next to the existing
  `Install plugin…` button. Same warm accent.
- Hidden when N == 0.
- Click: disables itself and all per-card pills, then iterates
  `POST /api/plugins/install-update` sequentially for every entry in
  the current `updates` array. Each per-card pill swaps to "Updating…"
  as its turn comes up. On completion, re-enables and re-renders.

Catalog fetch error surface:

- A small dismissable `Couldn't check for updates` hint in the
  section header when `last_error != null` and there are no updates
  to show. Clicking it retriggers `/api/plugins/refresh-catalog`. The
  hint stays dismissed only for the current dashboard load.
- No per-card error indication. Per-card we just don't show pills.

Refresh timing:

- `dashboard.html`'s existing startup flow calls `refreshPluginList()`
  once. We add a sibling `refreshPluginUpdates()` that
  `POST`s `/api/plugins/refresh-catalog` and renders the result.
  Runs in parallel with the initial plugin fetch.
- The same `_OverridesChanged` SSE listener is extended to also
  handle `_PluginUpdated`: re-fetch `/api/plugins`, re-render, and
  drop the now-installed entry from the local updates state.
- No background timer in v1. Manual refresh available via the
  catalog-error hint and (implicitly) by reopening the launcher.

### Publishing workflow

New `.github/workflows/release-plugins.yml`. Triggered by
`workflow_dispatch`. Steps:

1. Checkout, set up Go, build `rl-toolkit`.
2. For each subdirectory `D` of `plugins/` that does not contain a
   top-level `.norelease` marker file:
   - `rl-toolkit pack plugins/$D -out dist/` → `dist/<name>-<version>.rltp`.
   - Read `plugins/$D/manifest.json` to capture `title`, `author`,
     `description` for the catalog copy.
   - Compute `sha256sum` and file size.
3. Run a new generator at `backend/cmd/gen-plugin-catalog/main.go`
   that takes `dist/` + a release base URL and writes
   `dist/plugins.json` in the schema-1 shape. Follows the pattern
   of the existing `gen-update-manifest` tool.
4. Ensure the `plugins-latest` release exists
   (`gh release create plugins-latest --notes '...' || true`).
5. `gh release upload plugins-latest dist/*.rltp dist/plugins.json --clobber`.

Skipping a plugin from the catalog: drop a `.norelease` file in its
folder. Keeps the opt-out off the manifest, so no other code path has
to be taught to ignore the field.

Catalog URL in the binary:

```go
const PluginCatalogURL =
    "https://github.com/<owner>/RLToolkit/releases/download/plugins-latest/plugins.json"
```

Build-time constant, same shape as the launcher updater URL today. A
future override (env var or `--plugin-catalog-url` flag) is out of
scope for v1.

## Data flow

Startup, happy path:

1. Backend boots, `plugins.New(dir)` runs as before.
2. Backend boots `catalog.New(PluginCatalogURL, version, pm)`.
3. Dashboard `index.html` calls `refreshPluginList()` and
   `refreshPluginUpdates()` in parallel.
4. `refreshPluginUpdates()` POSTs `/api/plugins/refresh-catalog`, the
   backend fetches `plugins.json`, diffs against installed plugins,
   returns the updates list.
5. Dashboard renders cards. Cards whose name is in the updates list
   render a pill. If N ≥ 1, the section header shows
   `Update all (N)`.

User clicks a single pill:

1. `POST /api/plugins/install-update {name}`.
2. Backend looks up the entry, downloads the `.rltp` to a temp file,
   verifies sha256, unpacks via existing `install.Install` into the
   plugins dir.
3. Backend invalidates `pm.cache[folder]`, broadcasts `_PluginUpdated`.
4. Dashboard listener re-fetches `/api/plugins`, re-renders. Card
   shows the new version; pill is gone.
5. Launcher's plugin host receives the same event and reloads the
   plugin's overlay / dashboard / background iframes.

User clicks "Update all":

- Same as the single-pill path, iterated sequentially for every entry
  in the current updates array. Each step's success drops one pill
  and decrements the N. Partial failure: failed entries keep their
  pill and a single toast says `Some updates failed`; succeeded
  entries stay updated.

## Testing

- `catalog` package: unit tests for parse / validation (bad schema,
  missing required fields, invalid sha256, `min_launcher_version`
  filter), and a fake `http.Client` for fetch behavior. Diff logic
  tested against a small `plugins.Manager` fixture.
- `install.InstallFromURL`: `httptest.Server` serving a `.rltp`
  fixture. Cases: hash match, hash mismatch (must error and not
  unpack), short read, oversize body, server 500.
- HTTP handlers: existing server-test pattern. Cases: refresh + list
  happy path, install-update for unknown name (404), install-update
  when catalog hasn't been refreshed (424).
- UI: no JS test harness in the project. Manual smoke after wire-up:
  point the launcher at a local staging catalog with `demos2@1.1.0`
  while `plugins/demos2@1.0.0` is installed, confirm the pill renders,
  click installs, version updates on the card, iframe reloads.

## Error and edge cases

- Offline at startup. Refresh fails, `Updates()` returns empty, no
  pills, the catalog-error hint appears in the section header.
  Toolkit stays usable.
- Catalog has a plugin the user doesn't have installed. Ignored.
  Updates only; discovery is out of scope.
- Installed plugin not in the catalog. No pill. No "no longer
  maintained" warning.
- Plugin has an open settings modal or active dashboard view when the
  user clicks update. Reload event fires, iframe reloads, modal
  closes / view re-renders. Iframe state is the plugin's
  responsibility.
- User on the portable Linux tarball with `plugins/` next to the
  binary. Works unchanged — we install into whatever `pluginsDir`
  the settings resolve to, the same dir the sideload path uses.
- Concurrent double-click on "Update all". Button is disabled while
  a run is in flight; per-card pills are also disabled. Re-enabled
  on completion.
- Plugin currently disabled in overrides. Install still proceeds.
  The plugin stays disabled; enabling it later picks up the new
  version. No special-casing.
