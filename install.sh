#!/bin/bash
# claude2kiro installer - downloads latest release from GitHub
# Usage: curl -fsSL https://raw.githubusercontent.com/sgeraldes/claude2kiro/main/install.sh | bash

set -e

REPO="sgeraldes/claude2kiro"
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"

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
  INSTALL_DIR="${INSTALL_DIR:-$HOME/bin}"
fi

ASSET="claude2kiro-${OS}-${ARCH}${EXT}"

echo "claude2kiro installer"
echo "====================="
echo "Platform: ${OS}/${ARCH}"
echo "Install to: ${INSTALL_DIR}"
echo ""

# Get latest release
echo "Fetching latest release..."
RELEASE_URL=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
  | grep "browser_download_url.*${ASSET}" \
  | head -1 \
  | cut -d '"' -f 4)

if [ -z "$RELEASE_URL" ]; then
  echo "Error: No binary found for ${OS}/${ARCH}"
  echo "Check releases at: https://github.com/${REPO}/releases"
  exit 1
fi

VERSION=$(echo "$RELEASE_URL" | grep -oP 'v[\d.]+' | head -1)
echo "Latest version: ${VERSION}"
echo "Downloading ${ASSET}..."

# Create install directory
mkdir -p "$INSTALL_DIR"

# Download
curl -fsSL "$RELEASE_URL" -o "${INSTALL_DIR}/claude2kiro${EXT}"
chmod +x "${INSTALL_DIR}/claude2kiro${EXT}"

echo ""
echo "Installed claude2kiro ${VERSION} to ${INSTALL_DIR}/claude2kiro${EXT}"

# Check if install dir is in PATH
if ! echo "$PATH" | tr ':' '\n' | grep -q "^${INSTALL_DIR}$"; then
  echo ""
  echo "Add to your PATH:"
  echo "  export PATH=\"${INSTALL_DIR}:\$PATH\""
  echo ""
  echo "Or add to ~/.bashrc / ~/.zshrc:"
  echo "  echo 'export PATH=\"${INSTALL_DIR}:\$PATH\"' >> ~/.bashrc"
fi

echo ""
echo "Get started:"
echo "  claude2kiro login    # Authenticate with Kiro"
echo "  claude2kiro run      # Start Claude Code with Kiro proxy"
echo "  claude2kiro update   # Self-update to latest release"
