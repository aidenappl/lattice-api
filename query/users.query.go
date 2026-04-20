package query

import (
	"fmt"

	sq "github.com/Masterminds/squirrel"
	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/structs"
)

var userColumns = []string{
	"users.id",
	"users.email",
	"users.name",
	"users.auth_type",
	"users.password_hash",
	"users.forta_id",
	"users.role",
	"users.active",
	"users.updated_at",
	"users.inserted_at",
}

func scanUser(row scanner) (*structs.User, error) {
	var u structs.User
	err := row.Scan(
		&u.ID,
		&u.Email,
		&u.Name,
		&u.AuthType,
		&u.PasswordHash,
		&u.FortaID,
		&u.Role,
		&u.Active,
		&u.UpdatedAt,
		&u.InsertedAt,
	)
	return &u, err
}

type ListUsersRequest struct {
	Limit  int
	Offset int
	Active *bool
}

func ListUsers(engine db.Queryable, req ListUsersRequest) (*[]structs.User, error) {
	q := sq.Select(userColumns...).From("users")

	if req.Active != nil {
		q = q.Where(sq.Eq{"users.active": *req.Active})
	}

	if req.Limit == 0 || req.Limit > db.MAX_LIMIT {
		req.Limit = db.DEFAULT_LIMIT
	}
	q = q.Limit(uint64(req.Limit))
	if req.Offset > 0 {
		q = q.Offset(uint64(req.Offset))
	}

	q = q.OrderBy("users.id DESC")

	qStr, args, err := q.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}

	rows, err := engine.Query(qStr, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to execute sql query: %w", err)
	}
	defer rows.Close()

	var users []structs.User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan user: %w", err)
		}
		users = append(users, *u)
	}

	return &users, rows.Err()
}

func GetUserByID(engine db.Queryable, id int) (*structs.User, error) {
	q := sq.Select(userColumns...).From("users").Where(sq.Eq{"users.id": id})

	qStr, args, err := q.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}

	row := engine.QueryRow(qStr, args...)
	u, err := scanUser(row)
	if err != nil {
		return nil, fmt.Errorf("failed to scan user: %w", err)
	}

	return u, nil
}

func GetUserByEmail(engine db.Queryable, email string) (*structs.User, error) {
	q := sq.Select(userColumns...).From("users").Where(sq.Eq{"users.email": email})

	qStr, args, err := q.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}

	row := engine.QueryRow(qStr, args...)
	u, err := scanUser(row)
	if err != nil {
		return nil, fmt.Errorf("failed to scan user: %w", err)
	}

	return u, nil
}

func GetUserByFortaID(engine db.Queryable, fortaID int64) (*structs.User, error) {
	q := sq.Select(userColumns...).From("users").Where(sq.Eq{"users.forta_id": fortaID})

	qStr, args, err := q.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}

	row := engine.QueryRow(qStr, args...)
	u, err := scanUser(row)
	if err != nil {
		return nil, fmt.Errorf("failed to scan user: %w", err)
	}

	return u, nil
}

func CountUsers(engine db.Queryable) (int, error) {
	var count int
	err := engine.QueryRow("SELECT COUNT(*) FROM users").Scan(&count)
	return count, err
}

type CreateUserRequest struct {
	Email        string
	Name         *string
	AuthType     string
	PasswordHash *string
	FortaID      *int64
	Role         string
}

func CreateUser(engine db.Queryable, req CreateUserRequest) (*structs.User, error) {
	q := sq.Insert("users").
		Columns("email", "name", "auth_type", "password_hash", "forta_id", "role").
		Values(req.Email, req.Name, req.AuthType, req.PasswordHash, req.FortaID, req.Role)

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

	return GetUserByID(engine, int(id))
}

type UpdateUserRequest struct {
	Name   *string
	Role   *string
	Active *bool
}

func DeleteUser(engine db.Queryable, id int) error {
	q := sq.Update("users").Set("active", false).Where(sq.Eq{"id": id})

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

func UpdateUser(engine db.Queryable, id int, req UpdateUserRequest) (*structs.User, error) {
	q := sq.Update("users").Where(sq.Eq{"id": id})

	hasUpdate := false
	if req.Name != nil {
		q = q.Set("name", *req.Name)
		hasUpdate = true
	}
	if req.Role != nil {
		q = q.Set("role", *req.Role)
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

	return GetUserByID(engine, id)
}
