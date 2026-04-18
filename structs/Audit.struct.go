package structs

import "time"

type AuditLogEntry struct {
	ID           int       `json:"id"`
	UserID       *int      `json:"user_id"`
	Action       string    `json:"action"`
	ResourceType string    `json:"resource_type"`
	ResourceID   *int      `json:"resource_id"`
	Details      *string   `json:"details"`
	IPAddress    *string   `json:"ip_address"`
	InsertedAt   time.Time `json:"inserted_at"`
}
