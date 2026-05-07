package server

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"rl-toolkit/backend/internal/bootid"
	"rl-toolkit/backend/internal/bus"
	"rl-toolkit/backend/internal/source"
	"strings"
	"testing"
	"time"
)

func newBootIDTestServer(t *testing.T) *Server {
	t.Helper()
	return New(Deps{
		Bus:    bus.NewBus(),
		Source: source.NewRL("127.0.0.1:0"),
	})
}

func TestSSEFirstFrameIncludesBootID(t *testing.T) {
	srv := newBootIDTestServer(t)
	ts := httptest.NewServer(srv.Routes())
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
		wantID := `"bootId":"` + bootid.Get() + `"`
		if !strings.Contains(body, wantSnippet) || !strings.Contains(body, wantID) {
			t.Fatalf("first data frame did not contain _BootId; got: %s", body)
		}
		return
	}
	t.Fatalf("timed out waiting for first data frame")
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
	want := `{"bootId":"` + bootid.Get() + `"}`
	if got := rec.Body.String(); got != want {
		t.Fatalf("body = %q; want %q", got, want)
	}
}
