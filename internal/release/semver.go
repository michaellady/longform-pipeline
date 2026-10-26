// Package release decides what a merge to main releases: the next version,
// whether to release at all, and the plain-English title and summary the
// GitHub release carries. The release workflow runs it through
// cmd/release-meta; every git and gh call goes through injectable interfaces
// so the decision logic is tested from fixtures without a network.
package release

import (
	"fmt"
	"regexp"
	"strconv"
)

// Bump is a semver bump level.
type Bump string

const (
	Patch Bump = "patch"
	Minor Bump = "minor"
	Major Bump = "major"
)

// rank orders bump levels so the highest one among several PRs wins.
func (b Bump) rank() int {
	switch b {
	case Major:
		return 3
	case Minor:
		return 2
	case Patch:
		return 1
	}
	return 0
}

// ParseBump accepts patch, minor or major; "" and "auto" mean "not set".
func ParseBump(s string) (Bump, error) {
	switch s {
	case "", "auto":
		return "", nil
	case string(Patch), string(Minor), string(Major):
		return Bump(s), nil
	}
	return "", fmt.Errorf("bump %q: want patch, minor or major", s)
}

// Version is a plain vMAJOR.MINOR.PATCH release version. Pre-release and
// build suffixes are not used by this repo and are not parsed.
type Version struct{ Major, Minor, Patch int }

var versionRE = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

// ParseVersion parses "v1.2.3"; anything else (no "v", a suffix) is an error.
func ParseVersion(s string) (Version, error) {
	m := versionRE.FindStringSubmatch(s)
	if m == nil {
		return Version{}, fmt.Errorf("version %q: want vMAJOR.MINOR.PATCH, e.g. v0.3.0", s)
	}
	var v Version
	v.Major, _ = strconv.Atoi(m[1])
	v.Minor, _ = strconv.Atoi(m[2])
	v.Patch, _ = strconv.Atoi(m[3])
	return v, nil
}

func (v Version) String() string { return fmt.Sprintf("v%d.%d.%d", v.Major, v.Minor, v.Patch) }

// Less reports whether v sorts before o.
func (v Version) Less(o Version) bool {
	if v.Major != o.Major {
		return v.Major < o.Major
	}
	if v.Minor != o.Minor {
		return v.Minor < o.Minor
	}
	return v.Patch < o.Patch
}

// Next returns the version after v for the given bump.
func (v Version) Next(b Bump) Version {
	switch b {
	case Major:
		return Version{Major: v.Major + 1}
	case Minor:
		return Version{Major: v.Major, Minor: v.Minor + 1}
	default:
		return Version{Major: v.Major, Minor: v.Minor, Patch: v.Patch + 1}
	}
}

// Latest returns the highest vX.Y.Z among tags, ignoring tags that are not
// plain release versions (v1.0.0-rc1, v2, nightly). ok is false when none is.
func Latest(tags []string) (latest Version, ok bool) {
	for _, t := range tags {
		v, err := ParseVersion(t)
		if err != nil {
			continue
		}
		if !ok || latest.Less(v) {
			latest, ok = v, true
		}
	}
	return latest, ok
}
