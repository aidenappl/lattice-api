package sso

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync/atomic"

	oidcsession "github.com/aidenappl/go-oidc-session"
	"github.com/aidenappl/go-oidc-session/sqlstore"
	"github.com/aidenappl/lattice-api/crypto"
	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/env"
	"github.com/aidenappl/lattice-api/logger"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/structs"
)

// SessionCookie is the browser's SSO session cookie: opaque, HttpOnly, set on
// and only sent to the API host. The provider tokens it stands for stay in
// oidc_sessions, encrypted with crypto (the same key as every other secret).
const SessionCookie = "lattice_session"

// current is the live Manager. Rebuilt by Reload when the SSO settings change,
// so a new post-login URL or provider applies without a restart.
var current atomic.Pointer[oidcsession.Manager]

// OnLogin is called after every SSO sign-in. main wires the audit log here
// (routers cannot be imported from this package).
var OnLogin func(r *http.Request, user *structs.User)

// Manager returns the live SSO session manager.
func Manager() *oidcsession.Manager {
	if m := current.Load(); m != nil {
		return m
	}
	Reload()
	return current.Load()
}

// Store is the session/state store over the main database.
func Store() *sqlstore.Store {
	return &sqlstore.Store{
		DB:     db.DB,
		Sealer: cryptoSealer{},
		Logf: func(format string, args ...any) {
			logger.Warn("sso", "session store: "+fmt.Sprintf(format, args...))
		},
	}
}

// Reload rebuilds the Manager from the current SSO settings.
func Reload() {
	cfg := LoadConfig()
	frontend := cfg.PostLoginRedirectURL()
	store := Store()

	current.Store(&oidcsession.Manager{
		Providers: func(context.Context, string) (*oidcsession.Provider, error) {
			c := LoadConfig()
			if !c.Enabled || c.ClientID == "" {
				return nil, errors.New("sso is not configured")
			}
			return c.Provider(), nil
		},
		DefaultProvider:  ProviderSlug,
		States:           store,
		Sessions:         store,
		Users:            resolver{},
		CookieName:       SessionCookie,
		Insecure:         env.Environment != "production",
		DefaultReturnURL: frontend,
		LoginPageURL:     loginPageURL(frontend),
		// signed_out=1 stops the login page from immediately signing the user
		// back in silently. Register this exact URL as a logout redirect URI
		// with the provider, or it will not be honoured.
		PostLogoutURL: loginPageURL(frontend) + "?signed_out=1",
		OnLogin: func(_ context.Context, r *http.Request, s *oidcsession.Session, _ oidcsession.Identity) {
			if OnLogin == nil {
				return
			}
			if u, err := query.GetUserByID(db.DB, int(s.UserID)); err == nil && u != nil {
				OnLogin(r, u)
			}
		},
		OnEvent: func(ctx context.Context, e oidcsession.Event) {
			if e.Warn {
				logger.WarnCtx(ctx, "sso", e.Name, e.Fields)
				return
			}
			logger.InfoCtx(ctx, "sso", e.Name, e.Fields)
		},
		Logf: func(ctx context.Context, format string, args ...any) {
			logger.WarnCtx(ctx, "sso", "sso diagnostic", logger.F{"detail": fmt.Sprintf(format, args...)})
		},
	})
}

// loginPageURL is the frontend's /login on the same origin as the post-login URL.
func loginPageURL(frontend string) string {
	u, err := url.Parse(frontend)
	if err != nil {
		return "/login"
	}
	u.Path, u.RawQuery = "/login", ""
	return u.String()
}

// cryptoSealer encrypts session tokens with lattice's AES-GCM key.
type cryptoSealer struct{}

func (cryptoSealer) Seal(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	return crypto.Encrypt(plaintext)
}

func (cryptoSealer) Open(sealed string) (string, error) {
	if sealed == "" {
		return "", nil
	}
	return crypto.Decrypt(sealed)
}

// resolver maps a verified identity onto a Lattice user. Order: the stored
// provider subject, then an SSO user with the same email (healing its subject),
// then auto-provisioning a pending user when enabled.
type resolver struct{}

func (resolver) ResolveUser(ctx context.Context, p *oidcsession.Provider, id oidcsession.Identity) (int64, error) {
	if id.Email == "" {
		return 0, &oidcsession.LoginError{Code: "sso_no_email"}
	}
	name, picture := "", ""
	if id.Name != nil {
		name = *id.Name
	}
	if id.Picture != nil {
		picture = *id.Picture
	}

	var user *structs.User
	if id.Subject != "" {
		u, err := query.GetUserBySSOSubject(db.DB, id.Subject)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return 0, fmt.Errorf("lookup by subject: %w", err)
		}
		if err == nil {
			user = u
		}
	}
	if user == nil {
		u, err := query.GetUserByEmailAndAuthType(db.DB, id.Email, "sso")
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return 0, fmt.Errorf("lookup by email: %w", err)
		}
		if err == nil {
			user = u
		}
	}

	if user == nil {
		if !p.AutoProvision {
			logger.InfoCtx(ctx, "sso", "user not found and auto-provisioning disabled", logger.F{"email": id.Email})
			return 0, &oidcsession.LoginError{Code: "sso_no_account"}
		}
		subject := id.Subject
		created, err := query.CreateUser(db.DB, query.CreateUserRequest{
			Email:           id.Email,
			Name:            &name,
			AuthType:        "sso",
			SSOSubject:      &subject,
			ProfileImageURL: &picture,
			Role:            "pending",
		})
		if err != nil {
			return 0, fmt.Errorf("create user: %w", err)
		}
		logger.InfoCtx(ctx, "sso", "auto-provisioned user", logger.F{"email": id.Email, "user_id": created.ID, "sso_subject": subject})
		user = created
	} else if id.Subject != "" && (user.SSOSubject == nil || LooksLikeEmail(*user.SSOSubject)) {
		// Older rows stored the email as the subject; replace it with the real
		// OIDC sub so the subject lookup above finds the user next time.
		if err := query.UpdateUserSSOSubject(db.DB, user.ID, id.Subject); err != nil {
			logger.ErrorCtx(ctx, "sso", "failed to heal sso_subject", logger.F{"user_id": user.ID, "error": err})
		}
	}

	if picture != "" {
		if _, err := query.UpdateUser(db.DB, user.ID, query.UpdateUserRequest{ProfileImageURL: &picture}); err != nil && !errors.Is(err, query.ErrNoChanges) {
			logger.WarnCtx(ctx, "sso", "could not update profile image", logger.F{"user_id": user.ID, "error": err})
		}
	}

	if !user.Active {
		return 0, &oidcsession.LoginError{Code: "account_disabled"}
	}
	return int64(user.ID), nil
}

func (resolver) LinkIdentity(context.Context, *oidcsession.Provider, int64, oidcsession.Identity) error {
	return errors.New("account linking is not supported")
}
