package partitions

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 11, 8, 15, 30, 0, 0, time.UTC)

func day(s string) time.Time {
	d, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return d
}

func logsTable() Table    { return Tables[0] }
func metricsTable() Table { return Tables[1] }

func TestPartitionDDL(t *testing.T) {
	ddl := logsTable().PartitionDDL(now)
	for _, want := range []string{
		"ALTER TABLE container_logs MODIFY id BIGINT NOT NULL AUTO_INCREMENT, DROP PRIMARY KEY, ADD PRIMARY KEY (id, recorded_at) PARTITION BY RANGE COLUMNS(recorded_at)",
		"PARTITION p_old VALUES LESS THAN ('2026-11-01 00:00:00')",
		"PARTITION p20261101 VALUES LESS THAN ('2026-11-02 00:00:00')",
		"PARTITION p20261110 VALUES LESS THAN ('2026-11-11 00:00:00')",
		"PARTITION pmax VALUES LESS THAN (MAXVALUE))",
	} {
		if !strings.Contains(ddl, want) {
			t.Errorf("container_logs DDL missing %q:\n%s", want, ddl)
		}
	}
	// 7 kept days + today + 2 ahead, plus p_old and pmax.
	if n := strings.Count(ddl, "PARTITION p"); n != 12 {
		t.Errorf("container_logs DDL has %d partitions, want 12", n)
	}

	m := metricsTable().PartitionDDL(now)
	for _, want := range []string{
		"MODIFY recorded_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP",
		"PARTITION BY RANGE (UNIX_TIMESTAMP(recorded_at))",
		"PARTITION p_old VALUES LESS THAN (1793491200)", // 2026-11-01T00:00:00Z
		"PARTITION pmax VALUES LESS THAN MAXVALUE)",
	} {
		if !strings.Contains(m, want) {
			t.Errorf("container_metrics DDL missing %q:\n%s", want, m)
		}
	}
}

func TestPlanMaintenance(t *testing.T) {
	tbl := logsTable()
	tests := []struct {
		name  string
		parts []string
		now   time.Time
		want  Plan
	}{
		{
			name:  "fresh layout needs nothing but the p_old sweep",
			parts: []string{"p_old", "p20261101", "p20261102", "p20261103", "p20261104", "p20261105", "p20261106", "p20261107", "p20261108", "p20261109", "p20261110", "pmax"},
			now:   now,
			want:  Plan{TruncateOld: true},
		},
		{
			name:  "a day later: drop the expired day, add the new ahead day",
			parts: []string{"p_old", "p20261101", "p20261102", "p20261103", "p20261104", "p20261105", "p20261106", "p20261107", "p20261108", "p20261109", "p20261110", "pmax"},
			now:   now.AddDate(0, 0, 1),
			want:  Plan{Drop: []string{"p20261101"}, TruncateOld: true, Add: []time.Time{day("2026-11-11")}},
		},
		{
			name:  "down for days: drop the expired days, past days are not backfilled",
			parts: []string{"p_old", "p20261030", "p20261031", "p20261101", "p20261103", "pmax"},
			now:   now,
			want:  Plan{Drop: []string{"p20261030", "p20261031"}, TruncateOld: true, Add: []time.Time{day("2026-11-08"), day("2026-11-09"), day("2026-11-10")}},
		},
		{
			name:  "newest day long gone: start adding at today",
			parts: []string{"p_old", "p20261001", "pmax"},
			now:   now,
			want:  Plan{Drop: []string{"p20261001"}, TruncateOld: true, Add: []time.Time{day("2026-11-08"), day("2026-11-09"), day("2026-11-10")}},
		},
		{
			name:  "partitions it did not make are left alone",
			parts: []string{"p_old", "p_manual", "p20261108", "p20261109", "p20261110", "pmax"},
			now:   now,
			want:  Plan{TruncateOld: true},
		},
		{
			name:  "no p_old: nothing to truncate",
			parts: []string{"p20261108", "p20261109", "p20261110", "pmax"},
			now:   now,
			want:  Plan{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tbl.PlanMaintenance(tt.parts, tt.now)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("PlanMaintenance = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestStatements(t *testing.T) {
	tbl := logsTable()
	got := tbl.Statements(Plan{Drop: []string{"p20261031", "p20261101"}, TruncateOld: true, Add: []time.Time{day("2026-11-11")}})
	want := []string{
		"ALTER TABLE container_logs REORGANIZE PARTITION pmax INTO (PARTITION p20261111 VALUES LESS THAN ('2026-11-12 00:00:00'), PARTITION pmax VALUES LESS THAN (MAXVALUE))",
		"ALTER TABLE container_logs DROP PARTITION p20261031, p20261101",
		"ALTER TABLE container_logs TRUNCATE PARTITION p_old",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Statements =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if s := tbl.Statements(Plan{}); len(s) != 0 {
		t.Errorf("empty plan gave %v", s)
	}
}

// A plan applied and re-planned at the same instant is a fixed point, so
// running retention twice in an hour does no extra DDL.
func TestPlanIsIdempotent(t *testing.T) {
	tbl := logsTable()
	parts := []string{"p_old", "p20261030", "p20261108", "pmax"}
	p := tbl.PlanMaintenance(parts, now)
	var after []string
	dropped := map[string]bool{}
	for _, d := range p.Drop {
		dropped[d] = true
	}
	for _, n := range parts[:len(parts)-1] {
		if !dropped[n] {
			after = append(after, n)
		}
	}
	for _, d := range p.Add {
		after = append(after, dayName(d))
	}
	after = append(after, "pmax")
	again := tbl.PlanMaintenance(after, now)
	if len(again.Drop) != 0 || len(again.Add) != 0 {
		t.Errorf("second plan not empty: %+v (after %v)", again, after)
	}
}
