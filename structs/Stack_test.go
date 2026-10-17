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
