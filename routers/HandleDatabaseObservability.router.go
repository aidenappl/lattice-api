package routers

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
	"github.com/aidenappl/lattice-api/structs"
	"github.com/gorilla/mux"
)

// Database container logs have always been collected — the runner's log
// streamer enumerates every container on the host, not just stack containers,
// so lines from lattice-db-* containers have been landing in container_logs
// all along with a NULL container_id.
//
// They were unreachable only because the read path was keyed on containers.id,
// which managed databases do not have. These handlers address the same stored
// rows by (worker_id, container_name) instead.

// HandleGetDatabaseInstanceMetrics returns CPU and memory samples for a database
// instance.
//
// The samples were already being collected and stored — the runner reports stats
// for every container on the host, and they land in container_metrics with a NULL
// container_id keyed on the name. They were simply unreadable, because every
// other reader takes a containers.id that a managed database does not have. Same
// fix as the logs and lifecycle handlers above: address by (worker, name).
func HandleGetDatabaseInstanceMetrics(w http.ResponseWriter, r *http.Request) {
	instance, ok := loadInstanceForObservability(w, r)
	if !ok {
		return
	}

	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}

	var since *time.Time
	if v := r.URL.Query().Get("since"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			since = &t
		}
	}

	metrics, err := query.ListContainerMetricsByName(db.DB, instance.WorkerID, instance.ContainerName, since, limit)
	if err != nil {
		responder.QueryError(w, err, "failed to fetch database metrics")
		return
	}

	responder.New(w, metrics, "database metrics retrieved")
}

// HandleGetDatabaseInstanceLogs returns stdout/stderr for a database instance.
func HandleGetDatabaseInstanceLogs(w http.ResponseWriter, r *http.Request) {
	instance, ok := loadInstanceForObservability(w, r)
	if !ok {
		return
	}

	req := query.ListLogsRequest{
		WorkerID:      &instance.WorkerID,
		ContainerName: &instance.ContainerName,
	}
	if v := r.URL.Query().Get("stream"); v != "" {
		req.Stream = &v
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			req.Limit = n
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			req.Offset = n
		}
	}

	logs, err := query.ListContainerLogs(db.DB, req)
	if err != nil {
		responder.QueryError(w, err, "failed to fetch database logs")
		return
	}

	responder.New(w, logs, "database logs retrieved")
}

// HandleGetDatabaseInstanceLifecycle returns lifecycle events emitted by the
// worker for a database instance's container — including the provisioning
// progress messages and the failure reason when a create goes wrong.
func HandleGetDatabaseInstanceLifecycle(w http.ResponseWriter, r *http.Request) {
	instance, ok := loadInstanceForObservability(w, r)
	if !ok {
		return
	}

	req := query.ListLifecycleLogsRequest{
		WorkerID:      &instance.WorkerID,
		ContainerName: &instance.ContainerName,
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			req.Limit = n
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			req.Offset = n
		}
	}

	logs, err := query.ListLifecycleLogs(db.DB, req)
	if err != nil {
		responder.QueryError(w, err, "failed to fetch database lifecycle logs")
		return
	}

	responder.New(w, logs, "database lifecycle logs retrieved")
}

// shellQuote wraps s in single quotes for safe interpolation into an `sh -c`
// command string. Inside single quotes the shell treats everything literally,
// so the only character needing care is the single quote itself, which is
// closed, escaped and reopened.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func loadInstanceForObservability(w http.ResponseWriter, r *http.Request) (*structs.DatabaseInstance, bool) {
	id, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid database instance id")
		return nil, false
	}

	instance, err := query.GetDatabaseInstanceByID(db.DB, id)
	if err != nil {
		responder.NotFound(w)
		return nil, false
	}
	return instance, true
}

// HandleOpenDatabaseConsole authorises an interactive console session against a
// database instance and returns everything the browser terminal needs to open
// one over the admin WebSocket.
//
// The console reuses the existing container exec machinery, which addresses
// containers by name and worker — no separate transport, and no dependency on
// the instance having a row in the containers table.
//
// It defaults to the engine's SQL client rather than a bare shell: the point is
// to query the database, and a SQL session is far easier to reason about than
// unrestricted shell access to a container holding your data.
func (h *DatabaseHandler) HandleOpenDatabaseConsole(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid database instance id")
		return
	}

	instance, err := query.GetDatabaseInstanceByID(db.DB, id)
	if err != nil {
		responder.NotFound(w)
		return
	}

	if !h.WorkerHub.IsConnected(instance.WorkerID) {
		responder.SendError(w, http.StatusBadRequest, "worker is not connected")
		return
	}
	if instance.Status != string(structs.DBStatusRunning) {
		responder.SendError(w, http.StatusConflict,
			fmt.Sprintf("database is %s — a console can only be opened against a running instance", instance.Status))
		return
	}

	password := ""
	if instance.Password != nil {
		password = *instance.Password
	}

	// Credentials go in the exec argv rather than back to the browser, so the
	// client never handles them to open a session.
	var cmd []string
	switch instance.Engine {
	case "mysql", "mariadb":
		client := "mariadb"
		if instance.Engine == "mysql" {
			client = "mysql"
		}
		cmd = []string{client,
			"-u", instance.Username,
			"-p" + password,
			instance.DatabaseName,
		}
	case "postgres":
		// psql has no inline password flag, so it has to come from the
		// environment. That means a shell, which means the password is
		// interpolated into a command string — single-quoted and escaped so a
		// password containing quotes or semicolons can't break out of it.
		cmd = []string{"sh", "-c", fmt.Sprintf(
			"PGPASSWORD=%s exec psql -U %s -d %s",
			shellQuote(password), shellQuote(instance.Username), shellQuote(instance.DatabaseName),
		)}
	default:
		responder.SendError(w, http.StatusBadRequest, "unsupported engine for console access")
		return
	}

	dbEvent(id, structs.DBEventConsoleOpen,
		fmt.Sprintf("console opened against %s", instance.ContainerName), r)
	logAudit(r, "console", "database_instance", intPtr(id), strPtr(instance.Name))

	responder.New(w, map[string]any{
		"worker_id":      instance.WorkerID,
		"container_name": instance.ContainerName,
		"cmd":            cmd,
		"engine":         instance.Engine,
		"database_name":  instance.DatabaseName,
	}, "database console authorised")
}
