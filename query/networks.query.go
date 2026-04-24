package query

import (
	"fmt"

	sq "github.com/Masterminds/squirrel"
	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/structs"
)

var networkColumns = []string{
	"networks.id",
	"networks.stack_id",
	"networks.name",
	"networks.driver",
	"networks.subnet",
	"networks.options",
	"networks.updated_at",
	"networks.inserted_at",
}

func scanNetwork(row scanner) (*structs.Network, error) {
	var n structs.Network
	err := row.Scan(&n.ID, &n.StackID, &n.Name, &n.Driver, &n.Subnet, &n.Options, &n.UpdatedAt, &n.InsertedAt)
	return &n, err
}

func ListNetworksByStack(engine db.Queryable, stackID int) (*[]structs.Network, error) {
	q := sq.Select(networkColumns...).From("networks").Where(sq.Eq{"networks.stack_id": stackID}).OrderBy("networks.id ASC")

	qStr, args, err := q.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}

	rows, err := engine.Query(qStr, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to execute sql query: %w", err)
	}
	defer rows.Close()

	var networks []structs.Network
	for rows.Next() {
		n, err := scanNetwork(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan network: %w", err)
		}
		networks = append(networks, *n)
	}

	return &networks, rows.Err()
}

type CreateNetworkRequest struct {
	StackID int
	Name    string
	Driver  string
	Subnet  *string
	Options *string
}

func CreateNetwork(engine db.Queryable, req CreateNetworkRequest) error {
	q := sq.Insert("networks").
		Columns("stack_id", "name", "driver", "subnet", "options").
		Values(req.StackID, req.Name, req.Driver, req.Subnet, req.Options)

	qStr, args, err := q.ToSql()
	if err != nil {
		return fmt.Errorf("failed to build sql query: %w", err)
	}

	_, err = engine.Exec(qStr, args...)
	return err
}

func ListAllNetworks(engine db.Queryable) ([]structs.Network, error) {
	q := sq.Select(networkColumns...).From("networks").OrderBy("networks.stack_id ASC", "networks.id ASC")

	qStr, args, err := q.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}

	rows, err := engine.Query(qStr, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to execute sql query: %w", err)
	}
	defer rows.Close()

	var networks []structs.Network
	for rows.Next() {
		n, err := scanNetwork(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan network: %w", err)
		}
		networks = append(networks, *n)
	}

	return networks, rows.Err()
}

func DeleteNetworksByStack(engine db.Queryable, stackID int) error {
	_, err := engine.Exec("DELETE FROM networks WHERE stack_id = ?", stackID)
	return err
}

func DeleteNetworkByID(engine db.Queryable, id int) error {
	_, err := engine.Exec("DELETE FROM networks WHERE id = ?", id)
	return err
}
