package server

import (
	"context"
	"encoding/json"
	"log"
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
	_ = s.deps.Catalog.Refresh(ctx) // failure surfaces via LastError in the response body
	writeJSON(w, s.buildUpdatesResponse())
}

func (s *Server) handleInstallUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.deps.Catalog == nil || s.deps.Plugins == nil {
		writeJSONStatus(w, http.StatusInternalServerError,
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
	// Wipe any user overrides for this plugin. Update implies a schema
	// reset: the new manifest may have moved width/height/anchor in
	// ways that would render the old override nonsensical. Idempotent
	// — Delete on a missing entry is a no-op.
	if s.deps.Overrides != nil {
		if err := s.deps.Overrides.Delete(body.Name); err != nil {
			log.Printf("[server] install-update: clearing overrides for %s failed: %v", body.Name, err)
		}
	}
	s.deps.Plugins.NotifyUpdated(body.Name)
	m := s.deps.Plugins.Get(body.Name)
	if m == nil {
		// Install reported success but the freshly-unpacked manifest
		// can't be read back. The dashboard relies on installed_version
		// to swap the card label, so this is a corrupt-plugin signal,
		// not a partial success.
		writeJSONStatus(w, http.StatusInternalServerError,
			map[string]string{"error": "installed but manifest unreadable"})
		return
	}
	writeJSON(w, map[string]string{
		"name":              body.Name,
		"installed_version": m.Version,
	})
}

// writeJSONStatus is a companion to writeJSON for non-200 responses.
func writeJSONStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
