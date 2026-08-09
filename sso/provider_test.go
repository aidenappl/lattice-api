package sso

import (
	"testing"

	ssolib "github.com/aidenappl/go-forta/sso"
)

// TestProviderKindFollowsIssuer pins the security-relevant half of the OAuth2 →
// OIDC upgrade.
//
// The two adapters are not interchangeable. KindOAuth2 has no id_token, so
// identity arrives from an unsigned UserInfo call — anything able to obtain an
// access token can become that user — and there is no `sid`, so session-scoped
// back-channel logout is impossible. KindOIDC verifies a signed id_token, checks
// the nonce, and yields a `sid`.
//
// The OAuth2 fallback exists so an existing deployment survives the upgrade,
// which makes it easy to leave configured by accident. This states which input
// produces which adapter so that stays a visible choice.
func TestProviderKindFollowsIssuer(t *testing.T) {
	t.Run("issuer_set_upgrades_to_oidc", func(t *testing.T) {
		c := &SSOConfig{IssuerURL: "https://auth.appleby.cloud"}
		if got := c.Provider().Kind; got != ssolib.KindOIDC {
			t.Fatalf("Kind = %v with an issuer configured, want %v. Without the OIDC adapter "+
				"identity comes from an unsigned UserInfo response and no `sid` is available "+
				"for back-channel logout.", got, ssolib.KindOIDC)
		}
	})

	t.Run("no_issuer_stays_oauth2", func(t *testing.T) {
		c := &SSOConfig{AuthorizeURL: "https://auth.example/authorize"}
		if got := c.Provider().Kind; got != ssolib.KindOAuth2 {
			t.Fatalf("Kind = %v with no issuer, want %v. The fallback is what keeps an "+
				"existing deployment working across this upgrade.", got, ssolib.KindOAuth2)
		}
	})

	t.Run("whitespace_only_issuer_is_not_an_issuer", func(t *testing.T) {
		// An admin field submitted blank arrives as spaces. Treating that as
		// configured would build an OIDC adapter whose discovery cannot resolve,
		// failing every login instead of falling back.
		if got := (&SSOConfig{IssuerURL: "   "}).Provider().Kind; got != ssolib.KindOAuth2 {
			t.Fatalf("Kind = %v for a whitespace-only issuer, want %v", got, ssolib.KindOAuth2)
		}
	})

	// ⚠️ THE REGRESSION THAT BROKE EVERY LOGIN ON 2026-08-09.
	//
	// forta-api's id_token carries no email claim — OIDC Core §2 wants `sub`
	// stable and never reassigned, so Forta declines to put a reassignable
	// address beside it. This service REFUSES a login with no email
	// (sso_no_email). Under KindOAuth2 that never showed, because UserInfo was
	// the only identity source; under KindOIDC the adapter calls UserInfo only
	// when FetchUserInfo is set. Switching to OIDC without it turned every login
	// into "the SSO provider did not return an email address".
	t.Run("oidc_still_fetches_userinfo_for_the_email", func(t *testing.T) {
		c := &SSOConfig{IssuerURL: "https://auth.appleby.cloud"}
		if !c.Provider().FetchUserInfo {
			t.Fatal("FetchUserInfo is false under OIDC. Forta's id_token has no email claim " +
				"and this service refuses a login without one, so every SSO login fails with " +
				"sso_no_email. The library still enforces the OIDC Core §5.3.2 sub check on " +
				"the UserInfo response, so this adds claims without weakening identity.")
		}
	})

	t.Run("subject_claim_stays_empty", func(t *testing.T) {
		// ⚠️ Regression guard. sso.user_identifier used to name the claim treated as
		// identity and defaulted to "email"; reconnecting it here would key identity
		// on a reassignable address — an account-takeover primitive.
		c := &SSOConfig{IssuerURL: "https://auth.appleby.cloud", UserIdentifier: "email"}
		if got := c.Provider().SubjectClaim; got != "" {
			t.Fatalf("SubjectClaim = %q — user_identifier has been reconnected. Identity must "+
				"key on the standard `sub`, never on an email address.", got)
		}
	})
}
