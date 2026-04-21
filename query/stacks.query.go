package query

import (
	"fmt"

	sq "github.com/Masterminds/squirrel"
	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/structs"
)

var stackColumns = []string{
	"stacks.id",
	"stacks.name",
	"stacks.description",
	"stacks.worker_id",
	"stacks.status",
	"stacks.deployment_strategy",
	"stacks.auto_deploy",
	"stacks.env_vars",
	"stacks.compose_yaml",
	"stacks.placement_constraints",
	"stacks.active",
	"stacks.updated_at",
	"stacks.inserted_at",
}

func scanStack(row scanner) (*structs.Stack, error) {
	var s structs.Stack
	err := row.Scan(
		&s.ID,
		&s.Name,
		&s.Description,
		&s.WorkerID,
		&s.Status,
		&s.DeploymentStrategy,
		&s.AutoDeploy,
		&s.EnvVars,
		&s.ComposeYAML,
		&s.PlacementConstraints,
		&s.Active,
		&s.UpdatedAt,
		&s.InsertedAt,
	)
	return &s, err
}

type ListStacksRequest struct {
	Limit    int
	Offset   int
	Active   *bool
	WorkerID *int
	Status   *string
}

func ListStacks(engine db.Queryable, req ListStacksRequest) (*[]structs.Stack, error) {
	q := sq.Select(stackColumns...).From("stacks")

	if req.Active != nil {
		q = q.Where(sq.Eq{"stacks.active": *req.Active})
	} else {
		q = q.Where(sq.Eq{"stacks.active": true})
	}
	if req.WorkerID != nil {
		q = q.Where(sq.Eq{"stacks.worker_id": *req.WorkerID})
	}
	if req.Status != nil {
		q = q.Where(sq.Eq{"stacks.status": *req.Status})
	}

	if req.Limit == 0 || req.Limit > db.MAX_LIMIT {
		req.Limit = db.DEFAULT_LIMIT
	}
	q = q.Limit(uint64(req.Limit))
	if req.Offset > 0 {
		q = q.Offset(uint64(req.Offset))
	}

	q = q.OrderBy("stacks.id DESC")

	qStr, args, err := q.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}

	rows, err := engine.Query(qStr, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to execute sql query: %w", err)
	}
	defer rows.Close()

	var stacks []structs.Stack
	for rows.Next() {
		s, err := scanStack(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan stack: %w", err)
		}
		stacks = append(stacks, *s)
	}

	return &stacks, rows.Err()
}

func GetStackByID(engine db.Queryable, id int) (*structs.Stack, error) {
	q := sq.Select(stackColumns...).From("stacks").Where(sq.Eq{"stacks.id": id, "stacks.active": true})

	qStr, args, err := q.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}

	row := engine.QueryRow(qStr, args...)
	s, err := scanStack(row)
	if err != nil {
		return nil, fmt.Errorf("failed to scan stack: %w", err)
	}

	return s, nil
}

type CreateStackRequest struct {
	Name               string
	Description        *string
	WorkerID           *int
	DeploymentStrategy string
	AutoDeploy         bool
	EnvVars              *string
	ComposeYAML          *string
	PlacementConstraints *string
}

func CreateStack(engine db.Queryable, req CreateStackRequest) (*structs.Stack, error) {
	q := sq.Insert("stacks").
		Columns("name", "description", "worker_id", "deployment_strategy", "auto_deploy", "env_vars", "compose_yaml", "placement_constraints").
		Values(req.Name, req.Description, req.WorkerID, req.DeploymentStrategy, req.AutoDeploy, req.EnvVars, req.ComposeYAML, req.PlacementConstraints)

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

	return GetStackByID(engine, int(id))
}

type UpdateStackRequest struct {
	Name               *string
	Description        *string
	WorkerID           *int
	Status             *string
	DeploymentStrategy *string
	AutoDeploy         *bool
	EnvVars              *string
	ComposeYAML          *string
	PlacementConstraints *string
	Active               *bool
}

func UpdateStack(engine db.Queryable, id int, req UpdateStackRequest) (*structs.Stack, error) {
	q := sq.Update("stacks").Where(sq.Eq{"id": id})

	hasUpdate := false
	if req.Name != nil {
		q = q.Set("name", *req.Name)
		hasUpdate = true
	}
	if req.Description != nil {
		q = q.Set("description", *req.Description)
		hasUpdate = true
	}
	if req.WorkerID != nil {
		if *req.WorkerID == 0 {
			q = q.Set("worker_id", nil)
		} else {
			q = q.Set("worker_id", *req.WorkerID)
		}
		hasUpdate = true
	}
	if req.Status != nil {
		q = q.Set("status", *req.Status)
		hasUpdate = true
	}
	if req.DeploymentStrategy != nil {
		q = q.Set("deployment_strategy", *req.DeploymentStrategy)
		hasUpdate = true
	}
	if req.AutoDeploy != nil {
		q = q.Set("auto_deploy", *req.AutoDeploy)
		hasUpdate = true
	}
	if req.EnvVars != nil {
		q = q.Set("env_vars", *req.EnvVars)
		hasUpdate = true
	}
	if req.ComposeYAML != nil {
		q = q.Set("compose_yaml", *req.ComposeYAML)
		hasUpdate = true
	}
	if req.PlacementConstraints != nil {
		q = q.Set("placement_constraints", *req.PlacementConstraints)
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

	return GetStackByID(engine, id)
}

func DeleteStack(engine db.Queryable, id int) error {
	// Soft-delete the stack's containers first to prevent orphaned active containers
	// with the same name from interfering with future stacks.
	if _, err := engine.Exec("UPDATE containers SET active = 0 WHERE stack_id = ?", id); err != nil {
		return fmt.Errorf("failed to deactivate containers for stack %d: %w", id, err)
	}
	_, err := engine.Exec("UPDATE stacks SET active = 0 WHERE id = ?", id)
	return err
}
