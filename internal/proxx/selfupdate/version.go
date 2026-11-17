package selfupdate

import (
	"strconv"
	"strings"
)

// version is a parsed proxx release version: MAJOR.MINOR.PATCH[-proxx.N][-rcM].
type version struct {
	nums  [3]int
	proxx int
	rc    int // 0 = final release; >0 = release candidate number
}

// parseVersion accepts an optional leading "v". It returns ok=false for non-release versions such as "dev".
func parseVersion(s string) (version, bool) {
	var v version
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if s == "" {
		return v, false
	}
	core, rest, _ := strings.Cut(s, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return v, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return v, false
		}
		v.nums[i] = n
	}
	for rest != "" {
		var tok string
		tok, rest, _ = strings.Cut(rest, "-")
		tok = strings.ToLower(tok)
		switch {
		case strings.HasPrefix(tok, "proxx."):
			n, err := strconv.Atoi(strings.TrimPrefix(tok, "proxx."))
			if err != nil || n < 0 {
				return v, false
			}
			v.proxx = n
		case strings.HasPrefix(tok, "rc"):
			n, err := strconv.Atoi(strings.TrimPrefix(strings.TrimPrefix(tok, "rc"), "."))
			if err != nil || n < 0 {
				return v, false
			}
			v.rc = n + 1 // rc0 still sorts below the final release
		default:
			return v, false
		}
	}
	return v, true
}

// compareVersions returns -1, 0 or 1. ok is false when either side is not a release version.
func compareVersions(a, b string) (int, bool) {
	va, okA := parseVersion(a)
	vb, okB := parseVersion(b)
	if !okA || !okB {
		return 0, false
	}
	cmp := func(x, y int) int {
		switch {
		case x < y:
			return -1
		case x > y:
			return 1
		}
		return 0
	}
	for i := 0; i < 3; i++ {
		if c := cmp(va.nums[i], vb.nums[i]); c != 0 {
			return c, true
		}
	}
	if c := cmp(va.proxx, vb.proxx); c != 0 {
		return c, true
	}
	// A release candidate sorts below its final release.
	switch {
	case va.rc == vb.rc:
		return 0, true
	case va.rc == 0:
		return 1, true
	case vb.rc == 0:
		return -1, true
	}
	return cmp(va.rc, vb.rc), true
}

// isNewer reports whether candidate is strictly newer than current. Unparseable versions never are.
func isNewer(candidate, current string) bool {
	c, ok := compareVersions(candidate, current)
	return ok && c > 0
}
