package structs

import "time"

type Stack struct {
	ID                 int       `json:"id"`
	Name               string    `json:"name"`
	Description        *string   `json:"description"`
	WorkerID           *int      `json:"worker_id"`
	Status             string    `json:"status"`
	DeploymentStrategy string    `json:"deployment_strategy"`
	AutoDeploy         bool      `json:"auto_deploy"`
	Active             bool      `json:"active"`
	UpdatedAt          time.Time `json:"updated_at"`
	InsertedAt         time.Time `json:"inserted_at"`
}
