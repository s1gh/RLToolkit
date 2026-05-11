// Package plugincatalog contains the loose semver comparator and the
// catalog Manager used by the launcher to decide when an installed
// plugin has a newer version available.
package plugincatalog

import (
	"strconv"
	"strings"
)

// Compare returns -1, 0, or 1 for a < b, a == b, a > b. Inputs may
// have a leading "v", a missing patch component, or a "-pre" suffix.
// Unparseable inputs compare equal so a bad version string doesn't
// flag an update.
func Compare(a, b string) int {
	aMain, aPre := splitVer(a)
	bMain, bPre := splitVer(b)
	if c := cmpInts(aMain, bMain); c != 0 {
		return c
	}
	// Pre-release < release. Per semver: 1.0.0-rc < 1.0.0.
	switch {
	case aPre == "" && bPre == "":
		return 0
	case aPre == "":
		return 1
	case bPre == "":
		return -1
	}
	return cmpPre(aPre, bPre)
}

func splitVer(v string) ([]int, string) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	pre := ""
	if i := strings.Index(v, "-"); i >= 0 {
		pre = v[i+1:]
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	out := make([]int, 0, 3)
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			// Whole version is unparseable; wipe the pre-release too
			// so Compare's nil-guard short-circuits to "equal" rather
			// than leaking a pre-release suffix from a junk string.
			return nil, ""
		}
		out = append(out, n)
	}
	for len(out) < 3 {
		out = append(out, 0)
	}
	return out, pre
}

func cmpInts(a, b []int) int {
	if a == nil || b == nil {
		return 0
	}
	n := len(a)
	if len(b) > n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		var av, bv int
		if i < len(a) {
			av = a[i]
		}
		if i < len(b) {
			bv = b[i]
		}
		if av < bv {
			return -1
		}
		if av > bv {
			return 1
		}
	}
	return 0
}

func cmpPre(a, b string) int {
	ap, bp := strings.Split(a, "."), strings.Split(b, ".")
	n := len(ap)
	if len(bp) < n {
		n = len(bp)
	}
	for i := 0; i < n; i++ {
		an, aerr := strconv.Atoi(ap[i])
		bn, berr := strconv.Atoi(bp[i])
		if aerr == nil && berr == nil {
			if an != bn {
				if an < bn {
					return -1
				}
				return 1
			}
			continue
		}
		// Per semver: numeric identifiers have lower precedence than
		// alphanumeric ones. Treat one-side-numeric as "less than".
		if aerr == nil {
			return -1
		}
		if berr == nil {
			return 1
		}
		if ap[i] != bp[i] {
			if ap[i] < bp[i] {
				return -1
			}
			return 1
		}
	}
	if len(ap) < len(bp) {
		return -1
	}
	if len(ap) > len(bp) {
		return 1
	}
	return 0
}
