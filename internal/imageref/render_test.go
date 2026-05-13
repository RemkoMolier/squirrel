package imageref

import "testing"

var (
	hubNginx = Image{
		Registry:   "docker.io",
		Repository: "library/nginx",
		Tag:        "1.21",
		Digest:     testDigestSHA256,
	}
	gcrThreeSegment = Image{
		Registry:   "gcr.io",
		Repository: "google_containers/etcd-io/etcd",
		Tag:        "3.5",
	}
	singleSegment = Image{
		Registry:   "myregistry.io",
		Repository: "nginx",
		Tag:        "latest",
	}
	noDigest = Image{
		Registry:   "docker.io",
		Repository: "library/nginx",
		Tag:        "1.21",
	}
)

func TestRenderFieldKnownPlaceholders(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		template string
		img      Image
		want     string
	}{
		{name: "literal passes through unchanged", template: "mirror.internal", img: hubNginx, want: "mirror.internal"},
		{name: "{registry} substitutes the registry", template: "{registry}", img: hubNginx, want: "docker.io"},
		{name: "{repository} substitutes the full repository", template: "{repository}", img: hubNginx, want: "library/nginx"},
		{name: "{tag} substitutes the tag", template: "{tag}", img: hubNginx, want: "1.21"},
		{name: "{digest} substitutes the full digest including algorithm", template: "{digest}", img: hubNginx, want: testDigestSHA256},
		{name: "combined template", template: "{registry}/{repository}:{tag}", img: hubNginx, want: "docker.io/library/nginx:1.21"},
		{name: "{repository:owner} returns the first segment", template: "{repository:owner}", img: hubNginx, want: "library"},
		{name: "{repository:image} returns the last segment", template: "{repository:image}", img: hubNginx, want: "nginx"},
		{name: "{repository:path} returns everything before the last segment", template: "{repository:path}", img: hubNginx, want: "library"},
		{name: "{repository:flat} replaces slashes with dashes", template: "{repository:flat}", img: hubNginx, want: "library-nginx"},
		{name: "three-segment repo: owner is first segment", template: "{repository:owner}", img: gcrThreeSegment, want: "google_containers"},
		{name: "three-segment repo: image is last segment", template: "{repository:image}", img: gcrThreeSegment, want: "etcd"},
		{name: "three-segment repo: path is everything before last", template: "{repository:path}", img: gcrThreeSegment, want: "google_containers/etcd-io"},
		{name: "three-segment repo: flat replaces both slashes", template: "{repository:flat}", img: gcrThreeSegment, want: "google_containers-etcd-io-etcd"},
		{name: "single-segment repo: owner empty", template: "{repository:owner}", img: singleSegment, want: ""},
		{name: "single-segment repo: image equals the repo", template: "{repository:image}", img: singleSegment, want: "nginx"},
		{name: "single-segment repo: path empty", template: "{repository:path}", img: singleSegment, want: ""},
		{name: "single-segment repo: flat equals the repo", template: "{repository:flat}", img: singleSegment, want: "nginx"},
		{name: "{digest:hex} strips algorithm", template: "{digest:hex}", img: hubNginx, want: "1111111111111111111111111111111111111111111111111111111111111111"},
		{name: "{digest:algo} returns the algorithm", template: "{digest:algo}", img: hubNginx, want: "sha256"},
		{name: "{digest:short8} returns the first eight hex characters", template: "{digest:short8}", img: hubNginx, want: "11111111"},
		{name: "{digest:short12} returns the first twelve hex characters", template: "{digest:short12}", img: hubNginx, want: "111111111111"},
		{name: "{digest:short8} on an image with no digest returns empty", template: "{digest:short8}", img: noDigest, want: ""},
		{name: "{digest:short100} when N exceeds hex length returns empty", template: "{digest:short100}", img: hubNginx, want: ""},
		{name: "mirror prefix with repository:image", template: "dockerhub/{repository:image}", img: hubNginx, want: "dockerhub/nginx"},
		{name: "flat repository in a target template", template: "mirror-{repository:flat}", img: hubNginx, want: "mirror-library-nginx"},
		{name: "multiple placeholders in one template", template: "{registry}-{repository:image}-{tag}", img: hubNginx, want: "docker.io-nginx-1.21"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := RenderField(tt.template, tt.img)
			if err != nil {
				t.Fatalf("RenderField(%q, %+v): unexpected error: %v", tt.template, tt.img, err)
			}
			if got != tt.want {
				t.Errorf("RenderField(%q, %+v) = %q, want %q", tt.template, tt.img, got, tt.want)
			}
		})
	}
}

func TestRenderFieldUnknownPlaceholdersAreRejected(t *testing.T) {
	t.Parallel()

	cases := []string{
		"{repos}",
		"{repository:typo}",
		"{digest:tiny}",
		"{digest:short}",
		"{digest:short0}",
		"{digest:shortNaN}",
		"{registry:host}",
		"{tag:short}",
		"{unknown}",
	}

	for _, tmpl := range cases {
		t.Run(tmpl, func(t *testing.T) {
			t.Parallel()
			if _, err := RenderField(tmpl, hubNginx); err == nil {
				t.Errorf("RenderField(%q): expected error, got nil", tmpl)
			}
		})
	}
}

func TestRenderTagRejectsBareDigest(t *testing.T) {
	t.Parallel()

	if _, err := RenderTag("{digest}", hubNginx); err == nil {
		t.Errorf("RenderTag(%q): expected error, got nil", "{digest}")
	}
	if _, err := RenderTag("v{digest}-built", hubNginx); err == nil {
		t.Errorf("RenderTag(%q): expected error, got nil", "v{digest}-built")
	}
}

func TestRenderTagAllowsDigestSubforms(t *testing.T) {
	t.Parallel()

	cases := []struct {
		template string
		want     string
	}{
		{template: "{tag}", want: "1.21"},
		{template: "{digest:hex}", want: "1111111111111111111111111111111111111111111111111111111111111111"},
		{template: "{digest:short8}", want: "11111111"},
		{template: "{digest:algo}", want: "sha256"},
		{template: "v{tag}-{digest:short8}", want: "v1.21-11111111"},
	}

	for _, tt := range cases {
		t.Run(tt.template, func(t *testing.T) {
			t.Parallel()
			got, err := RenderTag(tt.template, hubNginx)
			if err != nil {
				t.Fatalf("RenderTag(%q): unexpected error: %v", tt.template, err)
			}
			if got != tt.want {
				t.Errorf("RenderTag(%q) = %q, want %q", tt.template, got, tt.want)
			}
		})
	}
}

func TestValidateFieldTemplateAcceptsKnown(t *testing.T) {
	t.Parallel()

	known := []string{
		"",
		"literal",
		"{registry}",
		"{repository}",
		"{repository:owner}",
		"{repository:image}",
		"{repository:path}",
		"{repository:flat}",
		"{tag}",
		"{digest}",
		"{digest:hex}",
		"{digest:short1}",
		"{digest:short64}",
		"{digest:algo}",
		"{registry}/{repository}:{tag}",
	}
	for _, tmpl := range known {
		if err := ValidateFieldTemplate(tmpl); err != nil {
			t.Errorf("ValidateFieldTemplate(%q): unexpected error: %v", tmpl, err)
		}
	}
}

func TestValidateFieldTemplateRejectsUnknown(t *testing.T) {
	t.Parallel()

	unknown := []string{
		"{repos}",
		"{repository:typo}",
		"{digest:tiny}",
		"{digest:short}",
		"{digest:short0}",
		"{digest:shortNaN}",
		"{registry:host}",
	}
	for _, tmpl := range unknown {
		if err := ValidateFieldTemplate(tmpl); err == nil {
			t.Errorf("ValidateFieldTemplate(%q): expected error, got nil", tmpl)
		}
	}
}

// TestValidateFieldTemplateRejectsMalformedShapes guards against the case
// where a `{...}` shape does not match the strict identifier grammar but
// would otherwise pass through as literal text, producing an admission-
// time rendering failure instead of a reconcile-time rejection.
func TestValidateFieldTemplateRejectsMalformedShapes(t *testing.T) {
	t.Parallel()

	malformed := []string{
		"{digest:}",
		"{repository:}",
		"{repository-name}",
		"{}",
		"{:hex}",
		"{digest: }",
		"{ registry}",
		"literal {digest:} text",
		"{digest:}{repository}",
	}
	for _, tmpl := range malformed {
		if err := ValidateFieldTemplate(tmpl); err == nil {
			t.Errorf("ValidateFieldTemplate(%q): expected error, got nil", tmpl)
		}
	}
}

// TestValidateTagTemplateRejectsMalformedShapes mirrors the field-template
// case so tag templates surface the same typos at reconcile time.
func TestValidateTagTemplateRejectsMalformedShapes(t *testing.T) {
	t.Parallel()

	malformed := []string{
		"{digest:}",
		"{repository-name}",
		"v{digest:}-built",
	}
	for _, tmpl := range malformed {
		if err := ValidateTagTemplate(tmpl); err == nil {
			t.Errorf("ValidateTagTemplate(%q): expected error, got nil", tmpl)
		}
	}
}

// TestValidateFieldTemplateRejectsUnmatchedBraces guards against the case
// where one half of a placeholder is missing: `mirror/{repository:image`
// or `mirror/registry}`. Without this check the malformed input would
// produce no regex matches at all and validation would silently accept it.
func TestValidateFieldTemplateRejectsUnmatchedBraces(t *testing.T) {
	t.Parallel()

	cases := []string{
		"{registry",
		"mirror/{repository:image",
		"registry}",
		"mirror/registry}",
		"{",
		"}",
		"}{",
		"{registry}}",
		"v{tag}/{",
	}
	for _, tmpl := range cases {
		if err := ValidateFieldTemplate(tmpl); err == nil {
			t.Errorf("ValidateFieldTemplate(%q): expected error, got nil", tmpl)
		}
	}
}

// TestValidateTagTemplateRejectsUnmatchedBraces mirrors the field check.
func TestValidateTagTemplateRejectsUnmatchedBraces(t *testing.T) {
	t.Parallel()

	cases := []string{
		"v{tag",
		"{digest:short8}}-built",
	}
	for _, tmpl := range cases {
		if err := ValidateTagTemplate(tmpl); err == nil {
			t.Errorf("ValidateTagTemplate(%q): expected error, got nil", tmpl)
		}
	}
}

// TestRenderFieldRejectsMalformedShapes is the defence-in-depth check: a
// template that somehow reached render time despite ValidateFieldTemplate
// still fails loudly rather than emitting literal braces into the output.
func TestRenderFieldRejectsMalformedShapes(t *testing.T) {
	t.Parallel()

	malformed := []string{
		"{digest:}",
		"{repository-name}",
		"{}",
		"mirror/{repository:image",
		"{registry}}",
	}
	for _, tmpl := range malformed {
		if _, err := RenderField(tmpl, hubNginx); err == nil {
			t.Errorf("RenderField(%q): expected error, got nil", tmpl)
		}
	}
}

func TestValidateTagTemplateRejectsBareDigest(t *testing.T) {
	t.Parallel()

	if err := ValidateTagTemplate("{digest}"); err == nil {
		t.Errorf("ValidateTagTemplate({digest}): expected error, got nil")
	}
	if err := ValidateTagTemplate("v{digest}-x"); err == nil {
		t.Errorf("ValidateTagTemplate(v{digest}-x): expected error, got nil")
	}
}

func TestValidateTagTemplateAcceptsDigestSubforms(t *testing.T) {
	t.Parallel()

	for _, tmpl := range []string{"{digest:hex}", "{digest:short8}", "{digest:algo}", "v{tag}-{digest:short8}"} {
		if err := ValidateTagTemplate(tmpl); err != nil {
			t.Errorf("ValidateTagTemplate(%q): unexpected error: %v", tmpl, err)
		}
	}
}
