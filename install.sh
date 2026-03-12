#!/bin/bash
set -e

# Help and usage
if [[ "$1" == "--help" || "$1" == "-h" || "$1" == "help" ]]; then
    echo "Angarium Installer"
    echo ""
    echo "Usage: ./install.sh [controller|agent]"
    echo ""
    echo "Commands:"
    echo "  controller    Install the Angarium Controller (API & Scheduler)"
    echo "  agent         Install the Angarium Agent (GPU Node Worker)"
    echo ""
    echo "Notes:"
    echo "  - It automatically downloads the latest release from GitHub."
    echo "  - Developers: Run 'make build' first to use local binaries."
    echo ""
    exit 0
fi

MODE="${1:-agent}"

if [[ "$MODE" != "controller" && "$MODE" != "agent" ]]; then
    echo "Invalid mode: $MODE. Must be 'controller' or 'agent'."
    exit 1
fi

echo "Installing Angarium $MODE..."

# Detect OS/Arch
OS="$(uname -s)"
ARCH="$(uname -m)"

# Check if running as root or can sudo
if [ "$EUID" -ne 0 ]; then
    if command -v sudo &> /dev/null; then
        SUDO="sudo"
    else
        echo "Error: This script requires root privileges (or sudo) to install services."
        exit 1
    fi
else
    SUDO=""
fi

# Stop existing services if systemctl exists
if command -v systemctl &> /dev/null; then
    $SUDO systemctl stop angarium-controller angarium-agent 2>/dev/null || true
fi

# BINARY CHECK/DOWNLOAD
HAS_LOCAL_BIN="false"
if [[ -f "bin/angarium" && -f "bin/angarium-controller" && -f "bin/angarium-agent" ]]; then
    HAS_LOCAL_BIN="true"
fi

if [[ "$HAS_LOCAL_BIN" == "true" ]]; then
    echo "Found local binaries in bin"
else
    echo "No local binaries found. Downloading release..."

    REPO="angariumd/angarium"
    LATEST_TAG=$(curl -sL "https://api.github.com/repos/$REPO/releases/latest" | grep '"tag_name":' | sed -E 's/.*"([^"]+)".*/\1/')

    if [ -z "$LATEST_TAG" ] || [[ "$LATEST_TAG" == "null" ]]; then
        echo "--------------------------------------------------------"
        echo "ERROR: No GitHub release and no local binaries in bin"
        echo ""
        echo "You must build from source first:"
        echo "  make build"
        echo "--------------------------------------------------------"
        exit 1
    fi

    echo "Found release: $LATEST_TAG"
    OS_LOWER=$(echo "$OS" | tr '[:upper:]' '[:lower:]')
    ARCH_LOWER=$(echo "$ARCH" | sed -e 's/x86_64/amd64/' -e 's/aarch64/arm64/')
    TAR_NAME="angarium_${LATEST_TAG#v}_${OS_LOWER}_${ARCH_LOWER}.tar.gz"
    DOWNLOAD_URL="https://github.com/$REPO/releases/download/$LATEST_TAG/$TAR_NAME"
    
    echo "Downloading $DOWNLOAD_URL..."
    curl -L -f -o "/tmp/$TAR_NAME" "$DOWNLOAD_URL" || exit 1
    mkdir -p bin
    tar -xzf "/tmp/$TAR_NAME" -C bin/
    rm -f "/tmp/$TAR_NAME"
fi

# INSTALLATION
echo "Installing Angarium $MODE..."

# Helper to run commands as root
run_as_root() {
    if [ "$EUID" -ne 0 ]; then
        sudo "$@"
    else
        "$@"
    fi
}

run_as_root cp bin/angarium /usr/local/bin/angarium
run_as_root cp bin/angarium-controller /usr/local/bin/angarium-controller
run_as_root cp bin/angarium-agent /usr/local/bin/angarium-agent

run_as_root mkdir -p /etc/angarium /var/lib/angarium /var/log/angarium

# Create angarium user if not exists
if ! id -u angarium >/dev/null 2>&1; then
    run_as_root useradd -r -s /bin/false angarium || true
fi

# Set permissions
run_as_root chown -R angarium:angarium /var/lib/angarium /var/log/angarium 2>/dev/null || true

# Create config file if not exists
if [[ ! -f "/etc/angarium/$MODE.yaml" ]]; then
    SAMPLE_CONFIG="deploy/samples/$MODE.yaml"
    if [ ! -f "$SAMPLE_CONFIG" ] && [ -f "bin/$SAMPLE_CONFIG" ]; then
        SAMPLE_CONFIG="bin/$SAMPLE_CONFIG"
    fi

    if [ -f "$SAMPLE_CONFIG" ]; then
         run_as_root cp "$SAMPLE_CONFIG" "/etc/angarium/$MODE.yaml"
    else
         echo "Generating minimal fallback config for $MODE..."
         if [[ "$MODE" == "controller" ]]; then
             echo -e "addr: \":8080\"\ndb_path: \"/var/lib/angarium/angarium.db\"\nshared_token: \"change-me\"" | run_as_root tee "/etc/angarium/controller.yaml" > /dev/null
         else
             echo -e "controller_url: \"http://localhost:8080\"\nshared_token: \"change-me\"" | run_as_root tee "/etc/angarium/agent.yaml" > /dev/null
         fi
    fi
    run_as_root chown angarium:angarium "/etc/angarium/$MODE.yaml"
fi

# Create systemd service if not exists
SERVICE_FILE="deploy/systemd/angarium-$MODE.service"
if [ ! -f "$SERVICE_FILE" ] && [ -f "bin/$SERVICE_FILE" ]; then
    SERVICE_FILE="bin/$SERVICE_FILE"
fi

if [ -f "$SERVICE_FILE" ]; then
    run_as_root cp "$SERVICE_FILE" "/etc/systemd/system/"
    run_as_root systemctl daemon-reload
    run_as_root systemctl enable "angarium-$MODE"
else
    echo "Warning: Systemd service file not found ($SERVICE_FILE). Skipping service installation."
fi

# Print summary
echo ""
echo "--------------------------------------------------------"
echo "Installation complete for: $MODE"
echo "Binary:        /usr/local/bin/angarium-$MODE"
echo "Config:        /etc/angarium/$MODE.yaml"
echo "Service:       angarium-$MODE"
echo "--------------------------------------------------------"
if command -v systemctl &> /dev/null && [ -f "/etc/systemd/system/angarium-$MODE.service" ]; then
    echo "Run 'sudo systemctl start angarium-$MODE' to start."
else
    echo "To start manually: sudo /usr/local/bin/angarium-$MODE"
fi
