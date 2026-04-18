#!/bin/bash
set -e

# Lattice Orchestrator — Update Script
# Pulls latest images, runs migrations, and restarts services.
#
# Run from the orchestrator VM:
#   curl -fsSL https://raw.githubusercontent.com/aidenappl/lattice-api/main/deploy/update.sh | bash
#
# Or locally:
#   cd /opt/lattice && bash update.sh

INSTALL_DIR="/opt/lattice"
MIGRATIONS_URL="https://raw.githubusercontent.com/aidenappl/lattice-api/main/migrations"

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

# Pull latest images
echo "Pulling latest images..."
sudo docker compose --env-file .env pull
echo ""

# Download latest migrations
echo "Downloading migrations..."
sudo mkdir -p "$INSTALL_DIR/migrations"

# Get list of migration files from GitHub API
MIGRATION_FILES=$(curl -fsSL "https://api.github.com/repos/aidenappl/lattice-api/contents/migrations" | \
    grep '"name"' | sed 's/.*"name": "\(.*\)".*/\1/' | sort)

for file in $MIGRATION_FILES; do
    sudo curl -fsSL -o "$INSTALL_DIR/migrations/$file" "$MIGRATIONS_URL/$file"
    echo "  $file"
done
echo ""

# Run migrations against the running MariaDB container
echo "Running migrations..."
DB_CONTAINER=$(sudo docker compose ps -q mariadb 2>/dev/null)

if [ -z "$DB_CONTAINER" ]; then
    echo "ERROR: MariaDB container not running. Start it first: sudo docker compose up -d mariadb"
    exit 1
fi

DB_PASSWORD="${DB_ROOT_PASSWORD:-lattice}"

for file in $(ls "$INSTALL_DIR/migrations"/*.sql | sort); do
    filename=$(basename "$file")
    echo "  Applying $filename..."
    sudo docker exec -i "$DB_CONTAINER" mariadb -u root -p"$DB_PASSWORD" lattice < "$file" 2>&1 | \
        grep -v "^$" | grep -vi "^warning" || true
done
echo ""

# Restart services with new images
echo "Restarting services..."
sudo docker compose --env-file .env up -d
echo ""

# Show status
echo "Status:"
sudo docker compose ps
echo ""
echo "Update complete."
echo ""
