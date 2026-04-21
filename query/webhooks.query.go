package query

import (
	"fmt"

	sq "github.com/Masterminds/squirrel"
	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/structs"
)

var webhookConfigColumns = []string{
	"webhook_configs.id",
	"webhook_configs.name",
	"webhook_configs.url",
	"webhook_configs.events",
	"webhook_configs.active",
	"webhook_configs.secret",
	"webhook_configs.updated_at",
	"webhook_configs.inserted_at",
}

func scanWebhookConfig(row scanner) (*structs.WebhookConfig, error) {
	var w structs.WebhookConfig
	err := row.Scan(
		&w.ID,
		&w.Name,
		&w.URL,
		&w.Events,
		&w.Active,
		&w.Secret,
		&w.UpdatedAt,
		&w.InsertedAt,
	)
	return &w, err
}

func ListWebhookConfigs(engine db.Queryable) (*[]structs.WebhookConfig, error) {
	q := sq.Select(webhookConfigColumns...).
		From("webhook_configs").
		Where(sq.Eq{"webhook_configs.active": true}).
		OrderBy("webhook_configs.id DESC")

	qStr, args, err := q.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}

	rows, err := engine.Query(qStr, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to execute sql query: %w", err)
	}
	defer rows.Close()

	var configs []structs.WebhookConfig
	for rows.Next() {
		w, err := scanWebhookConfig(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan webhook config: %w", err)
		}
		configs = append(configs, *w)
	}

	return &configs, rows.Err()
}

func GetWebhookConfig(engine db.Queryable, id int) (*structs.WebhookConfig, error) {
	q := sq.Select(webhookConfigColumns...).From("webhook_configs").Where(sq.Eq{"webhook_configs.id": id})

	qStr, args, err := q.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}

	row := engine.QueryRow(qStr, args...)
	w, err := scanWebhookConfig(row)
	if err != nil {
		return nil, fmt.Errorf("failed to scan webhook config: %w", err)
	}

	return w, nil
}

type CreateWebhookConfigRequest struct {
	Name   string
	URL    string
	Events string
	Secret *string
}

func CreateWebhookConfig(engine db.Queryable, req CreateWebhookConfigRequest) (*structs.WebhookConfig, error) {
	q := sq.Insert("webhook_configs").
		Columns("name", "url", "events", "secret").
		Values(req.Name, req.URL, req.Events, req.Secret)

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

	return GetWebhookConfig(engine, int(id))
}

type UpdateWebhookConfigRequest struct {
	Name   *string
	URL    *string
	Events *string
	Secret *string
	Active *bool
}

func UpdateWebhookConfig(engine db.Queryable, id int, req UpdateWebhookConfigRequest) (*structs.WebhookConfig, error) {
	q := sq.Update("webhook_configs").Where(sq.Eq{"id": id})

	hasUpdate := false
	if req.Name != nil {
		q = q.Set("name", *req.Name)
		hasUpdate = true
	}
	if req.URL != nil {
		q = q.Set("url", *req.URL)
		hasUpdate = true
	}
	if req.Events != nil {
		q = q.Set("events", *req.Events)
		hasUpdate = true
	}
	if req.Secret != nil {
		q = q.Set("secret", *req.Secret)
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

	return GetWebhookConfig(engine, id)
}

func DeleteWebhookConfig(engine db.Queryable, id int) error {
	_, err := engine.Exec("UPDATE webhook_configs SET active = 0 WHERE id = ?", id)
	return err
}
