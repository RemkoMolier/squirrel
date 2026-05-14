package webhook

import (
	"errors"
	"fmt"

	squirrelv1alpha1 "github.com/RemkoMolier/squirrel/api/v1alpha1"
	"github.com/RemkoMolier/squirrel/internal/engine"
	"github.com/RemkoMolier/squirrel/internal/imageref"
)

// errMatchNotPopulated is returned by compileMatch when MatchExpression
// produced an expression with neither the string nor the structured
// form set. The reconciler should have rejected the policy upstream;
// this is the defence-in-depth sentinel for that path.
var errMatchNotPopulated = errors.New("match: neither string nor structured form was populated")

// compileMatch turns a parsed MatchExpr into the imageref.Match form
// the engine consumes. Both API forms (glob string and structured
// object) collapse to the same four-field representation here.
func compileMatch(mx squirrelv1alpha1.MatchExpr) (imageref.Match, error) {
	switch {
	case mx.String != "":
		parsed, err := imageref.ParseMatchString(mx.String)
		if err != nil {
			return imageref.Match{}, fmt.Errorf("match: %w", err)
		}
		return parsed, nil
	case mx.Structured != nil:
		return imageref.Match{
			Registry:   mx.Structured.Registry,
			Repository: mx.Structured.Repository,
			Tag:        mx.Structured.Tag,
			Digest:     mx.Structured.Digest,
		}, nil
	}
	return imageref.Match{}, errMatchNotPopulated
}

// compileTarget produces the engine.Target by merging a rule-level
// target over a policy-level defaultTarget, field by field, per the
// design's per-field merge semantics:
//
//   - Registry  : rule.Registry overrides def.Registry.
//   - Repository: rule.Repository overrides def.Repository.
//   - Tags      : rule.EffectiveTags() overrides def.EffectiveTags()
//     (Tag/Tags sugar is collapsed by the API helper).
//
// When neither side supplies a value for a given field, the engine's
// renderTarget applies its own passthrough default at render time.
func compileTarget(rule, def *squirrelv1alpha1.Target) engine.Target {
	var et engine.Target

	switch {
	case rule != nil && rule.Registry != "":
		et.Registry = rule.Registry
	case def != nil:
		et.Registry = def.Registry
	}

	switch {
	case rule != nil && rule.Repository != "":
		et.Repository = rule.Repository
	case def != nil:
		et.Repository = def.Repository
	}

	switch {
	case rule != nil && len(rule.EffectiveTags()) > 0:
		et.Tags = rule.EffectiveTags()
	case def != nil:
		et.Tags = def.EffectiveTags()
	}

	return et
}

// effectiveAction resolves a rule's effective action and converts
// the result to the engine's Action type. The resolution rule lives
// on the API type (api/v1alpha1.EffectiveAction); this thin wrapper
// only handles the type conversion so the two call sites (reconciler
// + webhook) cannot drift on the resolution order.
func effectiveAction(ruleAction, defaultAction squirrelv1alpha1.Action) engine.Action {
	return engine.Action(squirrelv1alpha1.EffectiveAction(ruleAction, defaultAction))
}

// effectivePriority resolves a rule's effective priority: the
// rule-level override if set, otherwise the match's specificity score.
// Specificity is the byte length of the normalised match string; longer
// (more literal) globs sort higher in the rewrite phase.
func effectivePriority(rule squirrelv1alpha1.Rule, match imageref.Match) int32 {
	if rule.Priority != nil {
		return *rule.Priority
	}
	// Specificity returns int; the API priority field is int32. Bytes
	// in a glob string are bounded by Kubernetes' 1MB API object size,
	// which is far smaller than math.MaxInt32, so the conversion is
	// loss-free in practice.
	return int32(match.Specificity()) //nolint:gosec // glob string length is bounded by the K8s API object limit
}

// compileRule converts one api/v1alpha1.Rule into the engine's
// CompiledRule shape. Errors are returned when the match expression is
// malformed; the caller decides whether to skip the rule or to surface
// a higher-level failure. The webhook skips the rule and increments
// the invalid_rule metric (Phase 7 wiring).
func compileRule(
	rule squirrelv1alpha1.Rule,
	defaultTarget *squirrelv1alpha1.Target,
	defaultAction squirrelv1alpha1.Action,
	source engine.RuleSource,
) (engine.CompiledRule, error) {
	mx, err := rule.MatchExpression()
	if err != nil {
		return engine.CompiledRule{}, fmt.Errorf("match: %w", err)
	}
	match, err := compileMatch(mx)
	if err != nil {
		return engine.CompiledRule{}, err
	}
	return engine.CompiledRule{
		Match:    match,
		Action:   effectiveAction(rule.Action, defaultAction),
		Target:   compileTarget(rule.Target, defaultTarget),
		Priority: effectivePriority(rule, match),
		Source:   source,
	}, nil
}

// compilePolicyRules expands all rules from a single policy into the
// engine's flat CompiledRule slice. The source function produces a
// RuleSource for each rule index so the caller controls Scope / Name /
// Namespace without this helper having to know which kind it is
// operating on.
//
// Rules whose match fails to compile are skipped, and the returned
// errs slice carries one error per skipped rule with the rule index
// already in the message. The caller logs and emits the
// invalid_admission_rules_total metric; an Accepted=True policy whose
// rule fails to compile at admission time is the defence-in-depth
// branch the reconciler should already have caught.
func compilePolicyRules(
	rules []squirrelv1alpha1.Rule,
	defaultTarget *squirrelv1alpha1.Target,
	defaultAction squirrelv1alpha1.Action,
	sourceFor func(int) engine.RuleSource,
) ([]engine.CompiledRule, []error) {
	out := make([]engine.CompiledRule, 0, len(rules))
	var errs []error
	for i, rule := range rules {
		compiled, err := compileRule(rule, defaultTarget, defaultAction, sourceFor(i))
		if err != nil {
			errs = append(errs, fmt.Errorf("rule[%d]: %w", i, err))
			continue
		}
		out = append(out, compiled)
	}
	return out, errs
}
