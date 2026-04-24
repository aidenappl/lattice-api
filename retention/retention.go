package retention

import (
	"database/sql"
	"fmt"
	"regexp"
	"time"

	"github.com/aidenappl/lattice-api/logger"
)

const batchSize = 10000

// validIdentifier ensures table/column names are safe SQL identifiers.
var validIdentifier = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

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
	logger.Info("retention", "starting cleanup")

	// Container logs: keep 7 days
	purge(db, "container_logs", "recorded_at", "7 DAY")

	// Lifecycle logs: keep 14 days
	purge(db, "lifecycle_logs", "recorded_at", "14 DAY")

	// Worker metrics: keep 30 days
	purge(db, "worker_metrics", "recorded_at", "30 DAY")

	// Container metrics: keep 7 days (high volume, shorter retention)
	purge(db, "container_metrics", "recorded_at", "7 DAY")

	// Deployment logs: keep 90 days
	purge(db, "deployment_logs", "recorded_at", "90 DAY")

	// Audit log: keep 180 days
	purge(db, "audit_log", "inserted_at", "180 DAY")

	logger.Info("retention", "cleanup complete")
}

// purge deletes rows older than the retention interval in batches to avoid
// holding long table locks. Loops until fewer than batchSize rows are deleted.
// All arguments must be safe SQL identifiers or interval literals — they are
// validated before use but should only ever be hardcoded constants.
func purge(db *sql.DB, table, column, interval string) {
	if !validIdentifier.MatchString(table) || !validIdentifier.MatchString(column) {
		logger.Error("retention", "invalid table/column name", logger.F{"table": table, "column": column})
		return
	}
	query := fmt.Sprintf("DELETE FROM %s WHERE %s < NOW() - INTERVAL %s LIMIT %d", table, column, interval, batchSize)
	var totalDeleted int64

	for {
		result, err := db.Exec(query)
		if err != nil {
			logger.Error("retention", table+" cleanup error", logger.F{"error": err})
			return
		}
		affected, _ := result.RowsAffected()
		totalDeleted += affected
		if affected < batchSize {
			break
		}
		// Brief pause between batches to avoid lock contention
		time.Sleep(100 * time.Millisecond)
	}

	if totalDeleted > 0 {
		logger.Info("retention", "deleted old "+table+" rows", logger.F{"rows": totalDeleted})
	}
}
