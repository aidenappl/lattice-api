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
	_, _ = db.Exec("ALTER TABLE stacks ADD COLUMN placement_constraints TEXT DEFAULT NULL")

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
}

type Queryable interface {
	Exec(query string, args ...interface{}) (sql.Result, error)
	Prepare(query string) (*sql.Stmt, error)
	Query(query string, args ...interface{}) (*sql.Rows, error)
	QueryRow(query string, args ...interface{}) *sql.Row
}
