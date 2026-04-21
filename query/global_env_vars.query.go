package query

import (
	"fmt"

	sq "github.com/Masterminds/squirrel"
	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/structs"
)

var globalEnvVarColumns = []string{
	"global_env_vars.id",
	"global_env_vars.`key`",
	"global_env_vars.encrypted_value",
	"global_env_vars.is_secret",
	"global_env_vars.updated_at",
	"global_env_vars.inserted_at",
}

func scanGlobalEnvVar(row scanner) (*structs.GlobalEnvVar, error) {
	var g structs.GlobalEnvVar
	err := row.Scan(
		&g.ID,
		&g.Key,
		&g.EncryptedValue,
		&g.IsSecret,
		&g.UpdatedAt,
		&g.InsertedAt,
	)
	return &g, err
}

func ListGlobalEnvVars(engine db.Queryable) (*[]structs.GlobalEnvVar, error) {
	q := sq.Select(globalEnvVarColumns...).
		From("global_env_vars").
		OrderBy("global_env_vars.`key` ASC")

	qStr, args, err := q.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}

	rows, err := engine.Query(qStr, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to execute sql query: %w", err)
	}
	defer rows.Close()

	var vars []structs.GlobalEnvVar
	for rows.Next() {
		g, err := scanGlobalEnvVar(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan global env var: %w", err)
		}
		vars = append(vars, *g)
	}

	return &vars, rows.Err()
}

func GetGlobalEnvVar(engine db.Queryable, id int) (*structs.GlobalEnvVar, error) {
	q := sq.Select(globalEnvVarColumns...).From("global_env_vars").Where(sq.Eq{"global_env_vars.id": id})

	qStr, args, err := q.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}

	row := engine.QueryRow(qStr, args...)
	g, err := scanGlobalEnvVar(row)
	if err != nil {
		return nil, fmt.Errorf("failed to scan global env var: %w", err)
	}

	return g, nil
}

func CreateGlobalEnvVar(engine db.Queryable, key string, encryptedValue string, isSecret bool) (*structs.GlobalEnvVar, error) {
	q := sq.Insert("global_env_vars").
		Columns("`key`", "encrypted_value", "is_secret").
		Values(key, encryptedValue, isSecret)

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

	return GetGlobalEnvVar(engine, int(id))
}

func UpdateGlobalEnvVar(engine db.Queryable, id int, encryptedValue string, isSecret bool) error {
	q := sq.Update("global_env_vars").
		Set("encrypted_value", encryptedValue).
		Set("is_secret", isSecret).
		Where(sq.Eq{"id": id})

	qStr, args, err := q.ToSql()
	if err != nil {
		return fmt.Errorf("failed to build sql query: %w", err)
	}

	_, err = engine.Exec(qStr, args...)
	if err != nil {
		return fmt.Errorf("failed to execute sql query: %w", err)
	}

	return nil
}

func DeleteGlobalEnvVar(engine db.Queryable, id int) error {
	_, err := engine.Exec("DELETE FROM global_env_vars WHERE id = ?", id)
	return err
}
