package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// fakeDevAPI is an httptest.Server that records POSTs from the dev client.
type fakeDevAPI struct {
	mu          sync.Mutex
	registers   []map[string]string
	reloads     []string
	unregisters []string
}

func (f *fakeDevAPI) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/dev/register", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.registers = append(f.registers, body)
		f.mu.Unlock()
		w.WriteHeader(200)
	})
	mux.HandleFunc("/dev/reload", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.reloads = append(f.reloads, body["name"])
		f.mu.Unlock()
		w.WriteHeader(200)
	})
	mux.HandleFunc("/dev/unregister", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.unregisters = append(f.unregisters, body["name"])
		f.mu.Unlock()
		w.WriteHeader(200)
	})
	return mux
}

func TestDevClient_PostsRegisterReloadUnregister(t *testing.T) {
	fake := &fakeDevAPI{}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	host := strings.TrimPrefix(srv.URL, "http://")
	_, portStr, _ := strings.Cut(host, ":")
	port, _ := strconv.Atoi(portStr)

	dataDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dataDir, "dev.port"), []byte(strconv.Itoa(port)), 0644); err != nil {
		t.Fatal(err)
	}

	pluginDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(pluginDir, "manifest.json"), []byte(`{"name":"alpha","version":"0.1.0","overlay":{"file":"overlay.html"}}`), 0644); err != nil {
		t.Fatal(err)
	}

	client := newDevClient(dataDir)
	if err := client.register("alpha", pluginDir); err != nil {
		t.Fatalf("register: %v", err)
	}
	fake.mu.Lock()
	if len(fake.registers) != 1 {
		t.Fatalf("got %d registers, want 1", len(fake.registers))
	}
	if fake.registers[0]["name"] != "alpha" || fake.registers[0]["path"] == "" {
		t.Errorf("register payload wrong: %+v", fake.registers[0])
	}
	fake.mu.Unlock()

	if err := client.reload("alpha"); err != nil {
		t.Fatalf("reload: %v", err)
	}
	fake.mu.Lock()
	if len(fake.reloads) != 1 || fake.reloads[0] != "alpha" {
		t.Errorf("reloads = %v, want [alpha]", fake.reloads)
	}
	fake.mu.Unlock()

	if err := client.unregister("alpha"); err != nil {
		t.Fatalf("unregister: %v", err)
	}
	fake.mu.Lock()
	if len(fake.unregisters) != 1 || fake.unregisters[0] != "alpha" {
		t.Errorf("unregisters = %v, want [alpha]", fake.unregisters)
	}
	fake.mu.Unlock()
}

func TestDevClient_FailsWithoutPortFile(t *testing.T) {
	dataDir := t.TempDir()
	client := newDevClient(dataDir)
	if err := client.register("alpha", t.TempDir()); err == nil {
		t.Fatal("expected error when dev.port absent")
	}
}

func TestDevClient_RegisterSendsAbsolutePath(t *testing.T) {
	fake := &fakeDevAPI{}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	host := strings.TrimPrefix(srv.URL, "http://")
	_, portStr, _ := strings.Cut(host, ":")
	port, _ := strconv.Atoi(portStr)

	dataDir := t.TempDir()
	os.WriteFile(filepath.Join(dataDir, "dev.port"), []byte(strconv.Itoa(port)), 0644)

	pluginDir := t.TempDir()
	os.WriteFile(filepath.Join(pluginDir, "manifest.json"), []byte(`{"name":"alpha","version":"0.1.0"}`), 0644)

	client := newDevClient(dataDir)
	// Pass a relative path; client should resolve to absolute before sending.
	wd, _ := os.Getwd()
	rel, err := filepath.Rel(wd, pluginDir)
	if err != nil {
		t.Skip("cannot construct relative path between temp dirs")
	}
	if err := client.register("alpha", rel); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if !filepath.IsAbs(fake.registers[0]["path"]) {
		t.Errorf("register path %q is not absolute", fake.registers[0]["path"])
	}
}
