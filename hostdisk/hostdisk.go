// Package hostdisk samples free space on the control-plane host itself
// (APL-121). Workers report their disk in heartbeats (APL-86's low_disk); the
// host running lattice-api, lattice-web and MariaDB had nothing, and on
// 2026-11-08 it filled to 100% with no anomaly and no alert.
//
// What is sampled, every minute:
//
//   - "root": statfs("/") inside the lattice-api container. The container's
//     root is an overlay whose statfs reports the host filesystem behind
//     /var/lib/docker, which also holds named volumes (MariaDB's by default).
//   - "compose": statfs(DOCKER_COMPOSE_DIR), the host's /opt/lattice mounted
//     read-write for self-update, when set: the host root filesystem.
//   - "mariadb": `df -Pk @@datadir` run in the MariaDB container through the
//     Docker socket lattice-api already mounts. MariaDB names its own container
//     (@@hostname is the container ID) and data dir, so it needs no
//     configuration; it is skipped when the database isn't a container on this
//     Docker host (CONTROL_PLANE_DB_CONTAINER overrides the container).
//
// A path below LowFreePct or LowFreeMB free is low: the anomaly
// control_plane_low_disk (healthscan) and the Monitor event
// control_plane.low_disk (warn, repeated every RepeatEvery while low; info
// control_plane.disk_recovered when it clears).
package hostdisk

import (
	"context"
	"database/sql"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/aidenappl/lattice-api/logger"
)

// Thresholds are the worker low_disk ones (healthscan.LOW_DISK_FREE_PCT /
// LOW_DISK_FREE_MB, lattice-web src/lib/disk.ts): change them together.
const (
	LowFreePct = 10.0
	LowFreeMB  = 5 * 1024.0
)

const (
	// Interval is how often the host is sampled.
	Interval = time.Minute
	// Freshness bounds how old a sample may be and still count.
	Freshness = 5 * time.Minute
	// RepeatEvery is how often control_plane.low_disk repeats while low, so an
	// alert rule on a window keeps seeing it.
	RepeatEvery = 30 * time.Minute

	EventLow       = "control_plane.low_disk"
	EventRecovered = "control_plane.disk_recovered"
)

// Sample is one path's free space.
type Sample struct {
	Label   string    `json:"label"` // root, compose, mariadb
	Path    string    `json:"path"`
	TotalMB float64   `json:"total_mb"`
	FreeMB  float64   `json:"free_mb"`
	FreePct float64   `json:"free_pct"`
	Low     bool      `json:"low"`
	At      time.Time `json:"sampled_at"`
	Error   string    `json:"error,omitempty"` // set when this path could not be read
}

func newSample(label, path string, totalBytes, freeBytes uint64, at time.Time) Sample {
	s := Sample{Label: label, Path: path, At: at,
		TotalMB: float64(totalBytes) / (1 << 20), FreeMB: float64(freeBytes) / (1 << 20)}
	if s.TotalMB > 0 {
		s.FreePct = s.FreeMB / s.TotalMB * 100
	}
	s.Low = s.TotalMB > 0 && (s.FreePct < LowFreePct || s.FreeMB < LowFreeMB)
	return s
}

// statfsSample reads a mounted path. Free is what an unprivileged writer can
// use (Bavail): MariaDB doesn't run as root, and the root reserve is not free
// to it.
func statfsSample(label, path string, at time.Time) Sample {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return Sample{Label: label, Path: path, At: at, Error: err.Error()}
	}
	bsize := uint64(st.Bsize)
	return newSample(label, path, uint64(st.Blocks)*bsize, uint64(st.Bavail)*bsize, at)
}

// parseDF reads `df -Pk <path>` output: a header, then
// "<fs> <1K-blocks> <used> <available> <capacity> <mount>".
func parseDF(out string) (totalBytes, freeBytes uint64, err error) {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		return 0, 0, fmt.Errorf("unexpected df output %q", out)
	}
	f := strings.Fields(lines[len(lines)-1])
	if len(f) < 6 {
		return 0, 0, fmt.Errorf("unexpected df line %q", lines[len(lines)-1])
	}
	total, err1 := strconv.ParseUint(f[1], 10, 64)
	avail, err2 := strconv.ParseUint(f[3], 10, 64)
	if err1 != nil || err2 != nil {
		return 0, 0, fmt.Errorf("unexpected df numbers in %q", lines[len(lines)-1])
	}
	return total * 1024, avail * 1024, nil
}

// Config is what the sampler reads.
type Config struct {
	ComposeDir  string  // DOCKER_COMPOSE_DIR; "" skips the compose sample
	DBContainer string  // CONTROL_PLANE_DB_CONTAINER; "" asks MariaDB
	DB          *sql.DB // for @@hostname / @@datadir; nil skips the mariadb sample
	// Run executes a command and returns stdout; nil uses exec with a timeout.
	Run func(ctx context.Context, name string, args ...string) (string, error)
	// Locate returns MariaDB's @@hostname and @@datadir; nil queries DB.
	Locate func(ctx context.Context) (host, datadir string, err error)
}

func defaultRun(ctx context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	return string(out), err
}

// mariadbSample runs df for MariaDB's data dir inside its container.
func (c Config) mariadbSample(ctx context.Context, at time.Time) *Sample {
	locate := c.Locate
	if locate == nil {
		if c.DB == nil {
			return nil
		}
		locate = func(ctx context.Context) (host, datadir string, err error) {
			err = c.DB.QueryRowContext(ctx, "SELECT @@hostname, @@datadir").Scan(&host, &datadir)
			return
		}
	}
	host, datadir, err := locate(ctx)
	if err != nil {
		return &Sample{Label: "mariadb", At: at, Error: "reading @@datadir: " + err.Error()}
	}
	container := c.DBContainer
	if container == "" {
		container = host
	}
	run := c.Run
	if run == nil {
		run = defaultRun
	}
	out, err := run(ctx, "docker", "exec", container, "df", "-Pk", datadir)
	if err != nil {
		return &Sample{Label: "mariadb", Path: datadir, At: at, Error: fmt.Sprintf("df in container %s: %v", container, err)}
	}
	total, free, err := parseDF(out)
	if err != nil {
		return &Sample{Label: "mariadb", Path: datadir, At: at, Error: err.Error()}
	}
	s := newSample("mariadb", datadir, total, free, at)
	return &s
}

// Collect samples every configured path once.
func (c Config) Collect(ctx context.Context, now time.Time) []Sample {
	out := []Sample{statfsSample("root", "/", now)}
	if c.ComposeDir != "" {
		out = append(out, statfsSample("compose", c.ComposeDir, now))
	}
	if s := c.mariadbSample(ctx, now); s != nil {
		out = append(out, *s)
	}
	return out
}

// ── State and events ─────────────────────────────────────────────────────

type alertState struct {
	low       bool
	lastEvent time.Time
}

// Tracker holds the latest samples and decides when to raise events.
type Tracker struct {
	mu      sync.RWMutex
	latest  []Sample
	alerts  map[string]*alertState
	emitter func(level logger.Level, event string, s Sample)
}

// NewTracker returns a Tracker that reports events through emit (nil: the logger).
func NewTracker(emit func(level logger.Level, event string, s Sample)) *Tracker {
	if emit == nil {
		emit = logEvent
	}
	return &Tracker{alerts: map[string]*alertState{}, emitter: emit}
}

func logEvent(level logger.Level, event string, s Sample) {
	msg := "control-plane disk nearly full"
	if event == EventRecovered {
		msg = "control-plane disk recovered"
	}
	logger.EventCtx(context.Background(), level, event, "hostdisk", msg, logger.F{
		"label": s.Label, "path": s.Path, "disk_free_mb": s.FreeMB, "disk_total_mb": s.TotalMB,
		"disk_free_pct": s.FreePct, "threshold_free_pct": LowFreePct, "threshold_free_mb": LowFreeMB,
	})
}

// Observe records a round of samples and raises events: low on entering the
// low state and every RepeatEvery while it lasts, recovered on leaving it. An
// unreadable path changes nothing.
func (t *Tracker) Observe(samples []Sample, now time.Time) {
	t.mu.Lock()
	t.latest = samples
	type ev struct {
		level logger.Level
		name  string
		s     Sample
	}
	var events []ev
	for _, s := range samples {
		if s.Error != "" {
			continue
		}
		st := t.alerts[s.Label]
		if st == nil {
			st = &alertState{}
			t.alerts[s.Label] = st
		}
		switch {
		case s.Low && (!st.low || now.Sub(st.lastEvent) >= RepeatEvery):
			events = append(events, ev{logger.LevelWarn, EventLow, s})
			st.low, st.lastEvent = true, now
		case !s.Low && st.low:
			events = append(events, ev{logger.LevelInfo, EventRecovered, s})
			st.low, st.lastEvent = false, now
		}
	}
	t.mu.Unlock()
	for _, e := range events {
		t.emitter(e.level, e.name, e.s)
	}
}

// Latest returns the most recent samples that are still fresh.
func (t *Tracker) Latest(now time.Time) []Sample {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := []Sample{}
	for _, s := range t.latest {
		if now.Sub(s.At) <= Freshness {
			out = append(out, s)
		}
	}
	return out
}

// Default is the process-wide tracker the sampler feeds.
var Default = NewTracker(nil)

// Start samples now and then every Interval into Default.
func Start(cfg Config) {
	go func() {
		ticker := time.NewTicker(Interval)
		defer ticker.Stop()
		for {
			sampleOnce(cfg)
			<-ticker.C
		}
	}()
}

// sampleOnce is one round that cannot end the loop: a panic costs that round.
func sampleOnce(cfg Config) {
	defer logger.Recover("hostdisk")
	now := time.Now()
	Default.Observe(cfg.Collect(context.Background(), now), now)
}
