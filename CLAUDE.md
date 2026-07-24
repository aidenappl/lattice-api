# Lattice API

Central orchestrator for the Lattice container management platform. Manages workers, stacks, containers, deployments, registries, database instances, backups, and networks/volumes. Workers connect via WebSocket; the web dashboard connects via REST + a separate admin WebSocket.

> **See `AGENTS.md` for the full, authoritative reference** (complete route table, domain model, worker protocol, deploy mechanics). This file is a compact project-memory pointer.

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
  middleware.go          # RequestID, Logging, MuxHeader, SecurityHeaders, MaxBodySize middleware
  auth.go               # DualAuthMiddleware (local JWT + API token; SSO grant checkpoint), WorkerTokenAuth, RejectPending, RequireAdmin, RequireEditor
  csrf.go / ratelimit.go # CSRF double-submit; per-IP token-bucket rate limiting
sso/                     # Generic OAuth2/OIDC SSO client (sso.go) + RFC 7662 introspection (introspect.go)
crypto/crypto.go         # AES-256-GCM encrypt/decrypt for secrets at rest (passthrough if ENCRYPTION_KEY unset)
query/                   # 27 query files — squirrel-based SQL builders, all accept db.Queryable
  workers.query.go       # Worker CRUD, heartbeat, runner version updates
  stacks.query.go        # Stack CRUD, compose YAML
  containers.query.go    # Container CRUD, batch updates, lookup by name
  deployments.query.go   # Deployment CRUD, status updates
  deployment_containers.query.go
  registries.query.go    # Registry CRUD
  users.query.go         # User CRUD, lookup by email/sso_subject
  worker_tokens.query.go # Token generation/validation
  worker_metrics.query.go # Worker metrics storage/retrieval, fleet aggregation (DB-bucketed)
  container_metrics.query.go # Per-container CPU/memory metrics (batch insert, time-range queries)
  container_logs.query.go # Log persistence with dedup via unique index on recorded_at
  lifecycle_logs.query.go # Lifecycle event logging
  audit_log.query.go     # Audit trail (mutating handlers call logAudit -> CreateAuditLog)
  database_instances.query.go / database_snapshots.query.go / backup_destinations.query.go # Managed DB instances, snapshots, backup targets
  api_tokens.query.go / deploy_tokens.query.go # Long-lived API tokens; CI deploy tokens
  global_env_vars.query.go / templates.query.go / webhooks.query.go / settings.query.go / sso_sessions.query.go / search.query.go # Global env, stack templates, outbound webhooks, key-value settings, SSO sessions, global search
  networks.query.go      # Network CRUD (compose-based)
  volumes.query.go       # Volume CRUD (compose-based)
  container_events.query.go
registry/client.go       # Docker registry API client (list repos, tags, test credentials)
responder/               # Standard JSON response formatting
  responder.go           # New(), NewCreated(), NewWithCount(), SendError()
  templates.responder.go # BadBody(), MissingBodyFields(), QueryError(), NotFound()
  errors.go              # SendError()
routers/                 # ~80 handler files, named Handle<Action>.router.go (+ deployment_monitor.go watchdog, audit.go logAudit helper, backfill_networks.go startup migration)
socket/
  protocol.go            # Envelope (outgoing) and IncomingMessage (incoming) types, message constants
  hub.go                 # WorkerHub and AdminHub — manage connected WebSocket sessions
  handler.go             # WorkerHandler and AdminHandler — upgrade HTTP, manage read/write pumps
structs/                 # 20 struct files — Worker, Stack, Container, Deployment, Registry, User, DatabaseInstance, etc.
tools/                   # HashPassword, HashToken, input validators
versions/versions.go     # Background GitHub release polling (30min interval), in-memory cache
watcher/watcher.go       # Registry digest polling for mutable-tag re-pushes
retention/retention.go   # Hourly purge of old log/metric rows
webhooks/dispatcher.go   # Outbound webhooks (optional HMAC-SHA256 signing)
healthscan/scanner.go    # Worker-vs-DB reconciliation, anomaly detection
mailer/                  # SMTP alerting + notification prefs (config in settings table)
install/runner.sh        # Embedded runner install script served at GET /install/runner
```

> Schema is created/evolved **in-code** by idempotent `migrate()` calls in `db.Init()` — there is no external migration runner. (`docker-compose.yml` mounts `./migrations` into MariaDB's init hook only for a fresh volume.)

## API Routes

All `/admin/*` routes are protected by `DualAuthMiddleware`.

### Public
- `GET /healthcheck` — health check (skips logging)
- `GET /version` — `{"version":"<Version>"}`
- `GET /install/runner` — embedded install script

### Auth
- `POST /auth/login` — local email/password login, sets JWT cookies
- `POST /auth/refresh` — refresh JWT
- `GET/PUT /auth/self` — current user / update (protected); `POST /auth/logout`
- `GET /auth/sso/login`, `/auth/sso/callback`, `/auth/sso/config` — generic OAuth2/OIDC SSO (conditional)

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
- `GET /admin/containers/{id}/metrics` — container resource metrics history
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
- `GET /ws/admin` — admin live updates (requires DualAuthMiddleware authentication, origin validation via `CheckAllowedOrigin`)

## WebSocket Protocol

### Worker -> API Message Types
`heartbeat`, `registration`, `container_status`, `container_health_status`, `container_sync`, `container_logs`, `deployment_progress`, `lifecycle_log`, `worker_action_status`, `worker_shutdown`, `worker_crash`

### API -> Worker Message Types
`connected`, `deploy`, `start`, `stop`, `kill`, `restart`, `pause`, `unpause`, `remove`, `recreate`, `pull_image`, `reboot_os`, `upgrade_runner`, `stop_all`, `start_all`

## Handler Types

Most handlers are standalone functions. Six require WebSocket hub references and use struct receivers:

- `DeployHandler{WorkerHub, AdminHub}` — deploy, public/token deploy, rollback, deployment monitor
- `ContainerActionHandler{WorkerHub}` — start/stop/kill/restart/pause/unpause/remove/recreate/force-remove; delete (container + stack); stack restart/stop/start-all
- `WorkerActionHandler{WorkerHub}` — reboot, upgrade, stop-all, start-all
- `VolumeHandler{WorkerHub}` / `NetworkHandler{WorkerHub}` — worker volume/network list/create/delete
- `DatabaseHandler{WorkerHub, AdminHub}` — DB instance CRUD/actions, credentials, snapshots, backup-destination test

## Auth

`DualAuthMiddleware` accepts two credentials (Forta OAuth has been **removed**):

1. **Local JWT** — `Authorization: Bearer <token>` or `lattice-access-token` cookie. HS512, 15min access / 7d refresh. Rejected if issued before `users.tokens_revoked_at`.
2. **API token** — long-lived opaque token (`Authorization: Bearer`, SHA-256 hashed at rest) tied to a user; used by CI and lattice-mcp.

**SSO** — generic OAuth2/OIDC client (`sso/`), config in the `settings` table (`sso.*` keys) with `SSO_*` env fallback. The callback provisions the user (`SSO_AUTO_PROVISION`, default role handling) and issues a **Lattice JWT**, so SSO users authenticate like local users. `DualAuthMiddleware` re-introspects an SSO user's grant against the IDP at most every 5 min (fails open on network error). The old Forta integration and `forta_id` column were migrated to `sso_subject`.

**CSRF Protection** — Double-submit cookie pattern (`middleware/csrf.go`). State-changing requests must include a CSRF token matching the cookie; exempt for `Authorization: Bearer` requests, `/auth/login`, `/auth/refresh`, `/ws/worker`, `/api/deploy/*`, `/auth/sso/callback`.

**Role-Based Access Control** — roles `admin` > `editor` > `viewer`, plus `pending`. `RejectPending` blocks pending users from `/admin`; `RequireEditor` gates resource mutations; `RequireAdmin` gates users/config/tokens/audit. Per-IP rate limiting is applied globally.

**Security Headers** — Applied globally via middleware: `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `Referrer-Policy: strict-origin-when-cross-origin`, `Permissions-Policy` (restrictive).

**Input Validation** — `tools/validate.go` provides `ValidateName`, `ValidateEmail`, `ValidatePassword`, `ValidateYAMLSize`. Password requires minimum 8 characters on user creation.

**Audit Logging** — Mutation handlers now call `CreateAuditLog` to record state-changing operations.

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

- WebSocket callbacks live in `message_handlers.go` (`configureWorkerHandler`/`configureAdminHandler`); `OnMessage` routes by `msg.Type`, with `ws_dispatch.go` holding the persistence helpers. `safeGo` bounds handler goroutines (semaphore of 100)
- Deploy dispatch bounds concurrent monitors (`maxConcurrentDeploys=10`); the watchdog pings every 15s, retries after a 45s stall (max 3), and force-fails after 30min
- `container_cache.go` — 60s name→container cache to kill the per-message N+1 lookup
- Container log deduplication via Docker-recorded RFC3339Nano timestamps and DB unique index
- `handleContainerSync` reconciles Docker runtime state vs DB — only writes on diff
- Deployment status flow: `pending -> deploying -> deployed | failed | rolled_back` (terminal); stack status mirrors it. Deploy strategy (`rolling` default, `blue-green`, `canary`) is a string passed to the runner — the API does not implement the strategy
- Stack status mirrors deployment terminal state
- Worker registration on connect sends OS, arch, Docker version, IP, runner version
- Graceful shutdown with 10s timeout via SIGINT/SIGTERM
- YAML size limit: compose import/update rejects payloads exceeding 1MB (`ValidateYAMLSize`)
- WebSocket origin validation: `CheckAllowedOrigin` replaces permissive `CheckOrigin` to prevent cross-origin WebSocket hijacking
