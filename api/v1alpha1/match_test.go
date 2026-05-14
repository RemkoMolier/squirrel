package v1alpha1_test

import (
	"encoding/json"
	"testing"

	squirrelv1alpha1 "github.com/RemkoMolier/squirrel/api/v1alpha1"
)

func TestMatchExprUnmarshalJSONStringForm(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input string
		want  string
	}{
		{name: "dockerhub mirror glob", input: `"docker.io/library/**:*"`, want: "docker.io/library/**:*"},
		{name: "fully wildcarded glob", input: `"*/**:*@*"`, want: "*/**:*@*"},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var m squirrelv1alpha1.MatchExpr
			if err := json.Unmarshal([]byte(tt.input), &m); err != nil {
				t.Fatalf("Unmarshal(%q): %v", tt.input, err)
			}
			if m.Structured != nil {
				t.Errorf("Structured: got %+v, want nil", m.Structured)
			}
			if m.String != tt.want {
				t.Errorf("String: got %q, want %q", m.String, tt.want)
			}
		})
	}
}

func TestMatchExprUnmarshalJSONStructuredForm(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input string
		want  squirrelv1alpha1.Match
	}{
		{
			name:  "registry only",
			input: `{"registry":"docker.io"}`,
			want:  squirrelv1alpha1.Match{Registry: "docker.io"},
		},
		{
			name:  "all four fields",
			input: `{"registry":"docker.io","repository":"library/**","tag":"1.*","digest":"*"}`,
			want:  squirrelv1alpha1.Match{Registry: "docker.io", Repository: "library/**", Tag: "1.*", Digest: "*"},
		},
		{
			name:  "empty object yields zero-value Match",
			input: `{}`,
			want:  squirrelv1alpha1.Match{},
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var m squirrelv1alpha1.MatchExpr
			if err := json.Unmarshal([]byte(tt.input), &m); err != nil {
				t.Fatalf("Unmarshal(%s): %v", tt.input, err)
			}
			if m.String != "" {
				t.Errorf("String: got %q, want empty", m.String)
			}
			if m.Structured == nil {
				t.Fatalf("Structured: got nil, want %+v", tt.want)
			}
			if *m.Structured != tt.want {
				t.Errorf("Structured: got %+v, want %+v", *m.Structured, tt.want)
			}
		})
	}
}

// TestMatchExprUnmarshalJSONRejectsEmptyString catches the silent-zero
// bug: an empty string would otherwise pass the string-form branch and
// produce a MatchExpr indistinguishable from the zero value (which the
// matcher treats as "match every image"). Reject it at unmarshal time
// so MatchExpression() consistently surfaces it as InvalidMatch.
func TestMatchExprUnmarshalJSONRejectsEmptyString(t *testing.T) {
	t.Parallel()

	var m squirrelv1alpha1.MatchExpr
	err := json.Unmarshal([]byte(`""`), &m)
	if err == nil {
		t.Errorf(`Unmarshal(""): expected error, got nil; m=%+v`, m)
	}
}

func TestMatchExprUnmarshalJSONRejectsNull(t *testing.T) {
	t.Parallel()

	// `null` is rejected by MatchExpr.UnmarshalJSON because the API
	// requires match to be a glob string or a structured object. Callers
	// that bypass the API server's CEL validation (fake clients,
	// fixtures, raw etcd payloads, persisted-before-CEL data) hit this
	// path; without the rejection, null silently resolves to a zero
	// MatchExpr that the matcher would treat as a match-everything
	// catch-all - silently broadening a narrow rule.
	var m squirrelv1alpha1.MatchExpr
	err := json.Unmarshal([]byte("null"), &m)
	if err == nil {
		t.Errorf("Unmarshal(null): expected error, got nil; m=%+v", m)
	}
}

func TestMatchExprUnmarshalJSONRejectsUnsupportedShapes(t *testing.T) {
	t.Parallel()

	cases := []string{
		`42`,
		`true`,
		`[1, 2, 3]`,
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			t.Parallel()
			var m squirrelv1alpha1.MatchExpr
			if err := json.Unmarshal([]byte(in), &m); err == nil {
				t.Errorf("Unmarshal(%s): expected error, got nil; m=%+v", in, m)
			}
		})
	}
}

// TestMatchExprUnmarshalJSONRejectsUnknownFields is the regression guard
// against silently-broadening typos. With the CRD's match field
// schemaless (necessary for the string-or-object union), the API server
// will not reject keys like `registrry` before this unmarshaller runs;
// the strict decoder must catch them here, otherwise the zero-valued
// Match defaults to matching every image.
func TestMatchExprUnmarshalJSONRejectsUnknownFields(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input string
	}{
		{name: "typo in registry", input: `{"registrry":"docker.io"}`},
		{name: "typo in repository", input: `{"repostiory":"library/nginx"}`}, //nolint:misspell // intentional typo: the test asserts the strict decoder rejects misspelled keys
		{name: "totally unknown field", input: `{"foo":"bar"}`},
		{name: "known plus unknown field", input: `{"registry":"docker.io","extras":"foo"}`},
		{name: "all four known fields plus unknown", input: `{"registry":"docker.io","repository":"library/**","tag":"1.*","digest":"*","mode":"strict"}`},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var m squirrelv1alpha1.MatchExpr
			err := json.Unmarshal([]byte(tt.input), &m)
			if err == nil {
				t.Errorf("Unmarshal(%s): expected error for unknown field, got nil; m=%+v", tt.input, m)
			}
		})
	}
}

func TestMatchExprMarshalJSONEmitsStringWhenSet(t *testing.T) {
	t.Parallel()

	m := squirrelv1alpha1.MatchExpr{String: "docker.io/**:*"}
	got, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if want := `"docker.io/**:*"`; string(got) != want {
		t.Errorf("Marshal: got %s, want %s", got, want)
	}
}

func TestMatchExprMarshalJSONEmitsStructuredWhenSet(t *testing.T) {
	t.Parallel()

	m := squirrelv1alpha1.MatchExpr{
		Structured: &squirrelv1alpha1.Match{Registry: "docker.io", Repository: "library/**"},
	}
	got, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if want := `{"registry":"docker.io","repository":"library/**"}`; string(got) != want {
		t.Errorf("Marshal: got %s, want %s", got, want)
	}
}

func TestMatchExprMarshalJSONEmitsNullWhenUnset(t *testing.T) {
	t.Parallel()

	var m squirrelv1alpha1.MatchExpr
	got, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(got) != "null" {
		t.Errorf("Marshal: got %s, want null", got)
	}
}

func TestMatchExprRoundTripStringForm(t *testing.T) {
	t.Parallel()

	orig := squirrelv1alpha1.MatchExpr{String: "docker.io/library/**:*"}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var back squirrelv1alpha1.MatchExpr
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if back != orig {
		t.Errorf("round-trip: got %+v, want %+v", back, orig)
	}
}

func TestMatchExprRoundTripStructuredForm(t *testing.T) {
	t.Parallel()

	structured := &squirrelv1alpha1.Match{Registry: "docker.io", Repository: "library/**", Tag: "*"}
	orig := squirrelv1alpha1.MatchExpr{Structured: structured}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var back squirrelv1alpha1.MatchExpr
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if back.String != "" {
		t.Errorf("round-trip: String = %q, want empty", back.String)
	}
	if back.Structured == nil || *back.Structured != *structured {
		t.Errorf("round-trip: Structured = %+v, want %+v", back.Structured, structured)
	}
}

func TestMatchDeepCopyIsIndependent(t *testing.T) {
	t.Parallel()

	orig := squirrelv1alpha1.MatchExpr{
		Structured: &squirrelv1alpha1.Match{Registry: "docker.io", Repository: "library/**"},
	}
	clone := orig.DeepCopy()

	if clone.Structured == orig.Structured {
		t.Fatal("DeepCopy returned the same pointer for Structured")
	}

	clone.Structured.Registry = "gcr.io"
	if orig.Structured.Registry != "docker.io" {
		t.Errorf("DeepCopy: mutating clone.Structured.Registry affected the original; got %q, want %q",
			orig.Structured.Registry, "docker.io")
	}
}
