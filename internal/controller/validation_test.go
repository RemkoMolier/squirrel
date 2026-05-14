package controller_test

import (
	"encoding/json"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	squirrelv1alpha1 "github.com/RemkoMolier/squirrel/api/v1alpha1"
	"github.com/RemkoMolier/squirrel/internal/controller"
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

// matchRaw builds a Rule.Match payload from arbitrary raw bytes; used
// for the negative tests where MatchExpression() must reject the
// payload (scalars, malformed objects, null, etc.).
func matchRaw(s string) apiextensionsv1.JSON {
	return apiextensionsv1.JSON{Raw: []byte(s)}
}

func TestValidateSpecEmptyRules(t *testing.T) {
	t.Parallel()

	res := controller.ValidateSpec(nil, nil, "")
	if !res.HasErrors() {
		t.Fatalf("expected EmptyRules error, got %+v", res)
	}
	if got := res.Errors[0].Reason; got != squirrelv1alpha1.ReasonEmptyRules {
		t.Errorf("Reason: got %q, want %q", got, squirrelv1alpha1.ReasonEmptyRules)
	}
	if got := res.Errors[0].RuleIndex; got != controller.SpecRuleIndex {
		t.Errorf("RuleIndex: got %d, want SpecRuleIndex (%d)", got, controller.SpecRuleIndex)
	}
}

// TestValidateSpecInvalidMatchOnMalformedGlob is the regression guard
// against the documented gap where structured-form matches with a
// malformed glob landed Accepted=True and silently matched nothing at
// admission (matchGlob returns false on compile failures). String-form
// matches also slipped through because ParseMatchString only splits
// the reference - it doesn't compile the resulting field globs. Both
// forms are now glob-validated via imageref.ValidateGlob.
func TestValidateSpecInvalidMatchOnMalformedGlob(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		match apiextensionsv1.JSON
	}{
		{
			name:  "structured match with empty character class",
			match: matchObject(t, squirrelv1alpha1.Match{Registry: "docker.io", Repository: "[]"}),
		},
		{
			name:  "structured match with unterminated character class",
			match: matchObject(t, squirrelv1alpha1.Match{Repository: "library/["}),
		},
		{
			name:  "structured match with truncated range",
			match: matchObject(t, squirrelv1alpha1.Match{Repository: "library/[a-"}),
		},
		{
			name:  "string-form match with unterminated character class in repository",
			match: matchString(t, "docker.io/library/["),
		},
		{
			name:  "string-form match with empty character class in tag",
			match: matchString(t, "docker.io/library/nginx:[]"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			res := controller.ValidateSpec(
				[]squirrelv1alpha1.Rule{{Match: tt.match}},
				&squirrelv1alpha1.Target{Registry: "mirror.internal"},
				squirrelv1alpha1.ActionRewrite,
			)
			if !res.HasErrors() {
				t.Fatalf("expected InvalidMatch error, got %+v", res)
			}
			if got := res.Errors[0].Reason; got != squirrelv1alpha1.ReasonInvalidMatch {
				t.Errorf("Reason: got %q, want %q", got, squirrelv1alpha1.ReasonInvalidMatch)
			}
		})
	}
}

// TestValidateSpecInvalidMatchOnPartialDigestPattern pins the
// reconciler-side check that catches operator errors on
// Match.Digest. Cryptographic digests are content-addressed; a
// partial-prefix pattern like "sha256:abc*" suggests the author
// thought hash prefixes were meaningful (they are not, in the OCI
// model). Accepted: empty, "*", literal "<alg>:<hex>".
func TestValidateSpecInvalidMatchOnPartialDigestPattern(t *testing.T) {
	t.Parallel()

	rejected := []struct {
		name  string
		match apiextensionsv1.JSON
	}{
		{
			name:  "structured digest with trailing glob",
			match: matchObject(t, squirrelv1alpha1.Match{Registry: "docker.io", Digest: "sha256:abc*"}),
		},
		{
			name:  "structured digest with question mark",
			match: matchObject(t, squirrelv1alpha1.Match{Digest: "sha256:abc?"}),
		},
		{
			name:  "structured digest with character class",
			match: matchObject(t, squirrelv1alpha1.Match{Digest: "sha256:[a-f]"}),
		},
		{
			name:  "structured digest with doublestar",
			match: matchObject(t, squirrelv1alpha1.Match{Digest: "sha**"}),
		},
		{
			name:  "structured digest missing algorithm",
			match: matchObject(t, squirrelv1alpha1.Match{Digest: ":abc"}),
		},
		{
			name:  "structured digest missing hex",
			match: matchObject(t, squirrelv1alpha1.Match{Digest: "sha256:"}),
		},
	}
	for _, tt := range rejected {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			res := controller.ValidateSpec(
				[]squirrelv1alpha1.Rule{{Match: tt.match}},
				&squirrelv1alpha1.Target{Registry: "mirror.internal"},
				squirrelv1alpha1.ActionRewrite,
			)
			if !res.HasErrors() {
				t.Fatalf("expected InvalidMatch error, got %+v", res)
			}
			if got := res.Errors[0].Reason; got != squirrelv1alpha1.ReasonInvalidMatch {
				t.Errorf("Reason: got %q, want %q", got, squirrelv1alpha1.ReasonInvalidMatch)
			}
		})
	}

	accepted := []struct {
		name  string
		match apiextensionsv1.JSON
	}{
		{
			name:  "structured digest empty",
			match: matchObject(t, squirrelv1alpha1.Match{Registry: "docker.io"}),
		},
		{
			name:  "structured digest bare wildcard",
			match: matchObject(t, squirrelv1alpha1.Match{Registry: "docker.io", Digest: "*"}),
		},
		{
			name:  "structured digest literal sha256",
			match: matchObject(t, squirrelv1alpha1.Match{Digest: "sha256:abc123"}),
		},
	}
	for _, tt := range accepted {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			res := controller.ValidateSpec(
				[]squirrelv1alpha1.Rule{{Match: tt.match}},
				&squirrelv1alpha1.Target{Registry: "mirror.internal"},
				squirrelv1alpha1.ActionRewrite,
			)
			for _, e := range res.Errors {
				if e.Reason == squirrelv1alpha1.ReasonInvalidMatch {
					t.Errorf("unexpected InvalidMatch error on a valid digest form: %s", e.Message)
				}
			}
		})
	}
}

func TestValidateSpecInvalidMatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		match apiextensionsv1.JSON
	}{
		{name: "scalar number rejected at MatchExpression boundary", match: matchRaw(`42`)},
		{name: "object with unknown field", match: matchRaw(`{"registrry":"docker.io"}`)},
		{name: "null payload", match: matchRaw(`null`)},
		{name: "empty payload bytes", match: apiextensionsv1.JSON{}},
		{name: "empty string match", match: matchRaw(`""`)},
		{name: "string-form without registry/repository separator",
			match: func() apiextensionsv1.JSON {
				return apiextensionsv1.JSON{Raw: []byte(`"nginx-only"`)}
			}(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			res := controller.ValidateSpec(
				[]squirrelv1alpha1.Rule{{Match: tt.match}},
				&squirrelv1alpha1.Target{Registry: "mirror.internal"},
				squirrelv1alpha1.ActionRewrite,
			)
			if !res.HasErrors() {
				t.Fatalf("expected InvalidMatch error, got %+v", res)
			}
			if got := res.Errors[0].Reason; got != squirrelv1alpha1.ReasonInvalidMatch {
				t.Errorf("Reason: got %q, want %q", got, squirrelv1alpha1.ReasonInvalidMatch)
			}
		})
	}
}

func TestValidateSpecMissingRegistry(t *testing.T) {
	t.Parallel()

	res := controller.ValidateSpec(
		[]squirrelv1alpha1.Rule{{Match: matchString(t, "docker.io/**:*")}},
		nil, // no defaultTarget
		"",  // defaultAction defaults to rewrite
	)
	if !res.HasErrors() {
		t.Fatalf("expected MissingRegistry error, got %+v", res)
	}
	if got := res.Errors[0].Reason; got != squirrelv1alpha1.ReasonMissingRegistry {
		t.Errorf("Reason: got %q, want %q", got, squirrelv1alpha1.ReasonMissingRegistry)
	}
}

func TestValidateSpecMissingRegistrySatisfiedByDefaultTarget(t *testing.T) {
	t.Parallel()

	// Rule provides only a repository override; defaultTarget supplies
	// the registry. The merged effective target is valid.
	res := controller.ValidateSpec(
		[]squirrelv1alpha1.Rule{{
			Match: matchString(t, "docker.io/**:*"),
			Target: &squirrelv1alpha1.Target{
				Repository: "{repository:image}",
			},
		}},
		&squirrelv1alpha1.Target{Registry: "mirror.internal"},
		"",
	)
	if res.HasErrors() {
		t.Errorf("expected no errors, got %+v", res.Errors)
	}
}

func TestValidateSpecInvalidPlaceholder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		rule squirrelv1alpha1.Rule
	}{
		{
			name: "unknown placeholder in registry",
			rule: squirrelv1alpha1.Rule{
				Match: matchString(t, "docker.io/**:*"),
				Target: &squirrelv1alpha1.Target{
					Registry: "{regis}",
				},
			},
		},
		{
			name: "unknown sub-form in repository",
			rule: squirrelv1alpha1.Rule{
				Match: matchString(t, "docker.io/**:*"),
				Target: &squirrelv1alpha1.Target{
					Registry:   "mirror.internal",
					Repository: "{repository:typo}",
				},
			},
		},
		{
			name: "bare {digest} in tag template",
			rule: squirrelv1alpha1.Rule{
				Match: matchString(t, "docker.io/**:*"),
				Target: &squirrelv1alpha1.Target{
					Registry: "mirror.internal",
					Tags:     []string{"{digest}"},
				},
			},
		},
		{
			name: "unmatched brace in repository template",
			rule: squirrelv1alpha1.Rule{
				Match: matchString(t, "docker.io/**:*"),
				Target: &squirrelv1alpha1.Target{
					Registry:   "mirror.internal",
					Repository: "mirror/{repository:image",
				},
			},
		},
		{
			name: "negative N in digest short-form",
			rule: squirrelv1alpha1.Rule{
				Match: matchString(t, "docker.io/**:*"),
				Target: &squirrelv1alpha1.Target{
					Registry: "mirror.internal",
					Tags:     []string{"{digest:short0}"},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			res := controller.ValidateSpec([]squirrelv1alpha1.Rule{tt.rule}, nil, "")
			if !res.HasErrors() {
				t.Fatalf("expected InvalidPlaceholder error, got %+v", res)
			}
			if got := res.Errors[0].Reason; got != squirrelv1alpha1.ReasonInvalidPlaceholder {
				t.Errorf("Reason: got %q, want %q", got, squirrelv1alpha1.ReasonInvalidPlaceholder)
			}
		})
	}
}

func TestValidateSpecActionTargetConflictOnSkip(t *testing.T) {
	t.Parallel()

	res := controller.ValidateSpec(
		[]squirrelv1alpha1.Rule{{
			Match:  matchString(t, "docker.io/library/distroless-base:*"),
			Action: squirrelv1alpha1.ActionSkip,
			Target: &squirrelv1alpha1.Target{Registry: "mirror.internal"},
		}},
		nil, "",
	)
	if !res.HasErrors() {
		t.Fatalf("expected ActionTargetConflict, got %+v", res)
	}
	if got := res.Errors[0].Reason; got != squirrelv1alpha1.ReasonActionTargetConflict {
		t.Errorf("Reason: got %q, want %q", got, squirrelv1alpha1.ReasonActionTargetConflict)
	}
}

func TestValidateSpecActionTargetConflictBothTagForms(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		rule squirrelv1alpha1.Rule
		def  *squirrelv1alpha1.Target
	}{
		{
			name: "rule.target has both tag and tags",
			rule: squirrelv1alpha1.Rule{
				Match: matchString(t, "docker.io/**:*"),
				Target: &squirrelv1alpha1.Target{
					Registry: "mirror.internal",
					Tag:      "{tag}",
					Tags:     []string{"{tag}"},
				},
			},
		},
		{
			name: "spec.defaultTarget has both tag and tags",
			rule: squirrelv1alpha1.Rule{Match: matchString(t, "docker.io/**:*")},
			def: &squirrelv1alpha1.Target{
				Registry: "mirror.internal",
				Tag:      "{tag}",
				Tags:     []string{"{tag}"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			res := controller.ValidateSpec([]squirrelv1alpha1.Rule{tt.rule}, tt.def, "")
			if !res.HasErrors() {
				t.Fatalf("expected ActionTargetConflict, got %+v", res)
			}
			if got := res.Errors[0].Reason; got != squirrelv1alpha1.ReasonActionTargetConflict {
				t.Errorf("Reason: got %q, want %q", got, squirrelv1alpha1.ReasonActionTargetConflict)
			}
		})
	}
}

func TestValidateSpecPriorityIgnoredOnSkipIsWarning(t *testing.T) {
	t.Parallel()

	priority := int32(99999)
	res := controller.ValidateSpec(
		[]squirrelv1alpha1.Rule{{
			Match:    matchString(t, "docker.io/library/distroless-base:*"),
			Action:   squirrelv1alpha1.ActionSkip,
			Priority: &priority,
		}},
		nil, "",
	)
	if res.HasErrors() {
		t.Errorf("expected no errors, got %+v", res.Errors)
	}
	if !res.HasWarnings() {
		t.Fatalf("expected PriorityIgnoredOnSkip warning, got %+v", res)
	}
	if got := res.Warnings[0].Reason; got != squirrelv1alpha1.ReasonPriorityIgnoredOnSkip {
		t.Errorf("Warning Reason: got %q, want %q", got, squirrelv1alpha1.ReasonPriorityIgnoredOnSkip)
	}
}

func TestValidateSpecHappyPathFromDesignUserStories(t *testing.T) {
	t.Parallel()

	// The three design user stories must all validate cleanly. This
	// guards against accidental over-strict validation that would
	// reject documented policies.
	tests := []struct {
		name          string
		rules         []squirrelv1alpha1.Rule
		defaultTarget *squirrelv1alpha1.Target
	}{
		{
			name: "dockerhub mirror with default target",
			rules: []squirrelv1alpha1.Rule{
				{Match: matchString(t, "docker.io/**:*")},
			},
			defaultTarget: &squirrelv1alpha1.Target{
				Registry:   "mirror.internal",
				Repository: "dockerhub/{repository}",
				Tags:       []string{"{tag}", "{digest:short8}"},
			},
		},
		{
			name: "skip a specific image cluster-wide",
			rules: []squirrelv1alpha1.Rule{{
				Match:  matchString(t, "docker.io/library/distroless-base:*"),
				Action: squirrelv1alpha1.ActionSkip,
			}},
		},
		{
			name: "per-namespace override",
			rules: []squirrelv1alpha1.Rule{{
				Match: matchString(t, "docker.io/library/nginx:*"),
				Target: &squirrelv1alpha1.Target{
					Registry:   "registry.internal",
					Repository: "payments/nginx-patched",
					Tags:       []string{"{tag}"},
				},
			}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			res := controller.ValidateSpec(tt.rules, tt.defaultTarget, "")
			if res.HasErrors() {
				t.Errorf("expected no errors, got %+v", res.Errors)
			}
		})
	}
}

func TestValidateSpecStructuredMatchValidates(t *testing.T) {
	t.Parallel()

	res := controller.ValidateSpec(
		[]squirrelv1alpha1.Rule{{
			Match: matchObject(t, squirrelv1alpha1.Match{
				Registry:   "docker.io",
				Repository: "library/**",
				Tag:        "1.*",
			}),
		}},
		&squirrelv1alpha1.Target{Registry: "mirror.internal"},
		"",
	)
	if res.HasErrors() {
		t.Errorf("expected no errors, got %+v", res.Errors)
	}
}

// TestValidateSpecCollectsBothTagFormsAndMissingRegistry pins the
// no-early-return behaviour: a rule that sets both Tag/Tags AND lacks
// a registry surfaces both errors in a single ValidateSpec call so
// operators see every problem in one pass.
func TestValidateSpecCollectsBothTagFormsAndMissingRegistry(t *testing.T) {
	t.Parallel()

	res := controller.ValidateSpec(
		[]squirrelv1alpha1.Rule{{
			Match: matchString(t, "docker.io/**:*"),
			Target: &squirrelv1alpha1.Target{
				// No registry, and both tag forms set.
				Tag:  "{tag}",
				Tags: []string{"{tag}"},
			},
		}},
		nil, // no defaultTarget either
		"",
	)
	if len(res.Errors) < 2 {
		t.Fatalf("expected both ActionTargetConflict and MissingRegistry, got %d error(s): %+v", len(res.Errors), res.Errors)
	}
	wantReasons := map[string]bool{
		squirrelv1alpha1.ReasonActionTargetConflict: false,
		squirrelv1alpha1.ReasonMissingRegistry:      false,
	}
	for _, e := range res.Errors {
		if _, ok := wantReasons[e.Reason]; ok {
			wantReasons[e.Reason] = true
		}
	}
	for reason, found := range wantReasons {
		if !found {
			t.Errorf("expected error with Reason %q, none surfaced; errors=%+v", reason, res.Errors)
		}
	}
}

func TestValidateSpecCollectsMultipleErrors(t *testing.T) {
	t.Parallel()

	// Two rules, two distinct error reasons. The reconciler picks the
	// first reason for the Accepted condition but the result carries
	// every error so the message can list them all.
	res := controller.ValidateSpec(
		[]squirrelv1alpha1.Rule{
			{
				Match:  matchString(t, "docker.io/library/distroless-base:*"),
				Action: squirrelv1alpha1.ActionSkip,
				Target: &squirrelv1alpha1.Target{Registry: "ignored.internal"},
			},
			{
				Match: matchString(t, "docker.io/**:*"),
				Target: &squirrelv1alpha1.Target{
					Registry: "{repos}", // unknown placeholder
				},
			},
		},
		nil, "",
	)
	if got, want := len(res.Errors), 2; got != want {
		t.Fatalf("expected %d errors, got %d (%+v)", want, got, res.Errors)
	}
	if res.Errors[0].Reason != squirrelv1alpha1.ReasonActionTargetConflict {
		t.Errorf("first error Reason: got %q, want %q", res.Errors[0].Reason, squirrelv1alpha1.ReasonActionTargetConflict)
	}
	if res.Errors[1].Reason != squirrelv1alpha1.ReasonInvalidPlaceholder {
		t.Errorf("second error Reason: got %q, want %q", res.Errors[1].Reason, squirrelv1alpha1.ReasonInvalidPlaceholder)
	}
	if res.Errors[0].RuleIndex != 0 || res.Errors[1].RuleIndex != 1 {
		t.Errorf("rule indices: got [%d, %d], want [0, 1]", res.Errors[0].RuleIndex, res.Errors[1].RuleIndex)
	}
}

func TestValidateNamespaceSelectorNilIsValid(t *testing.T) {
	t.Parallel()

	if errs := controller.ValidateNamespaceSelector(nil); len(errs) != 0 {
		t.Errorf("nil selector: got %d errors, want 0", len(errs))
	}
}

func TestValidateNamespaceSelectorAcceptsValidSelectors(t *testing.T) {
	t.Parallel()

	cases := []*metav1.LabelSelector{
		{}, // empty selector - matches everything
		{MatchLabels: map[string]string{"tier": "production"}},
		{MatchExpressions: []metav1.LabelSelectorRequirement{
			{Key: "tier", Operator: metav1.LabelSelectorOpIn, Values: []string{"production", "staging"}},
		}},
		{MatchExpressions: []metav1.LabelSelectorRequirement{
			{Key: "tier", Operator: metav1.LabelSelectorOpExists},
		}},
	}
	for i, sel := range cases {
		if errs := controller.ValidateNamespaceSelector(sel); len(errs) != 0 {
			t.Errorf("case %d: got %d errors, want 0 (errs=%+v)", i, len(errs), errs)
		}
	}
}

// TestValidateNamespaceSelectorRejectsInvalidSelectors covers the
// failure mode the apiserver schema does not catch: a structurally
// valid LabelSelector whose contents cannot be compiled to a
// labels.Selector. Without this validation the policy would be
// Accepted=True and the webhook would silently skip it at admission
// (the LabelSelectorAsSelector call in ApplicableRules fails and the
// resolver continues past the policy), leaving operators with no
// visible explanation for why their rules never run.
func TestValidateNamespaceSelectorRejectsInvalidSelectors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		sel  *metav1.LabelSelector
	}{
		{
			name: "unsupported operator",
			sel: &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{
				{Key: "tier", Operator: metav1.LabelSelectorOperator("BogusOp"), Values: []string{"x"}},
			}},
		},
		{
			name: "invalid label key",
			sel: &metav1.LabelSelector{MatchLabels: map[string]string{
				"not a valid label key!": "x",
			}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			errs := controller.ValidateNamespaceSelector(tc.sel)
			if len(errs) != 1 {
				t.Fatalf("got %d errors, want 1 (%+v)", len(errs), errs)
			}
			if errs[0].Reason != squirrelv1alpha1.ReasonInvalidNamespaceSelector {
				t.Errorf("Reason: got %q, want %q", errs[0].Reason, squirrelv1alpha1.ReasonInvalidNamespaceSelector)
			}
			if errs[0].RuleIndex != controller.SpecRuleIndex {
				t.Errorf("RuleIndex: got %d, want SpecRuleIndex (%d)", errs[0].RuleIndex, controller.SpecRuleIndex)
			}
		})
	}
}
