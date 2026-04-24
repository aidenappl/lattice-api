#!/bin/bash
set -euo pipefail

# Lattice DB Backup Script
# Creates a compressed mysqldump of the lattice database and rotates old backups.
#
# Usage:
#   ./backup.sh                     # Uses defaults
#   BACKUP_DIR=/mnt/backups ./backup.sh
#
# Cron example (daily at 3am):
#   0 3 * * * /opt/lattice/deploy/backup.sh >> /var/log/lattice-backup.log 2>&1

BACKUP_DIR="${BACKUP_DIR:-/opt/lattice/backups}"
RETENTION_DAYS="${RETENTION_DAYS:-30}"
DB_CONTAINER="${DB_CONTAINER:-$(docker compose -f /opt/lattice/docker-compose.prod.yml ps -q mariadb 2>/dev/null || echo "")}"
COMPOSE_DIR="${COMPOSE_DIR:-/opt/lattice}"

log() { echo "[$(date '+%Y-%m-%d %H:%M:%S')] $*"; }

mkdir -p "$BACKUP_DIR"

TIMESTAMP=$(date '+%Y%m%d_%H%M%S')
BACKUP_FILE="${BACKUP_DIR}/lattice_${TIMESTAMP}.sql.gz"

# Determine how to access MariaDB
if [ -n "$DB_CONTAINER" ]; then
    log "Backing up via Docker container..."
    docker exec "$DB_CONTAINER" \
        mysqldump -u root -p"${DB_ROOT_PASSWORD:-lattice}" \
        --single-transaction --routines --triggers --events \
        lattice | gzip > "$BACKUP_FILE"
elif command -v mysqldump >/dev/null 2>&1; then
    log "Backing up via local mysqldump..."
    mysqldump -u root -p"${DB_ROOT_PASSWORD:-lattice}" \
        -h "${DB_HOST:-127.0.0.1}" -P "${DB_PORT:-3306}" \
        --single-transaction --routines --triggers --events \
        lattice | gzip > "$BACKUP_FILE"
else
    # Try finding the container by compose project
    DB_CONTAINER=$(docker compose -f "${COMPOSE_DIR}/docker-compose.prod.yml" ps -q mariadb 2>/dev/null || true)
    if [ -z "$DB_CONTAINER" ]; then
        log "ERROR: Cannot find MariaDB container or mysqldump binary"
        exit 1
    fi
    docker exec "$DB_CONTAINER" \
        mysqldump -u root -p"${DB_ROOT_PASSWORD:-lattice}" \
        --single-transaction --routines --triggers --events \
        lattice | gzip > "$BACKUP_FILE"
fi

BACKUP_SIZE=$(du -h "$BACKUP_FILE" | cut -f1)
log "Backup created: ${BACKUP_FILE} (${BACKUP_SIZE})"

# Rotate old backups
DELETED=$(find "$BACKUP_DIR" -name "lattice_*.sql.gz" -mtime +${RETENTION_DAYS} -delete -print | wc -l)
if [ "$DELETED" -gt 0 ]; then
    log "Rotated ${DELETED} backups older than ${RETENTION_DAYS} days"
fi

log "Done"
