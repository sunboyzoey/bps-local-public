#!/usr/bin/env bash
# Build release archives for all supported platforms.
#
# Usage:  ./scripts/package.sh [version]
# Output: dist/bps-local-<version>-<os>-<arch>.zip  (Windows)
#         dist/bps-local-<version>-<os>-<arch>.tar.gz (others)
set -euo pipefail

VERSION="${1:-}"
if [ -z "$VERSION" ]; then
  VERSION="$(grep -oP '(?<=^var version = ")[^"]+' cmd/bps-local/main.go 2>/dev/null || echo dev)"
fi

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

DIST="$ROOT/dist"
rm -rf "$DIST"
mkdir -p "$DIST"

echo "packaging bps-local $VERSION"

# Helper scripts and docs ship inside every archive so a user who unzips one
# file has everything needed to run it.
STAGE_FILES=(README.md docs/USAGE.md docs/BPS-PROTOCOL-DIAGNOSTIC.md LICENSE oauth.example.json)
STAGE_WIN=(bps.ps1 bps-codex.ps1)

build_one() {
  local os="$1" arch="$2"
  local name="bps-local-${VERSION}-${os}-${arch}"
  local exe="bps-local"
  [ "$os" = "windows" ] && exe="bps-local.exe"

  echo "  building ${os}/${arch}"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
    go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION}" \
      -o "$DIST/$name/$exe" ./cmd/bps-local

  for f in "${STAGE_FILES[@]}"; do
    mkdir -p "$DIST/$name/$(dirname "$f")"
    cp "$f" "$DIST/$name/$f"
  done
  if [ "$os" = "windows" ]; then
    for f in "${STAGE_WIN[@]}"; do
      cp "$f" "$DIST/$name/$f"
    done
    zip_dir "$DIST/$name" "$DIST/${name}.zip"
  else
    ( cd "$DIST" && tar czf "${name}.tar.gz" "$name" )
  fi
  rm -rf "$DIST/$name"
}

# zip_dir SRC_DIR DEST_ZIP -- uses zip when present, otherwise PowerShell, since
# Git Bash on Windows does not ship zip.
zip_dir() {
  local src="$1" dest="$2"
  if command -v zip >/dev/null 2>&1; then
    ( cd "$(dirname "$src")" && zip -qr "$dest" "$(basename "$src")" )
    return
  fi
  local wsrc wdest
  wsrc="$(cygpath -w "$src")"
  wdest="$(cygpath -w "$dest")"
  powershell -NoProfile -Command "Compress-Archive -Path (Join-Path '$wsrc' '*') -DestinationPath '$wdest' -Force"
}

build_one windows amd64
build_one windows arm64
build_one linux   amd64
build_one linux   arm64
build_one darwin  amd64
build_one darwin  arm64

echo
if [ -f "$DIST/checksums.txt" ]; then rm -f "$DIST/checksums.txt"; fi
( cd "$DIST" && sha256sum ./*.zip ./*.tar.gz > checksums.txt 2>/dev/null || \
  shasum -a 256 ./*.zip ./*.tar.gz > checksums.txt )

echo
echo "done:"
ls -la "$DIST"
