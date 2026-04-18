package structs

import "time"

type Deployment struct {
	ID          int        `json:"id"`
	StackID     int        `json:"stack_id"`
	Status      string     `json:"status"`
	Strategy    string     `json:"strategy"`
	TriggeredBy *int       `json:"triggered_by"`
	ApprovedBy  *int       `json:"approved_by"`
	StartedAt   *time.Time `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	InsertedAt  time.Time  `json:"inserted_at"`
}

type DeploymentContainer struct {
	ID            int       `json:"id"`
	DeploymentID  int       `json:"deployment_id"`
	ContainerID   int       `json:"container_id"`
	Image         string    `json:"image"`
	Tag           string    `json:"tag"`
	PreviousImage *string   `json:"previous_image"`
	PreviousTag   *string   `json:"previous_tag"`
	Status        string    `json:"status"`
	UpdatedAt     time.Time `json:"updated_at"`
	InsertedAt    time.Time `json:"inserted_at"`
}
