package versions

import (
	"strings"
	"testing"
)

func TestLatestSemverTag(t *testing.T) {
	tests := []struct {
		name string
		refs string
		want string
	}{
		{
			name: "numeric not lexical order",
			refs: "003e1111 refs/tags/v1.2.9\n003e2222 refs/tags/v1.2.10\n003e3333 refs/tags/v1.10.0\n",
			want: "v1.10.0",
		},
		{
			name: "peeled and non-semver tags ignored",
			refs: "0040aaaa refs/tags/v1.3.1^{}\n003ebbbb refs/tags/v1.3.0\n003ecccc refs/tags/nightly\n003edddd refs/tags/v2.0.0-rc1\n",
			want: "v1.3.0",
		},
		{
			name: "capabilities after NUL on the first ref line",
			refs: "001e# service=git-upload-pack\n0000015970a6 HEAD\x00multi_ack thin-pack\n003d70a6 refs/heads/main\n003eb919 refs/tags/v1.0.0\n",
			want: "v1.0.0",
		},
		{
			name: "branches are not tags",
			refs: "003d70a6 refs/heads/v9.9.9\n003eb919 refs/tags/v0.1.0\n",
			want: "v0.1.0",
		},
		{
			name: "leading zeros rejected",
			refs: "003eb919 refs/tags/v1.02.0\n003eb919 refs/tags/v1.1.0\n",
			want: "v1.1.0",
		},
		{"no tags", "003d70a6 refs/heads/main\n", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := latestSemverTag(strings.NewReader(tt.refs)); got != tt.want {
				t.Errorf("latestSemverTag() = %q, want %q", got, tt.want)
			}
		})
	}
}
