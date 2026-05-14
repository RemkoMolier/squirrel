// Package imageref parses, normalises, matches, and renders OCI container
// image references for the squirrel operator.
//
// It wraps github.com/google/go-containerregistry/pkg/name for parsing and
// normalisation and exposes:
//
//   - the Image four-tuple (Registry, Repository, Tag, Digest) used everywhere
//     downstream of admission decoding;
//   - glob matching with `*` (no `/`) and `**` (any) over individual fields
//     and over the joined reference;
//   - specificity scoring used as the default rule priority;
//   - template rendering with the placeholder grammar documented in
//     docs/design/v1alpha1.md.
//
// Behaviour is exhaustively defined by the table-driven tests in this package.
package imageref
