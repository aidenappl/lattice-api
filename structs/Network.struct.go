package structs

import "time"

type Network struct {
	ID         int       `json:"id"`
	StackID    int       `json:"stack_id"`
	Name       string    `json:"name"`
	Driver     string    `json:"driver"`
	Subnet     *string   `json:"subnet"`
	Options    *string   `json:"options"`
	UpdatedAt  time.Time `json:"updated_at"`
	InsertedAt time.Time `json:"inserted_at"`
}
