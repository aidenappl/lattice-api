package structs

import "time"

type DatabaseSnapshot struct {
	ID                  int        `json:"id"`
	DatabaseInstanceID  int        `json:"database_instance_id"`
	BackupDestinationID *int       `json:"backup_destination_id"`
	Filename            string     `json:"filename"`
	SizeBytes           *int64     `json:"size_bytes"`
	Engine              string     `json:"engine"`
	DatabaseName        string     `json:"database_name"`
	Status              string     `json:"status"`
	TriggerType         string     `json:"trigger_type"`
	ErrorMessage        *string    `json:"error_message"`
	CompletedAt         *time.Time `json:"completed_at"`
	Active              bool       `json:"active"`
	UpdatedAt           time.Time  `json:"updated_at"`
	InsertedAt          time.Time  `json:"inserted_at"`
}
