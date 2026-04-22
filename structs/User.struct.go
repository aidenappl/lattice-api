package structs

import "time"

type User struct {
	ID           int       `json:"id"`
	Email        string    `json:"email"`
	Name         *string   `json:"name"`
	AuthType     string    `json:"auth_type"`
	PasswordHash *string   `json:"-"`
	SSOSubject   *string   `json:"sso_subject,omitempty"`
	ProfileImageURL *string `json:"profile_image_url"`
	Role         string    `json:"role"`
	Active       bool      `json:"active"`
	UpdatedAt    time.Time `json:"updated_at"`
	InsertedAt   time.Time `json:"inserted_at"`
}
