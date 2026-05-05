package main

import (
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
