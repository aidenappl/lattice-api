package query

import "testing"

func TestWindowClause(t *testing.T) {
	tests := []struct {
		name      string
		windowSec int
		want      string
	}{
		{"one hour", 3600, "recorded_at >= NOW() - INTERVAL 3600 SECOND"},
		{"non-positive is clamped to one second", 0, "recorded_at >= NOW() - INTERVAL 1 SECOND"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := windowClause(tt.windowSec); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBucketSeconds(t *testing.T) {
	tests := []struct {
		name                      string
		windowSec, points, minSec int
		want                      int
	}{
		{"seven days in 300 buckets", 7 * 24 * 3600, 300, 10, 2016},
		{"one hour in 300 buckets hits the floor", 3600, 300, 60, 60},
		{"one hour at worker resolution", 3600, 300, 10, 12},
		{"zero points is treated as one", 600, 0, 10, 600},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := bucketSeconds(tt.windowSec, tt.points, tt.minSec); got != tt.want {
				t.Fatalf("got %d, want %d", got, tt.want)
			}
		})
	}
}
