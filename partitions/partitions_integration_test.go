//go:build integration

package partitions

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

// LATTICE_TEST_DSN points at a throwaway MariaDB the test may create a
// database in, e.g. root:x@tcp(127.0.0.1:33991)/ for
// `docker run --rm -e MARIADB_ROOT_PASSWORD=x -p 127.0.0.1:33991:3306 mariadb:11`.
// Unset, the test skips.
const testDSNEnv = "LATTICE_TEST_DSN"

// schema mirrors production before migration 027: container_logs as built by
// the deleted migrations 003/008/011 plus db.Init's legacy block (and 026's
// indexes), container_metrics as db.Init creates it.
var schema = []string{
	`CREATE TABLE container_logs (
		id INT AUTO_INCREMENT PRIMARY KEY,
		container_id INT,
		container_name VARCHAR(255) DEFAULT NULL,
		worker_id INT NOT NULL,
		stream VARCHAR(10) NOT NULL DEFAULT 'stdout',
		message TEXT NOT NULL,
		recorded_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
		KEY idx_logs_container (container_id),
		KEY idx_logs_worker (worker_id),
		KEY idx_logs_recorded (recorded_at),
		KEY idx_logs_container_name (container_name),
		UNIQUE KEY uq_container_log_dedup (container_id, recorded_at, message(191)),
		KEY idx_cl_recorded (recorded_at),
		KEY idx_cl_container_recorded (container_id, recorded_at),
		KEY idx_cl_name_orphan_recorded (container_name, container_id, recorded_at)
	) DEFAULT CHARSET=utf8mb4`,
	`CREATE TABLE container_metrics (
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
		INDEX idx_cm_worker_recorded (worker_id, recorded_at),
		INDEX idx_cm_recorded (recorded_at)
	)`,
}

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv(testDSNEnv)
	if dsn == "" {
		t.Skipf("%s not set", testDSNEnv)
	}
	admin, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	name := fmt.Sprintf("lattice_part_%d", time.Now().UnixNano())
	if _, err := admin.Exec("CREATE DATABASE " + name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec("DROP DATABASE " + name) })
	conn, err := sql.Open("mysql", strings.TrimSuffix(dsn, "/")+"/"+name+"?parseTime=true&time_zone=%27%2B00%3A00%27")
	if err != nil {
		t.Fatal(err)
	}
	conn.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = conn.Close() })
	for _, s := range schema {
		mustExec(t, conn, s)
	}
	return conn
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("%v\n%s", err, q)
	}
}

func count(t *testing.T, db *sql.DB, q string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q).Scan(&n); err != nil {
		t.Fatalf("%v\n%s", err, q)
	}
	return n
}

func TestMigrateAndMaintain(t *testing.T) {
	db := openDB(t)
	now := time.Date(2026, 11, 8, 15, 0, 0, 0, time.UTC)
	ctx := context.Background()

	// Rows from before the keep window, inside it, and today.
	for _, at := range []string{"2026-10-01 10:00:00", "2026-11-02 10:00:00", "2026-11-08 10:00:00"} {
		mustExec(t, db, "INSERT INTO container_logs (container_id, worker_id, message, recorded_at) VALUES (1, 1, ?, ?)", "line "+at, at)
		mustExec(t, db, "INSERT INTO container_metrics (worker_id, container_name, recorded_at) VALUES (1, 'web', ?)", at)
	}

	if err := migrate(db, now, MaxAutoBytes); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, tbl := range Tables {
		names, err := Partitions(db, tbl.Name)
		if err != nil {
			t.Fatal(err)
		}
		if len(names) != 12 || names[0] != "p_old" || names[len(names)-1] != "pmax" {
			t.Errorf("%s partitions = %v", tbl.Name, names)
		}
		if n := count(t, db, "SELECT COUNT(*) FROM "+tbl.Name); n != 3 {
			t.Errorf("%s lost rows in the rewrite: %d", tbl.Name, n)
		}
	}
	var idType string
	if err := db.QueryRow(`SELECT DATA_TYPE FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'container_logs' AND COLUMN_NAME = 'id'`).Scan(&idType); err != nil {
		t.Fatal(err)
	}
	if idType != "bigint" {
		t.Errorf("container_logs.id is %s, want bigint", idType)
	}

	// The insert paths still work: a replay of the same line is still dropped
	// by the dedup key, and a far-past line lands in p_old.
	mustExec(t, db, "INSERT INTO container_logs (container_id, worker_id, message, recorded_at) VALUES (1, 1, 'line 2026-11-08 10:00:00', '2026-11-08 10:00:00') ON DUPLICATE KEY UPDATE id = id")
	mustExec(t, db, "INSERT INTO container_logs (container_id, worker_id, message, recorded_at) VALUES (2, 1, 'replay', '2020-01-01 00:00:00')")
	mustExec(t, db, "INSERT INTO container_logs (container_id, worker_id, message) VALUES (2, 1, 'now')")
	mustExec(t, db, "INSERT INTO container_metrics (worker_id, container_name) VALUES (1, 'web')")
	if n := count(t, db, "SELECT COUNT(*) FROM container_logs"); n != 5 {
		t.Errorf("container_logs has %d rows after inserts, want 5 (replay deduped)", n)
	}

	// Idempotent: a second run (e.g. after a manual partitioning) is a no-op.
	if err := migrate(db, now, MaxAutoBytes); err != nil {
		t.Fatalf("second migrate: %v", err)
	}

	// Maintenance the same day: p_old (the 2026-10-01 and 2020 rows) is
	// emptied, nothing else changes.
	for _, tbl := range Tables {
		if err := Maintain(ctx, db, tbl, now); err != nil {
			t.Fatalf("maintain %s: %v", tbl.Name, err)
		}
	}
	if n := count(t, db, "SELECT COUNT(*) FROM container_logs WHERE recorded_at < '2026-11-01'"); n != 0 {
		t.Errorf("p_old still has %d rows", n)
	}

	// Five days later: 2026-11-02 has expired and its partition is gone,
	// 2026-11-08 is kept, and the days through 2026-11-15 exist.
	later := now.AddDate(0, 0, 5)
	for _, tbl := range Tables {
		if err := Maintain(ctx, db, tbl, later); err != nil {
			t.Fatalf("maintain %s later: %v", tbl.Name, err)
		}
		names, _ := Partitions(db, tbl.Name)
		joined := strings.Join(names, ",")
		if strings.Contains(joined, "p20261102") || !strings.Contains(joined, "p20261115") || !strings.Contains(joined, "p20261108") || names[len(names)-1] != "pmax" {
			t.Errorf("%s partitions after 5 days = %v", tbl.Name, names)
		}
	}
	if n := count(t, db, "SELECT COUNT(*) FROM container_logs WHERE recorded_at < '2026-11-03'"); n != 0 {
		t.Errorf("expired rows survived: %d", n)
	}
	if n := count(t, db, "SELECT COUNT(*) FROM container_logs WHERE recorded_at >= '2026-11-08'"); n == 0 {
		t.Error("kept rows were dropped")
	}
}

func TestMigrateSkipsLargeTable(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, "INSERT INTO container_logs (container_id, worker_id, message) VALUES (1, 1, 'x')")
	// A 1-byte limit stands in for a table over MaxAutoBytes.
	if err := migrate(db, time.Now(), 1); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, tbl := range Tables {
		names, err := Partitions(db, tbl.Name)
		if err != nil {
			t.Fatal(err)
		}
		if len(names) != 0 {
			t.Errorf("%s was partitioned despite the size guard: %v", tbl.Name, names)
		}
	}
	// Retention's fallback still applies to it: CheckStartup only reports.
	CheckStartup(db)
}
