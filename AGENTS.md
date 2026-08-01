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
  `registries`, `database_instances`, `database_instance_events`, `database_snapshots`, `backup_destinations`, `networks`,
  `volumes`, `global_env_vars`, `templates`, `webhook_configs`, `deploy_tokens`, `api_tokens`,
  `worker_tokens`, `users`, `sso_sessions`, `audit_log`, `settings`, and the log/metric tables.

  ⚠️ **`sso_sessions` was listed here for a long time before any migration created it.** That
  line is why nobody checked: the docs asserted the table existed, `query/sso_sessions.query.go`
  read it, and every read failed with *table doesn't exist* — which the checkpoint swallowed as a
  generic DB error and allowed. The table is now genuinely created in `db.migrate()`. When you
  add a table to this list, add the `migrate()` call in the same change.
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

- **SSO:** `github.com/aidenappl/go-forta/sso` **v1.6.0** — the shared relying-party SSO
  implementation. Brings `coreos/go-oidc/v3` and `golang.org/x/oauth2` transitively.

**Internal SDKs:** `go-forta/sso` only, and ⚠️ **that is NOT the same as importing `go-forta`
itself.** The root `forta` package validates Forta's own tokens for a service that has delegated
identity to Forta; this repo has its own users and its own JWTs, and imports only the `sso`
subpackage, which runs a login flow against *any* OIDC provider. Forta OAuth-as-identity was
removed and has not returned.

The `sso/` package used to be a self-contained OAuth2 client. It was replaced because it had
drifted: no PKCE at all, a three-attempt token exchange that could not work against single-use
codes, and an identity keyed on email. See *Domain & architecture*. Keyring is used
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
| `jwt/jwt.go` | HS512 local tokens. 15-min access / 7-day refresh. `Claims{UserID, Type}`. Validation pins the alg to HS512 (`WithValidMethods`), requires an expiry (`WithExpirationRequired`) and the issuer, and rejects a token with no `iat` (so it can't bypass `tokens_revoked_at`). Revocation compare is `!IssuedAt.After(revokedAt)` (a token minted in the same second as revocation is rejected). |
| `crypto/crypto.go` | AES-256-GCM encrypt/decrypt for secrets at rest. Passthrough (no-op) when `ENCRYPTION_KEY` unset **only in non-production** — `Init()` **panics at boot** if the key is empty and `ENVIRONMENT=production`. `Decrypt` returns a real error on bad base64 / short input / auth failure (no silent plaintext fallthrough); callers propagate it. |
| `migrate/encrypt.go` | One-off `migrate-encrypt` subcommand logic: encrypts existing plaintext secret values in-place (idempotent, transactional) so the DB can be moved from passthrough to an active `ENCRYPTION_KEY`. Target list must track every encrypted column/setting. |
| `sso/` | `sso.go` (generic OAuth2/OIDC client, DB-backed config, state handling), `introspect.go` (RFC 7662 introspection used by the auth checkpoint). |
| `responder/` | `responder.go` (success envelope: `New`/`NewCreated`/`NewWithCount`), `errors.go` (`SendError`/`SendErrorWithCode`), `templates.responder.go` (`BadBody`/`MissingBodyFields`/`QueryError`/`NotFound`). |
| `routers/` | ~80 handler files, `Handle<Verb><Entity>.router.go`. Most are plain funcs; six use struct receivers that hold hub refs (see *Handler types*). Also `deployment_monitor.go` (watchdog), `backfill_networks.go` (startup migration), `audit.go` (`logAudit` helper). |
| `query/` | 27 query files, `{entity}.query.go`. squirrel builders; every function takes `db.Queryable` first. `errors.go` holds shared query errors. |
| `structs/` | 20 `{Entity}.struct.go` domain types with `json` tags and pointer nullables. |
| `socket/` | `hub.go` (WorkerHub + WorkerSession), `admin.go` (AdminHub + AdminSession with topic subscriptions), `handler.go` (WorkerHandler — upgrade, read/write pumps, ping/pong), `protocol.go` (all message-type constants + `Envelope`/`IncomingMessage`). |
| `registry/client.go` | Docker Registry v2 HTTP client — `Ping`, `ListRepositories`, `ListTags`, `GetManifestDigest`. |
| `healthscan/scanner.go` | Periodic worker-vs-DB reconciliation; emits `health_anomalies` to the admin hub. |
| `watcher/watcher.go` | Polls registries for mutable-tag re-pushes (digest change) → fires `image.updated` / auto-deploy webhooks. |
| `retention/retention.go` | Hourly batch purge of old log/metric rows per retention window. Also `database_instance_events` (180d) and soft-deleted `database_snapshots` (90d, gated on `active = 0` via `purgeWhere` — a live snapshot row is the only record of where its remote file lives, so purging by age alone would orphan objects on the destination). |
| `webhooks/dispatcher.go` | `Fire(event, data)` — async outbound webhooks, optional HMAC-SHA256 signing. |
| `versions/versions.go` | Polls GitHub `releases/latest` for api/web/runner every 30 min; in-memory cache. |
| `mailer/` | `mailer.go` (SMTP send + HTML template, config from `settings`), `prefs.go` (notification prefs, cooldowns, grace timers, unhealthy thresholds). |
| `bootstrap/admin.go` | First-run: creates the local admin user from `LATTICE_ADMIN_EMAIL`/`_PASSWORD` if no users exist. |
| `tools/` | `password.tool.go` (bcrypt cost 12), `token.tool.go` (opaque token gen + SHA-256 hash-at-rest), `validate.go` (name/email/password/YAML-size validators; `ValidateExternalURL` SSRF guard — HTTPS-only, blocks private/reserved/CGNAT IPs, **fails closed** on DNS failure; `NewSafeHTTPClient` pins the dialer to public IPs at connect time to defeat DNS-rebinding). Outbound clients for user/admin-configured URLs (webhooks, registry) use `NewSafeHTTPClient`. |
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

**One-off subcommands:** the binary runs the server by default, but `lattice-api migrate-encrypt
[--dry-run]` runs the secrets-at-rest migration instead and exits (wired in `main.go`, implemented
in `migrate/`). It does minimal init (logger + `db.Init` + `crypto.Init`), so it needs
`ENCRYPTION_KEY` and `DATABASE_DSN` in the environment. Run it as a one-off against the prod stack
before booting the server with a newly-set key:

```bash
# on the host, image pinned to the new tag, ENCRYPTION_KEY already in .env:
docker compose -f /opt/lattice/docker-compose.yml run --rm lattice-api migrate-encrypt --dry-run
docker compose -f /opt/lattice/docker-compose.yml run --rm lattice-api migrate-encrypt
docker compose -f /opt/lattice/docker-compose.yml up -d lattice-api   # server boots, data already ciphertext
```

`--dry-run` reports encrypt/already/empty counts per target and rolls back. The real run is
idempotent and transactional.

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
- **Not-found handling.** Single-row `GetXByID` getters return `query.ErrNotFound` (a typed
  error exposing `NotFound() bool`) when the row is missing, instead of wrapping `sql.ErrNoRows`
  in a generic error. `responder.QueryError` detects it via a local `notFounder` interface and
  responds `404` rather than `500` — so any handler funnelling its query error through
  `QueryError` gets consistent not-found behaviour. (responder stays a leaf package: it detects
  the interface, it does not import `query`.)
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
- **Secrets at rest.** These fields are AES-256-GCM encrypted via `crypto.Encrypt` when
  `ENCRYPTION_KEY` is set: `registries.password`, `global_env_vars.encrypted_value`,
  `database_instances.root_password`/`.password`, `backup_destinations.config`, and the
  `settings` rows `sso.client_secret` and `smtp.password` (SSO session tokens too, but those are
  ephemeral). `crypto` degrades to passthrough when the key is unset (so existing plaintext keeps
  working). **Never log a decrypted value.**
- **Turning encryption on for an existing DB is a migration, not a config toggle.** If rows were
  written in passthrough (plaintext), setting `ENCRYPTION_KEY` makes reads either error
  (`registries` propagates it → deploys fail) or silently blank (`global_env_vars` swallows it).
  Run the one-off `migrate-encrypt` subcommand *before* the server boots with the key active — it
  encrypts every plaintext value in one transaction and is idempotent (already-ciphertext rows are
  skipped). See **Running, building & testing**. Keep `migrate/encrypt.go`'s target list in sync
  with every encrypted column/setting above.
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
   **Scopes are enforced:** a token's `scopes` column is a comma-separated list of `read` /
   `write` / `admin`. A `read`-only (or nil-of-write) token is limited to safe methods
   (GET/HEAD/OPTIONS) — a mutating request with it gets `403`. `write`/`admin` allow all methods
   (still subject to the owning user's RBAC role). A **nil/empty** scope is unrestricted — the
   backward-compatible default, since the dashboard and MCP historically never sent a scopes
   field (so every pre-existing token is nil). `HandleCreateApiToken` validates/normalizes the
   scopes field (`middleware.NormalizeApiTokenScopes`) and rejects unknown values with a `400`.
   Enforcement lives in `DualAuthMiddleware` (`apiTokenScopeAllows`).

SSO users are **not** a separate auth path at request time: the SSO callback issues them a
Lattice JWT, so they authenticate exactly like local users. The one extra step is
`checkpointSSOGrant` — for users with `auth_type == "sso"`, the middleware re-introspects the
stored refresh token against the IdP at most every **5 minutes** (`ssoCheckpointTTL`). The policy
itself lives in `go-forta/sso`'s `Checkpointer`; this repo supplies the stores and maps the result
onto the middleware's bool.

⚠️ **This had NEVER RUN before 2026-07-30, and the reason is worth remembering.** The logic below
was correct in outline, but it opened with `if err != nil { return true }` on the session lookup,
and `sso_sessions` was never created by any migration — so every request took that branch. The
bounded fail-open was unreachable; the effective behaviour was **unbounded** fail-open, and the
only symptom was a warning line. A second, independent reason it could not have worked:
`introspect_url` was read by `LoadConfig` but no handler ever wrote it, so there was no endpoint
to call. Both are fixed; `introspect_url` is now settable through `PUT /admin/sso-config`.

If the IdP reports `active: false`, the checkpoint **stamps `users.tokens_revoked_at = NOW()`**
(via `RevokeUserTokens`, wired as the library's `LocalTokenRevoker`) — this is what actually locks
the user out: `validateLatticeToken` then rejects every existing access/refresh JWT issued before
that moment. The `sso_sessions` row is also deleted and the request 401s. Revoking tokens is
essential — deleting the session alone would drop the *next* request into the `sess == nil`
allow-path, keeping a revoked user authenticated for the full JWT window.

"No answer" (network error, 5xx, decrypt failure) **fails open** only within a bounded grace
window (`ssoCheckpointGrace`, 30 min) measured from the last positive confirmation
(`last_checked_at`, never from now — measuring from now would restart the window on every failed
attempt and grant permanent access to a permanently unreachable IdP). Past that window the result
is `CheckpointUnavailable`.

⚠️ **`CheckpointUnavailable` should be HTTP 503, not 401, and this middleware cannot express
it.** The hook returns a bool, so it denies — which is the right choice of the two available,
since allowing would restore the unbounded fail-open. But a 401 tells clients to discard
credentials and re-authenticate against the IdP that is already down. Widening the hook to carry
a status is the fix.

**RBAC roles:** `admin` > `editor` > `viewer`, plus `pending`.
- `RejectPending` blocks `pending` users from all `/admin` routes (they can still hit
  `/auth/self` to see their status) with `error_code 4004`.
- `RequireEditor` gates all resource mutations (deploy, container/stack/worker/registry/DB CRUD).
- `RequireAdmin` gates users, webhooks, global env vars, audit log, SSO/SMTP config, notification
  prefs, deploy-token create/delete, worker reboot/upgrade, version refresh/self-update, and the
  DB-credentials endpoint (returns live secrets).

**Worker auth** is entirely separate: `WorkerTokenAuth` reads `X-Worker-Token` (or `?token=` on
the WebSocket upgrade, since browsers/clients can't set headers on the handshake), SHA-256 hashes
it, and looks it up in `worker_tokens` to resolve a `worker_id`.

**CSRF:** double-submit cookie (`lattice-csrf` / `X-CSRF-Token`), constant-time compare. Exempt:
safe methods, an `Authorization: Bearer` request **that carries no session cookie** (API-token/JWT
header clients don't need it; but a Bearer header no longer waives CSRF for a request that also
sends the `lattice-access-token` cookie — a cookie-authed browser request still gets checked),
`/auth/login`, `/auth/refresh`, `/ws/worker`, `/api/deploy/*`, `/auth/sso/callback`.

**SSO login CSRF:** the `state` parameter is bound to the browser and is single-use.
`/auth/sso/login` sets an HttpOnly, `SameSite=Lax`, `Path=/auth/sso` cookie (`lattice-sso-state`)
holding the same random `state` it sends to the IDP and stores in the DB (10-min expiry). The
callback requires **both** that the returned `state` constant-time-matches the cookie (an attacker
forging a callback can't set this browser's cookie → CSRF blocked) **and** that `ValidateState`
finds it in the DB, which **consumes** it (single-use). The state cookie is cleared on every
callback. A benign double-callback (provider redirect chains) is tolerated only when the browser
already holds a `lattice-access-token` session, in which case it is redirected to the dashboard.
**Follow-up (not yet implemented):** PKCE (S256 `code_challenge`/`code_verifier`) and OIDC `nonce`
validation — deferred because correctness depends on the IDP's support and the token-exchange has
three fallback request shapes (JSON / basic-auth / body-auth) plus a Forta-envelope response, so
it must be validated against the live IDP before shipping.

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
  `worker_connected`, re-push DB snapshot schedules to the runner (`distributeDbSchedules`), and
  request a full database-container report (`dbReconciler.RequestSync`) so anything that changed
  while the worker was unreachable is corrected immediately.
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
| **API → worker** | `connected`, `deploy`, `start`, `stop`, `kill`, `restart`, `pause`, `unpause`, `remove`, `recreate`, `pull_image`, `reboot_os`, `upgrade_runner`, `stop_all`, `start_all`, `list_volumes`, `create_volume`, `remove_volume`, `list_networks`, `create_network`, `remove_network`, `force_remove`, `deployment_ping`, exec (`exec_start`/`exec_input`/`exec_resize`/`exec_close`), db (`db_create`/`db_start`/`db_stop`/`db_restart`/`db_remove`/`db_snapshot`/`db_restore`/`db_update_schedule`/`db_delete_snapshot_file`/`db_sync_request`/`backup_dest_test`) |
| **worker → API** | `heartbeat`, `registration`, `container_status`, `container_health_status`, `container_sync`, `container_logs`, `deployment_progress`, `deployment_status`, `lifecycle_log`, `worker_action_status`, `worker_shutdown`, `worker_crash`, `list_volumes_response`, `list_networks_response`, `exec_output`, db responses (`db_status`/`db_health_status`/`db_snapshot_status`/`db_restore_status`/`db_delete_snapshot_result`/`db_schedule_status`/`db_sync`/`backup_dest_test_result`) |
| **admin client → API** | `subscribe`, `unsubscribe`, and the exec messages (relayed straight through to the target worker) |

### Managed database lifecycle

Database instances are **not** rows in `containers` — they are their own resource with their own
table, and their containers are labelled `managed-by=lattice`, `lattice-type=database`. Logs,
lifecycle messages and exec are nonetheless shared with ordinary containers, because all three
address a container by **name**, not by `containers.id`. That is why a managed database gets a log
viewer and a console without being registered in `containers`.

**Status vocabulary** — `structs/DatabaseInstance.struct.go` defines `DatabaseStatus`
(`pending` → `provisioning` → `running`, plus `stopped`, `restarting`, `degraded`, `deleting`,
`error`) and `DatabaseHealth` (`none`, `starting`, `healthy`, `unhealthy`). Both have `IsValid()`
and both are enforced at the API boundary. Failure detail never goes in the status: it goes in
`database_instances.last_error` as JSON (`code`, `message`, `occurred_at`, `retryable`), with a
stable `DBErrCode*` code. `degraded` is deliberately non-terminal — a restart-looping container is
impaired, not dead.

**`databaseLifecycle` (`database_lifecycle.go`) owns every status write.** Nothing else may write
`status`, `health_status` or `last_error`. `Transition` is idempotent, records an event in
`database_instance_events`, and broadcasts to the admin hub, so a state change cannot happen
invisibly. Recovery into `running`/`stopped` clears a stale `last_error` automatically.

**Reconciliation (`database_reconciler.go`)** is level-triggered and is what makes the subsystem
self-correcting:

- Every 60s, and on every worker reconnect, the API sends `db_sync_request`. The runner answers
  with `db_sync` — a full report of the database containers it can actually see, with Docker
  state, health, restart count and (when it recognises a fatal startup failure) a `fatal_hint`.
- `handleDbSync`/`reconcileDatabaseInstance` diff observed against desired state and correct the
  difference in either direction. A lost `db_status` is therefore survivable.
- A watchdog runs every 30s and fails out any instance that has sat in a transitional status for
  more than `dbProvisionTimeout` (10m), with `provision_timeout` or `worker_offline`. **This is
  the backstop that makes "stuck in pending forever" impossible.**

**Payload keys are constants, and there is a test that pins them.** `socket/protocol.go` defines a
`Payload*` constant for every cross-boundary key, and `socket/db_payload_contract_test.go` asserts
their wire spellings. This exists because **ten** defects shipped with one shape — the API writing
one key name and the runner reading another, each yielding an empty value rather than an error:

| API sent | runner read | result |
|---|---|---|
| `backup_destination` | `backup_dest` | every scheduled snapshot silently no-op'd, from May to July 2026 |
| `backup_destination.type` | `dest_type` | `NewDestination("")` — every *manual* snapshot failed too |
| `backup_destination.config` | `dest_config` | no credentials |
| `filename` | `remote_path` | empty object key; the remote-delete path refused with "missing remote_path" |
| `snapshot_id` (number) | `snapshot_id.(string)` | `""` → parsed to 0 → **every** snapshot status reply dropped |
| `snapshot_id` (restore) | `restore_id` | restore correlation lost |

The older `db_contract_test.go` and `db_dispatch_test.go` could not catch any of these: the first
pins reply *type* names and correlation keys, the second greps source text for constant
*identifiers*. Neither ever compared a payload's key set. **When adding or renaming a `db_*` payload
key, change the constant, the contract test, and the runner in the same commit.** The runner also
accepts the legacy spellings, so a runner upgraded ahead of the control plane repairs the family on
its own — deploy the runner first.

**A snapshot schedule requires a backup destination.** `distributeDbSchedules` only registers
instances that have one, so a cron with a null `backup_destination_id` saves, renders, and never
fires. Create and update reject that combination (`validateSnapshotSchedulable`), and the UI and MCP
descriptions say so — the API guard is the load-bearing one, since `lattice-mcp` bypasses the UI.

**Schedules reach the worker on change, not just on reconnect.** `routers.PushDbSchedule` is called
by the create and update handlers; `routers.DistributeDbSchedules` is the worker-connect sync. Both
build the payload with `routers.BuildDbSchedulePayload`, so a schedule cannot be expressed two ways.
Clearing a schedule pushes an empty cron so the runner drops the job.

**Backup fields can be unset.** `UpdateDatabaseInstanceRequest` has `ClearSnapshotSchedule`,
`ClearRetentionCount` and `ClearBackupDestination`, because a nil pointer means "not supplied" and is
indistinguishable from an explicit JSON null — so turning a schedule *off* was impossible: the UI
sent null, the handler treated it as absent, and the schedule kept running. The update handler
decodes the body twice (struct + `map[string]json.RawMessage`) to tell those apart.

**Retention is enforced on snapshot completion** (`applySnapshotRetention` in
`database_handlers.go`), sharing `routers.DeleteSnapshotArtifact` with the HTTP delete handler so an
expiry and an operator deletion take the same path, including the remote file delete. Two
invariants: a retention failure never fails the snapshot that triggered it, and retention never
reduces an instance below **two** successful snapshots regardless of `retention_count` — "keep 1"
plus a silently-failing backup is how one corrupt artifact becomes the only artifact. Before this,
`retention_count` was accepted, forwarded to the runner, stored in the scheduler job, and read by
nothing.

**Scheduled snapshots are adopted, not pre-created.** The runner's own cron starts them, so no row
exists when the first status arrives. `ensureScheduledSnapshotRow` finds-or-creates by
`(database_instance_id, filename)` — which is why the runner computes the filename *before* anything
can fail, and why a failed pre-flight still produces a visible failed snapshot row.

**Scheduling lives in the control plane** (`database_scheduler.go`), not in the runner. The runner
still *has* a cron package, but is deliberately given no jobs: `PushDbSchedule` sends an **empty
cron** and `DistributeDbSchedules` clears on worker connect, because two schedulers for one instance
means two backups per slot. That inversion is the migration — a runner reconnecting with a job
registered under the old model would otherwise keep firing forever.

**The unique index is the concurrency control.** Every slot becomes a `database_snapshot_runs` row
before anything is dispatched, keyed `UNIQUE(database_instance_id, scheduled_at)` on the **nominal,
un-jittered** fire time. Claiming a run *is* the insert; a duplicate loses harmlessly. That is why no
lock, no leader election and no `SKIP LOCKED` (which would need MariaDB 10.6+) is required. **Jitter
must never enter the key** — a restart that recomputed it would mint a second slot for the same
logical run and take two backups.

**A skipped slot is a row, not a log line.** `skipped` is a first-class status with a `skip_reason`
("the previous scheduled snapshot is still running", "database is stopped", "worker N is not
connected", "slot was 4h old"). Recording skips only in logs is exactly why "my scheduled backups
quietly stopped" is a genre of incident. `GET /database-instances/{id}/runs` exposes them.

Four deliberate behaviours, each with a test:

- **Skip on overrun — never queue, never kill.** Killing a 90%-complete backup to start one that will
  also overrun guarantees you never complete a backup; queueing turns an overrun into an unbounded
  backlog. Paired with `snapshotRunTimeout`, because skip-without-timeout turns one stuck run into an
  indefinite outage that looks like working config.
- **Deterministic jitter** — FNV-1a over instance identity XOR a per-deployment seed (Prometheus's
  scrape-offset design). Random jitter fixes the thundering herd but destroys predictability; this
  fixes it and keeps it. The per-deployment component stops staging and production colliding on a
  shared destination. Bounded by both `schedulerMaxJitter` and half the schedule's period.
- **Catch-up is bounded** (`schedulerCatchUpWindow`): a control plane down overnight takes one backup
  on recovery, not a replay of every owed slot.
- **`import _ "time/tzdata"`** — in a minimal image `LoadLocation` errors, and a naive fallback to UTC
  looks like it worked while being an hour wrong for half the year.

**Backup posture (3-2-1) reports what is true, not what was configured.**
`routers/HandleBackupPosture.router.go` scores three axes — three copies, two media, one off-site —
and every axis is deliberately conservative:

- a destination with no snapshot inside `postureFreshnessWindow` is **not** a copy (three copies
  where two are six weeks old is not three copies);
- **locality is asserted, never inferred.** `backup_destinations.locality` defaults to `unknown` and
  `unknown` is never off-site. Lattice genuinely cannot tell an OpenBucket bucket on the very worker
  being backed up from a bucket in another country — both are "type: s3" with a URL — so guessing
  would manufacture false confidence, which is worse than no indicator because it stops you looking;
- a `same_host` destination raises a warning that object-lock guarantees are cosmetic there:
  immutability enforced by a process whose filesystem you can reach is not immutability.

Expect every database to read `1/3` honestly while OpenBucket (which runs on this same fleet) is the
only destination. That is the indicator working.

**Backup freshness is the alarm nothing else can raise.** `flagStaleBackups` (`database_reconciler.go`,
every 10m) flags an instance whose scheduled backups have stopped producing. Reconciliation compares
observed containers to desired state and the watchdog catches hung operations — but a schedule that
silently stops firing produces **no observation and no stuck operation**, so absence is only
detectable by knowing when a run was due. `cron.go` is a 5-field evaluator mirroring the runner's,
walked *backwards* to find the last two fire times; the bar is **two consecutive missed runs**, since
one miss is plausible across a restart. `staleBackupLookback` is 70 days so a monthly schedule still
gets two fires to judge.

The result is written as a **warning, not a status**: `dbLifecycle.SetWarning` sets `last_error` with
code `backup_stale` while leaving `status` alone, because a database whose backups stopped is usually
running perfectly — and moving it to `error` would both lie about the database and be erased by the
next reconcile that observes a healthy container. `ClearWarning` removes it on the next successful
snapshot. The web renders `backup_stale` regardless of status for the same reason.

**Schema migrations** — `db/migrate.go` + `db/migrations/NNN_*.sql`, embedded, applied in filename
order, recorded in **`schema_migrations` (column `migration`, not `version`)**, run on boot and via
the `migrate` / `migrate status` subcommands.

⚠️ **There have been three mechanisms; know which is which.**

1. **The original numbered migrations** applied `001_initial.sql` … `011_container_logs_dedup.sql`
   on 2026-04-19 and were then **deleted from the repo** ("Remove stale migration files", `2876f49`,
   2026-04-18) while their rows stayed in every database. The current runner **adopts** that table
   and continues from `012` — one lineage, not two.
2. **The ~52 inline `migrate()` calls in `db/db.go`** became the de-facto mechanism afterwards. They
   are **frozen**: no record of what ran, idempotence by swallowing MySQL error numbers, and every
   other failure downgraded to a warning while the server starts anyway — which is how
   `sso_sessions` came to be referenced by code no statement created, surfacing as an unbounded
   fail-open. `TestNewDDLDoesNotGoInTheLegacyBlock` trips if that block grows.
3. **`db/migrate.go`** — where all new DDL goes.

⚠️ **A fresh database cannot currently be built from this repo.** `001_initial.sql` (261 lines,
creating `workers`/`stacks`/`containers` and the rest) was deleted, and `db/db.go` contains **no**
`CREATE TABLE` for those core tables — only `ALTER`s that assume they exist. Production works because
those tables were created in April by migrations that no longer exist in source. Restoring a baseline
migration reconstructed from the live schema is outstanding work; until then, treat "spin up a new
Lattice against an empty database" as unsupported.

**Boot failures are loud, not fatal**, and that is a deliberate departure from forta-api's runner.
Fatal-on-boot is safe there because a rolling recreate aborts the deploy and the old container keeps
serving. lattice-api updates *itself* through the control plane it is, so a failed migration is not
an aborted deploy — it is a crashloop that takes the API, its dashboard and its own rollback path
offline together. v1.3.25 proved it: a pre-existing `schema_migrations` table with a different shape
made `CREATE TABLE IF NOT EXISTS` a silent no-op, the first query hit a missing column, and the
control plane had to be recovered by hand on the host. `EnsureMigrationsTable` now verifies the
table's *shape* after creating it, because `IF NOT EXISTS` is a name check, not a schema check. The
`migrate` subcommand stays fatal — nothing is serving there.

⚠️ **The ~52 inline `migrate()` calls in `db/db.go` are frozen.** They still run on every boot and
still build a fresh database, but no new DDL goes in them: that mechanism keeps no record of what
ran, achieves idempotence by swallowing a fixed list of MySQL error numbers, and downgrades every
other failure to a *warning* while the server starts anyway — which is how `sso_sessions` came to be
referenced by code that no statement ever created, surfacing as an unbounded fail-open on a
revocation check. `TestNewDDLDoesNotGoInTheLegacyBlock` trips if that block grows.

New migrations must be **re-runnable**: the runner retries a failed file in full, and
`TestMigrationsAreGuarded` fails the build on a `CREATE TABLE`/`CREATE INDEX`/`ADD COLUMN`/`DROP
COLUMN` without its `IF [NOT] EXISTS` guard. Filenames are `NNN_lower_snake.sql`, zero-padded to
three digits, because lexical order is apply order.

**Deletion protection and final snapshots** — `deletion_protection` is checked before anything else
in the delete path, **including `?force=true`**: force exists for a dead worker and must never
become a way around a guard whose job is preventing data loss. `?final_snapshot=true` stages the
delete instead of performing it — it marks `pending_final_snapshot`, starts a snapshot, and returns
with the database still present; `finaliseDeleteAfterSnapshot` performs the teardown when that
snapshot completes. A failed final snapshot therefore leaves the database intact, which is the
point. Retention is skipped for that completion — expiring an old copy while destroying the database
is the worst possible timing.

**Volume size** — reported by the worker in `db_sync` and persisted to `volume_size_bytes` by
`reconcileDatabaseInstance`, regardless of status (a stopped database still occupies its volume, and
that is exactly when someone is deciding whether to delete it). The worker measures by walking the
volume on an hourly cache, **never** `docker system df -v`, which holds the daemon's container lock
and would wedge `docker ps` host-wide for minutes.

**Command correlation** — every `db_*` command carries `database_instance_id`, `request_id` (per
attempt) and `idempotency_key` (stable per logical operation), and every reply echoes all three
plus a `phase` (`ack` → `completed`/`failed`). `dbCommandPayload` in
`routers/HandleDatabaseInstances.router.go` builds them. The runner reports *what it did*; the
control plane decides *what the instance is* (`dbActionOutcome` in `database_handlers.go`) — never
write a runner outcome string into `status`.

**Host ports** are allocated from `DB_PORT_RANGE_MIN`–`DB_PORT_RANGE_MAX` (20000-29999, below the
Linux ephemeral range). `query.FindPortConflict` checks both other instances and stack containers'
published ports on the target worker and the create path returns **409 naming the conflict**; the
`idx_db_instance_worker_port` unique index over a `port_claim` virtual column (NULL when inactive,
so soft-deleted rows never collide) is the ledger. The runner additionally binds the port for real
before pulling the image — the ledger cannot see a foreign process, and there is a race between
checking and binding.

**Remove vs delete — one command, two meanings.** Both ride on `db_remove`; what separates them is
the `remove_volume` flag and the instance's status:

| | `POST /database-instances/{id}/remove` | `DELETE /database-instances/{id}` |
|---|---|---|
| Container | destroyed | destroyed |
| Data volume | **preserved** — the database can be started again with its data | **destroyed** (`remove_volume: true`) |
| Row | kept, lands in `stopped` | retired (`active = 0`) once the worker confirms |
| Snapshots | untouched | untouched — the only recovery path once the volume is gone |

- **The delete is deferred, not optimistic.** `HandleDeleteDatabaseInstance` sends the command,
  calls `Lifecycle.BeginDeleting` to put the instance in `deleting`, and returns. The row is retired
  by `FinalizeDeletion` (`database_lifecycle.go`) when the worker's `completed` reply arrives with
  `volume_removed: true`. It previously soft-deleted the row the instant the command was queued,
  which orphaned a container and a full data volume whenever the removal failed — or was never sent,
  because the worker was offline.
- **`FinalizeDeletion` is how a delete is told apart from a `remove`**: same reply, but it only acts
  when the instance is in `deleting`, and returns false otherwise so the reply falls through to the
  normal `stopped` outcome. A `completed` reply *without* `volume_removed` (a runner predating volume
  purging) is failed with `remove_failed` rather than retiring the row — never leak the volume.
- **A delete on a disconnected worker returns 409.** `?force=true` overrides it: the record is
  retired and the event trail records that the container and volume were abandoned on the worker.
- **`db_remove` is idempotent on the runner side** — an absent container is the goal of a remove, not
  a failure. It used to reply `failed: container not found`, so a second Remove click drove a
  perfectly fine instance into `error`. The API additionally tolerates that old reply
  (`isAbsentContainerMessage`) for a plain remove, but *not* mid-delete, where the volume it was
  asked to purge is still there.
- The watchdog fails a stuck `deleting` out to `error` like any other transitional status, naming the
  container and volume that may still exist. It used to only record an event, which left an
  interrupted delete stranded in `deleting` forever once deletion stopped being optimistic.

**Data volumes** — creating an instance whose `lattice-dbdata-<name>` volume already exists fails
with an explanatory error unless `adopt_existing_volume` is passed. Reusing a populated volume makes
the engine skip initialisation and keep its old credentials while the control plane stores newly
generated ones — a mismatch nothing surfaces until a connection is attempted.

**Credentials** — `POST /database-instances/{id}/reveal` is the supported path: audited, recorded
as a `reveal` event, and root-only on explicit request. `GET .../credentials` is deprecated and
returns root from a plain GET; it is kept only for existing clients.

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
| GET | `/stacks/{id}/export` | `HandleExportStack` `[E]` (export includes plaintext env config) |
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
| GET / PUT / DELETE | `/database-instances/{id}` | `HandleGetDatabaseInstance` / `DatabaseHandler.HandleUpdateDatabaseInstance` `[E]` / `HandleDeleteDatabaseInstance` `[E]` (destroys container **and data volume**, async; `409` if the worker is offline unless `?force=true`) |
| POST | `/database-instances/{id}/{start,stop,restart,remove}` | `DatabaseHandler.HandleDatabaseAction` `[E]` (action derived from the last path segment; `remove` is container-only — the volume survives) |
| GET | `/database-instances/{id}/credentials` | `DatabaseHandler.HandleGetDatabaseCredentials` `[A]` (**deprecated** — returns root from a plain GET; use `/reveal`) |
| POST | `/database-instances/{id}/reveal` | `DatabaseHandler.HandleRevealDatabaseCredentials` `[A]` (audited; root only when `include_root` is set) |
| GET | `/database-instances/{id}/connection` | `DatabaseHandler.HandleGetDatabaseConnection` (host/port/database/username — no secrets) |
| GET | `/database-instances/{id}/events` | `HandleListDatabaseInstanceEvents` (lifecycle history) |
| GET | `/database-instances/{id}/logs` | `HandleGetDatabaseInstanceLogs` (container stdout/stderr, resolved by container name) |
| GET | `/database-instances/{id}/lifecycle` | `HandleGetDatabaseInstanceLifecycle` (worker lifecycle messages) |
| GET | `/database-instances/{id}/backup-posture` | `HandleGetDatabaseBackupPosture` (3-2-1 axes, detail and warnings) |
| GET | `/database-instances/{id}/runs` | `HandleGetDatabaseInstanceRuns` (scheduled attempts, including skipped slots and their reason) |
| GET | `/database-instances/{id}/metrics` | `HandleGetDatabaseInstanceMetrics` (CPU/memory samples, addressed by worker+container name — the rows exist with `container_id NULL` and had no reader) |
| POST | `/database-instances/{id}/console` | `DatabaseHandler.HandleOpenDatabaseConsole` `[E]` (authorises an exec session; returns the SQL client argv) |
| GET | `/workers/{id}/port-availability` | `HandleGetWorkerPortAvailability` (claimed host ports + a free suggestion; pass `?port=` to check one) |
| GET / POST | `/database-instances/{id}/snapshots` | `HandleListSnapshots` / `DatabaseHandler.HandleCreateSnapshot` `[E]` |
| POST | `/database-instances/{id}/restore` | `DatabaseHandler.HandleRestoreSnapshot` `[E]` |
| DELETE | `/database-snapshots/{id}` | `DatabaseHandler.HandleDeleteSnapshot` `[E]` (also sends `db_delete_snapshot_file` so the remote file is removed) |
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
| POST | `/update/api` / `/update/web` | `HandleUpdateAPI` `[A]` / `HandleUpdateWeb` `[A]` (self-update via Docker socket; returns "already up to date" when the resolved image matches the running one — `?force=true` overrides) |

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
  `DOCKER_COMPOSE_DIR` to pull and recreate the API/web containers — hence the runtime
  image includes `docker-cli` + `docker-cli-compose` and runs as root.
- **Image tags are parameterised in the host compose file** and set by the self-update endpoint:

  ```yaml
  image: ${REGISTRY_URL:-registry.appleby.cloud}/lattice-api:${LATTICE_API_TAG:-latest}
  ```

  `POST /admin/update/api` (and `/update/web`) accept an optional `version`. When given, the tag is
  written to `LATTICE_API_TAG` / `LATTICE_WEB_TAG` in `DOCKER_COMPOSE_DIR/.env` **before** anything
  is resolved, so the pull, the image comparison and the recreate all agree on the target. The value
  persists, so a restart redeploys the same version rather than drifting. Rolling back is the same
  call with an older tag. Omitting `version` follows whatever the file already resolves to —
  `:latest` when the variable is unset.

  The env file is rewritten atomically (temp file + rename); `upsertEnvVar` replaces an existing
  assignment in place and leaves comments, blanks and prefix-sharing keys alone. Requesting a version
  when the compose file does not interpolate the variable is a 400 rather than a silent no-op —
  writing a variable nothing reads would deploy the old image and report success.

  History worth knowing: `lattice-api` was hard-pinned in the compose file until 2026-07-28. While
  pinned, self-update could not move it at all — the pull fetched the already-local tag and the
  recreate reproduced the same image — and before v1.3.19 the endpoint reported success anyway, so
  it looked like an update that refused to stick.
- **Self-update pre-flight.** `HandleUpdateAPI` compares the running container's image ID against
  the ID the pull resolved, and verifies `DOCKER_HELPER_CONTAINER` is running before claiming
  success — the API cannot recreate itself (Docker kills every process in the container during the
  stop step), so a missing helper means the recreate silently never happens. Both cases now return
  an actionable error or a no-op result rather than an optimistic 200. `?force=true` recreates even
  when the image is unchanged, which is what you want after an env-var change. Every attempt writes
  its outcome to the audit log (`resource_type: api`), which is the durable record — the HTTP
  response necessarily goes out before the container is replaced.
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
go test ./...     # must pass (crypto, jwt, healthscan, middleware, responder, socket, structs, tools)
```

**`go test ./...` needs `DATABASE_DSN` and `JWT_SIGNING_KEY` set to *anything*.** `env/env.go`
resolves both at package-init time via `getEnvOrPanic`, so every package that imports `env` —
`middleware` among them — panics on load without them, and no test in `package main` can run at
all. No test opens a connection; `db.Init()` is only reached from `initApp()`. Dummy values are
fine:

```bash
DATABASE_DSN="ci:ci@tcp(127.0.0.1:3306)" \
JWT_SIGNING_KEY="local-test-key-at-least-32-characters-long" \
  go test ./...
```

This is also why the database protocol-contract tests live in `socket/` rather than next to
`ws_dispatch.go` — see `socket/db_dispatch_test.go`.

`dev check` runs fmt + vet + test together. There is **no** integration/Docker test suite in this
repo, so a green `go test ./...` is sufficient here (unlike the Trailblaze repos). **CI runs all
four of the above in a `test` job that `build-and-push` depends on**, so unformatted code or a
failing test now blocks the image build and the deploy. If you changed
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
