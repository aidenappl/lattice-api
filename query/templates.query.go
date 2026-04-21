package query

import (
	"fmt"

	sq "github.com/Masterminds/squirrel"
	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/structs"
)

var templateColumns = []string{
	"templates.id",
	"templates.name",
	"templates.description",
	"templates.config",
	"templates.created_by",
	"templates.active",
	"templates.updated_at",
	"templates.inserted_at",
}

func scanTemplate(row scanner) (*structs.Template, error) {
	var t structs.Template
	err := row.Scan(
		&t.ID,
		&t.Name,
		&t.Description,
		&t.Config,
		&t.CreatedBy,
		&t.Active,
		&t.UpdatedAt,
		&t.InsertedAt,
	)
	return &t, err
}

func ListTemplates(engine db.Queryable) (*[]structs.Template, error) {
	q := sq.Select(templateColumns...).
		From("templates").
		Where(sq.Eq{"templates.active": true}).
		OrderBy("templates.id DESC")

	qStr, args, err := q.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}

	rows, err := engine.Query(qStr, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to execute sql query: %w", err)
	}
	defer rows.Close()

	var templates []structs.Template
	for rows.Next() {
		t, err := scanTemplate(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan template: %w", err)
		}
		templates = append(templates, *t)
	}

	return &templates, rows.Err()
}

func GetTemplateByID(engine db.Queryable, id int) (*structs.Template, error) {
	q := sq.Select(templateColumns...).From("templates").Where(sq.Eq{"templates.id": id})

	qStr, args, err := q.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}

	row := engine.QueryRow(qStr, args...)
	t, err := scanTemplate(row)
	if err != nil {
		return nil, fmt.Errorf("failed to scan template: %w", err)
	}

	return t, nil
}

type CreateTemplateRequest struct {
	Name        string
	Description *string
	Config      string
	CreatedBy   *int
}

func CreateTemplate(engine db.Queryable, req CreateTemplateRequest) (*structs.Template, error) {
	q := sq.Insert("templates").
		Columns("name", "description", "config", "created_by").
		Values(req.Name, req.Description, req.Config, req.CreatedBy)

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

	return GetTemplateByID(engine, int(id))
}

func DeleteTemplate(engine db.Queryable, id int) error {
	_, err := engine.Exec("UPDATE templates SET active = 0 WHERE id = ?", id)
	return err
}
