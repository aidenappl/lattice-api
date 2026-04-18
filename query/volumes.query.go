package query

import (
	"fmt"

	sq "github.com/Masterminds/squirrel"
	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/structs"
)

var volumeColumns = []string{
	"volumes.id",
	"volumes.stack_id",
	"volumes.name",
	"volumes.driver",
	"volumes.mount_path",
	"volumes.options",
	"volumes.updated_at",
	"volumes.inserted_at",
}

func scanVolume(row scanner) (*structs.Volume, error) {
	var v structs.Volume
	err := row.Scan(&v.ID, &v.StackID, &v.Name, &v.Driver, &v.MountPath, &v.Options, &v.UpdatedAt, &v.InsertedAt)
	return &v, err
}

func ListVolumesByStack(engine db.Queryable, stackID int) (*[]structs.Volume, error) {
	q := sq.Select(volumeColumns...).From("volumes").Where(sq.Eq{"volumes.stack_id": stackID}).OrderBy("volumes.id ASC")

	qStr, args, err := q.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}

	rows, err := engine.Query(qStr, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to execute sql query: %w", err)
	}
	defer rows.Close()

	var volumes []structs.Volume
	for rows.Next() {
		v, err := scanVolume(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan volume: %w", err)
		}
		volumes = append(volumes, *v)
	}

	return &volumes, rows.Err()
}

type CreateVolumeRequest struct {
	StackID   int
	Name      string
	Driver    string
	MountPath *string
	Options   *string
}

func CreateVolume(engine db.Queryable, req CreateVolumeRequest) error {
	q := sq.Insert("volumes").
		Columns("stack_id", "name", "driver", "mount_path", "options").
		Values(req.StackID, req.Name, req.Driver, req.MountPath, req.Options)

	qStr, args, err := q.ToSql()
	if err != nil {
		return fmt.Errorf("failed to build sql query: %w", err)
	}

	_, err = engine.Exec(qStr, args...)
	return err
}

func DeleteVolumesByStack(engine db.Queryable, stackID int) error {
	_, err := engine.Exec("DELETE FROM volumes WHERE stack_id = ?", stackID)
	return err
}
