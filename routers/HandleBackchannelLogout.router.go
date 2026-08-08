package routers

import (
	"context"
	"fmt"
	"log"
	"net/http"

	ssolib "github.com/aidenappl/go-forta/sso"
	"github.com/aidenappl/lattice-api/sso"
)

// backchannelLogout is the OIDC Back-Channel Logout 1.0 receiver.
//
// ─────────────────────────────────────────────────────────────────────────────
// WHAT IT BUYS, AND WHY THE CHECKPOINT REMAINS THE GUARANTEE
//
// The introspection checkpoint re-checks the upstream grant every five minutes,
// so without this a revoked grant keeps working for up to five. This closes the
// window: the provider POSTs a signed logout_token the instant a grant is
// revoked and the session ends on arrival.
//
// It does NOT replace the checkpoint. Back-channel logout is best-effort BY
// SPECIFICATION — notifications are lost, endpoints go down, retries exhaust — so
// the poll stays the guarantee. Do not relax CheckpointInterval because of this.
//
// ⚠️ REQUIRES sso.issuer_url. Verification needs the provider's JWKS, which only
// the OIDC adapter discovers; with an OAuth2 provider go-forta answers 501 rather
// than acting on a token it cannot verify. That refusal is correct — accepting an
// unverifiable POST here would let anyone who can reach this URL log out any user
// by guessing a subject.
//
// ⚠️ UNAUTHENTICATED IN THE ORDINARY SENSE: no cookie, no bearer token. Its
// authentication IS the signature. It is therefore exempt from CSRFMiddleware and
// must never move behind DualAuthMiddleware — the caller is the identity
// provider, which holds no Lattice session.
// ─────────────────────────────────────────────────────────────────────────────
var backchannelLogout = &ssolib.BackchannelLogout{
	// The SAME session store the checkpoint uses, which is what makes it a
	// BackchannelLogoutTarget — so push and poll end sessions through one path,
	// including stamping tokens_revoked_at. Without that, deleting the row leaves
	// Lattice's own JWTs working and the revocation does nothing visible.
	Sessions: sso.NewSessionStore(),
	Providers: func(_ context.Context, slug string) (*ssolib.Provider, error) {
		if slug != sso.ProviderSlug {
			return nil, fmt.Errorf("sso: unknown provider %q", slug)
		}
		// Re-resolved per call, so a rotated secret or a newly-set issuer takes
		// effect at the next notification rather than the next restart.
		return sso.LoadConfig().Provider(), nil
	},
	Logf: log.Printf,
}

// HandleBackchannelLogout serves the receiver.
func HandleBackchannelLogout(w http.ResponseWriter, r *http.Request) {
	backchannelLogout.Handler(sso.ProviderSlug).ServeHTTP(w, r)
}
