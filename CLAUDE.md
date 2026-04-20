# Lattice API

Central orchestrator for the Lattice container management platform. Manages workers, stacks, containers, deployments, and registries. Workers connect via WebSocket; the web dashboard connects via REST + a separate admin WebSocket.

## Commands

```bash
dev          # go run . (sources .env)
dev build    # go build -o bin/app .
dev test     # go test ./...
dev fmt      # gofmt -w -s .
dev vet      # go vet ./...
dev check    # fmt + vet + test
dev tidy     # go mod tidy
dev up       # docker compose up -d (MariaDB + API + Web)
dev down     # docker compose down
```

## Project Structure

```
main.go                  # Entry point, router setup, WebSocket message handlers
bootstrap/admin.go       # First-run admin user creation
db/db.go                 # MariaDB connection pool, Queryable interface, pagination constants
env/env.go               # Environment variable loading (getEnv/getEnvOrPanic)
jwt/jwt.go               # Local auth JWT generation/validation (HS512, 15min access / 7d refresh)
middleware/
  middleware.go          # RequestID, Logging, MuxHeader middleware
  auth.go               # DualAuthMiddleware (local JWT + Forta OAuth), WorkerTokenAuth, RequireAdmin
query/                   # 16 query files — squirrel-based SQL builders, all accept db.Queryable
  workers.query.go       # Worker CRUD, heartbeat, runner version updates
  stacks.query.go        # Stack CRUD, compose YAML
  containers.query.go    # Container CRUD, batch updates, lookup by name
  deployments.query.go   # Deployment CRUD, status updates
  deployment_containers.query.go
  registries.query.go    # Registry CRUD
  users.query.go         # User CRUD, lookup by email/forta_id
  worker_tokens.query.go # Token generation/validation
  worker_metrics.query.go # Metrics storage/retrieval
  container_logs.query.go # Log persistence with dedup via unique index on recorded_at
  lifecycle_logs.query.go # Lifecycle event logging
  audit_log.query.go     # Audit trail (CreateAuditLog exists but is NOT called anywhere yet)
  networks.query.go      # Network CRUD (compose-based)
  volumes.query.go       # Volume CRUD (compose-based)
  container_events.query.go
registry/client.go       # Docker registry API client (list repos, tags, test credentials)
responder/               # Standard JSON response formatting
  responder.go           # New(), NewCreated(), NewWithCount(), SendError()
  templates.responder.go # BadBody(), MissingBodyFields(), QueryError(), NotFound()
  errors.go              # SendError()
routers/                 # 49 handler files, named Handle<Action>.router.go
socket/
  protocol.go            # Envelope (outgoing) and IncomingMessage (incoming) types, message constants
  hub.go                 # WorkerHub and AdminHub — manage connected WebSocket sessions
  handler.go             # WorkerHandler and AdminHandler — upgrade HTTP, manage read/write pumps
structs/                 # 11 struct files — Worker, Stack, Container, Deployment, Registry, User, etc.
tools/                   # HashPassword, HashToken utilities
versions/versions.go     # Background GitHub release polling (30min interval), in-memory cache
install/runner.sh        # Embedded runner install script served at GET /install/runner
migrations/              # SQL migration scripts (auto-run by MariaDB on first boot)
```

## API Routes

All `/admin/*` routes are protected by `DualAuthMiddleware`.

### Public
- `GET /healthcheck` — health check (skips logging)
- `GET /version` — `{"version":"<Version>"}`
- `GET /install/runner` — embedded install script

### Auth
- `POST /auth/login` — local email/password login, sets JWT cookies
- `POST /auth/refresh` — refresh JWT
- `GET /auth/self` — current user (protected)
- `GET /forta/login`, `/forta/callback`, `/forta/logout` — Forta OAuth (conditional)

### Workers
- `GET/POST /admin/workers` — list/create
- `GET/PUT/DELETE /admin/workers/{id}` — get/update/delete
- `GET/POST /admin/workers/{id}/tokens` — list/create tokens
- `DELETE /admin/worker-tokens/{id}` — revoke token
- `GET /admin/workers/{id}/metrics` — metrics history
- `POST /admin/workers/{id}/reboot` — reboot OS
- `POST /admin/workers/{id}/upgrade` — upgrade runner
- `POST /admin/workers/{id}/stop-all` — stop all containers
- `POST /admin/workers/{id}/start-all` — start all containers

### Stacks
- `GET/POST /admin/stacks` — list/create
- `POST /admin/stacks/import` — import from docker-compose.yml
- `GET/PUT/DELETE /admin/stacks/{id}` — get/update/delete
- `PUT /admin/stacks/{id}/compose` — update compose YAML
- `POST /admin/stacks/{id}/sync-compose` — sync compose with DB containers
- `POST /admin/stacks/{id}/deploy` — trigger deployment

### Containers
- `GET /admin/containers` — list all (filterable by stack_id, worker_id)
- `GET/POST /admin/stacks/{id}/containers` — list/create per stack
- `GET/PUT/DELETE /admin/containers/{id}` — get/update/delete
- `GET /admin/containers/{id}/logs` — container logs
- `GET /admin/containers/{id}/lifecycle` — lifecycle logs
- `POST /admin/containers/{id}/{action}` — start, stop, kill, restart, pause, unpause, remove, recreate

### Deployments
- `GET /admin/deployments` — list
- `GET /admin/deployments/{id}` — get
- `GET /admin/deployments/{id}/logs` — deployment logs
- `POST /admin/deployments/{id}/approve` — approve pending deployment
- `POST /admin/deployments/{id}/rollback` — rollback

### Registries
- `GET/POST /admin/registries` — list/create
- `PUT/DELETE /admin/registries/{id}` — update/delete
- `POST /admin/registries/test` — test inline credentials
- `POST /admin/registries/{id}/test` — test saved registry
- `GET /admin/registries/{id}/repositories` — list repos
- `GET /admin/registries/{id}/tags` — list tags

### Users & Admin
- `GET/POST /admin/users` — list/create
- `PUT /admin/users/{id}` — update
- `GET /admin/audit-log` — audit log
- `GET /admin/overview` — dashboard stats

### Versions & Updates
- `GET /admin/versions` — version info (API, web, runner + worker versions)
- `POST /admin/versions/refresh` — refresh version cache from GitHub
- `POST /admin/update/api` — self-update API container
- `POST /admin/update/web` — update web container

### WebSocket
- `GET /ws/worker?token=<token>` — worker connection (token auth)
- `GET /ws/admin` — admin live updates (DualAuthMiddleware via cookie)

## WebSocket Protocol

### Worker -> API Message Types
`heartbeat`, `registration`, `container_status`, `container_health_status`, `container_sync`, `container_logs`, `deployment_progress`, `lifecycle_log`, `worker_action_status`, `worker_shutdown`, `worker_crash`

### API -> Worker Message Types
`connected`, `deploy`, `start`, `stop`, `kill`, `restart`, `pause`, `unpause`, `remove`, `recreate`, `pull_image`, `reboot_os`, `upgrade_runner`, `stop_all`, `start_all`

## Handler Types

Most handlers are standalone functions. Three require WebSocket hub references and use struct receivers:

- `DeployHandler{WorkerHub, AdminHub}` — deploy, rollback
- `ContainerActionHandler{WorkerHub}` — start/stop/kill/restart/pause/unpause/remove/recreate/delete (container + stack)
- `WorkerActionHandler{WorkerHub}` — reboot, upgrade, stop-all, start-all

## Auth

Dual auth system — both can coexist:

1. **Local JWT** — `Authorization: Bearer <token>` or `lattice-access-token` cookie. HS512, 15min access / 7d refresh.
2. **Forta OAuth** — `forta-access-token` cookie. First OAuth login auto-creates user with role `viewer`.

`RequireAdmin` middleware exists in `middleware/auth.go` but is not applied to any routes yet.

## Version Management

`versions/versions.go` polls GitHub releases every 30 minutes for `lattice-api`, `lattice-web`, `lattice-runner`. The API's own version is set via ldflags: `-X main.Version=<tag>`.

## Build

```dockerfile
# Multi-stage: golang:1.25-alpine -> alpine:3.19
# Includes: ca-certificates, curl, docker-cli, docker-compose
# Runs as root (needs Docker socket access)
ARG VERSION=dev
RUN go build -ldflags="-w -s -X main.Version=${VERSION}" -o /lattice-api .
```

## Key Patterns

- All message handlers in `main.go` (lines 117-221) route by `msg.Type` and dispatch to handler functions
- Container status updates write lifecycle logs synchronously BEFORE broadcasting to admin hub
- Container log deduplication via Docker-recorded RFC3339Nano timestamps and DB unique index
- `handleContainerSync` reconciles Docker runtime state vs DB — only writes on diff
- Deployment status flow: `pending -> deploying -> deployed|failed -> rolled_back`
- Stack status mirrors deployment terminal state
- Worker registration on connect sends OS, arch, Docker version, IP, runner version
- Graceful shutdown with 10s timeout via SIGINT/SIGTERM
