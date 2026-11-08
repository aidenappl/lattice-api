package query

import (
	"fmt"
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/structs"
)

var logColumns = []string{
	"container_logs.id",
	"container_logs.container_id",
	"container_logs.container_name",
	"container_logs.worker_id",
	"container_logs.stream",
	"container_logs.message",
	"container_logs.recorded_at",
}

func scanLog(row scanner) (*structs.ContainerLog, error) {
	var l structs.ContainerLog
	err := row.Scan(
		&l.ID,
		&l.ContainerID,
		&l.ContainerName,
		&l.WorkerID,
		&l.Stream,
		&l.Message,
		&l.RecordedAt,
	)
	return &l, err
}

type ListLogsRequest struct {
	Limit         int
	Offset        int
	WorkerID      *int
	ContainerID   *int
	ContainerName *string // fallback: also match logs where container_id IS NULL but name matches
	Stream        *string
}

func ListContainerLogs(engine db.Queryable, req ListLogsRequest) (*[]structs.ContainerLog, error) {
	qStr, args, err := listContainerLogsQuery(req)
	if err != nil {
		return nil, err
	}

	rows, err := engine.Query(qStr, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to execute sql query: %w", err)
	}
	defer rows.Close()

	var logs []structs.ContainerLog
	for rows.Next() {
		l, err := scanLog(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan log: %w", err)
		}
		logs = append(logs, *l)
	}

	return &logs, rows.Err()
}

// listContainerLogsQuery builds the logs query. Matching by container ID
// with the name fallback is a UNION ALL of two branches rather than one OR:
// with the OR, MariaDB could use no per-container index and walked
// idx_cl_recorded backwards over every container's logs, which took over 10s
// for a quiet container (APL-113). Each branch is served by its own index
// (uq_container_log_dedup on container_id, idx_cl_name_recorded on
// container_name) and
// is limited to limit+offset rows, so the outer sort only sees those.
func listContainerLogsQuery(req ListLogsRequest) (string, []any, error) {
	if req.Limit == 0 || req.Limit > db.MAX_LIMIT {
		req.Limit = db.DEFAULT_LIMIT
	}
	if req.Offset < 0 {
		req.Offset = 0
	}

	branch := func(where sq.Sqlizer) sq.SelectBuilder {
		q := sq.Select(logColumns...).From("container_logs").Where(where)
		if req.WorkerID != nil {
			q = q.Where(sq.Eq{"container_logs.worker_id": *req.WorkerID})
		}
		if req.Stream != nil {
			q = q.Where(sq.Eq{"container_logs.stream": *req.Stream})
		}
		return q
	}

	if req.ContainerID != nil && req.ContainerName != nil {
		// The second branch catches logs stored before the container name
		// could be resolved to an ID.
		window := uint64(req.Limit + req.Offset)
		byID, idArgs, err := branch(sq.Eq{"container_logs.container_id": *req.ContainerID}).
			OrderBy("container_logs.recorded_at DESC").Limit(window).ToSql()
		if err != nil {
			return "", nil, fmt.Errorf("failed to build sql query: %w", err)
		}
		byName, nameArgs, err := branch(sq.And{
			sq.Expr("container_logs.container_id IS NULL"),
			sq.Eq{"container_logs.container_name": *req.ContainerName},
		}).OrderBy("container_logs.recorded_at DESC").Limit(window).ToSql()
		if err != nil {
			return "", nil, fmt.Errorf("failed to build sql query: %w", err)
		}
		qStr := "SELECT * FROM ((" + byID + ") UNION ALL (" + byName + ")) AS container_logs ORDER BY container_logs.recorded_at DESC LIMIT ?"
		args := append(append(idArgs, nameArgs...), req.Limit)
		if req.Offset > 0 {
			qStr += " OFFSET ?"
			args = append(args, req.Offset)
		}
		return qStr, args, nil
	}

	var where sq.Sqlizer = sq.And{}
	if req.ContainerID != nil {
		where = sq.Eq{"container_logs.container_id": *req.ContainerID}
	} else if req.ContainerName != nil {
		// No resolved ID but a name was given — filter by name so we don't
		// return every container's logs.
		where = sq.Eq{"container_logs.container_name": *req.ContainerName}
	}
	q := branch(where).OrderBy("container_logs.recorded_at DESC").Limit(uint64(req.Limit))
	if req.Offset > 0 {
		q = q.Offset(uint64(req.Offset))
	}
	qStr, args, err := q.ToSql()
	if err != nil {
		return "", nil, fmt.Errorf("failed to build sql query: %w", err)
	}
	return qStr, args, nil
}

type CreateContainerLogRequest struct {
	ContainerID   *int
	ContainerName *string
	WorkerID      int
	Stream        string
	Message       string
	// RecordedAt is the Docker-recorded timestamp from the log line. When set
	// it is stored verbatim so that reconnect replays of the same line produce
	// an identical recorded_at and can be discarded by the unique index.
	RecordedAt *time.Time
}

func CreateContainerLog(engine db.Queryable, req CreateContainerLogRequest) error {
	cols := []string{"container_id", "container_name", "worker_id", "stream", "message"}
	vals := []any{req.ContainerID, req.ContainerName, req.WorkerID, req.Stream, req.Message}

	if req.RecordedAt != nil {
		cols = append(cols, "recorded_at")
		// MariaDB DATETIME(6) stores microseconds; format accordingly.
		vals = append(vals, req.RecordedAt.UTC().Format("2006-01-02 15:04:05.999999"))
	}

	// ON DUPLICATE KEY UPDATE id = id makes a replay of the same log line (unique
	// index on (container_id, recorded_at, message)) a no-op, while still
	// surfacing real errors — unlike INSERT IGNORE, which swallows every error
	// (truncation, FK, etc.), not just duplicate-key.
	q := sq.Insert("container_logs").Columns(cols...).Values(vals...).
		Suffix("ON DUPLICATE KEY UPDATE id = id")

	qStr, args, err := q.ToSql()
	if err != nil {
		return fmt.Errorf("failed to build sql query: %w", err)
	}

	_, err = engine.Exec(qStr, args...)
	return err
}

// CreateContainerLogs stores a batch of log lines in one multi-row INSERT —
// one transaction and one commit instead of one per line. ON DUPLICATE KEY
// UPDATE applies per row, so replayed lines are still dropped by the unique
// index exactly as in CreateContainerLog.
func CreateContainerLogs(engine db.Queryable, reqs []CreateContainerLogRequest) error {
	if len(reqs) == 0 {
		return nil
	}
	qStr, args, err := containerLogsInsert(reqs)
	if err != nil {
		return err
	}
	_, err = engine.Exec(qStr, args...)
	return err
}

// containerLogsInsert builds the batch INSERT. Every row has the same columns;
// a line without a runner timestamp gets CURRENT_TIMESTAMP(6), which is what
// the column default gave the single-row insert.
func containerLogsInsert(reqs []CreateContainerLogRequest) (string, []any, error) {
	q := sq.Insert("container_logs").
		Columns("container_id", "container_name", "worker_id", "stream", "message", "recorded_at")
	for _, r := range reqs {
		var recordedAt any = sq.Expr("CURRENT_TIMESTAMP(6)")
		if r.RecordedAt != nil {
			recordedAt = r.RecordedAt.UTC().Format("2006-01-02 15:04:05.999999")
		}
		q = q.Values(r.ContainerID, r.ContainerName, r.WorkerID, r.Stream, r.Message, recordedAt)
	}
	qStr, args, err := q.Suffix("ON DUPLICATE KEY UPDATE id = id").ToSql()
	if err != nil {
		return "", nil, fmt.Errorf("failed to build sql query: %w", err)
	}
	return qStr, args, nil
}
