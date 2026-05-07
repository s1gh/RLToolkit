package backend

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

func newBootIDTestServer(t *testing.T) *Server {
	t.Helper()
	bus := NewBus()
	source := NewRLSource("127.0.0.1:0")
	return &Server{bus: bus, source: source}
}

func TestSSEFirstFrameIncludesBootID(t *testing.T) {
	srv := newBootIDTestServer(t)
	ts := httptest.NewServer(srv.routes())
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer resp.Body.Close()

	rd := bufio.NewReader(resp.Body)
	// First non-empty data line must be the _BootId frame.
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		line, err := rd.ReadString('\n')
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		body := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
		wantSnippet := `"Event":"_BootId"`
		wantID := `"bootId":"` + BootID() + `"`
		if !strings.Contains(body, wantSnippet) || !strings.Contains(body, wantID) {
			t.Fatalf("first data frame did not contain _BootId; got: %s", body)
		}
		return
	}
	t.Fatalf("timed out waiting for first data frame")
}

func TestBootIDIsSixteenLowerHex(t *testing.T) {
	id := BootID()
	if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(id) {
		t.Fatalf("BootID() = %q; want 16 lowercase hex chars", id)
	}
}

func TestBootIDIsStableWithinProcess(t *testing.T) {
	a := BootID()
	b := BootID()
	if a != b {
		t.Fatalf("BootID() returned different values within a process: %q vs %q", a, b)
	}
}

func TestHandleBootIDReturnsJSON(t *testing.T) {
	s := &Server{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/boot-id", nil)
	s.handleBootID(rec, req)

	if got, want := rec.Code, http.StatusOK; got != want {
		t.Fatalf("status = %d; want %d", got, want)
	}
	if got, want := rec.Header().Get("Content-Type"), "application/json"; got != want {
		t.Fatalf("Content-Type = %q; want %q", got, want)
	}
	want := `{"bootId":"` + BootID() + `"}`
	if got := rec.Body.String(); got != want {
		t.Fatalf("body = %q; want %q", got, want)
	}
}
