package healthscan

import (
	"database/sql"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aidenappl/lattice-api/logger"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/socket"
)

// AnomalyType categorizes what kind of issue was detected.
type AnomalyType string

const (
	AnomalyOrphaned   AnomalyType = "orphaned_container"   // container on worker not tracked in DB
	AnomalyMissing    AnomalyType = "missing_container"     // container in DB but not running on worker
	AnomalyMismatch   AnomalyType = "status_mismatch"       // DB says running but worker says stopped (or vice versa)
	AnomalyUnmanaged  AnomalyType = "unmanaged_container"   // container on worker not in any stack
	AnomalyStaleState AnomalyType = "stale_state"           // DB shows running but worker hasn't reported in >2min
)

// Anomaly represents a single detected issue.
type Anomaly struct {
	ID            string      `json:"id"`              // unique key for dedup
	Type          AnomalyType `json:"type"`
	WorkerID      int         `json:"worker_id"`
	WorkerName    string      `json:"worker_name"`
	ContainerName string      `json:"container_name,omitempty"`
	Message       string      `json:"message"`
	DetectedAt    time.Time   `json:"detected_at"`
	Details       map[string]any `json:"details,omitempty"`
}

// Scanner periodically audits worker/container state and reports anomalies.
type Scanner struct {
	db       *sql.DB
	adminHub *socket.AdminHub
	workerHub *socket.WorkerHub

	mu        sync.RWMutex
	anomalies []Anomaly

	// latestContainerState tracks the last container_sync snapshot per worker.
	// Updated from heartbeat container_stats payloads.
	containerStateMu sync.RWMutex
	containerState   map[int]workerContainerState // workerID -> state
}

type workerContainerState struct {
	Containers []string  // container names reported by worker
	UpdatedAt  time.Time
}

func New(db *sql.DB, adminHub *socket.AdminHub, workerHub *socket.WorkerHub) *Scanner {
	return &Scanner{
		db:             db,
		adminHub:       adminHub,
		workerHub:      workerHub,
		containerState: make(map[int]workerContainerState),
	}
}

// Start launches the periodic scan goroutine.
func (s *Scanner) Start() {
	go func() {
		// Wait for workers to connect and report state
		time.Sleep(2 * time.Minute)
		s.scan()

		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			s.scan()
		}
	}()
}

// UpdateWorkerContainers is called from the heartbeat handler to keep the scanner
// aware of what containers each worker currently has.
func (s *Scanner) UpdateWorkerContainers(workerID int, containerNames []string) {
	s.containerStateMu.Lock()
	defer s.containerStateMu.Unlock()
	s.containerState[workerID] = workerContainerState{
		Containers: containerNames,
		UpdatedAt:  time.Now(),
	}
}

// GetAnomalies returns the current list of active anomalies.
func (s *Scanner) GetAnomalies() []Anomaly {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]Anomaly, len(s.anomalies))
	copy(result, s.anomalies)
	return result
}

func (s *Scanner) scan() {
	logger.Info("healthscan", "starting worker health scan")

	var anomalies []Anomaly

	// Get all workers
	workers, err := query.ListWorkers(s.db, query.ListWorkersRequest{})
	if err != nil {
		logger.Error("healthscan", "failed to list workers", logger.F{"error": err})
		return
	}
	if workers == nil {
		return
	}

	for _, w := range *workers {
		// Only scan online workers that have reported container state
		if !s.workerHub.IsConnected(w.ID) {
			// Check for stale state: DB says containers are running but worker is offline
			anomalies = append(anomalies, s.checkStaleWorker(w.ID, w.Name)...)
			continue
		}

		s.containerStateMu.RLock()
		state, hasState := s.containerState[w.ID]
		s.containerStateMu.RUnlock()

		if !hasState || time.Since(state.UpdatedAt) > 2*time.Minute {
			continue // no fresh data to compare
		}

		// Get expected containers for this worker from DB
		expected, err := query.ListAllContainers(s.db, query.ListAllContainersRequest{
			WorkerID: &w.ID,
		})
		if err != nil {
			logger.Error("healthscan", "failed to list containers for worker", logger.F{"worker_id": w.ID, "error": err})
			continue
		}

		expectedNames := make(map[string]string) // name -> expected status
		if expected != nil {
			for _, c := range *expected {
				expectedNames[c.Name] = c.Status
			}
		}

		workerNames := make(map[string]bool)
		for _, name := range state.Containers {
			workerNames[name] = true
		}

		// Check for orphaned/unmanaged containers on worker
		for _, name := range state.Containers {
			if _, tracked := expectedNames[name]; !tracked {
				aType := AnomalyUnmanaged
				msg := "Container running on worker but not tracked in any stack"
				if strings.Contains(name, "-retired-") ||
					strings.HasSuffix(name, "-lattice-retired") ||
					strings.HasSuffix(name, "-lattice-updating") {
					aType = AnomalyOrphaned
					msg = "Orphaned container from a failed deployment"
				}
				anomalies = append(anomalies, Anomaly{
					ID:            anomalyID(w.ID, string(aType), name),
					Type:          aType,
					WorkerID:      w.ID,
					WorkerName:    w.Name,
					ContainerName: name,
					Message:       msg,
					DetectedAt:    time.Now(),
				})
			}
		}

		// Check for missing containers (in DB but not on worker)
		for name, status := range expectedNames {
			if status == "running" && !workerNames[name] {
				anomalies = append(anomalies, Anomaly{
					ID:            anomalyID(w.ID, "missing_container", name),
					Type:          AnomalyMissing,
					WorkerID:      w.ID,
					WorkerName:    w.Name,
					ContainerName: name,
					Message:       "Container expected to be running but not found on worker",
					DetectedAt:    time.Now(),
					Details:       map[string]any{"expected_status": status},
				})
			}
		}
	}

	// Update stored anomalies and broadcast if changed
	s.mu.Lock()
	changed := len(anomalies) != len(s.anomalies)
	if !changed {
		// Quick check: compare IDs
		oldIDs := make(map[string]bool, len(s.anomalies))
		for _, a := range s.anomalies {
			oldIDs[a.ID] = true
		}
		for _, a := range anomalies {
			if !oldIDs[a.ID] {
				changed = true
				break
			}
		}
	}
	s.anomalies = anomalies
	s.mu.Unlock()

	if changed {
		s.broadcast(anomalies)
	}

	if len(anomalies) > 0 {
		logger.Info("healthscan", "scan complete", logger.F{"anomalies": len(anomalies)})
	} else {
		logger.Info("healthscan", "scan complete, all workers healthy")
	}
}

func (s *Scanner) checkStaleWorker(workerID int, workerName string) []Anomaly {
	containers, err := query.ListAllContainers(s.db, query.ListAllContainersRequest{
		WorkerID: &workerID,
	})
	if err != nil || containers == nil {
		return nil
	}

	var anomalies []Anomaly
	for _, c := range *containers {
		if c.Status == "running" {
			anomalies = append(anomalies, Anomaly{
				ID:            anomalyID(workerID, "stale_state", c.Name),
				Type:          AnomalyStaleState,
				WorkerID:      workerID,
				WorkerName:    workerName,
				ContainerName: c.Name,
				Message:       "Container shows as running but worker is offline",
				DetectedAt:    time.Now(),
			})
		}
	}
	return anomalies
}

func (s *Scanner) broadcast(anomalies []Anomaly) {
	s.adminHub.BroadcastJSON(map[string]any{
		"type":    "health_anomalies",
		"payload": anomalies,
	})
}

func anomalyID(workerID int, anomalyType, containerName string) string {
	return strings.Join([]string{
		strconv.Itoa(workerID),
		anomalyType,
		containerName,
	}, ":")
}

// ParseContainerNames extracts container names from heartbeat container_stats payload.
func ParseContainerNames(payload map[string]any) []string {
	statsRaw, ok := payload["container_stats"]
	if !ok {
		return nil
	}
	// container_stats is a JSON-decoded []any from the heartbeat
	statsSlice, ok := statsRaw.([]any)
	if !ok {
		return nil
	}
	var names []string
	for _, item := range statsSlice {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if name, ok := m["name"].(string); ok && name != "" {
			names = append(names, name)
		}
	}
	return names
}

// ParseContainerNamesFromSync builds a name list from individual container_sync messages.
// Call this on each sync message and aggregate externally.
func ParseContainerNameFromSync(payload map[string]any) string {
	name, _ := payload["container_name"].(string)
	return name
}

