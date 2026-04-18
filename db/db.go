package db

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/aidenappl/lattice-api/env"
	_ "github.com/go-sql-driver/mysql"
)

const (
	DEFAULT_LIMIT = 50
	MAX_LIMIT     = 500
)

func PingDB(db *sql.DB) error {
	fmt.Print("Connecting to Lattice DB...")
	if err := db.Ping(); err != nil {
		fmt.Println(" ❌ Failed")
		return err
	}
	fmt.Println(" ✅ Done")
	return nil
}

var DB = func() *sql.DB {
	db, err := sql.Open("mysql", env.CoreDBDSN)
	if err != nil {
		panic(fmt.Sprintf("failed to connect to database: %v", err))
	}

	// Connection pool settings
	db.SetMaxOpenConns(25)                 // Max concurrent connections
	db.SetMaxIdleConns(10)                 // Keep idle connections ready
	db.SetConnMaxLifetime(5 * time.Minute) // Recycle connections

	return db
}()

type Queryable interface {
	Exec(query string, args ...interface{}) (sql.Result, error)
	Prepare(query string) (*sql.Stmt, error)
	Query(query string, args ...interface{}) (*sql.Rows, error)
	QueryRow(query string, args ...interface{}) *sql.Row
}
