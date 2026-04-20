#!/bin/bash
set -e

# Lattice Runner installer
# Usage: curl -fsSL https://lattice-api.appleby.cloud/install/runner | bash

REPO="aidenappl/lattice-runner"
INSTALL_DIR="/opt/lattice-runner"
BINARY_NAME="lattice-runner"
GO_VERSION="1.24.10"
SERVICE_NAME="lattice-runner"

echo ""
echo "╔══════════════════════════════════════════╗"
echo "║      Lattice Runner Installer            ║"
echo "╚══════════════════════════════════════════╝"
echo ""

# Detect if this is an upgrade (service already exists and running)
IS_UPGRADE=false
if systemctl is-active --quiet "$SERVICE_NAME" 2>/dev/null; then
    IS_UPGRADE=true
    echo "  Mode:     upgrade (existing service detected)"
elif [ -f "${INSTALL_DIR}/${BINARY_NAME}" ]; then
    IS_UPGRADE=true
    echo "  Mode:     upgrade (existing binary detected)"
else
    echo "  Mode:     fresh install"
fi

# Detect platform
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)

case "$ARCH" in
    x86_64|amd64) ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
    *) echo "Unsupported architecture: $ARCH"; exit 1 ;;
esac

echo "  Platform: ${OS}/${ARCH}"

# Ensure common paths are available (curl|bash doesn't source profile.d)
for p in /usr/local/go/bin /usr/lib/go/bin /snap/bin "$HOME/go/bin"; do
    [ -d "$p" ] && export PATH="$p:$PATH"
done

# ── Ensure Docker is installed ─────────────────────────────────────────────

if command -v docker >/dev/null 2>&1; then
    echo "  Docker:   $(docker --version | awk '{print $3}' | tr -d ',')"
else
    echo "  Docker:   not found — installing..."
    echo ""
    curl -fsSL https://get.docker.com | sh
    sudo systemctl enable --now docker
    # Add current user to docker group so runner doesn't need sudo
    sudo usermod -aG docker "${USER:-root}" 2>/dev/null || true
    echo ""
    echo "  Docker installed: $(docker --version | awk '{print $3}' | tr -d ',')"
fi

# ── Ensure Go is installed ──────────────────────────────────────────────────

if command -v go >/dev/null 2>&1; then
    echo "  Go:       $(go version | awk '{print $3}')"
else
    echo "  Go:       not found — installing go${GO_VERSION}..."
    echo ""
    curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${ARCH}.tar.gz" -o /tmp/go.tar.gz
    sudo rm -rf /usr/local/go
    sudo tar -C /usr/local -xzf /tmp/go.tar.gz
    rm -f /tmp/go.tar.gz
    export PATH="/usr/local/go/bin:$PATH"
    echo "  Go installed: $(go version | awk '{print $3}')"
fi

# ── Ensure git is installed ─────────────────────────────────────────────────

if ! command -v git >/dev/null 2>&1; then
    echo "  Git:      not found — installing..."
    if command -v apt-get >/dev/null 2>&1; then
        sudo apt-get update -qq && sudo apt-get install -y -qq git
    elif command -v yum >/dev/null 2>&1; then
        sudo yum install -y -q git
    elif command -v dnf >/dev/null 2>&1; then
        sudo dnf install -y -q git
    else
        echo "ERROR: Could not install git. Please install it manually."
        exit 1
    fi
fi

echo ""

# Fetch latest release tag from GitHub
LATEST_TAG=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name"' | head -1 | cut -d'"' -f4)
if [ -z "$LATEST_TAG" ]; then
    echo "ERROR: Could not determine latest release tag from GitHub."
    exit 1
fi
echo "  Version:  ${LATEST_TAG}"

# Clone and build
TMPDIR=$(mktemp -d)
echo "Building lattice-runner..."
git clone --depth=1 --branch "${LATEST_TAG}" "https://github.com/${REPO}.git" "$TMPDIR/lattice-runner" 2>/dev/null
cd "$TMPDIR/lattice-runner"
CGO_ENABLED=0 go build -ldflags="-w -s -X main.Version=${LATEST_TAG}" -o "${BINARY_NAME}" .

# Install binary
echo "Installing to ${INSTALL_DIR}..."
sudo mkdir -p "$INSTALL_DIR"
sudo cp "${BINARY_NAME}" "${INSTALL_DIR}/${BINARY_NAME}"
sudo chmod +x "${INSTALL_DIR}/${BINARY_NAME}"

# Symlink to PATH
sudo ln -sf "${INSTALL_DIR}/${BINARY_NAME}" /usr/local/bin/lattice-runner

# Cleanup
rm -rf "$TMPDIR"

echo ""
echo "Lattice Runner installed to ${INSTALL_DIR}/${BINARY_NAME}"
echo ""

if [ "$IS_UPGRADE" = true ]; then
    # ── Upgrade path: restart the existing service ──────────────────────────
    echo "Restarting ${SERVICE_NAME} service..."
    sudo systemctl restart "$SERVICE_NAME"
    echo ""
    echo "Upgrade complete. Runner restarted with new binary."
    echo ""
    echo "  sudo systemctl status ${SERVICE_NAME}"
    echo "  sudo journalctl -u ${SERVICE_NAME} -f"
    echo ""
elif [ -n "$WORKER_TOKEN" ]; then
    # ── Non-interactive fresh install: token was passed as env var ───────────
    echo "Configuring with provided token..."

    ORCHESTRATOR_URL="${ORCHESTRATOR_URL:-wss://lattice-api.appleby.cloud/ws/worker}"
    WORKER_NAME="${WORKER_NAME:-$(hostname)}"

    sudo tee "${INSTALL_DIR}/.env" > /dev/null <<ENVEOF
ORCHESTRATOR_URL=${ORCHESTRATOR_URL}
WORKER_TOKEN=${WORKER_TOKEN}
WORKER_NAME=${WORKER_NAME}
ENVEOF
    sudo chmod 600 "${INSTALL_DIR}/.env"

    # Install systemd service
    sudo tee /etc/systemd/system/${SERVICE_NAME}.service > /dev/null <<SVCEOF
[Unit]
Description=Lattice Runner
After=network.target docker.service
Requires=docker.service

[Service]
Type=simple
WorkingDirectory=/opt/lattice-runner
EnvironmentFile=/opt/lattice-runner/.env
ExecStart=/opt/lattice-runner/lattice-runner
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
SVCEOF

    sudo systemctl daemon-reload
    sudo systemctl enable "$SERVICE_NAME"
    sudo systemctl start "$SERVICE_NAME"

    echo "Lattice Runner installed and started."
    echo ""
    echo "  sudo systemctl status ${SERVICE_NAME}"
    echo "  sudo journalctl -u ${SERVICE_NAME} -f"
    echo ""
else
    # ── Interactive fresh install: run setup wizard ─────────────────────────
    sudo lattice-runner setup
fi
