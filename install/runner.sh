#!/bin/bash
set -e

# Lattice Runner installer
# Usage: curl -fsSL https://lattice-api.appleby.cloud/install/runner | bash

REPO="aidenappl/lattice-runner"
INSTALL_DIR="/opt/lattice-runner"
BINARY_NAME="lattice-runner"

echo ""
echo "╔══════════════════════════════════════════╗"
echo "║      Lattice Runner Installer            ║"
echo "╚══════════════════════════════════════════╝"
echo ""

# Detect platform
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)

case "$ARCH" in
    x86_64|amd64) ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
    *) echo "Unsupported architecture: $ARCH"; exit 1 ;;
esac

echo "  Platform: ${OS}/${ARCH}"

# Check for required tools
command -v docker >/dev/null 2>&1 || {
    echo ""
    echo "Docker is required but not installed."
    echo "Install it with: curl -fsSL https://get.docker.com | sh"
    exit 1
}

echo "  Docker:   $(docker --version | awk '{print $3}' | tr -d ',')"

# Check for Go (needed to build from source)
if command -v go >/dev/null 2>&1; then
    GO_VERSION=$(go version | awk '{print $3}')
    echo "  Go:       $GO_VERSION"
    BUILD_METHOD="go"
else
    echo "  Go:       not found"
    echo ""
    echo "Go is required to build the runner."
    echo "Install it from: https://go.dev/dl/"
    echo ""
    echo "Quick install:"
    echo "  wget https://go.dev/dl/go1.24.10.linux-${ARCH}.tar.gz"
    echo "  sudo tar -C /usr/local -xzf go1.24.10.linux-${ARCH}.tar.gz"
    echo "  export PATH=\$PATH:/usr/local/go/bin"
    exit 1
fi

echo ""

# Clone and build
TMPDIR=$(mktemp -d)
echo "Building lattice-runner..."
git clone --depth=1 "https://github.com/${REPO}.git" "$TMPDIR/lattice-runner" 2>/dev/null
cd "$TMPDIR/lattice-runner"
GIT_HASH=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")
CGO_ENABLED=0 go build -ldflags="-w -s -X main.Version=v0.1.2-${GIT_HASH}" -o "${BINARY_NAME}" .

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

# Run setup
if [ -n "$WORKER_TOKEN" ]; then
    # Non-interactive: token was passed as env var
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
    sudo tee /etc/systemd/system/lattice-runner.service > /dev/null <<SVCEOF
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
    sudo systemctl enable lattice-runner
    sudo systemctl start lattice-runner

    echo "Lattice Runner installed and started."
    echo ""
    echo "  sudo systemctl status lattice-runner"
    echo "  sudo journalctl -u lattice-runner -f"
    echo ""
else
    # Interactive: run setup wizard
    sudo lattice-runner setup
fi
