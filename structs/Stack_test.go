package structs

import "testing"

func TestDeploymentStrategyIsValid(t *testing.T) {
	tests := []struct {
		strategy DeploymentStrategy
		want     bool
	}{
		{"rolling", true},
		{"blue-green", true},
		{"canary", true},
		{"blue_green", false},
		{"recreate", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(string(tt.strategy), func(t *testing.T) {
			if got := tt.strategy.IsValid(); got != tt.want {
				t.Errorf("DeploymentStrategy(%q).IsValid() = %v, want %v", tt.strategy, got, tt.want)
			}
		})
	}
}

func TestValidImagePruneKeep(t *testing.T) {
	cases := []struct {
		keep int
		want bool
	}{{-2, false}, {-1, true}, {0, true}, {2, true}, {20, true}, {21, false}}
	for _, c := range cases {
		if got := ValidImagePruneKeep(c.keep); got != c.want {
			t.Errorf("ValidImagePruneKeep(%d) = %v, want %v", c.keep, got, c.want)
		}
	}
}

func TestImagePrunePayload(t *testing.T) {
	if p := ImagePrunePayload(IMAGE_PRUNE_DISABLED); p["enabled"] != false {
		t.Errorf("disabled payload = %v", p)
	}
	if p := ImagePrunePayload(3); p["enabled"] != true || p["keep"] != 3 {
		t.Errorf("keep 3 payload = %v", p)
	}
}
