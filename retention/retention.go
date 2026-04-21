package retention

import (
	"database/sql"
	"log"
	"time"
)

// Start launches a background goroutine that periodically purges old logs and metrics.
// Runs every hour.
func Start(db *sql.DB) {
	go func() {
		// Run initial cleanup after 1 minute (let the app fully start)
		time.Sleep(1 * time.Minute)
		run(db)

		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			run(db)
		}
	}()
}

func run(db *sql.DB) {
	log.Println("[retention] starting cleanup...")

	// Container logs: keep 7 days
	if result, err := db.Exec("DELETE FROM container_logs WHERE recorded_at < NOW() - INTERVAL 7 DAY"); err != nil {
		log.Printf("[retention] container_logs cleanup error: %v", err)
	} else if rows, _ := result.RowsAffected(); rows > 0 {
		log.Printf("[retention] deleted %d old container log rows", rows)
	}

	// Lifecycle logs: keep 14 days
	if result, err := db.Exec("DELETE FROM lifecycle_logs WHERE recorded_at < NOW() - INTERVAL 14 DAY"); err != nil {
		log.Printf("[retention] lifecycle_logs cleanup error: %v", err)
	} else if rows, _ := result.RowsAffected(); rows > 0 {
		log.Printf("[retention] deleted %d old lifecycle log rows", rows)
	}

	// Worker metrics: keep 30 days
	if result, err := db.Exec("DELETE FROM worker_metrics WHERE recorded_at < NOW() - INTERVAL 30 DAY"); err != nil {
		log.Printf("[retention] worker_metrics cleanup error: %v", err)
	} else if rows, _ := result.RowsAffected(); rows > 0 {
		log.Printf("[retention] deleted %d old metric rows", rows)
	}

	// Deployment logs: keep 90 days
	if result, err := db.Exec("DELETE FROM deployment_logs WHERE recorded_at < NOW() - INTERVAL 90 DAY"); err != nil {
		log.Printf("[retention] deployment_logs cleanup error: %v", err)
	} else if rows, _ := result.RowsAffected(); rows > 0 {
		log.Printf("[retention] deleted %d old deployment log rows", rows)
	}

	// Audit log: keep 180 days
	if result, err := db.Exec("DELETE FROM audit_log WHERE inserted_at < NOW() - INTERVAL 180 DAY"); err != nil {
		log.Printf("[retention] audit_log cleanup error: %v", err)
	} else if rows, _ := result.RowsAffected(); rows > 0 {
		log.Printf("[retention] deleted %d old audit log rows", rows)
	}

	log.Println("[retention] cleanup complete")
}
