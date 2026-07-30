package sso

import (
	"strings"

	ssolib "github.com/aidenappl/go-forta/sso"
)

// ProviderSlug is the single provider identifier this service uses.
//
// ⚠️ Lattice is still SINGLE-PROVIDER, and that is a deliberate deferral rather
// than an oversight. The plan called for an `sso_providers` table here, matching
// monitor-core. Adding one is not just a migration: it changes the admin API's
// request and response shapes, which means lattice-web and lattice-mcp change with
// it — and the per-site SSO controls those front-ends need are Phase 7's subject.
// Shipping the table now would mean shipping it twice, or shipping an admin API
// nothing can drive.
//
// So the protocol correctness lands now, on the existing single-config model, and
// the table lands with the UI that configures it.
//
// The slug is recorded in `sso_sessions.provider` and is what the checkpoint looks
// a provider up by, so ⚠️ CHANGING IT ORPHANS EVERY STORED SESSION — those rows
// stop resolving to a provider and their users get re-checked as if new.
const ProviderSlug = "sso"

// Provider maps the stored SSOConfig onto the library's provider view.
//
// Kind is always KindOAuth2. Lattice configures explicit authorize/token/userinfo
// URLs rather than an issuer, so there is no discovery document to read and no
// id_token to verify.
//
// ⚠️ THAT IS A REAL LIMITATION, not a preference. With no id_token there is no
// signed assertion of identity: the subject comes from a bearer-token call to
// UserInfo, so anything that can obtain an access token can become that user. The
// upgrade is to store an issuer URL and switch to KindOIDC — forta-api publishes a
// conforming discovery document as of Phase 1, so the only thing missing is the
// config field. Worth doing when the provider table lands.
//
// PKCE applies either way and is enforced by the library, which is what stops a
// leaked authorization code being redeemable.
func (c *SSOConfig) Provider() *ssolib.Provider {
	return &ssolib.Provider{
		Slug:        ProviderSlug,
		DisplayName: c.ButtonLabel,
		Kind:        ssolib.KindOAuth2,

		AuthorizeURL:  c.AuthorizeURL,
		TokenURL:      c.TokenURL,
		UserInfoURL:   c.UserInfoURL,
		IntrospectURL: c.IntrospectURL,

		ClientID:     c.ClientID,
		ClientSecret: c.ClientSecret,

		Scopes:      c.Scopes,
		RedirectURL: c.RedirectURL,

		// ⚠️ SubjectClaim IS DELIBERATELY EMPTY, so the library reads the standard
		// `sub` and falls back to `id`.
		//
		// It is NOT wired to `sso.user_identifier`, and that omission is the fix for
		// the bug this migration exists to close. That setting is "email" in
		// production; the old GetUserIdentifier read it, fell back to "email"
		// regardless, and the callback stored the result in `users.sso_subject` under
		// a comment claiming it was the OIDC `sub`. Identity keyed on a reassignable
		// address is an account-takeover primitive: an address changes hands and the
		// new holder inherits the account.
		//
		// `user_identifier` is retained in SSOConfig only because the admin API still
		// returns it. Nothing reads it any more. Do not reconnect it here.
		SubjectClaim: "",

		EmailClaim:         "",
		EmailVerifiedClaim: "",

		// Lattice has no local-account-with-verified-email model to link against, so
		// auto-linking is off and cannot mislead: matching is on subject, then on
		// email among SSO-only accounts, which the callback does explicitly.
		AllowAutoLink: false,
		AutoProvision: c.AutoProvision,

		// forta-api's /oauth/userinfo does not assert email_verified, and Forta is a
		// provider we operate that verifies addresses itself. Trusting it here is a
		// statement about THAT provider, not about any user.
		TrustEmailVerified: true,
	}
}

// LooksLikeEmail reports whether a stored subject is actually an email address.
//
// It exists for exactly one reason: to recognise the rows the old
// GetUserIdentifier wrote, so the callback can heal them on the next login rather
// than requiring a manual backfill. At the time of the migration there was one
// such row in production.
//
// The test is deliberately crude — a real `sub` from forta-api is a UUID and can
// never contain "@", so anything containing one is a legacy value. It is not
// validating an address; it is distinguishing two generations of stored data.
func LooksLikeEmail(subject string) bool {
	return strings.Contains(subject, "@")
}
