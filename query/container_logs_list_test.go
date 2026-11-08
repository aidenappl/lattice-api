package query

import (
	"strings"
	"testing"
)

func TestListContainerLogsQuery(t *testing.T) {
	id, name, worker, stream := 7, "api", 3, "stderr"
	tests := []struct {
		name     string
		req      ListLogsRequest
		want     []string
		notWant  []string
		wantArgs []any
	}{
		{
			name: "id with name fallback is a union of two limited branches",
			req:  ListLogsRequest{ContainerID: &id, ContainerName: &name, Limit: 20, Offset: 40},
			want: []string{
				"SELECT * FROM ((SELECT ",
				"WHERE container_logs.container_id = ? ORDER BY container_logs.recorded_at DESC LIMIT 60) UNION ALL (SELECT ",
				"WHERE (container_logs.container_id IS NULL AND container_logs.container_name = ?) ORDER BY container_logs.recorded_at DESC LIMIT 60)) AS container_logs ORDER BY container_logs.recorded_at DESC LIMIT ? OFFSET ?",
			},
			notWant:  []string{" OR "},
			wantArgs: []any{7, "api", 20, 40},
		},
		{
			name:     "worker and stream filters apply to both branches",
			req:      ListLogsRequest{ContainerID: &id, ContainerName: &name, WorkerID: &worker, Stream: &stream},
			want:     []string{"container_logs.worker_id = ? AND container_logs.stream = ?"},
			notWant:  []string{"OFFSET"},
			wantArgs: []any{7, 3, "stderr", "api", 3, "stderr", 50},
		},
		{
			name:     "id only",
			req:      ListLogsRequest{ContainerID: &id, Limit: 10},
			want:     []string{"FROM container_logs WHERE container_logs.container_id = ? ORDER BY container_logs.recorded_at DESC LIMIT 10"},
			notWant:  []string{"UNION"},
			wantArgs: []any{7},
		},
		{
			name:     "name only",
			req:      ListLogsRequest{ContainerName: &name, Limit: 10, Offset: 5},
			want:     []string{"WHERE container_logs.container_name = ? ORDER BY container_logs.recorded_at DESC LIMIT 10 OFFSET 5"},
			notWant:  []string{"UNION"},
			wantArgs: []any{"api"},
		},
		{
			name:     "limit over max falls back to the default",
			req:      ListLogsRequest{Limit: 10000},
			want:     []string{"LIMIT 50"},
			wantArgs: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			qStr, args, err := listContainerLogsQuery(tt.req)
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range tt.want {
				if !strings.Contains(qStr, w) {
					t.Fatalf("query %q missing %q", qStr, w)
				}
			}
			for _, w := range tt.notWant {
				if strings.Contains(qStr, w) {
					t.Fatalf("query %q should not contain %q", qStr, w)
				}
			}
			if len(args) != len(tt.wantArgs) {
				t.Fatalf("args = %v, want %v", args, tt.wantArgs)
			}
			for i := range args {
				if args[i] != tt.wantArgs[i] {
					t.Fatalf("args = %v, want %v", args, tt.wantArgs)
				}
			}
		})
	}
}
