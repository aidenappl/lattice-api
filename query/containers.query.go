package query

import (
	"fmt"

	sq "github.com/Masterminds/squirrel"
	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/structs"
)

var containerColumns = []string{
	"containers.id",
	"containers.stack_id",
	"containers.name",
	"containers.image",
	"containers.tag",
	"containers.status",
	"containers.port_mappings",
	"containers.env_vars",
	"containers.volumes",
	"containers.cpu_limit",
	"containers.memory_limit",
	"containers.replicas",
	"containers.restart_policy",
	"containers.command",
	"containers.entrypoint",
	"containers.health_check",
	"containers.health_status",
	"containers.registry_id",
	"containers.active",
	"containers.updated_at",
	"containers.inserted_at",
}

func scanContainer(row scanner) (*structs.Container, error) {
	var c structs.Container
	err := row.Scan(
		&c.ID,
		&c.StackID,
		&c.Name,
		&c.Image,
		&c.Tag,
		&c.Status,
		&c.PortMappings,
		&c.EnvVars,
		&c.Volumes,
		&c.CPULimit,
		&c.MemoryLimit,
		&c.Replicas,
		&c.RestartPolicy,
		&c.Command,
		&c.Entrypoint,
		&c.HealthCheck,
		&c.HealthStatus,
		&c.RegistryID,
		&c.Active,
		&c.UpdatedAt,
		&c.InsertedAt,
	)
	return &c, err
}

func ListContainersByStack(engine db.Queryable, stackID int) (*[]structs.Container, error) {
	q := sq.Select(containerColumns...).
		From("containers").
		Where(sq.Eq{"containers.stack_id": stackID}).
		Where(sq.Eq{"containers.active": true}).
		OrderBy("containers.id ASC")

	qStr, args, err := q.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}

	rows, err := engine.Query(qStr, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to execute sql query: %w", err)
	}
	defer rows.Close()

	var containers []structs.Container
	for rows.Next() {
		c, err := scanContainer(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan container: %w", err)
		}
		containers = append(containers, *c)
	}

	return &containers, rows.Err()
}

// ListAllContainers returns all active containers from active stacks, optionally filtered by stackID or workerID
// (workerID is resolved through the stack→worker relationship).
func ListAllContainers(engine db.Queryable, stackID *int, workerID *int) (*[]structs.Container, error) {
	q := sq.Select(containerColumns...).
		From("containers").
		Join("stacks ON stacks.id = containers.stack_id").
		Where(sq.Eq{"containers.active": true}).
		Where(sq.Eq{"stacks.active": true}).
		OrderBy("containers.stack_id ASC, containers.id ASC")

	if stackID != nil {
		q = q.Where(sq.Eq{"containers.stack_id": *stackID})
	}
	if workerID != nil {
		// stacks is already joined above; just filter by worker_id
		q = q.Where(sq.Eq{"stacks.worker_id": *workerID})
	}

	qStr, args, err := q.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}

	rows, err := engine.Query(qStr, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to execute sql query: %w", err)
	}
	defer rows.Close()

	var containers []structs.Container
	for rows.Next() {
		c, err := scanContainer(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan container: %w", err)
		}
		containers = append(containers, *c)
	}

	return &containers, rows.Err()
}

func GetContainerByID(engine db.Queryable, id int) (*structs.Container, error) {
	q := sq.Select(containerColumns...).From("containers").Where(sq.Eq{"containers.id": id})

	qStr, args, err := q.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}

	row := engine.QueryRow(qStr, args...)
	c, err := scanContainer(row)
	if err != nil {
		return nil, fmt.Errorf("failed to scan container: %w", err)
	}

	return c, nil
}

type CreateContainerRequest struct {
	StackID       int
	Name          string
	Image         string
	Tag           string
	PortMappings  *string
	EnvVars       *string
	Volumes       *string
	CPULimit      *float64
	MemoryLimit   *int
	Replicas      int
	RestartPolicy *string
	Command       *string
	Entrypoint    *string
	HealthCheck   *string
	RegistryID    *int
}

func CreateContainer(engine db.Queryable, req CreateContainerRequest) (*structs.Container, error) {
	q := sq.Insert("containers").
		Columns("stack_id", "name", "image", "tag", "port_mappings", "env_vars", "volumes",
			"cpu_limit", "memory_limit", "replicas", "restart_policy", "command", "entrypoint", "health_check", "registry_id").
		Values(req.StackID, req.Name, req.Image, req.Tag, req.PortMappings, req.EnvVars, req.Volumes,
			req.CPULimit, req.MemoryLimit, req.Replicas, req.RestartPolicy, req.Command, req.Entrypoint, req.HealthCheck, req.RegistryID)

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

	return GetContainerByID(engine, int(id))
}

type UpdateContainerRequest struct {
	Name          *string
	Image         *string
	Tag           *string
	Status        *string
	PortMappings  *string
	EnvVars       *string
	Volumes       *string
	CPULimit      *float64
	MemoryLimit   *int
	Replicas      *int
	RestartPolicy *string
	Command       *string
	Entrypoint    *string
	HealthCheck   *string
	HealthStatus  *string
	RegistryID    *int
	Active        *bool
}

func UpdateContainer(engine db.Queryable, id int, req UpdateContainerRequest) (*structs.Container, error) {
	q := sq.Update("containers").Where(sq.Eq{"id": id})

	hasUpdate := false
	if req.Name != nil {
		q = q.Set("name", *req.Name)
		hasUpdate = true
	}
	if req.Image != nil {
		q = q.Set("image", *req.Image)
		hasUpdate = true
	}
	if req.Tag != nil {
		q = q.Set("tag", *req.Tag)
		hasUpdate = true
	}
	if req.Status != nil {
		q = q.Set("status", *req.Status)
		hasUpdate = true
	}
	if req.PortMappings != nil {
		q = q.Set("port_mappings", *req.PortMappings)
		hasUpdate = true
	}
	if req.EnvVars != nil {
		q = q.Set("env_vars", *req.EnvVars)
		hasUpdate = true
	}
	if req.Volumes != nil {
		q = q.Set("volumes", *req.Volumes)
		hasUpdate = true
	}
	if req.CPULimit != nil {
		q = q.Set("cpu_limit", *req.CPULimit)
		hasUpdate = true
	}
	if req.MemoryLimit != nil {
		q = q.Set("memory_limit", *req.MemoryLimit)
		hasUpdate = true
	}
	if req.Replicas != nil {
		q = q.Set("replicas", *req.Replicas)
		hasUpdate = true
	}
	if req.RestartPolicy != nil {
		q = q.Set("restart_policy", *req.RestartPolicy)
		hasUpdate = true
	}
	if req.Command != nil {
		q = q.Set("command", *req.Command)
		hasUpdate = true
	}
	if req.Entrypoint != nil {
		q = q.Set("entrypoint", *req.Entrypoint)
		hasUpdate = true
	}
	if req.HealthCheck != nil {
		q = q.Set("health_check", *req.HealthCheck)
		hasUpdate = true
	}
	if req.HealthStatus != nil {
		q = q.Set("health_status", *req.HealthStatus)
		hasUpdate = true
	}
	if req.RegistryID != nil {
		q = q.Set("registry_id", *req.RegistryID)
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

	return GetContainerByID(engine, id)
}

func DeleteContainer(engine db.Queryable, id int) error {
	_, err := engine.Exec("UPDATE containers SET active = 0 WHERE id = ?", id)
	return err
}

func GetContainerByName(engine db.Queryable, name string) (*structs.Container, error) {
	q := sq.Select(containerColumns...).From("containers").
		Where(sq.Eq{"containers.name": name}).
		Where(sq.Eq{"containers.active": true}).
		OrderBy("containers.id DESC").
		Limit(1)

	qStr, args, err := q.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}

	row := engine.QueryRow(qStr, args...)
	c, err := scanContainer(row)
	if err != nil {
		return nil, fmt.Errorf("failed to scan container: %w", err)
	}

	return c, nil
}
