# lattice-api

Central orchestrator API for the Lattice platform. Manages workers, stacks, containers, deployments, and registries across a distributed set of Docker hosts. Workers connect via WebSocket for real-time command dispatch and telemetry.

---

## Tech Stack

- **Go 1.25** with gorilla/mux
- **MariaDB** — primary data store (schema `lattice`, migrated in-code via `db.Init()`)
- **gorilla/websocket** — real-time worker communication
- **Generic OAuth2 / OIDC SSO** — optional, self-contained client under `sso/` (no `go-forta` dependency)
- **squirrel** — SQL query builder (no ORM)
- **golang-jwt/jwt/v5** — local JWT auth (HS512) for standalone operation

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
| `COOKIE_DOMAIN` | No | Cookie domain (e.g. `.appleby.cloud`) for cross-subdomain auth cookies |
| `ENCRYPTION_KEY` | No | 64 hex chars (32 bytes) for AES-256-GCM encryption of secrets at rest; passthrough if unset |
| `SSO_CLIENT_ID` | No | OAuth2/OIDC client ID (enables "Sign in with SSO"); env fallback for the DB-backed `sso.*` settings |
| `SSO_CLIENT_SECRET` | No | OAuth2 client secret |
| `SSO_AUTHORIZE_URL` / `SSO_TOKEN_URL` / `SSO_USERINFO_URL` / `SSO_INTROSPECT_URL` | No | IDP endpoints |
| `SSO_REDIRECT_URL` / `SSO_LOGOUT_URL` / `SSO_POST_LOGIN_URL` | No | Redirect URLs |
| `SSO_SCOPES` | No | OAuth scopes (default `openid email profile`) |
| `SSO_USER_IDENTIFIER` | No | userinfo field to match users on (default `email`) |
| `SSO_AUTO_PROVISION` | No | Auto-create users on first SSO login (default `true`) |
| `DOCKER_COMPOSE_DIR` | No | Compose dir for the self-update path (`/admin/update/api`,`/web`) |

> **Note:** SSO configuration is primarily stored in the `settings` table (`sso.*` keys, editable via
> `PUT /admin/sso-config`); the `SSO_*` env vars above act as a fallback. Forta-specific `FORTA_*`
> variables are **no longer used** — Forta OAuth was replaced by the generic SSO client.

---

## Authentication

Lattice authenticates every protected route via `DualAuthMiddleware`, which accepts, in order:

1. **Local JWT** (HS512) — email/password login issues an access token (15 min) + refresh token
   (7 days), sent as `Authorization: Bearer` or the `lattice-access-token` HttpOnly cookie.
2. **API token** — a long-lived opaque token (`Authorization: Bearer`, SHA-256 hashed at rest)
   tied to a user, for CI and the MCP server.

SSO users are not a separate request-time path: the SSO callback provisions the user and issues a
Lattice JWT, so they authenticate like local users, with a 5-minute IDP grant re-check. Local auth
provides a fallback when the SSO IDP (which itself runs on Lattice) is down.

RBAC roles: `admin` > `editor` > `viewer`, plus `pending` (blocked from `/admin` until approved).
Mutations require `editor`; user/config/token administration requires `admin`. CSRF (double-submit
cookie) and per-IP rate limiting are applied globally.

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
- `GET /auth/self` — Get current user (protected); `PUT /auth/self` to update
- `POST /auth/logout` — Log out (protected)
- `GET /auth/sso/login` — SSO OAuth2 redirect (if enabled)
- `GET /auth/sso/callback` — SSO OAuth2 callback
- `GET /auth/sso/config` — Public SSO config for the login page

> For the complete, authoritative route surface (workers, stacks, containers, deployments,
> registries, database instances, backups, admin config, WebSocket), see `AGENTS.md`.

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
