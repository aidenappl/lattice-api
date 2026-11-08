package query

import (
	"strings"
	"testing"

	sq "github.com/Masterminds/squirrel"
)

func TestLogScopeSQL(t *testing.T) {
	id, name, worker := 843, "nexus-api", 4
	tests := []struct {
		name     string
		scope    logScope
		want     []string
		notWant  []string
		wantArgs int
	}{
		{
			name:  "id and name: union of two ordered limited branches",
			scope: logScope{containerID: &id, containerName: &name, limit: 500},
			want: []string{
				"(SELECT container_logs.id",
				"WHERE container_logs.container_id = ? ORDER BY container_logs.recorded_at DESC LIMIT 500) UNION ALL (",
				"WHERE (container_logs.container_id IS NULL AND container_logs.container_name = ?) ORDER BY container_logs.recorded_at DESC LIMIT 500)",
				") ORDER BY recorded_at DESC LIMIT 500",
			},
			notWant:  []string{" OR ", "OFFSET"},
			wantArgs: 2,
		},
		{
			name:  "offset widens each branch window",
			scope: logScope{containerID: &id, containerName: &name, limit: 50, offset: 100},
			want: []string{
				"DESC LIMIT 150) UNION ALL (",
				"DESC LIMIT 150) ORDER BY recorded_at DESC LIMIT 50 OFFSET 100",
			},
			wantArgs: 2,
		},
		{
			name:  "filters apply in both branches",
			scope: logScope{containerID: &id, containerName: &name, limit: 10, filters: sq.And{sq.Eq{"container_logs.worker_id": worker}}},
			want: []string{
				"container_logs.container_id = ? AND (container_logs.worker_id = ?)",
				"container_logs.container_name = ?) AND (container_logs.worker_id = ?)",
			},
			wantArgs: 4,
		},
		{
			name:     "id only",
			scope:    logScope{containerID: &id, limit: 10, offset: 20},
			want:     []string{"WHERE container_logs.container_id = ? ORDER BY container_logs.recorded_at DESC LIMIT 10 OFFSET 20"},
			notWant:  []string{"UNION"},
			wantArgs: 1,
		},
		{
			name:     "name only",
			scope:    logScope{containerName: &name, limit: 10},
			want:     []string{"WHERE container_logs.container_name = ? ORDER BY"},
			notWant:  []string{"UNION", "IS NULL"},
			wantArgs: 1,
		},
		{
			name:     "no scope",
			scope:    logScope{limit: 10},
			want:     []string{"WHERE 1 = 1 ORDER BY container_logs.recorded_at DESC LIMIT 10"},
			wantArgs: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.scope.table = "container_logs"
			tt.scope.columns = logColumns
			q, args, err := tt.scope.ToSql()
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range tt.want {
				if !strings.Contains(q, w) {
					t.Errorf("query missing %q\n%s", w, q)
				}
			}
			for _, w := range tt.notWant {
				if strings.Contains(q, w) {
					t.Errorf("query has %q\n%s", w, q)
				}
			}
			if len(args) != tt.wantArgs {
				t.Errorf("%d args, want %d", len(args), tt.wantArgs)
			}
		})
	}
}
