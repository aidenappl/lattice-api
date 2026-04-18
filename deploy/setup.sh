#!/bin/bash
set -e

# Lattice Orchestrator - Production Setup
# Usage:
#   Interactive:  curl -fsSL .../setup.sh -o setup.sh && bash setup.sh
#   Non-interactive: curl -fsSL .../setup.sh | REGISTRY_USERNAME=x REGISTRY_PASSWORD=x ADMIN_PASSWORD=x PUBLIC_API_URL=https://... ALLOWED_ORIGINS=https://... bash

INSTALL_DIR="/opt/lattice"
REGISTRY_URL="registry.appleby.cloud"

echo ""
echo "╔══════════════════════════════════════════╗"
echo "║     Lattice Orchestrator Setup           ║"
echo "╚══════════════════════════════════════════╝"
echo ""

# Check Docker
command -v docker >/dev/null 2>&1 || {
    echo "Docker not found. Installing..."
    curl -fsSL https://get.docker.com | sh
    sudo systemctl enable docker
    sudo systemctl start docker
}
echo "Docker: $(docker --version | awk '{print $3}' | tr -d ',')"

# Check Docker Compose
docker compose version >/dev/null 2>&1 || {
    echo "Docker Compose not found. Please install Docker Compose v2."
    exit 1
}
echo "Compose: $(docker compose version --short)"
echo ""

# Login to registry (as root so sudo docker compose pull works)
echo "Logging into registry..."
if [ -n "$REGISTRY_USERNAME" ] && [ -n "$REGISTRY_PASSWORD" ]; then
    echo "$REGISTRY_PASSWORD" | sudo docker login "$REGISTRY_URL" -u "$REGISTRY_USERNAME" --password-stdin
else
    echo "ERROR: REGISTRY_USERNAME and REGISTRY_PASSWORD must be set."
    echo ""
    echo "Usage:"
    echo "  curl -fsSL .../setup.sh | REGISTRY_USERNAME=x REGISTRY_PASSWORD=x ADMIN_PASSWORD=x PUBLIC_API_URL=https://lattice-api.appleby.cloud ALLOWED_ORIGINS=https://lattice.appleby.cloud bash"
    exit 1
fi
echo ""

# Validate required vars
if [ -z "$ADMIN_PASSWORD" ]; then
    echo "ERROR: ADMIN_PASSWORD must be set."
    exit 1
fi
if [ -z "$PUBLIC_API_URL" ]; then
    echo "ERROR: PUBLIC_API_URL must be set (e.g. https://lattice-api.appleby.cloud)."
    exit 1
fi
if [ -z "$ALLOWED_ORIGINS" ]; then
    echo "ERROR: ALLOWED_ORIGINS must be set (e.g. https://lattice.appleby.cloud)."
    exit 1
fi

# Generate JWT signing key
JWT_SIGNING_KEY=${JWT_SIGNING_KEY:-$(openssl rand -hex 32)}
DB_ROOT_PASSWORD=${DB_ROOT_PASSWORD:-$(openssl rand -hex 16)}

# Create install directory
sudo mkdir -p "$INSTALL_DIR/migrations"

# Write env file
sudo tee "$INSTALL_DIR/.env" > /dev/null <<ENVEOF
REGISTRY_URL=${REGISTRY_URL}
DB_ROOT_PASSWORD=${DB_ROOT_PASSWORD}
JWT_SIGNING_KEY=${JWT_SIGNING_KEY}
ADMIN_EMAIL=${ADMIN_EMAIL:-admin@lattice.local}
ADMIN_PASSWORD=${ADMIN_PASSWORD}
PUBLIC_API_URL=${PUBLIC_API_URL}
ALLOWED_ORIGINS=${ALLOWED_ORIGINS}
API_PORT=${API_PORT:-8000}
WEB_PORT=${WEB_PORT:-3000}
ENVEOF
sudo chmod 600 "$INSTALL_DIR/.env"
echo "Wrote $INSTALL_DIR/.env"

# Download compose file
sudo curl -fsSL -o "$INSTALL_DIR/docker-compose.yml" \
    "https://raw.githubusercontent.com/aidenappl/lattice-api/main/deploy/docker-compose.prod.yml"
echo "Wrote $INSTALL_DIR/docker-compose.yml"

# Download migration
sudo curl -fsSL -o "$INSTALL_DIR/migrations/001_initial.sql" \
    "https://raw.githubusercontent.com/aidenappl/lattice-api/main/migrations/001_initial.sql"
echo "Wrote $INSTALL_DIR/migrations/001_initial.sql"

echo ""

# Pull images
echo "Pulling images..."
cd "$INSTALL_DIR"
sudo docker compose --env-file .env pull

# Start
echo ""
echo "Starting Lattice..."
sudo docker compose --env-file .env up -d

IP=$(hostname -I | awk '{print $1}')

echo ""
echo "╔══════════════════════════════════════════╗"
echo "║     Lattice is running!                  ║"
echo "╚══════════════════════════════════════════╝"
echo ""
echo "  API: http://${IP}:${API_PORT:-8000}"
echo "  Web: http://${IP}:${WEB_PORT:-3000}"
echo ""
echo "  Admin login:"
echo "    Email:    ${ADMIN_EMAIL:-admin@lattice.local}"
echo "    Password: (as provided)"
echo ""
echo "  Config:  $INSTALL_DIR/.env"
echo "  Compose: $INSTALL_DIR/docker-compose.yml"
echo ""
echo "  Commands:"
echo "    cd $INSTALL_DIR && sudo docker compose logs -f"
echo "    cd $INSTALL_DIR && sudo docker compose restart"
echo "    cd $INSTALL_DIR && sudo docker compose pull && sudo docker compose up -d"
echo ""
