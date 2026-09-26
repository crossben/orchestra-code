package update

import (
	"fmt"
	"strconv"
	"strings"
)

// semver is a parsed MAJOR.MINOR.PATCH[-PRERELEASE] version. Build metadata
// (+...) is ignored, as semver requires.
type semver struct {
	nums [3]int
	pre  []string
}

// parseVersion accepts an optional leading "v" and a missing patch ("1.2").
func parseVersion(s string) (semver, error) {
	var v semver
	raw := s
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	if i := strings.IndexByte(s, '-'); i >= 0 {
		if s[i+1:] == "" {
			return v, fmt.Errorf("invalid version %q", raw)
		}
		v.pre = strings.Split(s[i+1:], ".")
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return v, fmt.Errorf("invalid version %q", raw)
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || p == "" || p[0] == '+' {
			return v, fmt.Errorf("invalid version %q", raw)
		}
		v.nums[i] = n
	}
	return v, nil
}

func (v semver) prerelease() bool { return len(v.pre) > 0 }

// snapshot reports a GoReleaser snapshot build ("<next patch>-next").
func (v semver) snapshot() bool {
	return len(v.pre) > 0 && v.pre[0] == "next"
}

func (v semver) compare(o semver) int {
	for i := 0; i < 3; i++ {
		if v.nums[i] != o.nums[i] {
			return sign(v.nums[i] - o.nums[i])
		}
	}
	// A release ranks above any prerelease of the same version.
	switch {
	case !v.prerelease() && !o.prerelease():
		return 0
	case !v.prerelease():
		return 1
	case !o.prerelease():
		return -1
	}
	for i := 0; i < len(v.pre) && i < len(o.pre); i++ {
		if c := comparePreIdent(v.pre[i], o.pre[i]); c != 0 {
			return c
		}
	}
	return sign(len(v.pre) - len(o.pre))
}

// comparePreIdent follows semver §11 for well-formed identifiers: numeric ones
// compare numerically and rank below alphanumeric ones. Alphanumeric
// identifiers with a trailing number ("rc2" vs "rc10") compare that number
// numerically, which matches how tags like v1.0.0-rc10 are usually meant.
func comparePreIdent(a, b string) int {
	na, ea := strconv.Atoi(a)
	nb, eb := strconv.Atoi(b)
	switch {
	case ea == nil && eb == nil:
		return sign(na - nb)
	case ea == nil:
		return -1
	case eb == nil:
		return 1
	}
	pa, sa := splitTrailingNum(a)
	pb, sb := splitTrailingNum(b)
	if pa == pb && sa != "" && sb != "" {
		x, _ := strconv.Atoi(sa)
		y, _ := strconv.Atoi(sb)
		return sign(x - y)
	}
	return strings.Compare(a, b)
}

func splitTrailingNum(s string) (prefix, num string) {
	i := len(s)
	for i > 0 && s[i-1] >= '0' && s[i-1] <= '9' {
		i--
	}
	return s[:i], s[i:]
}

func sign(n int) int {
	switch {
	case n > 0:
		return 1
	case n < 0:
		return -1
	}
	return 0
}

// Compare returns -1, 0 or 1 as version a is lower than, equal to or higher
// than b. Both may carry a leading "v".
func Compare(a, b string) (int, error) {
	va, err := parseVersion(a)
	if err != nil {
		return 0, err
	}
	vb, err := parseVersion(b)
	if err != nil {
		return 0, err
	}
	return va.compare(vb), nil
}

// Checkable reports whether a build of this version should look for updates.
// Development builds ("dev", unparseable) and GoReleaser snapshots ("-next")
// never do: there is no meaningful release to compare them with.
func Checkable(current string) bool {
	v, err := parseVersion(current)
	return err == nil && !v.snapshot()
}
