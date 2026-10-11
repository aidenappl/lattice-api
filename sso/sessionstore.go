package sso

import (
	"context"
	"fmt"

	ssolib "github.com/aidenappl/go-forta/sso"
	"github.com/aidenappl/lattice-api/crypto"
	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/query"
)

// SessionStore implements ssolib.SessionStore over the sso_sessions table, with
// AES-256-GCM encryption at rest.
//
// ⚠️ UNTIL THIS CHANGE, sso_sessions WAS NEVER CREATED BY ANY MIGRATION. Fifty-one
// migrate() statements in db/db.go and not one of them made this table. So
// GetSSOSession failed with "table doesn't exist" on every request, and
// checkpointSSOGrant caught that as a generic DB error and returned true —
// an UNBOUNDED fail-open, in a function whose own doc comment promised the opposite.
// The revocation checkpoint had therefore never run in production; the only symptom
// was a repeated warning log. db/db.go now creates the table.
//
// It also could not have worked for a second, independent reason: `introspect_url`
// was read by LoadConfig but no handler ever wrote it, so there was no endpoint to
// call. That is fixed in HandleSSOConfig in the same change.
type SessionStore struct{}

// NewSessionStore returns a SessionStore over the package-level DB handle.
func NewSessionStore() *SessionStore { return &SessionStore{} }

// SaveSession encrypts and upserts the IdP tokens for a user.
func (s *SessionStore) SaveSession(_ context.Context, userID int64, sess ssolib.Session) error {
	encAccess, err := crypto.Encrypt(sess.Tokens.AccessToken)
	if err != nil {
		return fmt.Errorf("sso: encrypt access token: %w", err)
	}

	encRefresh := ""
	if sess.Tokens.RefreshToken != "" {
		encRefresh, err = crypto.Encrypt(sess.Tokens.RefreshToken)
		if err != nil {
			return fmt.Errorf("sso: encrypt refresh token: %w", err)
		}
	}

	// Subject and SID are persisted even though nothing reads them until a logout
	// token arrives. Neither can be added later: `sid` lives only in the id_token
	// of the login that created this row, and it stays empty while sso.issuer_url
	// is unset because the OAuth2 adapter has no id_token at all.
	return query.UpsertSSOSession(db.DB, userID, ProviderSlug, sess.Subject, sess.SID, encAccess, encRefresh)
}

// LoadSession returns the decrypted session, or (nil, nil) when the user has none.
//
// ⚠️ (nil, nil) MUST NOT BECOME AN ERROR. Lattice has local accounts —
// admin@lattice.local among them — and a local login has no row here. The
// checkpoint reads (nil, nil) as "not an SSO session, pass"; returning an error
// would deny every local login, including the break-glass admin.
func (s *SessionStore) LoadSession(_ context.Context, userID int64) (*ssolib.Session, error) {
	row, err := query.GetSSOSession(db.DB, userID)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, nil
	}

	access, err := crypto.Decrypt(row.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("sso: decrypt access token for user %d: %w", userID, err)
	}

	refresh := ""
	if row.RefreshToken != "" {
		refresh, err = crypto.Decrypt(row.RefreshToken)
		if err != nil {
			return nil, fmt.Errorf("sso: decrypt refresh token for user %d: %w", userID, err)
		}
	}

	return &ssolib.Session{
		Provider:      ProviderSlug,
		Subject:       derefOr(row.Subject),
		SID:           derefOr(row.SID),
		Tokens:        ssolib.TokenSet{AccessToken: access, RefreshToken: refresh},
		LastCheckedAt: row.LastCheckedAt,
	}, nil
}

// derefOr flattens a nullable column. NULL and "" mean the same thing to every
// caller here: this session cannot be addressed that way.
func derefOr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// ─────────────────────────────────────────────────────────────────────────────
// BackchannelLogoutTarget — the receiving half of OIDC Back-Channel Logout 1.0.
//
// ⚠️ DELETING THE ROW IS NOT ENOUGH, FOR EXACTLY THE REASON RevokeLocalTokens
// EXISTS BELOW. Lattice issues its own access and refresh JWTs, validated locally
// with no lookup against sso_sessions, so they outlive the row. A back-channel
// logout that only deleted rows would end nothing the user notices — the same bug
// the checkpoint path already had, arriving through a new door.
//
// go-forta's handler does not call RevokeLocalTokens and has no way to know it is
// needed, so stamping tokens_revoked_at is this implementation's job. The user ids
// are read BEFORE the delete because afterwards they are unrecoverable.
//
// ⚠️ Both methods return (0, nil) for "nothing matched", never an error: a
// duplicate delivery, an expired session and a pre-column row all land here and
// all are normal.
// ─────────────────────────────────────────────────────────────────────────────

// DeleteSessionsBySID ends the single session with this OIDC session id.
func (s *SessionStore) DeleteSessionsBySID(ctx context.Context, provider, sid string) (int, error) {
	ids, err := query.SSOSessionUserIDsBySID(db.DB, provider, sid)
	if err != nil {
		return 0, err
	}
	n, err := query.DeleteSSOSessionsBySID(db.DB, provider, sid)
	if err != nil {
		return 0, err
	}
	return n, s.revokeAll(ctx, ids)
}

// DeleteSessionsBySubject ends every session this subject holds with the provider.
func (s *SessionStore) DeleteSessionsBySubject(ctx context.Context, provider, subject string) (int, error) {
	ids, err := query.SSOSessionUserIDsBySubject(db.DB, provider, subject)
	if err != nil {
		return 0, err
	}
	n, err := query.DeleteSSOSessionsBySubject(db.DB, provider, subject)
	if err != nil {
		return 0, err
	}
	return n, s.revokeAll(ctx, ids)
}

// revokeAll stamps tokens_revoked_at for every affected user.
//
// The error is returned, not swallowed: the session row is already gone, so if
// this does not land the user keeps working local tokens and the revocation
// silently did nothing. Returning it makes the receiver answer 500, which makes
// the provider RETRY — the one case where retrying is exactly right.
func (s *SessionStore) revokeAll(ctx context.Context, userIDs []int64) error {
	for _, id := range userIDs {
		if err := s.RevokeLocalTokens(ctx, id); err != nil {
			return fmt.Errorf("sso: revoke local tokens for user %d: %w", id, err)
		}
	}
	return nil
}

// TouchSession resets the checkpoint interval after a successful check.
func (s *SessionStore) TouchSession(_ context.Context, userID int64) error {
	return query.TouchSSOSession(db.DB, userID)
}

// DeleteSession removes the SSO session row.
func (s *SessionStore) DeleteSession(_ context.Context, userID int64) error {
	return query.DeleteSSOSession(db.DB, userID)
}

// RevokeLocalTokens satisfies ssolib.LocalTokenRevoker.
//
// ─────────────────────────────────────────────────────────────────────────────
// ⚠️ THIS IS WHAT ACTUALLY LOCKS A REVOKED USER OUT, AND LATTICE CANNOT SKIP IT.
//
// Lattice issues its OWN access and refresh JWTs, validated locally with no
// database read of the session row. So deleting sso_sessions on its own achieves
// nothing: the user's existing Lattice JWTs keep working for their full lifetime,
// and worse, the next request finds no session row and takes the "not an SSO
// session, pass" branch — turning a detected revocation into a permanent pass.
//
// Stamping tokens_revoked_at is the mechanism that bites: validateLatticeToken
// rejects any token issued before that timestamp, so revocation is immediate
// across every token the user holds.
//
// This is precisely the failure the library's LocalTokenRevoker hook exists for,
// and lattice is the service that demonstrates why it is not optional.
// ─────────────────────────────────────────────────────────────────────────────
func (s *SessionStore) RevokeLocalTokens(_ context.Context, userID int64) error {
	return query.RevokeUserTokens(db.DB, int(userID))
}
