package imageref

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// placeholderShapeRe detects any `{...}` shape regardless of whether the
// body is a well-formed placeholder. parsePlaceholder then enforces the
// strict identifier grammar, so malformed shapes such as `{digest:}` or
// `{repository-name}` surface as validation errors rather than passing
// through as literal text and breaking the rendered reference at
// admission time.
//
// No escape syntax: the OCI distribution spec forbids `{` and `}` in
// every reference component (registry hostname, repository path,
// tag, and digest), so a target template never needs to emit a
// literal brace. An accidentally-bare `{` or `}` in the template is
// therefore unambiguously an operator error, and {{/}} escape
// sequences would be a footgun rather than a feature. Templates
// that need a brace-shaped literal can use a different rewriter
// upstream of squirrel.
var placeholderShapeRe = regexp.MustCompile(`\{[^}]*\}`)

// ValidateFieldTemplate checks that a template intended for the registry
// or repository field of a target uses only known placeholders. It does
// not require an Image because validation runs at reconcile time, before
// any admission request.
func ValidateFieldTemplate(template string) error {
	return validateTemplate(template, true)
}

// ValidateTagTemplate checks that a template intended for a tag list entry
// uses only known placeholders. It additionally rejects the bare `{digest}`
// placeholder, which always produces an OCI-invalid tag (digests carry a
// `:` separator).
func ValidateTagTemplate(template string) error {
	return validateTemplate(template, false)
}

func validateTemplate(template string, allowBareDigest bool) error {
	var firstErr error
	placeholderShapeRe.ReplaceAllStringFunc(template, func(match string) string {
		if firstErr != nil {
			return match
		}
		p, err := parsePlaceholder(match[1 : len(match)-1])
		if err != nil {
			firstErr = err
			return match
		}
		if p.name == "digest" && p.sub == "" && !allowBareDigest {
			firstErr = errors.New("placeholder {digest} is not valid in tag templates; use {digest:hex}, {digest:shortN}, or {digest:algo}")
		}
		return match
	})
	if firstErr != nil {
		return firstErr
	}
	// After all well-formed `{...}` shapes have been validated, anything
	// left over that still contains `{` or `}` is an unmatched brace - a
	// template like `mirror/{repository:image` would otherwise pass with
	// no shapes detected and only fail later at render time.
	if stripped := placeholderShapeRe.ReplaceAllString(template, ""); strings.ContainsAny(stripped, "{}") {
		return fmt.Errorf("malformed template %q: contains an unmatched `{` or `}`", template)
	}
	return nil
}

// RenderField expands a template against an Image, substituting placeholders
// with the corresponding components of the matched image. It is intended for
// the registry and repository fields of a target; the bare `{digest}` form
// is permitted.
//
// Substitutions that produce an empty string (for example {repository:owner}
// against a single-segment repository, or {digest:short8} against an image
// with no digest) are inserted as-is. The caller is responsible for judging
// whether the assembled reference is a valid OCI image reference; if not,
// the engine treats the rule as non-applicable for the container.
func RenderField(template string, img Image) (string, error) {
	return renderTemplate(template, img, true)
}

// RenderTag expands a template against an Image for use as a tag list entry.
// Like RenderField except that the bare `{digest}` placeholder is rejected
// because OCI tags forbid `:`.
func RenderTag(template string, img Image) (string, error) {
	return renderTemplate(template, img, false)
}

func renderTemplate(template string, img Image, allowBareDigest bool) (string, error) {
	var firstErr error
	out := placeholderShapeRe.ReplaceAllStringFunc(template, func(match string) string {
		if firstErr != nil {
			return match
		}
		p, err := parsePlaceholder(match[1 : len(match)-1])
		if err != nil {
			firstErr = err
			return match
		}
		value, err := resolvePlaceholder(p, img, allowBareDigest)
		if err != nil {
			firstErr = err
			return match
		}
		return value
	})
	if firstErr != nil {
		return out, firstErr
	}
	// Defence in depth: if validation was skipped, refuse to emit literal
	// braces into the rewritten reference. The caller treats this as a
	// non-applicable rule and falls through. We check `out` rather
	// than `template` so the predicate matches the stated intent
	// (about what we are emitting, not what we received); the two
	// are equivalent in practice because the OCI distribution spec
	// forbids `{` and `}` in registry, repository, tag, and digest
	// components, so a substituted placeholder cannot reintroduce
	// braces into `out` that the input did not contain.
	if strings.ContainsAny(out, "{}") {
		return out, fmt.Errorf("malformed template %q: contains an unmatched `{` or `}`", template)
	}
	return out, nil
}

// placeholder is the parsed form of `{name}` or `{name:sub}`.
type placeholder struct {
	name string
	sub  string
}

// parsePlaceholder splits a placeholder body on `:` and validates that the
// (name, sub) pair is one we recognise. A colon with no sub-form after it
// (e.g. `{digest:}`) is malformed and rejected, as are bodies that fail
// validatePlaceholderName's identifier grammar.
func parsePlaceholder(body string) (placeholder, error) {
	name, sub, hadColon := strings.Cut(body, ":")
	if hadColon && sub == "" {
		return placeholder{}, fmt.Errorf("malformed placeholder {%s:}: the colon must be followed by a sub-form", name)
	}
	p := placeholder{name: name, sub: sub}
	if err := validatePlaceholderName(p); err != nil {
		return placeholder{}, err
	}
	return p, nil
}

func validatePlaceholderName(p placeholder) error {
	switch p.name {
	case "registry", "tag":
		if p.sub != "" {
			return fmt.Errorf("unknown placeholder {%s:%s}", p.name, p.sub)
		}
		return nil
	case "repository":
		switch p.sub {
		case "", "owner", "image", "path", "flat":
			return nil
		default:
			return fmt.Errorf("unknown placeholder {repository:%s}", p.sub)
		}
	case "digest":
		switch {
		case p.sub == "":
			return nil
		case p.sub == "hex", p.sub == "algo":
			return nil
		case strings.HasPrefix(p.sub, "short"):
			n, err := strconv.Atoi(p.sub[len("short"):])
			if err != nil {
				return fmt.Errorf("invalid placeholder {digest:%s}: short must be followed by a positive integer", p.sub)
			}
			if n <= 0 {
				return fmt.Errorf("invalid placeholder {digest:short%d}: N must be a positive integer", n)
			}
			return nil
		default:
			return fmt.Errorf("unknown placeholder {digest:%s}", p.sub)
		}
	default:
		if p.sub != "" {
			return fmt.Errorf("unknown placeholder {%s:%s}", p.name, p.sub)
		}
		return fmt.Errorf("unknown placeholder {%s}", p.name)
	}
}

// resolvePlaceholder returns the substituted value for a placeholder against
// the input image. The validation guarantees the (name, sub) pair is known.
func resolvePlaceholder(p placeholder, img Image, allowBareDigest bool) (string, error) {
	switch p.name {
	case "registry":
		return img.Registry, nil
	case "tag":
		return img.Tag, nil
	case "repository":
		return repositorySubform(p.sub, img.Repository), nil
	case "digest":
		if p.sub == "" {
			if !allowBareDigest {
				return "", errors.New("placeholder {digest} is not valid in tag templates")
			}
			return img.Digest, nil
		}
		return digestSubform(p.sub, img.Digest), nil
	default:
		return "", fmt.Errorf("unknown placeholder {%s}", p.name)
	}
}

func repositorySubform(sub, repo string) string {
	switch sub {
	case "":
		return repo
	case "owner":
		if before, _, found := strings.Cut(repo, "/"); found {
			return before
		}
		return ""
	case "image":
		if i := strings.LastIndex(repo, "/"); i >= 0 {
			return repo[i+1:]
		}
		return repo
	case "path":
		if i := strings.LastIndex(repo, "/"); i >= 0 {
			return repo[:i]
		}
		return ""
	case "flat":
		return strings.ReplaceAll(repo, "/", "-")
	}
	return repo
}

func digestSubform(sub, digest string) string {
	if digest == "" {
		return ""
	}
	algo, hex, ok := strings.Cut(digest, ":")
	if !ok {
		algo = ""
		hex = digest
	}
	switch {
	case sub == "hex":
		return hex
	case sub == "algo":
		return algo
	case strings.HasPrefix(sub, "short"):
		n, err := strconv.Atoi(sub[len("short"):])
		if err != nil || n <= 0 || n > len(hex) {
			return ""
		}
		return hex[:n]
	}
	return ""
}
