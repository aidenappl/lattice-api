//go:build integration

package query

import (
	"database/sql"
	"encoding/json"
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

// logsSchema mirrors production: container_logs as built by the deleted
// migrations 003/008/011 plus db.Init's legacy block, lifecycle_logs with the
// indexes db.Init adds. Migration 026 is then applied from its file.
var logsSchema = []string{
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
		KEY idx_cl_recorded (recorded_at)
	) DEFAULT CHARSET=utf8mb4`,
	`CREATE TABLE lifecycle_logs (
		id INT AUTO_INCREMENT PRIMARY KEY,
		container_id INT,
		container_name VARCHAR(255) DEFAULT NULL,
		worker_id INT NOT NULL,
		event VARCHAR(64) NOT NULL,
		message TEXT NOT NULL,
		recorded_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
		UNIQUE KEY idx_lifecycle_dedup (container_id, worker_id, event, recorded_at),
		KEY idx_ll_recorded (recorded_at)
	) DEFAULT CHARSET=utf8mb4`,
}

// seedRows is how many rows each table gets. Container 843 (nexus-api) is one
// row in 200, sparse among its neighbours' recent output, the shape that made
// the legacy query slow: it walks the recorded_at index backwards through
// every other container's rows to find 843's. One in 40,000 of 843's rows has
// container_id NULL and is matched by name only.
const seedRows = 600000

func seed(table, extraCol, extraVal string) string {
	return fmt.Sprintf(`INSERT INTO %[1]s (container_id, container_name, worker_id, %[2]s, message, recorded_at)
		WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM seq WHERE n < %[4]d)
		SELECT CASE WHEN n %% 40000 = 0 THEN NULL WHEN n %% 200 = 0 THEN 843 ELSE 800 + n %% 40 END,
		       IF(n %% 200 = 0, 'nexus-api', CONCAT('c', n %% 40)), 4, %[3]s,
		       CONCAT('{"level":"INFO","fields":{"uri":"/v1/threads/APL-', n, '","request_id":"', UUID(), '"}}'),
		       DATE_SUB('2026-11-08 21:00:00', INTERVAL n * 400000 MICROSECOND)
		FROM seq`, table, extraCol, extraVal, seedRows)
}

func openLogsDB(t *testing.T) *sql.DB {
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
	name := fmt.Sprintf("lattice_it_%d", time.Now().UnixNano())
	if _, err := admin.Exec("CREATE DATABASE " + name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec("DROP DATABASE " + name) })

	conn, err := sql.Open("mysql", strings.TrimSuffix(dsn, "/")+"/"+name+"?parseTime=true")
	if err != nil {
		t.Fatal(err)
	}
	conn.SetMaxOpenConns(1) // the session variable below must stick
	t.Cleanup(func() { _ = conn.Close() })

	stmts := append([]string{"SET SESSION max_recursive_iterations = 10000000"}, logsSchema...)
	stmts = append(stmts, seed("container_logs", "stream", "'stdout'"), seed("lifecycle_logs", "event", "CONCAT('e', n)"))
	mig, err := os.ReadFile("../db/migrations/026_log_scope_indexes.sql")
	if err != nil {
		t.Fatal(err)
	}
	stmts = append(stmts, sqlStatements(string(mig))...)
	stmts = append(stmts, "ANALYZE TABLE container_logs", "ANALYZE TABLE lifecycle_logs")
	for _, s := range stmts {
		if _, err := conn.Exec(s); err != nil {
			t.Fatalf("%v\n%s", err, s)
		}
	}
	return conn
}

// sqlStatements splits a migration file on ';', dropping comment lines.
func sqlStatements(file string) []string {
	var lines []string
	for _, l := range strings.Split(file, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(l), "--") {
			lines = append(lines, l)
		}
	}
	var stmts []string
	for _, s := range strings.Split(strings.Join(lines, "\n"), ";") {
		if s = strings.TrimSpace(s); s != "" {
			stmts = append(stmts, s)
		}
	}
	return stmts
}

// tableAccess is one read of a log table in an ANALYZE FORMAT=JSON plan.
type tableAccess struct {
	key  string
	rows float64
}

// analyze runs ANALYZE FORMAT=JSON and returns every log-table read, whether a
// filesort read a log table directly, and how long the query took.
func analyze(t *testing.T, conn *sql.DB, q string, args []any) ([]tableAccess, bool, time.Duration) {
	t.Helper()
	var raw string
	start := time.Now()
	if err := conn.QueryRow("ANALYZE FORMAT=JSON "+q, args...).Scan(&raw); err != nil {
		t.Fatalf("analyze: %v\n%s", err, q)
	}
	took := time.Since(start)
	var plan any
	if err := json.Unmarshal([]byte(raw), &plan); err != nil {
		t.Fatal(err)
	}
	var tables []tableAccess
	sortedBase := false
	var walk func(v any, inFilesort bool)
	walk = func(v any, inFilesort bool) {
		switch n := v.(type) {
		case map[string]any:
			if tbl, ok := n["table"].(map[string]any); ok {
				if name, _ := tbl["table_name"].(string); name == "container_logs" || name == "lifecycle_logs" {
					key, _ := tbl["key"].(string)
					rows, _ := tbl["r_rows"].(float64)
					tables = append(tables, tableAccess{key, rows})
					if inFilesort {
						sortedBase = true
					}
				}
			}
			for k, c := range n {
				// A union's own sort of the merged rows sits above
				// query_specifications; only a filesort directly over a log
				// table counts.
				walk(c, (inFilesort || k == "filesort") && k != "query_specifications")
			}
		case []any:
			for _, c := range n {
				walk(c, inFilesort)
			}
		}
	}
	walk(plan, false)
	return tables, sortedBase, took
}

// legacy is the single WHERE ... OR ... query ListContainerLogs and
// ListLifecycleLogs ran before APL-119, kept to compare plans and results.
func legacy(table string, cols []string, id int, name string, limit int) (string, []any) {
	return fmt.Sprintf(`SELECT %s FROM %s WHERE (%[2]s.container_id = ? OR (%[2]s.container_id IS NULL AND %[2]s.container_name = ?))
		ORDER BY %[2]s.recorded_at DESC LIMIT %d`, strings.Join(cols, ", "), table, limit), []any{id, name}
}

func TestLogScopePlans(t *testing.T) {
	conn := openLogsDB(t)
	id, name := 843, "nexus-api"

	tests := []struct {
		name  string
		table string
		cols  []string
		limit int
	}{
		{"container_logs limit 500", "container_logs", logColumns, 500},
		{"container_logs limit 50", "container_logs", logColumns, 50},
		{"lifecycle_logs limit 500", "lifecycle_logs", lifecycleLogColumns, 500},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q, args, err := logScope{table: tt.table, columns: tt.cols, containerID: &id, containerName: &name, limit: tt.limit}.ToSql()
			if err != nil {
				t.Fatal(err)
			}
			tables, sortedBase, took := analyze(t, conn, q, args)
			oldQ, oldArgs := legacy(tt.table, tt.cols, id, name, tt.limit)
			oldTables, oldSorted, oldTook := analyze(t, conn, oldQ, oldArgs)
			t.Logf("legacy: %v base-filesort=%v %s | logScope: %v base-filesort=%v %s", oldTables, oldSorted, oldTook, tables, sortedBase, took)

			// Each branch reads at most limit rows off an index; nothing
			// filesorts the log table itself.
			if sortedBase {
				t.Errorf("a branch filesorts the log table: %v", tables)
			}
			if len(tables) != 2 {
				t.Fatalf("want 2 branch reads, got %v", tables)
			}
			for _, a := range tables {
				if a.key == "" {
					t.Errorf("branch reads without an index: %v", tables)
				}
				if a.rows > float64(tt.limit) {
					t.Errorf("branch read %.0f rows for limit %d: %v", a.rows, tt.limit, tables)
				}
			}

			// Same rows, same order as the legacy query.
			if got, want := rowIDs(t, conn, q, args), rowIDs(t, conn, oldQ, oldArgs); fmt.Sprint(got) != fmt.Sprint(want) {
				t.Errorf("logScope returned different rows than the legacy query")
			}
		})
	}

	t.Run("public functions", func(t *testing.T) {
		logs, err := ListContainerLogs(conn, ListLogsRequest{ContainerID: &id, ContainerName: &name, Limit: 500})
		if err != nil || logs == nil || len(*logs) != 500 {
			t.Fatalf("ListContainerLogs: %v", err)
		}
		for i, l := range *logs {
			if i > 0 && l.RecordedAt.After((*logs)[i-1].RecordedAt) {
				t.Fatal("not newest first")
			}
		}
		// An ID with no rows of its own still finds the name-only rows.
		none := 999
		orphans, err := ListContainerLogs(conn, ListLogsRequest{ContainerID: &none, ContainerName: &name, Limit: 500})
		if err != nil || orphans == nil || len(*orphans) != seedRows/40000 {
			t.Fatalf("name-only rows: %v, %v", orphans, err)
		}
		for _, l := range *orphans {
			if l.ContainerID != nil {
				t.Fatalf("name-only branch returned container_id %d", *l.ContainerID)
			}
		}
		page2, err := ListLifecycleLogs(conn, ListLifecycleLogsRequest{ContainerID: &id, ContainerName: &name, Limit: 10, Offset: 10})
		if err != nil || page2 == nil || len(*page2) != 10 {
			t.Fatalf("ListLifecycleLogs page 2: %v", err)
		}
	})
}

// rowIDs runs q and returns the first column (id) of every row, in order.
func rowIDs(t *testing.T, conn *sql.DB, q string, args []any) []int64 {
	t.Helper()
	rows, err := conn.Query(q, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		ptrs := make([]any, len(cols))
		ptrs[0] = &id
		for i := 1; i < len(cols); i++ {
			ptrs[i] = new(sql.RawBytes)
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}
