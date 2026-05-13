package imageref

import (
	"testing"
)

// sha256 64-hex-char placeholder used in tests; the exact bytes do not matter,
// only that pkg/name accepts the syntactic form.
const testDigestSHA256 = "sha256:1111111111111111111111111111111111111111111111111111111111111111"

func TestParse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		ref     string
		want    Image
		wantErr bool
	}{
		{
			name: "short form gets docker hub library namespace and latest tag",
			ref:  "nginx",
			want: Image{
				Registry:   "docker.io",
				Repository: "library/nginx",
				Tag:        "latest",
			},
		},
		{
			name: "explicit tag is preserved",
			ref:  "nginx:1.21",
			want: Image{
				Registry:   "docker.io",
				Repository: "library/nginx",
				Tag:        "1.21",
			},
		},
		{
			name: "explicit docker.io is normalised the same way",
			ref:  "docker.io/library/nginx:1.21",
			want: Image{
				Registry:   "docker.io",
				Repository: "library/nginx",
				Tag:        "1.21",
			},
		},
		{
			name: "digest-only reference leaves tag empty",
			ref:  "nginx@" + testDigestSHA256,
			want: Image{
				Registry:   "docker.io",
				Repository: "library/nginx",
				Tag:        "",
				Digest:     testDigestSHA256,
			},
		},
		{
			name: "reference with both tag and digest preserves both",
			ref:  "nginx:1.21@" + testDigestSHA256,
			want: Image{
				Registry:   "docker.io",
				Repository: "library/nginx",
				Tag:        "1.21",
				Digest:     testDigestSHA256,
			},
		},
		{
			name: "non-docker registry is preserved as-is",
			ref:  "gcr.io/google_containers/etcd:3.5",
			want: Image{
				Registry:   "gcr.io",
				Repository: "google_containers/etcd",
				Tag:        "3.5",
			},
		},
		{
			name: "registry with port and no tag defaults to latest",
			ref:  "localhost:5000/foo",
			want: Image{
				Registry:   "localhost:5000",
				Repository: "foo",
				Tag:        "latest",
			},
		},
		{
			name: "registry with port preserves an explicit tag",
			ref:  "localhost:5000/foo:bar",
			want: Image{
				Registry:   "localhost:5000",
				Repository: "foo",
				Tag:        "bar",
			},
		},
		{
			name: "multi-segment repository is preserved",
			ref:  "mirror.local/squirrel/policies/nginx:test",
			want: Image{
				Registry:   "mirror.local",
				Repository: "squirrel/policies/nginx",
				Tag:        "test",
			},
		},
		{
			name: "index.docker.io is canonicalised to docker.io",
			ref:  "index.docker.io/library/nginx",
			want: Image{
				Registry:   "docker.io",
				Repository: "library/nginx",
				Tag:        "latest",
			},
		},
		{
			name:    "empty reference is rejected",
			ref:     "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := Parse(tt.ref)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Parse(%q) = %+v, want error", tt.ref, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse(%q): unexpected error: %v", tt.ref, err)
			}
			if got != tt.want {
				t.Errorf("Parse(%q):\n got: %+v\nwant: %+v", tt.ref, got, tt.want)
			}
		})
	}
}
