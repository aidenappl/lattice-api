package query

import (
	"fmt"

	sq "github.com/Masterminds/squirrel"
	"github.com/aidenappl/lattice-api/crypto"
	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/structs"
)

var backupDestinationColumns = []string{
	"backup_destinations.id",
	"backup_destinations.name",
	"backup_destinations.type",
	"backup_destinations.config",
	"backup_destinations.active",
	"backup_destinations.updated_at",
	"backup_destinations.inserted_at",
}

func scanBackupDestination(row scanner) (*structs.BackupDestination, error) {
	var b structs.BackupDestination
	err := row.Scan(
		&b.ID,
		&b.Name,
		&b.Type,
		&b.Config,
		&b.Active,
		&b.UpdatedAt,
		&b.InsertedAt,
	)
	if err == nil && b.Config != nil && *b.Config != "" {
		decrypted, _ := crypto.Decrypt(*b.Config)
		b.Config = &decrypted
	}
	return &b, err
}

func ListBackupDestinations(engine db.Queryable) (*[]structs.BackupDestination, error) {
	q := sq.Select(backupDestinationColumns...).
		From("backup_destinations").
		Where(sq.Eq{"backup_destinations.active": true}).
		OrderBy("backup_destinations.id DESC")

	qStr, args, err := q.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}

	rows, err := engine.Query(qStr, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to execute sql query: %w", err)
	}
	defer rows.Close()

	var destinations []structs.BackupDestination
	for rows.Next() {
		b, err := scanBackupDestination(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan backup destination: %w", err)
		}
		destinations = append(destinations, *b)
	}

	return &destinations, rows.Err()
}

func GetBackupDestinationByID(engine db.Queryable, id int) (*structs.BackupDestination, error) {
	q := sq.Select(backupDestinationColumns...).From("backup_destinations").Where(sq.Eq{"backup_destinations.id": id})

	qStr, args, err := q.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}

	row := engine.QueryRow(qStr, args...)
	b, err := scanBackupDestination(row)
	if err != nil {
		return nil, fmt.Errorf("failed to scan backup destination: %w", err)
	}

	return b, nil
}

type CreateBackupDestinationRequest struct {
	Name   string
	Type   string
	Config string
}

func CreateBackupDestination(engine db.Queryable, req CreateBackupDestinationRequest) (*structs.BackupDestination, error) {
	encConfig := req.Config
	if encConfig != "" {
		encrypted, err := crypto.Encrypt(encConfig)
		if err == nil {
			encConfig = encrypted
		}
	}

	q := sq.Insert("backup_destinations").
		Columns("name", "type", "config").
		Values(req.Name, req.Type, encConfig)

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

	return GetBackupDestinationByID(engine, int(id))
}

type UpdateBackupDestinationRequest struct {
	Name   *string
	Type   *string
	Config *string
	Active *bool
}

func UpdateBackupDestination(engine db.Queryable, id int, req UpdateBackupDestinationRequest) (*structs.BackupDestination, error) {
	q := sq.Update("backup_destinations").Where(sq.Eq{"id": id})

	hasUpdate := false
	if req.Name != nil {
		q = q.Set("name", *req.Name)
		hasUpdate = true
	}
	if req.Type != nil {
		q = q.Set("type", *req.Type)
		hasUpdate = true
	}
	if req.Config != nil {
		cfg := *req.Config
		if cfg != "" {
			if encrypted, err := crypto.Encrypt(cfg); err == nil {
				cfg = encrypted
			}
		}
		q = q.Set("config", cfg)
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

	return GetBackupDestinationByID(engine, id)
}

func DeleteBackupDestination(engine db.Queryable, id int) error {
	_, err := engine.Exec("UPDATE backup_destinations SET active = 0 WHERE id = ?", id)
	return err
}
