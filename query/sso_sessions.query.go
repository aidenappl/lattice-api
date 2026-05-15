package query

import (
	"database/sql"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/structs"
)

// UpsertSSOSession inserts or replaces the SSO session row for a user.
// Tokens must be encrypted before passing in.
func UpsertSSOSession(engine db.Queryable, userID int64, encAccessToken, encRefreshToken string) error {
	const stmt = `
		INSERT INTO sso_sessions (user_id, access_token, refresh_token)
		VALUES (?, ?, ?)
		ON DUPLICATE KEY UPDATE
			access_token = VALUES(access_token),
			refresh_token = VALUES(refresh_token),
			last_checked_at = CURRENT_TIMESTAMP
	`
	_, err := engine.Exec(stmt, userID, encAccessToken, encRefreshToken)
	return err
}

// GetSSOSession returns the SSO session row for a user, or nil if none exists.
func GetSSOSession(engine db.Queryable, userID int64) (*structs.SSOSession, error) {
	const stmt = `
		SELECT user_id, access_token, refresh_token, last_checked_at, inserted_at
		FROM sso_sessions
		WHERE user_id = ?
	`
	row := engine.QueryRow(stmt, userID)
	s := &structs.SSOSession{}
	if err := row.Scan(&s.UserID, &s.AccessToken, &s.RefreshToken, &s.LastCheckedAt, &s.InsertedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return s, nil
}

// TouchSSOSession bumps last_checked_at to now() for a successful introspection.
func TouchSSOSession(engine db.Queryable, userID int64) error {
	_, err := engine.Exec("UPDATE sso_sessions SET last_checked_at = CURRENT_TIMESTAMP WHERE user_id = ?", userID)
	return err
}

// DeleteSSOSession removes the SSO session row (used on logout or revoke).
func DeleteSSOSession(engine db.Queryable, userID int64) error {
	_, err := engine.Exec("DELETE FROM sso_sessions WHERE user_id = ?", userID)
	return err
}
