package imageref

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
