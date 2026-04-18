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
| `DATABASE_DSN` | Yes | MariaDB DSN (e.g. `user:pass@tcp(host:3306)/lattice?parseTime=true`) |
| `JWT_SIGNING_KEY` | Yes | HMAC-SHA512 signing key for local auth tokens |
| `PORT` | No | Server port (default `8000`) |
| `LATTICE_ADMIN_EMAIL` | No | Bootstrap admin email (creates local admin if no users exist) |
| `LATTICE_ADMIN_PASSWORD` | No | Bootstrap admin password |
| `ALLOWED_ORIGINS` | No | Comma-separated CORS origins |
| `FORTA_API_DOMAIN` | No | Forta API base URL (enables OAuth) |
| `FORTA_CLIENT_ID` | No | OAuth2 client ID |
| `FORTA_CLIENT_SECRET` | No | OAuth2 client secret |
| `FORTA_CALLBACK_URL` | No | OAuth2 callback URL |
| `FORTA_LOGIN_DOMAIN` | No | Forta login UI URL |
| `FORTA_JWT_SIGNING_KEY` | No | Forta JWT key for local token validation |
| `FORTA_COOKIE_DOMAIN` | No | Cookie domain (e.g. `.appleby.cloud`) |

---

## Authentication

Lattice supports dual authentication:

1. **Local auth** — Email/password login issues a Lattice JWT stored as an HttpOnly cookie. Used for bootstrap and when Forta is unavailable.
2. **Forta OAuth** — Standard OAuth2 flow via `go-forta`. OAuth users are auto-created on first login.

Since Forta itself runs on Lattice, local auth provides a fallback when the auth service is down.

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

## API Routes

### Auth
- `POST /auth/login` — Local email/password login
- `POST /auth/refresh` — Refresh local JWT
- `GET /auth/self` — Get current user (protected)
- `GET /forta/login` — Forta OAuth redirect
- `GET /forta/callback` — Forta OAuth callback
- `GET /forta/logout` — Clear Forta cookies

### Admin (protected)
- `GET|POST /admin/workers` — List/create workers
- `GET|PUT|DELETE /admin/workers/{id}` — Worker CRUD
- `POST /admin/workers/{id}/tokens` — Generate worker API token
- `GET|POST /admin/stacks` — List/create stacks
- `GET|PUT|DELETE /admin/stacks/{id}` — Stack CRUD
- `POST /admin/stacks/{id}/deploy` — Trigger deployment
- `GET|POST /admin/stacks/{id}/containers` — Container management
- `GET /admin/deployments` — List deployments
- `POST /admin/deployments/{id}/approve` — Approve pending deployment
- `POST /admin/deployments/{id}/rollback` — Rollback deployment
- `GET|POST /admin/registries` — Registry management
- `GET /admin/users` — User management
- `GET /admin/overview` — Dashboard statistics
- `GET /admin/audit-log` — Audit log

### WebSocket
- `GET /ws/worker?token=<token>` — Worker connection endpoint
- `GET /ws/admin` — Admin live updates (protected)
