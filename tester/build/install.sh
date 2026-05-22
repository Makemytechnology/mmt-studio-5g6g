#!/bin/bash
# install.sh — SA Tester Installer
#
# Installs the bundled SA Tester and sets up systemd service.
#
# Usage:
#   sudo ./install.sh                    # Install to /opt/satester
#   sudo ./install.sh /path/to/dir       # Install to custom path
#   sudo ./install.sh --uninstall        # Remove installation

set -e

INSTALL_DIR="${1:-/opt/satester}"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

# ── Colors ──
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

info()  { echo -e "${GREEN}[+]${NC} $1"; }
warn()  { echo -e "${YELLOW}[!]${NC} $1"; }
error() { echo -e "${RED}[x]${NC} $1"; exit 1; }

# ── Root check ──
if [ "$(id -u)" -ne 0 ]; then
    error "Run as root: sudo $0 $*"
fi

# ── Uninstall ──
if [ "$1" = "--uninstall" ]; then
    info "Uninstalling SA Tester..."
    rm -f /usr/local/bin/satester
    systemctl stop satester.service 2>/dev/null || true
    systemctl disable satester.service 2>/dev/null || true
    rm -f /etc/systemd/system/satester.service
    systemctl daemon-reload 2>/dev/null || true
    info "Removed systemd service and symlink."
    info "Data directory NOT removed. Delete manually if needed:"
    echo "    rm -rf $INSTALL_DIR"
    exit 0
fi

echo ""
echo "======================================"
echo "   SA Tester 5G — Installer"
echo "======================================"
echo ""

# ── Check architecture ──
ARCH=$(uname -m)
if [ "$ARCH" != "x86_64" ]; then
    error "Unsupported architecture: $ARCH (requires x86_64)"
fi

# ── Install system dependencies ──
info "Checking dependencies..."

# iperf3
if ! command -v iperf3 &>/dev/null; then
    info "Installing iperf3..."
    if command -v apt-get &>/dev/null; then
        apt-get install -y -qq iperf3
    elif command -v dnf &>/dev/null; then
        dnf install -y iperf3
    fi
fi

# ── Backup existing DB ──
if [ -d "$INSTALL_DIR" ] && [ -f "$INSTALL_DIR/data/sa_tester.db" ]; then
    warn "Existing installation found. Backing up database..."
    cp "$INSTALL_DIR/data/sa_tester.db" "/tmp/sa_tester.db.bak.$(date +%s)" 2>/dev/null || true
fi

# ── Install ──
info "Installing SA Tester to $INSTALL_DIR ..."
mkdir -p "$INSTALL_DIR"
cp -a "$SCRIPT_DIR"/* "$INSTALL_DIR/"
chmod +x "$INSTALL_DIR/satester" 2>/dev/null || true
mkdir -p "$INSTALL_DIR/data/logs" "$INSTALL_DIR/data/test_results"

# Restore DB backup
LATEST_BACKUP=$(ls -t /tmp/sa_tester.db.bak.* 2>/dev/null | head -1)
if [ -n "$LATEST_BACKUP" ]; then
    info "Restoring database from backup..."
    cp "$LATEST_BACKUP" "$INSTALL_DIR/data/sa_tester.db"
fi

# ── Create symlink ──
ln -sf "$INSTALL_DIR/satester" /usr/local/bin/satester 2>/dev/null || true
info "Installed to $INSTALL_DIR"

# ── Create systemd service ──
info "Creating systemd service..."
cat > /etc/systemd/system/satester.service << SYSTEMD_EOF
[Unit]
Description=SA Tester — 5G Core Network Tester
After=network.target
Wants=network.target

[Service]
Type=simple
ExecStart=/opt/satester/satester
WorkingDirectory=/opt/satester
Restart=on-failure
RestartSec=5
LimitNOFILE=65535

# Required capabilities for GTP-U tunnels and SCTP
AmbientCapabilities=CAP_NET_ADMIN CAP_NET_RAW CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_ADMIN CAP_NET_RAW CAP_NET_BIND_SERVICE

# Logging
StandardOutput=journal
StandardError=journal
SyslogIdentifier=satester

[Install]
WantedBy=multi-user.target
SYSTEMD_EOF

# Update paths if custom install dir
if [ "$INSTALL_DIR" != "/opt/satester" ]; then
    sed -i "s|/opt/satester/satester|$INSTALL_DIR/satester|g" /etc/systemd/system/satester.service
    sed -i "s|/opt/satester|$INSTALL_DIR|g" /etc/systemd/system/satester.service
fi

systemctl daemon-reload

echo ""
echo "======================================"
echo "   Installation Complete!"
echo "======================================"
echo ""
echo "  Install path:  $INSTALL_DIR"
echo "  Database:      $INSTALL_DIR/data/sa_tester.db"
echo ""
echo "  Run directly:"
echo "    sudo satester"
echo ""
echo "  Or use systemd:"
echo "    sudo systemctl start satester"
echo "    sudo systemctl enable satester    # start on boot"
echo "    sudo journalctl -u satester -f    # view logs"
echo ""
echo "  Web UI:        http://<ip>:5000"
echo "  Core target:   configured in gNB Config (AMF IP)"
echo ""
echo "  Uninstall:"
echo "    sudo $INSTALL_DIR/install.sh --uninstall"
echo ""
