#!/bin/bash
set -euo pipefail

# Lattice Runner installer
# Usage: curl -fsSL https://lattice-api.appleby.cloud/install/runner | WORKER_TOKEN=<token> bash
#   or:  curl -fsSL https://lattice-api.appleby.cloud/install/runner | WORKER_TOKEN=<token> WORKER_NAME=my-worker bash
#
# The script auto-elevates to root via sudo, preserving WORKER_TOKEN,
# WORKER_NAME, and ORCHESTRATOR_URL. No need for `sudo -E`.

# Wrap everything in main() so bash reads the entire script before executing.
# This is critical for `curl | bash` — without it, sudo re-exec would fail
# because stdin (the pipe) would already be partially consumed.
main() {

# ── Auto-elevate to root, preserving env vars ─────────────────────────────
if [ "$(id -u)" -ne 0 ]; then
    exec sudo WORKER_TOKEN="${WORKER_TOKEN:-}" \
              WORKER_NAME="${WORKER_NAME:-}" \
              ORCHESTRATOR_URL="${ORCHESTRATOR_URL:-}" \
              bash -c "$(declare -f main); main"
fi

REPO="aidenappl/lattice-runner"
INSTALL_DIR="/opt/lattice-runner"
BINARY_NAME="lattice-runner"
GO_VERSION="1.25.0"
SERVICE_NAME="lattice-runner"

# Known SHA256 checksums for Go tarball verification
declare -A GO_CHECKSUMS=(
    ["amd64"]="2852af0cb20a13139b3448992e69b868e50ed0f8a1e5940ee1de9e19a123b613"
    ["arm64"]="05de75d6994a2783699815ee553bd5a9327d8b79991de36e38b66862782f54ae"
)

# Secure cleanup on exit
BUILD_DIR=""
cleanup() {
    if [ -n "$BUILD_DIR" ] && [ -d "$BUILD_DIR" ]; then
        rm -rf "$BUILD_DIR"
    fi
}
trap cleanup EXIT

log() { echo "  $*"; }
err() { echo "ERROR: $*" >&2; exit 1; }

echo ""
echo "╔══════════════════════════════════════════╗"
echo "║      Lattice Runner Installer            ║"
echo "╚══════════════════════════════════════════╝"
echo ""

# Detect if this is an upgrade (service already exists and running)
IS_UPGRADE=false
if systemctl is-active --quiet "$SERVICE_NAME" 2>/dev/null; then
    IS_UPGRADE=true
    log "Mode:     upgrade (existing service detected)"
elif [ -f "${INSTALL_DIR}/${BINARY_NAME}" ]; then
    IS_UPGRADE=true
    log "Mode:     upgrade (existing binary detected)"
else
    log "Mode:     fresh install"
fi

# Detect platform
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)

case "$ARCH" in
    x86_64|amd64) ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
    *) err "Unsupported architecture: $ARCH" ;;
esac

log "Platform: ${OS}/${ARCH}"

# Ensure common paths are available (curl|bash doesn't source profile.d)
for p in /usr/local/go/bin /usr/lib/go/bin /snap/bin "${HOME:-/root}/go/bin"; do
    [ -d "$p" ] && export PATH="$p:$PATH"
done

# ── Ensure Docker is installed ─────────────────────────────────────────────

if command -v docker >/dev/null 2>&1; then
    log "Docker:   $(docker --version | awk '{print $3}' | tr -d ',')"
else
    log "Docker:   not found — installing..."
    echo ""
    curl -fsSL https://get.docker.com | sh
    systemctl enable --now docker
    # Add current user to docker group so runner doesn't need sudo
    usermod -aG docker "${SUDO_USER:-root}" 2>/dev/null || true
    echo ""
    log "Docker installed: $(docker --version | awk '{print $3}' | tr -d ',')"
fi

# ── Ensure Go is installed ──────────────────────────────────────────────────

if command -v go >/dev/null 2>&1; then
    log "Go:       $(go version | awk '{print $3}')"
else
    log "Go:       not found — installing go${GO_VERSION}..."
    echo ""

    GO_TARBALL="/tmp/go-${GO_VERSION}-${ARCH}.tar.gz"
    curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${ARCH}.tar.gz" -o "$GO_TARBALL"

    # Verify checksum if available
    EXPECTED_HASH="${GO_CHECKSUMS[$ARCH]:-}"
    if [ -n "$EXPECTED_HASH" ]; then
        ACTUAL_HASH=$(sha256sum "$GO_TARBALL" | awk '{print $1}')
        if [ "$ACTUAL_HASH" != "$EXPECTED_HASH" ]; then
            rm -f "$GO_TARBALL"
            err "Go tarball checksum mismatch!\n  Expected: $EXPECTED_HASH\n  Got:      $ACTUAL_HASH"
        fi
        log "Go tarball checksum verified"
    else
        log "WARNING: No checksum available for go${GO_VERSION}-${ARCH}, skipping verification"
    fi

    rm -rf /usr/local/go
    tar -C /usr/local -xzf "$GO_TARBALL"
    rm -f "$GO_TARBALL"
    export PATH="/usr/local/go/bin:$PATH"
    log "Go installed: $(go version | awk '{print $3}')"
fi

# ── Ensure git is installed ─────────────────────────────────────────────────

if ! command -v git >/dev/null 2>&1; then
    log "Git:      not found — installing..."
    if command -v apt-get >/dev/null 2>&1; then
        apt-get update -qq && apt-get install -y -qq git
    elif command -v yum >/dev/null 2>&1; then
        yum install -y -q git
    elif command -v dnf >/dev/null 2>&1; then
        dnf install -y -q git
    else
        err "Could not install git. Please install it manually."
    fi
fi

echo ""

# Fetch latest release tag from GitHub
LATEST_TAG=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name"' | head -1 | cut -d'"' -f4)
if [ -z "$LATEST_TAG" ]; then
    err "Could not determine latest release tag from GitHub."
fi
log "Version:  ${LATEST_TAG}"

# Clone and build in a secure temp directory
BUILD_DIR=$(mktemp -d)
chmod 700 "$BUILD_DIR"
export GOPATH="${BUILD_DIR}/gopath"
export GOMODCACHE="${BUILD_DIR}/gomodcache"
export GOCACHE="${BUILD_DIR}/gocache"
mkdir -p "$GOPATH" "$GOMODCACHE" "$GOCACHE"

log "Building lattice-runner ${LATEST_TAG}..."
git clone --depth=1 --branch "${LATEST_TAG}" "https://github.com/${REPO}.git" "$BUILD_DIR/lattice-runner" 2>/dev/null
cd "$BUILD_DIR/lattice-runner"
CGO_ENABLED=0 go build -ldflags="-w -s -X main.Version=${LATEST_TAG}" -o "${BINARY_NAME}" .

# Verify binary was created
if [ ! -f "${BINARY_NAME}" ]; then
    err "Build failed — binary not created"
fi
log "Build complete: $(ls -lh ${BINARY_NAME} | awk '{print $5}')"

# Install binary
log "Installing to ${INSTALL_DIR}..."
mkdir -p "$INSTALL_DIR"
# Copy to a temp path then rename atomically — avoids "Text file busy" when upgrading a running binary
cp "${BINARY_NAME}" "${INSTALL_DIR}/${BINARY_NAME}.new"
chmod +x "${INSTALL_DIR}/${BINARY_NAME}.new"
mv -f "${INSTALL_DIR}/${BINARY_NAME}.new" "${INSTALL_DIR}/${BINARY_NAME}"

# Symlink to PATH
ln -sf "${INSTALL_DIR}/${BINARY_NAME}" /usr/local/bin/lattice-runner

# Cleanup build dir (trap will also clean up on failure)
rm -rf "$BUILD_DIR"
BUILD_DIR=""

echo ""
log "Lattice Runner installed to ${INSTALL_DIR}/${BINARY_NAME}"
echo ""

if [ "$IS_UPGRADE" = true ]; then
    # ── Upgrade path: restart the existing service ──────────────────────────

    # Ensure the systemd service file exists (may be missing if the original
    # install was interrupted or the binary was placed manually).
    if [ ! -f "/etc/systemd/system/${SERVICE_NAME}.service" ]; then
        log "Systemd service not found — creating..."

        # Create .env if it doesn't exist and a token was provided
        if [ ! -f "${INSTALL_DIR}/.env" ] && [ -n "${WORKER_TOKEN:-}" ]; then
            ORCHESTRATOR_URL="${ORCHESTRATOR_URL:-wss://lattice-api.appleby.cloud/ws/worker}"
            WORKER_NAME="${WORKER_NAME:-$(hostname)}"

            tee "${INSTALL_DIR}/.env" > /dev/null <<ENVEOF
ORCHESTRATOR_URL=${ORCHESTRATOR_URL}
WORKER_TOKEN=${WORKER_TOKEN}
WORKER_NAME=${WORKER_NAME}
ENVEOF
            chmod 600 "${INSTALL_DIR}/.env"
            log "Created ${INSTALL_DIR}/.env"
        elif [ ! -f "${INSTALL_DIR}/.env" ]; then
            echo "WARNING: ${INSTALL_DIR}/.env not found and no WORKER_TOKEN provided."
            echo "  Run: sudo lattice-runner setup"
        fi

        tee /etc/systemd/system/${SERVICE_NAME}.service > /dev/null <<SVCEOF
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
        systemctl daemon-reload
        systemctl enable "$SERVICE_NAME"
        log "Created and enabled ${SERVICE_NAME}.service"
    fi

    # Delay the restart by 3 seconds so that the process that invoked this
    # script (lattice-runner itself) has time to read exit-code 0 and send
    # the success worker_action_status message before systemd kills it.
    log "Scheduling ${SERVICE_NAME} restart in 3 seconds..."
    (sleep 3 && systemctl restart "$SERVICE_NAME") &
    echo ""
    log "Upgrade complete. Runner will restart shortly."
    echo ""
    echo "  sudo systemctl status ${SERVICE_NAME}"
    echo "  sudo journalctl -u ${SERVICE_NAME} -f"
    echo ""
elif [ -n "${WORKER_TOKEN:-}" ]; then
    # ── Non-interactive fresh install: token was passed as env var ───────────
    log "Configuring with provided token..."

    ORCHESTRATOR_URL="${ORCHESTRATOR_URL:-wss://lattice-api.appleby.cloud/ws/worker}"
    WORKER_NAME="${WORKER_NAME:-$(hostname)}"

    tee "${INSTALL_DIR}/.env" > /dev/null <<ENVEOF
ORCHESTRATOR_URL=${ORCHESTRATOR_URL}
WORKER_TOKEN=${WORKER_TOKEN}
WORKER_NAME=${WORKER_NAME}
ENVEOF
    chmod 600 "${INSTALL_DIR}/.env"

    # Install systemd service
    tee /etc/systemd/system/${SERVICE_NAME}.service > /dev/null <<SVCEOF
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

    systemctl daemon-reload
    systemctl enable "$SERVICE_NAME"
    systemctl start "$SERVICE_NAME"

    log "Lattice Runner installed and started."
    echo ""
    echo "  sudo systemctl status ${SERVICE_NAME}"
    echo "  sudo journalctl -u ${SERVICE_NAME} -f"
    echo ""
else
    # ── Interactive fresh install: run setup wizard ─────────────────────────
    lattice-runner setup
fi

} # end main

main "$@"
