package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"rl-toolkit/backend/internal/tracker"
)

// handleMMRSelf is GET /api/mmr; it looks up the configured identity.
func (s *Server) handleMMRSelf(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeMMRError(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	id := s.deps.Identity.Get()
	if id == nil {
		writeMMRError(w, http.StatusConflict, map[string]any{"error": "identity not set"})
		return
	}
	slug, playerID, err := tracker.ResolveSlug(id.PrimaryID)
	if err != nil {
		platform := ""
		if i := strings.IndexByte(id.PrimaryID, '|'); i > 0 {
			platform = strings.ToLower(id.PrimaryID[:i])
		}
		writeMMRError(w, http.StatusNotImplemented, map[string]any{
			"error":    "platform not supported",
			"platform": platform,
		})
		return
	}
	s.runMMRLookup(w, r, slug, playerID)
}

// handleMMRLookup is GET /api/mmr/{platform}/{id}; explicit lookup.
func (s *Server) handleMMRLookup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeMMRError(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/mmr/")
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		writeMMRError(w, http.StatusBadRequest, map[string]any{"error": "bad request"})
		return
	}
	platform := strings.ToLower(parts[0])
	if !tracker.ValidExplicitSlug(platform) {
		writeMMRError(w, http.StatusBadRequest, map[string]any{"error": "bad request"})
		return
	}
	s.runMMRLookup(w, r, platform, parts[1])
}

func (s *Server) runMMRLookup(w http.ResponseWriter, r *http.Request, platform, id string) {
	if s.deps.Tracker == nil {
		writeMMRError(w, http.StatusInternalServerError, map[string]any{"error": "tracker not configured"})
		return
	}
	res, err := s.deps.Tracker.Lookup(r.Context(), platform, id)
	switch {
	case err == nil:
		writeJSON(w, res)
	case errors.Is(err, tracker.ErrCircuitOpen):
		retry := int(s.deps.Tracker.BreakerRetryAfter().Seconds())
		if retry < 1 {
			retry = 1
		}
		w.Header().Set("Retry-After", strconv.Itoa(retry))
		writeMMRError(w, http.StatusServiceUnavailable, map[string]any{"error": "upstream temporarily unavailable"})
	case errors.Is(err, tracker.ErrRateLimited):
		w.Header().Set("Retry-After", "1")
		writeMMRError(w, http.StatusServiceUnavailable, map[string]any{"error": "rate limited"})
	case errors.Is(err, tracker.ErrUpstreamBlocked):
		writeMMRError(w, http.StatusBadGateway, map[string]any{
			"error":          "upstream blocked",
			"upstreamStatus": upstreamStatus(err),
		})
	case errors.Is(err, tracker.ErrPlayerNotFound):
		writeMMRError(w, http.StatusNotFound, map[string]any{
			"error":    "player not found",
			"platform": platform,
			"playerId": id,
		})
	default: // ErrUpstream and anything else
		writeMMRError(w, http.StatusBadGateway, map[string]any{"error": "upstream error"})
	}
}

func writeMMRError(w http.ResponseWriter, status int, body map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// upstreamStatus extracts the HTTP status code from a tracker upstream
// failure. Returns 0 when the error doesn't carry one.
func upstreamStatus(err error) int {
	var ue *tracker.UpstreamError
	if errors.As(err, &ue) {
		return ue.Status
	}
	return 0
}
