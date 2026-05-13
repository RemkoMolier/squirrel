package imageref

import "testing"

func TestMatchGlob(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		pattern string
		input   string
		want    bool
	}{
		// Literal patterns.
		{name: "literal exact match", pattern: "nginx", input: "nginx", want: true},
		{name: "literal mismatch", pattern: "nginx", input: "redis", want: false},
		{name: "literal case-sensitive", pattern: "Nginx", input: "nginx", want: false},
		{name: "literal empty pattern matches empty input", pattern: "", input: "", want: true},
		{name: "literal empty pattern rejects non-empty input", pattern: "", input: "x", want: false},

		// Single-star behaviour: matches any sequence not containing `/`.
		{name: "single star matches any non-slash sequence", pattern: "ngi*", input: "nginx", want: true},
		{name: "single star matches the empty sequence", pattern: "*", input: "", want: true},
		{name: "single star alone matches a single segment", pattern: "*", input: "nginx", want: true},
		{name: "single star refuses to cross a slash", pattern: "*", input: "library/nginx", want: false},
		{name: "single star refuses to cross a slash in the middle", pattern: "lib*nginx", input: "library/nginx", want: false},
		{name: "single star at start matches any prefix without slash", pattern: "*nginx", input: "nginx", want: true},
		{name: "single star surrounded by literals", pattern: "ng*x", input: "nginx", want: true},
		{name: "single star does not match across slash with literal suffix", pattern: "ng*x", input: "ng/x", want: false},

		// Double-star behaviour: matches any sequence including slashes and empty.
		{name: "double star matches across a slash", pattern: "**", input: "library/nginx", want: true},
		{name: "double star matches deeply nested paths", pattern: "**", input: "google_containers/etcd-io/etcd", want: true},
		{name: "double star matches the empty sequence", pattern: "**", input: "", want: true},
		{name: "double star at end matches anything after the prefix", pattern: "library/**", input: "library/nginx", want: true},
		{name: "double star at end matches the empty remainder", pattern: "library/**", input: "library/", want: true},
		{name: "double star at end requires the literal prefix", pattern: "library/**", input: "redis/nginx", want: false},

		// Mixed and edge cases.
		{name: "literal period is treated as literal", pattern: "a.b", input: "a.b", want: true},
		{name: "literal period does not match arbitrary character", pattern: "a.b", input: "aXb", want: false},
		{name: "literal colon is treated as literal", pattern: "host:5000", input: "host:5000", want: true},
		{name: "triple star is equivalent to double star", pattern: "***", input: "library/nginx", want: true},
		{name: "double star then literal segment", pattern: "**/nginx", input: "library/nginx", want: true},
		{name: "double star then literal segment also matches deeper", pattern: "**/nginx", input: "a/b/c/nginx", want: true},
		{name: "double star then literal segment refuses missing suffix", pattern: "**/nginx", input: "library/redis", want: false},

		// Question-mark single-character wildcard.
		{name: "question mark matches a single non-slash character", pattern: "lib?ary", input: "library", want: true},
		{name: "question mark refuses zero characters", pattern: "lib?ary", input: "libary", want: false}, //nolint:misspell // intentional: pattern requires exactly one character between `b` and `a`
		{name: "question mark refuses slash", pattern: "lib?ary", input: "lib/ary", want: false},

		// Character class.
		{name: "character class matches a single included character", pattern: "library/nginx[0-9]", input: "library/nginx5", want: true},
		{name: "character class rejects a character outside the range", pattern: "library/nginx[0-9]", input: "library/nginxA", want: false},
		{name: "character class explicit set matches", pattern: "library/redis[abc]", input: "library/redisb", want: true},
		{name: "negated character class matches outside the set", pattern: "library/redis[!abc]", input: "library/redisz", want: true},
		{name: "negated character class refuses inside the set", pattern: "library/redis[!abc]", input: "library/redisa", want: false},

		// Alternation.
		{name: "alternation accepts first option", pattern: "{docker.io,gcr.io}", input: "docker.io", want: true},
		{name: "alternation accepts second option", pattern: "{docker.io,gcr.io}", input: "gcr.io", want: true},
		{name: "alternation rejects neither option", pattern: "{docker.io,gcr.io}", input: "quay.io", want: false},
		{name: "alternation with embedded wildcard", pattern: "{docker.io,gcr.io}/**", input: "gcr.io/google_containers/etcd", want: true},

		// Escape: a backslash forces the next character to be literal.
		{name: "escaped star matches a literal star", pattern: `\*`, input: "*", want: true},
		{name: "escaped star does not match an arbitrary character", pattern: `\*`, input: "x", want: false},
		{name: "escaped question mark matches a literal question mark", pattern: `lib\?ary`, input: "lib?ary", want: true},

		// Empty input. Under our documented semantics only `*` and `**`
		// match an empty sequence; single-character wildcards (`?`,
		// `[...]`, `[!...]`) require at least one character. gobwas's
		// raw matcher accepts the empty string for `?` and `[!...]`,
		// so matchGlob applies an additional guard - without it,
		// match.tag: "?" would match a digest-pinned image's empty
		// tag and match.digest: "?" would match an image with no
		// digest. These cases pin the guard.
		{name: "question mark does not match empty input", pattern: "?", input: "", want: false},
		{name: "negated class does not match empty input", pattern: "[!a]", input: "", want: false},
		{name: "character class does not match empty input", pattern: "[a-z]", input: "", want: false},
		{name: "explicit character set does not match empty input", pattern: "[abc]", input: "", want: false},
		{name: "leading question mark does not match empty input", pattern: "?abc", input: "", want: false},
		{name: "trailing question mark does not match empty input", pattern: "abc?", input: "", want: false},
		{name: "literal does not match empty input", pattern: "nginx", input: "", want: false},
		// Stars must still match empty - this is the documented
		// default for empty Tag/Digest fields (Match defaults `*`).
		{name: "double star alone matches empty input", pattern: "**", input: "", want: true},
		{name: "single star alone still matches empty input", pattern: "*", input: "", want: true},
		// Alternations whose `*` branch matches empty: the operator's
		// stated intent ("any tag, including digest-pinned images
		// with no tag") is honoured. Patterns whose alternatives
		// all require at least one character (e.g. `{a,b}`) still
		// reject the empty input.
		{name: "alternation containing star matches empty input via the * branch", pattern: "{a,*}", input: "", want: true},
		{name: "alternation of literals does not match empty input", pattern: "{a,b}", input: "", want: false},
		{name: "single-star alternation matches empty input", pattern: "{*}", input: "", want: true},
		{name: "star plus prefixed star alternation matches empty input", pattern: "{*,sha256:*}", input: "", want: true},
		// Nested alternation case: the outer split must respect brace
		// depth, otherwise `strings.Split` on commas would chop the
		// inner `{b,c}` into `{b` and `c}` and neither branch would
		// recurse to a true patternMatchesEmpty answer. With the
		// depth-aware splitter the inner `{*,b}` alternative matches
		// empty, so the whole expression does too.
		{name: "nested alternation propagates empty match through the inner branch", pattern: "{a,{*,b}}", input: "", want: true},
		{name: "nested alternation with all literal alternatives rejects empty", pattern: "{a,{b,c}}", input: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := matchGlob(tt.pattern, tt.input)
			if got != tt.want {
				t.Errorf("matchGlob(%q, %q) = %t, want %t", tt.pattern, tt.input, got, tt.want)
			}
		})
	}
}

func TestValidateGlobAcceptsLiteralAndWildcards(t *testing.T) {
	t.Parallel()

	cases := []string{
		"",
		"library/nginx",
		"docker.io/library/**:*",
		"library/nginx[0-9]",
		"library/redis[!abc]",
		"{docker.io,gcr.io}/**",
		`\?literal-question`,
		"library/**/nginx",
	}
	for _, pattern := range cases {
		if err := ValidateGlob(pattern); err != nil {
			t.Errorf("ValidateGlob(%q): unexpected error %v", pattern, err)
		}
	}
}

func TestValidateGlobRejectsCompileFailures(t *testing.T) {
	t.Parallel()

	// The matchGlob runtime silently returns false on a compile error,
	// so an unguarded malformed glob would make its rule a no-op. The
	// controller's validation layer calls ValidateGlob to surface
	// these as InvalidMatch conditions at policy creation time.
	cases := []string{
		"library/[",   // unterminated character class
		"library/[a-", // truncated range
		"[]",          // empty character class (the example from the review)
		"library/[]",  // empty character class with prefix
	}
	for _, pattern := range cases {
		if err := ValidateGlob(pattern); err == nil {
			t.Errorf("ValidateGlob(%q): expected error, got nil", pattern)
		}
	}
}
