package structs

import "time"

type WebhookConfig struct {
	ID         int       `json:"id"`
	Name       string    `json:"name"`
	URL        string    `json:"url"`
	Events     string    `json:"events"`     // JSON array of event types
	Active     bool      `json:"active"`
	Secret     *string   `json:"secret"`     // HMAC signing secret
	UpdatedAt  time.Time `json:"updated_at"`
	InsertedAt time.Time `json:"inserted_at"`
}
