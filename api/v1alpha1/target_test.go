package v1alpha1_test

import (
	"encoding/json"
	"reflect"
	"testing"

	squirrelv1alpha1 "github.com/RemkoMolier/squirrel/api/v1alpha1"
)

func TestTargetEffectiveTagsPromotesSugar(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		t    squirrelv1alpha1.Target
		want []string
	}{
		{
			name: "tag sugar produces single-entry list",
			t:    squirrelv1alpha1.Target{Tag: "{tag}"},
			want: []string{"{tag}"},
		},
		{
			name: "tags list passes through unchanged",
			t:    squirrelv1alpha1.Target{Tags: []string{"{tag}", "{digest:short8}"}},
			want: []string{"{tag}", "{digest:short8}"},
		},
		{
			name: "empty target returns nil",
			t:    squirrelv1alpha1.Target{Registry: "mirror.internal"},
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tt.t.EffectiveTags()
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("EffectiveTags(): got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTargetHasBothTagForms(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		t    squirrelv1alpha1.Target
		want bool
	}{
		{name: "neither set", t: squirrelv1alpha1.Target{}, want: false},
		{name: "only tag set", t: squirrelv1alpha1.Target{Tag: "{tag}"}, want: false},
		{name: "only tags set", t: squirrelv1alpha1.Target{Tags: []string{"{tag}"}}, want: false},
		{name: "both set", t: squirrelv1alpha1.Target{Tag: "{tag}", Tags: []string{"{tag}"}}, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.t.HasBothTagForms(); got != tt.want {
				t.Errorf("HasBothTagForms(): got %t, want %t", got, tt.want)
			}
		})
	}
}

func TestTargetRoundTripJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		json string
		want squirrelv1alpha1.Target
	}{
		{
			name: "full target with tags list",
			json: `{"registry":"mirror.internal","repository":"dockerhub/{repository}","tags":["{tag}","{digest:short8}"]}`,
			want: squirrelv1alpha1.Target{
				Registry:   "mirror.internal",
				Repository: "dockerhub/{repository}",
				Tags:       []string{"{tag}", "{digest:short8}"},
			},
		},
		{
			name: "registry only",
			json: `{"registry":"mirror.internal"}`,
			want: squirrelv1alpha1.Target{Registry: "mirror.internal"},
		},
		{
			name: "tag sugar preserved as singular field",
			json: `{"registry":"mirror.internal","tag":"latest"}`,
			want: squirrelv1alpha1.Target{Registry: "mirror.internal", Tag: "latest"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var got squirrelv1alpha1.Target
			if err := json.Unmarshal([]byte(tt.json), &got); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Unmarshal: got %+v, want %+v", got, tt.want)
			}
			back, err := json.Marshal(got)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if string(back) != tt.json {
				t.Errorf("re-Marshal: got %s, want %s", back, tt.json)
			}
		})
	}
}

func TestTargetDeepCopyIsIndependent(t *testing.T) {
	t.Parallel()

	orig := squirrelv1alpha1.Target{
		Registry:   "mirror.internal",
		Repository: "{repository}",
		Tags:       []string{"{tag}", "{digest:short8}"},
	}
	clone := orig.DeepCopy()
	clone.Tags[0] = "mutated"
	if orig.Tags[0] != "{tag}" {
		t.Errorf("DeepCopy: mutating clone.Tags[0] affected the original; got %q, want %q",
			orig.Tags[0], "{tag}")
	}
}
