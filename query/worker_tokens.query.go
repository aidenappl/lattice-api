package query

import (
	"fmt"

	sq "github.com/Masterminds/squirrel"
	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/structs"
)

var workerTokenColumns = []string{
	"worker_tokens.id",
	"worker_tokens.worker_id",
	"worker_tokens.name",
	"worker_tokens.token_hash",
	"worker_tokens.last_used_at",
	"worker_tokens.active",
	"worker_tokens.updated_at",
	"worker_tokens.inserted_at",
}

func scanWorkerToken(row scanner) (*structs.WorkerToken, error) {
	var t structs.WorkerToken
	err := row.Scan(
		&t.ID,
		&t.WorkerID,
		&t.Name,
		&t.TokenHash,
		&t.LastUsedAt,
		&t.Active,
		&t.UpdatedAt,
		&t.InsertedAt,
	)
	return &t, err
}

func ListWorkerTokens(engine db.Queryable, workerID int) (*[]structs.WorkerToken, error) {
	q := sq.Select(workerTokenColumns...).
		From("worker_tokens").
		Where(sq.Eq{"worker_tokens.worker_id": workerID}).
		Where(sq.Eq{"worker_tokens.active": true}).
		OrderBy("worker_tokens.id DESC")

	qStr, args, err := q.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}

	rows, err := engine.Query(qStr, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to execute sql query: %w", err)
	}
	defer rows.Close()

	var tokens []structs.WorkerToken
	for rows.Next() {
		t, err := scanWorkerToken(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan worker token: %w", err)
		}
		tokens = append(tokens, *t)
	}

	return &tokens, rows.Err()
}

func GetWorkerTokenByHash(engine db.Queryable, tokenHash string) (*structs.WorkerToken, error) {
	q := sq.Select(workerTokenColumns...).
		From("worker_tokens").
		Where(sq.Eq{"worker_tokens.token_hash": tokenHash}).
		Where(sq.Eq{"worker_tokens.active": true})

	qStr, args, err := q.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}

	row := engine.QueryRow(qStr, args...)
	t, err := scanWorkerToken(row)
	if err != nil {
		return nil, fmt.Errorf("failed to scan worker token: %w", err)
	}

	return t, nil
}

type CreateWorkerTokenRequest struct {
	WorkerID  int
	Name      string
	TokenHash string
}

func CreateWorkerToken(engine db.Queryable, req CreateWorkerTokenRequest) (*structs.WorkerToken, error) {
	q := sq.Insert("worker_tokens").
		Columns("worker_id", "name", "token_hash").
		Values(req.WorkerID, req.Name, req.TokenHash)

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

	q2 := sq.Select(workerTokenColumns...).From("worker_tokens").Where(sq.Eq{"id": id})
	qStr2, args2, _ := q2.ToSql()
	row := engine.QueryRow(qStr2, args2...)
	return scanWorkerToken(row)
}

func DeleteWorkerToken(engine db.Queryable, id int) error {
	_, err := engine.Exec("UPDATE worker_tokens SET active = 0 WHERE id = ?", id)
	return err
}

func TouchWorkerToken(engine db.Queryable, id int) error {
	_, err := engine.Exec("UPDATE worker_tokens SET last_used_at = NOW() WHERE id = ?", id)
	return err
}
