#!/bin/bash
set -e

# Angarium Installer
# Usage: sudo ./install.sh [controller|agent]

MODE=$1
if [[ -z "$MODE" ]]; then
    echo "Usage: $0 [controller|agent]"
    exit 1
fi

if [[ "$MODE" != "controller" && "$MODE" != "agent" ]]; then
    echo "Invalid mode: $MODE. Must be 'controller' or 'agent'."
    exit 1
fi

echo "Installing Angarium $MODE..."

# 1. Build
make build

# 2. Copy binaries
sudo cp bin/angarium /usr/local/bin/angarium
sudo cp bin/angarium-controller /usr/local/bin/angarium-controller
sudo cp bin/angarium-agent /usr/local/bin/angarium-agent

# 3. Create directories
sudo mkdir -p /etc/angarium
sudo mkdir -p /var/lib/angarium
sudo useradd -r -s /bin/false angarium 2>/dev/null || true
sudo chown -R angarium:angarium /var/lib/angarium 2>/dev/null || true

# 4. Copy config if doesn't exist
if [[ ! -f "/etc/angarium/$MODE.yaml" ]]; then
    sudo cp "deploy/samples/$MODE.yaml" "/etc/angarium/$MODE.yaml"
    sudo chown angarium:angarium "/etc/angarium/$MODE.yaml"
    echo "Created /etc/angarium/$MODE.yaml from sample. Please edit it. Defaults are for local testing."
fi

# 5. Register systemd
sudo cp "deploy/systemd/angarium-$MODE.service" "/etc/systemd/system/"
sudo systemctl daemon-reload
sudo systemctl enable "angarium-$MODE"

echo ""
echo "--------------------------------------------------------"
echo "Installation complete for: $MODE"
echo "Configuration: /etc/angarium/$MODE.yaml"
echo "Binary:        /usr/local/bin/angarium-$MODE"
echo "--------------------------------------------------------"
echo "Run 'sudo systemctl start angarium-$MODE' to start."
