package mailer

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/logger"
	"github.com/aidenappl/lattice-api/query"
)

// EventConfig holds the full notification preferences for a single event type.
type EventConfig struct {
	Enabled bool `json:"enabled"`
	// GraceSeconds: for worker.disconnected — wait this long before alerting, cancel if worker reconnects.
	GraceSeconds int `json:"grace_seconds,omitempty"`
	// Threshold: for container.unhealthy — how many consecutive unhealthy reports before alerting.
	Threshold int `json:"threshold,omitempty"`
	// CooldownMinutes: suppress duplicate alerts for the same resource within this window.
	CooldownMinutes int `json:"cooldown_minutes,omitempty"`
}

// EventPreferences is the full map of event type -> config.
type EventPreferences map[string]EventConfig

// DefaultPrefs returns the default configuration for all event types.
func DefaultPrefs() EventPreferences {
	return EventPreferences{
		"worker.disconnected":  {Enabled: true, GraceSeconds: 30, CooldownMinutes: 5},
		"worker.crash":         {Enabled: true, CooldownMinutes: 5},
		"container.unhealthy":  {Enabled: true, Threshold: 3, CooldownMinutes: 10},
		"deployment.triggered": {Enabled: true, CooldownMinutes: 0},
		"deployment.failed":    {Enabled: true, CooldownMinutes: 0},
		"deployment.success":   {Enabled: true, CooldownMinutes: 0},
	}
}

// LoadPreferences loads all notification preferences from the DB, merging with defaults.
func LoadPreferences() EventPreferences {
	prefs := DefaultPrefs()
	raw, err := query.GetSetting(db.DB, "notification_prefs")
	if err != nil || raw == "" {
		return prefs
	}
	var stored EventPreferences
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		logger.Error("mailer", "failed to parse notification prefs", logger.F{"error": err})
		return prefs
	}
	// Merge stored values over defaults
	for key, cfg := range stored {
		if _, exists := prefs[key]; exists {
			prefs[key] = cfg
		}
	}
	return prefs
}

// SavePreferences persists the full preference map to the DB.
func SavePreferences(prefs EventPreferences) error {
	b, err := json.Marshal(prefs)
	if err != nil {
		return err
	}
	return query.SetSetting(db.DB, "notification_prefs", string(b))
}

// GetEventConfig returns the config for a specific event type.
func GetEventConfig(eventType string) EventConfig {
	prefs := LoadPreferences()
	if cfg, ok := prefs[eventType]; ok {
		return cfg
	}
	return EventConfig{Enabled: true}
}

// ─── Cooldown tracker ──────────────────────────────────────────────────────

var (
	cooldownMu  sync.Mutex
	cooldownMap = make(map[string]time.Time) // "eventType:resourceKey" -> cooldown expiry time
)

// ShouldAlert checks if an alert should fire based on enabled status, cooldown, and threshold.
// resourceKey is a unique identifier for the resource (e.g., worker ID, container name).
func ShouldAlert(eventType, resourceKey string) bool {
	cfg := GetEventConfig(eventType)
	if !cfg.Enabled {
		return false
	}

	// Check cooldown. The map stores the expiry time (last alert + cooldown) so a
	// background sweep can safely prune any entry that is already in the past.
	if cfg.CooldownMinutes > 0 {
		cooldownMu.Lock()
		defer cooldownMu.Unlock()
		key := eventType + ":" + resourceKey
		if exp, ok := cooldownMap[key]; ok && time.Now().Before(exp) {
			return false // still in cooldown
		}
		cooldownMap[key] = time.Now().Add(time.Duration(cfg.CooldownMinutes) * time.Minute)
	}

	return true
}

// ─── Unhealthy counter ─────────────────────────────────────────────────────

type unhealthyEntry struct {
	count     int
	updatedAt time.Time
}

var (
	unhealthyMu     sync.Mutex
	unhealthyCounts = make(map[string]unhealthyEntry) // containerName -> consecutive unhealthy count
)

// TrackUnhealthy increments the unhealthy counter for a container and returns
// true if the threshold has been reached (i.e., should alert).
func TrackUnhealthy(containerName string) bool {
	cfg := GetEventConfig("container.unhealthy")
	threshold := cfg.Threshold
	if threshold <= 0 {
		threshold = 1
	}

	unhealthyMu.Lock()
	defer unhealthyMu.Unlock()
	e := unhealthyCounts[containerName]
	e.count++
	e.updatedAt = time.Now()
	unhealthyCounts[containerName] = e
	return e.count >= threshold
}

// ClearUnhealthy resets the unhealthy counter when a container becomes healthy.
func ClearUnhealthy(containerName string) {
	unhealthyMu.Lock()
	defer unhealthyMu.Unlock()
	delete(unhealthyCounts, containerName)
}

// StartEviction launches a background goroutine that periodically prunes expired
// cooldown entries and stale unhealthy counters so these maps can't grow without
// bound as containers and workers come and go.
func StartEviction() {
	go func() {
		ticker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			now := time.Now()

			cooldownMu.Lock()
			for k, exp := range cooldownMap {
				if now.After(exp) {
					delete(cooldownMap, k)
				}
			}
			cooldownMu.Unlock()

			unhealthyMu.Lock()
			for k, e := range unhealthyCounts {
				if now.Sub(e.updatedAt) > time.Hour {
					delete(unhealthyCounts, k)
				}
			}
			unhealthyMu.Unlock()
		}
	}()
}

// ─── Delayed disconnect alerts ─────────────────────────────────────────────

var (
	graceMu     sync.Mutex
	graceTimers = make(map[int]*time.Timer) // workerID -> pending alert timer
)

// ScheduleDisconnectAlert sets up a delayed alert for a worker going offline.
// If the worker reconnects before the grace period, call CancelDisconnectAlert.
func ScheduleDisconnectAlert(workerID int, alertFn func()) {
	cfg := GetEventConfig("worker.disconnected")
	if !cfg.Enabled {
		return
	}

	grace := time.Duration(cfg.GraceSeconds) * time.Second
	if grace <= 0 {
		// No grace period — alert immediately
		if ShouldAlert("worker.disconnected", intToStr(workerID)) {
			go alertFn()
		}
		return
	}

	graceMu.Lock()
	defer graceMu.Unlock()

	// Cancel any existing timer for this worker
	if t, ok := graceTimers[workerID]; ok {
		t.Stop()
		delete(graceTimers, workerID)
	}

	graceTimers[workerID] = time.AfterFunc(grace, func() {
		graceMu.Lock()
		delete(graceTimers, workerID)
		graceMu.Unlock()

		if ShouldAlert("worker.disconnected", intToStr(workerID)) {
			alertFn()
		}
	})
}

// CancelDisconnectAlert cancels a pending disconnect alert (worker reconnected).
func CancelDisconnectAlert(workerID int) {
	graceMu.Lock()
	defer graceMu.Unlock()
	if t, ok := graceTimers[workerID]; ok {
		t.Stop()
		delete(graceTimers, workerID)
		logger.Info("mailer", "disconnect alert cancelled (worker reconnected)", logger.F{"worker_id": workerID})
	}
}

func intToStr(i int) string {
	return fmt.Sprintf("%d", i)
}
