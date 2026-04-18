package query

import (
	"fmt"

	sq "github.com/Masterminds/squirrel"
	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/structs"
)

var deploymentContainerColumns = []string{
	"deployment_containers.id",
	"deployment_containers.deployment_id",
	"deployment_containers.container_id",
	"deployment_containers.image",
	"deployment_containers.tag",
	"deployment_containers.previous_image",
	"deployment_containers.previous_tag",
	"deployment_containers.status",
	"deployment_containers.updated_at",
	"deployment_containers.inserted_at",
}

func scanDeploymentContainer(row scanner) (*structs.DeploymentContainer, error) {
	var dc structs.DeploymentContainer
	err := row.Scan(
		&dc.ID,
		&dc.DeploymentID,
		&dc.ContainerID,
		&dc.Image,
		&dc.Tag,
		&dc.PreviousImage,
		&dc.PreviousTag,
		&dc.Status,
		&dc.UpdatedAt,
		&dc.InsertedAt,
	)
	return &dc, err
}

func ListDeploymentContainers(engine db.Queryable, deploymentID int) (*[]structs.DeploymentContainer, error) {
	q := sq.Select(deploymentContainerColumns...).
		From("deployment_containers").
		Where(sq.Eq{"deployment_containers.deployment_id": deploymentID}).
		OrderBy("deployment_containers.id ASC")

	qStr, args, err := q.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build sql query: %w", err)
	}

	rows, err := engine.Query(qStr, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to execute sql query: %w", err)
	}
	defer rows.Close()

	var dcs []structs.DeploymentContainer
	for rows.Next() {
		dc, err := scanDeploymentContainer(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan deployment container: %w", err)
		}
		dcs = append(dcs, *dc)
	}

	return &dcs, rows.Err()
}

type CreateDeploymentContainerRequest struct {
	DeploymentID  int
	ContainerID   int
	Image         string
	Tag           string
	PreviousImage *string
	PreviousTag   *string
}

func CreateDeploymentContainer(engine db.Queryable, req CreateDeploymentContainerRequest) (*structs.DeploymentContainer, error) {
	q := sq.Insert("deployment_containers").
		Columns("deployment_id", "container_id", "image", "tag", "previous_image", "previous_tag").
		Values(req.DeploymentID, req.ContainerID, req.Image, req.Tag, req.PreviousImage, req.PreviousTag)

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

	q2 := sq.Select(deploymentContainerColumns...).From("deployment_containers").Where(sq.Eq{"id": id})
	qStr2, args2, _ := q2.ToSql()
	row := engine.QueryRow(qStr2, args2...)
	return scanDeploymentContainer(row)
}

func UpdateDeploymentContainerStatus(engine db.Queryable, id int, status string) error {
	_, err := engine.Exec("UPDATE deployment_containers SET status = ? WHERE id = ?", status, id)
	return err
}
