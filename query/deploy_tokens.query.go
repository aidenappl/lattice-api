package query

import (
	"fmt"

	sq "github.com/Masterminds/squirrel"
	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/structs"
)

var deployTokenColumns = []string{
	"deploy_tokens.id",
	"deploy_tokens.stack_id",
	"deploy_tokens.name",
	"deploy_tokens.token_hash",
	"deploy_tokens.last_used_at",
	"deploy_tokens.active",
	"deploy_tokens.updated_at",
	"deploy_tokens.inserted_at",
}

func scanDeployToken(row scanner) (*structs.DeployToken, error) {
	var t structs.DeployToken
	err := row.Scan(
		&t.ID,
		&t.StackID,
		&t.Name,
		&t.TokenHash,
		&t.LastUsedAt,
		&t.Active,
		&t.UpdatedAt,
		&t.InsertedAt,
	)
	return &t, err
}

func ListDeployTokensByStack(engine db.Queryable, stackID int) (*[]structs.DeployToken, error) {
	q := sq.Select(deployTokenColumns...).
		From("deploy_tokens").
		Where(sq.Eq{"deploy_tokens.stack_id": stackID}).
		Where(sq.Eq{"deploy_tokens.active": true}).
		OrderBy("deploy_tokens.id DESC")

	qStr, args, err := q.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}

	rows, err := engine.Query(qStr, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to execute sql query: %w", err)
	}
	defer rows.Close()

	var tokens []structs.DeployToken
	for rows.Next() {
		t, err := scanDeployToken(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan deploy token: %w", err)
		}
		tokens = append(tokens, *t)
	}

	return &tokens, rows.Err()
}

func GetDeployTokenByHash(engine db.Queryable, tokenHash string) (*structs.DeployToken, error) {
	q := sq.Select(deployTokenColumns...).
		From("deploy_tokens").
		Where(sq.Eq{"deploy_tokens.token_hash": tokenHash}).
		Where(sq.Eq{"deploy_tokens.active": true})

	qStr, args, err := q.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}

	row := engine.QueryRow(qStr, args...)
	t, err := scanDeployToken(row)
	if err != nil {
		return nil, fmt.Errorf("failed to scan deploy token: %w", err)
	}

	return t, nil
}

type CreateDeployTokenRequest struct {
	StackID   int
	Name      string
	TokenHash string
}

func CreateDeployToken(engine db.Queryable, req CreateDeployTokenRequest) (*structs.DeployToken, error) {
	q := sq.Insert("deploy_tokens").
		Columns("stack_id", "name", "token_hash").
		Values(req.StackID, req.Name, req.TokenHash)

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

	q2 := sq.Select(deployTokenColumns...).From("deploy_tokens").Where(sq.Eq{"id": id})
	qStr2, args2, _ := q2.ToSql()
	row := engine.QueryRow(qStr2, args2...)
	return scanDeployToken(row)
}

func DeleteDeployToken(engine db.Queryable, id int) error {
	_, err := engine.Exec("UPDATE deploy_tokens SET active = 0 WHERE id = ?", id)
	return err
}

func TouchDeployToken(engine db.Queryable, id int) error {
	_, err := engine.Exec("UPDATE deploy_tokens SET last_used_at = NOW() WHERE id = ?", id)
	return err
}
