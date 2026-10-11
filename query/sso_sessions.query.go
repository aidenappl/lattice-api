package query

import (
	"database/sql"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/structs"
)

// UpsertSSOSession inserts or replaces the SSO session row for a user.
// Tokens must be encrypted before passing in.
func UpsertSSOSession(engine db.Queryable, userID int64, provider, subject, sid, encAccessToken, encRefreshToken string) error {
	const stmt = `
		INSERT INTO sso_sessions (user_id, provider, subject, sid, access_token, refresh_token)
		VALUES (?, ?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE
			provider = VALUES(provider),
			subject = VALUES(subject),
			sid = VALUES(sid),
			access_token = VALUES(access_token),
			refresh_token = VALUES(refresh_token),
			last_checked_at = CURRENT_TIMESTAMP
	`
	_, err := engine.Exec(stmt, userID, provider, nullIfEmpty(subject), nullIfEmpty(sid), encAccessToken, encRefreshToken)
	return err
}

// nullIfEmpty stores "" as SQL NULL.
//
// ⚠️ Load-bearing for `sid`: a row holding the empty string would MATCH a lookup
// built from a logout token that named no session, so a subject-wide event could
// end a session it never addressed. NULL never matches an equality test.
func nullIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// SSOSessionUserIDsBySID returns the user ids of sessions matching this `sid`.
//
// ⚠️ READ BEFORE DELETE — the ids are needed AFTER the rows are gone. Deleting a
// lattice sso_sessions row does not end access on its own: Lattice issues its own
// JWTs, which outlive the row. The caller must stamp tokens_revoked_at for each
// id and cannot recover them once deleted.
func SSOSessionUserIDsBySID(engine db.Queryable, provider, sid string) ([]int64, error) {
	if provider == "" || sid == "" {
		return nil, nil
	}
	return ssoSessionUserIDs(engine, "SELECT user_id FROM sso_sessions WHERE provider = ? AND sid = ?", provider, sid)
}

// SSOSessionUserIDsBySubject is the subject-wide equivalent.
func SSOSessionUserIDsBySubject(engine db.Queryable, provider, subject string) ([]int64, error) {
	if provider == "" || subject == "" {
		return nil, nil
	}
	return ssoSessionUserIDs(engine, "SELECT user_id FROM sso_sessions WHERE provider = ? AND subject = ?", provider, subject)
}

func ssoSessionUserIDs(engine db.Queryable, stmt string, args ...interface{}) ([]int64, error) {
	rows, err := engine.Query(stmt, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// DeleteSSOSessionsBySID removes the session named by `sid`. Scoped by provider:
// `sid` is unique only within an issuer.
func DeleteSSOSessionsBySID(engine db.Queryable, provider, sid string) (int, error) {
	if provider == "" || sid == "" {
		return 0, nil
	}
	res, err := engine.Exec("DELETE FROM sso_sessions WHERE provider = ? AND sid = ?", provider, sid)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// DeleteSSOSessionsBySubject removes every session a subject holds with one
// provider. Zero rows is NORMAL — expired, already logged out, or a row predating
// the subject column.
func DeleteSSOSessionsBySubject(engine db.Queryable, provider, subject string) (int, error) {
	if provider == "" || subject == "" {
		return 0, nil
	}
	res, err := engine.Exec("DELETE FROM sso_sessions WHERE provider = ? AND subject = ?", provider, subject)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// GetSSOSession returns the SSO session row for a user, or nil if none exists.
func GetSSOSession(engine db.Queryable, userID int64) (*structs.SSOSession, error) {
	const stmt = `
		SELECT user_id, provider, subject, sid, access_token, refresh_token, last_checked_at, inserted_at
		FROM sso_sessions
		WHERE user_id = ?
	`
	row := engine.QueryRow(stmt, userID)
	s := &structs.SSOSession{}
	if err := row.Scan(&s.UserID, &s.Provider, &s.Subject, &s.SID, &s.AccessToken, &s.RefreshToken, &s.LastCheckedAt, &s.InsertedAt); err != nil {
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
