#!/bin/bash
# Builds self-contained SuperSync executables for macOS, Windows and Linux into dist/.
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${1:-$(git describe --tags --always 2>/dev/null || echo dev)}"
LDFLAGS="-s -w -X main.version=$VERSION"
rm -rf dist && mkdir -p dist

build() { # os arch output
  echo "  $3"
  CGO_ENABLED=0 GOOS=$1 GOARCH=$2 go build -trimpath -ldflags "$LDFLAGS" -o "dist/$3" .
}

echo "Building SuperSync $VERSION"
build darwin  arm64 supersync-mac-arm64
build darwin  amd64 supersync-mac-intel
build windows amd64 SuperSync-windows.exe
build windows arm64 SuperSync-windows-arm64.exe
build linux   amd64 supersync-linux-amd64
build linux   arm64 supersync-linux-arm64

# One macOS file that runs on both Apple Silicon and Intel.
if command -v lipo >/dev/null; then
  lipo -create -output dist/SuperSync-mac dist/supersync-mac-arm64 dist/supersync-mac-intel
  rm dist/supersync-mac-arm64 dist/supersync-mac-intel
  # Ad-hoc signature so Apple Silicon will run it (it's still unnotarized).
  codesign --force --sign - dist/SuperSync-mac 2>/dev/null || true
  echo "  SuperSync-mac (universal)"
fi
ls -lh dist
