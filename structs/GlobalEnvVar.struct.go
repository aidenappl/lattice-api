package structs

import "time"

type GlobalEnvVar struct {
	ID             int       `json:"id"`
	Key            string    `json:"key"`
	Value          string    `json:"value"`
	IsSecret       bool      `json:"is_secret"`
	EncryptedValue string    `json:"-"`
	UpdatedAt      time.Time `json:"updated_at"`
	InsertedAt     time.Time `json:"inserted_at"`
}
