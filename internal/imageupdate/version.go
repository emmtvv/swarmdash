// Package imageupdate finds newer versions of a service's image: it lists
// a repository's tags from its registry (registry.go) and picks the newest
// one that's a patch, minor or major upgrade of the tag currently deployed
// (this file).
package imageupdate

import (
	"regexp"
	"strconv"
)

// Policy is how far an automatic or one-click update may move a tag.
type Policy string

const (
	PolicyOff Policy = "off"
	// PolicyPatch allows any newer version with the same major and minor
	// version (1.2.3 -> 1.2.7, never 1.3.0).
	PolicyPatch Policy = "patch"
	// PolicyMinor allows any newer version with the same major version
	// (1.2.3 -> 1.4.0, never 2.0.0).
	PolicyMinor Policy = "minor"
	// PolicyMajor allows any newer version (1.2.3 -> 2.0.1).
	PolicyMajor Policy = "major"
)

// ParsePolicy validates a policy string from a form, label or setting.
func ParsePolicy(s string) (Policy, bool) {
	switch p := Policy(s); p {
	case PolicyOff, PolicyPatch, PolicyMinor, PolicyMajor:
		return p, true
	}
	return "", false
}

// tagPattern matches version-like tags: an optional "v", up to four dotted
// numbers, then an optional variant suffix starting with "-", "_" or "+"
// (1.27.3, v2.1, 16, 8.0.1-jdk17, 3.12.1-slim-bookworm).
var tagPattern = regexp.MustCompile(`^(v?)(\d+(?:\.\d+){0,3})([-_+].*)?$`)

// version is a parsed tag. Two tags are only ever compared when they have
// the same "shape" - prefix, number of components and suffix - so a
// service on nginx:1.27.1-alpine only moves to other "-alpine" tags with
// three components, never to 1.27 or 1.28.0 (Debian) or 1.29.0-rc1.
type version struct {
	prefix string
	nums   []int
	suffix string
}

func parseVersion(tag string) (version, bool) {
	m := tagPattern.FindStringSubmatch(tag)
	if m == nil {
		return version{}, false
	}
	v := version{prefix: m[1], suffix: m[3]}
	start := 0
	s := m[2]
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == '.' {
			n, err := strconv.Atoi(s[start:i])
			if err != nil {
				return version{}, false
			}
			v.nums = append(v.nums, n)
			start = i + 1
		}
	}
	return v, true
}

func (v version) sameShape(o version) bool {
	return v.prefix == o.prefix && v.suffix == o.suffix && len(v.nums) == len(o.nums)
}

// compare returns -1, 0 or 1; only meaningful for versions of the same shape.
func (v version) compare(o version) int {
	for i := range v.nums {
		switch {
		case v.nums[i] < o.nums[i]:
			return -1
		case v.nums[i] > o.nums[i]:
			return 1
		}
	}
	return 0
}

// IsVersionTag reports whether tag looks like a version this package can
// upgrade (so "latest", "stable" or "main" are not).
func IsVersionTag(tag string) bool {
	_, ok := parseVersion(tag)
	return ok
}

// Newest returns the newest tag in tags that's an upgrade of current under
// policy, or "" if there's none (or current isn't a version tag, or the
// policy is off).
func Newest(current string, tags []string, policy Policy) string {
	// fixed is how many leading components must stay the same.
	var fixed int
	switch policy {
	case PolicyPatch:
		fixed = 2
	case PolicyMinor:
		fixed = 1
	case PolicyMajor:
		fixed = 0
	default:
		return ""
	}
	cur, ok := parseVersion(current)
	if !ok {
		return ""
	}
	best, bestTag := cur, ""
	for _, t := range tags {
		v, ok := parseVersion(t)
		if !ok || !v.sameShape(cur) {
			continue
		}
		if !samePrefix(v.nums, cur.nums, fixed) {
			continue
		}
		if v.compare(best) > 0 {
			best, bestTag = v, t
		}
	}
	return bestTag
}

// samePrefix reports whether a and b (of equal length) agree on their first
// n components.
func samePrefix(a, b []int, n int) bool {
	for i := 0; i < n && i < len(a); i++ {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
