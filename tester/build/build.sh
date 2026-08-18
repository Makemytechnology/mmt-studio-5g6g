#!/bin/bash
# build.sh — Build self-contained SA Tester executable
#
# Output: dist/satester/ (directory with executable + all dependencies)
#
# Prerequisites:
#   pip install pyinstaller
#
# Usage:
#   ./build/build.sh           # full build
#   ./build/build.sh --clean   # clean + rebuild

set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$PROJECT_ROOT"

# ── Clean previous builds ──
if [ "$1" = "--clean" ]; then
    echo "==> Cleaning previous builds..."
    rm -rf build/dist/ build/output/ __pycache__
fi

# ── Check prerequisites ──
if ! command -v pyinstaller &>/dev/null; then
    echo "ERROR: pyinstaller not found. Install with: pip install pyinstaller"
    exit 1
fi

# ── Set PYTHONPATH for pycrate and bundled libs ──
export PYTHONPATH="${PROJECT_ROOT}:${PROJECT_ROOT}/libs:${PROJECT_ROOT}/libs/pycrate"

echo "==> Building SA Tester executable..."
pyinstaller build/satester.spec --noconfirm \
    --distpath build/dist \
    --workpath build/output \
    2>&1 | tail -20

if [ -f build/dist/satester/satester ]; then
    # Include deploy scripts in dist
    cp build/deploy/install.sh build/dist/satester/
    cp build/deploy/uninstall.sh build/dist/satester/

    echo ""
    echo "==> Build successful!"
    echo "    Output: build/dist/satester/"
    echo "    Executable: build/dist/satester/satester"
    echo ""
    SIZE=$(du -sh build/dist/satester/ | cut -f1)
    echo "    Total size: $SIZE"
    echo ""

    # Create distributable tarball
    echo "==> Creating satester.tar.gz ..."
    tar czf build/dist/satester.tar.gz -C build/dist satester/
    TARSIZE=$(du -sh build/dist/satester.tar.gz | cut -f1)
    echo "    Tarball: build/dist/satester.tar.gz ($TARSIZE)"
    echo ""
    echo "    Deploy to any Linux x86_64 machine:"
    echo "      scp build/dist/satester.tar.gz user@target:~/"
    echo "      ssh user@target 'tar xzf satester.tar.gz && sudo ./satester/install.sh'"
    echo ""
    echo "    Or run directly (without install):"
    echo "      sudo ./satester/satester"
else
    echo ""
    echo "==> Build FAILED. Check output above."
    exit 1
fi
