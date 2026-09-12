package query

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/structs"
)

var automationRunColumns = []string{
	"automation_runs.id",
	"automation_runs.automation_id",
	"automation_runs.trigger_source",
	"automation_runs.trigger_detail",
	"automation_runs.scheduled_at",
	"automation_runs.status",
	"automation_runs.skip_reason",
	"automation_runs.failed_step",
	"automation_runs.error_message",
	"automation_runs.steps",
	"automation_runs.run_as_user_id",
	"automation_runs.triggered_by",
	"automation_runs.started_at",
	"automation_runs.finished_at",
	"automation_runs.inserted_at",
	"automation_runs.updated_at",
}

func scanAutomationRun(row scanner) (*structs.AutomationRun, error) {
	var r structs.AutomationRun
	var steps sql.NullString
	err := row.Scan(
		&r.ID,
		&r.AutomationID,
		&r.TriggerSource,
		&r.TriggerDetail,
		&r.ScheduledAt,
		&r.Status,
		&r.SkipReason,
		&r.FailedStep,
		&r.Error,
		&steps,
		&r.RunAsUserID,
		&r.TriggeredBy,
		&r.StartedAt,
		&r.FinishedAt,
		&r.InsertedAt,
		&r.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	if steps.Valid && steps.String != "" {
		if err := json.Unmarshal([]byte(steps.String), &r.Steps); err != nil {
			return nil, fmt.Errorf("automation run %d has unreadable steps: %w", r.ID, err)
		}
	}
	if r.Steps == nil {
		r.Steps = []structs.AutomationStepResult{}
	}
	return &r, nil
}

type CreateAutomationRunRequest struct {
	AutomationID  int
	TriggerSource structs.AutomationTriggerSource
	TriggerDetail *string
	// ScheduledAt is the nominal slot for schedule runs and the claim key.
	ScheduledAt *time.Time
	Status      structs.AutomationRunStatus
	SkipReason  *string
	RunAsUserID *int
	TriggeredBy *int
	Steps       []structs.AutomationStepResult
	StartedAt   time.Time
	// Finished stamps finished_at, for a run that is terminal on insert.
	Finished bool
}

// CreateAutomationRun inserts one run row and reports whether this caller won it.
//
// For a schedule run the unique key on (automation_id, scheduled_at) is the
// claim, exactly as ClaimSnapshotRun: a second attempt at the same nominal slot
// loses on insert and returns (nil, false, nil) — a duplicate is a no-op, never
// a double run. Webhook and manual runs have a NULL scheduled_at, which a
// MariaDB unique key ignores, so they always win.
func CreateAutomationRun(engine db.Queryable, req CreateAutomationRunRequest) (*structs.AutomationRun, bool, error) {
	var steps *string
	if req.Steps != nil {
		b, err := json.Marshal(req.Steps)
		if err != nil {
			return nil, false, fmt.Errorf("failed to encode steps: %w", err)
		}
		s := string(b)
		steps = &s
	}
	var scheduledAt *time.Time
	if req.ScheduledAt != nil {
		utc := req.ScheduledAt.UTC()
		scheduledAt = &utc
	}
	var finishedAt *time.Time
	if req.Finished {
		now := time.Now().UTC()
		finishedAt = &now
	}

	q := sq.Insert("automation_runs").
		Columns("automation_id", "trigger_source", "trigger_detail", "scheduled_at", "status", "skip_reason",
			"steps", "run_as_user_id", "triggered_by", "started_at", "finished_at").
		Values(req.AutomationID, req.TriggerSource, req.TriggerDetail, scheduledAt, req.Status, req.SkipReason,
			steps, req.RunAsUserID, req.TriggeredBy, req.StartedAt.UTC(), finishedAt)

	qStr, args, err := q.ToSql()
	if err != nil {
		return nil, false, fmt.Errorf("failed to build sql query: %w", err)
	}

	result, err := engine.Exec(qStr, args...)
	if err != nil {
		if isDuplicateEntry(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("failed to create automation run: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return nil, false, fmt.Errorf("failed to read automation run id: %w", err)
	}

	run, err := GetAutomationRunByID(engine, int(id))
	if err != nil {
		return nil, false, err
	}
	return run, true, nil
}

func GetAutomationRunByID(engine db.Queryable, id int) (*structs.AutomationRun, error) {
	q := sq.Select(automationRunColumns...).
		From("automation_runs").
		Where(sq.Eq{"automation_runs.id": id})

	qStr, args, err := q.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}

	run, err := scanAutomationRun(engine.QueryRow(qStr, args...))
	if err != nil {
		if isNoRows(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to scan automation run: %w", err)
	}
	return run, nil
}

type UpdateAutomationRunRequest struct {
	Status     *structs.AutomationRunStatus
	SkipReason *string
	FailedStep *int
	Error      *string
	Steps      *[]structs.AutomationStepResult
	Finished   bool
}

func UpdateAutomationRun(engine db.Queryable, id int, req UpdateAutomationRunRequest) error {
	q := sq.Update("automation_runs").Where(sq.Eq{"id": id})
	has := false

	if req.Status != nil {
		q = q.Set("status", *req.Status)
		has = true
	}
	if req.SkipReason != nil {
		q = q.Set("skip_reason", *req.SkipReason)
		has = true
	}
	if req.FailedStep != nil {
		q = q.Set("failed_step", *req.FailedStep)
		has = true
	}
	if req.Error != nil {
		q = q.Set("error_message", *req.Error)
		has = true
	}
	if req.Steps != nil {
		b, err := json.Marshal(*req.Steps)
		if err != nil {
			return fmt.Errorf("failed to encode steps: %w", err)
		}
		q = q.Set("steps", string(b))
		has = true
	}
	if req.Finished {
		q = q.Set("finished_at", time.Now().UTC())
		has = true
	}
	if !has {
		return nil
	}

	qStr, args, err := q.ToSql()
	if err != nil {
		return fmt.Errorf("failed to build sql query: %w", err)
	}
	_, err = engine.Exec(qStr, args...)
	return err
}

// ListAutomationRuns returns an automation's runs, newest first — including the
// skipped ones, which is the point.
func ListAutomationRuns(engine db.Queryable, automationID, limit, offset int) ([]structs.AutomationRun, error) {
	if limit <= 0 || limit > db.MAX_LIMIT {
		limit = db.DEFAULT_LIMIT
	}
	q := sq.Select(automationRunColumns...).
		From("automation_runs").
		Where(sq.Eq{"automation_runs.automation_id": automationID}).
		OrderBy("automation_runs.id DESC").
		Limit(uint64(limit))
	if offset > 0 {
		q = q.Offset(uint64(offset))
	}

	return queryAutomationRuns(engine, q)
}

// ListLatestAutomationRuns returns the newest run of each given automation,
// keyed by automation id, for the list view's "last run" column.
func ListLatestAutomationRuns(engine db.Queryable, automationIDs []int) (map[int]structs.AutomationRun, error) {
	latest := make(map[int]structs.AutomationRun, len(automationIDs))
	if len(automationIDs) == 0 {
		return latest, nil
	}

	sub, subArgs, err := sq.Select("MAX(id)").
		From("automation_runs").
		Where(sq.Eq{"automation_id": automationIDs}).
		GroupBy("automation_id").
		ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}

	q := sq.Select(automationRunColumns...).
		From("automation_runs").
		Where(sq.Expr("automation_runs.id IN ("+sub+")", subArgs...))

	runs, err := queryAutomationRuns(engine, q)
	if err != nil {
		return nil, err
	}
	for _, r := range runs {
		latest[r.AutomationID] = r
	}
	return latest, nil
}

// ListStuckAutomationRuns returns runs still in progress that started before
// cutoff — runs whose control plane died before it could record an outcome.
func ListStuckAutomationRuns(engine db.Queryable, cutoff time.Time) ([]structs.AutomationRun, error) {
	q := sq.Select(automationRunColumns...).
		From("automation_runs").
		Where(sq.Eq{"automation_runs.status": structs.AutomationRunInProgress}).
		Where(sq.Lt{"automation_runs.started_at": cutoff.UTC()}).
		Limit(uint64(db.MAX_LIMIT))

	return queryAutomationRuns(engine, q)
}

func queryAutomationRuns(engine db.Queryable, q sq.SelectBuilder) ([]structs.AutomationRun, error) {
	qStr, args, err := q.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}

	rows, err := engine.Query(qStr, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to execute sql query: %w", err)
	}
	defer rows.Close()

	runs := []structs.AutomationRun{}
	for rows.Next() {
		r, err := scanAutomationRun(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan automation run: %w", err)
		}
		runs = append(runs, *r)
	}
	return runs, rows.Err()
}
