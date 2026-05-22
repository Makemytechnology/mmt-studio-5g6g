#!/bin/bash
# SA Tester 5G — Uninstall Script
set -e
INSTALL_DIR="${1:-/opt/satester}"
if [ "$EUID" -ne 0 ]; then echo "ERROR: Run as root"; exit 1; fi

echo "Stopping SA Tester..."
systemctl stop satester 2>/dev/null || true
systemctl disable satester 2>/dev/null || true
rm -f /etc/systemd/system/satester.service
systemctl daemon-reload

echo "Removing symlink..."
rm -f /usr/local/bin/satester

echo "SA Tester uninstalled."
echo "Data directory NOT removed: $INSTALL_DIR"
echo "  To remove completely: rm -rf $INSTALL_DIR"
