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
