package imageref

import "testing"

func TestMatchMatches(t *testing.T) {
	t.Parallel()

	// Reusable input images.
	dockerHubNginx := Image{
		Registry:   "docker.io",
		Repository: "library/nginx",
		Tag:        "1.21",
	}
	gcrEtcd := Image{
		Registry:   "gcr.io",
		Repository: "google_containers/etcd",
		Tag:        "3.5",
	}
	pinnedByDigest := Image{
		Registry:   "docker.io",
		Repository: "library/nginx",
		Tag:        "",
		Digest:     testDigestSHA256,
	}

	tests := []struct {
		name  string
		match Match
		img   Image
		want  bool
	}{
		{
			name:  "empty match matches any image",
			match: Match{},
			img:   dockerHubNginx,
			want:  true,
		},
		{
			name:  "empty match matches digest-only image",
			match: Match{},
			img:   pinnedByDigest,
			want:  true,
		},
		{
			name:  "registry only narrows the match",
			match: Match{Registry: "docker.io"},
			img:   dockerHubNginx,
			want:  true,
		},
		{
			name:  "registry only rejects the wrong registry",
			match: Match{Registry: "docker.io"},
			img:   gcrEtcd,
			want:  false,
		},
		{
			name:  "registry glob with star matches any single-segment registry",
			match: Match{Registry: "*"},
			img:   gcrEtcd,
			want:  true,
		},
		{
			name:  "repository double-star covers multi-segment paths",
			match: Match{Registry: "docker.io", Repository: "library/**"},
			img:   dockerHubNginx,
			want:  true,
		},
		{
			name:  "repository single-star refuses multi-segment paths",
			match: Match{Registry: "gcr.io", Repository: "*"},
			img:   gcrEtcd,
			want:  false,
		},
		{
			name:  "tag glob matches an arbitrary tag",
			match: Match{Tag: "*"},
			img:   dockerHubNginx,
			want:  true,
		},
		{
			name:  "tag literal matches an exact tag",
			match: Match{Tag: "1.21"},
			img:   dockerHubNginx,
			want:  true,
		},
		{
			name:  "tag literal rejects the wrong tag",
			match: Match{Tag: "1.20"},
			img:   dockerHubNginx,
			want:  false,
		},
		{
			name:  "digest field matches when no digest is present (via star)",
			match: Match{Digest: "*"},
			img:   dockerHubNginx,
			want:  true,
		},
		{
			name:  "digest literal matches the same digest",
			match: Match{Digest: testDigestSHA256},
			img:   pinnedByDigest,
			want:  true,
		},
		{
			name:  "digest literal rejects an image with no digest",
			match: Match{Digest: testDigestSHA256},
			img:   dockerHubNginx,
			want:  false,
		},
		{
			name: "all four fields combine with AND",
			match: Match{
				Registry:   "docker.io",
				Repository: "library/**",
				Tag:        "1.*",
				Digest:     "*",
			},
			img:  dockerHubNginx,
			want: true,
		},
		{
			name: "all four fields reject when any one mismatches",
			match: Match{
				Registry:   "docker.io",
				Repository: "library/**",
				Tag:        "1.21",
				Digest:     "sha256:0000000000000000000000000000000000000000000000000000000000000000",
			},
			img:  dockerHubNginx,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tt.match.Matches(tt.img)
			if got != tt.want {
				t.Errorf("%+v.Matches(%+v) = %t, want %t", tt.match, tt.img, got, tt.want)
			}
		})
	}
}

func TestParseMatchString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		want    Match
		wantErr bool
	}{
		{
			name:  "registry, repository, tag",
			input: "docker.io/library/nginx:1.21",
			want:  Match{Registry: "docker.io", Repository: "library/nginx", Tag: "1.21"},
		},
		{
			name:  "registry and repository without tag or digest",
			input: "docker.io/library/nginx",
			want:  Match{Registry: "docker.io", Repository: "library/nginx"},
		},
		{
			name:  "digest only",
			input: "docker.io/library/nginx@" + testDigestSHA256,
			want:  Match{Registry: "docker.io", Repository: "library/nginx", Digest: testDigestSHA256},
		},
		{
			name:  "tag and digest",
			input: "docker.io/library/nginx:1.21@" + testDigestSHA256,
			want:  Match{Registry: "docker.io", Repository: "library/nginx", Tag: "1.21", Digest: testDigestSHA256},
		},
		{
			name:  "common dockerhub mirror glob",
			input: "docker.io/**:*",
			want:  Match{Registry: "docker.io", Repository: "**", Tag: "*"},
		},
		{
			name:  "fully wildcarded glob",
			input: "*/**:*@*",
			want:  Match{Registry: "*", Repository: "**", Tag: "*", Digest: "*"},
		},
		{
			name:  "registry with port preserved in registry field",
			input: "localhost:5000/foo",
			want:  Match{Registry: "localhost:5000", Repository: "foo"},
		},
		{
			name:  "registry with port plus tag",
			input: "localhost:5000/foo:bar",
			want:  Match{Registry: "localhost:5000", Repository: "foo", Tag: "bar"},
		},
		{
			name:  "multi-segment repository",
			input: "gcr.io/google_containers/etcd-io/etcd:3.5",
			want:  Match{Registry: "gcr.io", Repository: "google_containers/etcd-io/etcd", Tag: "3.5"},
		},
		{
			name:    "missing registry/repository separator rejected",
			input:   "nginx",
			wantErr: true,
		},
		{
			name:    "missing separator with tag still rejected",
			input:   "nginx:1.21",
			wantErr: true,
		},
		{
			name:    "empty input rejected",
			input:   "",
			wantErr: true,
		},
		{
			name:    "trailing-slash empty repository rejected",
			input:   "docker.io/",
			wantErr: true,
		},
		{
			name:    "trailing colon with empty tag rejected",
			input:   "docker.io/library/nginx:",
			wantErr: true,
		},
		{
			name:    "trailing at-sign with empty digest rejected",
			input:   "docker.io/library/nginx@",
			wantErr: true,
		},
		{
			name:    "empty tag combined with valid digest rejected",
			input:   "docker.io/library/nginx:@" + testDigestSHA256,
			wantErr: true,
		},
		{
			name:    "valid tag with trailing empty digest rejected",
			input:   "docker.io/library/nginx:1.21@",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseMatchString(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseMatchString(%q) = %+v, want error", tt.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseMatchString(%q): unexpected error: %v", tt.input, err)
			}
			if got != tt.want {
				t.Errorf("ParseMatchString(%q):\n got: %+v\nwant: %+v", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseMatchStringRoundTripsWithStructuredMatching(t *testing.T) {
	t.Parallel()

	img := Image{
		Registry:   "docker.io",
		Repository: "library/nginx",
		Tag:        "1.21",
	}

	tests := []struct {
		name string
		s    string
		want bool
	}{
		{name: "matches the input image", s: "docker.io/library/**:*", want: true},
		{name: "rejects a wrong registry", s: "gcr.io/library/**:*", want: false},
		{name: "rejects a wrong tag", s: "docker.io/library/**:1.20", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m, err := ParseMatchString(tt.s)
			if err != nil {
				t.Fatalf("ParseMatchString(%q): %v", tt.s, err)
			}
			if got := m.Matches(img); got != tt.want {
				t.Errorf("parsed %q matches %+v = %t, want %t", tt.s, img, got, tt.want)
			}
		})
	}
}
