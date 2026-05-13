package engine_test

import (
	"testing"

	"github.com/RemkoMolier/squirrel/internal/engine"
	"github.com/RemkoMolier/squirrel/internal/imageref"
)

// skipRule is a small constructor used by the skip-phase tests to keep
// the table rows readable.
func skipRule(match imageref.Match, source engine.RuleSource) engine.CompiledRule {
	return engine.CompiledRule{
		Match:  match,
		Action: engine.ActionSkip,
		Source: source,
	}
}

func TestResolveNoRulesLeavesImageUnchanged(t *testing.T) {
	t.Parallel()

	dec, err := engine.Resolve("nginx:1.21", nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if dec.Action != "" {
		t.Errorf("Action: got %q, want \"\"", dec.Action)
	}
	if dec.RewrittenImage != "docker.io/library/nginx:1.21" {
		t.Errorf("RewrittenImage: got %q, want canonical form", dec.RewrittenImage)
	}
	if (dec.Source != engine.RuleSource{}) {
		t.Errorf("Source: got %+v, want zero", dec.Source)
	}
}

func TestResolveUnparseableImageReturnsError(t *testing.T) {
	t.Parallel()

	if _, err := engine.Resolve("", nil); err == nil {
		t.Errorf("Resolve(\"\"): expected error, got nil")
	}
}

// TestResolveSkipPreservesDigestInRewrittenImage pins the contract
// that a skip outcome leaves the image *fully* unchanged - including
// the digest a caller might have pinned for content-integrity. The
// engine assembles RewrittenImage via canonicalForm, so a regression
// where the @<digest> suffix is dropped would silently strip the pin
// even though Action=Skip says "image unchanged". TestResolveSkipPhase
// covers the tag side; this is the digest counterpart.
func TestResolveSkipPreservesDigestInRewrittenImage(t *testing.T) {
	t.Parallel()

	dec, err := engine.Resolve(
		"docker.io/library/nginx:1.21@"+testRewriteDigest,
		[]engine.CompiledRule{
			skipRule(
				imageref.Match{Registry: "docker.io"},
				engine.RuleSource{Scope: engine.ScopeCluster, Name: "skip-rule"},
			),
		},
	)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if dec.Action != engine.ActionSkip {
		t.Fatalf("Action: got %q, want %q", dec.Action, engine.ActionSkip)
	}
	if want := "docker.io/library/nginx:1.21@" + testRewriteDigest; dec.RewrittenImage != want {
		t.Errorf("RewrittenImage: got %q, want %q (digest must pass through skip unchanged)",
			dec.RewrittenImage, want)
	}
}

// TestResolveSkipPhaseSourceIsDeterministic guards against the
// webhook (Phase 5) building the rule slice from map iteration and
// producing arbitrary Decision.Source values across admissions. Two
// skip rules match; the namespaced one wins (Scope > Name > RuleIndex
// tie-breaker), regardless of the order the caller passed in.
func TestResolveSkipPhaseSourceIsDeterministic(t *testing.T) {
	t.Parallel()

	clusterRule := engine.CompiledRule{
		Match:  imageref.Match{Registry: "docker.io"},
		Action: engine.ActionSkip,
		Source: engine.RuleSource{Scope: engine.ScopeCluster, Name: "cluster-skip"},
	}
	namespacedRule := engine.CompiledRule{
		Match:  imageref.Match{Registry: "docker.io"},
		Action: engine.ActionSkip,
		Source: engine.RuleSource{Scope: engine.ScopeNamespaced, Name: "ns-skip", Namespace: "team"},
	}

	for _, order := range [][]engine.CompiledRule{
		{clusterRule, namespacedRule},
		{namespacedRule, clusterRule},
	} {
		dec, err := engine.Resolve("docker.io/library/nginx:1.21", order)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if dec.Action != engine.ActionSkip {
			t.Errorf("Action: got %q, want %q", dec.Action, engine.ActionSkip)
		}
		if dec.Source != namespacedRule.Source {
			t.Errorf("Source: got %+v, want namespacedRule.Source (namespaced beats cluster)", dec.Source)
		}
	}
}

func TestResolveSkipPhase(t *testing.T) {
	t.Parallel()

	sourceA := engine.RuleSource{
		Scope:     engine.ScopeCluster,
		Name:      "allow-direct-pulls",
		RuleIndex: 0,
	}
	sourceB := engine.RuleSource{
		Scope:     engine.ScopeNamespaced,
		Name:      "ns-skip",
		Namespace: "payments",
		RuleIndex: 0,
	}

	tests := []struct {
		name       string
		image      string
		rules      []engine.CompiledRule
		wantAction engine.Action
		wantSource engine.RuleSource
	}{
		{
			name:  "single skip rule matches",
			image: "docker.io/library/distroless-base:1",
			rules: []engine.CompiledRule{
				skipRule(imageref.Match{Registry: "docker.io", Repository: "library/distroless-base"}, sourceA),
			},
			wantAction: engine.ActionSkip,
			wantSource: sourceA,
		},
		{
			name:  "single skip rule does not match",
			image: "docker.io/library/nginx:1.21",
			rules: []engine.CompiledRule{
				skipRule(imageref.Match{Registry: "docker.io", Repository: "library/distroless-base"}, sourceA),
			},
			wantAction: "",
		},
		{
			name:  "skip-glob matches via wildcards",
			image: "docker.io/library/nginx:1.21",
			rules: []engine.CompiledRule{
				skipRule(imageref.Match{Registry: "docker.io", Repository: "library/**"}, sourceA),
			},
			wantAction: engine.ActionSkip,
			wantSource: sourceA,
		},
		{
			// Only one skip rule matches; the other is skipped over
			// and the matching rule's Source is recorded. The
			// tie-breaker case where multiple skip rules match is
			// covered separately by TestResolveSkipPhaseSourceIsDeterministic.
			name:  "non-matching skip rules are skipped and the matching rule's source is recorded",
			image: "docker.io/library/nginx:1.21",
			rules: []engine.CompiledRule{
				// Non-matching skip first.
				skipRule(imageref.Match{Registry: "gcr.io"}, sourceA),
				skipRule(imageref.Match{Registry: "docker.io", Repository: "library/**"}, sourceB),
			},
			wantAction: engine.ActionSkip,
			wantSource: sourceB,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dec, err := engine.Resolve(tt.image, tt.rules)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if dec.Action != tt.wantAction {
				t.Errorf("Action: got %q, want %q", dec.Action, tt.wantAction)
			}
			if tt.wantAction != "" && dec.Source != tt.wantSource {
				t.Errorf("Source:\n got: %+v\nwant: %+v", dec.Source, tt.wantSource)
			}
			// For both skip and no-match outcomes the rewritten image
			// is the canonical form of the input. The webhook relies
			// on RewrittenImage being non-empty so it can compare
			// against the original Pod-spec value.
			if dec.RewrittenImage == "" {
				t.Errorf("RewrittenImage was empty; the webhook requires the canonical form even when no rewrite is applied")
			}
		})
	}
}
