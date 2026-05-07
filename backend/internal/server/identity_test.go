package server

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"rl-toolkit/backend/internal/bus"
	"rl-toolkit/backend/internal/identity"
	"rl-toolkit/backend/internal/source"
	"strings"
	"testing"
	"time"
)

func TestSSE_InitialIdentitySnapshot(t *testing.T) {
	store, err := identity.New(t.TempDir())
	if err != nil {
		t.Fatalf("new identity: %v", err)
	}
	if err := store.Set(identity.Identity{PrimaryID: "Steam|7|0", Name: "Ada"}); err != nil {
		t.Fatal(err)
	}

	srv := New(Deps{
		Bus:      bus.NewBus(),
		Source:   source.NewRL("127.0.0.1:0"),
		Identity: store,
	})
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
		if !strings.Contains(body, `"_IdentityChanged"`) {
			continue
		}
		if !strings.Contains(body, `"primaryId":"Steam|7|0"`) {
			t.Fatalf("identity frame missing persisted id: %s", body)
		}
		return
	}
	t.Fatalf("timed out waiting for _IdentityChanged frame")
}
