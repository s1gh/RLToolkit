package tracker

import (
	"errors"
	"testing"
)

func TestResolveSlug(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantSlug string
		wantID   string
		wantErr  error
	}{
		{"steam identity", "Steam|76561197960287930|0", "steam", "76561197960287930", nil},
		{"sony identity", "Sony|abc123|0", "psn", "abc123", nil},
		{"ps4 identity", "PS4|abc123|0", "psn", "abc123", nil},
		{"ps5 identity", "PS5|abc123|0", "psn", "abc123", nil},
		{"xboxone identity", "XboxOne|gt|0", "xbl", "gt", nil},
		{"xbox identity", "Xbox|gt|0", "xbl", "gt", nil},
		{"nswitch identity", "NSwitch|name|0", "switch", "name", nil},
		{"switch identity", "Switch|name|0", "switch", "name", nil},
		{"already-slug steam", "steam", "", "", ErrUnsupportedPlatform}, // no id present
		{"epic identity", "Epic|Jhzer|0", "epic", "Jhzer", nil},
		{"empty input", "", "", "", ErrUnsupportedPlatform},
		{"garbage", "garbage", "", "", ErrUnsupportedPlatform},
		{"missing id", "Steam||0", "", "", ErrUnsupportedPlatform},
		{"single segment", "Steam|", "", "", ErrUnsupportedPlatform},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			slug, id, err := ResolveSlug(tc.input)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err: got %v, want %v", err, tc.wantErr)
			}
			if slug != tc.wantSlug || id != tc.wantID {
				t.Fatalf("got (%q,%q), want (%q,%q)", slug, id, tc.wantSlug, tc.wantID)
			}
		})
	}
}

func TestValidExplicitSlug(t *testing.T) {
	for _, ok := range []string{"steam", "psn", "xbl", "switch", "epic", "STEAM", "Psn", "EPIC"} {
		if !ValidExplicitSlug(ok) {
			t.Errorf("%q should be valid", ok)
		}
	}
	for _, bad := range []string{"foo", "", "  steam  "} {
		if ValidExplicitSlug(bad) {
			t.Errorf("%q should be invalid", bad)
		}
	}
}
