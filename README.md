# lattice-api

Central orchestrator API for the Lattice platform. Manages workers, stacks, containers, deployments, and registries across a distributed set of Docker hosts. Workers connect via WebSocket for real-time command dispatch and telemetry.

---

## Tech Stack

- **Go 1.25** with gorilla/mux
- **MariaDB** — primary data store
- **gorilla/websocket** — real-time worker communication
- **Forta** — OAuth2 SSO (optional, for appleby.cloud ecosystem)
- **squirrel** — SQL query builder
- **golang-jwt/jwt/v5** — local JWT auth for standalone operation

---

## Environment Variables

| Variable | Required | Description |
|---|---|---|
| `DATABASE_DSN` | Yes | MariaDB base DSN without database name (e.g. `user:pass@tcp(host:3306)`) — schema `lattice` is appended automatically |
| `JWT_SIGNING_KEY` | Yes | HMAC-SHA512 signing key for local auth tokens |
| `PORT` | No | Server port (default `8000`) |
| `LATTICE_ADMIN_EMAIL` | No | Bootstrap admin email (creates local admin if no users exist) |
| `LATTICE_ADMIN_PASSWORD` | No | Bootstrap admin password |
| `ALLOWED_ORIGINS` | No | Comma-separated CORS origins |
| `TLS_CERT` | No | Path to TLS certificate file (enables HTTPS) |
| `TLS_KEY` | No | Path to TLS key file |
| `FORTA_API_DOMAIN` | No | Forta API base URL (enables OAuth) |
| `FORTA_CLIENT_ID` | No | OAuth2 client ID |
| `FORTA_CLIENT_SECRET` | No | OAuth2 client secret |
| `FORTA_CALLBACK_URL` | No | OAuth2 callback URL |
| `FORTA_LOGIN_DOMAIN` | No | Forta login UI URL |
| `FORTA_APP_DOMAIN` | No | Forta app domain |
| `FORTA_JWT_SIGNING_KEY` | No | Forta JWT key for local token validation |
| `FORTA_COOKIE_DOMAIN` | No | Cookie domain (e.g. `.appleby.cloud`) |
| `FORTA_COOKIE_INSECURE` | No | Allow insecure cookies (development) |
| `FORTA_POST_LOGIN_REDIRECT` | No | Redirect URL after Forta login |
| `FORTA_POST_LOGOUT_REDIRECT` | No | Redirect URL after Forta logout |
| `FORTA_FETCH_USER_ON_PROTECT` | No | Fetch full user profile on each protected request |
| `FORTA_DISABLE_AUTO_REFRESH` | No | Disable automatic Forta token refresh |
| `FORTA_ENFORCE_GRANTS` | No | Enforce Forta grant-based access control |

---

## Authentication

Lattice supports dual authentication:

1. **Local auth** — Email/password login issues a Lattice JWT stored as an HttpOnly cookie. Used for bootstrap and when Forta is unavailable.
2. **Forta OAuth** — Standard OAuth2 flow via `go-forta`. OAuth users are auto-created on first login.

Since Forta itself runs on Lattice, local auth provides a fallback when the auth service is down. The `DualAuthMiddleware` checks both auth methods on every protected route so either mechanism can be used transparently.

---

## Quick Start

```bash
# Copy env and configure
cp .env.example .env

# Start MariaDB + API
docker compose up -d

# Or run locally
go run .
```

---

## Install Script

Workers can be provisioned from any machine with a one-liner:

```bash
curl -fsSL https://lattice-api.appleby.cloud/install/runner | \
  REGISTRY_USERNAME=x REGISTRY_PASSWORD=x WORKER_TOKEN=<token> WORKER_NAME=<name> bash
```

The script is served from `GET /install/runner` and handles cloning, building, configuring, and installing the runner as a systemd service.

---

## Update Script

To update an existing runner to the latest version:

```bash
curl -fsSL https://lattice-api.appleby.cloud/install/update.sh | bash
```

---

## API Routes

### Public
- `GET /` — Service identifier
- `GET /healthcheck` — Health check
- `GET /version` — Returns `{"version":"v0.0.1"}`
- `GET /install/runner` — Runner install script

### Auth
- `POST /auth/login` — Local email/password login
- `POST /auth/refresh` — Refresh local JWT
- `GET /auth/self` — Get current user (protected)
- `GET /forta/login` — Forta OAuth redirect (if enabled)
- `GET /forta/callback` — Forta OAuth callback
- `GET /forta/logout` — Clear Forta cookies

### Admin (protected)
- `GET|POST /admin/workers` — List/create workers
- `GET|PUT|DELETE /admin/workers/{id}` — Worker CRUD
- `GET /admin/workers/{id}/tokens` — List worker tokens
- `POST /admin/workers/{id}/tokens` — Generate worker API token
- `DELETE /admin/worker-tokens/{id}` — Revoke a worker token
- `GET /admin/workers/{id}/metrics` — Worker metrics history
- `GET|POST /admin/stacks` — List/create stacks
- `GET|PUT|DELETE /admin/stacks/{id}` — Stack CRUD
- `POST /admin/stacks/{id}/deploy` — Trigger deployment
- `GET|POST /admin/stacks/{id}/containers` — Container management
- `PUT|DELETE /admin/containers/{id}` — Update/delete container
- `GET /admin/deployments` — List deployments
- `GET /admin/deployments/{id}` — Deployment detail
- `POST /admin/deployments/{id}/approve` — Approve pending deployment
- `POST /admin/deployments/{id}/rollback` — Rollback deployment
- `GET|POST /admin/registries` — Registry management
- `PUT|DELETE /admin/registries/{id}` — Update/delete registry
- `POST /admin/registries/{id}/test` — Test registry connectivity
- `POST /admin/registries/test` — Test registry inline (no save)
- `GET /admin/registries/{id}/repositories` — List registry repos
- `GET /admin/registries/{id}/tags` — List image tags
- `GET|POST /admin/users` — User management
- `PUT /admin/users/{id}` — Update user
- `GET /admin/overview` — Dashboard statistics
- `GET /admin/audit-log` — Audit log

### WebSocket
- `GET /ws/worker?token=<token>` — Worker connection endpoint
- `GET /ws/admin` — Admin live updates (protected)

---

## Versioning

The API version is hardcoded in the binary and can be overridden at build time via ldflags:

```bash
go build -ldflags "-X main.Version=v1.2.3" -o lattice-api .
```

The version endpoint returns the current version:

```
GET /version
{"version":"v0.0.1"}
```
