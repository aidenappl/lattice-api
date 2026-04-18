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
}

type Queryable interface {
	Exec(query string, args ...interface{}) (sql.Result, error)
	Prepare(query string) (*sql.Stmt, error)
	Query(query string, args ...interface{}) (*sql.Rows, error)
	QueryRow(query string, args ...interface{}) *sql.Row
}
