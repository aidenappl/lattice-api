#!/bin/bash
set -e

# Lattice Orchestrator — Update Script
# Pulls latest images, runs migrations, and restarts services.
#
# Run from the orchestrator VM:
#   curl -fsSL "https://raw.githubusercontent.com/aidenappl/lattice-api/main/deploy/update.sh?$(date +%s)" | bash
#
# Or locally:
#   cd /opt/lattice && bash update.sh

INSTALL_DIR="/opt/lattice"
CACHE_BUST="$(date +%s)"
MIGRATIONS_URL="https://raw.githubusercontent.com/aidenappl/lattice-api/main/migrations"

# Migrations that existed before schema_migrations tracking was introduced.
# On a fresh tracking table these will be marked as pre-applied so they are
# never re-executed against a database that already has those changes.
LEGACY_MIGRATIONS="001_initial.sql 002_expand_metrics.sql 003_container_logs.sql 004_stack_env_vars.sql 005_stack_compose_yaml.sql 006_deployment_logs.sql 007_expand_stack_status.sql 008_container_logs_name.sql 009_container_healthcheck.sql"

echo ""
echo "╔══════════════════════════════════════════╗"
echo "║     Lattice Orchestrator — Update        ║"
echo "╚══════════════════════════════════════════╝"
echo ""

cd "$INSTALL_DIR" 2>/dev/null || {
    echo "ERROR: $INSTALL_DIR not found. Run setup.sh first."
    exit 1
}

# Load env
if [ -f "$INSTALL_DIR/.env" ]; then
    set -a
    . "$INSTALL_DIR/.env"
    set +a
fi

# Capture current versions before updating
API_PORT_LOCAL="${API_PORT:-8000}"
WEB_PORT_LOCAL="${WEB_PORT:-3000}"
OLD_API_VERSION=$(curl -sf "http://localhost:${API_PORT_LOCAL}/version" 2>/dev/null | sed 's/.*"version":"\([^"]*\)".*/\1/' || echo "unknown")
OLD_WEB_VERSION=$(curl -sf "http://localhost:${WEB_PORT_LOCAL}/api/version" 2>/dev/null | sed 's/.*"version":"\([^"]*\)".*/\1/' || echo "unknown")
echo "Current versions:  API=$OLD_API_VERSION  Web=$OLD_WEB_VERSION"
echo ""

# Pull latest app images (skip mariadb — only re-pull if image tag changes in compose)
echo "Pulling latest images..."
sudo docker compose --env-file .env pull lattice-api lattice-web
echo ""

# Download latest migrations
echo "Downloading migrations..."
sudo mkdir -p "$INSTALL_DIR/migrations"

# Get list of migration files from GitHub API
MIGRATION_FILES=$(curl -fsSL "https://api.github.com/repos/aidenappl/lattice-api/contents/migrations?${CACHE_BUST}" \
    2>/dev/null | grep '"name"' | sed 's/.*"name": "\(.*\)".*/\1/' | sort)

for file in $MIGRATION_FILES; do
    sudo curl -fsSL -o "$INSTALL_DIR/migrations/$file" "$MIGRATIONS_URL/$file?${CACHE_BUST}" 2>/dev/null && \
        echo "  downloaded $file" || echo "  WARNING: failed to download $file"
done
echo ""

# Run migrations against the running MariaDB container
echo "Running migrations..."
DB_CONTAINER=$(sudo docker compose ps -q mariadb 2>/dev/null)

if [ -z "$DB_CONTAINER" ]; then
    echo "MariaDB not running — starting it..."
    sudo docker compose --env-file .env up -d mariadb
    echo "Waiting for MariaDB to be ready..."
    RETRIES=30
    until sudo docker compose exec mariadb mariadb -u root -p"$DB_PASSWORD" -e "SELECT 1" >/dev/null 2>&1; do
        RETRIES=$((RETRIES - 1))
        if [ "$RETRIES" -le 0 ]; then
            echo "ERROR: MariaDB did not become ready in time."
            exit 1
        fi
        sleep 2
    done
    DB_CONTAINER=$(sudo docker compose ps -q mariadb)
fi

DB_PASSWORD="${DB_ROOT_PASSWORD:-lattice}"

# Ensure a migration-tracking table exists so we only apply each file once.
sudo docker exec -i "$DB_CONTAINER" mariadb -u root -p"$DB_PASSWORD" lattice <<'SQL' 2>/dev/null
CREATE TABLE IF NOT EXISTS schema_migrations (
    migration VARCHAR(255) NOT NULL PRIMARY KEY,
    applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
SQL

# Backfill legacy migrations that pre-date the tracking table.
# If schema_migrations is empty (first run with this script) and the DB
# already has the legacy schema applied, mark them as done so they are skipped.
TRACKING_ROWS=$(sudo docker exec -i "$DB_CONTAINER" \
    mariadb -u root -p"$DB_PASSWORD" lattice -sNe \
    "SELECT COUNT(*) FROM schema_migrations;" 2>/dev/null || echo "0")

if [ "$TRACKING_ROWS" = "0" ]; then
    # Check whether the DB already has the base schema (users table exists).
    has_schema=$(sudo docker exec -i "$DB_CONTAINER" \
        mariadb -u root -p"$DB_PASSWORD" lattice -sNe \
        "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='lattice' AND table_name='users';" 2>/dev/null || echo "0")

    if [ "$has_schema" != "0" ]; then
        echo "  Backfilling legacy migration records (existing DB detected)..."
        for legacy in $LEGACY_MIGRATIONS; do
            sudo docker exec -i "$DB_CONTAINER" mariadb -u root -p"$DB_PASSWORD" lattice -e \
                "INSERT IGNORE INTO schema_migrations (migration) VALUES ('$legacy');" 2>/dev/null
        done
    fi
fi

APPLIED=0
SKIPPED=0

for file in $(ls "$INSTALL_DIR/migrations"/*.sql 2>/dev/null | sort); do
    filename=$(basename "$file")

    # Check whether this migration has already been applied.
    already_applied=$(sudo docker exec -i "$DB_CONTAINER" \
        mariadb -u root -p"$DB_PASSWORD" lattice -sNe \
        "SELECT COUNT(*) FROM schema_migrations WHERE migration='$filename';" 2>/dev/null)

    if [ "$already_applied" = "1" ]; then
        echo "  Skipping $filename (already applied)"
        SKIPPED=$((SKIPPED + 1))
        continue
    fi

    echo "  Applying $filename..."
    if sudo docker exec -i "$DB_CONTAINER" mariadb -u root -p"$DB_PASSWORD" lattice < "$file" 2>&1 | \
            grep -v "^$" | grep -vi "^warning"; then
        # Record the successful migration.
        sudo docker exec -i "$DB_CONTAINER" mariadb -u root -p"$DB_PASSWORD" lattice -e \
            "INSERT IGNORE INTO schema_migrations (migration) VALUES ('$filename');" 2>/dev/null
        APPLIED=$((APPLIED + 1))
    else
        echo "  ERROR: $filename failed — stopping migration run."
        exit 1
    fi
done

echo "  Migrations complete: $APPLIED applied, $SKIPPED skipped."
echo ""

# Restart app services with new images (mariadb only restarts if its config changed)
echo "Restarting services..."
sudo docker compose --env-file .env up -d --force-recreate lattice-api lattice-web
echo ""

# Show status
echo "Status:"
sudo docker compose ps
echo ""

# Show version diff
echo "Waiting for services to start..."
sleep 3
NEW_API_VERSION=$(curl -sf "http://localhost:${API_PORT_LOCAL}/version" 2>/dev/null | sed 's/.*"version":"\([^"]*\)".*/\1/' || echo "unknown")
NEW_WEB_VERSION=$(curl -sf "http://localhost:${WEB_PORT_LOCAL}/api/version" 2>/dev/null | sed 's/.*"version":"\([^"]*\)".*/\1/' || echo "unknown")
echo "Version changes:"
if [ "$OLD_API_VERSION" = "$NEW_API_VERSION" ]; then
    echo "  API: $OLD_API_VERSION (unchanged)"
else
    echo "  API: $OLD_API_VERSION → $NEW_API_VERSION"
fi
if [ "$OLD_WEB_VERSION" = "$NEW_WEB_VERSION" ]; then
    echo "  Web: $OLD_WEB_VERSION (unchanged)"
else
    echo "  Web: $OLD_WEB_VERSION → $NEW_WEB_VERSION"
fi
echo ""
echo "Update complete."
echo ""
