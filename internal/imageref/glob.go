package imageref

import (
	"fmt"
	"sync"

	"github.com/gobwas/glob"
)

// matchGlob reports whether the given pattern matches the input under
// the glob alphabet documented in docs/design/v1alpha1.md, which is the
// full alphabet of github.com/gobwas/glob compiled with `/` as the path
// separator:
//
//   - `*` matches any sequence of characters not containing `/`.
//   - `**` matches any sequence of characters, including `/`, including
//     the empty sequence.
//   - `?` matches any single character not containing `/`.
//   - `[abc]`, `[a-z]` match a single character drawn from the set or
//     range. `[!abc]` (or `[^abc]`) matches a single character outside
//     the set.
//   - `{foo,bar,baz}` matches any of the comma-separated alternatives.
//     Each alternative is itself a glob and may contain wildcards.
//   - `\<x>` escapes `<x>` so the next character is treated as literal.
//
// The matcher is case-sensitive. Exposing the full alphabet is safe
// because OCI image references forbid every punctuation character that
// drives these features (`?`, `[`, `]`, `{`, `}`, `\` are not legal in
// any of registry, repository, tag, or digest per the OCI distribution
// spec), so a policy that puts one of those characters in a match
// field is unambiguously asking for the glob interpretation.
//
// Compiled patterns are cached in a process-wide sync.Map so admission-
// time matching of a single rule against many containers reuses one
// compiled trie.
func matchGlob(pattern, input string) bool {
	if !containsGlobMetachar(pattern) {
		// Fast path: a pattern with no glob metacharacters is a literal
		// reference, and OCI references forbid every metacharacter we
		// look for, so this is the common case for skip-action policies
		// targeting a single image.
		return pattern == input
	}
	if input == "" {
		// Empty-input handling lives fully in patternMatchesEmpty
		// rather than gobwas: the underlying matcher accepts empty
		// for `?` / `[!a]` that should require a character (the
		// original bug), AND it rejects empty for alternations like
		// `{*,sha256:*}` whose `*` branch should match empty (the
		// inverse quirk - gobwas appears to commit to the longest
		// alternative). patternMatchesEmpty is authoritative for both.
		return patternMatchesEmpty(pattern)
	}
	g, err := compiledGlob(pattern)
	if err != nil {
		return false
	}
	return g.Match(input)
}

// patternMatchesEmpty reports whether pattern can match an empty input
// under our documented glob semantics. Three shapes match empty:
//
//   - a pattern composed entirely of `*` characters (including the
//     bare `*` and `**`);
//   - the empty pattern itself;
//   - an alternation `{a,b,...}` where at least one alternative
//     recursively matches empty - so `{*}`, `{*,sha256:*}`,
//     `{a,*,b}`, and the nested `{a,{*,b}}` all qualify even though
//     they contain non-`*` characters.
//
// Nesting is handled by splitOnTopLevelComma, which respects brace
// depth so `{a,{b,c}}` splits as `[a, {b,c}]` rather than as the
// naive `strings.Split` shape `[a, {b, c}]`. Nothing else in our
// published alphabet can consume zero characters.
func patternMatchesEmpty(pattern string) bool {
	if len(pattern) >= 2 && pattern[0] == '{' && pattern[len(pattern)-1] == '}' {
		for _, alt := range splitOnTopLevelComma(pattern[1 : len(pattern)-1]) {
			if patternMatchesEmpty(alt) {
				return true
			}
		}
		return false
	}
	for i := 0; i < len(pattern); i++ {
		if pattern[i] != '*' {
			return false
		}
	}
	return true
}

// splitOnTopLevelComma splits an alternation body on commas that
// appear at brace depth zero. `{a,b}` splits as `[a, b]`; the nested
// case `a,{b,c},d` splits as `[a, {b,c}, d]` rather than chopping
// inside the inner alternation.
//
// Unbalanced braces are tolerated and treated as literal characters
// in the depth tracking. This is by design: the only caller is
// patternMatchesEmpty, which gates the alternation handling behind
// a balanced-outer-`{...}` check, and the upstream gobwas/glob
// compiler rejects an unbalanced pattern at compile time anyway, so
// the function never receives such an input in practice. Returning
// an error here would force every caller through an error path
// that never fires.
func splitOnTopLevelComma(body string) []string {
	var parts []string
	depth := 0
	start := 0
	for i, r := range body {
		switch r {
		case '{':
			depth++
		case '}':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				parts = append(parts, body[start:i])
				start = i + 1
			}
		}
	}
	parts = append(parts, body[start:])
	return parts
}

// containsGlobMetachar reports whether pattern uses any character that
// triggers a glob-time interpretation under our published alphabet
// (`*`, `?`, `[`, `{`, `\`). The fast path in matchGlob relies on this
// to skip the gobwas/glob compile/match round-trip for literal
// references such as `docker.io/library/distroless-base:1`.
func containsGlobMetachar(pattern string) bool {
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '*', '?', '[', '{', '\\':
			return true
		}
	}
	return false
}

// ValidateGlob reports whether the pattern compiles under the alphabet
// matchGlob accepts. Reconcilers call this to reject unbalanced
// brackets, unterminated alternations, and other gobwas/glob compile
// errors at policy creation time. Without this check matchGlob would
// silently return false for every input at admission time, so a
// malformed rule would land as Accepted=True yet never actually apply.
//
// Patterns containing no glob metacharacter are treated as literal and
// always compile, so the function short-circuits without touching the
// compile cache.
func ValidateGlob(pattern string) error {
	if !containsGlobMetachar(pattern) {
		return nil
	}
	_, err := compiledGlob(pattern)
	return err
}

// globCache memoises compiled glob.Glob values by their source pattern.
// The cache is unbounded; in practice the entry count is bounded by the
// total number of unique match-field globs in all installed policies,
// which is small (tens to hundreds) and effectively constant once the
// policy set has stabilised.
//
// The entry type wraps a sync.Once so concurrent callers that miss the
// cache on the same pattern still call glob.Compile exactly once
// between them; the simpler Load/LoadOrStore pattern can issue
// duplicate Compiles when two admission goroutines race on a freshly-
// installed policy. Compile is cheap individually but admission is hot
// enough that "exactly once per pattern" is the right contract.
var globCache sync.Map // map[string]*cachedGlob

type cachedGlob struct {
	once sync.Once
	glob glob.Glob
	err  error
}

// compiledGlob returns the cached or freshly-compiled gobwas/glob.Glob
// for pattern. Errors are cached alongside successful compiles so a
// malformed pattern is rejected by the same fast path on every call;
// the calling matchGlob treats compile failures as "no match", which
// (combined with reconcile-time imageref.ValidateGlob) bounds malformed
// patterns to skipping the rule rather than crashing admission.
func compiledGlob(pattern string) (glob.Glob, error) {
	entry, _ := globCache.LoadOrStore(pattern, &cachedGlob{})
	c := entry.(*cachedGlob)
	c.once.Do(func() {
		c.glob, c.err = glob.Compile(pattern, '/')
		if c.err != nil {
			c.err = fmt.Errorf("compile glob %q: %w", pattern, c.err)
		}
	})
	return c.glob, c.err
}
