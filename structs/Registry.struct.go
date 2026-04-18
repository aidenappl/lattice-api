package structs

import "time"

type Registry struct {
	ID              int       `json:"id"`
	Name            string    `json:"name"`
	URL             string    `json:"url"`
	Type            string    `json:"type"`
	KeyringSecretKey *string  `json:"keyring_secret_key"`
	AuthConfig      *string   `json:"-"`
	Active          bool      `json:"active"`
	UpdatedAt       time.Time `json:"updated_at"`
	InsertedAt      time.Time `json:"inserted_at"`
}
