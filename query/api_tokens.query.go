package query

import (
	"fmt"
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/structs"
)

var apiTokenColumns = []string{
	"api_tokens.id",
	"api_tokens.user_id",
	"api_tokens.name",
	"api_tokens.token_hash",
	"api_tokens.scopes",
	"api_tokens.expires_at",
	"api_tokens.last_used_at",
	"api_tokens.active",
	"api_tokens.updated_at",
	"api_tokens.inserted_at",
}

func scanApiToken(row scanner) (*structs.ApiToken, error) {
	var t structs.ApiToken
	err := row.Scan(
		&t.ID,
		&t.UserID,
		&t.Name,
		&t.TokenHash,
		&t.Scopes,
		&t.ExpiresAt,
		&t.LastUsedAt,
		&t.Active,
		&t.UpdatedAt,
		&t.InsertedAt,
	)
	return &t, err
}

func ListApiTokens(engine db.Queryable, userID *int) (*[]structs.ApiToken, error) {
	q := sq.Select(apiTokenColumns...).
		From("api_tokens").
		Where(sq.Eq{"api_tokens.active": true}).
		OrderBy("api_tokens.id DESC").
		Limit(uint64(db.MAX_LIMIT))

	if userID != nil {
		q = q.Where(sq.Eq{"api_tokens.user_id": *userID})
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

	var tokens []structs.ApiToken
	for rows.Next() {
		t, err := scanApiToken(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan api token: %w", err)
		}
		tokens = append(tokens, *t)
	}

	return &tokens, rows.Err()
}

func GetApiTokenByHash(engine db.Queryable, tokenHash string) (*structs.ApiToken, error) {
	q := sq.Select(apiTokenColumns...).
		From("api_tokens").
		Where(sq.Eq{"api_tokens.token_hash": tokenHash}).
		Where(sq.Eq{"api_tokens.active": true}).
		Where(sq.Or{
			sq.Eq{"api_tokens.expires_at": nil},
			sq.Gt{"api_tokens.expires_at": sq.Expr("NOW()")},
		})

	qStr, args, err := q.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}

	row := engine.QueryRow(qStr, args...)
	t, err := scanApiToken(row)
	if err != nil {
		return nil, fmt.Errorf("failed to scan api token: %w", err)
	}

	return t, nil
}

type CreateApiTokenRequest struct {
	UserID    int
	Name      string
	TokenHash string
	Scopes    *string
	ExpiresAt *time.Time
}

func CreateApiToken(engine db.Queryable, req CreateApiTokenRequest) (*structs.ApiToken, error) {
	q := sq.Insert("api_tokens").
		Columns("user_id", "name", "token_hash", "scopes", "expires_at").
		Values(req.UserID, req.Name, req.TokenHash, req.Scopes, req.ExpiresAt)

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

	q2 := sq.Select(apiTokenColumns...).From("api_tokens").Where(sq.Eq{"id": id})
	qStr2, args2, _ := q2.ToSql()
	row := engine.QueryRow(qStr2, args2...)
	return scanApiToken(row)
}

func DeleteApiToken(engine db.Queryable, id int) error {
	_, err := engine.Exec("UPDATE api_tokens SET active = 0 WHERE id = ?", id)
	return err
}

func TouchApiToken(engine db.Queryable, id int) error {
	_, err := engine.Exec("UPDATE api_tokens SET last_used_at = NOW() WHERE id = ?", id)
	return err
}
