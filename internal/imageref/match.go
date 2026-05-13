package imageref

import (
	"errors"
	"fmt"
	"strings"
)

// Match is the structured form of a match expression in a squirrel policy.
//
// Each field is an independent glob expressed in the alphabet documented
// for matchGlob. An empty field falls back to its per-field default:
//
//   - Registry   defaults to "*"   (any single hostname segment)
//   - Repository defaults to "**"  (any path, including multi-segment)
//   - Tag        defaults to "*"   (any tag)
//   - Digest     defaults to "*"   (any digest, including the empty string
//     produced when the input has no digest)
//
// A zero-value Match (all fields empty) therefore matches every Image.
type Match struct {
	Registry   string
	Repository string
	Tag        string
	Digest     string
}

// Matches reports whether the image matches this expression.
func (m Match) Matches(img Image) bool {
	return matchGlob(orDefault(m.Registry, "*"), img.Registry) &&
		matchGlob(orDefault(m.Repository, "**"), img.Repository) &&
		matchGlob(orDefault(m.Tag, "*"), img.Tag) &&
		matchGlob(orDefault(m.Digest, "*"), img.Digest)
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// ParseMatchString parses a glob-string match expression into the structured
// Match form. The grammar is:
//
//	match-string := registry-glob "/" repository-glob [ ":" tag-glob ] [ "@" digest-glob ]
//
// Both registry-glob and repository-glob are required, so the input must
// contain at least one `/` before any `:` or `@`. Tag and digest are
// optional and stay empty (matching everything via the per-field defaults)
// when omitted.
func ParseMatchString(s string) (Match, error) {
	if s == "" {
		return Match{}, errors.New("match string is empty")
	}

	var m Match

	// Strip digest portion first; everything after the last `@` is the
	// digest glob. A digest itself may contain a `:`, so this must run
	// before tag extraction. A trailing `@` with no digest after it is
	// a malformed input, not a request to match every digest.
	if i := strings.LastIndex(s, "@"); i >= 0 {
		m.Digest = s[i+1:]
		if m.Digest == "" {
			return Match{}, fmt.Errorf("match string %q has a trailing `@` with no digest", s)
		}
		s = s[:i]
	}

	// Tag is the substring after the last `:` that appears after the last
	// `/`. The qualifier disambiguates a registry-port colon
	// (e.g. `localhost:5000/foo`). A trailing `:` with no tag after it is
	// a malformed input, not a request to match every tag.
	slashIdx := strings.LastIndex(s, "/")
	colonIdx := strings.LastIndex(s, ":")
	if colonIdx > slashIdx {
		m.Tag = s[colonIdx+1:]
		if m.Tag == "" {
			return Match{}, fmt.Errorf("match string %q has a trailing `:` with no tag", s)
		}
		s = s[:colonIdx]
	}

	// Registry and repository split on the first `/`.
	registry, repository, ok := strings.Cut(s, "/")
	if !ok {
		return Match{}, fmt.Errorf("match string %q has no registry/repository separator", s)
	}
	m.Registry = registry
	m.Repository = repository

	if m.Registry == "" || m.Repository == "" {
		return Match{}, fmt.Errorf("match string %q has an empty registry or repository", s)
	}

	return m, nil
}
