package imageref

import (
	"fmt"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
)

// Image is the normalised four-tuple form of a container image reference.
//
// Registry is the canonical hostname presented as `docker.io` for Docker Hub
// (pkg/name returns `index.docker.io` internally and we canonicalise it here
// so match expressions and templates can use the friendlier form).
// Repository is the path part, e.g. `library/nginx`.
// Tag is empty only when the input pinned the image by digest alone;
// otherwise it carries the explicit tag, or `latest` for short forms.
// Digest is empty when no `@sha256:...` was specified.
type Image struct {
	Registry   string
	Repository string
	Tag        string
	Digest     string
}

// Parse parses and normalises an OCI image reference.
//
// Defaults are applied per the squirrel design (docs/design/v1alpha1.md):
//
//   - short forms such as `nginx` become `docker.io/library/nginx:latest`;
//   - a missing tag is filled with `latest` only if no digest was specified;
//   - a missing digest is left empty;
//   - the Docker Hub default registry `index.docker.io` is canonicalised to
//     `docker.io` so match globs and templates write the friendlier form.
//
// References that carry both a tag and a digest preserve both values; the
// underlying pkg/name parser drops the tag in that case, so we recover it
// from the original input string.
func Parse(ref string) (Image, error) {
	parsed, err := name.ParseReference(
		ref,
		name.WithDefaultRegistry(name.DefaultRegistry),
		name.WithDefaultTag(name.DefaultTag),
	)
	if err != nil {
		return Image{}, fmt.Errorf("parse image reference %q: %w", ref, err)
	}

	img := Image{
		Registry:   parsed.Context().RegistryStr(),
		Repository: parsed.Context().RepositoryStr(),
	}

	// pkg/name uses `index.docker.io` for the Docker Hub default; users
	// write `docker.io` in policies, so canonicalise here once.
	if img.Registry == name.DefaultRegistry {
		img.Registry = "docker.io"
	}

	// pkg/name returns either name.Tag or name.Digest; when a reference
	// carries both, the tag is dropped from the returned Reference. Recover
	// both values by scanning the original input ourselves.
	img.Tag, img.Digest = splitTagAndDigest(ref)
	if img.Tag == "" && img.Digest == "" {
		img.Tag = name.DefaultTag
	}

	return img, nil
}

// splitTagAndDigest extracts the tag and digest from a raw reference string,
// preserving both when present. A registry port colon (e.g. `localhost:5000`)
// is disambiguated by requiring the tag's colon to appear after the last
// path separator.
func splitTagAndDigest(ref string) (tag, digest string) {
	s := ref
	if i := strings.LastIndex(s, "@"); i >= 0 {
		digest = s[i+1:]
		s = s[:i]
	}
	slashIdx := strings.LastIndex(s, "/")
	colonIdx := strings.LastIndex(s, ":")
	if colonIdx > slashIdx {
		tag = s[colonIdx+1:]
	}
	return
}
