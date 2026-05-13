package webhook

import (
	"encoding/json"
	"reflect"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	squirrelv1alpha1 "github.com/RemkoMolier/squirrel/api/v1alpha1"
	"github.com/RemkoMolier/squirrel/internal/engine"
	"github.com/RemkoMolier/squirrel/internal/imageref"
)

// matchString builds a Rule.Match payload from a glob-string form.
func matchString(t *testing.T, s string) apiextensionsv1.JSON {
	t.Helper()
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal helper: %v", err)
	}
	return apiextensionsv1.JSON{Raw: raw}
}

// matchObject builds a Rule.Match payload from a structured form.
func matchObject(t *testing.T, m squirrelv1alpha1.Match) apiextensionsv1.JSON {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal helper: %v", err)
	}
	return apiextensionsv1.JSON{Raw: raw}
}

// fixedSource is a deterministic RuleSource for tests that only have
// one rule under inspection.
var fixedSource = engine.RuleSource{
	Scope:     engine.ScopeCluster,
	Name:      "test-policy",
	RuleIndex: 0,
}

func TestCompileMatchStringForm(t *testing.T) {
	t.Parallel()

	mx := squirrelv1alpha1.MatchExpr{String: "docker.io/library/**:*"}
	got, err := compileMatch(mx)
	if err != nil {
		t.Fatalf("compileMatch: %v", err)
	}
	want := imageref.Match{Registry: "docker.io", Repository: "library/**", Tag: "*"}
	if got != want {
		t.Errorf("compileMatch: got %+v, want %+v", got, want)
	}
}

func TestCompileMatchStructuredForm(t *testing.T) {
	t.Parallel()

	mx := squirrelv1alpha1.MatchExpr{
		Structured: &squirrelv1alpha1.Match{
			Registry:   "gcr.io",
			Repository: "google_containers/**",
			Tag:        "1.*",
		},
	}
	got, err := compileMatch(mx)
	if err != nil {
		t.Fatalf("compileMatch: %v", err)
	}
	want := imageref.Match{
		Registry:   "gcr.io",
		Repository: "google_containers/**",
		Tag:        "1.*",
	}
	if got != want {
		t.Errorf("compileMatch: got %+v, want %+v", got, want)
	}
}

func TestCompileMatchNeitherFormPopulated(t *testing.T) {
	t.Parallel()

	if _, err := compileMatch(squirrelv1alpha1.MatchExpr{}); err == nil {
		t.Errorf("compileMatch on empty MatchExpr: expected error, got nil")
	}
}

func TestCompileTargetRuleOverridesDefault(t *testing.T) {
	t.Parallel()

	rule := &squirrelv1alpha1.Target{
		Registry:   "mirror.internal",
		Repository: "rule/{repository:image}",
	}
	def := &squirrelv1alpha1.Target{
		Registry:   "default.internal",
		Repository: "default/{repository}",
		Tags:       []string{"{tag}", "{digest:short8}"},
	}
	got := compileTarget(rule, def)
	want := engine.Target{
		Registry:   "mirror.internal",
		Repository: "rule/{repository:image}",
		// Tags inherited from default since rule did not supply them.
		Tags: []string{"{tag}", "{digest:short8}"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("compileTarget:\n got: %+v\nwant: %+v", got, want)
	}
}

func TestCompileTargetTagsSugar(t *testing.T) {
	t.Parallel()

	// The rule uses Tag (singular) sugar; the compile path must
	// promote it into a single-entry Tags list via EffectiveTags().
	rule := &squirrelv1alpha1.Target{
		Registry: "mirror.internal",
		Tag:      "{tag}",
	}
	got := compileTarget(rule, nil)
	want := engine.Target{
		Registry: "mirror.internal",
		Tags:     []string{"{tag}"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("compileTarget:\n got: %+v\nwant: %+v", got, want)
	}
}

func TestCompileTargetNilRuleUsesDefault(t *testing.T) {
	t.Parallel()

	def := &squirrelv1alpha1.Target{
		Registry:   "default.internal",
		Repository: "default/{repository}",
		Tags:       []string{"{tag}"},
	}
	got := compileTarget(nil, def)
	want := engine.Target{
		Registry:   "default.internal",
		Repository: "default/{repository}",
		Tags:       []string{"{tag}"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("compileTarget:\n got: %+v\nwant: %+v", got, want)
	}
}

func TestEffectiveActionResolution(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		ruleAction  squirrelv1alpha1.Action
		defAction   squirrelv1alpha1.Action
		wantBack    engine.Action
		description string
	}{
		{
			name:       "rule action overrides default",
			ruleAction: squirrelv1alpha1.ActionSkip,
			defAction:  squirrelv1alpha1.ActionRewrite,
			wantBack:   engine.ActionSkip,
		},
		{
			name:      "default action used when rule action empty",
			defAction: squirrelv1alpha1.ActionSkip,
			wantBack:  engine.ActionSkip,
		},
		{
			name:     "both empty falls back to rewrite",
			wantBack: engine.ActionRewrite,
		},
		{
			name:       "rule action wins even with empty default",
			ruleAction: squirrelv1alpha1.ActionSkip,
			wantBack:   engine.ActionSkip,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := effectiveAction(tt.ruleAction, tt.defAction); got != tt.wantBack {
				t.Errorf("effectiveAction: got %q, want %q", got, tt.wantBack)
			}
		})
	}
}

func TestEffectivePriorityOverrideWinsOverSpecificity(t *testing.T) {
	t.Parallel()

	priority := int32(99999)
	match := imageref.Match{Registry: "docker.io", Repository: "library/**"}
	got := effectivePriority(squirrelv1alpha1.Rule{Priority: &priority}, match)
	if got != 99999 {
		t.Errorf("priority override: got %d, want 99999", got)
	}
}

func TestEffectivePriorityDefaultsToSpecificity(t *testing.T) {
	t.Parallel()

	match := imageref.Match{Registry: "docker.io", Repository: "library/nginx", Tag: "1.21"}
	got := effectivePriority(squirrelv1alpha1.Rule{}, match)
	want := int32(match.Specificity()) //nolint:gosec // glob length is bounded by the K8s 1MB object limit, far below MaxInt32
	if got != want {
		t.Errorf("default priority: got %d, want %d (specificity)", got, want)
	}
}

func TestCompileRuleFromStringMatchHappyPath(t *testing.T) {
	t.Parallel()

	rule := squirrelv1alpha1.Rule{
		Match: matchString(t, "docker.io/**:*"),
	}
	got, err := compileRule(rule, &squirrelv1alpha1.Target{Registry: "mirror.internal"}, "", fixedSource)
	if err != nil {
		t.Fatalf("compileRule: %v", err)
	}
	wantMatch := imageref.Match{Registry: "docker.io", Repository: "**", Tag: "*"}
	if got.Match != wantMatch {
		t.Errorf("Match: got %+v, want %+v", got.Match, wantMatch)
	}
	if got.Action != engine.ActionRewrite {
		t.Errorf("Action: got %q, want %q", got.Action, engine.ActionRewrite)
	}
	if got.Target.Registry != "mirror.internal" {
		t.Errorf("Target.Registry: got %q, want mirror.internal", got.Target.Registry)
	}
	if got.Source != fixedSource {
		t.Errorf("Source: got %+v, want %+v", got.Source, fixedSource)
	}
	wantPriority := int32(wantMatch.Specificity()) //nolint:gosec // glob length is bounded by the K8s 1MB object limit, far below MaxInt32
	if got.Priority != wantPriority {
		t.Errorf("Priority: got %d, want %d (specificity)", got.Priority, wantPriority)
	}
}

func TestCompileRuleSkipFromDefaultAction(t *testing.T) {
	t.Parallel()

	rule := squirrelv1alpha1.Rule{
		Match: matchObject(t, squirrelv1alpha1.Match{Repository: "library/distroless-base"}),
		// No explicit Action; defaultAction is Skip.
	}
	got, err := compileRule(rule, nil, squirrelv1alpha1.ActionSkip, fixedSource)
	if err != nil {
		t.Fatalf("compileRule: %v", err)
	}
	if got.Action != engine.ActionSkip {
		t.Errorf("Action: got %q, want %q (inherited from defaultAction)", got.Action, engine.ActionSkip)
	}
}

func TestCompileRuleSurfacesMatchErrors(t *testing.T) {
	t.Parallel()

	// Raw `null` reaches MatchExpression as a bytes-form null; the
	// compile path must surface the parse error rather than emitting
	// a zero CompiledRule that would silently match every image.
	rule := squirrelv1alpha1.Rule{
		Match: apiextensionsv1.JSON{Raw: []byte("null")},
	}
	if _, err := compileRule(rule, nil, "", fixedSource); err == nil {
		t.Errorf("compileRule on null match: expected error, got nil")
	}
}

func TestCompilePolicyRulesProducesCompiledSliceAndSurfacesErrors(t *testing.T) {
	t.Parallel()

	priority := int32(42)
	rules := []squirrelv1alpha1.Rule{
		{Match: matchString(t, "docker.io/library/**:*")},
		{Match: apiextensionsv1.JSON{Raw: []byte("42")}}, // scalar, will fail
		{Match: matchString(t, "gcr.io/**:*"), Priority: &priority},
	}
	sourceFor := func(i int) engine.RuleSource {
		return engine.RuleSource{Scope: engine.ScopeCluster, Name: "mirror", RuleIndex: i}
	}
	compiled, errs := compilePolicyRules(rules, &squirrelv1alpha1.Target{Registry: "mirror.internal"}, "", sourceFor)

	if len(compiled) != 2 {
		t.Errorf("compiled count: got %d, want 2 (rule index 1 must be skipped)", len(compiled))
	}
	if len(errs) != 1 {
		t.Errorf("errs count: got %d, want 1 (rule index 1 failed)", len(errs))
	}
	// Spot-check that the successful rules carry the right priorities
	// and source.
	if compiled[0].Source.RuleIndex != 0 {
		t.Errorf("first compiled rule RuleIndex: got %d, want 0", compiled[0].Source.RuleIndex)
	}
	if compiled[1].Source.RuleIndex != 2 {
		t.Errorf("second compiled rule RuleIndex: got %d, want 2 (skipped index 1)", compiled[1].Source.RuleIndex)
	}
	if compiled[1].Priority != 42 {
		t.Errorf("priority override: got %d, want 42", compiled[1].Priority)
	}
}
