package bootid

import (
	"regexp"
	"testing"
)

func TestGetIsSixteenLowerHex(t *testing.T) {
	id := Get()
	if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(id) {
		t.Fatalf("Get() = %q; want 16 lowercase hex chars", id)
	}
}

func TestGetIsStableWithinProcess(t *testing.T) {
	a := Get()
	b := Get()
	if a != b {
		t.Fatalf("Get() returned different values within a process: %q vs %q", a, b)
	}
}
