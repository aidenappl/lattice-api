# AGENTS.md — lattice-api

> `lattice-api` is the **Go control-plane for Lattice**, the container-orchestration platform
> that runs every `appleby.cloud` service (it replaces Portainer). It owns the data model —
> workers, stacks, containers, deployments, registries, database instances, networks, volumes,
> backups — and it is the single point every worker VM dials home to over a persistent
> WebSocket. The Next.js dashboard ([`lattice-web`](https://github.com/aidenappl/lattice-web))
> talks to it over REST + a second admin WebSocket; the MCP server
> ([`lattice-mcp`](https://github.com/aidenappl/lattice-mcp)) wraps its `/admin` surface as
> tools; the agent on each host ([`lattice-runner`](https://github.com/aidenappl/lattice-runner))
> connects **outbound** to `/ws/worker`. It runs at `lattice.appleby.cloud`.
>
> **⚠️ Golden rule — keep this file current:** any change that adds/removes a route, changes the
> auth model, changes the worker WebSocket protocol, alters the DB schema (the in-code
> migrations in `db/db.go`), or touches deploy mechanics MUST update this AGENTS.md in the SAME
> change — and `README.md` too if setup, commands, env vars, or the repo's role moved. Stale
> context here misleads every future agent and every sibling repo that mirrors this surface. If
> you finish work and haven't touched AGENTS.md, confirm that's actually correct.

---

## What this repo is

`lattice-api` is a monolithic Go HTTP + WebSocket server. It is the **brain** of Lattice: it
holds all persistent state in a MariaDB `lattice` database and it is the only component that can
issue commands to workers. Workers hold no authoritative state of their own — they execute
Docker operations and stream telemetry back; everything they should be running is derived from
rows in this database.

**What it owns:**

- The **data model** — `workers`, `stacks`, `containers`, `deployments`, `deployment_containers`,
  `registries`, `database_instances`, `database_snapshots`, `backup_destinations`, `networks`,
  `volumes`, `global_env_vars`, `templates`, `webhook_configs`, `deploy_tokens`, `api_tokens`,
  `worker_tokens`, `users`, `sso_sessions`, `audit_log`, `settings`, and the log/metric tables.
- **Command dispatch** — translating REST calls (deploy, restart, rollback, exec, DB actions)
  into `Envelope` messages pushed to the right worker over its WebSocket.
- **Deployment orchestration bookkeeping** — creating deployment records, resolving env
  vars/registry auth/networks into container specs, and running a per-deployment watchdog that
  pings, retries, and force-fails stalled deploys.
- **Auth** — local email/password JWTs, long-lived API tokens, and a generic OAuth2/OIDC SSO
  client; RBAC (`admin` / `editor` / `viewer` / `pending`); CSRF; per-IP rate limiting.
- **Fleet observability plumbing** — health scanning, worker/container metrics ingestion,
  anomaly detection, retention purging, outbound webhooks, and SMTP alerting.

**What it does NOT own:**

- **The actual Docker work.** Pulling images, creating/stopping containers, running the
  blue-green/canary/rolling strategy step-by-step, taking DB snapshots, exec PTY — all of that
  lives in [`lattice-runner`](https://github.com/aidenappl/lattice-runner). This API sends a
  `strategy` string and a container spec; the runner interprets it. Do not add
  Docker-strategy logic here.
- **The dashboard UI.** That is [`lattice-web`](https://github.com/aidenappl/lattice-web).
- **The MCP tool surface.** That is [`lattice-mcp`](https://github.com/aidenappl/lattice-mcp),
  which mirrors the routes in `main.go`.

---

## Stack & dependencies

- **Go 1.25** (`go.mod` declares `go 1.25.0`; Docker builds on `golang:1.25-alpine`).
- **Router:** `github.com/gorilla/mux` v1.8.1 — subrouters for `/auth` and `/admin`.
- **WebSocket:** `github.com/gorilla/websocket` v1.5.3 — both the worker hub and the admin hub.
- **SQL builder:** `github.com/Masterminds/squirrel` v1.5.4, imported as `sq`. **No ORM.**
- **DB driver:** `github.com/go-sql-driver/mysql` v1.9.3 (MariaDB/MySQL). `database/sql` only.
- **JWT:** `github.com/golang-jwt/jwt/v5` v5.3.1 — local auth tokens, HS512.
- **CORS:** `github.com/rs/cors` v1.11.1 (never `gorilla/handlers`).
- **IDs:** `github.com/google/uuid` v1.6.0 — request IDs.
- **Crypto:** `golang.org/x/crypto` v0.50.0 — bcrypt for passwords.
- **YAML:** `gopkg.in/yaml.v3` v3.0.1 — compose import/parsing.

**Internal SDKs:** none imported as Go modules. Notably this repo does **not** import
`go-forta` or `go-keyring` — Forta OAuth was removed and replaced with a self-contained,
provider-agnostic OAuth2/OIDC client under `sso/` (see *Domain & architecture*). Keyring is used
only at **CI** time via the `aidenappl/keyring-actions` GitHub Action to inject registry
credentials for the image build; the running binary reads plain env vars.

There is **no third-party logging, metrics, or DI framework** — `logger/` is hand-rolled
structured logging, and dependencies are wired manually in `init.go`.

---

## Project structure

Flat, package-per-concern layout at the repo root (no `cmd/`, `internal/`, or `pkg/`). Files
that carry runtime state or wire the hubs live in **package `main`** at the root alongside
`main.go`.

| Path | Role |
|------|------|
| `main.go` | Entry point. Builds the `mux.Router`, registers **every** route (this is the authoritative route table), mounts middleware, wires WebSocket endpoints, calls `startServer`. |
| `init.go` | `initApp()` — bootstraps logger, env validation, DB, crypto, retention, watcher, versions, admin bootstrap, network backfill, SSO check, and constructs the two hubs + all struct-receiver handlers into an `appContext`. |
| `server.go` | `startServer` — CORS config, `http.Server` with timeouts, TLS toggle, graceful shutdown on SIGINT/SIGTERM (10s drain). |
| `message_handlers.go` | `configureWorkerHandler` / `configureAdminHandler` — the OnConnect/OnDisconnect/OnMessage callbacks. This is where inbound worker messages are routed by `msg.Type` and fanned out to the admin hub, DB, webhooks, and mailer. `safeGo` bounds handler goroutines (semaphore of 100). |
| `ws_dispatch.go` | Helper dispatch logic for worker→API messages (heartbeat metrics, container status/sync/logs, lifecycle logs, deployment progress). Referenced by `message_handlers.go`. |
| `container_cache.go` | 60s in-memory `name → *structs.Container` cache to kill the N+1 lookup on every heartbeat/log/status message. Status/health writes call `Invalidate(name)` so the next read is fresh, and a background `StartEviction` goroutine prunes expired entries to bound memory. |
| `env/env.go` | All env vars via `getEnv`/`getEnvOrPanic`. `ValidateSecurityDefaults()` panics in production on weak `JWT_SIGNING_KEY` / admin password. |
| `db/db.go` | MariaDB pool (IIFE-free lazy `Init()`), `Queryable` interface, `DEFAULT_LIMIT`/`MAX_LIMIT`, `BeginTx`, and **all schema migrations run in-code** via the idempotent `migrate()` helper. |
| `logger/logger.go` | Structured leveled logger (text/ANSI or JSON). `logger.F` = `map[string]any`. `Request()` picks level by HTTP status. |
| `middleware/` | `middleware.go` (RequestID, Logging, MuxHeader, SecurityHeaders, MaxBodySize, `statusResponseWriter` with Hijack), `auth.go` (DualAuth, RejectPending, RequireAdmin, RequireEditor, WorkerTokenAuth, SSO checkpoint), `csrf.go` (double-submit), `ratelimit.go` (per-IP token bucket). |
| `jwt/jwt.go` | HS512 local tokens. 15-min access / 7-day refresh. `Claims{UserID, Type}`. |
| `crypto/crypto.go` | AES-256-GCM encrypt/decrypt for secrets at rest. Passthrough (no-op) when `ENCRYPTION_KEY` unset **only in non-production** — `Init()` **panics at boot** if the key is empty and `ENVIRONMENT=production`. `Decrypt` returns a real error on bad base64 / short input / auth failure (no silent plaintext fallthrough); callers propagate it. |
| `sso/` | `sso.go` (generic OAuth2/OIDC client, DB-backed config, state handling), `introspect.go` (RFC 7662 introspection used by the auth checkpoint). |
| `responder/` | `responder.go` (success envelope: `New`/`NewCreated`/`NewWithCount`), `errors.go` (`SendError`/`SendErrorWithCode`), `templates.responder.go` (`BadBody`/`MissingBodyFields`/`QueryError`/`NotFound`). |
| `routers/` | ~80 handler files, `Handle<Verb><Entity>.router.go`. Most are plain funcs; six use struct receivers that hold hub refs (see *Handler types*). Also `deployment_monitor.go` (watchdog), `backfill_networks.go` (startup migration), `audit.go` (`logAudit` helper). |
| `query/` | 27 query files, `{entity}.query.go`. squirrel builders; every function takes `db.Queryable` first. `errors.go` holds shared query errors. |
| `structs/` | 20 `{Entity}.struct.go` domain types with `json` tags and pointer nullables. |
| `socket/` | `hub.go` (WorkerHub + WorkerSession), `admin.go` (AdminHub + AdminSession with topic subscriptions), `handler.go` (WorkerHandler — upgrade, read/write pumps, ping/pong), `protocol.go` (all message-type constants + `Envelope`/`IncomingMessage`). |
| `registry/client.go` | Docker Registry v2 HTTP client — `Ping`, `ListRepositories`, `ListTags`, `GetManifestDigest`. |
| `healthscan/scanner.go` | Periodic worker-vs-DB reconciliation; emits `health_anomalies` to the admin hub. |
| `watcher/watcher.go` | Polls registries for mutable-tag re-pushes (digest change) → fires `image.updated` / auto-deploy webhooks. |
| `retention/retention.go` | Hourly batch purge of old log/metric rows per retention window. |
| `webhooks/dispatcher.go` | `Fire(event, data)` — async outbound webhooks, optional HMAC-SHA256 signing. |
| `versions/versions.go` | Polls GitHub `releases/latest` for api/web/runner every 30 min; in-memory cache. |
| `mailer/` | `mailer.go` (SMTP send + HTML template, config from `settings`), `prefs.go` (notification prefs, cooldowns, grace timers, unhealthy thresholds). |
| `bootstrap/admin.go` | First-run: creates the local admin user from `LATTICE_ADMIN_EMAIL`/`_PASSWORD` if no users exist. |
| `tools/` | `password.tool.go` (bcrypt cost 12), `token.tool.go` (opaque token gen + SHA-256 hash-at-rest), `validate.go` (name/email/password/YAML-size/SSRF-guard validators). |
| `install/runner.sh` | Embedded (`//go:embed`) worker install script served at `GET /install/runner`. |
| `deploy/` | Ops shell scripts (`setup.sh`, `update.sh`, `backup.sh`, `docker-compose.prod.yml`) — production host bootstrap, not part of the Go build. |
| `Dockerfile` / `docker-compose.yml` / `Devfile.yaml` | Build + local orchestration + `dev` CLI wiring. |
| `.github/workflows/build-and-deploy.yml` | CI: version resolve → buildx → push to `registry.appleby.cloud/lattice-api`. |

---

## Running, building & testing

All commands go through the `dev` CLI (`Devfile.yaml`).

| Command | What it runs |
|---------|--------------|
| `dev` | `source .env && go run .` — start the HTTP dev server |
| `dev dev-https` | Same, with `TLS_CERT`/`TLS_KEY` pointed at local mkcert certs |
| `dev build` | `go build -o bin/app .` |
| `dev test` | `go test ./...` |
| `dev fmt` | `gofmt -w -s .` |
| `dev vet` | `go vet ./...` |
| `dev check` | `gofmt -w -s . && go vet ./... && go test ./...` |
| `dev tidy` | `go mod tidy` |
| `dev up` / `dev down` | `docker compose up -d` / `down` (MariaDB + API + web) |
| `dev setup-local` | One-time mkcert + `/etc/hosts` entry for `lattice-api.local.appleby.cloud` |

**Prerequisites:**

- **A MariaDB reachable via `DATABASE_DSN`.** The DSN is the base only — `user:pass@tcp(host:3306)`
  with **no** database name and **no** query params; `db.Init()` strips anything after the first
  `/` or `?` and appends `/lattice?charset=utf8mb4&parseTime=True`. `dev up` provides a
  `mariadb:11` container for this.
- **A `.env` file** (there is `.env.example`). Do not commit it. The server sources it.
- **Docker** — only needed for `dev up`, and in production for the Docker-socket-mounted
  self-update path. The Go tests themselves do not require Docker (there is no integration-test
  harness in this repo — unlike the Trailblaze repos, there is no `tbtest`/fixtures wiring here).

**Schema:** there is no external migration runner and no `migrations/` SQL files loaded by the
app. Schema is created and evolved entirely by the idempotent `migrate()` calls in
`db.Init()` (`CREATE TABLE IF NOT EXISTS`, `ALTER TABLE ADD COLUMN`, `ADD INDEX`), which swallow
MySQL "already exists" errors (1050/1054/1060/1061/1062/1091). The `docker-compose.yml` also
mounts a `./migrations` dir into MariaDB's init hook for a fresh volume, but the running binary
does not depend on it.

---

## How code is written here

This repo follows the global Go standards. Specifics and deviations:

- **Two-layer handler → query flow.** Handlers live in `routers/`, parse the request, call a
  `query.*` function directly, and hand the result to `responder`. There is **no service
  layer** — the only orchestration objects are the six hub-holding handler structs below, and
  those still call queries directly; they hold a WebSocket hub, not business logic.
- **`Queryable` interface.** Identical to every other repo: `Exec`, `Prepare`, `Query`,
  `QueryRow`. Every query function takes `db.Queryable` first, so `*sql.DB` and `*sql.Tx` are
  interchangeable (deploy/rollback wrap record creation in a `db.BeginTx()` transaction).
- **squirrel, always as `sq`.** Build with `sq.Select(...).From(...).Where(sq.Eq{...})`,
  `.ToSql()`, then `engine.Query`. Never hand-concatenate SQL.
- **Pagination.** `db.DEFAULT_LIMIT = 50`, `db.MAX_LIMIT = 500`. List queries clamp: a limit of
  0 or > MAX becomes DEFAULT.
- **Responder envelope.** Success `{success, message, data, pagination?}`; error
  `{success, error, error_message, error_code}`. Messages are lowercased. 5xx never leaks the
  raw error (`error` becomes `"internal server error"`); 4xx exposes it. Custom error codes are
  used where the frontend branches on them: `4004` pending user, `4030`/`4031` CSRF,
  `4290` rate limit, `4003`-class → frontend redirects to `/unauthorized`.
- **Naming.** Handlers `Handle{Verb}{Entity}`, files `Handle{...}.router.go` (PascalCase).
  Query files lowercase `{entity}.query.go`, functions mirror the handler minus `Handle`.
  Structs `{Entity}.struct.go` PascalCase. Constants `SCREAMING_SNAKE_CASE`.
- **Nullable columns are pointers** (`*string`, `*int`, `*time.Time`). `PasswordHash` and
  `TokensRevokedAt` carry `json:"-"` so they never serialize.
- **Env-var interpolation in deploy specs.** When building container specs the deploy path
  resolves `${VAR}` / `$VAR` references (compose semantics) against a merged map of
  `global_env_vars` (decrypted, base layer) then stack-level env vars (override). See
  `resolveEnvRef` / `resolveVarsInValue` in `HandleDeployStack.router.go`. Memory limits are
  stored in MB and converted to bytes (`*1024*1024`) before being sent to the runner.
- **Secrets at rest.** Registry passwords, DB passwords, SSO client secret, and global env-var
  values are AES-256-GCM encrypted via `crypto.Encrypt` when `ENCRYPTION_KEY` is set; `crypto`
  degrades to passthrough when it is not (so existing plaintext keeps working). **Never log a
  decrypted value.**
- **Goroutine discipline.** Background message handling goes through `safeGo` (semaphore of 100,
  panic-recovered). Deployment monitors are bounded by `maxConcurrentDeploys = 10`. Follow this
  pattern for any new fan-out — do not spawn unbounded goroutines per message.
- **Audit logging.** State-changing handlers call `logAudit(r, action, entity, id, details)`
  (in `routers/audit.go`). Add an audit call to any new mutating route.
- **Tests.** Standard `testing`, table-driven, no testify. Present for `crypto`, `jwt`,
  `healthscan`, `middleware` (auth/csrf), `responder`, `socket/protocol`, `tools`. There is no
  DB integration suite; query packages are exercised indirectly.

### Handler types (struct receivers)

Six handlers need a WebSocket hub reference and are therefore methods on structs constructed in
`initApp()` and registered in `main.go`:

| Struct | Holds | Handles |
|--------|-------|---------|
| `DeployHandler` | WorkerHub, AdminHub | deploy stack, public/token deploy, rollback, deployment monitor |
| `ContainerActionHandler` | WorkerHub | start/stop/kill/restart/pause/unpause/remove/recreate/force-remove container; delete container; stack restart/stop/start-all; delete stack |
| `WorkerActionHandler` | WorkerHub | reboot OS, upgrade runner, stop-all, start-all |
| `VolumeHandler` | WorkerHub | list/create/delete worker volumes |
| `NetworkHandler` | WorkerHub | list/create/delete worker networks |
| `DatabaseHandler` | WorkerHub, AdminHub | DB instance create/update/delete, start/stop/restart/remove action, credentials, snapshot create/restore, backup-destination test |

Everything else is a package-level `routers.Handle*` function.

---

## Domain & architecture

### Request flow

```
lattice-web (browser) ──REST/HTTPS──▶ /admin/*  ─┐
lattice-mcp (Claude)  ──REST + Bearer─▶ /admin/*  ─┼─▶ DualAuthMiddleware ─▶ RejectPending ─▶ [RequireEditor|RequireAdmin] ─▶ handler ─▶ query ─▶ MariaDB
CI/CD (deploy token)  ──POST──────────▶ /api/deploy/{token}                                                                        │
                                                                                                                                   ▼
lattice-web (browser) ──WS───────────▶ /ws/admin  ◀── AdminHub broadcast ◀───────────────────────────── command dispatch ─▶ WorkerHub.SendJSONToWorker
lattice-runner (host) ──WS (outbound)─▶ /ws/worker ◀──────────────────────────────────────────────────────────────────────────────┘
```

The middleware chain applied globally (in order) is: `RateLimitMiddleware` → `RequestIDMiddleware`
→ `LoggingMiddleware` → `MuxHeaderMiddleware` → `SecurityHeadersMiddleware` → `CSRFMiddleware` →
`MaxBodySize(1MB)`. `/admin` additionally applies `DualAuthMiddleware` then `RejectPending`;
individual mutating routes wrap the handler in `RequireEditor` or `RequireAdmin`.

### Auth model — three credentials, one middleware

> **Note:** Forta OAuth has been **removed** from this repo and replaced by a generic
> OAuth2/OIDC SSO client. The real config is the `SSO_*` env vars and DB-backed `sso.*`
> settings — there are no `FORTA_*` vars or `/forta/*` routes anymore. A DB migration renames
> `users.forta_id → sso_subject`. If you find a stray Forta reference anywhere, it is stale.

`DualAuthMiddleware` (`middleware/auth.go`) authenticates a request by trying, in order:

1. **Local JWT** from `Authorization: Bearer` — HS512, validated by `jwt.ValidateToken`,
   `Type == "access"`, user active, and issued **after** `users.tokens_revoked_at` if set.
2. **Local JWT** from the `lattice-access-token` cookie — same validation.
3. **Long-lived API token** from `Authorization: Bearer` — SHA-256 hashed, looked up in
   `api_tokens`, must be active and belong to an active user. `last_used_at` is touched.

SSO users are **not** a separate auth path at request time: the SSO callback issues them a
Lattice JWT, so they authenticate exactly like local users. The one extra step is
`checkpointSSOGrant` — for users with `auth_type == "sso"`, the middleware re-introspects the
stored refresh token against the IDP at most every **5 minutes** (`ssoCheckpointTTL`). If the IDP
reports `active: false`, the middleware **stamps `users.tokens_revoked_at = NOW()`** (via
`RevokeUserTokens`) — this is what actually locks the user out: `validateLatticeToken` then rejects
every existing access/refresh JWT issued before that moment. The `sso_sessions` row is also
deleted and the request 401s. (Revoking tokens is essential — deleting the session alone would
drop the *next* request into the `sess == nil` allow-path, keeping a revoked user authenticated
for the full JWT window.) Network errors **fail open** (allow) to avoid logging everyone out
during an IDP blip.

**RBAC roles:** `admin` > `editor` > `viewer`, plus `pending`.
- `RejectPending` blocks `pending` users from all `/admin` routes (they can still hit
  `/auth/self` to see their status) with `error_code 4004`.
- `RequireEditor` gates all resource mutations (deploy, container/stack/worker/registry/DB CRUD).
- `RequireAdmin` gates users, webhooks, global env vars, audit log, SSO/SMTP config, notification
  prefs, deploy-token create/delete, worker reboot/upgrade, and version refresh/self-update.

**Worker auth** is entirely separate: `WorkerTokenAuth` reads `X-Worker-Token` (or `?token=` on
the WebSocket upgrade, since browsers/clients can't set headers on the handshake), SHA-256 hashes
it, and looks it up in `worker_tokens` to resolve a `worker_id`.

**CSRF:** double-submit cookie (`lattice-csrf` / `X-CSRF-Token`), constant-time compare. Exempt:
safe methods, any `Authorization: Bearer` request (API tokens/JWT headers don't need it),
`/auth/login`, `/auth/refresh`, `/ws/worker`, `/api/deploy/*`, `/auth/sso/callback`.

**Rate limiting:** per-IP token bucket. Auth/deploy endpoints 1 rps burst 5; general `/admin`
& `/auth` 30 rps burst 60. `/healthcheck`, `/ws/*`, `/version`, `/install/runner`, and the SSO
config/login/callback routes are exempt. The client IP is taken from the **TCP peer
(`RemoteAddr`) by default** — `X-Forwarded-For` / `X-Real-IP` are only honored when the peer is
listed in `TRUSTED_PROXIES` (comma-separated IPs/CIDRs), so header spoofing can't bypass the
limiter or flood the bucket map. The bucket map is capped (`maxBuckets`) and stale entries are
evicted, bounding memory.

### WebSocket hubs

Two independent hubs, both created in `initApp()`:

- **WorkerHub** (`socket/hub.go`) — `map[int]*WorkerSession` keyed by `worker_id`, cap
  `MaxWorkerSessions = 500`. Re-registration of an existing worker **replaces** the old session
  (so a reconnecting runner takes over its slot). `SendToWorker`/`SendJSONToWorker` push an
  `Envelope` into the session's buffered `Send` channel (returns `ErrWorkerNotConnected` /
  `ErrSendQueueFull` — every deploy path checks `IsConnected` before claiming a stack).
- **AdminHub** (`socket/admin.go`) — `map[string]*AdminSession`, cap `MaxAdminSessions = 50`.
  `BroadcastJSON` fans an event out to every connected dashboard. Sessions can `subscribe` /
  `unsubscribe` to topics (e.g. `worker:3`); `BroadcastFiltered` respects them, and a session
  with no subscriptions receives everything.

`socket/handler.go` runs the worker read/write pumps: `writeWait 10s`, `pongWait 90s`,
`pingPeriod ~72s`, `maxMessageSize 64KB`, send buffer 128. Origin is checked by
`CheckAllowedOrigin` (matches scheme+host against `ALLOWED_ORIGINS`; empty Origin — non-browser
runner — is allowed). The connect/disconnect/message callbacks are set in `message_handlers.go`:

- **OnConnect:** mark worker online, cancel any pending disconnect alert, broadcast
  `worker_connected`, and re-push DB snapshot schedules to the runner (`distributeDbSchedules`).
- **OnDisconnect:** mark offline, broadcast `worker_disconnected`, fire the
  `worker.disconnected` webhook, and schedule a grace-delayed email alert.
- **OnMessage:** switch on `msg.Type` — heartbeat (metrics + container-name reconciliation),
  registration (OS/arch/docker/runner version; resolves pending upgrade actions),
  container status/health/sync/logs, lifecycle logs, deployment progress/status, worker
  action status, shutdown/crash, volume/network list responses, exec output, and the
  `db_*` database-management responses. Most cases both broadcast to the admin hub **and**
  persist to the DB (often via `safeGo`).

### Worker protocol (`socket/protocol.go`)

`Envelope` (API→worker: `type`, `command_id`, `worker_id`, `issued_at`, `payload`) and
`IncomingMessage` (worker→API: adds `status`, keeps `Raw`).

| Direction | Message types |
|-----------|---------------|
| **API → worker** | `connected`, `deploy`, `start`, `stop`, `kill`, `restart`, `pause`, `unpause`, `remove`, `recreate`, `pull_image`, `reboot_os`, `upgrade_runner`, `stop_all`, `start_all`, `list_volumes`, `create_volume`, `remove_volume`, `list_networks`, `create_network`, `remove_network`, `force_remove`, `deployment_ping`, exec (`exec_start`/`exec_input`/`exec_resize`/`exec_close`), db (`db_create`/`db_start`/`db_stop`/`db_restart`/`db_remove`/`db_snapshot`/`db_restore`/`db_update_schedule`/`db_delete_snapshot_file`/`backup_dest_test`) |
| **worker → API** | `heartbeat`, `registration`, `container_status`, `container_health_status`, `container_sync`, `container_logs`, `deployment_progress`, `deployment_status`, `lifecycle_log`, `worker_action_status`, `worker_shutdown`, `worker_crash`, `list_volumes_response`, `list_networks_response`, `exec_output`, db responses (`db_status`/`db_health_status`/`db_snapshot_status`/`db_restore_status`/`backup_dest_test_result`) |
| **admin client → API** | `subscribe`, `unsubscribe`, and the exec messages (relayed straight through to the target worker) |

### Deploy strategies & the deployment monitor

A stack carries a `deployment_strategy` string (default **`rolling`**, set in
`HandleCreateStack`; `blue-green` and `canary` are the other values). **The API does not
implement the strategy** — it resolves each container into a full spec (image+tag, merged env
vars, ports, volumes, health check, CPU/mem limits, networks + DNS aliases, and registry auth
matched by image hostname), records a `deployment` + `deployment_containers` in a transaction,
and sends one `deploy` Envelope to the worker with `strategy` in the payload. The runner performs
the strategy. `canary` deploys can require an explicit `POST /deployments/{id}/approve` to
proceed (`ApproveDeployment`).

Concurrency safety: `ClaimStackForDeploy` atomically flips the stack to `deploying` — a second
deploy/rollback returns `409`. `deployment_monitor.go` then runs a watchdog goroutine
(`deployPingInterval 15s`, `deployStallTimeout 45s`, `deployMaxRetryCount 3`,
`deployMaxRuntime 30m`, bounded to `maxConcurrentDeploys 10`): it pings the worker, and if no
non-monitor deployment-log progress appears within 45s it re-sends the deploy up to 3 times,
finally marking the deployment + stack `failed` transactionally. When the monitor pool
(`maxConcurrentDeploys 10`) is saturated, an overflow deploy is **not** left unwatched — it gets a
lightweight watchdog that does no pinging/retrying but still force-fails the deploy if it never
reaches a terminal state within `deployMaxRuntime`. Deployment status flow:
`pending → deploying → deployed | failed | rolled_back`; the stack status mirrors the terminal
state. **Rollback** (`HandleRollbackDeployment`) rebuilds specs from the previous successful
deployment's recorded image/tag (everything else from the live container rows) and dispatches a
`deploy` with `rollback: true`.

### External systems

- **Every worker VM** runs `lattice-runner`, which dials `/ws/worker` outbound (so no inbound
  ports on hosts) and executes Docker commands.
- **Docker registries** — `registry/client.go` speaks the Registry v2 API for repo/tag listing,
  credential tests, and manifest-digest polling (the `watcher` uses digests to detect `latest`
  re-pushes).
- **SMTP** — `mailer/` sends alert emails; config lives in the `settings` table.
- **GitHub** — `versions/` polls release tags for update availability.
- **An OAuth2/OIDC IDP** — optional SSO; config in `settings` (`sso.*`) with `SSO_*` env-var
  fallback. Provider-agnostic, with special handling for a "Forta envelope" response shape.

---

## Route surface

Built directly from the registrations in `main.go`. `[E]` = wrapped in `RequireEditor`,
`[A]` = `RequireAdmin`; all `/admin/*` first pass `DualAuthMiddleware` + `RejectPending`.

**Public / unauthenticated**

| Method | Path | Handler |
|--------|------|---------|
| GET | `/` | inline — "Lattice API" |
| GET | `/healthcheck` | inline — pings DB, `{status,db}` (skips logging) |
| GET | `/version` | inline — `{"version": Version}` |
| GET | `/install/runner` | `HandleInstallRunner` (embedded script) |
| POST | `/api/deploy/{token}` | `DeployHandler.HandlePublicDeploy` (deploy-token auth; `?container=` for single-container, `?commit=` for audit) |

**Auth**

| Method | Path | Handler |
|--------|------|---------|
| POST | `/auth/login` | `HandleLocalLogin` (sets JWT cookies) |
| POST | `/auth/refresh` | `HandleAuthRefresh` |
| GET | `/auth/sso/login` | `sso.LoginHandler` (302 to IDP) |
| GET | `/auth/sso/callback` | `HandleSSOCallback` (exchanges code, provisions user, issues JWT) |
| GET | `/auth/sso/config` | inline — public SSO config for the login page |
| GET | `/auth/self` | `HandleAuthSelf` (protected) |
| PUT | `/auth/self` | `HandleUpdateSelf` (protected) |
| POST | `/auth/logout` | `HandleLogout` (protected) |

**Workers** (`/admin`)

| Method | Path | Handler |
|--------|------|---------|
| GET / POST | `/workers` | `HandleGetWorkers` / `HandleCreateWorker` `[E]` |
| GET / PUT / DELETE | `/workers/{id}` | `HandleGetWorker` / `HandleUpdateWorker` `[E]` / `HandleDeleteWorker` `[E]` |
| GET / POST | `/workers/{id}/tokens` | `HandleGetWorkerTokens` / `HandleCreateWorkerToken` `[E]` |
| GET | `/workers/{id}/metrics` | `HandleGetWorkerMetrics` |
| GET | `/workers/{id}/container-stats` | `HandleGetWorkerContainerStats` |
| POST | `/workers/{id}/reboot` | `WorkerActionHandler.HandleRebootWorker` `[A]` |
| POST | `/workers/{id}/upgrade` | `WorkerActionHandler.HandleUpgradeRunner` `[A]` |
| POST | `/workers/{id}/stop-all` / `/start-all` | `WorkerActionHandler.HandleStopAllContainers` / `HandleStartAllContainers` `[E]` |
| POST | `/workers/{id}/force-remove` | `ContainerActionHandler.HandleForceRemoveContainer` `[E]` |
| GET / POST | `/workers/{id}/volumes` | `VolumeHandler.HandleListVolumes` / `HandleCreateVolume` `[E]` |
| DELETE | `/workers/{id}/volumes/{name}` | `VolumeHandler.HandleDeleteVolume` `[E]` |
| GET / POST | `/workers/{id}/networks` | `NetworkHandler.HandleListNetworks` / `HandleCreateNetwork` `[E]` |
| DELETE | `/workers/{id}/networks/{name}` | `NetworkHandler.HandleDeleteNetwork` `[E]` |
| DELETE | `/worker-tokens/{id}` | `HandleDeleteWorkerToken` `[E]` |

**API tokens & networks**

| Method | Path | Handler |
|--------|------|---------|
| GET / POST | `/api-tokens` | `HandleListApiTokens` / `HandleCreateApiToken` `[E]` |
| DELETE | `/api-tokens/{id}` | `HandleDeleteApiToken` `[E]` |
| GET | `/networks` | `HandleListAllNetworks` |
| DELETE | `/networks/{id}` | `HandleDeleteNetworkByID` `[E]` |

**Stacks & deploy tokens**

| Method | Path | Handler |
|--------|------|---------|
| GET / POST | `/stacks` | `HandleGetStacks` / `HandleCreateStack` `[E]` |
| POST | `/stacks/import` | `HandleImportCompose` `[E]` |
| GET / PUT / DELETE | `/stacks/{id}` | `HandleGetStack` / `HandleUpdateStack` `[E]` / `ContainerActionHandler.HandleDeleteStack` `[E]` |
| PUT | `/stacks/{id}/compose` | `HandleUpdateCompose` `[E]` |
| POST | `/stacks/{id}/sync-compose` | `HandleSyncCompose` `[E]` |
| POST | `/stacks/{id}/deploy` | `DeployHandler.HandleDeployStack` `[E]` |
| POST | `/stacks/{id}/restart-all` / `stop-all` / `start-all` | `ContainerActionHandler.HandleRestartStack` / `HandleStopStack` / `HandleStartStack` `[E]` |
| GET | `/stacks/{id}/export` | `HandleExportStack` |
| POST | `/stacks/import-export` | `HandleImportStackExport` `[E]` |
| POST | `/stacks/{id}/save-template` | `HandleCreateTemplateFromStack` `[E]` |
| GET / POST | `/stacks/{id}/deploy-tokens` | `HandleListDeployTokens` / `HandleCreateDeployToken` `[A]` |
| DELETE | `/deploy-tokens/{id}` | `HandleDeleteDeployToken` `[A]` |

**Containers**

| Method | Path | Handler |
|--------|------|---------|
| GET | `/containers` | `HandleListAllContainers` (filter by `stack_id`, `worker_id`) |
| GET / POST | `/stacks/{id}/containers` | `HandleGetContainers` / `HandleCreateContainer` `[E]` |
| GET / PUT / DELETE | `/containers/{id}` | `HandleGetContainer` / `HandleUpdateContainer` `[E]` / `ContainerActionHandler.HandleDeleteContainer` `[E]` |
| GET | `/containers/{id}/logs` | `HandleGetContainerLogs` |
| GET | `/containers/{id}/lifecycle` | `HandleGetLifecycleLogs` |
| GET | `/containers/{id}/metrics` | `HandleGetContainerMetrics` |
| POST | `/containers/{id}/{start,stop,kill,restart,pause,unpause,remove,recreate}` | `ContainerActionHandler.*` `[E]` (eight separate routes) |

**Deployments**

| Method | Path | Handler |
|--------|------|---------|
| GET | `/deployments` | `HandleGetDeployments` |
| GET | `/deployments/{id}` | `HandleGetDeployment` |
| GET | `/deployments/{id}/logs` | `HandleGetDeploymentLogs` |
| POST | `/deployments/{id}/approve` | `HandleApproveDeployment` `[E]` |
| POST | `/deployments/{id}/rollback` | `DeployHandler.HandleRollbackDeployment` `[E]` |

**Registries**

| Method | Path | Handler |
|--------|------|---------|
| GET / POST | `/registries` | `HandleGetRegistries` / `HandleCreateRegistry` `[E]` |
| POST | `/registries/test` | `HandleTestRegistryInline` `[E]` |
| PUT / DELETE | `/registries/{id}` | `HandleUpdateRegistry` `[E]` / `HandleDeleteRegistry` `[E]` |
| POST | `/registries/{id}/test` | `HandleTestRegistry` `[E]` |
| GET | `/registries/{id}/repositories` / `/tags` | `HandleListRegistryRepos` / `HandleListRegistryTags` |

**Database instances, snapshots & backup destinations**

| Method | Path | Handler |
|--------|------|---------|
| GET / POST | `/database-instances` | `HandleListDatabaseInstances` / `DatabaseHandler.HandleCreateDatabaseInstance` `[E]` |
| GET / PUT / DELETE | `/database-instances/{id}` | `HandleGetDatabaseInstance` / `DatabaseHandler.HandleUpdateDatabaseInstance` `[E]` / `HandleDeleteDatabaseInstance` `[E]` |
| POST | `/database-instances/{id}/{start,stop,restart,remove}` | `DatabaseHandler.HandleDatabaseAction` `[E]` (action derived from the last path segment) |
| GET | `/database-instances/{id}/credentials` | `DatabaseHandler.HandleGetDatabaseCredentials` `[E]` (returns live secrets) |
| GET / POST | `/database-instances/{id}/snapshots` | `HandleListSnapshots` / `DatabaseHandler.HandleCreateSnapshot` `[E]` |
| POST | `/database-instances/{id}/restore` | `DatabaseHandler.HandleRestoreSnapshot` `[E]` |
| DELETE | `/database-snapshots/{id}` | `HandleDeleteSnapshot` `[E]` |
| GET / POST | `/backup-destinations` | `HandleListBackupDestinations` / `HandleCreateBackupDestination` `[E]` |
| GET / PUT / DELETE | `/backup-destinations/{id}` | `HandleGetBackupDestination` / `HandleUpdateBackupDestination` `[E]` / `HandleDeleteBackupDestination` `[E]` |
| POST | `/backup-destinations/{id}/test` | `DatabaseHandler.HandleTestBackupDestination` `[E]` |

**Admin config, discovery & self-update** (all `/admin`)

| Method | Path | Handler |
|--------|------|---------|
| GET / POST / PUT / DELETE | `/users`, `/users/{id}` | `HandleGetUsers` / `HandleCreateUser` / `HandleUpdateUser` / `HandleDeleteUser` `[A]` |
| GET / POST / PUT / DELETE | `/webhooks`, `/webhooks/{id}` | `HandleListWebhooks` / `HandleCreateWebhook` / `HandleUpdateWebhook` / `HandleDeleteWebhook` `[A]` |
| POST | `/webhooks/{id}/test` | `HandleTestWebhook` `[A]` |
| GET / POST / PUT / DELETE | `/env-vars`, `/env-vars/{id}` | `HandleListGlobalEnvVars` / `HandleCreateGlobalEnvVar` `[A]` / `HandleUpdateGlobalEnvVar` `[A]` / `HandleDeleteGlobalEnvVar` `[A]` |
| GET / POST / DELETE | `/templates`, `/templates/{id}` | `HandleListTemplates` / `HandleCreateTemplate` `[E]` / `HandleDeleteTemplate` `[E]` |
| GET | `/audit-log` | `HandleGetAuditLog` `[A]` |
| GET / PUT | `/sso-config` | `HandleGetSSOConfig` `[A]` / `HandleUpdateSSOConfig` `[A]` |
| GET / PUT | `/smtp-config` | `HandleGetSMTPConfig` `[A]` / `HandleUpdateSMTPConfig` `[A]` |
| POST | `/smtp-config/test` | `HandleTestSMTP` `[A]` |
| GET / PUT | `/notification-prefs` | `HandleGetNotificationPrefs` `[A]` / `HandleUpdateNotificationPrefs` `[A]` |
| GET | `/search` | `HandleSearch` |
| GET | `/overview` | `HandleGetOverview` |
| GET | `/fleet-metrics` | `HandleGetFleetMetrics` |
| GET | `/anomalies` | `HandleGetAnomalies` |
| GET | `/versions` | `HandleGetVersions` |
| POST | `/versions/refresh` | `HandleRefreshVersions` `[A]` |
| POST | `/update/api` / `/update/web` | `HandleUpdateAPI` `[A]` / `HandleUpdateWeb` `[A]` (self-update via Docker socket) |

**WebSocket**

| Method | Path | Handler |
|--------|------|---------|
| GET | `/ws/worker` | `WorkerHandler` (`X-Worker-Token` / `?token=`) |
| GET | `/ws/admin` | `AdminHandler` wrapped in `DualAuthMiddleware` → `RejectPending`; the socket `AuthFunc` also rejects `pending`. The exec relay (`exec_start`/`exec_input`/`exec_resize`/`exec_close`) is gated to **editor+** using the role captured onto the `AdminSession` at connect — a `viewer` cannot open a container shell. |

---

## Ecosystem & related repos

Owner is **`aidenappl`** for the appleby.cloud repos.

| Repo | Relationship |
|------|--------------|
| [`lattice-web`](https://github.com/aidenappl/lattice-web) | Next.js dashboard. Consumes `/admin/*` REST + `/ws/admin`. Field names in this API's JSON are its contract. |
| [`lattice-runner`](https://github.com/aidenappl/lattice-runner) | The agent on each worker VM. Dials `/ws/worker` **outbound**, executes Docker ops, and implements the deploy strategies. The worker protocol in `socket/protocol.go` is the shared contract — change both together. |
| [`lattice-mcp`](https://github.com/aidenappl/lattice-mcp) | MCP server exposing `/admin` as 125 Claude tools. **When you add/change/remove an `/admin` route, add or consciously skip the matching MCP tool in the same change** — lattice-mcp previously drifted ~2 months behind this API. |
| [`forta-api`](https://github.com/aidenappl/forta-api) / [`forta-web`](https://github.com/aidenappl/forta-web) | The appleby.cloud SSO/identity provider. Lattice's SSO client can point at Forta (hence the "Forta envelope" handling in `sso/`), but Lattice no longer depends on `go-forta` — it uses a generic OAuth2/OIDC client and can run standalone on local auth. |
| [`keyring-api`](https://github.com/aidenappl/keyring-api) / [`keyring-actions`](https://github.com/aidenappl/keyring-actions) | Secrets platform. Used only in **CI** (`keyring-actions`) to inject registry credentials for the image build; the running binary reads plain env vars. |
| [`monitor-core`](https://github.com/aidenappl/monitor-core) | Observability. Runtime errors/latency for `lattice-api` surface in Monitor (service name `lattice-api`). |

Lattice is self-hosting: Forta, Keyring, Monitor, OpenBucket and the rest all run **as stacks on
Lattice workers**, which is why local auth exists as a fallback when the SSO IDP is itself down.

---

## Operations

- **Deployed on Lattice itself**, at `lattice.appleby.cloud`, as the `lattice-api` container in
  its stack (alongside `lattice-web` and a `lattice-docker-helper`). It mounts the Docker socket
  because `POST /admin/update/api` and `/update/web` shell out to `docker compose` in
  `DOCKER_COMPOSE_DIR` to pull `:latest` and recreate the API/web containers — hence the runtime
  image includes `docker-cli` + `docker-cli-compose` and runs as root.
- **Image:** multi-stage `golang:1.25-alpine` → `alpine:3.19`, static `CGO_ENABLED=0` build with
  `-ldflags="-w -s -X main.Version=${VERSION}"`, `EXPOSE 8000`, Docker `HEALTHCHECK` hitting
  `/healthcheck`. Pushed to `registry.appleby.cloud/lattice-api`.
- **CI** (`.github/workflows/build-and-deploy.yml`): on push to `main`, resolves a version (a
  commit-message `[release-patch|minor|major]` tag bumps the latest `vX.Y.Z` git tag and pushes
  `:latest` + `:vX.Y.Z` + a GitHub release; otherwise pushes `:development` + `:<short-sha>`),
  injects registry creds via `keyring-actions`, builds with buildx, and pushes. **CI builds and
  pushes the image; it does not deploy** — rollout happens via Lattice's own self-update path.
- **Database:** MariaDB, schema `lattice`. Schema is owned by the in-code `migrate()` calls in
  `db.Init()` (no external migration runner). Pool: 25 open / 10 idle / 5-min lifetime.
- **Background jobs** (all started in `initApp`): `versions` (GitHub poll, 30m), `retention`
  (log/metric purge, hourly; windows: container_logs 7d, lifecycle 14d, worker_metrics 30d,
  container_metrics 7d, deployment_logs 90d, audit_log 180d), `watcher` (registry digest poll,
  5m), `healthscan` (worker-vs-DB reconcile, 5m). Plus per-connection ping/pong and
  per-deployment watchdog goroutines.
- **Logs/metrics:** structured logs to stdout (`LOG_FORMAT=text|json`), scraped by the platform
  log pipeline; app errors go to **Monitor** (service `lattice-api`); alert emails via SMTP.
- **Common failure modes:**
  - *Boot panic* — `ValidateSecurityDefaults` panics in production on a short/known-weak
    `JWT_SIGNING_KEY` or weak `LATTICE_ADMIN_PASSWORD`; `db.Init` panics if `DATABASE_DSN` is
    missing/unreachable. Check the container's first log lines.
  - *All `/admin` calls 401* — expired/revoked JWT, or an SSO user whose IDP grant was revoked
    (the 5-min checkpoint killed the session).
  - *Deploy returns 400 "worker is not connected"* — the runner's WebSocket dropped; check
    worker status / `lattice_get_anomalies`.
  - *Deploy stuck in `deploying`* — the watchdog force-fails after 30 min; a stale
    `deploy_claimed_at` (>30 min) is auto-breakable.
  - *"fetch failed" on every Monitor/Lattice MCP call* — the shared TLS proxy cert expired
    (a platform-wide symptom, not a `lattice-api` bug).

---

## Rules & guardrails

- **Never add Docker-strategy logic here.** The API sends a `strategy` string and a spec; the
  runner executes. Blue-green/canary/rolling mechanics belong in `lattice-runner`.
- **Never bypass `DualAuthMiddleware` / RBAC.** New mutating routes go under `/admin` and are
  wrapped in `RequireEditor` or `RequireAdmin`. Match the existing role for the resource class
  (config/users/tokens = admin; resource mutations = editor).
- **Never break the worker protocol unilaterally.** `socket/protocol.go` is shared with
  `lattice-runner`. Add message types additively and update both repos together.
- **Never log a decrypted secret.** Registry passwords, DB passwords, SSO client secret, and
  `global_env_vars` values are encrypted at rest and decrypted only to build a deploy spec or a
  credentials response. There is no debug logging of specs — keep it that way.
- **Never hand-write SQL or reach for an ORM.** squirrel (`sq`) + `db.Queryable` only.
- **Keep schema changes in `db.Init()`** as idempotent `migrate()` calls — there is no separate
  migration runner and no fixtures harness. Test that a fresh MariaDB and an existing one both
  come up clean.
- **Respect goroutine bounds** — `safeGo` (100) for message handling, `maxConcurrentDeploys`
  (10) for monitors. No unbounded per-message goroutines.
- **Add an audit call** (`logAudit`) to every new mutating handler, and **add/skip the matching
  `lattice-mcp` tool** in the same change as any route change.
- **Follow the global git/deploy guardrails:** never push (except when explicitly authorized —
  as for this doc task), never amend/rebase/force, never trigger a remote deploy or touch
  production infra, never create/modify `.env`, never commit secrets or the `certs/*.pem` files.

---

## Verification — always before "done"

```bash
gofmt -l .        # must print nothing (CI rejects unformatted code); `gofmt -w -s .` to fix
go build ./...    # must succeed
go vet ./...      # must be clean
go test ./...     # must pass (crypto, jwt, healthscan, middleware, responder, socket, tools)
```

`dev check` runs fmt + vet + test together. There is **no** integration/Docker test suite in this
repo, so a green `go test ./...` is sufficient here (unlike the Trailblaze repos). If you changed
a route, exercise it against a running instance or via `lattice-mcp` — schema-level confidence is
not enough for a control-plane that dispatches real Docker commands.

**Never report work complete on a failing build, vet, or test.**

---

## Keeping this file updated

Update this AGENTS.md **in the same change** when you:

- **Add/remove/rename a route** → update the route surface tables *and* reconcile
  [`lattice-mcp`](https://github.com/aidenappl/lattice-mcp).
- **Change the auth model, roles, CSRF, or rate limits** → update *Domain & architecture* →
  *Auth model*.
- **Change the worker WebSocket protocol** (`socket/protocol.go`) → update the protocol tables
  *and* [`lattice-runner`](https://github.com/aidenappl/lattice-runner).
- **Change deploy/rollback mechanics or the monitor constants** → update *Deploy strategies*.
- **Alter the schema** (a new `migrate()` call in `db/db.go`) → update *Project structure* /
  *Operations* and the affected struct/query notes.
- **Add/change a background job or its interval** → update *Operations*.
- **Change env vars, commands, Docker/CI, or the repo's role** → update this file **and**
  `README.md` (its env-var table and route list are the fastest-drifting parts, and both are
  currently stale on the removed Forta auth — fix them when you touch that area).
