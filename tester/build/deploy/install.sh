#!/bin/bash
# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
# SA Tester 5G — Fresh Installation Script
set -e
INSTALL_DIR="${1:-/opt/satester}"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SATESTER_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
echo "====================================="
echo " SA Tester 5G — Installation"
echo " MakeMyTechnology"
echo "====================================="
if [ "$EUID" -ne 0 ]; then echo "ERROR: Run as root"; exit 1; fi

echo "[1/5] System dependencies..."
# Python, iperf3, and libsctp are all bundled — only need iproute2 for ip/sysctl
apt-get update -qq
apt-get install -y -qq iproute2 2>/dev/null || true
modprobe sctp 2>/dev/null || true

echo "[2/5] Copying to $INSTALL_DIR..."
mkdir -p "$INSTALL_DIR"
rsync -a --exclude='__pycache__' --exclude='.venv' --exclude='.git' --exclude='dist' --exclude='.claude' "$SATESTER_ROOT/" "$INSTALL_DIR/"

echo "[3/5] Initialize DB..."
cd "$INSTALL_DIR"
PYTHON="$INSTALL_DIR/.venv/bin/python"
[ ! -x "$PYTHON" ] && PYTHON="python3"
PYTHONPATH="$INSTALL_DIR:$INSTALL_DIR/libs:$INSTALL_DIR/libs/pycrate" \
$PYTHON -c "from src.db.engine import ensure_schema; ensure_schema(); print('  DB OK')" 2>/dev/null || echo "  DB init skipped (will init on first run)"

echo "[4/5] Systemd service..."
cp "$INSTALL_DIR/build/deploy/satester.service" /etc/systemd/system/
mkdir -p "$INSTALL_DIR/config"
[ ! -f "$INSTALL_DIR/config/satester.env" ] && cp "$INSTALL_DIR/build/deploy/config/satester.env" "$INSTALL_DIR/config/"
systemctl daemon-reload && systemctl enable satester

echo "[5/5] Starting..."
systemctl start satester && sleep 2
systemctl is-active --quiet satester && echo "SA Tester running! http://$(hostname -I | awk '{print $1}'):5000" || echo "WARN: Check journalctl -u satester"
