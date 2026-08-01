package socket

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These two tests inspect source rather than behaviour, and they live in the
// socket package rather than in main for a practical reason: the main package
// imports env, whose package-level init panics without DATABASE_DSN, so no test
// in main can run without a configured database. The protocol is owned here
// anyway.
//
// They guard against the two defect shapes that a conventional test cannot see,
// because in both cases the code compiles and nothing errors at runtime:
// a reply type with no dispatch case, and a command that is defined but never
// sent.

// TestDbReplyTypesHaveDispatchCase asserts every database reply type the runner
// can emit is handled in ws_dispatch.go.
//
// db_delete_snapshot_result and db_schedule_status were both emitted by the
// runner with no constant and no case for months. The messages fell through the
// switch and vanished; snapshot deletions and schedule updates were silently
// lost.
func TestDbReplyTypesHaveDispatchCase(t *testing.T) {
	dispatch := readRepoFile(t, "ws_dispatch.go")

	constNames := map[string]string{
		MsgDbStatus:               "MsgDbStatus",
		MsgDbHealthStatus:         "MsgDbHealthStatus",
		MsgDbSnapshotProgress:     "MsgDbSnapshotProgress",
		MsgDbRestoreStatus:        "MsgDbRestoreStatus",
		MsgDbDeleteSnapshotResult: "MsgDbDeleteSnapshotResult",
		MsgDbScheduleStatus:       "MsgDbScheduleStatus",
		MsgDbMirrorStatus:         "MsgDbMirrorStatus",
		MsgDbSync:                 "MsgDbSync",
		MsgBackupDestTestResult:   "MsgBackupDestTestResult",
	}

	for _, reply := range DbReplyTypes {
		name, ok := constNames[reply]
		if !ok {
			t.Errorf("reply type %q has no constant mapping in this test — add it", reply)
			continue
		}
		if !strings.Contains(dispatch, "socket."+name) {
			t.Errorf("reply type %q (socket.%s) has no case in ws_dispatch.go — messages of this type are silently discarded",
				reply, name)
		}
	}
}

// TestDbCommandsAreDispatched asserts every command constant is actually sent.
//
// MsgDbDeleteSnapshot existed and the runner implemented a handler for it, but
// the orchestrator never sent it — so deleting a snapshot removed the row and
// orphaned the file on S3/Drive/Samba indefinitely.
func TestDbCommandsAreDispatched(t *testing.T) {
	commandNames := []string{
		"MsgDbCreate",
		"MsgDbStart",
		"MsgDbStop",
		"MsgDbRestart",
		"MsgDbRemove",
		"MsgDbSnapshot",
		"MsgDbRestore",
		"MsgDbUpdateSchedule",
		"MsgDbDeleteSnapshot",
		"MsgDbSyncRequest",
		"MsgBackupDestTest",
	}

	// Commands are sent from the repo root (reconciler, dispatch) and from
	// routers/. Tests are excluded so a constant referenced only by a test
	// doesn't count as wired up.
	corpus := readGoSources(t, "", "routers")

	for _, name := range commandNames {
		if !strings.Contains(corpus, "socket."+name) {
			t.Errorf("command socket.%s is defined but never sent — either wire it up or remove it", name)
		}
	}
}

// repoRoot returns the module root, one level above this package.
func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to resolve working directory: %v", err)
	}
	return filepath.Dir(wd)
}

func readRepoFile(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot(t), name))
	if err != nil {
		t.Fatalf("failed to read %s: %v", name, err)
	}
	return string(data)
}

// readGoSources concatenates non-test .go files in the given repo-relative
// directories. An empty string means the repo root.
func readGoSources(t *testing.T, dirs ...string) string {
	t.Helper()
	root := repoRoot(t)

	var sb strings.Builder
	for _, dir := range dirs {
		full := filepath.Join(root, dir)
		entries, err := os.ReadDir(full)
		if err != nil {
			t.Fatalf("failed to read %s: %v", full, err)
		}
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(full, name))
			if err != nil {
				t.Fatalf("failed to read %s: %v", filepath.Join(full, name), err)
			}
			sb.Write(data)
		}
	}
	return sb.String()
}
