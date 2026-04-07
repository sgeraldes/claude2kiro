#!/bin/bash
set -e

# Get version from .version file or fallback to datetime
if [ -f .version ]; then
    VERSION=$(cat .version | tr -d '[:space:]')
else
    VERSION="0.0.$(date +%y%m%d%H%M)"
fi

echo "Building Claude2Kiro v${VERSION} for all platforms..."
echo

# Create dist directory
mkdir -p dist

# Windows AMD64
echo "Building Windows AMD64..."
GOOS=windows GOARCH=amd64 go build -ldflags "-X github.com/sgeraldes/claude2kiro/internal/tui/menu.Version=${VERSION}" -o dist/claude2kiro-windows-amd64.exe main.go

# Linux AMD64
echo "Building Linux AMD64..."
GOOS=linux GOARCH=amd64 go build -ldflags "-X github.com/sgeraldes/claude2kiro/internal/tui/menu.Version=${VERSION}" -o dist/claude2kiro-linux-amd64 main.go

# Linux ARM64
echo "Building Linux ARM64..."
GOOS=linux GOARCH=arm64 go build -ldflags "-X github.com/sgeraldes/claude2kiro/internal/tui/menu.Version=${VERSION}" -o dist/claude2kiro-linux-arm64 main.go

# macOS AMD64 (Intel)
echo "Building macOS AMD64..."
GOOS=darwin GOARCH=amd64 go build -ldflags "-X github.com/sgeraldes/claude2kiro/internal/tui/menu.Version=${VERSION}" -o dist/claude2kiro-darwin-amd64 main.go

# macOS ARM64 (Apple Silicon)
echo "Building macOS ARM64..."
GOOS=darwin GOARCH=arm64 go build -ldflags "-X github.com/sgeraldes/claude2kiro/internal/tui/menu.Version=${VERSION}" -o dist/claude2kiro-darwin-arm64 main.go

echo
echo "Build successful! Binaries in dist/"
ls -la dist/
