package query

import (
	"strings"
	"testing"
	"time"
)

func TestContainerLogsInsert(t *testing.T) {
	ts := time.Date(2026, 10, 16, 19, 0, 0, 123456000, time.UTC)
	id := 7
	reqs := []CreateContainerLogRequest{
		{ContainerID: &id, WorkerID: 1, Stream: "stdout", Message: "with ts", RecordedAt: &ts},
		{WorkerID: 2, Stream: "stderr", Message: "no ts"},
	}
	qStr, args, err := containerLogsInsert(reqs)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"INSERT INTO container_logs (container_id,container_name,worker_id,stream,message,recorded_at) VALUES (?,?,?,?,?,?),(?,?,?,?,?,CURRENT_TIMESTAMP(6))",
		"ON DUPLICATE KEY UPDATE id = id",
	} {
		if !strings.Contains(qStr, want) {
			t.Fatalf("query %q missing %q", qStr, want)
		}
	}
	if len(args) != 11 {
		t.Fatalf("got %d args, want 11", len(args))
	}
	if args[5] != "2026-10-16 19:00:00.123456" {
		t.Fatalf("recorded_at arg = %v", args[5])
	}
}
