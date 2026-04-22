package db

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/aidenappl/lattice-api/env"
	_ "github.com/go-sql-driver/mysql"
)

const (
	DEFAULT_LIMIT = 50
	MAX_LIMIT     = 500
)

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
	_, _ = db.Exec("ALTER TABLE workers ADD COLUMN pending_action TEXT DEFAULT NULL")
	_, _ = db.Exec("ALTER TABLE containers ADD COLUMN depends_on TEXT DEFAULT NULL")
	_, _ = db.Exec("ALTER TABLE containers ADD COLUMN started_at DATETIME DEFAULT NULL")
	// Backfill started_at for running containers that don't have it set
	_, _ = db.Exec("UPDATE containers SET started_at = updated_at WHERE status = 'running' AND started_at IS NULL")
	_, _ = db.Exec("ALTER TABLE stacks ADD COLUMN placement_constraints TEXT DEFAULT NULL")

	// Rename forta_id -> sso_subject (Forta removal migration)
	_, _ = db.Exec("ALTER TABLE users CHANGE COLUMN forta_id sso_subject VARCHAR(255) DEFAULT NULL")
	// If forta_id doesn't exist, add sso_subject directly
	_, _ = db.Exec("ALTER TABLE users ADD COLUMN sso_subject VARCHAR(255) DEFAULT NULL")

	// Expand auth_type and role columns to support new values (may be ENUMs)
	_, _ = db.Exec("ALTER TABLE users MODIFY COLUMN auth_type VARCHAR(20) NOT NULL DEFAULT 'local'")
	_, _ = db.Exec("ALTER TABLE users MODIFY COLUMN role VARCHAR(20) NOT NULL DEFAULT 'viewer'")

	// Allow same email with different auth types (drop unique on email, add composite unique)
	_, _ = db.Exec("ALTER TABLE users DROP INDEX email")
	_, _ = db.Exec("ALTER TABLE users ADD UNIQUE INDEX idx_users_email_auth (email, auth_type)")

	// Add profile image URL column
	_, _ = db.Exec("ALTER TABLE users ADD COLUMN profile_image_url TEXT DEFAULT NULL")

	// Auto-create global_env_vars table if it doesn't exist
	_, _ = db.Exec("CREATE TABLE IF NOT EXISTS global_env_vars (" +
		"id INT AUTO_INCREMENT PRIMARY KEY," +
		"`key` VARCHAR(255) NOT NULL UNIQUE," +
		"encrypted_value TEXT NOT NULL," +
		"is_secret BOOLEAN DEFAULT FALSE," +
		"updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP," +
		"inserted_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP)")

	// Auto-create deploy_tokens table if it doesn't exist
	_, _ = db.Exec(`CREATE TABLE IF NOT EXISTS deploy_tokens (
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
	_, _ = db.Exec(`CREATE TABLE IF NOT EXISTS webhook_configs (
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
	_, _ = db.Exec(`CREATE TABLE IF NOT EXISTS templates (
		id INT AUTO_INCREMENT PRIMARY KEY,
		name VARCHAR(255) NOT NULL,
		description TEXT DEFAULT NULL,
		config LONGTEXT NOT NULL,
		created_by INT DEFAULT NULL,
		active BOOLEAN DEFAULT TRUE,
		updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
		inserted_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	)`)

	// Auto-create settings table if it doesn't exist
	_, _ = db.Exec("CREATE TABLE IF NOT EXISTS settings (" +
		"`key` VARCHAR(255) PRIMARY KEY," +
		"value TEXT NOT NULL," +
		"updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP)")
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
