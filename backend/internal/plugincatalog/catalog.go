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

// InstalledLookup is the subset of plugins.Manager the catalog reads
// from. Defined here to avoid an import cycle and to keep tests
// trivial. The real plugins.Manager satisfies this interface.
type InstalledLookup interface {
	DevNames() []string
	InstalledVersions() map[string]string
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

	mu        sync.Mutex
	entries   []Entry
	byName    map[string]Entry
	lastCheck time.Time
	lastErr   error
}

// New returns a Manager configured to fetch from url. pm may be nil;
// Updates() then returns an empty slice.
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

// Find returns the catalog entry for name, if present.
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
		Schema  int        `json:"schema"`
		Plugins []rawEntry `json:"plugins"`
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
		return Entry{}, errors.New("invalid sha256")
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
