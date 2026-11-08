//go:build integration

package query

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aidenappl/lattice-api/partitions"
)

func TestContainerLogVolumeIntegration(t *testing.T) {
	dsn := os.Getenv(testDSNEnv)
	if dsn == "" {
		t.Skipf("%s not set", testDSNEnv)
	}
	admin, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	name := fmt.Sprintf("lattice_vol_%d", time.Now().UnixNano())
	if _, err := admin.Exec("CREATE DATABASE " + name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec("DROP DATABASE " + name) })
	conn, err := sql.Open("mysql", strings.TrimSuffix(dsn, "/")+"/"+name+"?parseTime=true")
	if err != nil {
		t.Fatal(err)
	}
	conn.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = conn.Close() })

	now := time.Date(2026, 11, 8, 15, 0, 0, 0, time.UTC)
	stmts := []string{
		logsSchema[0],
		`CREATE TABLE containers (id INT PRIMARY KEY, stack_id INT NOT NULL)`,
		`CREATE TABLE container_metrics (id BIGINT AUTO_INCREMENT PRIMARY KEY, worker_id INT NOT NULL, container_id INT, container_name VARCHAR(255) NOT NULL, recorded_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP)`,
		`INSERT INTO containers VALUES (1, 10), (2, 20)`,
		// Container 1: 3 lines of 10 bytes today; container 2: one 4-byte line
		// today and one yesterday; an unmanaged container by name only.
		`INSERT INTO container_logs (container_id, container_name, worker_id, message, recorded_at) VALUES
			(1, 'chatty', 1, '0123456789', '2026-11-08 01:00:00'),
			(1, 'chatty', 1, 'abcdefghij', '2026-11-08 02:00:00'),
			(1, 'chatty', 1, 'klmnopqrst', '2026-11-08 03:00:00'),
			(2, 'quiet', 1, 'four', '2026-11-08 04:00:00'),
			(2, 'quiet', 1, 'old!', '2026-11-07 04:00:00'),
			(NULL, 'stray', 1, 'xy', '2026-11-08 05:00:00')`,
	}
	for _, s := range stmts {
		if _, err := conn.Exec(s); err != nil {
			t.Fatalf("%v\n%s", err, s)
		}
	}
	// The report must work on the partitioned table too.
	if err := partitions.Migrate(conn, now); err != nil {
		t.Fatal(err)
	}

	got, err := ContainerLogVolume(conn, ContainerLogVolumeRequest{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if len(*got) != 3 {
		t.Fatalf("got %d rows, want 3 (today only): %+v", len(*got), *got)
	}
	top := (*got)[0]
	if top.Day != "2026-11-08" || top.ContainerID == nil || *top.ContainerID != 1 || top.Lines != 3 || top.Bytes != 30 || top.StackID == nil || *top.StackID != 10 {
		t.Errorf("top row = %+v", top)
	}
	last := (*got)[2]
	if last.ContainerID != nil || last.ContainerName == nil || *last.ContainerName != "stray" || last.StackID != nil {
		t.Errorf("unmanaged row = %+v", last)
	}

	two, err := ContainerLogVolume(conn, ContainerLogVolumeRequest{Days: 2, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if len(*two) != 4 {
		t.Errorf("two days gave %d rows, want 4: %+v", len(*two), *two)
	}
}
