package structs

import "time"

type Volume struct {
	ID         int       `json:"id"`
	StackID    int       `json:"stack_id"`
	Name       string    `json:"name"`
	Driver     string    `json:"driver"`
	MountPath  *string   `json:"mount_path"`
	Options    *string   `json:"options"`
	UpdatedAt  time.Time `json:"updated_at"`
	InsertedAt time.Time `json:"inserted_at"`
}
