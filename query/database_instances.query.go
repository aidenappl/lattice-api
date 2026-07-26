package query

import (
	"encoding/json"
	"fmt"
	"strconv"
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
	"database_instances.last_error",
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
	var lastError *string
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
		&lastError,
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
		decrypted, decErr := crypto.Decrypt(*d.RootPassword)
		if decErr != nil {
			return &d, fmt.Errorf("failed to decrypt database root password: %w", decErr)
		}
		d.RootPassword = &decrypted
	}
	if err == nil && d.Password != nil && *d.Password != "" {
		decrypted, decErr := crypto.Decrypt(*d.Password)
		if decErr != nil {
			return &d, fmt.Errorf("failed to decrypt database password: %w", decErr)
		}
		d.Password = &decrypted
	}
	// A malformed last_error must never make an instance unreadable — the whole
	// point of the column is to explain a broken instance.
	if err == nil && lastError != nil && *lastError != "" {
		var de structs.DatabaseError
		if jsonErr := json.Unmarshal([]byte(*lastError), &de); jsonErr == nil {
			d.LastError = &de
		} else {
			d.LastError = &structs.DatabaseError{
				Code:    "unparsable_error",
				Message: *lastError,
			}
		}
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
		if isNoRows(err) {
			return nil, ErrNotFound
		}
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
		encrypted, err := crypto.Encrypt(encRootPassword)
		if err != nil {
			return nil, fmt.Errorf("failed to encrypt database root password: %w", err)
		}
		encRootPassword = encrypted
	}

	encPassword := req.Password
	if encPassword != "" {
		encrypted, err := crypto.Encrypt(encPassword)
		if err != nil {
			return nil, fmt.Errorf("failed to encrypt database password: %w", err)
		}
		encPassword = encrypted
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

	// LastError sets the structured failure detail. ClearLastError wins if both
	// are set — recovery should never leave a stale error behind.
	LastError      *structs.DatabaseError
	ClearLastError bool
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
			encrypted, err := crypto.Encrypt(pw)
			if err != nil {
				return nil, fmt.Errorf("failed to encrypt database root password: %w", err)
			}
			pw = encrypted
		}
		q = q.Set("root_password", pw)
		hasUpdate = true
	}
	if req.Password != nil {
		pw := *req.Password
		if pw != "" {
			encrypted, err := crypto.Encrypt(pw)
			if err != nil {
				return nil, fmt.Errorf("failed to encrypt database password: %w", err)
			}
			pw = encrypted
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
	if req.ClearLastError {
		q = q.Set("last_error", nil)
		hasUpdate = true
	} else if req.LastError != nil {
		encoded, err := json.Marshal(req.LastError)
		if err != nil {
			return nil, fmt.Errorf("failed to encode last error: %w", err)
		}
		q = q.Set("last_error", string(encoded))
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

// ListDatabaseInstancesByWorker returns every active instance on a worker with
// no pagination. The reconciler needs the complete set to diff desired against
// observed state — a paginated view would silently ignore drift past the limit.
func ListDatabaseInstancesByWorker(engine db.Queryable, workerID int) ([]structs.DatabaseInstance, error) {
	q := sq.Select(databaseInstanceColumns...).
		From("database_instances").
		Where(sq.Eq{"database_instances.worker_id": workerID}).
		Where(sq.Eq{"database_instances.active": true}).
		OrderBy("database_instances.id ASC")

	qStr, args, err := q.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}

	rows, err := engine.Query(qStr, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to execute sql query: %w", err)
	}
	defer rows.Close()

	var instances []structs.DatabaseInstance
	for rows.Next() {
		d, err := scanDatabaseInstance(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan database instance: %w", err)
		}
		instances = append(instances, *d)
	}

	return instances, rows.Err()
}

// ListStuckDatabaseInstances returns active instances that have sat in a
// transitional status since before cutoff. These are what the watchdog fails
// out — an instance stuck in pending forever is the exact failure this whole
// subsystem was missing.
func ListStuckDatabaseInstances(engine db.Queryable, cutoff time.Time) ([]structs.DatabaseInstance, error) {
	transitional := []string{
		string(structs.DBStatusPending),
		string(structs.DBStatusProvisioning),
		string(structs.DBStatusRestarting),
		string(structs.DBStatusDeleting),
	}

	q := sq.Select(databaseInstanceColumns...).
		From("database_instances").
		Where(sq.Eq{"database_instances.active": true}).
		Where(sq.Eq{"database_instances.status": transitional}).
		Where(sq.Lt{"database_instances.updated_at": cutoff}).
		OrderBy("database_instances.id ASC")

	qStr, args, err := q.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}

	rows, err := engine.Query(qStr, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to execute sql query: %w", err)
	}
	defer rows.Close()

	var instances []structs.DatabaseInstance
	for rows.Next() {
		d, err := scanDatabaseInstance(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan database instance: %w", err)
		}
		instances = append(instances, *d)
	}

	return instances, rows.Err()
}

// PortConflict describes what already holds a host port on a worker.
type PortConflict struct {
	Kind string `json:"kind"` // "database" or "container"
	ID   int    `json:"id"`
	Name string `json:"name"`
	Port int    `json:"port"`
}

// FindPortConflict reports whether port is already claimed on workerID, by
// either another database instance or a stack container's published port.
// excludeInstanceID lets an update re-assert its own port without conflicting
// with itself; pass 0 when creating.
//
// This is the allocation ledger check. It is not sufficient on its own — a
// foreign process outside Lattice can hold the port, and there is a race
// between this check and the container binding — so the runner also attempts a
// real bind before pulling the image.
func FindPortConflict(engine db.Queryable, workerID, port, excludeInstanceID int) (*PortConflict, error) {
	dbQ := sq.Select("database_instances.id", "database_instances.name").
		From("database_instances").
		Where(sq.Eq{"database_instances.worker_id": workerID}).
		Where(sq.Eq{"database_instances.port": port}).
		Where(sq.Eq{"database_instances.active": true}).
		Limit(1)
	if excludeInstanceID > 0 {
		dbQ = dbQ.Where(sq.NotEq{"database_instances.id": excludeInstanceID})
	}

	qStr, args, err := dbQ.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}

	var id int
	var name string
	err = engine.QueryRow(qStr, args...).Scan(&id, &name)
	if err == nil {
		return &PortConflict{Kind: "database", ID: id, Name: name, Port: port}, nil
	}
	if !isNoRows(err) {
		return nil, fmt.Errorf("failed to check database port conflict: %w", err)
	}

	// Stack containers publish ports too. containers has no worker_id of its
	// own — placement is resolved through the owning stack.
	cQ := sq.Select("containers.id", "containers.name", "containers.port_mappings").
		From("containers").
		Join("stacks ON stacks.id = containers.stack_id").
		Where(sq.Eq{"stacks.worker_id": workerID}).
		Where(sq.Eq{"stacks.active": true}).
		Where(sq.Eq{"containers.active": true}).
		Where(sq.NotEq{"containers.port_mappings": nil})

	cStr, cArgs, err := cQ.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}

	rows, err := engine.Query(cStr, cArgs...)
	if err != nil {
		return nil, fmt.Errorf("failed to check container port conflict: %w", err)
	}
	defer rows.Close()

	target := fmt.Sprintf("%d", port)
	for rows.Next() {
		var cID int
		var cName string
		var mappings *string
		if err := rows.Scan(&cID, &cName, &mappings); err != nil {
			return nil, fmt.Errorf("failed to scan container port mappings: %w", err)
		}
		if mappings == nil || *mappings == "" {
			continue
		}
		var parsed []struct {
			HostPort string `json:"host_port"`
		}
		if err := json.Unmarshal([]byte(*mappings), &parsed); err != nil {
			// A container with unparsable mappings shouldn't block provisioning.
			continue
		}
		for _, m := range parsed {
			if m.HostPort == target {
				return &PortConflict{Kind: "container", ID: cID, Name: cName, Port: port}, nil
			}
		}
	}

	return nil, rows.Err()
}

// Host port range Lattice allocates managed database ports from. Chosen to sit
// below the Linux ephemeral range (net.ipv4.ip_local_port_range, typically
// 32768-60999) so an allocated port can't be transiently claimed by an
// unrelated outbound connection. Same reasoning as Nomad's 20000-32000.
const (
	DB_PORT_RANGE_MIN = 20000
	DB_PORT_RANGE_MAX = 29999
)

// AllocateDatabasePort finds the lowest free host port on a worker within the
// managed range. Returns ErrNoFreePort when the range is exhausted.
//
// Claimed ports are gathered in two queries rather than probing each candidate
// individually — the range is 10,000 wide.
func AllocateDatabasePort(engine db.Queryable, workerID int) (int, error) {
	claimed, err := ClaimedPortsOnWorker(engine, workerID)
	if err != nil {
		return 0, err
	}
	for port := DB_PORT_RANGE_MIN; port <= DB_PORT_RANGE_MAX; port++ {
		if _, taken := claimed[port]; !taken {
			return port, nil
		}
	}
	return 0, ErrNoFreePort
}

// ClaimedPortsOnWorker returns every host port currently claimed on a worker by
// an active database instance or an active stack container.
func ClaimedPortsOnWorker(engine db.Queryable, workerID int) (map[int]PortConflict, error) {
	claimed := map[int]PortConflict{}

	dbQ := sq.Select("database_instances.id", "database_instances.name", "database_instances.port").
		From("database_instances").
		Where(sq.Eq{"database_instances.worker_id": workerID}).
		Where(sq.Eq{"database_instances.active": true})

	qStr, args, err := dbQ.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}
	rows, err := engine.Query(qStr, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list claimed database ports: %w", err)
	}
	for rows.Next() {
		var c PortConflict
		if err := rows.Scan(&c.ID, &c.Name, &c.Port); err != nil {
			rows.Close()
			return nil, fmt.Errorf("failed to scan claimed database port: %w", err)
		}
		c.Kind = "database"
		claimed[c.Port] = c
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	cQ := sq.Select("containers.id", "containers.name", "containers.port_mappings").
		From("containers").
		Join("stacks ON stacks.id = containers.stack_id").
		Where(sq.Eq{"stacks.worker_id": workerID}).
		Where(sq.Eq{"stacks.active": true}).
		Where(sq.Eq{"containers.active": true}).
		Where(sq.NotEq{"containers.port_mappings": nil})

	cStr, cArgs, err := cQ.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}
	cRows, err := engine.Query(cStr, cArgs...)
	if err != nil {
		return nil, fmt.Errorf("failed to list claimed container ports: %w", err)
	}
	defer cRows.Close()

	for cRows.Next() {
		var cID int
		var cName string
		var mappings *string
		if err := cRows.Scan(&cID, &cName, &mappings); err != nil {
			return nil, fmt.Errorf("failed to scan container port mappings: %w", err)
		}
		if mappings == nil || *mappings == "" {
			continue
		}
		var parsed []struct {
			HostPort string `json:"host_port"`
		}
		if err := json.Unmarshal([]byte(*mappings), &parsed); err != nil {
			continue
		}
		for _, m := range parsed {
			p, convErr := strconv.Atoi(m.HostPort)
			if convErr != nil {
				continue
			}
			// A database claim on the same port is the more actionable
			// conflict to report, so don't let a container overwrite it.
			if _, exists := claimed[p]; exists {
				continue
			}
			claimed[p] = PortConflict{Kind: "container", ID: cID, Name: cName, Port: p}
		}
	}

	return claimed, cRows.Err()
}
