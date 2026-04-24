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
		// Clear the deploy claim timestamp when leaving the deploying state
		if *req.Status != "deploying" {
			q = q.Set("deploy_claimed_at", nil)
		}
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

// ClaimStackForDeploy atomically transitions a stack to "deploying" if it is
// not already in that state, or if the existing claim is stale (older than 30
// minutes). Sets deploy_claimed_at to track when the claim was acquired.
// Returns true if the claim was acquired, false if the stack is already
// deploying with an active (non-stale) claim.
func ClaimStackForDeploy(engine db.Queryable, id int) (bool, error) {
	result, err := engine.Exec(
		`UPDATE stacks SET status = 'deploying', deploy_claimed_at = NOW()
		 WHERE id = ? AND active = 1
		   AND (status != 'deploying' OR deploy_claimed_at IS NULL OR deploy_claimed_at < NOW() - INTERVAL 30 MINUTE)`,
		id,
	)
	if err != nil {
		return false, fmt.Errorf("failed to claim stack for deploy: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return rows > 0, nil
}

// StackNameExists checks if an active stack with the given name exists,
// optionally excluding a specific stack ID (for updates/renames).
func StackNameExists(engine db.Queryable, name string, excludeID *int) (bool, error) {
	q := sq.Select("COUNT(*)").From("stacks").
		Where(sq.Eq{"name": name}).
		Where(sq.Eq{"active": true})
	if excludeID != nil {
		q = q.Where(sq.NotEq{"id": *excludeID})
	}
	qStr, args, err := q.ToSql()
	if err != nil {
		return false, err
	}
	var count int
	err = engine.QueryRow(qStr, args...).Scan(&count)
	return count > 0, err
}

// WorkerNameExists checks if an active worker with the given name exists,
// optionally excluding a specific worker ID.
func WorkerNameExists(engine db.Queryable, name string, excludeID *int) (bool, error) {
	q := sq.Select("COUNT(*)").From("workers").
		Where(sq.Eq{"name": name}).
		Where(sq.Eq{"active": true})
	if excludeID != nil {
		q = q.Where(sq.NotEq{"id": *excludeID})
	}
	qStr, args, err := q.ToSql()
	if err != nil {
		return false, err
	}
	var count int
	err = engine.QueryRow(qStr, args...).Scan(&count)
	return count > 0, err
}

// RegistryNameExists checks if an active registry with the given name exists,
// optionally excluding a specific registry ID.
func RegistryNameExists(engine db.Queryable, name string, excludeID *int) (bool, error) {
	q := sq.Select("COUNT(*)").From("registries").
		Where(sq.Eq{"name": name}).
		Where(sq.Eq{"active": true})
	if excludeID != nil {
		q = q.Where(sq.NotEq{"id": *excludeID})
	}
	qStr, args, err := q.ToSql()
	if err != nil {
		return false, err
	}
	var count int
	err = engine.QueryRow(qStr, args...).Scan(&count)
	return count > 0, err
}

func DeleteStack(tx db.Queryable, id int) error {
	// Soft-delete the stack's containers first to prevent orphaned active containers
	// with the same name from interfering with future stacks.
	if _, err := tx.Exec("UPDATE containers SET active = 0 WHERE stack_id = ?", id); err != nil {
		return fmt.Errorf("failed to deactivate containers for stack %d: %w", id, err)
	}
	_, err := tx.Exec("UPDATE stacks SET active = 0 WHERE id = ?", id)
	return err
}
