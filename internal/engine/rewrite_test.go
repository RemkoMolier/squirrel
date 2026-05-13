package engine_test

import (
	"testing"

	"github.com/RemkoMolier/squirrel/internal/engine"
	"github.com/RemkoMolier/squirrel/internal/imageref"
)

// rewriteRule is a small constructor for table-driven rewrite tests.
// The priority parameter is reintroduced when the priority-ordering
// iteration in this phase lands.
func rewriteRule(match imageref.Match, target engine.Target, source engine.RuleSource) engine.CompiledRule {
	return engine.CompiledRule{
		Match:  match,
		Action: engine.ActionRewrite,
		Target: target,
		Source: source,
	}
}

const (
	testRewriteDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

func TestResolveRewritePhase(t *testing.T) {
	t.Parallel()

	mirrorSource := engine.RuleSource{
		Scope: engine.ScopeCluster,
		Name:  "dockerhub-mirror",
	}

	tests := []struct {
		name       string
		image      string
		rules      []engine.CompiledRule
		wantAction engine.Action
		wantImage  string
		wantSource engine.RuleSource
	}{
		{
			name:  "registry swap rewrites a simple image",
			image: "docker.io/library/nginx:1.21",
			rules: []engine.CompiledRule{
				rewriteRule(
					imageref.Match{Registry: "docker.io"},
					engine.Target{Registry: "mirror.internal"},
					mirrorSource,
				),
			},
			wantAction: engine.ActionRewrite,
			wantImage:  "mirror.internal/library/nginx:1.21",
			wantSource: mirrorSource,
		},
		{
			name:  "repository template uses placeholders",
			image: "docker.io/library/nginx:1.21",
			rules: []engine.CompiledRule{
				rewriteRule(
					imageref.Match{Registry: "docker.io"},
					engine.Target{
						Registry:   "mirror.internal",
						Repository: "dockerhub/{repository}",
					},
					mirrorSource,
				),
			},
			wantAction: engine.ActionRewrite,
			wantImage:  "mirror.internal/dockerhub/library/nginx:1.21",
			wantSource: mirrorSource,
		},
		{
			name:  "repository:image strips the dockerhub library prefix",
			image: "docker.io/library/nginx:1.21",
			rules: []engine.CompiledRule{
				rewriteRule(
					imageref.Match{Registry: "docker.io"},
					engine.Target{
						Registry:   "mirror.internal",
						Repository: "dockerhub/{repository:image}",
					},
					mirrorSource,
				),
			},
			wantAction: engine.ActionRewrite,
			wantImage:  "mirror.internal/dockerhub/nginx:1.21",
			wantSource: mirrorSource,
		},
		{
			name:  "repository:flat replaces slashes with dashes",
			image: "gcr.io/google_containers/etcd:3.5",
			rules: []engine.CompiledRule{
				rewriteRule(
					imageref.Match{Registry: "gcr.io"},
					engine.Target{
						Registry:   "mirror.internal",
						Repository: "mirror-{repository:flat}",
					},
					mirrorSource,
				),
			},
			wantAction: engine.ActionRewrite,
			wantImage:  "mirror.internal/mirror-google_containers-etcd:3.5",
			wantSource: mirrorSource,
		},
		{
			name:  "tag list falls through to digest sub-form when no tag is present",
			image: "docker.io/library/nginx@" + testRewriteDigest,
			rules: []engine.CompiledRule{
				rewriteRule(
					imageref.Match{Registry: "docker.io"},
					engine.Target{
						Registry: "mirror.internal",
						Tags:     []string{"{tag}", "{digest:short8}"},
					},
					mirrorSource,
				),
			},
			wantAction: engine.ActionRewrite,
			wantImage:  "mirror.internal/library/nginx:aaaaaaaa@" + testRewriteDigest,
			wantSource: mirrorSource,
		},
		{
			name:  "non-matching rewrite rule does not apply",
			image: "gcr.io/google_containers/etcd:3.5",
			rules: []engine.CompiledRule{
				rewriteRule(
					imageref.Match{Registry: "docker.io"},
					engine.Target{Registry: "mirror.internal"},
					mirrorSource,
				),
			},
			wantAction: "",
		},
		{
			name:  "name-ascending tie-breaker picks alpha over beta when priority and scope tie",
			image: "docker.io/library/nginx:1.21",
			rules: []engine.CompiledRule{
				rewriteRule(
					imageref.Match{Registry: "docker.io"},
					engine.Target{Registry: "mirror-a.internal"},
					engine.RuleSource{Scope: engine.ScopeCluster, Name: "a"},
				),
				rewriteRule(
					imageref.Match{Registry: "docker.io"},
					engine.Target{Registry: "mirror-b.internal"},
					engine.RuleSource{Scope: engine.ScopeCluster, Name: "b"},
				),
			},
			wantAction: engine.ActionRewrite,
			wantImage:  "mirror-a.internal/library/nginx:1.21",
			wantSource: engine.RuleSource{Scope: engine.ScopeCluster, Name: "a"},
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
			if tt.wantAction == engine.ActionRewrite {
				if dec.RewrittenImage != tt.wantImage {
					t.Errorf("RewrittenImage:\n got: %q\nwant: %q", dec.RewrittenImage, tt.wantImage)
				}
				if dec.Source != tt.wantSource {
					t.Errorf("Source:\n got: %+v\nwant: %+v", dec.Source, tt.wantSource)
				}
			}
		})
	}
}

// TestResolveRewriteNonApplicableFallthrough exercises the design's
// "rule is non-applicable for this container, try the next rule" path.
func TestResolveRewriteNonApplicableFallthrough(t *testing.T) {
	t.Parallel()

	primary := engine.RuleSource{Scope: engine.ScopeCluster, Name: "primary"}
	fallback := engine.RuleSource{Scope: engine.ScopeCluster, Name: "fallback"}

	tests := []struct {
		name       string
		image      string
		rules      []engine.CompiledRule
		wantAction engine.Action
		wantImage  string
		wantSource engine.RuleSource
	}{
		{
			name:  "no tag candidate produces a valid render -> fall through to next rule",
			image: "docker.io/library/nginx", // tag defaults to latest, no digest
			rules: []engine.CompiledRule{
				// First rule's tag list resolves only to {digest:short8},
				// which is empty for an image with no digest - this rule is
				// non-applicable for the input.
				rewriteRule(
					imageref.Match{Registry: "docker.io"},
					engine.Target{
						Registry: "mirror.internal",
						Tags:     []string{"{digest:short8}"},
					},
					primary,
				),
				// Fallback rule with a sensible passthrough tag.
				rewriteRule(
					imageref.Match{Registry: "docker.io"},
					engine.Target{Registry: "mirror.internal"},
					fallback,
				),
			},
			wantAction: engine.ActionRewrite,
			wantImage:  "mirror.internal/library/nginx:latest",
			wantSource: fallback,
		},
		{
			name:  "every rule non-applicable -> no rewrite (image unchanged)",
			image: "docker.io/library/nginx",
			rules: []engine.CompiledRule{
				rewriteRule(
					imageref.Match{Registry: "docker.io"},
					engine.Target{
						Registry: "mirror.internal",
						Tags:     []string{"{digest:short8}"},
					},
					primary,
				),
			},
			wantAction: "",
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
			if tt.wantAction == engine.ActionRewrite {
				if dec.RewrittenImage != tt.wantImage {
					t.Errorf("RewrittenImage:\n got: %q\nwant: %q", dec.RewrittenImage, tt.wantImage)
				}
				if dec.Source != tt.wantSource {
					t.Errorf("Source:\n got: %+v\nwant: %+v", dec.Source, tt.wantSource)
				}
			}
		})
	}
}

// TestResolveSkipBeatsRewriteRegardlessOfOrdering pins down the
// design's phase ordering: every skip rule is considered before any
// rewrite rule. The test sets up an image that matches both a skip
// rule and a rewrite rule, with the rewrite rule placed first in slice
// order, and asserts the skip wins. This guards against a future
// refactor that folds the two phases into one loop and accidentally
// re-orders them.
func TestResolveSkipBeatsRewriteRegardlessOfOrdering(t *testing.T) {
	t.Parallel()

	skipSource := engine.RuleSource{Scope: engine.ScopeCluster, Name: "skip-rule"}
	rewriteSource := engine.RuleSource{Scope: engine.ScopeCluster, Name: "rewrite-rule"}

	dec, err := engine.Resolve(
		"docker.io/library/nginx:1.21",
		[]engine.CompiledRule{
			// Rewrite rule listed first; the engine must still apply
			// the skip rule below because skip is a separate, earlier
			// phase.
			rewriteRule(
				imageref.Match{Registry: "docker.io"},
				engine.Target{Registry: "mirror.internal"},
				rewriteSource,
			),
			{
				Match:  imageref.Match{Registry: "docker.io", Repository: "library/nginx"},
				Action: engine.ActionSkip,
				Source: skipSource,
			},
		},
	)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if dec.Action != engine.ActionSkip {
		t.Errorf("Action: got %q, want %q", dec.Action, engine.ActionSkip)
	}
	if dec.Source != skipSource {
		t.Errorf("Source: got %+v, want %+v", dec.Source, skipSource)
	}
	if want := "docker.io/library/nginx:1.21"; dec.RewrittenImage != want {
		t.Errorf("RewrittenImage: got %q, want %q (skip should leave the image canonicalised but unchanged)",
			dec.RewrittenImage, want)
	}
}

// TestResolveRewriteDigestPassthrough confirms the design's content-
// integrity guarantee: the input digest is carried verbatim into the
// rewritten reference, regardless of which target fields are templated.
func TestResolveRewriteDigestPassthrough(t *testing.T) {
	t.Parallel()

	dec, err := engine.Resolve(
		"docker.io/library/nginx:1.21@"+testRewriteDigest,
		[]engine.CompiledRule{
			rewriteRule(
				imageref.Match{Registry: "docker.io"},
				engine.Target{
					Registry:   "mirror.internal",
					Repository: "dockerhub/{repository:image}",
				},
				engine.RuleSource{Scope: engine.ScopeCluster, Name: "mirror"},
			),
		},
	)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if want := "mirror.internal/dockerhub/nginx:1.21@" + testRewriteDigest; dec.RewrittenImage != want {
		t.Errorf("RewrittenImage:\n got: %q\nwant: %q", dec.RewrittenImage, want)
	}
}
