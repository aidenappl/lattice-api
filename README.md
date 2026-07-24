# lattice-api

Central orchestrator API for the Lattice container-orchestration platform — workers, stacks, containers, deployments, registries, database instances, and backups across a fleet of Docker hosts.

> **appleby.cloud platform** · Go API · `lattice.appleby.cloud`

---

## Overview

`lattice-api` is the brain of Lattice (an in-house replacement for Portainer). It is a monolithic Go HTTP + WebSocket server that holds all persistent state in a MariaDB `lattice` database and is the only component that can issue commands to workers. Each host runs a `lattice-runner` agent that dials **outbound** to `/ws/worker`; the API pushes deploy/start/stop/rollback and database commands down that socket and ingests heartbeats, metrics, and lifecycle telemetry back. The Next.js dashboard talks to it over REST plus a second admin WebSocket (`/ws/admin`) for live updates.

It owns the data model, command dispatch, deployment bookkeeping (record + watchdog that pings, retries, and force-fails stalled deploys), auth/RBAC, and fleet observability plumbing (health scanning, metrics, anomalies, retention, webhooks, SMTP alerts). It does **not** perform the Docker work itself — pulling images and executing the blue-green/canary/rolling strategy step-by-step lives in `lattice-runner`; the API only sends a `strategy` string and a resolved container spec.

## Role in the Lattice ecosystem

| Repo | Relationship |
|------|--------------|
| [`lattice-web`](https://github.com/aidenappl/lattice-web) | Next.js dashboard — consumes `/admin/*` REST + `/ws/admin`. |
| [`lattice-runner`](https://github.com/aidenappl/lattice-runner) | Agent on each worker VM — dials `/ws/worker` outbound and executes Docker ops. The worker protocol is a shared contract; change both together. |
| [`lattice-mcp`](https://github.com/aidenappl/lattice-mcp) | MCP server exposing `/admin` as Claude tools — keep it in sync when routes change. |

Lattice is self-hosting: Forta (SSO), Keyring, Monitor and the rest all run as stacks on Lattice workers, which is why local auth exists as a fallback when the SSO IDP is itself down.

## Tech stack

- **Go 1.25** with `gorilla/mux` (subrouters for `/auth` and `/admin`)
- **MariaDB** — primary data store (schema `lattice`, migrated **in-code** via idempotent `migrate()` calls in `db.Init()`; no external migration runner)
- **`gorilla/websocket`** — worker hub and admin hub
- **`squirrel`** (`sq`) — SQL query builder, no ORM
- **`golang-jwt/jwt/v5`** — local JWT auth (HS512)
- **Generic OAuth2 / OIDC SSO** — optional, self-contained client under `sso/` (no `go-forta` dependency)
- **`rs/cors`** for CORS, `golang.org/x/crypto` (bcrypt) for passwords, AES-256-GCM for secrets at rest

## Getting started

### Prerequisites

- **Go 1.25+**
- **Docker** — for `dev up` (bundled MariaDB) and, in production, the Docker-socket self-update path
- A **MariaDB** reachable via `DATABASE_DSN` (the base DSN only, no database name — `db.Init()` appends `/lattice`)
- A **`.env`** file (copy `.env.example`); the `dev` server sources it

### Setup

```bash
cp .env.example .env      # then set DATABASE_DSN and a strong JWT_SIGNING_KEY
dev up                    # start MariaDB + API + web via docker compose
# or run the API alone against your own MariaDB:
dev                       # sources .env, go run .
```

Set at least `DATABASE_DSN` and `JWT_SIGNING_KEY` (min 32 chars; production panics on weak/known-default keys). In production `ENCRYPTION_KEY` (64 hex chars) is **required** — the app panics at boot without it rather than storing secrets as plaintext; in development it may be omitted (loud warning, plaintext passthrough). Set `TRUSTED_PROXIES` (comma-separated IPs/CIDRs) if the API sits behind a reverse proxy so rate limiting reads the forwarded client IP instead of the proxy's. SSO is optional — uncomment the `SSO_*` block in `.env.example` or configure it at runtime via `PUT /admin/sso-config`. See `.env.example` for the full list.

## Development

| Command | What it does |
|---------|--------------|
| `dev` | Run the HTTP dev server (`source .env && go run .`) |
| `dev dev-https` | Run over HTTPS with local mkcert certs |
| `dev build` | Build the binary (`go build -o bin/app .`) |
| `dev test` | Run tests (`go test ./...`) |
| `dev fmt` | Format (`gofmt -w -s .`) |
| `dev vet` | Vet (`go vet ./...`) |
| `dev check` | fmt + vet + test |
| `dev tidy` | `go mod tidy` |
| `dev up` / `dev down` | `docker compose up -d` / `down` (MariaDB + API + web) |
| `dev setup-local` | One-time mkcert + `/etc/hosts` for `lattice-api.local.appleby.cloud` |

**Provisioning a worker** — from any host:

```bash
curl -fsSL https://lattice-api.appleby.cloud/install/runner | \
  REGISTRY_USERNAME=x REGISTRY_PASSWORD=x WORKER_TOKEN=<token> WORKER_NAME=<name> bash
```

The script (served from `GET /install/runner`) clones, builds, configures, and installs the runner as a systemd service.

## Project structure

```
main.go              # Entry point — router, all route registrations, middleware, WS endpoints
init.go / server.go  # Dependency wiring (hubs, handlers, background jobs) / HTTP server + graceful shutdown
message_handlers.go  # Worker & admin WebSocket OnConnect/OnDisconnect/OnMessage callbacks
ws_dispatch.go       # Persistence helpers for inbound worker messages
container_cache.go   # 60s name→container cache (kills the per-message N+1 lookup)
env/  db/  logger/    # Env vars; MariaDB pool + Queryable + in-code migrations; structured logging
middleware/          # DualAuth, RejectPending, RequireAdmin/Editor, WorkerTokenAuth, CSRF, rate limit
jwt/  crypto/  sso/    # Local JWTs (HS512); AES-256-GCM secrets; OAuth2/OIDC client + introspection
routers/             # ~80 Handle<Verb><Entity>.router.go handlers (+ deployment monitor, audit helper)
query/               # 27 squirrel query files, all taking db.Queryable first
structs/             # 20 domain types with json tags + pointer nullables
socket/              # WorkerHub / AdminHub, connection handler, protocol constants
registry/            # Docker Registry v2 client (repos, tags, credential test, manifest digest)
healthscan/ watcher/ retention/ webhooks/ versions/ mailer/  # Background subsystems
bootstrap/ tools/    # First-run admin creation; password/token hashing + validators
```

See **[`AGENTS.md`](AGENTS.md)** for the full route table, auth model, worker protocol, and deploy mechanics.

## Deployment

Deployed **on Lattice itself** at `lattice.appleby.cloud`, as the `lattice-api` container in its stack (alongside `lattice-web` and a docker-helper). It mounts the Docker socket because `POST /admin/update/api|web` shells out to `docker compose` in `DOCKER_COMPOSE_DIR` to pull `:latest` and recreate the API/web containers.

CI (`.github/workflows/build-and-deploy.yml`): on push to `main` it resolves a version (a commit-message `[release-patch|minor|major]` tag bumps the latest `vX.Y.Z` git tag, pushes `:latest` + `:vX.Y.Z` + a GitHub release; otherwise `:development` + `:<short-sha>`), injects registry credentials via `keyring-actions`, then builds and pushes to `registry.appleby.cloud/lattice-api`. **CI builds and pushes the image; rollout happens through Lattice's own self-update path.** The API version is stamped at build time via `-ldflags "-X main.Version=<tag>"` and exposed at `GET /version`.

## Contributing & further reading

- **[`AGENTS.md`](AGENTS.md)** — the authoritative deep reference (full route surface, dual-auth model, WebSocket hubs, worker protocol, deploy strategies, schema, operations, guardrails).
- Related repos: [`lattice-web`](https://github.com/aidenappl/lattice-web), [`lattice-runner`](https://github.com/aidenappl/lattice-runner), [`lattice-mcp`](https://github.com/aidenappl/lattice-mcp).
- Verify before "done": `gofmt -l .` (clean), `go build ./...`, `go vet ./...`, `go test ./...` (or `dev check`).
