package env

import (
	"fmt"
	"os"
)

var (
	Port        = getEnv("PORT", "8000")
	Environment = getEnv("ENVIRONMENT", "production")
	CoreDBDSN   = getEnvOrPanic("DATABASE_DSN")

	// JWT signing key for local auth tokens
	JWTSigningKey = getEnvOrPanic("JWT_SIGNING_KEY")

	// Bootstrap admin credentials (creates initial local admin if no users exist)
	LatticeAdminEmail    = getEnv("LATTICE_ADMIN_EMAIL", "")
	LatticeAdminPassword = getEnv("LATTICE_ADMIN_PASSWORD", "")

	// Forta OAuth2 configuration (optional — only needed when Forta is running)
	FortaAPIDomain          = getEnv("FORTA_API_DOMAIN", "")
	FortaLoginDomain        = getEnv("FORTA_LOGIN_DOMAIN", "")
	FortaAppDomain          = getEnv("FORTA_APP_DOMAIN", "")
	FortaClientID           = getEnv("FORTA_CLIENT_ID", "")
	FortaClientSecret       = getEnv("FORTA_CLIENT_SECRET", "")
	FortaCallbackURL        = getEnv("FORTA_CALLBACK_URL", "")
	FortaJWTSigningKey      = getEnv("FORTA_JWT_SIGNING_KEY", "")
	FortaCookieDomain       = getEnv("FORTA_COOKIE_DOMAIN", "")
	FortaCookieInsecure     = getEnv("FORTA_COOKIE_INSECURE", "false") == "true"
	FortaPostLoginRedirect  = getEnv("FORTA_POST_LOGIN_REDIRECT", "/")
	FortaPostLogoutRedirect = getEnv("FORTA_POST_LOGOUT_REDIRECT", "/")
	FortaFetchUserOnProtect = getEnv("FORTA_FETCH_USER_ON_PROTECT", "true") == "true"
	FortaDisableAutoRefresh = getEnv("FORTA_DISABLE_AUTO_REFRESH", "false") == "true"
	FortaEnforceGrants      = getEnv("FORTA_ENFORCE_GRANTS", "true") == "true"

	// TLS (optional — for local HTTPS development)
	TLSCert = getEnv("TLS_CERT", "")
	TLSKey  = getEnv("TLS_KEY", "")

	// CORS
	AllowedOrigins = getEnv("ALLOWED_ORIGINS", "")
)

func getEnv(key string, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}

func getEnvOrPanic(key string) string {
	value, ok := os.LookupEnv(key)
	if !ok {
		panic(fmt.Sprintf("missing required environment variable: '%v'", key))
	}
	return value
}
