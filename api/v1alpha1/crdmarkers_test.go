package v1alpha1_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// TestRuleMatchFieldHasUnionMarkers guards against the controller-gen
// pitfall where Schemaless / XPreserveUnknownFields markers placed on a
// struct type (rather than the use-site field) produce an OpenAPI schema
// of `type: object`. The Kubernetes API server then rejects the
// documented `match: "docker.io/**:*"` shorthand before MatchExpr's
// custom UnmarshalJSON can run.
//
// The right place for these markers is the Rule.Match field declaration.
// Parse rule.go and assert both markers appear on that field's doc
// comment so a future contributor cannot silently move them back.
func TestRuleMatchFieldHasUnionMarkers(t *testing.T) {
	t.Parallel()

	src, err := os.ReadFile("rule.go")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "rule.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	matchComment, ok := findFieldDoc(file, "Rule", "Match")
	if !ok {
		t.Fatal("could not find Rule.Match field in rule.go")
	}

	for _, marker := range []string{
		"+kubebuilder:validation:Schemaless",
		"+kubebuilder:validation:XPreserveUnknownFields",
	} {
		if !strings.Contains(matchComment, marker) {
			t.Errorf("Rule.Match is missing marker %q on the field declaration.\n"+
				"Placing the marker on the MatchExpr *type* instead causes\n"+
				"controller-gen to emit OpenAPI `type: object`, which makes\n"+
				"the API server reject `match: \"docker.io/**:*\"`.\n\n"+
				"Field doc was:\n%s",
				marker, matchComment)
		}
	}
}

// findFieldDoc returns the doc comment text of fieldName on the struct
// type structName declared in file. ok is false when either is missing.
func findFieldDoc(file *ast.File, structName, fieldName string) (string, bool) {
	var got string
	var found bool

	ast.Inspect(file, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name == nil || ts.Name.Name != structName {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok || st.Fields == nil {
			return false
		}
		for _, field := range st.Fields.List {
			for _, name := range field.Names {
				if name.Name == fieldName && field.Doc != nil {
					got = field.Doc.Text()
					found = true
					return false
				}
			}
		}
		return false
	})
	return got, found
}
