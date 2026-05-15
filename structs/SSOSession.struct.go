package structs

import "time"

// SSOSession holds the IDP tokens for a user who logged in via SSO, plus
// the timestamp of the last successful introspection. Tokens are stored
// encrypted at rest via crypto.Encrypt/Decrypt.
type SSOSession struct {
	UserID        int64     `json:"user_id"`
	AccessToken   string    `json:"-"`
	RefreshToken  string    `json:"-"`
	LastCheckedAt time.Time `json:"last_checked_at"`
	InsertedAt    time.Time `json:"inserted_at"`
}
