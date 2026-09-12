package query

import (
	"encoding/json"
	"fmt"
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/structs"
)

var automationColumns = []string{
	"automations.id",
	"automations.name",
	"automations.description",
	"automations.enabled",
	"automations.trigger_config",
	"automations.actions",
	"automations.webhook_token_hash",
	"automations.webhook_last_used_at",
	"automations.run_as_user_id",
	"automations.created_by",
	"automations.running_run_id",
	"automations.running_since",
	"automations.active",
	"automations.updated_at",
	"automations.inserted_at",
}

func scanAutomation(row scanner) (*structs.Automation, error) {
	var a structs.Automation
	var trigger, actions string
	err := row.Scan(
		&a.ID,
		&a.Name,
		&a.Description,
		&a.Enabled,
		&trigger,
		&actions,
		&a.WebhookTokenHash,
		&a.WebhookLastUsedAt,
		&a.RunAsUserID,
		&a.CreatedBy,
		&a.RunningRunID,
		&a.RunningSince,
		&a.Active,
		&a.UpdatedAt,
		&a.InsertedAt,
	)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(trigger), &a.Trigger); err != nil {
		return nil, fmt.Errorf("automation %d has an unreadable trigger_config: %w", a.ID, err)
	}
	if err := json.Unmarshal([]byte(actions), &a.Actions); err != nil {
		return nil, fmt.Errorf("automation %d has unreadable actions: %w", a.ID, err)
	}
	return &a, nil
}

type ListAutomationsRequest struct {
	Limit   int
	Offset  int
	Enabled *bool
}

func ListAutomations(engine db.Queryable, req ListAutomationsRequest) ([]structs.Automation, error) {
	q := sq.Select(automationColumns...).
		From("automations").
		Where(sq.Eq{"automations.active": true})

	if req.Enabled != nil {
		q = q.Where(sq.Eq{"automations.enabled": *req.Enabled})
	}

	if req.Limit == 0 || req.Limit > db.MAX_LIMIT {
		req.Limit = db.DEFAULT_LIMIT
	}
	q = q.Limit(uint64(req.Limit))
	if req.Offset > 0 {
		q = q.Offset(uint64(req.Offset))
	}
	q = q.OrderBy("automations.id DESC")

	qStr, args, err := q.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}

	rows, err := engine.Query(qStr, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to execute sql query: %w", err)
	}
	defer rows.Close()

	automations := []structs.Automation{}
	for rows.Next() {
		a, err := scanAutomation(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan automation: %w", err)
		}
		automations = append(automations, *a)
	}
	return automations, rows.Err()
}

// ListScheduledAutomations returns every enabled automation with a schedule
// trigger, for the scheduler. The trigger type lives inside trigger_config, so
// it is filtered here rather than in SQL; the set is bounded by MAX_LIMIT.
func ListScheduledAutomations(engine db.Queryable) ([]structs.Automation, error) {
	enabled := true
	all, err := ListAutomations(engine, ListAutomationsRequest{Limit: db.MAX_LIMIT, Enabled: &enabled})
	if err != nil {
		return nil, err
	}
	scheduled := make([]structs.Automation, 0, len(all))
	for _, a := range all {
		if a.Trigger.Type == structs.AutomationTriggerSchedule {
			scheduled = append(scheduled, a)
		}
	}
	return scheduled, nil
}

func GetAutomationByID(engine db.Queryable, id int) (*structs.Automation, error) {
	q := sq.Select(automationColumns...).
		From("automations").
		Where(sq.Eq{"automations.id": id, "automations.active": true})

	qStr, args, err := q.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}

	a, err := scanAutomation(engine.QueryRow(qStr, args...))
	if err != nil {
		if isNoRows(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to scan automation: %w", err)
	}
	return a, nil
}

// GetAutomationByWebhookHash resolves a webhook token hash to its automation.
// Deleted automations are excluded, and deletion also nulls the hash, so a
// token outlives its automation in neither direction.
func GetAutomationByWebhookHash(engine db.Queryable, tokenHash string) (*structs.Automation, error) {
	q := sq.Select(automationColumns...).
		From("automations").
		Where(sq.Eq{"automations.webhook_token_hash": tokenHash, "automations.active": true})

	qStr, args, err := q.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}

	a, err := scanAutomation(engine.QueryRow(qStr, args...))
	if err != nil {
		if isNoRows(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to scan automation: %w", err)
	}
	return a, nil
}

type CreateAutomationRequest struct {
	Name             string
	Description      *string
	Enabled          bool
	Trigger          structs.AutomationTrigger
	Actions          []structs.AutomationAction
	WebhookTokenHash *string
	RunAsUserID      int
	CreatedBy        int
}

func CreateAutomation(engine db.Queryable, req CreateAutomationRequest) (*structs.Automation, error) {
	trigger, err := json.Marshal(req.Trigger)
	if err != nil {
		return nil, fmt.Errorf("failed to encode trigger: %w", err)
	}
	actions, err := json.Marshal(req.Actions)
	if err != nil {
		return nil, fmt.Errorf("failed to encode actions: %w", err)
	}

	q := sq.Insert("automations").
		Columns("name", "description", "enabled", "trigger_config", "actions", "webhook_token_hash", "run_as_user_id", "created_by").
		Values(req.Name, req.Description, req.Enabled, string(trigger), string(actions), req.WebhookTokenHash, req.RunAsUserID, req.CreatedBy)

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
	return GetAutomationByID(engine, int(id))
}

type UpdateAutomationRequest struct {
	Name             *string
	Description      *string
	ClearDescription bool
	Enabled          *bool
	Trigger          *structs.AutomationTrigger
	Actions          *[]structs.AutomationAction
	WebhookTokenHash *string
	// ClearWebhookToken drops the token hash, for a trigger that stops being a webhook.
	ClearWebhookToken bool
	RunAsUserID       *int
}

func UpdateAutomation(engine db.Queryable, id int, req UpdateAutomationRequest) (*structs.Automation, error) {
	q := sq.Update("automations").Where(sq.Eq{"id": id, "active": true})
	has := false

	if req.Name != nil {
		q = q.Set("name", *req.Name)
		has = true
	}
	if req.ClearDescription {
		q = q.Set("description", nil)
		has = true
	} else if req.Description != nil {
		q = q.Set("description", *req.Description)
		has = true
	}
	if req.Enabled != nil {
		q = q.Set("enabled", *req.Enabled)
		has = true
	}
	if req.Trigger != nil {
		trigger, err := json.Marshal(*req.Trigger)
		if err != nil {
			return nil, fmt.Errorf("failed to encode trigger: %w", err)
		}
		q = q.Set("trigger_config", string(trigger))
		has = true
	}
	if req.Actions != nil {
		actions, err := json.Marshal(*req.Actions)
		if err != nil {
			return nil, fmt.Errorf("failed to encode actions: %w", err)
		}
		q = q.Set("actions", string(actions))
		has = true
	}
	if req.ClearWebhookToken {
		q = q.Set("webhook_token_hash", nil)
		has = true
	} else if req.WebhookTokenHash != nil {
		q = q.Set("webhook_token_hash", *req.WebhookTokenHash)
		has = true
	}
	if req.RunAsUserID != nil {
		q = q.Set("run_as_user_id", *req.RunAsUserID)
		has = true
	}

	if has {
		qStr, args, err := q.ToSql()
		if err != nil {
			return nil, fmt.Errorf("failed to build sql query: %w", err)
		}
		if _, err := engine.Exec(qStr, args...); err != nil {
			return nil, fmt.Errorf("failed to execute sql query: %w", err)
		}
	}
	return GetAutomationByID(engine, id)
}

// DeleteAutomation retires an automation. The token hash is nulled in the same
// statement, so a leaked webhook URL is dead the moment the automation is.
func DeleteAutomation(engine db.Queryable, id int) error {
	_, err := engine.Exec("UPDATE automations SET active = 0, enabled = 0, webhook_token_hash = NULL WHERE id = ?", id)
	return err
}

// TouchAutomationWebhook stamps webhook_last_used_at. updated_at is pinned to
// itself so that "last edited" keeps meaning a person changed the definition.
func TouchAutomationWebhook(engine db.Queryable, id int) error {
	_, err := engine.Exec("UPDATE automations SET webhook_last_used_at = ?, updated_at = updated_at WHERE id = ?",
		time.Now().UTC(), id)
	return err
}

// ClaimAutomationRun takes an automation's run guard for runID, and reports
// whether it was taken.
//
// A conditional UPDATE, the same shape as ClaimStackForDeploy: the row changes
// only if nobody holds the guard, or the holder's claim is older than
// staleBefore. A row lock (SELECT ... FOR UPDATE) was the alternative and is the
// wrong tool — it would hold a transaction, and a pooled connection, open for
// the whole run, across outbound HTTP calls.
//
// The stale-claim clause is what makes a crash survivable. Without it, a control
// plane that died mid-run would leave the automation refusing every future
// firing with "already running", which is an outage dressed as a safety feature.
func ClaimAutomationRun(engine db.Queryable, automationID, runID int, staleBefore time.Time) (bool, error) {
	q := sq.Update("automations").
		Set("running_run_id", runID).
		Set("running_since", time.Now().UTC()).
		Set("updated_at", sq.Expr("updated_at")).
		Where(sq.Eq{"id": automationID, "active": true}).
		Where(sq.Or{
			sq.Eq{"running_run_id": nil},
			sq.Lt{"running_since": staleBefore.UTC()},
		})

	qStr, args, err := q.ToSql()
	if err != nil {
		return false, fmt.Errorf("failed to build sql query: %w", err)
	}
	result, err := engine.Exec(qStr, args...)
	if err != nil {
		return false, fmt.Errorf("failed to claim automation run: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return rows > 0, nil
}

// ReleaseAutomationRun drops the run guard — but only if runID still holds it.
// A run whose stale claim was broken by a later run must not release the later
// run's claim when it finally finishes.
func ReleaseAutomationRun(engine db.Queryable, automationID, runID int) error {
	_, err := engine.Exec(
		"UPDATE automations SET running_run_id = NULL, running_since = NULL, updated_at = updated_at WHERE id = ? AND running_run_id = ?",
		automationID, runID,
	)
	return err
}
