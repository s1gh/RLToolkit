package server

import (
	"net/http"
	"time"

	"rl-toolkit/backend/internal/plugincatalog"
)

// catalogResponse is the shape returned by GET /api/plugins/catalog.
// It mirrors plugins.json plus per-entry installed-state, so the
// dashboard's Browse tab can render every entry without further joins.
type catalogResponse struct {
	Entries       []catalogRow `json:"entries"`
	LastCheckedAt *string      `json:"last_checked_at"`
	LastError     *string      `json:"last_error"`
}

type catalogRow struct {
	Name             string `json:"name"`
	Title            string `json:"title"`
	Author           string `json:"author"`
	Description      string `json:"description"`
	Version          string `json:"version"`
	InstalledVersion string `json:"installed_version"`
	State            string `json:"state"`
	SizeBytes        int64  `json:"size_bytes"`
}

const (
	stateNotInstalled    = "not_installed"
	stateUpToDate        = "up_to_date"
	stateUpdateAvailable = "update_available"
)

// handlePluginCatalog returns the full catalog joined with installed
// versions. Reads cache; never triggers a Refresh. The dashboard hits
// POST /api/plugins/refresh-catalog separately when it wants fresh data.
func (s *Server) handlePluginCatalog(w http.ResponseWriter, _ *http.Request) {
	if s.deps.Catalog == nil {
		writeJSON(w, catalogResponse{Entries: []catalogRow{}})
		return
	}
	installed := map[string]string{}
	devSet := map[string]struct{}{}
	if s.deps.Plugins != nil {
		installed = s.deps.Plugins.InstalledVersions()
		for _, n := range s.deps.Plugins.DevNames() {
			devSet[n] = struct{}{}
		}
	}

	rows := make([]catalogRow, 0)
	for _, e := range s.deps.Catalog.Entries() {
		if _, isDev := devSet[e.Name]; isDev {
			continue
		}
		row := catalogRow{
			Name:        e.Name,
			Title:       e.Title,
			Author:      e.Author,
			Description: e.Description,
			Version:     e.Version,
			SizeBytes:   e.SizeBytes,
			State:       stateNotInstalled,
		}
		if cur, ok := installed[e.Name]; ok {
			row.InstalledVersion = cur
			if plugincatalog.Compare(e.Version, cur) > 0 {
				row.State = stateUpdateAvailable
			} else {
				row.State = stateUpToDate
			}
		}
		rows = append(rows, row)
	}

	resp := catalogResponse{Entries: rows}
	if t := s.deps.Catalog.LastChecked(); !t.IsZero() {
		f := t.UTC().Format(time.RFC3339)
		resp.LastCheckedAt = &f
	}
	if err := s.deps.Catalog.LastError(); err != nil {
		msg := err.Error()
		resp.LastError = &msg
	}
	writeJSON(w, resp)
}
