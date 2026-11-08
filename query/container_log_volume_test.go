package query

import (
	"strings"
	"testing"
	"time"
)

func TestContainerLogVolumeQuery(t *testing.T) {
	now := time.Date(2026, 11, 8, 15, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		req       ContainerLogVolumeRequest
		wantSince string
		wantLimit string
	}{
		{"default is today", ContainerLogVolumeRequest{Now: now}, "2026-11-08 00:00:00", "LIMIT 50"},
		{"three days", ContainerLogVolumeRequest{Days: 3, Limit: 10, Now: now}, "2026-11-06 00:00:00", "LIMIT 10"},
		{"over the max falls back", ContainerLogVolumeRequest{Days: 30, Limit: 100000, Now: now}, "2026-11-08 00:00:00", "LIMIT 50"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q, args, err := containerLogVolumeQuery(tt.req)
			if err != nil {
				t.Fatal(err)
			}
			if len(args) != 1 || args[0] != tt.wantSince {
				t.Errorf("args = %v, want since %s", args, tt.wantSince)
			}
			for _, want := range []string{"container_logs.recorded_at >= ?", "GROUP BY day", "ORDER BY byte_count DESC", tt.wantLimit} {
				if !strings.Contains(q, want) {
					t.Errorf("query missing %q: %s", want, q)
				}
			}
		})
	}
}
