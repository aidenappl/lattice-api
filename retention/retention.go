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

	// Database instance lifecycle events: keep 180 days. This table had no
	// retention at all and grows with every status change, health observation,
	// reconcile and credential reveal.
	purge(db, "database_instance_events", "inserted_at", "180 DAY")

	// Retired snapshot rows: keep 90 days, and only rows already soft-deleted.
	//
	// The active = 0 condition is load-bearing. A live snapshot row is the only
	// record of where its remote file lives, so purging by age alone would
	// silently orphan objects on S3/Drive/Samba with nothing left to find them
	// by — the same leak the remote-delete path exists to prevent.
	purgeWhere(db, "database_snapshots", "inserted_at", "90 DAY", "active = 0")

	logger.Info("retention", "cleanup complete")
}

// purge deletes rows older than the retention interval in batches to avoid
// holding long table locks. Loops until fewer than batchSize rows are deleted.
// All arguments must be safe SQL identifiers or interval literals — they are
// validated before use but should only ever be hardcoded constants.
func purge(db *sql.DB, table, column, interval string) {
	purgeWhere(db, table, column, interval, "")
}

// purgeWhere is purge with an additional hardcoded predicate, for tables where
// age alone is not a safe criterion. Like the other arguments, `extra` must be a
// compile-time constant — it is interpolated, not parameterised.
func purgeWhere(db *sql.DB, table, column, interval, extra string) {
	if !validIdentifier.MatchString(table) || !validIdentifier.MatchString(column) {
		logger.Error("retention", "invalid table/column name", logger.F{"table": table, "column": column})
		return
	}
	condition := ""
	if extra != "" {
		condition = " AND " + extra
	}
	query := fmt.Sprintf("DELETE FROM %s WHERE %s < NOW() - INTERVAL %s%s LIMIT %d", table, column, interval, condition, batchSize)
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
