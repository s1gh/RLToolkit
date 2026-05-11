# Plugin updates from inside the launcher — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let users update installed plugins from inside the launcher's plugin grid via a per-card "Update available" pill and an "Update all" button, sourced from a curated GitHub-release-hosted JSON catalog and verified by SHA-256.

**Architecture:** A new `plugincatalog` Go package fetches `plugins.json` and diffs against installed plugins. The existing `install` package gains a URL-based entry point that downloads, verifies SHA-256, and delegates to the existing zip-extract path. Three new HTTP endpoints expose the catalog state and trigger installs. The dashboard renders pills + a bulk button; an SSE `_PluginUpdated` event drives in-place iframe reloads via the SDK's existing dev-reload mechanism. Plugin packaging is automated by a new GitHub workflow that runs `rl-toolkit pack` per plugin folder and uploads the result to a `plugins-latest` release.

**Tech Stack:** Go 1.x (backend), Vanilla JS + HTML in `dashboard.html` (UI), GitHub Actions for the publish workflow. Existing deps reused: `archive/zip`, `net/http`, the in-tree `bus`, `plugins`, `install`, `overrides` packages, and the dashboard's existing SSE listener pattern.

---

## File map

**Create:**
- `backend/internal/plugincatalog/catalog.go` — fetch, parse, validate `plugins.json`; expose `Updates()` diff against `plugins.Manager`.
- `backend/internal/plugincatalog/catalog_test.go` — unit tests.
- `backend/internal/plugincatalog/testdata/plugins.json` — fixture used by tests.
- `backend/internal/plugincatalog/semver.go` — minimal semver comparator (loose semver, two leading-`v`-tolerant parts; avoids a new third-party dep).
- `backend/internal/plugincatalog/semver_test.go`
- `backend/cmd/gen-plugin-catalog/main.go` — CI tool that walks `dist/*.rltp`, reads each archive's `manifest.json`, computes SHA-256 and size, writes `plugins.json`.
- `backend/cmd/gen-plugin-catalog/main_test.go`
- `.github/workflows/release-plugins.yml`

**Modify:**
- `backend/internal/install/install.go` — add `InstallFromURL`.
- `backend/internal/install/install_test.go` — add tests for `InstallFromURL`.
- `backend/internal/plugins/plugins.go` — add `DevNames()`, `NotifyUpdated(name)`.
- `backend/internal/plugins/plugins_test.go` — tests for the two new methods.
- `backend/internal/bus/bus.go` — add `_PluginUpdated` to the synthetic-framing allowlist (so SSE handlers without a filter on that name still receive it, same shape as the existing `_DevPluginReload` row).
- `backend/internal/server/server.go` — add `Catalog *plugincatalog.Manager` to `Deps`; mount three new routes.
- `backend/internal/server/plugin_updates.go` — new file inside the server package holding the three handlers; keeps `server.go` from growing further.
- `backend/internal/server/plugin_updates_test.go`
- `backend/internal/server/web/dashboard.html` — pill rendering, Update-all button, refresh wiring, `_PluginUpdated` listener.
- `backend/internal/server/web/sdk/src/dev-reload.js` — reuse same handler for `_PluginUpdated`.
- `backend/internal/server/web/sdk/dist/sdk.js` — rebuilt artifact (handled by existing build step).
- `backend/cmd/rl-toolkit/main.go` — instantiate `plugincatalog.Manager` and inject into `server.Deps`. Pin a build-time `version` and `pluginCatalogURL` constant.
- `backend/cmd/rl-toolkit/version.go` — new tiny file pinning `const Version` (set via `-ldflags` in the workflow). Default `"0.0.0-dev"`.

**Don't modify:**
- `backend/internal/catalog/` (event catalog — name collision avoided by using `plugincatalog`).

---

## Task 1: Add `DevNames()` to plugins.Manager

**Files:**
- Modify: `backend/internal/plugins/plugins.go`
- Test: `backend/internal/plugins/plugins_test.go`

Reason: the catalog diff needs to skip dev-registered plugins, but `plugins.Manager.dev` is unexported.

- [ ] **Step 1: Write the failing test**

Append to `backend/internal/plugins/plugins_test.go`:

```go
func TestManagerDevNames(t *testing.T) {
    dir := t.TempDir()
    pm := New(dir)
    if got := pm.DevNames(); len(got) != 0 {
        t.Fatalf("expected empty DevNames, got %v", got)
    }

    devDir := t.TempDir()
    if err := os.WriteFile(filepath.Join(devDir, "manifest.json"),
        []byte(`{"name":"foo","version":"1.0.0","overlay":{"file":"o.html","width":10,"height":10}}`),
        0o644); err != nil {
        t.Fatal(err)
    }
    if err := os.WriteFile(filepath.Join(devDir, "o.html"), []byte("<html></html>"), 0o644); err != nil {
        t.Fatal(err)
    }
    if err := pm.RegisterDev("foo", devDir); err != nil {
        t.Fatal(err)
    }
    got := pm.DevNames()
    if len(got) != 1 || got[0] != "foo" {
        t.Fatalf("DevNames() = %v, want [foo]", got)
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./backend/internal/plugins/ -run TestManagerDevNames -v`

Expected: FAIL with `pm.DevNames undefined`.

- [ ] **Step 3: Add the method**

Append to `backend/internal/plugins/plugins.go` (after `DevPath`):

```go
// DevNames returns the names of every dev-registered plugin. Order is
// unspecified; callers that need stability should sort. Used by the
// plugin catalog to exclude dev plugins from update diffs.
func (pm *Manager) DevNames() []string {
    pm.mu.Lock()
    defer pm.mu.Unlock()
    out := make([]string, 0, len(pm.dev))
    for name := range pm.dev {
        out = append(out, name)
    }
    return out
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./backend/internal/plugins/ -run TestManagerDevNames -v`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/plugins/plugins.go backend/internal/plugins/plugins_test.go
git commit -m "plugins: expose DevNames for catalog diff"
```

---

## Task 2: Add `NotifyUpdated` to plugins.Manager

**Files:**
- Modify: `backend/internal/plugins/plugins.go`
- Test: `backend/internal/plugins/plugins_test.go`

Mirrors `NotifyReload` but invalidates the *installed* (non-dev) manifest cache and broadcasts `_PluginUpdated`.

- [ ] **Step 1: Inspect the existing pattern**

Read `backend/internal/plugins/plugins.go` around `NotifyReload` (the function near the bottom of the file). Confirm that `pm.cache` is keyed by folder name and that the broadcast pattern uses `json.Marshal(map[string]string{...})`.

- [ ] **Step 2: Write the failing test**

Append to `backend/internal/plugins/plugins_test.go`:

```go
func TestManagerNotifyUpdated(t *testing.T) {
    dir := t.TempDir()
    pluginDir := filepath.Join(dir, "foo")
    if err := os.MkdirAll(pluginDir, 0o755); err != nil {
        t.Fatal(err)
    }
    if err := os.WriteFile(filepath.Join(pluginDir, "manifest.json"),
        []byte(`{"name":"foo","version":"1.0.0","overlay":{"file":"o.html","width":10,"height":10}}`),
        0o644); err != nil {
        t.Fatal(err)
    }
    if err := os.WriteFile(filepath.Join(pluginDir, "o.html"), []byte("<html></html>"), 0o644); err != nil {
        t.Fatal(err)
    }
    pm := New(dir)
    if got := pm.Get("foo"); got == nil || got.Version != "1.0.0" {
        t.Fatalf("Get foo = %v, want version 1.0.0", got)
    }

    stub := &stubBroadcaster{}
    pm.AttachBroadcaster(stub)

    // Overwrite manifest with v1.1.0 then notify. The notify must
    // invalidate the cache so the next Get returns the new version.
    if err := os.WriteFile(filepath.Join(pluginDir, "manifest.json"),
        []byte(`{"name":"foo","version":"1.1.0","overlay":{"file":"o.html","width":10,"height":10}}`),
        0o644); err != nil {
        t.Fatal(err)
    }
    pm.NotifyUpdated("foo")

    if len(stub.events) != 1 || stub.events[0].Name != "_PluginUpdated" {
        t.Fatalf("expected one _PluginUpdated event, got %+v", stub.events)
    }
    var payload struct {
        Name             string `json:"name"`
        InstalledVersion string `json:"installed_version"`
    }
    if err := json.Unmarshal(stub.events[0].Data, &payload); err != nil {
        t.Fatal(err)
    }
    if payload.Name != "foo" || payload.InstalledVersion != "1.1.0" {
        t.Fatalf("payload = %+v, want {foo 1.1.0}", payload)
    }
}
```

(The `stubBroadcaster` type already exists in `plugins_test.go` — reuse it.)

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./backend/internal/plugins/ -run TestManagerNotifyUpdated -v`

Expected: FAIL — `NotifyUpdated` undefined.

- [ ] **Step 4: Add the method**

Append to `backend/internal/plugins/plugins.go` (just after `NotifyReload`):

```go
// NotifyUpdated invalidates the installed-manifest cache for `name`
// and broadcasts `_PluginUpdated` with the post-update installed
// version. Called by the install-update HTTP handler so open
// iframes can hot-reload and the dashboard re-renders. Unlike
// NotifyReload (which targets dev-registered plugins), this clears
// the entry from pm.cache, where installed plugins live.
func (pm *Manager) NotifyUpdated(name string) {
    pm.mu.Lock()
    for folder, entry := range pm.cache {
        if entry.manifest != nil && entry.manifest.Name == name {
            delete(pm.cache, folder)
        }
    }
    b := pm.bus
    pm.mu.Unlock()

    // Re-read the manifest so the broadcast carries the new version.
    var installed string
    if m := pm.Get(name); m != nil {
        installed = m.Version
    }

    log.Printf("[plugins] Updated: %s -> %s", name, installed)
    if b != nil {
        body, err := json.Marshal(map[string]string{
            "name":              name,
            "installed_version": installed,
        })
        if err == nil {
            b.Broadcast(bus.Event{Name: "_PluginUpdated", Data: body})
        }
    }
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./backend/internal/plugins/ -run TestManagerNotifyUpdated -v`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/plugins/plugins.go backend/internal/plugins/plugins_test.go
git commit -m "plugins: NotifyUpdated invalidates cache and emits event"
```

---

## Task 3: Allow `_PluginUpdated` past the bus framing filter

**Files:**
- Modify: `backend/internal/bus/bus.go`
- Test: `backend/internal/bus/bus_test.go` (if it exists; otherwise skip the test addition — see Step 0)

`_PluginUpdated` is a "framing" event (not a plugin-observable RL event); we want it delivered to every SSE subscriber regardless of their `events=` filter, the same way `_DevPluginReload` is today.

- [ ] **Step 0: Confirm the framing list lives there**

Run: `grep -n "_DevPluginReload" backend/internal/bus/bus.go`

Expected: a line inside a literal map (around line ~122) listing framing-signal event names.

- [ ] **Step 1: Add to the framing allowlist**

Edit `backend/internal/bus/bus.go`. Locate the `framingSignals` map (it contains `"_DevPluginReload": {},` among other entries) and add a sibling line:

```go
    "_PluginUpdated":    {},
```

(Match the indentation and the `:    {},` spacing of the surrounding rows.)

- [ ] **Step 2: Verify**

Run: `go test ./backend/internal/bus/...`

Expected: PASS (no changes in test behavior; we're just enlarging the allowlist).

- [ ] **Step 3: Commit**

```bash
git add backend/internal/bus/bus.go
git commit -m "bus: pass _PluginUpdated through the framing filter"
```

---

## Task 4: Add a minimal semver comparator

**Files:**
- Create: `backend/internal/plugincatalog/semver.go`
- Create: `backend/internal/plugincatalog/semver_test.go`

We compare versions of the form `1.0.0`, `0.2.1`, `1.0.0-rc.1`. We do not need full semver; we need a stable "is A > B" answer. Pulling in a new third-party module just for this is overkill; we write ~40 lines.

- [ ] **Step 1: Write the failing test**

Create `backend/internal/plugincatalog/semver_test.go`:

```go
package plugincatalog

import "testing"

func TestCompare(t *testing.T) {
    cases := []struct {
        a, b string
        want int
    }{
        {"1.0.0", "1.0.0", 0},
        {"1.0.1", "1.0.0", 1},
        {"1.0.0", "1.0.1", -1},
        {"2.0.0", "1.99.99", 1},
        {"1.10.0", "1.9.0", 1},
        {"v1.0.0", "1.0.0", 0},
        {"1.0", "1.0.0", 0},
        {"1.0.0-rc.1", "1.0.0", -1},
        {"1.0.0", "1.0.0-rc.1", 1},
        {"1.0.0-rc.2", "1.0.0-rc.1", 1},
        {"1.0.0-rc.10", "1.0.0-rc.2", 1},
        {"garbage", "1.0.0", 0}, // unparsable falls back to equal
    }
    for _, tc := range cases {
        if got := Compare(tc.a, tc.b); got != tc.want {
            t.Errorf("Compare(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
        }
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./backend/internal/plugincatalog/ -run TestCompare -v`

Expected: FAIL — package doesn't exist.

- [ ] **Step 3: Implement**

Create `backend/internal/plugincatalog/semver.go`:

```go
// Package plugincatalog contains the loose semver comparator and the
// catalog Manager used by the launcher to decide when an installed
// plugin has a newer version available.
package plugincatalog

import (
    "strconv"
    "strings"
)

// Compare returns -1, 0, or 1 for a < b, a == b, a > b. Inputs may
// have a leading "v", a missing patch component, or a "-pre" suffix.
// Unparseable inputs compare equal so a bad version string doesn't
// flag an update.
func Compare(a, b string) int {
    aMain, aPre := splitVer(a)
    bMain, bPre := splitVer(b)
    if c := cmpInts(aMain, bMain); c != 0 {
        return c
    }
    // Pre-release < release. Per semver: 1.0.0-rc < 1.0.0.
    switch {
    case aPre == "" && bPre == "":
        return 0
    case aPre == "":
        return 1
    case bPre == "":
        return -1
    }
    return cmpPre(aPre, bPre)
}

func splitVer(v string) ([]int, string) {
    v = strings.TrimPrefix(strings.TrimSpace(v), "v")
    pre := ""
    if i := strings.Index(v, "-"); i >= 0 {
        pre = v[i+1:]
        v = v[:i]
    }
    parts := strings.Split(v, ".")
    out := make([]int, 0, 3)
    for _, p := range parts {
        n, err := strconv.Atoi(p)
        if err != nil {
            return nil, pre
        }
        out = append(out, n)
    }
    for len(out) < 3 {
        out = append(out, 0)
    }
    return out, pre
}

func cmpInts(a, b []int) int {
    if a == nil || b == nil {
        return 0
    }
    n := len(a)
    if len(b) > n {
        n = len(b)
    }
    for i := 0; i < n; i++ {
        var av, bv int
        if i < len(a) {
            av = a[i]
        }
        if i < len(b) {
            bv = b[i]
        }
        if av < bv {
            return -1
        }
        if av > bv {
            return 1
        }
    }
    return 0
}

func cmpPre(a, b string) int {
    ap, bp := strings.Split(a, "."), strings.Split(b, ".")
    n := len(ap)
    if len(bp) < n {
        n = len(bp)
    }
    for i := 0; i < n; i++ {
        an, aerr := strconv.Atoi(ap[i])
        bn, berr := strconv.Atoi(bp[i])
        if aerr == nil && berr == nil {
            if an != bn {
                if an < bn {
                    return -1
                }
                return 1
            }
            continue
        }
        if ap[i] != bp[i] {
            if ap[i] < bp[i] {
                return -1
            }
            return 1
        }
    }
    if len(ap) < len(bp) {
        return -1
    }
    if len(ap) > len(bp) {
        return 1
    }
    return 0
}
```

- [ ] **Step 4: Run test**

Run: `go test ./backend/internal/plugincatalog/ -run TestCompare -v`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/plugincatalog/semver.go backend/internal/plugincatalog/semver_test.go
git commit -m "plugincatalog: minimal semver comparator"
```

---

## Task 5: Catalog Manager — types and parse

**Files:**
- Create: `backend/internal/plugincatalog/catalog.go`
- Create: `backend/internal/plugincatalog/testdata/plugins.json`
- Modify: `backend/internal/plugincatalog/catalog_test.go` (created in this task)

Defines `Entry`, `Update`, `Manager`, and the parse / validate path. No HTTP yet — we test parsing against a fixture.

- [ ] **Step 1: Create the fixture**

Create `backend/internal/plugincatalog/testdata/plugins.json`:

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
      "description": "Demos demo.",
      "url": "https://example.com/demos2-1.1.0.rltp",
      "sha256": "0000000000000000000000000000000000000000000000000000000000000001",
      "size_bytes": 1234
    },
    {
      "name": "needs-newer",
      "version": "2.0.0",
      "title": "Needs Newer",
      "author": "x",
      "url": "https://example.com/needs-newer-2.0.0.rltp",
      "sha256": "0000000000000000000000000000000000000000000000000000000000000002",
      "size_bytes": 100,
      "min_launcher_version": "99.0.0"
    },
    {
      "name": "bad-hash",
      "version": "1.0.0",
      "url": "https://example.com/bad.rltp",
      "sha256": "not-hex"
    }
  ]
}
```

- [ ] **Step 2: Write the failing test**

Create `backend/internal/plugincatalog/catalog_test.go`:

```go
package plugincatalog

import (
    "context"
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "os"
    "testing"
)

func TestParseFixtureFiltersInvalidAndMinVersion(t *testing.T) {
    raw, err := os.ReadFile("testdata/plugins.json")
    if err != nil {
        t.Fatal(err)
    }
    entries, err := parseCatalog(raw, "1.0.0")
    if err != nil {
        t.Fatal(err)
    }
    if len(entries) != 1 || entries[0].Name != "demos2" {
        t.Fatalf("got %+v, want only demos2", entries)
    }
}

func TestParseRejectsWrongSchema(t *testing.T) {
    raw, _ := json.Marshal(map[string]any{"schema": 2, "plugins": []any{}})
    if _, err := parseCatalog(raw, "1.0.0"); err == nil {
        t.Fatal("expected error for schema != 1")
    }
}

func TestRefreshHTTP(t *testing.T) {
    body, _ := os.ReadFile("testdata/plugins.json")
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "application/json")
        _, _ = w.Write(body)
    }))
    defer srv.Close()

    m := New(srv.URL, "1.0.0", nil)
    if err := m.Refresh(context.Background()); err != nil {
        t.Fatal(err)
    }
    if _, ok := m.Find("demos2"); !ok {
        t.Fatal("expected demos2 in catalog")
    }
    if _, ok := m.Find("needs-newer"); ok {
        t.Fatal("entry above launcher version should be filtered")
    }
}
```

- [ ] **Step 3: Run test**

Run: `go test ./backend/internal/plugincatalog/ -v`

Expected: FAIL — `parseCatalog`, `New`, `Manager` not defined.

- [ ] **Step 4: Implement**

Create `backend/internal/plugincatalog/catalog.go`:

```go
package plugincatalog

import (
    "context"
    "encoding/hex"
    "encoding/json"
    "errors"
    "fmt"
    "io"
    "log"
    "net/http"
    "net/url"
    "strings"
    "sync"
    "time"
)

// Entry mirrors one element of plugins.json. Fields that are
// optional in the catalog are tolerated as zero values.
type Entry struct {
    Name               string
    Version            string
    Title              string
    Author             string
    Description        string
    URL                string
    SHA256             string
    SizeBytes          int64
    MinLauncherVersion string
}

// pluginsLister is the read surface we need from plugins.Manager.
// Declared narrowly so tests can pass nil or a stub.
type pluginsLister interface {
    DevNames() []string
}

// pluginsListerWithGet adds the per-name accessor needed for the
// installed-version diff. Real plugins.Manager satisfies this.
type pluginsListerWithGet interface {
    pluginsLister
    Get(name string) interface {
        // matches plugins.Manifest fields we need; we use a struct
        // type assertion instead to keep this package free of import
        // cycles. See Updates().
    }
}

// Update is one row in the dashboard's "updates available" diff.
type Update struct {
    Name             string
    InstalledVersion string
    LatestVersion    string
    Entry            Entry
}

// Manager owns the catalog cache. Refresh is the only mutating call;
// every read is lock-protected.
type Manager struct {
    url             string
    launcherVersion string
    plugins         InstalledLookup
    client          *http.Client

    mu         sync.Mutex
    entries    []Entry
    byName     map[string]Entry
    lastCheck  time.Time
    lastErr    error
}

// InstalledLookup is the subset of plugins.Manager the catalog reads
// from. Defined here to avoid an import cycle and to keep tests
// trivial. Real plugins.Manager satisfies this interface.
type InstalledLookup interface {
    DevNames() []string
    InstalledVersions() map[string]string
}

// New returns a Manager configured to fetch from `url`. `pm` may be
// nil; Updates() then returns an empty slice.
func New(url, launcherVersion string, pm InstalledLookup) *Manager {
    return &Manager{
        url:             url,
        launcherVersion: launcherVersion,
        plugins:         pm,
        client:          &http.Client{Timeout: 10 * time.Second},
    }
}

// Refresh fetches and replaces the in-memory catalog. On error the
// previous catalog is kept; LastError surfaces the most recent failure.
func (m *Manager) Refresh(ctx context.Context) error {
    req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.url, nil)
    if err != nil {
        m.recordErr(err)
        return err
    }
    resp, err := m.client.Do(req)
    if err != nil {
        m.recordErr(err)
        return err
    }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        e := fmt.Errorf("catalog: HTTP %d", resp.StatusCode)
        m.recordErr(e)
        return e
    }
    body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MiB
    if err != nil {
        m.recordErr(err)
        return err
    }
    entries, err := parseCatalog(body, m.launcherVersion)
    if err != nil {
        m.recordErr(err)
        return err
    }
    byName := make(map[string]Entry, len(entries))
    for _, e := range entries {
        byName[e.Name] = e
    }
    m.mu.Lock()
    m.entries = entries
    m.byName = byName
    m.lastCheck = time.Now()
    m.lastErr = nil
    m.mu.Unlock()
    return nil
}

// Find returns the catalog entry for `name`, if present.
func (m *Manager) Find(name string) (Entry, bool) {
    m.mu.Lock()
    defer m.mu.Unlock()
    e, ok := m.byName[name]
    return e, ok
}

// LastChecked is the time the catalog was last successfully fetched,
// zero if never.
func (m *Manager) LastChecked() time.Time {
    m.mu.Lock()
    defer m.mu.Unlock()
    return m.lastCheck
}

// LastError is the most recent Refresh error, nil if the last
// Refresh succeeded or none ran.
func (m *Manager) LastError() error {
    m.mu.Lock()
    defer m.mu.Unlock()
    return m.lastErr
}

// Updates diffs the catalog against installed plugins. Dev-registered
// plugins are skipped. Returns an empty slice if pm is nil or the
// catalog hasn't been refreshed.
func (m *Manager) Updates() []Update {
    if m.plugins == nil {
        return nil
    }
    installed := m.plugins.InstalledVersions()
    devSet := make(map[string]struct{})
    for _, n := range m.plugins.DevNames() {
        devSet[n] = struct{}{}
    }
    m.mu.Lock()
    entries := m.entries
    m.mu.Unlock()

    out := make([]Update, 0)
    for _, e := range entries {
        if _, isDev := devSet[e.Name]; isDev {
            continue
        }
        cur, ok := installed[e.Name]
        if !ok {
            continue
        }
        if Compare(e.Version, cur) > 0 {
            out = append(out, Update{
                Name:             e.Name,
                InstalledVersion: cur,
                LatestVersion:    e.Version,
                Entry:            e,
            })
        }
    }
    return out
}

func (m *Manager) recordErr(err error) {
    m.mu.Lock()
    m.lastErr = err
    m.mu.Unlock()
}

// parseCatalog validates the on-wire shape and drops entries that
// fail per-row validation. Returns an error only for top-level
// problems (bad JSON, wrong schema).
func parseCatalog(raw []byte, launcherVersion string) ([]Entry, error) {
    var doc struct {
        Schema  int             `json:"schema"`
        Plugins []rawEntry      `json:"plugins"`
    }
    if err := json.Unmarshal(raw, &doc); err != nil {
        return nil, fmt.Errorf("catalog: parse: %w", err)
    }
    if doc.Schema != 1 {
        return nil, fmt.Errorf("catalog: unsupported schema %d", doc.Schema)
    }
    out := make([]Entry, 0, len(doc.Plugins))
    for _, r := range doc.Plugins {
        e, err := r.toEntry()
        if err != nil {
            log.Printf("[plugincatalog] dropped %q: %v", r.Name, err)
            continue
        }
        if e.MinLauncherVersion != "" && Compare(launcherVersion, e.MinLauncherVersion) < 0 {
            log.Printf("[plugincatalog] dropped %s@%s: launcher %s < min %s",
                e.Name, e.Version, launcherVersion, e.MinLauncherVersion)
            continue
        }
        out = append(out, e)
    }
    return out, nil
}

type rawEntry struct {
    Name               string `json:"name"`
    Version            string `json:"version"`
    Title              string `json:"title"`
    Author             string `json:"author"`
    Description        string `json:"description"`
    URL                string `json:"url"`
    SHA256             string `json:"sha256"`
    SizeBytes          int64  `json:"size_bytes"`
    MinLauncherVersion string `json:"min_launcher_version"`
}

func (r rawEntry) toEntry() (Entry, error) {
    if strings.TrimSpace(r.Name) == "" {
        return Entry{}, errors.New("missing name")
    }
    if strings.TrimSpace(r.Version) == "" {
        return Entry{}, errors.New("missing version")
    }
    if !isValidHTTPSURL(r.URL) {
        return Entry{}, fmt.Errorf("invalid url %q", r.URL)
    }
    if !isValidSHA256(r.SHA256) {
        return Entry{}, fmt.Errorf("invalid sha256")
    }
    return Entry{
        Name: r.Name, Version: r.Version, Title: r.Title, Author: r.Author,
        Description: r.Description, URL: r.URL, SHA256: strings.ToLower(r.SHA256),
        SizeBytes: r.SizeBytes, MinLauncherVersion: r.MinLauncherVersion,
    }, nil
}

func isValidHTTPSURL(s string) bool {
    u, err := url.Parse(s)
    if err != nil || u.Scheme != "https" || u.Host == "" {
        return false
    }
    return true
}

func isValidSHA256(s string) bool {
    if len(s) != 64 {
        return false
    }
    _, err := hex.DecodeString(s)
    return err == nil
}
```

Note: the `pluginsLister` / `pluginsListerWithGet` types in the test skeleton above are scaffolding from an earlier draft — they are NOT used in the final code. Delete those lines from the implementation; the real interface is `InstalledLookup`.

- [ ] **Step 5: Run test**

Run: `go test ./backend/internal/plugincatalog/ -v`

Expected: PASS for all three tests.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/plugincatalog/
git commit -m "plugincatalog: parse, validate, fetch plugins.json"
```

---

## Task 6: Add `InstalledVersions()` to plugins.Manager

**Files:**
- Modify: `backend/internal/plugins/plugins.go`
- Test: `backend/internal/plugins/plugins_test.go`

The catalog's `Updates()` calls `pm.InstalledVersions()` (defined in Task 5's `InstalledLookup` interface). We add it.

- [ ] **Step 1: Write the failing test**

Append to `backend/internal/plugins/plugins_test.go`:

```go
func TestManagerInstalledVersions(t *testing.T) {
    dir := t.TempDir()
    pluginDir := filepath.Join(dir, "foo")
    if err := os.MkdirAll(pluginDir, 0o755); err != nil {
        t.Fatal(err)
    }
    if err := os.WriteFile(filepath.Join(pluginDir, "manifest.json"),
        []byte(`{"name":"foo","version":"1.2.3","overlay":{"file":"o.html","width":10,"height":10}}`),
        0o644); err != nil {
        t.Fatal(err)
    }
    if err := os.WriteFile(filepath.Join(pluginDir, "o.html"), []byte("<html></html>"), 0o644); err != nil {
        t.Fatal(err)
    }
    pm := New(dir)
    got := pm.InstalledVersions()
    if v, ok := got["foo"]; !ok || v != "1.2.3" {
        t.Fatalf("InstalledVersions() = %v, want foo=1.2.3", got)
    }
}
```

- [ ] **Step 2: Run test (expect FAIL)**

Run: `go test ./backend/internal/plugins/ -run TestManagerInstalledVersions -v`

Expected: FAIL — `InstalledVersions` undefined.

- [ ] **Step 3: Implement**

Append to `backend/internal/plugins/plugins.go` (after `DevNames`):

```go
// InstalledVersions returns name → version for every installed
// plugin (excluding dev-registered ones). Used by the plugin catalog
// to compute updates available.
func (pm *Manager) InstalledVersions() map[string]string {
    list := pm.List()
    pm.mu.Lock()
    devSet := make(map[string]struct{}, len(pm.dev))
    for n := range pm.dev {
        devSet[n] = struct{}{}
    }
    pm.mu.Unlock()
    out := make(map[string]string, len(list))
    for _, m := range list {
        if m == nil {
            continue
        }
        if _, isDev := devSet[m.Name]; isDev {
            continue
        }
        out[m.Name] = m.Version
    }
    return out
}
```

- [ ] **Step 4: Run test**

Run: `go test ./backend/internal/plugins/ -run TestManagerInstalledVersions -v`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/plugins/plugins.go backend/internal/plugins/plugins_test.go
git commit -m "plugins: expose InstalledVersions for catalog diff"
```

---

## Task 7: `install.InstallFromURL`

**Files:**
- Modify: `backend/internal/install/install.go`
- Modify: `backend/internal/install/install_test.go`

Adds the URL-based entry point. Reuses the existing `Install` for unpack.

- [ ] **Step 1: Inspect existing tests**

Read the first ~40 lines of `backend/internal/install/install_test.go` to identify the fixture-creation pattern used by existing tests (look for `makeArchive` or similar — it'll build a `.rltp` zip in a `t.TempDir()`).

- [ ] **Step 2: Write the failing test**

Append to `backend/internal/install/install_test.go`:

```go
func TestInstallFromURLHappyPath(t *testing.T) {
    archive := makeTestArchive(t, "foo", "1.0.0")
    archiveBytes, err := os.ReadFile(archive)
    if err != nil {
        t.Fatal(err)
    }
    sum := sha256.Sum256(archiveBytes)

    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
        _, _ = w.Write(archiveBytes)
    }))
    defer srv.Close()

    pluginsDir := t.TempDir()
    name, err := InstallFromURL(context.Background(), srv.URL,
        hex.EncodeToString(sum[:]), pluginsDir)
    if err != nil {
        t.Fatal(err)
    }
    if name != "foo" {
        t.Fatalf("name = %q, want foo", name)
    }
    if _, err := os.Stat(filepath.Join(pluginsDir, "foo", "manifest.json")); err != nil {
        t.Fatalf("manifest missing: %v", err)
    }
}

func TestInstallFromURLHashMismatch(t *testing.T) {
    archive := makeTestArchive(t, "foo", "1.0.0")
    archiveBytes, _ := os.ReadFile(archive)
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
        _, _ = w.Write(archiveBytes)
    }))
    defer srv.Close()
    pluginsDir := t.TempDir()
    _, err := InstallFromURL(context.Background(), srv.URL,
        "0000000000000000000000000000000000000000000000000000000000000000",
        pluginsDir)
    if err == nil {
        t.Fatal("expected hash mismatch error")
    }
    if _, err := os.Stat(filepath.Join(pluginsDir, "foo")); !os.IsNotExist(err) {
        t.Fatalf("plugin folder should not exist after hash mismatch: %v", err)
    }
}

// makeTestArchive helper: if a helper of this exact name already
// exists in install_test.go, REMOVE the helper below. Otherwise add
// it. The helper builds a minimal valid .rltp under t.TempDir() and
// returns its path. Reads the file to bytes elsewhere.
func makeTestArchive(t *testing.T, name, version string) string {
    t.Helper()
    dir := t.TempDir()
    path := filepath.Join(dir, name+".rltp")
    out, err := os.Create(path)
    if err != nil {
        t.Fatal(err)
    }
    defer out.Close()
    zw := zip.NewWriter(out)
    manifest, _ := zw.Create("manifest.json")
    _, _ = manifest.Write([]byte(`{"name":"` + name + `","version":"` + version +
        `","overlay":{"file":"o.html","width":10,"height":10}}`))
    overlay, _ := zw.Create("o.html")
    _, _ = overlay.Write([]byte("<html></html>"))
    if err := zw.Close(); err != nil {
        t.Fatal(err)
    }
    return path
}
```

Required imports for the test file (add if missing): `archive/zip`, `context`, `crypto/sha256`, `encoding/hex`, `net/http`, `net/http/httptest`, `os`, `path/filepath`, `testing`.

- [ ] **Step 3: Run test (expect FAIL)**

Run: `go test ./backend/internal/install/ -run TestInstallFromURL -v`

Expected: FAIL — `InstallFromURL` not defined.

- [ ] **Step 4: Implement**

Add to `backend/internal/install/install.go` (at the bottom, after `extractEntry`):

```go
// MaxArchiveBytes caps the .rltp download size to refuse pathological
// catalogs without OOM. 50 MiB is generous for HTML/JS plugins.
const MaxArchiveBytes = 50 << 20

// InstallFromURL downloads a .rltp from `url`, verifies SHA-256
// against `expectedSHA256` (lowercase hex), and unpacks it into
// pluginsDir/<name>/. Returns the unpacked plugin name on success.
//
// The download is streamed to a temp file so we never hold the whole
// archive in memory. Hash mismatches return without unpacking; the
// temp file is removed in either case.
func InstallFromURL(ctx context.Context, url, expectedSHA256, pluginsDir string) (string, error) {
    if !strings.EqualFold(expectedSHA256, expectedSHA256) || len(expectedSHA256) != 64 {
        return "", fmt.Errorf("install: expected sha256 must be 64 hex chars")
    }
    req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
    if err != nil {
        return "", fmt.Errorf("install: build request: %w", err)
    }
    client := &http.Client{Timeout: 5 * time.Minute}
    resp, err := client.Do(req)
    if err != nil {
        return "", fmt.Errorf("install: download: %w", err)
    }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        return "", fmt.Errorf("install: HTTP %d", resp.StatusCode)
    }

    tmp, err := os.CreateTemp("", "rltp-*.zip")
    if err != nil {
        return "", fmt.Errorf("install: temp file: %w", err)
    }
    tmpPath := tmp.Name()
    defer os.Remove(tmpPath)

    h := sha256.New()
    limited := io.LimitReader(resp.Body, MaxArchiveBytes+1)
    n, err := io.Copy(io.MultiWriter(tmp, h), limited)
    closeErr := tmp.Close()
    if err != nil {
        return "", fmt.Errorf("install: write temp: %w", err)
    }
    if closeErr != nil {
        return "", fmt.Errorf("install: close temp: %w", closeErr)
    }
    if n > MaxArchiveBytes {
        return "", fmt.Errorf("install: archive exceeds %d bytes", MaxArchiveBytes)
    }
    got := hex.EncodeToString(h.Sum(nil))
    if !strings.EqualFold(got, expectedSHA256) {
        return "", fmt.Errorf("install: sha256 mismatch (want %s, got %s)", expectedSHA256, got)
    }
    return Install(tmpPath, pluginsDir)
}
```

Required imports for `install.go` (add if missing): `context`, `crypto/sha256`, `encoding/hex`, `net/http`, `time`. `os`, `fmt`, `strings`, `io` should already be there.

Also: delete the bogus `strings.EqualFold(expectedSHA256, expectedSHA256)` self-check (that was a typo). Replace those four lines with:

```go
    if len(expectedSHA256) != 64 {
        return "", fmt.Errorf("install: expected sha256 must be 64 hex chars")
    }
```

- [ ] **Step 5: Run test**

Run: `go test ./backend/internal/install/ -v`

Expected: all tests PASS, including the two new ones.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/install/install.go backend/internal/install/install_test.go
git commit -m "install: add InstallFromURL with SHA-256 verification"
```

---

## Task 8: HTTP handlers — refresh + list

**Files:**
- Create: `backend/internal/server/plugin_updates.go`
- Create: `backend/internal/server/plugin_updates_test.go`
- Modify: `backend/internal/server/server.go` (`Deps` struct + routes)

- [ ] **Step 1: Add Catalog to Deps and wire routes**

Edit `backend/internal/server/server.go`:

1. Add `Catalog *plugincatalog.Manager` to the `Deps` struct (after `Identity`).

2. Below the existing `/api/plugins/` route registration in `Routes()`, add three new routes:

```go
    mux.HandleFunc("/api/plugins/updates", s.handlePluginUpdates)
    mux.HandleFunc("/api/plugins/refresh-catalog", s.handleRefreshCatalog)
    mux.HandleFunc("/api/plugins/install-update", s.handleInstallUpdate)
```

3. Add the import `"rl-toolkit/backend/internal/plugincatalog"` to the import block.

- [ ] **Step 2: Write the failing handler tests**

Create `backend/internal/server/plugin_updates_test.go`:

```go
package server

import (
    "context"
    "encoding/json"
    "io"
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"

    "rl-toolkit/backend/internal/plugincatalog"
    "rl-toolkit/backend/internal/plugins"
)

func TestUpdatesEndpointEmptyWhenCatalogNotRefreshed(t *testing.T) {
    pluginsDir := t.TempDir()
    pm := plugins.New(pluginsDir)
    cat := plugincatalog.New("http://127.0.0.1:0", "1.0.0", pm)
    s := New(Deps{Plugins: pm, Catalog: cat, PluginDir: pluginsDir})

    ts := httptest.NewServer(s.Routes())
    defer ts.Close()

    resp, err := http.Get(ts.URL + "/api/plugins/updates")
    if err != nil {
        t.Fatal(err)
    }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        t.Fatalf("status = %d", resp.StatusCode)
    }
    var body struct {
        Updates []any   `json:"updates"`
        LastErr *string `json:"last_error"`
    }
    if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
        t.Fatal(err)
    }
    if len(body.Updates) != 0 {
        t.Fatalf("expected empty updates, got %d", len(body.Updates))
    }
}

func TestRefreshCatalogEndpointRoundtrip(t *testing.T) {
    catalogBody := `{"schema":1,"plugins":[{"name":"demos2","version":"1.1.0","url":"https://example.com/x.rltp","sha256":"0000000000000000000000000000000000000000000000000000000000000001"}]}`
    catSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
        _, _ = io.WriteString(w, catalogBody)
    }))
    defer catSrv.Close()

    pluginsDir := t.TempDir()
    pm := plugins.New(pluginsDir)
    cat := plugincatalog.New(catSrv.URL, "1.0.0", pm)
    s := New(Deps{Plugins: pm, Catalog: cat, PluginDir: pluginsDir})

    ts := httptest.NewServer(s.Routes())
    defer ts.Close()

    resp, err := http.Post(ts.URL+"/api/plugins/refresh-catalog", "", nil)
    if err != nil {
        t.Fatal(err)
    }
    resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        t.Fatalf("refresh status = %d", resp.StatusCode)
    }
    if _, ok := cat.Find("demos2"); !ok {
        t.Fatal("expected demos2 in catalog after refresh")
    }
    _ = context.Background()
    _ = strings.TrimSpace
}
```

- [ ] **Step 3: Run test (expect FAIL — handlers not defined)**

Run: `go test ./backend/internal/server/ -run TestUpdatesEndpoint -v`
Run: `go test ./backend/internal/server/ -run TestRefreshCatalog -v`

Both expected: FAIL.

- [ ] **Step 4: Implement handlers**

Create `backend/internal/server/plugin_updates.go`:

```go
package server

import (
    "context"
    "encoding/json"
    "errors"
    "net/http"
    "time"

    "rl-toolkit/backend/internal/install"
)

// updatesResponse is the shape returned by both /api/plugins/updates
// and /api/plugins/refresh-catalog.
type updatesResponse struct {
    Updates       []updateRow `json:"updates"`
    LastCheckedAt *string     `json:"last_checked_at"`
    LastError     *string     `json:"last_error"`
}

type updateRow struct {
    Name             string `json:"name"`
    InstalledVersion string `json:"installed_version"`
    LatestVersion    string `json:"latest_version"`
    SizeBytes        int64  `json:"size_bytes"`
}

func (s *Server) buildUpdatesResponse() updatesResponse {
    if s.deps.Catalog == nil {
        return updatesResponse{Updates: []updateRow{}}
    }
    rows := make([]updateRow, 0)
    for _, u := range s.deps.Catalog.Updates() {
        rows = append(rows, updateRow{
            Name:             u.Name,
            InstalledVersion: u.InstalledVersion,
            LatestVersion:    u.LatestVersion,
            SizeBytes:        u.Entry.SizeBytes,
        })
    }
    var lastChecked *string
    if t := s.deps.Catalog.LastChecked(); !t.IsZero() {
        f := t.UTC().Format(time.RFC3339)
        lastChecked = &f
    }
    var lastErr *string
    if e := s.deps.Catalog.LastError(); e != nil {
        msg := e.Error()
        lastErr = &msg
    }
    return updatesResponse{Updates: rows, LastCheckedAt: lastChecked, LastError: lastErr}
}

func (s *Server) handlePluginUpdates(w http.ResponseWriter, _ *http.Request) {
    writeJSON(w, s.buildUpdatesResponse())
}

func (s *Server) handleRefreshCatalog(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
        return
    }
    if s.deps.Catalog == nil {
        writeJSON(w, updatesResponse{Updates: []updateRow{}})
        return
    }
    ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
    defer cancel()
    _ = s.deps.Catalog.Refresh(ctx) // error surfaced via LastError
    writeJSON(w, s.buildUpdatesResponse())
}

func (s *Server) handleInstallUpdate(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
        return
    }
    if s.deps.Catalog == nil || s.deps.Plugins == nil {
        writeJSONStatus(w, http.StatusFailedDependency,
            map[string]string{"error": "catalog or plugins not configured"})
        return
    }
    var body struct {
        Name string `json:"name"`
    }
    if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
        writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "missing name"})
        return
    }
    entry, ok := s.deps.Catalog.Find(body.Name)
    if !ok {
        writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": "unknown plugin"})
        return
    }
    ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
    defer cancel()
    if _, err := install.InstallFromURL(ctx, entry.URL, entry.SHA256, s.deps.PluginDir); err != nil {
        writeJSONStatus(w, http.StatusInternalServerError,
            map[string]string{"error": err.Error()})
        return
    }
    s.deps.Plugins.NotifyUpdated(body.Name)
    installed := body.Name
    if m := s.deps.Plugins.Get(body.Name); m != nil {
        writeJSON(w, map[string]string{
            "name":              installed,
            "installed_version": m.Version,
        })
        return
    }
    writeJSON(w, map[string]string{"name": installed})
}

// writeJSONStatus is a small companion to writeJSON for non-200
// responses.
func writeJSONStatus(w http.ResponseWriter, status int, v any) {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(status)
    _ = json.NewEncoder(w).Encode(v)
}

// Errorless ensures the import "errors" is used; remove if not.
var _ = errors.New
```

(Delete the trailing `errors.New` line if your linter complains about unused imports — it's only there because earlier drafts used `errors.Is`. Drop the `"errors"` import too if you remove that line.)

- [ ] **Step 5: Run tests**

Run: `go test ./backend/internal/server/ -run TestUpdatesEndpoint -v`
Run: `go test ./backend/internal/server/ -run TestRefreshCatalog -v`

Both expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/server/plugin_updates.go backend/internal/server/plugin_updates_test.go backend/internal/server/server.go
git commit -m "server: plugin update endpoints (updates, refresh, install-update)"
```

---

## Task 9: Handler test for install-update happy path + 404

**Files:**
- Modify: `backend/internal/server/plugin_updates_test.go`

We didn't write the install-update tests in Task 8 to keep that task scoped. Add them now.

- [ ] **Step 1: Append the tests**

Append to `backend/internal/server/plugin_updates_test.go`:

```go
func TestInstallUpdateUnknownPlugin(t *testing.T) {
    pluginsDir := t.TempDir()
    pm := plugins.New(pluginsDir)
    cat := plugincatalog.New("http://127.0.0.1:0", "1.0.0", pm)
    s := New(Deps{Plugins: pm, Catalog: cat, PluginDir: pluginsDir})
    ts := httptest.NewServer(s.Routes())
    defer ts.Close()

    resp, err := http.Post(ts.URL+"/api/plugins/install-update",
        "application/json", strings.NewReader(`{"name":"nope"}`))
    if err != nil {
        t.Fatal(err)
    }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusNotFound {
        t.Fatalf("status = %d, want 404", resp.StatusCode)
    }
}
```

(`strings` is already imported in this file thanks to the `_ = strings.TrimSpace` line; if you cleaned that up, re-add the import.)

The happy-path install-update is exercised end-to-end by the manual smoke test in Task 13. A pure-unit happy-path would re-implement the archive fixture from Task 7's `install_test.go`; the install package already covers that path.

- [ ] **Step 2: Run**

Run: `go test ./backend/internal/server/ -run TestInstallUpdate -v`

Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add backend/internal/server/plugin_updates_test.go
git commit -m "server: test install-update 404 path"
```

---

## Task 10: Wire the catalog into the binary

**Files:**
- Create: `backend/cmd/rl-toolkit/version.go`
- Modify: `backend/cmd/rl-toolkit/main.go`

- [ ] **Step 1: Create the version file**

Create `backend/cmd/rl-toolkit/version.go`:

```go
package main

// Version is the rl-toolkit build version. Overridden at link time
// via:
//   go build -ldflags="-X main.Version=0.3.0" ./backend/cmd/rl-toolkit
//
// The launcher reads this through /api/status (extended in this
// change) so the plugin catalog can filter entries whose
// min_launcher_version exceeds the running launcher.
var Version = "0.0.0-dev"

// PluginCatalogURL is the on-network location of plugins.json. It's
// a const so a corrupted user setting can't redirect plugin updates
// to a malicious host. Override at build time only.
var PluginCatalogURL = "https://github.com/s1gh/RLToolkit/releases/download/plugins-latest/plugins.json"
```

(Adjust the `s1gh/RLToolkit` path if your repo owner differs — match what `release-linux.yml` uses today.)

- [ ] **Step 2: Wire the catalog Manager**

Edit `backend/cmd/rl-toolkit/main.go`. After the line `pm.AttachBroadcaster(eventBus)` (around line 140), insert:

```go
    pluginCatalog := plugincatalog.New(PluginCatalogURL, Version, pm)
    // Initial refresh runs asynchronously so a slow GitHub doesn't
    // delay startup. The dashboard re-fetches on first paint anyway.
    go func() {
        ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
        defer cancel()
        if err := pluginCatalog.Refresh(ctx); err != nil {
            log.Printf("[plugincatalog] initial refresh: %v", err)
        }
    }()
```

Add the import `"rl-toolkit/backend/internal/plugincatalog"` to the import block.

Then locate the existing `server.New(server.Deps{...})` call and add the field:

```go
        Catalog: pluginCatalog,
```

(Place it in the struct literal alongside the other `Plugins:` / `Identity:` lines.)

- [ ] **Step 3: Build**

Run: `go build ./backend/cmd/rl-toolkit`

Expected: no errors.

Run: `go test ./...`

Expected: all PASS.

- [ ] **Step 4: Commit**

```bash
git add backend/cmd/rl-toolkit/version.go backend/cmd/rl-toolkit/main.go
git commit -m "rl-toolkit: instantiate plugin catalog manager at startup"
```

---

## Task 11: Dashboard UI — refresh wiring and pill rendering

**Files:**
- Modify: `backend/internal/server/web/dashboard.html`

We render the pill in each card's `card-sub` row and the bulk button next to "Install plugin…". Refresh is triggered on startup and re-rendered on `_PluginUpdated`.

- [ ] **Step 1: Locate the existing plugins state and render code**

Read `backend/internal/server/web/dashboard.html` between lines ~770 and ~1010 to refresh your context on: `plugins`, `overrides`, `refreshPluginList`, `renderPlugins`, and the click delegation block beneath it.

- [ ] **Step 2: Add CSS for the pill and bulk button**

Locate the existing `.card-sub` rule in the `<style>` block (search for `card-sub`). Add the following block immediately after it:

```css
.update-pill{
  display:inline-flex; align-items:center; gap:.25em;
  margin-left:.5em; padding:.1em .55em;
  font-size:11px; font-weight:600; line-height:1.4;
  color:#1d1300; background:#ffc857;
  border:none; border-radius:999px; cursor:pointer;
  transition:filter .12s;
}
.update-pill:hover{ filter:brightness(1.07); }
.update-pill:disabled{ opacity:.6; cursor:default; }
.update-pill[data-busy="1"]::before{
  content:""; width:.7em; height:.7em; border-radius:50%;
  border:2px solid #1d1300; border-top-color:transparent;
  display:inline-block; animation:upd-spin .8s linear infinite;
}
@keyframes upd-spin{ to{ transform:rotate(360deg); } }
.btn.update-all{ background:#ffc857; color:#1d1300; border-color:#ffc857; }
.btn.update-all:hover{ filter:brightness(1.07); }
.section-head-right .catalog-error{
  font-size:11px; color:var(--txt-3); cursor:pointer; user-select:none;
}
```

- [ ] **Step 3: Add the Update-all button and error hint to the section header**

Locate the `<div class="section-head-right">` block (search for `install-plugin-btn`). Replace the entire `<div class="section-head-right">…</div>` block with:

```html
      <div class="section-head-right">
        <span id="catalog-error" class="catalog-error" hidden>Couldn’t check for updates · retry</span>
        <button class="btn update-all" type="button" id="update-all-btn" hidden>Update all</button>
        <button class="btn" type="button" id="install-plugin-btn">Install plugin…</button>
        <input type="file" id="install-plugin-input" accept=".rltp" hidden />
      </div>
```

- [ ] **Step 4: Add the updates state and renderer**

Right below the existing `let pluginsFp = '';` declaration (around line 790), insert:

```javascript
let pluginUpdates = []; // [{name, installed_version, latest_version, size_bytes}]
let catalogError = null;

async function refreshPluginUpdates({ trigger } = {}) {
  try {
    const url = trigger === 'startup' || trigger === 'manual'
      ? '/api/plugins/refresh-catalog'
      : '/api/plugins/updates';
    const opts = trigger === 'startup' || trigger === 'manual' ? { method: 'POST' } : undefined;
    const r = await fetch(url, opts);
    const body = await r.json().catch(() => ({}));
    pluginUpdates = Array.isArray(body.updates) ? body.updates : [];
    catalogError = body.last_error || null;
  } catch (err) {
    pluginUpdates = [];
    catalogError = String(err && err.message || err);
  }
  renderUpdateHeader();
  renderPlugins();
}

function renderUpdateHeader() {
  const btn = document.getElementById('update-all-btn');
  const err = document.getElementById('catalog-error');
  if (pluginUpdates.length > 0) {
    btn.hidden = false;
    btn.textContent = 'Update all (' + pluginUpdates.length + ')';
  } else {
    btn.hidden = true;
  }
  err.hidden = !(catalogError && pluginUpdates.length === 0);
}

function updateForName(name) {
  return pluginUpdates.find(u => u.name === name) || null;
}
```

- [ ] **Step 5: Inject the pill into `renderPlugins`**

In `renderPlugins`, find the line that builds the `card-sub` div:

```javascript
          + '<div class="card-sub">'
            + (p.version ? '<span class="v">v' + esc(p.version) + '</span>' : '')
            + (p.author ? '<span class="author">@' + esc(p.author) + '</span>' : '')
          + '</div>'
```

Replace it with:

```javascript
          + '<div class="card-sub">'
            + (p.version ? '<span class="v">v' + esc(p.version) + '</span>' : '')
            + (p.author ? '<span class="author">@' + esc(p.author) + '</span>' : '')
            + (updateForName(p.name)
                ? '<button class="update-pill" type="button" data-role="update-one" data-name="' + esc(p.name) + '" title="Click to update">↑ Update to ' + esc(updateForName(p.name).latest_version) + '</button>'
                : '')
          + '</div>'
```

- [ ] **Step 6: Wire the click handlers**

Locate the existing event-delegation block on `#pl` (look for `document.getElementById('pl').addEventListener('click'`). After the existing handlers, add a branch for the pill (still inside the same delegated listener):

```javascript
  const updPill = ev.target.closest('[data-role="update-one"]');
  if (updPill) {
    ev.preventDefault();
    runUpdateOne(updPill.dataset.name, updPill).catch(() => {});
    return;
  }
```

Then add these two functions just below the listener:

```javascript
async function runUpdateOne(name, pillEl) {
  if (!name) return;
  if (pillEl) { pillEl.disabled = true; pillEl.dataset.busy = '1'; pillEl.textContent = 'Updating…'; }
  try {
    const r = await fetch('/api/plugins/install-update', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name }),
    });
    if (!r.ok) {
      const e = await r.json().catch(() => ({}));
      throw new Error(e.error || ('HTTP ' + r.status));
    }
  } catch (err) {
    showToast('Update failed: ' + (err && err.message || err));
    if (pillEl) { pillEl.disabled = false; pillEl.dataset.busy = '0'; pillEl.textContent = '↑ Retry'; }
    throw err;
  }
  // Success: _PluginUpdated SSE re-renders. Optimistic: drop from list.
  pluginUpdates = pluginUpdates.filter(u => u.name !== name);
  renderUpdateHeader();
}

async function runUpdateAll() {
  const btn = document.getElementById('update-all-btn');
  btn.disabled = true;
  const names = pluginUpdates.map(u => u.name);
  let failed = 0;
  for (const name of names) {
    try {
      await runUpdateOne(name, document.querySelector('[data-role="update-one"][data-name="' + cssEscape(name) + '"]'));
    } catch (_) { failed++; }
  }
  btn.disabled = false;
  if (failed > 0) showToast(failed + ' update' + (failed === 1 ? '' : 's') + ' failed');
}

function cssEscape(s) {
  return s.replace(/[^a-zA-Z0-9_-]/g, c => '\\' + c.charCodeAt(0).toString(16) + ' ');
}

function showToast(msg) {
  const root = document.getElementById('install-toasts');
  if (!root) return;
  const el = document.createElement('div');
  el.className = 'toast';
  el.textContent = msg;
  root.appendChild(el);
  setTimeout(() => el.remove(), 4000);
}
```

- [ ] **Step 7: Wire the Update-all and retry buttons**

Below `renderSources();` (or wherever the existing one-time wiring lives), add:

```javascript
document.getElementById('update-all-btn').addEventListener('click', () => runUpdateAll());
document.getElementById('catalog-error').addEventListener('click', () => refreshPluginUpdates({ trigger: 'manual' }));
```

- [ ] **Step 8: Hook into startup and SSE listener**

Find `const initialPluginListReady = refreshPluginList();` and replace with:

```javascript
const initialPluginListReady = Promise.all([
  refreshPluginList(),
  refreshPluginUpdates({ trigger: 'startup' }),
]);
```

Find the existing SSE listener that handles `_OverridesChanged` (search for `_OverridesChanged`). Extend it:

```javascript
  es.onmessage = (e) => {
    let env;
    try { env = JSON.parse(e.data); } catch (_) { return; }
    if (!env) return;
    if (env.Event === '_OverridesChanged') {
      overrides = env.Data || {};
      renderPlugins();
      return;
    }
    if (env.Event === '_PluginUpdated') {
      // Re-fetch the plugin list so the new version renders, and
      // drop the now-installed entry from the updates state.
      refreshPluginList();
      const data = env.Data || {};
      if (data.name) {
        pluginUpdates = pluginUpdates.filter(u => u.name !== data.name);
        renderUpdateHeader();
      }
    }
  };
```

- [ ] **Step 9: Build and smoke-test**

Run: `make backend` (or whatever the project's backend build target is — see `Makefile`).

Expected: backend binary builds.

Manual: launch a dev backend pointing at a local plugins dir with one outdated plugin, point a local HTTP server at a custom plugins.json containing the newer version, override `PluginCatalogURL` via a quick local edit. Confirm: pill appears, click installs, version updates on the card, pill disappears.

- [ ] **Step 10: Commit**

```bash
git add backend/internal/server/web/dashboard.html
git commit -m "dashboard: render update pills and bulk update button"
```

---

## Task 12: SDK dev-reload also handles `_PluginUpdated`

**Files:**
- Modify: `backend/internal/server/web/sdk/src/dev-reload.js`
- Modify: `backend/internal/server/web/sdk/dist/sdk.js` (rebuilt — do not hand-edit)

- [ ] **Step 1: Extend the listener**

Edit `backend/internal/server/web/sdk/src/dev-reload.js`. Replace the body of `installDevReload` with:

```javascript
export function installDevReload() {
  const reloadIfMine = (data, kind) => {
    if (!data || data.name !== pluginName) return;
    try { console.info('[RLT] ' + kind + ' reload:', pluginName); } catch (_) {}
    try { window.location.reload(); } catch (_) { /* no-op */ }
  };
  bus.on('_DevPluginReload', (data) => reloadIfMine(data, 'dev'));
  bus.on('_PluginUpdated',   (data) => reloadIfMine(data, 'update'));
}
```

- [ ] **Step 2: Rebuild the SDK dist**

Run: `npm run build:sdk` (or whatever the project uses — check `package.json` for the relevant script).

Expected: `backend/internal/server/web/sdk/dist/sdk.js` updates.

- [ ] **Step 3: Commit**

```bash
git add backend/internal/server/web/sdk/src/dev-reload.js backend/internal/server/web/sdk/dist/sdk.js
git commit -m "sdk: reload plugin iframe on _PluginUpdated too"
```

---

## Task 13: gen-plugin-catalog tool

**Files:**
- Create: `backend/cmd/gen-plugin-catalog/main.go`
- Create: `backend/cmd/gen-plugin-catalog/main_test.go`

CI tool: scans a directory of `.rltp` files, reads each archive's `manifest.json`, computes sha256 + size, writes `plugins.json` in the schema-1 shape.

- [ ] **Step 1: Write the failing test**

Create `backend/cmd/gen-plugin-catalog/main_test.go`:

```go
package main

import (
    "archive/zip"
    "crypto/sha256"
    "encoding/hex"
    "encoding/json"
    "os"
    "path/filepath"
    "testing"
)

func makeRltp(t *testing.T, dir, name, version string) string {
    t.Helper()
    p := filepath.Join(dir, name+"-"+version+".rltp")
    out, err := os.Create(p)
    if err != nil {
        t.Fatal(err)
    }
    zw := zip.NewWriter(out)
    mf, _ := zw.Create("manifest.json")
    _, _ = mf.Write([]byte(`{"name":"` + name + `","version":"` + version +
        `","title":"` + name + `","author":"test","description":"t","overlay":{"file":"o.html","width":10,"height":10}}`))
    oh, _ := zw.Create("o.html")
    _, _ = oh.Write([]byte("<html></html>"))
    _ = zw.Close()
    _ = out.Close()
    return p
}

func TestBuildCatalog(t *testing.T) {
    dir := t.TempDir()
    p := makeRltp(t, dir, "demos2", "1.1.0")

    cat, err := buildCatalog(dir, "https://example.com/dl/")
    if err != nil {
        t.Fatal(err)
    }
    if len(cat.Plugins) != 1 {
        t.Fatalf("plugins len = %d", len(cat.Plugins))
    }
    if cat.Plugins[0].Name != "demos2" || cat.Plugins[0].Version != "1.1.0" {
        t.Fatalf("bad row: %+v", cat.Plugins[0])
    }
    raw, _ := os.ReadFile(p)
    want := sha256.Sum256(raw)
    if cat.Plugins[0].SHA256 != hex.EncodeToString(want[:]) {
        t.Fatalf("sha256 mismatch")
    }
    if cat.Plugins[0].URL != "https://example.com/dl/demos2-1.1.0.rltp" {
        t.Fatalf("url = %q", cat.Plugins[0].URL)
    }
    if cat.Schema != 1 {
        t.Fatal("schema != 1")
    }
    // generated_at parsable
    var doc map[string]any
    b, _ := json.Marshal(cat)
    _ = json.Unmarshal(b, &doc)
    if _, ok := doc["generated_at"].(string); !ok {
        t.Fatal("generated_at missing")
    }
}
```

- [ ] **Step 2: Run (expect FAIL)**

Run: `go test ./backend/cmd/gen-plugin-catalog/ -v`

Expected: FAIL — undefined.

- [ ] **Step 3: Implement**

Create `backend/cmd/gen-plugin-catalog/main.go`:

```go
// Command gen-plugin-catalog walks a directory of .rltp archives,
// reads each archive's manifest.json, and writes a schema-1
// plugins.json describing them. Used by the release-plugins CI
// workflow.
package main

import (
    "archive/zip"
    "crypto/sha256"
    "encoding/hex"
    "encoding/json"
    "flag"
    "fmt"
    "io"
    "os"
    "path/filepath"
    "sort"
    "strings"
    "time"
)

type entry struct {
    Name               string `json:"name"`
    Version            string `json:"version"`
    Title              string `json:"title,omitempty"`
    Author             string `json:"author,omitempty"`
    Description        string `json:"description,omitempty"`
    URL                string `json:"url"`
    SHA256             string `json:"sha256"`
    SizeBytes          int64  `json:"size_bytes"`
    MinLauncherVersion string `json:"min_launcher_version,omitempty"`
}

type doc struct {
    Schema      int       `json:"schema"`
    GeneratedAt string    `json:"generated_at"`
    Plugins     []entry   `json:"plugins"`
}

func buildCatalog(distDir, baseURL string) (doc, error) {
    if !strings.HasSuffix(baseURL, "/") {
        baseURL += "/"
    }
    entries, err := os.ReadDir(distDir)
    if err != nil {
        return doc{}, err
    }
    out := doc{Schema: 1, GeneratedAt: time.Now().UTC().Format(time.RFC3339), Plugins: []entry{}}
    for _, de := range entries {
        if de.IsDir() || !strings.HasSuffix(de.Name(), ".rltp") {
            continue
        }
        full := filepath.Join(distDir, de.Name())
        e, err := scanArchive(full)
        if err != nil {
            return doc{}, fmt.Errorf("%s: %w", de.Name(), err)
        }
        e.URL = baseURL + de.Name()
        out.Plugins = append(out.Plugins, e)
    }
    sort.Slice(out.Plugins, func(i, j int) bool { return out.Plugins[i].Name < out.Plugins[j].Name })
    return out, nil
}

func scanArchive(path string) (entry, error) {
    info, err := os.Stat(path)
    if err != nil {
        return entry{}, err
    }
    f, err := os.Open(path)
    if err != nil {
        return entry{}, err
    }
    defer f.Close()
    h := sha256.New()
    if _, err := io.Copy(h, f); err != nil {
        return entry{}, err
    }
    zr, err := zip.OpenReader(path)
    if err != nil {
        return entry{}, err
    }
    defer zr.Close()
    var mfBytes []byte
    for _, zf := range zr.File {
        if zf.Name != "manifest.json" {
            continue
        }
        rc, err := zf.Open()
        if err != nil {
            return entry{}, err
        }
        mfBytes, err = io.ReadAll(rc)
        _ = rc.Close()
        if err != nil {
            return entry{}, err
        }
        break
    }
    if mfBytes == nil {
        return entry{}, fmt.Errorf("manifest.json missing")
    }
    var mf struct {
        Name, Version, Title, Author, Description string
    }
    if err := json.Unmarshal(mfBytes, &mf); err != nil {
        return entry{}, err
    }
    return entry{
        Name:        mf.Name,
        Version:     mf.Version,
        Title:       mf.Title,
        Author:      mf.Author,
        Description: mf.Description,
        SHA256:      hex.EncodeToString(h.Sum(nil)),
        SizeBytes:   info.Size(),
    }, nil
}

func main() {
    in := flag.String("in", "dist", "Directory of .rltp files")
    base := flag.String("base-url", "", "URL prefix for downloads (release asset base)")
    out := flag.String("out", "dist/plugins.json", "Output path for plugins.json")
    flag.Parse()
    if *base == "" {
        fmt.Fprintln(os.Stderr, "gen-plugin-catalog: -base-url is required")
        os.Exit(2)
    }
    cat, err := buildCatalog(*in, *base)
    if err != nil {
        fmt.Fprintln(os.Stderr, "gen-plugin-catalog:", err)
        os.Exit(1)
    }
    b, _ := json.MarshalIndent(cat, "", "  ")
    if err := os.WriteFile(*out, b, 0o644); err != nil {
        fmt.Fprintln(os.Stderr, "gen-plugin-catalog: write:", err)
        os.Exit(1)
    }
}
```

- [ ] **Step 4: Run test**

Run: `go test ./backend/cmd/gen-plugin-catalog/ -v`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/cmd/gen-plugin-catalog/
git commit -m "gen-plugin-catalog: build plugins.json from a dist dir"
```

---

## Task 14: GitHub Actions workflow to release plugins

**Files:**
- Create: `.github/workflows/release-plugins.yml`

- [ ] **Step 1: Write the workflow**

Create `.github/workflows/release-plugins.yml`:

```yaml
# Packages every plugin under plugins/ into a .rltp, generates a
# schema-1 plugins.json, and uploads the lot to the long-lived
# `plugins-latest` GitHub release. The launcher fetches plugins.json
# from this release and uses it to surface "update available" pills.
#
# Plugins that should NOT ship to users can place a top-level
# .norelease file in their folder; the loop below skips those.

name: release-plugins

on:
  workflow_dispatch: {}

permissions:
  contents: write

jobs:
  build:
    runs-on: ubuntu-24.04
    env:
      GH_TOKEN: ${{ secrets.GITHUB_TOKEN }}
    steps:
      - name: Checkout
        uses: actions/checkout@v4

      - name: Set up Go
        uses: actions/setup-go@v5
        with:
          go-version-file: go.mod

      - name: Build rl-toolkit
        run: go build -o ./bin/rl-toolkit ./backend/cmd/rl-toolkit

      - name: Build gen-plugin-catalog
        run: go build -o ./bin/gen-plugin-catalog ./backend/cmd/gen-plugin-catalog

      - name: Pack every plugin (skipping .norelease)
        run: |
          mkdir -p dist
          for dir in plugins/*/; do
            name=$(basename "$dir")
            if [ -f "$dir/.norelease" ]; then
              echo "Skipping $name (.norelease)"; continue
            fi
            if [ ! -f "$dir/manifest.json" ]; then
              echo "Skipping $name (no manifest.json)"; continue
            fi
            ./bin/rl-toolkit pack "$dir" -out dist/
          done
          ls -la dist/

      - name: Ensure plugins-latest release exists
        run: |
          gh release view plugins-latest >/dev/null 2>&1 || \
            gh release create plugins-latest \
              --title "Latest plugin catalog" \
              --notes "Rolling release of packaged plugins. Updated by the release-plugins workflow."

      - name: Generate plugins.json
        env:
          OWNER: ${{ github.repository_owner }}
          REPO: ${{ github.event.repository.name }}
        run: |
          BASE="https://github.com/${OWNER}/${REPO}/releases/download/plugins-latest/"
          ./bin/gen-plugin-catalog -in dist -base-url "$BASE" -out dist/plugins.json
          cat dist/plugins.json

      - name: Upload assets to plugins-latest (clobber)
        run: |
          gh release upload plugins-latest dist/*.rltp dist/plugins.json --clobber
```

- [ ] **Step 2: Commit**

```bash
git add .github/workflows/release-plugins.yml
git commit -m "ci: release-plugins workflow packs plugins and updates the catalog"
```

- [ ] **Step 3: Smoke-test the workflow end-to-end**

This step requires repo admin and is one-time:

1. Push the branch to GitHub.
2. From the Actions tab, run `release-plugins`.
3. After it completes, fetch `https://github.com/<owner>/RLToolkit/releases/download/plugins-latest/plugins.json` and confirm it contains one entry per non-`.norelease` plugin with a valid sha256 and a download URL that 200s.

Do not commit anything from this step; it's verification only.

---

## Task 15: Wire build-time version flag into the release workflows

**Files:**
- Modify: `Makefile`

`Version` is currently `"0.0.0-dev"`. We want releases to embed the real version so `min_launcher_version` filtering actually works.

- [ ] **Step 1: Find the existing Go build invocation**

Run: `grep -n "go build" Makefile`

Identify the target that builds `rl-toolkit` for releases (likely under `backend:` near line 100).

- [ ] **Step 2: Pass the version via ldflags**

Edit the backend build target so the `go build` line reads:

```make
	go build -ldflags="-X main.Version=$(VERSION)" -o $(OUT_DIR)/rl-toolkit ./backend/cmd/rl-toolkit
```

(`$(VERSION)` is already defined at the top of the Makefile — the launcher build uses it.)

- [ ] **Step 3: Verify**

Run: `make backend VERSION=0.3.0`

Run the resulting binary with a flag that prints the banner (the `printStartupBanner` call referenced in `main.go`). If the banner doesn't print the version, leave that for a follow-up — the catalog code uses the constant directly, not the banner.

A direct verification: add a temporary `log.Printf("rlt version: %s", Version)` line at the top of `runServe`, run `make backend VERSION=0.3.0 && ./bin/rl-toolkit serve`, confirm the log says `0.3.0`, then remove the line. (Do not commit the temporary log line.)

- [ ] **Step 4: Commit**

```bash
git add Makefile
git commit -m "make: embed version in rl-toolkit via -ldflags"
```

---

## Task 16: End-to-end smoke test

**Files:** none (manual verification)

- [ ] **Step 1: Set up a local staging catalog**

```bash
# In a scratch dir on your machine:
mkdir -p /tmp/rlt-stage/dist
cp ~/Documents/Projects/RLToolkit/plugins/demos2 /tmp/rlt-stage/src/demos2 -R
# Bump version in /tmp/rlt-stage/src/demos2/manifest.json to 999.0.0.
./bin/rl-toolkit pack /tmp/rlt-stage/src/demos2 -out /tmp/rlt-stage/dist/
./bin/gen-plugin-catalog -in /tmp/rlt-stage/dist -base-url "http://127.0.0.1:8080/" -out /tmp/rlt-stage/dist/plugins.json
# Serve dist over plain HTTP:
( cd /tmp/rlt-stage/dist && python3 -m http.server 8080 ) &
```

The catalog's URL validator only accepts `https://` — for this smoke test you'll need to *temporarily* relax `isValidHTTPSURL` in `plugincatalog/catalog.go` to also accept `http://` (do NOT commit that change), or run a quick `ngrok http 8080` and use the https URL it gives you.

- [ ] **Step 2: Build a launcher pointed at the staging catalog**

```bash
go build -ldflags="-X main.Version=0.3.0 -X main.PluginCatalogURL=https://<ngrok-id>.ngrok.io/plugins.json" \
  -o ./bin/rl-toolkit ./backend/cmd/rl-toolkit
./bin/rl-toolkit serve
```

- [ ] **Step 3: Verify the UX**

Open the launcher. Confirm:
1. The demos2 card shows a "↑ Update to 999.0.0" pill.
2. The section header shows "Update all (1)".
3. Click the pill. It shows "Updating…" then disappears within a few seconds.
4. The card's version stamp updates to v999.0.0.
5. If the overlay was running with demos2 active, the iframe reloads (a `console.info` in DevTools shows "[RLT] update reload: demos2").
6. Refreshing the dashboard re-fetches and shows no pill.

- [ ] **Step 4: Revert the temporary http:// allowance and commit nothing from this task**

Verify no uncommitted code changes remain after the smoke test.

---

## Self-review

**Spec coverage:**
- Catalog format → Task 5 (schema-1 parse, validation, min-version filter).
- `plugincatalog` package → Tasks 4, 5, 6 (semver, parse + manager, installed-versions accessor).
- `install.InstallFromURL` → Task 7.
- Three HTTP endpoints → Tasks 8, 9.
- Live iframe reload via `_PluginUpdated` → Tasks 2 (NotifyUpdated), 3 (bus framing), 12 (SDK listener).
- Dashboard UI (pill, Update all, error hint, refresh wiring) → Task 11.
- Publishing workflow → Tasks 13 (gen-plugin-catalog), 14 (release-plugins.yml).
- Version embedding for `min_launcher_version` → Tasks 10 (constant), 15 (build flag).
- End-to-end verification → Task 16.

**Placeholder scan:** Two notes-to-implementer remain in Tasks 5 and 8 (the `pluginsLister`/`errors.New` scaffolding lines that the implementer must delete). They are explicit instructions, not unresolved TBDs. The rest of the plan ships complete code.

**Type consistency:** `Manager` exposes `Refresh`, `Updates`, `Find`, `LastChecked`, `LastError`, used consistently in Tasks 5 and 8. The SSE event name `_PluginUpdated` and its payload `{name, installed_version}` appear identically in Tasks 2, 3, 11, and 12. The catalog row JSON shape (`name`, `installed_version`, `latest_version`, `size_bytes`) matches between Task 8's `updateRow` and Task 11's `pluginUpdates`. `InstalledLookup` interface in Task 5 matches the two methods added in Tasks 1 and 6.

**Scope:** Each task produces a buildable, committable change. Tasks 1–10 are pure-Go and could be done by a backend agent in series. Task 11 is the only frontend-heavy task. Tasks 12 onward are independent of 11. No task crosses ~150 LOC of new code.
