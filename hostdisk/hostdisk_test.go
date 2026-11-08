package hostdisk

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aidenappl/lattice-api/logger"
)

const gb = 1 << 30

func TestNewSampleLow(t *testing.T) {
	at := time.Now()
	tests := []struct {
		name        string
		total, free uint64
		want        bool
	}{
		{"plenty", 100 * gb, 40 * gb, false},
		{"under 10 percent", 100 * gb, 9 * gb, true},
		{"under 5 GB on a small disk", 40 * gb, 4 * gb, true},
		{"exactly 10 percent and 10 GB", 100 * gb, 10 * gb, false},
		{"full", 61 * gb, 0, true},
		{"empty total is not low", 0, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newSample("root", "/", tt.total, tt.free, at)
			if s.Low != tt.want {
				t.Errorf("Low = %v (free %.0f MB, %.1f%%), want %v", s.Low, s.FreeMB, s.FreePct, tt.want)
			}
		})
	}
}

func TestStatfsSample(t *testing.T) {
	s := statfsSample("root", "/", time.Now())
	if s.Error != "" || s.TotalMB <= 0 || s.FreeMB < 0 || s.FreeMB > s.TotalMB {
		t.Errorf("statfs(/) = %+v", s)
	}
	if bad := statfsSample("x", "/does/not/exist", time.Now()); bad.Error == "" {
		t.Error("missing path gave no error")
	}
}

func TestParseDF(t *testing.T) {
	out := "Filesystem     1024-blocks     Used Available Capacity Mounted on\n/dev/sda1         63278576 59134012   1012340      99% /var/lib/mysql\n"
	total, free, err := parseDF(out)
	if err != nil {
		t.Fatal(err)
	}
	if total != 63278576*1024 || free != 1012340*1024 {
		t.Errorf("parseDF = %d, %d", total, free)
	}
	for _, bad := range []string{"", "Filesystem 1024-blocks\n", "h\n/dev/x a b c d /m\n"} {
		if _, _, err := parseDF(bad); err == nil {
			t.Errorf("parseDF(%q) gave no error", bad)
		}
	}
}

func TestMariadbSample(t *testing.T) {
	df := "Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/sda1 1048576 1000000 48576 96% /var/lib/mysql\n"
	locate := func(context.Context) (string, string, error) { return "3f2a9c1b7d0e", "/var/lib/mysql/", nil }
	tests := []struct {
		name          string
		cfg           Config
		wantContainer string
		wantErr       bool
		wantLow       bool
	}{
		{"container from @@hostname", Config{Locate: locate}, "3f2a9c1b7d0e", false, true},
		{"configured container wins", Config{Locate: locate, DBContainer: "lattice-mariadb"}, "lattice-mariadb", false, true},
		{"datadir unreadable", Config{Locate: func(context.Context) (string, string, error) { return "", "", errors.New("denied") }}, "", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var ran []string
			tt.cfg.Run = func(_ context.Context, name string, args ...string) (string, error) {
				ran = append([]string{name}, args...)
				return df, nil
			}
			s := tt.cfg.mariadbSample(context.Background(), time.Now())
			if s == nil {
				t.Fatal("no sample")
			}
			if (s.Error != "") != tt.wantErr {
				t.Fatalf("sample = %+v", s)
			}
			if tt.wantErr {
				return
			}
			want := []string{"docker", "exec", tt.wantContainer, "df", "-Pk", "/var/lib/mysql/"}
			if len(ran) != len(want) {
				t.Fatalf("ran %v", ran)
			}
			for i := range want {
				if ran[i] != want[i] {
					t.Fatalf("ran %v, want %v", ran, want)
				}
			}
			if s.Low != tt.wantLow || s.TotalMB != 1024 {
				t.Errorf("sample = %+v", s)
			}
		})
	}
	// docker exec failing (the DB is not a container here) is an error sample.
	c := Config{Locate: locate, Run: func(context.Context, string, ...string) (string, error) { return "", errors.New("no such container") }}
	if s := c.mariadbSample(context.Background(), time.Now()); s == nil || s.Error == "" {
		t.Errorf("failed exec gave %+v", s)
	}
	// Without a DB or Locate, there is no mariadb sample at all.
	if s := (Config{}).mariadbSample(context.Background(), time.Now()); s != nil {
		t.Errorf("sample without a DB: %+v", s)
	}
}

func TestTrackerEvents(t *testing.T) {
	t0 := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	type ev struct {
		level logger.Level
		name  string
	}
	var events []ev
	tr := NewTracker(func(level logger.Level, event string, s Sample) { events = append(events, ev{level, event}) })
	low := func(at time.Time) []Sample { return []Sample{newSample("root", "/", 100*gb, 2*gb, at)} }
	ok := func(at time.Time) []Sample { return []Sample{newSample("root", "/", 100*gb, 50*gb, at)} }

	tr.Observe(ok(t0), t0)
	if len(events) != 0 {
		t.Fatalf("healthy disk raised %v", events)
	}
	tr.Observe(low(t0), t0)
	tr.Observe(low(t0.Add(time.Minute)), t0.Add(time.Minute))
	if len(events) != 1 || events[0] != (ev{logger.LevelWarn, EventLow}) {
		t.Fatalf("entering low: %v", events)
	}
	tr.Observe(low(t0.Add(RepeatEvery)), t0.Add(RepeatEvery))
	if len(events) != 2 || events[1].name != EventLow {
		t.Fatalf("no repeat after RepeatEvery: %v", events)
	}
	// An unreadable round changes nothing.
	tr.Observe([]Sample{{Label: "root", Error: "boom", At: t0}}, t0.Add(RepeatEvery+time.Minute))
	tr.Observe(ok(t0.Add(RepeatEvery+2*time.Minute)), t0.Add(RepeatEvery+2*time.Minute))
	if len(events) != 3 || events[2] != (ev{logger.LevelInfo, EventRecovered}) {
		t.Fatalf("recovery: %v", events)
	}
	tr.Observe(ok(t0.Add(time.Hour)), t0.Add(time.Hour))
	if len(events) != 3 {
		t.Fatalf("healthy repeat raised %v", events)
	}
}

func TestTrackerLatestDropsStale(t *testing.T) {
	t0 := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	tr := NewTracker(func(logger.Level, string, Sample) {})
	tr.Observe([]Sample{newSample("root", "/", 100*gb, 50*gb, t0)}, t0)
	if n := len(tr.Latest(t0.Add(time.Minute))); n != 1 {
		t.Errorf("fresh samples = %d, want 1", n)
	}
	if n := len(tr.Latest(t0.Add(Freshness + time.Second))); n != 0 {
		t.Errorf("stale samples = %d, want 0", n)
	}
}

func TestCollectReportsDFFailure(t *testing.T) {
	c := Config{ComposeDir: "/does/not/exist"}
	samples := c.Collect(context.Background(), time.Now())
	if len(samples) != 2 || samples[0].Label != "root" || samples[1].Label != "compose" || samples[1].Error == "" {
		t.Errorf("Collect = %+v", samples)
	}
}
