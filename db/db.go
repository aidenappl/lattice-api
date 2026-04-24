package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/aidenappl/lattice-api/env"
	"github.com/go-sql-driver/mysql"
	_ "github.com/go-sql-driver/mysql"
)

const (
	DEFAULT_LIMIT = 50
	MAX_LIMIT     = 500
)

// migrate runs an idempotent DDL statement (ALTER TABLE ADD COLUMN, CREATE TABLE IF NOT EXISTS,
// ADD INDEX, etc.) and logs any unexpected errors. Expected errors like "duplicate column" or
// "duplicate key name" (MySQL 1060/1061) are silently ignored since they indicate the migration
// was already applied.
func migrate(db *sql.DB, stmt string) {
	_, err := db.Exec(stmt)
	if err == nil {
		return
	}
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) {
		switch mysqlErr.Number {
		case 1060, // Duplicate column name
			1061, // Duplicate key name
			1062, // Duplicate entry
			1050, // Table already exists
			1054, // Unknown column (harmless for CHANGE COLUMN when already renamed)
			1091: // Can't DROP; check that column/key exists
			return
		}
	}
	log.Printf("migration warning: %v\n  stmt: %.200s", err, stmt)
}

func PingDB(db *sql.DB) error {
	if err := db.Ping(); err != nil {
		fmt.Println(" ❌ Failed")
		return err
	}
	return nil
}

var DB *sql.DB

const schema = "lattice"

func Init() {
	fmt.Print("Connecting to Lattice DB...")

	// Strip any existing path or query params from the base DSN so we can
	// cleanly append the schema and our own params. The DSN may be provided
	// with or without query params already attached.
	base := env.CoreDBDSN
	if idx := strings.IndexAny(base, "/?"); idx != -1 {
		base = base[:idx]
	}
	dsn := base + "/" + schema + "?charset=utf8mb4&parseTime=True"

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		panic(fmt.Sprintf("failed to connect to database: %v", err))
	}

	// Connection pool settings
	db.SetMaxOpenConns(25)                 // Max concurrent connections
	db.SetMaxIdleConns(10)                 // Keep idle connections ready
	db.SetConnMaxLifetime(5 * time.Minute) // Recycle connections

	DB = db

	// Auto-add columns if they don't exist (ignore error if already present)
	migrate(db, "ALTER TABLE workers ADD COLUMN pending_action TEXT DEFAULT NULL")
	migrate(db, "ALTER TABLE containers ADD COLUMN depends_on TEXT DEFAULT NULL")
	migrate(db, "ALTER TABLE containers ADD COLUMN started_at DATETIME DEFAULT NULL")
	// Backfill started_at for running containers that don't have it set
	migrate(db, "UPDATE containers SET started_at = updated_at WHERE status = 'running' AND started_at IS NULL")
	migrate(db, "ALTER TABLE stacks ADD COLUMN placement_constraints TEXT DEFAULT NULL")

	// Rename forta_id -> sso_subject (Forta removal migration)
	migrate(db, "ALTER TABLE users CHANGE COLUMN forta_id sso_subject VARCHAR(255) DEFAULT NULL")
	// If forta_id doesn't exist, add sso_subject directly
	migrate(db, "ALTER TABLE users ADD COLUMN sso_subject VARCHAR(255) DEFAULT NULL")

	// Expand auth_type and role columns to support new values (may be ENUMs)
	migrate(db, "ALTER TABLE users MODIFY COLUMN auth_type VARCHAR(20) NOT NULL DEFAULT 'local'")
	migrate(db, "ALTER TABLE users MODIFY COLUMN role VARCHAR(20) NOT NULL DEFAULT 'viewer'")

	// Allow same email with different auth types (drop unique on email, add composite unique)
	migrate(db, "ALTER TABLE users DROP INDEX email")
	migrate(db, "ALTER TABLE users ADD UNIQUE INDEX idx_users_email_auth (email, auth_type)")

	// Add profile image URL column
	migrate(db, "ALTER TABLE users ADD COLUMN profile_image_url TEXT DEFAULT NULL")

	// Token revocation: setting this timestamp invalidates all JWTs issued before it
	migrate(db, "ALTER TABLE users ADD COLUMN tokens_revoked_at DATETIME DEFAULT NULL")

	// Auto-create global_env_vars table if it doesn't exist
	migrate(db, "CREATE TABLE IF NOT EXISTS global_env_vars (" +
		"id INT AUTO_INCREMENT PRIMARY KEY," +
		"`key` VARCHAR(255) NOT NULL UNIQUE," +
		"encrypted_value TEXT NOT NULL," +
		"is_secret BOOLEAN DEFAULT FALSE," +
		"updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP," +
		"inserted_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP)")

	// Auto-create deploy_tokens table if it doesn't exist
	migrate(db, `CREATE TABLE IF NOT EXISTS deploy_tokens (
		id INT AUTO_INCREMENT PRIMARY KEY,
		stack_id INT NOT NULL,
		name VARCHAR(255) NOT NULL,
		token_hash VARCHAR(255) NOT NULL,
		last_used_at TIMESTAMP NULL DEFAULT NULL,
		active BOOLEAN DEFAULT TRUE,
		updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
		inserted_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		INDEX idx_deploy_tokens_hash (token_hash))`)

	// Auto-create webhook_configs table if it doesn't exist
	migrate(db, `CREATE TABLE IF NOT EXISTS webhook_configs (
		id INT AUTO_INCREMENT PRIMARY KEY,
		name VARCHAR(255) NOT NULL,
		url TEXT NOT NULL,
		events TEXT NOT NULL,
		active BOOLEAN DEFAULT TRUE,
		secret VARCHAR(255) DEFAULT NULL,
		updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
		inserted_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	)`)

	// Auto-create templates table if it doesn't exist
	migrate(db, `CREATE TABLE IF NOT EXISTS templates (
		id INT AUTO_INCREMENT PRIMARY KEY,
		name VARCHAR(255) NOT NULL,
		description TEXT DEFAULT NULL,
		config LONGTEXT NOT NULL,
		created_by INT DEFAULT NULL,
		active BOOLEAN DEFAULT TRUE,
		updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
		inserted_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	)`)

	// Add network_aliases column for compose service name DNS resolution
	migrate(db, "ALTER TABLE containers ADD COLUMN network_aliases TEXT DEFAULT NULL AFTER depends_on")

	// Auto-create settings table if it doesn't exist
	migrate(db, "CREATE TABLE IF NOT EXISTS settings (" +
		"`key` VARCHAR(255) PRIMARY KEY," +
		"value TEXT NOT NULL," +
		"updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP)")

	// ── Idempotency migrations ────────────────────────────────────────────

	// Unique name constraints to prevent duplicate resource creation.
	// MariaDB doesn't support partial indexes, so we use a conditional unique
	// index approach: only active=1 rows are constrained. We create a virtual
	// column and index on it, OR just add the index and let soft-delete
	// handle collisions at reactivation time. For simplicity we add
	// composite unique indexes — soft-deleted rows (active=0) are excluded
	// because their names can be reused.
	migrate(db, "ALTER TABLE stacks ADD UNIQUE INDEX idx_stacks_name_active (name, active)")
	migrate(db, "ALTER TABLE workers ADD UNIQUE INDEX idx_workers_name_active (name, active)")
	migrate(db, "ALTER TABLE containers ADD UNIQUE INDEX idx_containers_stack_name_active (stack_id, name, active)")
	migrate(db, "ALTER TABLE registries ADD UNIQUE INDEX idx_registries_name_active (name, active)")

	// Unique hash constraints on tokens
	migrate(db, "ALTER TABLE worker_tokens ADD UNIQUE INDEX idx_worker_tokens_hash (token_hash)")
	migrate(db, "ALTER TABLE deploy_tokens ADD UNIQUE INDEX idx_deploy_tokens_hash_unique (token_hash)")

	// Lifecycle log dedup index (mirrors container_logs pattern)
	migrate(db, "ALTER TABLE lifecycle_logs ADD UNIQUE INDEX idx_lifecycle_dedup (container_id, worker_id, event, recorded_at)")

	// Deployment log dedup index
	migrate(db, "ALTER TABLE deployment_logs ADD UNIQUE INDEX idx_deployment_log_dedup (deployment_id, level, recorded_at, message(255))")

	// ── Metrics indexes ─────────────────────��────────────────────────────
	migrate(db, "ALTER TABLE worker_metrics ADD INDEX idx_wm_recorded (recorded_at)")
	migrate(db, "ALTER TABLE worker_metrics ADD INDEX idx_wm_worker_recorded (worker_id, recorded_at)")

	// ── Retention purge indexes ──────────────────────────────────────────
	// These cover the `DELETE ... WHERE recorded_at < ...` queries in retention/retention.go.
	migrate(db, "ALTER TABLE container_logs ADD INDEX idx_cl_recorded (recorded_at)")
	migrate(db, "ALTER TABLE lifecycle_logs ADD INDEX idx_ll_recorded (recorded_at)")
	migrate(db, "ALTER TABLE container_metrics ADD INDEX idx_cm_recorded (recorded_at)")
	migrate(db, "ALTER TABLE deployment_logs ADD INDEX idx_dl_recorded (recorded_at)")

	// ── Container metrics table ──────────────────────────────────────────
	migrate(db, `CREATE TABLE IF NOT EXISTS container_metrics (
		id BIGINT AUTO_INCREMENT PRIMARY KEY,
		worker_id INT NOT NULL,
		container_id INT DEFAULT NULL,
		container_name VARCHAR(255) NOT NULL,
		cpu_percent DOUBLE DEFAULT NULL,
		mem_usage_mb DOUBLE DEFAULT NULL,
		mem_limit_mb DOUBLE DEFAULT NULL,
		mem_percent DOUBLE DEFAULT NULL,
		recorded_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		INDEX idx_cm_container_recorded (container_id, recorded_at),
		INDEX idx_cm_worker_recorded (worker_id, recorded_at)
	)`)

	// Deploy claim TTL — tracks when a stack was claimed for deployment so
	// stale claims (>30 min) can be broken automatically.
	migrate(db, "ALTER TABLE stacks ADD COLUMN deploy_claimed_at DATETIME DEFAULT NULL")

	// Add network rate and runner self-metrics columns
	migrate(db, "ALTER TABLE worker_metrics ADD COLUMN network_rx_rate DOUBLE DEFAULT NULL AFTER network_tx_bytes")
	migrate(db, "ALTER TABLE worker_metrics ADD COLUMN network_tx_rate DOUBLE DEFAULT NULL AFTER network_rx_rate")
	migrate(db, "ALTER TABLE worker_metrics ADD COLUMN runner_goroutines INT DEFAULT NULL AFTER process_count")
	migrate(db, "ALTER TABLE worker_metrics ADD COLUMN runner_heap_mb DOUBLE DEFAULT NULL AFTER runner_goroutines")
	migrate(db, "ALTER TABLE worker_metrics ADD COLUMN runner_sys_mb DOUBLE DEFAULT NULL AFTER runner_heap_mb")
}

// QueryContext returns a context with a 30-second timeout suitable for
// database queries. Callers can opt into this for long-running or
// user-facing queries to prevent unbounded execution.
func QueryContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

type Queryable interface {
	Exec(query string, args ...interface{}) (sql.Result, error)
	Prepare(query string) (*sql.Stmt, error)
	Query(query string, args ...interface{}) (*sql.Rows, error)
	QueryRow(query string, args ...interface{}) *sql.Row
}

func BeginTx() (*sql.Tx, error) {
	return DB.Begin()
}
