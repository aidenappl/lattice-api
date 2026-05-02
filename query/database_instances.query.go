package query

import (
	"fmt"
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/aidenappl/lattice-api/crypto"
	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/structs"
)

var databaseInstanceColumns = []string{
	"database_instances.id",
	"database_instances.name",
	"database_instances.engine",
	"database_instances.engine_version",
	"database_instances.worker_id",
	"database_instances.status",
	"database_instances.port",
	"database_instances.root_password",
	"database_instances.database_name",
	"database_instances.username",
	"database_instances.password",
	"database_instances.cpu_limit",
	"database_instances.memory_limit",
	"database_instances.health_status",
	"database_instances.snapshot_schedule",
	"database_instances.retention_count",
	"database_instances.backup_destination_id",
	"database_instances.container_name",
	"database_instances.volume_name",
	"database_instances.active",
	"database_instances.started_at",
	"database_instances.updated_at",
	"database_instances.inserted_at",
}

func scanDatabaseInstance(row scanner) (*structs.DatabaseInstance, error) {
	var d structs.DatabaseInstance
	err := row.Scan(
		&d.ID,
		&d.Name,
		&d.Engine,
		&d.EngineVersion,
		&d.WorkerID,
		&d.Status,
		&d.Port,
		&d.RootPassword,
		&d.DatabaseName,
		&d.Username,
		&d.Password,
		&d.CPULimit,
		&d.MemoryLimit,
		&d.HealthStatus,
		&d.SnapshotSchedule,
		&d.RetentionCount,
		&d.BackupDestinationID,
		&d.ContainerName,
		&d.VolumeName,
		&d.Active,
		&d.StartedAt,
		&d.UpdatedAt,
		&d.InsertedAt,
	)
	if err == nil && d.RootPassword != nil && *d.RootPassword != "" {
		decrypted, _ := crypto.Decrypt(*d.RootPassword)
		d.RootPassword = &decrypted
	}
	if err == nil && d.Password != nil && *d.Password != "" {
		decrypted, _ := crypto.Decrypt(*d.Password)
		d.Password = &decrypted
	}
	return &d, err
}

type ListDatabaseInstancesRequest struct {
	Limit    int
	Offset   int
	WorkerID *int
	Engine   *string
	Status   *string
}

func ListDatabaseInstances(engine db.Queryable, req ListDatabaseInstancesRequest) (*[]structs.DatabaseInstance, int, error) {
	q := sq.Select(databaseInstanceColumns...).
		From("database_instances").
		Where(sq.Eq{"database_instances.active": true}).
		OrderBy("database_instances.id DESC")

	if req.WorkerID != nil {
		q = q.Where(sq.Eq{"database_instances.worker_id": *req.WorkerID})
	}
	if req.Engine != nil {
		q = q.Where(sq.Eq{"database_instances.engine": *req.Engine})
	}
	if req.Status != nil {
		q = q.Where(sq.Eq{"database_instances.status": *req.Status})
	}

	// Count query (same filters, no limit/offset)
	countQ := sq.Select("COUNT(*)").
		From("database_instances").
		Where(sq.Eq{"database_instances.active": true})
	if req.WorkerID != nil {
		countQ = countQ.Where(sq.Eq{"database_instances.worker_id": *req.WorkerID})
	}
	if req.Engine != nil {
		countQ = countQ.Where(sq.Eq{"database_instances.engine": *req.Engine})
	}
	if req.Status != nil {
		countQ = countQ.Where(sq.Eq{"database_instances.status": *req.Status})
	}

	countStr, countArgs, err := countQ.ToSql()
	if err != nil {
		return nil, 0, fmt.Errorf("failed to build count query: %w", err)
	}

	var total int
	if err := engine.QueryRow(countStr, countArgs...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("failed to execute count query: %w", err)
	}

	if req.Limit == 0 || req.Limit > db.MAX_LIMIT {
		req.Limit = db.DEFAULT_LIMIT
	}
	q = q.Limit(uint64(req.Limit))
	if req.Offset > 0 {
		q = q.Offset(uint64(req.Offset))
	}

	qStr, args, err := q.ToSql()
	if err != nil {
		return nil, 0, fmt.Errorf("failed to build sql query: %w", err)
	}

	rows, err := engine.Query(qStr, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to execute sql query: %w", err)
	}
	defer rows.Close()

	var instances []structs.DatabaseInstance
	for rows.Next() {
		d, err := scanDatabaseInstance(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to scan database instance: %w", err)
		}
		instances = append(instances, *d)
	}

	return &instances, total, rows.Err()
}

func GetDatabaseInstanceByID(engine db.Queryable, id int) (*structs.DatabaseInstance, error) {
	q := sq.Select(databaseInstanceColumns...).From("database_instances").Where(sq.Eq{"database_instances.id": id}).Where(sq.Eq{"database_instances.active": true})

	qStr, args, err := q.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}

	row := engine.QueryRow(qStr, args...)
	d, err := scanDatabaseInstance(row)
	if err != nil {
		return nil, fmt.Errorf("failed to scan database instance: %w", err)
	}

	return d, nil
}

func GetDatabaseInstanceByName(engine db.Queryable, name string) (*structs.DatabaseInstance, error) {
	q := sq.Select(databaseInstanceColumns...).From("database_instances").
		Where(sq.Eq{"database_instances.name": name}).
		Where(sq.Eq{"database_instances.active": true}).
		Limit(1)

	qStr, args, err := q.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}

	row := engine.QueryRow(qStr, args...)
	d, err := scanDatabaseInstance(row)
	if err != nil {
		return nil, fmt.Errorf("failed to scan database instance: %w", err)
	}

	return d, nil
}

type CreateDatabaseInstanceRequest struct {
	Name                string
	Engine              string
	EngineVersion       string
	WorkerID            int
	Port                int
	RootPassword        string
	DatabaseName        string
	Username            string
	Password            string
	CPULimit            *float64
	MemoryLimit         *int
	SnapshotSchedule    *string
	RetentionCount      *int
	BackupDestinationID *int
	ContainerName       string
	VolumeName          string
}

func CreateDatabaseInstance(engine db.Queryable, req CreateDatabaseInstanceRequest) (*structs.DatabaseInstance, error) {
	encRootPassword := req.RootPassword
	if encRootPassword != "" {
		if encrypted, err := crypto.Encrypt(encRootPassword); err == nil {
			encRootPassword = encrypted
		}
	}

	encPassword := req.Password
	if encPassword != "" {
		if encrypted, err := crypto.Encrypt(encPassword); err == nil {
			encPassword = encrypted
		}
	}

	q := sq.Insert("database_instances").
		Columns("name", "engine", "engine_version", "worker_id", "port",
			"root_password", "database_name", "username", "password",
			"cpu_limit", "memory_limit", "snapshot_schedule", "retention_count",
			"backup_destination_id", "container_name", "volume_name").
		Values(req.Name, req.Engine, req.EngineVersion, req.WorkerID, req.Port,
			encRootPassword, req.DatabaseName, req.Username, encPassword,
			req.CPULimit, req.MemoryLimit, req.SnapshotSchedule, req.RetentionCount,
			req.BackupDestinationID, req.ContainerName, req.VolumeName)

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

	return GetDatabaseInstanceByID(engine, int(id))
}

type UpdateDatabaseInstanceRequest struct {
	Name                *string
	Status              *string
	Port                *int
	RootPassword        *string
	Password            *string
	CPULimit            *float64
	MemoryLimit         *int
	HealthStatus        *string
	SnapshotSchedule    *string
	RetentionCount      *int
	BackupDestinationID *int
	StartedAt           *time.Time
	Active              *bool
}

func UpdateDatabaseInstance(engine db.Queryable, id int, req UpdateDatabaseInstanceRequest) (*structs.DatabaseInstance, error) {
	q := sq.Update("database_instances").Where(sq.Eq{"id": id})

	hasUpdate := false
	if req.Name != nil {
		q = q.Set("name", *req.Name)
		hasUpdate = true
	}
	if req.Status != nil {
		q = q.Set("status", *req.Status)
		hasUpdate = true
	}
	if req.Port != nil {
		q = q.Set("port", *req.Port)
		hasUpdate = true
	}
	if req.RootPassword != nil {
		pw := *req.RootPassword
		if pw != "" {
			if encrypted, err := crypto.Encrypt(pw); err == nil {
				pw = encrypted
			}
		}
		q = q.Set("root_password", pw)
		hasUpdate = true
	}
	if req.Password != nil {
		pw := *req.Password
		if pw != "" {
			if encrypted, err := crypto.Encrypt(pw); err == nil {
				pw = encrypted
			}
		}
		q = q.Set("password", pw)
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
	if req.HealthStatus != nil {
		q = q.Set("health_status", *req.HealthStatus)
		hasUpdate = true
	}
	if req.SnapshotSchedule != nil {
		q = q.Set("snapshot_schedule", *req.SnapshotSchedule)
		hasUpdate = true
	}
	if req.RetentionCount != nil {
		q = q.Set("retention_count", *req.RetentionCount)
		hasUpdate = true
	}
	if req.BackupDestinationID != nil {
		q = q.Set("backup_destination_id", *req.BackupDestinationID)
		hasUpdate = true
	}
	if req.StartedAt != nil {
		q = q.Set("started_at", *req.StartedAt)
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

	return GetDatabaseInstanceByID(engine, id)
}

func DeleteDatabaseInstance(engine db.Queryable, id int) error {
	_, err := engine.Exec("UPDATE database_instances SET active = 0 WHERE id = ?", id)
	return err
}
