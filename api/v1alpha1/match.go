package v1alpha1

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// Match is the structured form of a match expression.
//
// Each field is an independent glob expressed in the alphabet documented
// for internal/imageref.matchGlob (`*` no slash, `**` any). An empty
// field falls back to its per-field default:
//
//	Registry   -> "*"
//	Repository -> "**"
//	Tag        -> "*"
//	Digest     -> "*"
//
// so a zero-value Match matches every image. The reconciler validates
// individual fields at policy creation; admission applies them against
// the input image's normalised four-tuple.
type Match struct {
	// Registry is the registry-hostname glob (e.g. "docker.io", "gcr.io",
	// "*"). Empty matches any registry.
	// +optional
	Registry string `json:"registry,omitempty"`

	// Repository is the repository-path glob (e.g. "library/**",
	// "google_containers/etcd"). Empty matches any repository.
	// +optional
	Repository string `json:"repository,omitempty"`

	// Tag is the tag glob (e.g. "1.21", "1.*", "*"). Empty matches any
	// tag, including the empty tag a digest-pinned image carries.
	// +optional
	Tag string `json:"tag,omitempty"`

	// Digest pins the matcher to a specific image digest. Three shapes
	// are accepted:
	//
	//   - Empty: matches images regardless of whether they carry a
	//     digest. This is the common case.
	//   - "*": bare wildcard. Matches any digest, equivalent to empty
	//     for purposes of admission filtering.
	//   - "<algorithm>:<hex>": a literal content-addressed digest such
	//     as "sha256:abc123...". Pins the matcher to that exact image.
	//
	// Partial-prefix globs such as "sha256:abc*" are explicitly
	// rejected by the reconciler's validation pass (see
	// internal/controller.isValidDigestMatch). The check lives in
	// the reconciler rather than the CRD because Match is stored as
	// raw JSON on Rule.Match, so per-field CEL gates do not reach it
	// - a policy with a bad digest pattern lands in etcd, gets
	// Accepted=False with reason InvalidMatch, and the webhook skips
	// every rule in the policy until the operator fixes it.
	//
	// +optional
	Digest string `json:"digest,omitempty"`
}

// MatchExpr is the match expression on a Rule. Two YAML / JSON forms are
// accepted:
//
//   - A glob string covering the whole reference:
//
//     match: "docker.io/library/**:*"
//
//   - A structured object:
//
//     match:
//     registry: docker.io
//     repository: library/**
//
// At reconcile time the string form is parsed into the structured form
// via internal/imageref.ParseMatchString; both forms then go through the
// same matching path. Exactly one of String / Structured is set after a
// successful unmarshal.
//
// The Schemaless / XPreserveUnknownFields kubebuilder markers must be
// placed at the *use-site* (the Rule.Match field) rather than on this
// type, otherwise controller-gen emits `type: object` in the generated
// CRD and the API server rejects the documented string form
// (`match: "docker.io/**:*"`) before the custom UnmarshalJSON can run.
type MatchExpr struct {
	// String holds the glob-string form when set. Mutually exclusive with
	// Structured.
	String string `json:"-"`

	// Structured holds the per-field glob form when set. Mutually
	// exclusive with String.
	Structured *Match `json:"-"`
}

// UnmarshalJSON accepts either a JSON string (glob form) or a JSON object
// (structured form). `null` is explicitly rejected: the design requires
// match to be a glob string or a structured object, and the CRD's CEL
// rule rejects null at admission time. We reject it here too so callers
// that bypass live API-server validation - fake clients, fixtures, raw
// etcd payloads, data persisted before the CEL rule was introduced -
// see the same error path as live admission, rather than silently
// resolving to a zero MatchExpr that the matcher would treat as a
// match-everything catch-all.
func (m *MatchExpr) UnmarshalJSON(data []byte) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return errors.New("match: null is not a valid match payload; expected a glob string or a structured object")
	}

	// Try the glob-string form first; if the input looks like a JSON
	// string it cannot also be a JSON object, so this disambiguates.
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		// An empty string is indistinguishable from a zero MatchExpr
		// (String=="" and Structured==nil), so callers using
		// MatchExpression() as the rule-validity gate cannot tell
		// "match was set to the empty string" from "match was unset".
		// The documented string form requires a `<registry>/<repository>`
		// glob and imageref.ParseMatchString rejects an empty input, so
		// surface that here as a parse error rather than silently
		// producing a match-everything zero value.
		if s == "" {
			return errors.New("match: empty string is not a valid match payload; expected a glob of the form <registry>/<repository>[:<tag>][@<digest>] or a structured object")
		}
		m.String = s
		m.Structured = nil
		return nil
	}

	// Otherwise expect an object. The CRD's schema for `match` is
	// schemaless to accommodate the string-or-object union, which means
	// the API server cannot reject unknown fields before this
	// unmarshaller runs. Use a strict decoder so a typo such as
	// `registrry` is surfaced as an error rather than silently leaving a
	// zero-valued Match - which would default to matching every image
	// and turn a narrow rule into a broad one.
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var sm Match
	if err := dec.Decode(&sm); err != nil {
		return fmt.Errorf("match: expected a glob string or a structured object, got %s: %w", data, err)
	}
	m.String = ""
	m.Structured = &sm
	return nil
}

// MarshalJSON emits the string form when set, otherwise the structured
// form. An unset MatchExpr marshals to `null`.
func (m MatchExpr) MarshalJSON() ([]byte, error) {
	if m.String != "" {
		out, err := json.Marshal(m.String)
		if err != nil {
			return nil, fmt.Errorf("marshal MatchExpr string form: %w", err)
		}
		return out, nil
	}
	if m.Structured != nil {
		out, err := json.Marshal(*m.Structured)
		if err != nil {
			return nil, fmt.Errorf("marshal MatchExpr structured form: %w", err)
		}
		return out, nil
	}
	return []byte("null"), nil
}
