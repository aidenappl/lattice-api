package structs

import "time"

type DeployToken struct {
	ID         int        `json:"id"`
	StackID    int        `json:"stack_id"`
	Name       string     `json:"name"`
	TokenHash  string     `json:"-"`
	LastUsedAt *time.Time `json:"last_used_at"`
	Active     bool       `json:"active"`
	UpdatedAt  time.Time  `json:"updated_at"`
	InsertedAt time.Time  `json:"inserted_at"`
}
