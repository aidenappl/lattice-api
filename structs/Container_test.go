package structs

import "testing"

func TestContainerHasHealthCheck(t *testing.T) {
	empty, set := "", `{"test":["CMD","true"]}`
	tests := []struct {
		name string
		hc   *string
		want bool
	}{
		{"never set", nil, false},
		{"cleared", &empty, false},
		{"configured", &set, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := (Container{HealthCheck: tt.hc}).HasHealthCheck(); got != tt.want {
				t.Errorf("HasHealthCheck() = %v, want %v", got, tt.want)
			}
		})
	}
}
