// Semver function versions.
//
// actions.toml may declare `version = "1.2.3"` or `bump = "major"|"minor"|"patch"`
// (mutually exclusive). Absent both, the server bumps patch. The first deploy
// of a function starts at 0.1.0.
//
// Legacy versions are bare counters ("v1", "v2"); they map to N.0.0 for
// comparison, so v3 < 3.0.1 and the next auto version after v3 is 3.0.1.
// Explicit versions must be strictly greater than the active version.
package deploy

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Version is a parsed semantic version (prerelease supported, build ignored).
type Version struct {
	Major, Minor, Patch int
	Pre                 string
	Raw                 string // normalized "M.m.p[-pre]"
}

var semverRe = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?(?:\+[0-9A-Za-z.-]+)?$`)
var legacyRe = regexp.MustCompile(`^v(\d+)$`)
var legacyMinorRe = regexp.MustCompile(`^v?(\d+)\.(\d+)$`)

// ParseVersion normalizes s to semver. Accepts a leading "v", two-part
// "M.m" (= M.m.0), legacy bare counters ("v3" = 3.0.0), and ignores build
// metadata suffixes.
func ParseVersion(s string) (Version, error) {
	t := strings.TrimSpace(s)
	if m := semverRe.FindStringSubmatch(t); m != nil {
		maj, _ := strconv.Atoi(m[1])
		min, _ := strconv.Atoi(m[2])
		patch, _ := strconv.Atoi(m[3])
		v := Version{Major: maj, Minor: min, Patch: patch, Pre: m[4]}
		v.Raw = fmt.Sprintf("%d.%d.%d", maj, min, patch)
		if m[4] != "" {
			v.Raw += "-" + m[4]
		}
		return v, nil
	}
	if m := legacyRe.FindStringSubmatch(t); m != nil {
		n, _ := strconv.Atoi(m[1])
		return Version{Major: n, Raw: fmt.Sprintf("%d.0.0", n)}, nil
	}
	if m := legacyMinorRe.FindStringSubmatch(t); m != nil {
		maj, _ := strconv.Atoi(m[1])
		min, _ := strconv.Atoi(m[2])
		return Version{Major: maj, Minor: min, Raw: fmt.Sprintf("%d.%d.0", maj, min)}, nil
	}
	return Version{}, fmt.Errorf("invalid version %q (want semver like 1.2.3)", s)
}

// Compare returns -1/0/+1. Numeric fields first; a release outranks any
// prerelease of the same triple; prereleases compare identifier by identifier
// (numeric identifiers numerically, numeric < alphanumeric, else lexically).
func Compare(a, b Version) int {
	for _, pair := range [][2]int{{a.Major, b.Major}, {a.Minor, b.Minor}, {a.Patch, b.Patch}} {
		if pair[0] != pair[1] {
			if pair[0] < pair[1] {
				return -1
			}
			return 1
		}
	}
	switch {
	case a.Pre == b.Pre:
		return 0
	case a.Pre == "":
		return 1
	case b.Pre == "":
		return -1
	}
	ai, bi := strings.Split(a.Pre, "."), strings.Split(b.Pre, ".")
	for i := 0; i < len(ai) && i < len(bi); i++ {
		an, aerr := strconv.Atoi(ai[i])
		bn, berr := strconv.Atoi(bi[i])
		switch {
		case aerr == nil && berr == nil:
			if an != bn {
				if an < bn {
					return -1
				}
				return 1
			}
		case aerr == nil:
			return -1 // numeric < alphanumeric
		case berr == nil:
			return 1
		default:
			if ai[i] != bi[i] {
				if ai[i] < bi[i] {
					return -1
				}
				return 1
			}
		}
	}
	switch {
	case len(ai) < len(bi):
		return -1
	case len(ai) > len(bi):
		return 1
	}
	return 0
}

// Bump returns v incremented at level (major|minor|patch), clearing prerelease.
func (v Version) Bump(level string) Version {
	switch level {
	case "major":
		return Version{Major: v.Major + 1, Raw: fmt.Sprintf("%d.0.0", v.Major+1)}
	case "minor":
		return Version{Major: v.Major, Minor: v.Minor + 1, Raw: fmt.Sprintf("%d.%d.0", v.Major, v.Minor+1)}
	default: // patch
		return Version{Major: v.Major, Minor: v.Minor, Patch: v.Patch + 1,
			Raw: fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch+1)}
	}
}

// DockerTag renders v safe for image tags ("+" is illegal in tags).
func (v Version) DockerTag() string {
	return strings.ReplaceAll(v.Raw, "+", "-")
}

// DockerTagFor is DockerTag for an already-normalized version string.
func DockerTagFor(normalized string) string {
	return strings.ReplaceAll(normalized, "+", "-")
}
