package engine_test

import (
	"reflect"
	"testing"

	"github.com/RemkoMolier/squirrel/internal/engine"
	"github.com/RemkoMolier/squirrel/internal/imageref"
)

// The ordering tests use a single input image and rules that all match
// it, so the winning rule is determined solely by the priority and
// tie-breaker chain documented in docs/design/v1alpha1.md:
//
//  1. Effective priority, descending.
//  2. Namespaced (ImagePolicy) rules beat cluster (ClusterImagePolicy).
//  3. Policy name, ascending lexicographic order.
//  4. Rule index within the policy, ascending.
//
// Each test sets up rules in an order that does NOT match the design
// order, then asserts the engine picks the right one - proving the
// sort runs and applies the tie-breakers in the documented order.

const orderingImage = "docker.io/library/nginx:1.21"

var orderingMatch = imageref.Match{Registry: "docker.io"}

func TestResolveOrdering(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		rules      []engine.CompiledRule
		wantSource engine.RuleSource
	}{
		{
			name: "higher priority wins regardless of slice order",
			rules: []engine.CompiledRule{
				rewriteRuleP(orderingMatch, engine.Target{Registry: "low"}, 10,
					engine.RuleSource{Scope: engine.ScopeCluster, Name: "low-priority"}),
				rewriteRuleP(orderingMatch, engine.Target{Registry: "high"}, 100,
					engine.RuleSource{Scope: engine.ScopeCluster, Name: "high-priority"}),
				rewriteRuleP(orderingMatch, engine.Target{Registry: "mid"}, 50,
					engine.RuleSource{Scope: engine.ScopeCluster, Name: "mid-priority"}),
			},
			wantSource: engine.RuleSource{Scope: engine.ScopeCluster, Name: "high-priority"},
		},
		{
			name: "negative priorities are honoured as the lowest",
			rules: []engine.CompiledRule{
				rewriteRuleP(orderingMatch, engine.Target{Registry: "negative"}, -100,
					engine.RuleSource{Scope: engine.ScopeCluster, Name: "negative-priority"}),
				rewriteRuleP(orderingMatch, engine.Target{Registry: "zero"}, 0,
					engine.RuleSource{Scope: engine.ScopeCluster, Name: "zero-priority"}),
			},
			wantSource: engine.RuleSource{Scope: engine.ScopeCluster, Name: "zero-priority"},
		},
		{
			name: "namespaced beats cluster at equal priority",
			rules: []engine.CompiledRule{
				rewriteRuleP(orderingMatch, engine.Target{Registry: "cluster"}, 50,
					engine.RuleSource{Scope: engine.ScopeCluster, Name: "alpha"}),
				rewriteRuleP(orderingMatch, engine.Target{Registry: "namespaced"}, 50,
					engine.RuleSource{Scope: engine.ScopeNamespaced, Name: "alpha", Namespace: "team"}),
			},
			wantSource: engine.RuleSource{Scope: engine.ScopeNamespaced, Name: "alpha", Namespace: "team"},
		},
		{
			name: "name ascending breaks the tie at equal priority and scope",
			rules: []engine.CompiledRule{
				rewriteRuleP(orderingMatch, engine.Target{Registry: "z"}, 50,
					engine.RuleSource{Scope: engine.ScopeCluster, Name: "zeta"}),
				rewriteRuleP(orderingMatch, engine.Target{Registry: "a"}, 50,
					engine.RuleSource{Scope: engine.ScopeCluster, Name: "alpha"}),
				rewriteRuleP(orderingMatch, engine.Target{Registry: "m"}, 50,
					engine.RuleSource{Scope: engine.ScopeCluster, Name: "mu"}),
			},
			wantSource: engine.RuleSource{Scope: engine.ScopeCluster, Name: "alpha"},
		},
		{
			name: "rule index ascending breaks the tie at equal priority, scope, and name",
			rules: []engine.CompiledRule{
				rewriteRuleP(orderingMatch, engine.Target{Registry: "second"}, 50,
					engine.RuleSource{Scope: engine.ScopeCluster, Name: "policy", RuleIndex: 5}),
				rewriteRuleP(orderingMatch, engine.Target{Registry: "first"}, 50,
					engine.RuleSource{Scope: engine.ScopeCluster, Name: "policy", RuleIndex: 1}),
			},
			wantSource: engine.RuleSource{Scope: engine.ScopeCluster, Name: "policy", RuleIndex: 1},
		},
		{
			name: "priority overrides scope tie-breaker (cluster with higher priority wins)",
			rules: []engine.CompiledRule{
				rewriteRuleP(orderingMatch, engine.Target{Registry: "cluster"}, 100,
					engine.RuleSource{Scope: engine.ScopeCluster, Name: "important"}),
				rewriteRuleP(orderingMatch, engine.Target{Registry: "namespaced"}, 50,
					engine.RuleSource{Scope: engine.ScopeNamespaced, Name: "background", Namespace: "team"}),
			},
			wantSource: engine.RuleSource{Scope: engine.ScopeCluster, Name: "important"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dec, err := engine.Resolve(orderingImage, tt.rules)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if dec.Action != engine.ActionRewrite {
				t.Fatalf("Action: got %q, want %q", dec.Action, engine.ActionRewrite)
			}
			if dec.Source != tt.wantSource {
				t.Errorf("Source:\n got: %+v\nwant: %+v", dec.Source, tt.wantSource)
			}
		})
	}
}

// TestResolveOrderingDoesNotMutateInput guards against an in-place sort
// regression. The webhook will likely pass a cached, shared slice of
// CompiledRule values (or one derived from such a cache); a sort that
// reorders the caller's slice could cause non-deterministic admission
// decisions in concurrent admission requests.
func TestResolveOrderingDoesNotMutateInput(t *testing.T) {
	t.Parallel()

	rules := []engine.CompiledRule{
		rewriteRuleP(orderingMatch, engine.Target{Registry: "low"}, 10,
			engine.RuleSource{Scope: engine.ScopeCluster, Name: "low"}),
		rewriteRuleP(orderingMatch, engine.Target{Registry: "high"}, 100,
			engine.RuleSource{Scope: engine.ScopeCluster, Name: "high"}),
	}
	// Capture the full slice values - not just selected fields - so a
	// future refactor that adds new fields to CompiledRule, or that
	// mutates Match/Action/Target/Tags in place during sorting, fails
	// this test loudly.
	before := []engine.CompiledRule{rules[0], rules[1]}

	if _, err := engine.Resolve(orderingImage, rules); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	for i := range rules {
		if !reflect.DeepEqual(rules[i], before[i]) {
			t.Errorf("Resolve mutated the input slice at index %d:\n got: %+v\nwant: %+v",
				i, rules[i], before[i])
		}
	}
}
