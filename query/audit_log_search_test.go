package query

import "testing"

func TestLikeEscape(t *testing.T) {
	tests := map[string]string{
		"deploy":  "deploy",
		"100%":    `100\%`,
		"a_b":     `a\_b`,
		`back\sl`: `back\\sl`,
	}
	for in, want := range tests {
		if got := likeEscape(in); got != want {
			t.Errorf("likeEscape(%q) = %q, want %q", in, got, want)
		}
	}
}
