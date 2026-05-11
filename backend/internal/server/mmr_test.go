package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"rl-toolkit/backend/internal/identity"
	"rl-toolkit/backend/internal/tracker"
)

// stdlibDoer drives net/http.DefaultClient against the URL the tracker
// client has already constructed (with BaseURL pointing at our
// httptest server). Surf is deliberately not exercised here; its value
// is JA3/H2 wire fingerprinting that httptest cannot validate, so
// running it against a local server would test nothing useful.
type stdlibDoer struct{}

func (stdlibDoer) Do(ctx context.Context, url string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, body, nil
}

func newMMRTestServer(t *testing.T, upstream *httptest.Server, identityID string) (*httptest.Server, func()) {
	t.Helper()

	tmp := t.TempDir()
	idStore, err := identity.New(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if identityID != "" {
		if err := idStore.Set(identity.Identity{PrimaryID: identityID, Name: "Tester"}); err != nil {
			t.Fatal(err)
		}
	}

	trk, err := tracker.New(tracker.Options{
		BaseURL: upstream.URL,
		Doer:    stdlibDoer{},
		// DataDir empty: cache is in-memory only, per-test isolation.
	})
	if err != nil {
		t.Fatal(err)
	}

	srv := New(Deps{Identity: idStore, Tracker: trk})
	httpSrv := httptest.NewServer(srv.Routes())
	return httpSrv, func() { httpSrv.Close(); upstream.Close() }
}

func upstreamFixture(t *testing.T, status int, bodyPath string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if bodyPath != "" {
			b, err := os.ReadFile(bodyPath)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write(b)
			return
		}
		_, _ = io.WriteString(w, `{"errors":[{"code":"X"}]}`)
	}))
}

func TestMMRSelf_NoIdentity(t *testing.T) {
	upstream := upstreamFixture(t, 200, "../tracker/testdata/profile_steam_ok.json")
	srv, done := newMMRTestServer(t, upstream, "")
	defer done()
	res, err := http.Get(srv.URL + "/api/mmr")
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 409 {
		t.Fatalf("status: got %d want 409", res.StatusCode)
	}
}

func TestMMRSelf_SteamIdentity(t *testing.T) {
	upstream := upstreamFixture(t, 200, "../tracker/testdata/profile_steam_ok.json")
	srv, done := newMMRTestServer(t, upstream, "Steam|76561197964340095|0")
	defer done()
	res, err := http.Get(srv.URL + "/api/mmr")
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("status: got %d want 200", res.StatusCode)
	}
	var body struct {
		Platform  string                    `json:"platform"`
		PlayerID  string                    `json:"playerId"`
		Playlists map[string]map[string]any `json:"playlists"`
	}
	_ = json.NewDecoder(res.Body).Decode(&body)
	if body.Platform != "steam" || body.PlayerID != "76561197964340095" {
		t.Fatalf("identity wrong: %+v", body)
	}
	if _, ok := body.Playlists["3v3"]; !ok {
		t.Fatalf("missing 3v3 in playlists: %+v", body.Playlists)
	}
}

func TestMMRSelf_EpicIdentity(t *testing.T) {
	upstream := upstreamFixture(t, 200, "../tracker/testdata/profile_steam_ok.json")
	srv, done := newMMRTestServer(t, upstream, "Epic|some-display-name|0")
	defer done()
	res, err := http.Get(srv.URL + "/api/mmr")
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 501 {
		t.Fatalf("status: got %d want 501", res.StatusCode)
	}
}

func TestMMRLookup_OK(t *testing.T) {
	upstream := upstreamFixture(t, 200, "../tracker/testdata/profile_steam_ok.json")
	srv, done := newMMRTestServer(t, upstream, "")
	defer done()
	res, err := http.Get(srv.URL + "/api/mmr/steam/76561197964340095")
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("status: got %d want 200", res.StatusCode)
	}
}

func TestMMRLookup_CaseInsensitivePlatform(t *testing.T) {
	upstream := upstreamFixture(t, 200, "../tracker/testdata/profile_steam_ok.json")
	srv, done := newMMRTestServer(t, upstream, "")
	defer done()
	res, err := http.Get(srv.URL + "/api/mmr/STEAM/123")
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("status: got %d want 200", res.StatusCode)
	}
}

func TestMMRLookup_BadPlatform(t *testing.T) {
	upstream := upstreamFixture(t, 200, "")
	srv, done := newMMRTestServer(t, upstream, "")
	defer done()
	res, err := http.Get(srv.URL + "/api/mmr/foo/123")
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 400 {
		t.Fatalf("status: got %d want 400", res.StatusCode)
	}
}

func TestMMRLookup_MissingID(t *testing.T) {
	upstream := upstreamFixture(t, 200, "")
	srv, done := newMMRTestServer(t, upstream, "")
	defer done()
	res, err := http.Get(srv.URL + "/api/mmr/steam")
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 400 {
		t.Fatalf("status: got %d want 400", res.StatusCode)
	}
}

func TestMMRSelf_RejectsPOST(t *testing.T) {
	upstream := upstreamFixture(t, 200, "")
	srv, done := newMMRTestServer(t, upstream, "Steam|1|0")
	defer done()
	res, err := http.Post(srv.URL+"/api/mmr", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 405 {
		t.Fatalf("status: got %d want 405", res.StatusCode)
	}
}

func TestMMRLookup_Upstream403Maps502(t *testing.T) {
	upstream := upstreamFixture(t, 403, "")
	srv, done := newMMRTestServer(t, upstream, "")
	defer done()
	res, err := http.Get(srv.URL + "/api/mmr/steam/1")
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 502 {
		t.Fatalf("status: got %d want 502", res.StatusCode)
	}
	var body struct {
		UpstreamStatus int `json:"upstreamStatus"`
	}
	_ = json.NewDecoder(res.Body).Decode(&body)
	if body.UpstreamStatus != 403 {
		t.Fatalf("body upstreamStatus: got %d want 403", body.UpstreamStatus)
	}
}

func TestMMRLookup_Upstream429Maps502(t *testing.T) {
	upstream := upstreamFixture(t, 429, "")
	srv, done := newMMRTestServer(t, upstream, "")
	defer done()
	res, err := http.Get(srv.URL + "/api/mmr/steam/1")
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 502 {
		t.Fatalf("status: got %d want 502", res.StatusCode)
	}
	// Pin the typed-error contract: the body's upstreamStatus is the
	// real upstream code, recovered via errors.As against
	// tracker.UpstreamError, not by parsing the error message.
	var body struct {
		UpstreamStatus int `json:"upstreamStatus"`
	}
	_ = json.NewDecoder(res.Body).Decode(&body)
	if body.UpstreamStatus != 429 {
		t.Fatalf("body upstreamStatus: got %d want 429", body.UpstreamStatus)
	}
}

func TestMMRLookup_Upstream404Maps404(t *testing.T) {
	upstream := upstreamFixture(t, 404, "")
	srv, done := newMMRTestServer(t, upstream, "")
	defer done()
	res, err := http.Get(srv.URL + "/api/mmr/steam/1")
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 404 {
		t.Fatalf("status: got %d want 404", res.StatusCode)
	}
}

func TestMMRLookup_Upstream500CarriesStatus(t *testing.T) {
	// Other 5xx responses produce ErrUpstream wrapped in
	// tracker.UpstreamError; the handler should surface the upstream
	// status in the response body so plugins can branch on it.
	upstream := upstreamFixture(t, 500, "")
	srv, done := newMMRTestServer(t, upstream, "")
	defer done()
	res, err := http.Get(srv.URL + "/api/mmr/steam/1")
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 502 {
		t.Fatalf("status: got %d want 502", res.StatusCode)
	}
	var body struct {
		Error          string `json:"error"`
		UpstreamStatus int    `json:"upstreamStatus"`
	}
	_ = json.NewDecoder(res.Body).Decode(&body)
	if body.Error != "upstream error" {
		t.Errorf("body.error: got %q want 'upstream error'", body.Error)
	}
	if body.UpstreamStatus != 500 {
		t.Errorf("body.upstreamStatus: got %d want 500", body.UpstreamStatus)
	}
}
