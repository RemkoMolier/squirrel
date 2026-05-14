package imageref

import "testing"

func TestMatchNormalisedFillsDefaults(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		match Match
		want  string
	}{
		{
			name:  "empty match yields the catch-all normalised form",
			match: Match{},
			want:  "*/**:*@*",
		},
		{
			name:  "registry-only match keeps the other defaults",
			match: Match{Registry: "docker.io"},
			want:  "docker.io/**:*@*",
		},
		{
			name: "fully specified literal match round-trips losslessly",
			match: Match{
				Registry:   "docker.io",
				Repository: "library/nginx",
				Tag:        "1.21",
				Digest:     testDigestSHA256,
			},
			want: "docker.io/library/nginx:1.21@" + testDigestSHA256,
		},
		{
			name: "wildcards are preserved verbatim",
			match: Match{
				Registry:   "*",
				Repository: "**",
				Tag:        "*",
				Digest:     "*",
			},
			want: "*/**:*@*",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.match.Normalised(); got != tt.want {
				t.Errorf("%+v.Normalised() = %q, want %q", tt.match, got, tt.want)
			}
		})
	}
}

func TestMatchSpecificityIsLengthOfNormalised(t *testing.T) {
	t.Parallel()

	cases := []Match{
		{},
		{Registry: "docker.io"},
		{Registry: "docker.io", Repository: "library/**"},
		{Registry: "docker.io", Repository: "library/nginx", Tag: "1.21"},
		{Registry: "docker.io", Repository: "library/nginx", Tag: "1.21", Digest: testDigestSHA256},
	}

	for _, m := range cases {
		if got, want := m.Specificity(), len(m.Normalised()); got != want {
			t.Errorf("%+v.Specificity() = %d, want %d", m, got, want)
		}
	}
}

func TestMatchSpecificityOrdersFromBroadToNarrow(t *testing.T) {
	t.Parallel()

	// Increasingly specific matches must have strictly increasing specificity
	// scores so they sort to the front of the rewrite phase.
	ordered := []Match{
		{},                      // "*/**:*@*"
		{Registry: "docker.io"}, // "docker.io/**:*@*"
		{Registry: "docker.io", Repository: "library/**"},    // "docker.io/library/**:*@*"
		{Registry: "docker.io", Repository: "library/nginx"}, // "docker.io/library/nginx:*@*"
		{Registry: "docker.io", Repository: "library/nginx", Tag: "1.21"},
	}

	prev := ordered[0].Specificity()
	for i := 1; i < len(ordered); i++ {
		curr := ordered[i].Specificity()
		if curr <= prev {
			t.Errorf("expected specificity[%d] = %+v (%d) > specificity[%d] = %+v (%d)",
				i, ordered[i], curr, i-1, ordered[i-1], prev)
		}
		prev = curr
	}
}

func TestMatchSpecificityIsEqualForParsedAndStructured(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		stringForm string
		structured Match
	}{
		{
			name:       "string and structured docker-hub mirror match agree",
			stringForm: "docker.io/**:*",
			structured: Match{Registry: "docker.io", Repository: "**", Tag: "*"},
		},
		{
			name:       "string and structured literal match agree",
			stringForm: "docker.io/library/nginx:1.21",
			structured: Match{Registry: "docker.io", Repository: "library/nginx", Tag: "1.21"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			parsed, err := ParseMatchString(tt.stringForm)
			if err != nil {
				t.Fatalf("ParseMatchString(%q): %v", tt.stringForm, err)
			}
			if parsed.Specificity() != tt.structured.Specificity() {
				t.Errorf("parsed %+v specificity=%d differs from structured %+v specificity=%d",
					parsed, parsed.Specificity(), tt.structured, tt.structured.Specificity())
			}
			if parsed.Normalised() != tt.structured.Normalised() {
				t.Errorf("parsed Normalised=%q != structured Normalised=%q",
					parsed.Normalised(), tt.structured.Normalised())
			}
		})
	}
}
