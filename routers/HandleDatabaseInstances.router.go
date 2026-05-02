package routers

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
	"github.com/aidenappl/lattice-api/socket"
	"github.com/aidenappl/lattice-api/tools"
	"github.com/gorilla/mux"
)

type DatabaseHandler struct {
	WorkerHub *socket.WorkerHub
	AdminHub  *socket.AdminHub
}

// HandleListDatabaseInstances returns all active database instances with optional filters.
func HandleListDatabaseInstances(w http.ResponseWriter, r *http.Request) {
	var req query.ListDatabaseInstancesRequest

	if v := r.URL.Query().Get("worker_id"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			req.WorkerID = &n
		}
	}
	if v := r.URL.Query().Get("engine"); v != "" {
		req.Engine = &v
	}
	if v := r.URL.Query().Get("status"); v != "" {
		req.Status = &v
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

	instances, total, err := query.ListDatabaseInstances(db.DB, req)
	if err != nil {
		responder.QueryError(w, err, "failed to list database instances")
		return
	}

	responder.NewWithCount(w, instances, total, "", "", "database instances retrieved")
}

// HandleGetDatabaseInstance returns a single database instance by ID.
func HandleGetDatabaseInstance(w http.ResponseWriter, r *http.Request) {
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

	responder.New(w, instance, "database instance retrieved")
}

// HandleCreateDatabaseInstance creates a new database instance and sends
// the db_create command to the target worker.
func (h *DatabaseHandler) HandleCreateDatabaseInstance(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name                string   `json:"name"`
		Engine              string   `json:"engine"`
		EngineVersion       string   `json:"engine_version"`
		WorkerID            int      `json:"worker_id"`
		Port                int      `json:"port"`
		RootPassword        string   `json:"root_password"`
		DatabaseName        string   `json:"database_name"`
		Username            string   `json:"username"`
		Password            string   `json:"password"`
		CPULimit            *float64 `json:"cpu_limit"`
		MemoryLimit         *int     `json:"memory_limit"`
		SnapshotSchedule    *string  `json:"snapshot_schedule"`
		RetentionCount      *int     `json:"retention_count"`
		BackupDestinationID *int     `json:"backup_destination_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		responder.BadBody(w, err)
		return
	}

	// Validate required fields
	if body.Name == "" {
		responder.MissingBodyFields(w, "name")
		return
	}
	if err := tools.ValidateName(body.Name); err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid name: "+err.Error())
		return
	}
	existing, _ := query.GetDatabaseInstanceByName(db.DB, body.Name)
	if existing != nil {
		responder.SendError(w, http.StatusConflict, "a database instance with that name already exists")
		return
	}
	if body.Engine == "" {
		responder.MissingBodyFields(w, "engine")
		return
	}
	if body.DatabaseName == "" {
		responder.MissingBodyFields(w, "database_name")
		return
	}
	if body.Username == "" {
		responder.MissingBodyFields(w, "username")
		return
	}
	if body.WorkerID == 0 {
		responder.MissingBodyFields(w, "worker_id")
		return
	}

	// Validate engine
	if body.Engine != "mysql" && body.Engine != "mariadb" && body.Engine != "postgres" {
		responder.SendError(w, http.StatusBadRequest, "engine must be one of: mysql, mariadb, postgres")
		return
	}

	// Default engine version
	if body.EngineVersion == "" {
		switch body.Engine {
		case "mysql":
			body.EngineVersion = "8"
		case "mariadb":
			body.EngineVersion = "11"
		case "postgres":
			body.EngineVersion = "16"
		}
	}

	// Default port
	if body.Port == 0 {
		switch body.Engine {
		case "mysql", "mariadb":
			body.Port = 3306
		case "postgres":
			body.Port = 5432
		}
	}

	containerName := "lattice-db-" + body.Name
	volumeName := "lattice-dbdata-" + body.Name

	// Generate random passwords if not provided
	if body.RootPassword == "" {
		b := make([]byte, 12)
		if _, err := rand.Read(b); err != nil {
			responder.SendError(w, http.StatusInternalServerError, "failed to generate root password")
			return
		}
		body.RootPassword = hex.EncodeToString(b)
	}
	if body.Password == "" {
		b := make([]byte, 12)
		if _, err := rand.Read(b); err != nil {
			responder.SendError(w, http.StatusInternalServerError, "failed to generate password")
			return
		}
		body.Password = hex.EncodeToString(b)
	}

	// Check worker is connected
	if !h.WorkerHub.IsConnected(body.WorkerID) {
		responder.SendError(w, http.StatusBadRequest, "worker is not connected")
		return
	}

	instance, err := query.CreateDatabaseInstance(db.DB, query.CreateDatabaseInstanceRequest{
		Name:                body.Name,
		Engine:              body.Engine,
		EngineVersion:       body.EngineVersion,
		WorkerID:            body.WorkerID,
		Port:                body.Port,
		RootPassword:        body.RootPassword,
		DatabaseName:        body.DatabaseName,
		Username:            body.Username,
		Password:            body.Password,
		CPULimit:            body.CPULimit,
		MemoryLimit:         body.MemoryLimit,
		SnapshotSchedule:    body.SnapshotSchedule,
		RetentionCount:      body.RetentionCount,
		BackupDestinationID: body.BackupDestinationID,
		ContainerName:       containerName,
		VolumeName:          volumeName,
	})
	if err != nil {
		responder.QueryError(w, err, "failed to create database instance")
		return
	}

	// Send db_create to worker
	payload := map[string]any{
		"database_instance_id": instance.ID,
		"container_name":       containerName,
		"volume_name":          volumeName,
		"engine":               body.Engine,
		"engine_version":       body.EngineVersion,
		"port":                 body.Port,
		"root_password":        body.RootPassword,
		"database_name":        body.DatabaseName,
		"username":             body.Username,
		"password":             body.Password,
	}
	if body.CPULimit != nil {
		payload["cpu_limit"] = *body.CPULimit
	}
	if body.MemoryLimit != nil {
		payload["memory_limit"] = *body.MemoryLimit
	}

	if err := h.WorkerHub.SendJSONToWorker(body.WorkerID, socket.Envelope{
		Type:    socket.MsgDbCreate,
		Payload: payload,
	}); err != nil {
		responder.SendError(w, http.StatusInternalServerError, fmt.Sprintf("failed to send db_create command: %v", err))
		return
	}

	logAudit(r, "create", "database_instance", intPtr(instance.ID), strPtr(instance.Name))
	responder.NewCreated(w, instance, "database instance created")
}

// HandleUpdateDatabaseInstance updates a database instance by ID.
func (h *DatabaseHandler) HandleUpdateDatabaseInstance(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid database instance id")
		return
	}

	var body struct {
		Name                *string  `json:"name"`
		Status              *string  `json:"status"`
		Port                *int     `json:"port"`
		RootPassword        *string  `json:"root_password"`
		Password            *string  `json:"password"`
		CPULimit            *float64 `json:"cpu_limit"`
		MemoryLimit         *int     `json:"memory_limit"`
		HealthStatus        *string  `json:"health_status"`
		SnapshotSchedule    *string  `json:"snapshot_schedule"`
		RetentionCount      *int     `json:"retention_count"`
		BackupDestinationID *int     `json:"backup_destination_id"`
		Active              *bool    `json:"active"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		responder.BadBody(w, err)
		return
	}

	instance, err := query.UpdateDatabaseInstance(db.DB, id, query.UpdateDatabaseInstanceRequest{
		Name:                body.Name,
		Status:              body.Status,
		Port:                body.Port,
		RootPassword:        body.RootPassword,
		Password:            body.Password,
		CPULimit:            body.CPULimit,
		MemoryLimit:         body.MemoryLimit,
		HealthStatus:        body.HealthStatus,
		SnapshotSchedule:    body.SnapshotSchedule,
		RetentionCount:      body.RetentionCount,
		BackupDestinationID: body.BackupDestinationID,
		Active:              body.Active,
	})
	if err != nil {
		responder.QueryError(w, err, "failed to update database instance")
		return
	}

	logAudit(r, "update", "database_instance", intPtr(id), nil)
	responder.New(w, instance, "database instance updated")
}

// HandleDeleteDatabaseInstance soft-deletes a database instance and sends
// db_remove to the worker if connected.
func (h *DatabaseHandler) HandleDeleteDatabaseInstance(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid database instance id")
		return
	}

	instance, err := query.GetDatabaseInstanceByID(db.DB, id)
	if err != nil {
		responder.QueryError(w, err, "failed to get database instance")
		return
	}

	// Best-effort: send remove command to the worker
	if h.WorkerHub.IsConnected(instance.WorkerID) {
		if err := h.WorkerHub.SendJSONToWorker(instance.WorkerID, socket.Envelope{
			Type: socket.MsgDbRemove,
			Payload: map[string]any{
				"container_name": instance.ContainerName,
				"volume_name":    instance.VolumeName,
			},
		}); err != nil {
			log.Printf("delete database instance %d: failed to send db_remove to worker %d: %v", id, instance.WorkerID, err)
		}
	}

	if err := query.DeleteDatabaseInstance(db.DB, id); err != nil {
		responder.QueryError(w, err, "failed to delete database instance")
		return
	}

	logAudit(r, "delete", "database_instance", intPtr(id), strPtr(instance.Name))
	responder.New(w, nil, "database instance deleted")
}

// HandleDatabaseAction dispatches start/stop/restart/remove actions for a
// database instance to the appropriate worker.
func (h *DatabaseHandler) HandleDatabaseAction(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid database instance id")
		return
	}

	// Extract the action from the last segment of the URL path.
	parts := strings.Split(strings.TrimSuffix(r.URL.Path, "/"), "/")
	action := parts[len(parts)-1]

	var msgType string
	switch action {
	case "start":
		msgType = socket.MsgDbStart
	case "stop":
		msgType = socket.MsgDbStop
	case "restart":
		msgType = socket.MsgDbRestart
	case "remove":
		msgType = socket.MsgDbRemove
	default:
		responder.SendError(w, http.StatusBadRequest, fmt.Sprintf("unknown action: %s", action))
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

	if err := h.WorkerHub.SendJSONToWorker(instance.WorkerID, socket.Envelope{
		Type: msgType,
		Payload: map[string]any{
			"container_name": instance.ContainerName,
		},
	}); err != nil {
		responder.SendError(w, http.StatusInternalServerError, fmt.Sprintf("failed to send %s command: %v", action, err))
		return
	}

	logAudit(r, action, "database_instance", intPtr(id), strPtr(instance.Name))
	responder.New(w, nil, fmt.Sprintf("database instance %s command sent", action))
}

// HandleGetDatabaseCredentials returns the connection credentials and
// connection string for a database instance.
func (h *DatabaseHandler) HandleGetDatabaseCredentials(w http.ResponseWriter, r *http.Request) {
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

	worker, err := query.GetWorkerByID(db.DB, instance.WorkerID)
	if err != nil {
		responder.QueryError(w, err, "failed to get worker")
		return
	}

	// Passwords are already decrypted by the query layer.
	rootPassword := ""
	if instance.RootPassword != nil {
		rootPassword = *instance.RootPassword
	}
	password := ""
	if instance.Password != nil {
		password = *instance.Password
	}

	// Build connection string based on engine.
	var connString string
	switch instance.Engine {
	case "mysql", "mariadb":
		connString = fmt.Sprintf("mysql://%s:%s@%s:%d/%s",
			url.QueryEscape(instance.Username), url.QueryEscape(password), worker.Hostname, instance.Port, instance.DatabaseName)
	case "postgres":
		connString = fmt.Sprintf("postgresql://%s:%s@%s:%d/%s",
			url.QueryEscape(instance.Username), url.QueryEscape(password), worker.Hostname, instance.Port, instance.DatabaseName)
	}

	responder.New(w, map[string]any{
		"root_password":     rootPassword,
		"username":          instance.Username,
		"password":          password,
		"connection_string": connString,
		"host":              worker.Hostname,
		"port":              instance.Port,
	}, "database credentials retrieved")
}
