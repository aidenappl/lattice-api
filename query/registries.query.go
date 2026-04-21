package query

import (
	"fmt"

	sq "github.com/Masterminds/squirrel"
	"github.com/aidenappl/lattice-api/crypto"
	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/structs"
)

var registryColumns = []string{
	"registries.id",
	"registries.name",
	"registries.url",
	"registries.type",
	"registries.username",
	"registries.password",
	"registries.active",
	"registries.updated_at",
	"registries.inserted_at",
}

func scanRegistry(row scanner) (*structs.Registry, error) {
	var r structs.Registry
	err := row.Scan(
		&r.ID,
		&r.Name,
		&r.URL,
		&r.Type,
		&r.Username,
		&r.Password,
		&r.Active,
		&r.UpdatedAt,
		&r.InsertedAt,
	)
	if err == nil && r.Password != nil && *r.Password != "" {
		decrypted, _ := crypto.Decrypt(*r.Password)
		r.Password = &decrypted
	}
	return &r, err
}

func ListRegistries(engine db.Queryable) (*[]structs.Registry, error) {
	q := sq.Select(registryColumns...).
		From("registries").
		Where(sq.Eq{"registries.active": true}).
		OrderBy("registries.id DESC")

	qStr, args, err := q.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}

	rows, err := engine.Query(qStr, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to execute sql query: %w", err)
	}
	defer rows.Close()

	var registries []structs.Registry
	for rows.Next() {
		r, err := scanRegistry(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan registry: %w", err)
		}
		registries = append(registries, *r)
	}

	return &registries, rows.Err()
}

func GetRegistryByID(engine db.Queryable, id int) (*structs.Registry, error) {
	q := sq.Select(registryColumns...).From("registries").Where(sq.Eq{"registries.id": id})

	qStr, args, err := q.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}

	row := engine.QueryRow(qStr, args...)
	r, err := scanRegistry(row)
	if err != nil {
		return nil, fmt.Errorf("failed to scan registry: %w", err)
	}

	return r, nil
}

type CreateRegistryRequest struct {
	Name     string
	URL      string
	Type     string
	Username *string
	Password *string
}

func CreateRegistry(engine db.Queryable, req CreateRegistryRequest) (*structs.Registry, error) {
	encPassword := req.Password
	if encPassword != nil && *encPassword != "" {
		encrypted, err := crypto.Encrypt(*encPassword)
		if err == nil {
			encPassword = &encrypted
		}
	}
	q := sq.Insert("registries").
		Columns("name", "url", "type", "username", "password").
		Values(req.Name, req.URL, req.Type, req.Username, encPassword)

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

	return GetRegistryByID(engine, int(id))
}

type UpdateRegistryRequest struct {
	Name     *string
	URL      *string
	Type     *string
	Username *string
	Password *string
	Active   *bool
}

func UpdateRegistry(engine db.Queryable, id int, req UpdateRegistryRequest) (*structs.Registry, error) {
	q := sq.Update("registries").Where(sq.Eq{"id": id})

	hasUpdate := false
	if req.Name != nil {
		q = q.Set("name", *req.Name)
		hasUpdate = true
	}
	if req.URL != nil {
		q = q.Set("url", *req.URL)
		hasUpdate = true
	}
	if req.Type != nil {
		q = q.Set("type", *req.Type)
		hasUpdate = true
	}
	if req.Username != nil {
		q = q.Set("username", *req.Username)
		hasUpdate = true
	}
	if req.Password != nil {
		pw := *req.Password
		if pw != "" {
			if encrypted, err := crypto.Encrypt(pw); err == nil {
				pw = encrypted
			}
		}
		q = q.Set("password", pw)
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

	return GetRegistryByID(engine, id)
}

func DeleteRegistry(engine db.Queryable, id int) error {
	_, err := engine.Exec("UPDATE registries SET active = 0 WHERE id = ?", id)
	return err
}
