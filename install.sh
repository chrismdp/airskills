#!/bin/bash
set -euo pipefail

# Airskills CLI installer
# Usage: curl -fsSL https://airskills.ai/install.sh | bash

REPO="chrismdp/airskills"
BINARY="airskills"
INSTALL_DIR="${AIRSKILLS_INSTALL_DIR:-$HOME/.local/bin}"

# Colours
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

info()  { echo -e "${GREEN}$1${NC}"; }
warn()  { echo -e "${YELLOW}$1${NC}"; }
error() { echo -e "${RED}$1${NC}" >&2; exit 1; }

# Detect OS and architecture
detect_platform() {
  OS=$(uname -s | tr '[:upper:]' '[:lower:]')
  ARCH=$(uname -m)

  case "$OS" in
    linux)  OS="linux" ;;
    darwin) OS="darwin" ;;
    *)      error "Unsupported OS: $OS" ;;
  esac

  case "$ARCH" in
    x86_64|amd64)  ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
    *)             error "Unsupported architecture: $ARCH" ;;
  esac

  echo "${OS}_${ARCH}"
}

# Get latest release tag from GitHub
get_latest_version() {
  curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
    | grep '"tag_name"' \
    | head -1 \
    | sed 's/.*"tag_name": *"//;s/".*//'
}

main() {
  echo ""
  info "Airskills CLI installer"
  echo ""

  PLATFORM=$(detect_platform)
  info "Platform: ${PLATFORM}"

  info "Fetching latest release..."
  VERSION=$(get_latest_version)
  if [[ -z "$VERSION" ]]; then
    error "Could not determine latest version. Is the repo public?"
  fi
  info "Version: ${VERSION}"

  ARCHIVE="${BINARY}_${PLATFORM}.tar.gz"
  URL="https://github.com/${REPO}/releases/download/${VERSION}/${ARCHIVE}"
  CHECKSUM_URL="https://github.com/${REPO}/releases/download/${VERSION}/checksums.txt"

  # Download
  TMPDIR=$(mktemp -d)
  trap "rm -rf $TMPDIR" EXIT

  info "Downloading ${ARCHIVE}..."
  curl -fsSL -o "${TMPDIR}/${ARCHIVE}" "$URL" || error "Download failed. Check https://github.com/${REPO}/releases"

  # Verify checksum
  if curl -fsSL -o "${TMPDIR}/checksums.txt" "$CHECKSUM_URL" 2>/dev/null; then
    EXPECTED=$(grep "$ARCHIVE" "${TMPDIR}/checksums.txt" | awk '{print $1}')
    if [[ -n "$EXPECTED" ]]; then
      if command -v sha256sum &>/dev/null; then
        ACTUAL=$(sha256sum "${TMPDIR}/${ARCHIVE}" | awk '{print $1}')
      else
        ACTUAL=$(shasum -a 256 "${TMPDIR}/${ARCHIVE}" | awk '{print $1}')
      fi
      if [[ "$ACTUAL" != "$EXPECTED" ]]; then
        error "Checksum mismatch!\n  Expected: ${EXPECTED}\n  Got:      ${ACTUAL}"
      fi
      info "Checksum verified."
    fi
  fi

  # Extract
  tar -xzf "${TMPDIR}/${ARCHIVE}" -C "$TMPDIR"
  EXTRACTED=$(find "$TMPDIR" -name "$BINARY" -type f | head -1)
  if [[ -z "$EXTRACTED" ]]; then
    error "Binary not found in archive"
  fi
  chmod +x "$EXTRACTED"

  # Install
  mkdir -p "$INSTALL_DIR"
  mv "$EXTRACTED" "${INSTALL_DIR}/${BINARY}"
  info "Installed to ${INSTALL_DIR}/${BINARY}"

  # Check PATH
  if ! echo "$PATH" | tr ':' '\n' | grep -q "^${INSTALL_DIR}$"; then
    echo ""
    warn "Add ${INSTALL_DIR} to your PATH:"
    echo ""
    echo "  echo 'export PATH=\"${INSTALL_DIR}:\$PATH\"' >> ~/.bashrc"
    echo "  # or for zsh:"
    echo "  echo 'export PATH=\"${INSTALL_DIR}:\$PATH\"' >> ~/.zshrc"
    echo ""
  fi

  echo ""
  info "Done! Run 'airskills login' to get started."
  echo ""
}

main
