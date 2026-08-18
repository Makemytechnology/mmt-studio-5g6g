#!/bin/bash
# SA Tester 5G — Development Environment Setup
# Installs system dependencies needed for running from source.

set -e

echo "Updating package lists..."
sudo apt update

echo "Installing SCTP development libraries..."
sudo apt install -y libsctp-dev lksctp-tools

echo "Installing iperf3 (traffic testing)..."
sudo apt install -y iperf3

echo "Installing iproute2 (TUN/routing)..."
sudo apt install -y iproute2

echo "Setup completed."
