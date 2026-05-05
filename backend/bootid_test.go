package main

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
)

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
