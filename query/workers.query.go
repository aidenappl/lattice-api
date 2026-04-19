package query

import (
	"fmt"
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/structs"
)

var workerColumns = []string{
	"workers.id",
	"workers.name",
	"workers.hostname",
	"workers.ip_address",
	"workers.status",
	"workers.os",
	"workers.arch",
	"workers.docker_version",
	"workers.runner_version",
	"workers.last_heartbeat_at",
	"workers.labels",
	"workers.active",
	"workers.updated_at",
	"workers.inserted_at",
}

func scanWorker(row scanner) (*structs.Worker, error) {
	var w structs.Worker
	err := row.Scan(
		&w.ID,
		&w.Name,
		&w.Hostname,
		&w.IPAddress,
		&w.Status,
		&w.OS,
		&w.Arch,
		&w.DockerVersion,
		&w.RunnerVersion,
		&w.LastHeartbeatAt,
		&w.Labels,
		&w.Active,
		&w.UpdatedAt,
		&w.InsertedAt,
	)
	return &w, err
}

type ListWorkersRequest struct {
	Limit  int
	Offset int
	Active *bool
	Status *string
}

func ListWorkers(engine db.Queryable, req ListWorkersRequest) (*[]structs.Worker, error) {
	q := sq.Select(workerColumns...).From("workers")

	if req.Active != nil {
		q = q.Where(sq.Eq{"workers.active": *req.Active})
	}
	if req.Status != nil {
		q = q.Where(sq.Eq{"workers.status": *req.Status})
	}

	if req.Limit == 0 || req.Limit > db.MAX_LIMIT {
		req.Limit = db.DEFAULT_LIMIT
	}
	q = q.Limit(uint64(req.Limit))
	if req.Offset > 0 {
		q = q.Offset(uint64(req.Offset))
	}

	q = q.OrderBy("workers.id DESC")

	qStr, args, err := q.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}

	rows, err := engine.Query(qStr, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to execute sql query: %w", err)
	}
	defer rows.Close()

	var workers []structs.Worker
	for rows.Next() {
		w, err := scanWorker(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan worker: %w", err)
		}
		workers = append(workers, *w)
	}

	return &workers, rows.Err()
}

func GetWorkerByID(engine db.Queryable, id int) (*structs.Worker, error) {
	q := sq.Select(workerColumns...).From("workers").Where(sq.Eq{"workers.id": id})

	qStr, args, err := q.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}

	row := engine.QueryRow(qStr, args...)
	w, err := scanWorker(row)
	if err != nil {
		return nil, fmt.Errorf("failed to scan worker: %w", err)
	}

	return w, nil
}

type CreateWorkerRequest struct {
	Name      string
	Hostname  string
	IPAddress *string
	Labels    *string
}

func CreateWorker(engine db.Queryable, req CreateWorkerRequest) (*structs.Worker, error) {
	q := sq.Insert("workers").
		Columns("name", "hostname", "ip_address", "labels").
		Values(req.Name, req.Hostname, req.IPAddress, req.Labels)

	qStr, args, err := q.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}

	result, err := engine.Exec(qStr, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to execute sql query: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("failed to get last insert id: %w", err)
	}

	return GetWorkerByID(engine, int(id))
}

type UpdateWorkerRequest struct {
	Name      *string
	Hostname  *string
	IPAddress *string
	Status    *string
	Labels    *string
	Active    *bool
}

func UpdateWorker(engine db.Queryable, id int, req UpdateWorkerRequest) (*structs.Worker, error) {
	q := sq.Update("workers").Where(sq.Eq{"id": id})

	hasUpdate := false
	if req.Name != nil {
		q = q.Set("name", *req.Name)
		hasUpdate = true
	}
	if req.Hostname != nil {
		q = q.Set("hostname", *req.Hostname)
		hasUpdate = true
	}
	if req.IPAddress != nil {
		q = q.Set("ip_address", *req.IPAddress)
		hasUpdate = true
	}
	if req.Status != nil {
		q = q.Set("status", *req.Status)
		hasUpdate = true
	}
	if req.Labels != nil {
		q = q.Set("labels", *req.Labels)
		hasUpdate = true
	}
	if req.Active != nil {
		q = q.Set("active", *req.Active)
		hasUpdate = true
	}

	if !hasUpdate {
		return nil, ErrNoChanges
	}

	qStr, args, err := q.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}

	_, err = engine.Exec(qStr, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to execute sql query: %w", err)
	}

	return GetWorkerByID(engine, id)
}

func DeleteWorker(engine db.Queryable, id int) error {
	_, err := engine.Exec("UPDATE workers SET active = 0 WHERE id = ?", id)
	return err
}

func UpdateWorkerHeartbeat(engine db.Queryable, workerID int, status string) error {
	_, err := engine.Exec(
		"UPDATE workers SET status = ?, last_heartbeat_at = ? WHERE id = ?",
		status, time.Now(), workerID,
	)
	return err
}

func UpdateWorkerRunnerVersion(engine db.Queryable, workerID int, runnerVersion string) error {
	_, err := engine.Exec(
		"UPDATE workers SET runner_version = ? WHERE id = ?",
		runnerVersion, workerID,
	)
	return err
}

func UpdateWorkerInfo(engine db.Queryable, workerID int, os, arch, dockerVersion, ipAddress, runnerVersion string) error {
	_, err := engine.Exec(
		"UPDATE workers SET os = ?, arch = ?, docker_version = ?, ip_address = ?, runner_version = ? WHERE id = ?",
		os, arch, dockerVersion, ipAddress, runnerVersion, workerID,
	)
	return err
}
