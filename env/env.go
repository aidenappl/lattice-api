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
	SSOPostLoginURL   = getEnv("SSO_POST_LOGIN_URL", "/")             // URL to redirect to after SSO login (frontend URL)

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

// weakJWTKeys is a blocklist of known-insecure default signing keys that must
// not be used in production.
var weakJWTKeys = map[string]bool{
	"change-me-to-a-random-secret":               true,
	"dev-jwt-signing-key-change-in-production":    true,
	"changeme":                                    true,
	"secret":                                      true,
}

// ValidateSecurityDefaults checks that critical security configuration is not
// left at insecure defaults. Panics in production, warns in development.
func ValidateSecurityDefaults() {
	isProd := Environment == "production"

	// JWT signing key strength
	if len(JWTSigningKey) < 32 {
		msg := fmt.Sprintf("JWT_SIGNING_KEY is too short (%d chars, minimum 32). Generate one with: openssl rand -hex 32", len(JWTSigningKey))
		if isProd {
			panic(msg)
		}
		fmt.Printf("⚠️  WARNING: %s\n", msg)
	}
	if weakJWTKeys[JWTSigningKey] {
		msg := "JWT_SIGNING_KEY is a known default value. Generate a secure key with: openssl rand -hex 32"
		if isProd {
			panic(msg)
		}
		fmt.Printf("⚠️  WARNING: %s\n", msg)
	}

	// Bootstrap admin password
	if LatticeAdminPassword != "" && len(LatticeAdminPassword) < 8 {
		msg := "LATTICE_ADMIN_PASSWORD is too short (minimum 8 characters)"
		if isProd {
			panic(msg)
		}
		fmt.Printf("⚠️  WARNING: %s\n", msg)
	}
	if LatticeAdminPassword == "changeme" || LatticeAdminPassword == "password" {
		msg := "LATTICE_ADMIN_PASSWORD is a known weak default. Set a strong password."
		if isProd {
			panic(msg)
		}
		fmt.Printf("⚠️  WARNING: %s\n", msg)
	}

	// Encryption key format (if provided)
	if EncryptionKey != "" && len(EncryptionKey) != 64 {
		fmt.Printf("⚠️  WARNING: ENCRYPTION_KEY should be exactly 64 hex characters (32 bytes). Current length: %d\n", len(EncryptionKey))
	}
}

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
