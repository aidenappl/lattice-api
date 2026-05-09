package structs

import "time"

type ApiToken struct {
	ID         int        `json:"id"`
	UserID     int        `json:"user_id"`
	Name       string     `json:"name"`
	TokenHash  string     `json:"-"`
	Scopes     *string    `json:"scopes"`
	ExpiresAt  *time.Time `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	Active     bool       `json:"active"`
	UpdatedAt  time.Time  `json:"updated_at"`
	InsertedAt time.Time  `json:"inserted_at"`
}
