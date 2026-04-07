#!/bin/bash
# claude2kiro installer
# Usage: curl -fsSL https://raw.githubusercontent.com/sgeraldes/claude2kiro/main/install.sh | bash
#
# Installs:
#   ~/.local/bin/claude2kiro          <- Lightweight launcher (in PATH)
#   ~/.claude2kiro/bin/claude2kiro-X.Y.Z  <- Versioned app binary (auto-managed)

set -e

REPO="sgeraldes/claude2kiro"
LAUNCHER_DIR="${LAUNCHER_DIR:-$HOME/.local/bin}"
APP_DIR="$HOME/.claude2kiro/bin"

# Detect platform
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)

case "$ARCH" in
  x86_64|amd64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) echo "Unsupported architecture: $ARCH"; exit 1 ;;
esac

case "$OS" in
  linux|darwin) ;;
  mingw*|msys*|cygwin*) OS="windows" ;;
  *) echo "Unsupported OS: $OS"; exit 1 ;;
esac

EXT=""
if [ "$OS" = "windows" ]; then
  EXT=".exe"
fi

LAUNCHER_ASSET="claude2kiro-launcher-${OS}-${ARCH}${EXT}"
APP_ASSET="claude2kiro-${OS}-${ARCH}${EXT}"

echo "claude2kiro installer"
echo "====================="
echo "Platform: ${OS}/${ARCH}"
echo ""

# Get latest release info
echo "Fetching latest release..."
RELEASE_JSON=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest")

VERSION=$(echo "$RELEASE_JSON" | grep '"tag_name"' | head -1 | cut -d '"' -f 4 | sed 's/^v//')

LAUNCHER_URL=$(echo "$RELEASE_JSON" | grep "browser_download_url.*${LAUNCHER_ASSET}" | head -1 | cut -d '"' -f 4)
APP_URL=$(echo "$RELEASE_JSON" | grep "browser_download_url.*${APP_ASSET}" | head -1 | cut -d '"' -f 4)

if [ -z "$LAUNCHER_URL" ] || [ -z "$APP_URL" ]; then
  echo "Error: Release assets not found for ${OS}/${ARCH}"
  echo "Check: https://github.com/${REPO}/releases"
  exit 1
fi

echo "Version: v${VERSION}"

# Create directories
mkdir -p "$LAUNCHER_DIR" "$APP_DIR"

# Download launcher
echo "Downloading launcher..."
curl -fsSL "$LAUNCHER_URL" -o "${LAUNCHER_DIR}/claude2kiro${EXT}"
chmod +x "${LAUNCHER_DIR}/claude2kiro${EXT}"

# Download app binary
echo "Downloading claude2kiro v${VERSION}..."
curl -fsSL "$APP_URL" -o "${APP_DIR}/claude2kiro-${VERSION}${EXT}"
chmod +x "${APP_DIR}/claude2kiro-${VERSION}${EXT}"

# Set current version
echo "$VERSION" > "${APP_DIR}/current.txt"

echo ""
echo "Installed:"
echo "  Launcher: ${LAUNCHER_DIR}/claude2kiro${EXT}"
echo "  Binary:   ${APP_DIR}/claude2kiro-${VERSION}${EXT}"

# Check PATH
if ! echo "$PATH" | tr ':' '\n' | grep -q "^${LAUNCHER_DIR}$"; then
  echo ""
  echo "Add to PATH:"
  echo "  export PATH=\"${LAUNCHER_DIR}:\$PATH\""
fi

echo ""
echo "Get started:"
echo "  claude2kiro login    # Authenticate with Kiro"
echo "  claude2kiro run      # Start Claude Code via Kiro"
echo "  claude2kiro update   # Auto-update (works with running instances)"
