package env

import (
	"fmt"
	"os"
)

var (
	Port        = getEnv("PORT", "8000")
	Environment = getEnv("ENVIRONMENT", "production")
	LogLevel    = getEnv("LOG_LEVEL", "info")
	LogFormat   = getEnv("LOG_FORMAT", "text") // "text" or "json"
	CoreDBDSN   = getEnvOrPanic("DATABASE_DSN")

	// JWT signing key for local auth tokens
	JWTSigningKey = getEnvOrPanic("JWT_SIGNING_KEY")

	// Bootstrap admin credentials (creates initial local admin if no users exist)
	LatticeAdminEmail    = getEnv("LATTICE_ADMIN_EMAIL", "")
	LatticeAdminPassword = getEnv("LATTICE_ADMIN_PASSWORD", "")

	// SSO OAuth2 configuration (optional — enables "Sign in with SSO" on the login page)
	SSOClientID       = getEnv("SSO_CLIENT_ID", "")
	SSOClientSecret   = getEnv("SSO_CLIENT_SECRET", "")
	SSOAuthorizeURL   = getEnv("SSO_AUTHORIZE_URL", "")
	SSOTokenURL       = getEnv("SSO_TOKEN_URL", "")
	SSOUserInfoURL    = getEnv("SSO_USERINFO_URL", "")
	SSORedirectURL    = getEnv("SSO_REDIRECT_URL", "")
	SSOLogoutURL      = getEnv("SSO_LOGOUT_URL", "")
	SSOScopes         = getEnv("SSO_SCOPES", "openid email profile")
	SSOUserIdentifier = getEnv("SSO_USER_IDENTIFIER", "email") // field in userinfo to match user
	SSOButtonLabel    = getEnv("SSO_BUTTON_LABEL", "Sign in with SSO")
	SSOAutoProvision  = getEnv("SSO_AUTO_PROVISION", "true") == "true" // auto-create users on first SSO login

	// TLS (optional — for local HTTPS development)
	TLSCert = getEnv("TLS_CERT", "")
	TLSKey  = getEnv("TLS_KEY", "")

	// CORS
	AllowedOrigins = getEnv("ALLOWED_ORIGINS", "")

	// Cookie domain (e.g. ".appleby.cloud") — required when frontend and API
	// are on different subdomains so that cookies are readable cross-subdomain.
	CookieDomain = getEnv("COOKIE_DOMAIN", "")

	// Encryption key for secrets (optional — 64 hex chars / 32 bytes AES-256-GCM)
	EncryptionKey = getEnv("ENCRYPTION_KEY", "")

	// Docker update configuration (for self-update capability).
	DockerComposeDir      = getEnv("DOCKER_COMPOSE_DIR", "")
	APIServiceName        = getEnv("API_SERVICE_NAME", "lattice-api")
	WebServiceName        = getEnv("WEB_SERVICE_NAME", "lattice-web")
	DockerHelperContainer = getEnv("DOCKER_HELPER_CONTAINER", "lattice-docker-helper")
	RegistryURL           = getEnv("REGISTRY_URL", "")
	RegistryUsername      = getEnv("REGISTRY_USERNAME", "")
	RegistryPassword      = getEnv("REGISTRY_PASSWORD", "")
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
