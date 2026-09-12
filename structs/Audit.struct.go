package structs

import "time"

type AuditLogEntry struct {
	ID           int    `json:"id"`
	UserID       *int   `json:"user_id"`
	Action       string `json:"action"`
	ResourceType string `json:"resource_type"`
	ResourceID   *int   `json:"resource_id"`
	// AutomationRunID is set when the action was performed by an automation run
	// acting as UserID. The actor is the pair: without it an automated redeploy
	// reads exactly like UserID clicking the button.
	AutomationRunID *int      `json:"automation_run_id"`
	Details         *string   `json:"details"`
	IPAddress       *string   `json:"ip_address"`
	InsertedAt      time.Time `json:"inserted_at"`
}
