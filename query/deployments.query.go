package query

import (
	"fmt"
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/structs"
)

var deploymentColumns = []string{
	"deployments.id",
	"deployments.stack_id",
	"deployments.status",
	"deployments.strategy",
	"deployments.triggered_by",
	"deployments.approved_by",
	"deployments.started_at",
	"deployments.completed_at",
	"deployments.updated_at",
	"deployments.inserted_at",
}

func scanDeployment(row scanner) (*structs.Deployment, error) {
	var d structs.Deployment
	err := row.Scan(
		&d.ID,
		&d.StackID,
		&d.Status,
		&d.Strategy,
		&d.TriggeredBy,
		&d.ApprovedBy,
		&d.StartedAt,
		&d.CompletedAt,
		&d.UpdatedAt,
		&d.InsertedAt,
	)
	return &d, err
}

type ListDeploymentsRequest struct {
	Limit   int
	Offset  int
	StackID *int
	Status  *string
}

func ListDeployments(engine db.Queryable, req ListDeploymentsRequest) (*[]structs.Deployment, error) {
	q := sq.Select(deploymentColumns...).From("deployments")

	if req.StackID != nil {
		q = q.Where(sq.Eq{"deployments.stack_id": *req.StackID})
	}
	if req.Status != nil {
		q = q.Where(sq.Eq{"deployments.status": *req.Status})
	}

	if req.Limit == 0 || req.Limit > db.MAX_LIMIT {
		req.Limit = db.DEFAULT_LIMIT
	}
	q = q.Limit(uint64(req.Limit))
	if req.Offset > 0 {
		q = q.Offset(uint64(req.Offset))
	}

	q = q.OrderBy("deployments.id DESC")

	qStr, args, err := q.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}

	rows, err := engine.Query(qStr, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to execute sql query: %w", err)
	}
	defer rows.Close()

	var deployments []structs.Deployment
	for rows.Next() {
		d, err := scanDeployment(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan deployment: %w", err)
		}
		deployments = append(deployments, *d)
	}

	return &deployments, rows.Err()
}

func GetDeploymentByID(engine db.Queryable, id int) (*structs.Deployment, error) {
	q := sq.Select(deploymentColumns...).From("deployments").Where(sq.Eq{"deployments.id": id})

	qStr, args, err := q.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}

	row := engine.QueryRow(qStr, args...)
	d, err := scanDeployment(row)
	if err != nil {
		return nil, fmt.Errorf("failed to scan deployment: %w", err)
	}

	return d, nil
}

type CreateDeploymentRequest struct {
	StackID     int
	Strategy    string
	TriggeredBy *int
}

func CreateDeployment(engine db.Queryable, req CreateDeploymentRequest) (*structs.Deployment, error) {
	q := sq.Insert("deployments").
		Columns("stack_id", "strategy", "triggered_by").
		Values(req.StackID, req.Strategy, req.TriggeredBy)

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

	return GetDeploymentByID(engine, int(id))
}

func UpdateDeploymentStatus(engine db.Queryable, id int, status string) error {
	q := sq.Update("deployments").Set("status", status).Where(sq.Eq{"id": id})

	if status == "deploying" {
		q = q.Set("started_at", time.Now())
	}
	if status == "deployed" || status == "failed" || status == "rolled_back" {
		q = q.Set("completed_at", time.Now())
	}

	qStr, args, err := q.ToSql()
	if err != nil {
		return fmt.Errorf("failed to build sql query: %w", err)
	}

	_, err = engine.Exec(qStr, args...)
	return err
}

func ApproveDeployment(engine db.Queryable, id int, approvedBy int) error {
	_, err := engine.Exec(
		"UPDATE deployments SET status = 'approved', approved_by = ? WHERE id = ? AND status = 'pending'",
		approvedBy, id,
	)
	return err
}

// Deployment Logs

type CreateDeploymentLogRequest struct {
	DeploymentID int
	Level        string
	Stage        *string
	Message      string
}

func CreateDeploymentLog(engine db.Queryable, req CreateDeploymentLogRequest) error {
	level := req.Level
	if level == "" {
		level = "info"
	}
	_, err := engine.Exec(
		"INSERT INTO deployment_logs (deployment_id, level, stage, message) VALUES (?, ?, ?, ?)",
		req.DeploymentID, level, req.Stage, req.Message,
	)
	return err
}

func ListDeploymentLogs(engine db.Queryable, deploymentID int) (*[]structs.DeploymentLog, error) {
	rows, err := engine.Query(
		"SELECT id, deployment_id, level, stage, message, recorded_at FROM deployment_logs WHERE deployment_id = ? ORDER BY id ASC",
		deploymentID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query deployment logs: %w", err)
	}
	defer rows.Close()

	var logs []structs.DeploymentLog
	for rows.Next() {
		var l structs.DeploymentLog
		if err := rows.Scan(&l.ID, &l.DeploymentID, &l.Level, &l.Stage, &l.Message, &l.RecordedAt); err != nil {
			return nil, fmt.Errorf("failed to scan deployment log: %w", err)
		}
		logs = append(logs, l)
	}
	return &logs, rows.Err()
}
