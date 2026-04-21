package structs

import "time"

type Template struct {
	ID          int       `json:"id"`
	Name        string    `json:"name"`
	Description *string   `json:"description"`
	Config      string    `json:"config"`
	CreatedBy   *int      `json:"created_by"`
	Active      bool      `json:"active"`
	UpdatedAt   time.Time `json:"updated_at"`
	InsertedAt  time.Time `json:"inserted_at"`
}
