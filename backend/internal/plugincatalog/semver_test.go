package plugincatalog

import "testing"

func TestCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"1.0.1", "1.0.0", 1},
		{"1.0.0", "1.0.1", -1},
		{"2.0.0", "1.99.99", 1},
		{"1.10.0", "1.9.0", 1},
		{"v1.0.0", "1.0.0", 0},
		{"1.0", "1.0.0", 0},
		{"1.0.0-rc.1", "1.0.0", -1},
		{"1.0.0", "1.0.0-rc.1", 1},
		{"1.0.0-rc.2", "1.0.0-rc.1", 1},
		{"1.0.0-rc.10", "1.0.0-rc.2", 1},
		{"garbage", "1.0.0", 0}, // unparsable falls back to equal
		{"", "1.0.0", 0},        // empty input is unparsable
		{"", "", 0},
		{"1.0.-1", "1.0.0", 0}, // negative number is unparsable; no leak via pre-release
		{"-rc.1", "1.0.0", 0},  // leading hyphen with no main version
		// Mixed numeric/alpha pre-release: numeric has lower precedence
		// per semver, so "alpha" > "1".
		{"1.0.0-1", "1.0.0-alpha", -1},
		{"1.0.0-alpha", "1.0.0-1", 1},
	}
	for _, tc := range cases {
		if got := Compare(tc.a, tc.b); got != tc.want {
			t.Errorf("Compare(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}
