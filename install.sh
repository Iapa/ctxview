#!/bin/sh
set -eu

REPO="${CTXVIEW_REPO:-Iapa/ctxview}"
VERSION="${CTXVIEW_VERSION:-latest}"
BIN_NAME="ctxview"

log() {
  printf '%s\n' "$*" >&2
}

fail() {
  log "Error: $*"
  exit 1
}

need_cmd() {
  command -v "$1" >/dev/null 2>&1
}

resolve_install_dir() {
  if [ -n "${CTXVIEW_INSTALL_DIR:-}" ]; then
    printf '%s' "$CTXVIEW_INSTALL_DIR"
    return
  fi
  if [ -d /usr/local/bin ] && [ -w /usr/local/bin ]; then
    printf '%s' "/usr/local/bin"
    return
  fi
  printf '%s' "${HOME}/.local/bin"
}

detect_os() {
  case "$(uname -s)" in
    Darwin) printf 'darwin' ;;
    Linux)  printf 'linux' ;;
    *)      fail "unsupported OS: $(uname -s)" ;;
  esac
}

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64)  printf 'amd64' ;;
    arm64|aarch64) printf 'arm64' ;;
    *)             fail "unsupported CPU architecture: $(uname -m)" ;;
  esac
}

fetch_text() {
  url="$1"
  if need_cmd curl; then
    curl -fsSL "$url"
  elif need_cmd wget; then
    wget -qO- "$url"
  else
    fail "curl or wget is required"
  fi
}

download_file() {
  url="$1"
  dest="$2"
  if need_cmd curl; then
    curl -fsSL "$url" -o "$dest"
  elif need_cmd wget; then
    wget -qO "$dest" "$url"
  else
    fail "curl or wget is required"
  fi
}

verify_checksum() {
  file="$1"
  checksums="$2"
  base="$(basename "$file")"
  expected="$(grep " ${base}\$" "$checksums" | awk '{print $1}' | head -n 1 || true)"
  if [ -z "$expected" ]; then
    return 0
  fi
  if need_cmd sha256sum; then
    actual="$(sha256sum "$file" | awk '{print $1}')"
  elif need_cmd shasum; then
    actual="$(shasum -a 256 "$file" | awk '{print $1}')"
  else
    return 0
  fi
  if [ "$expected" != "$actual" ]; then
    fail "checksum mismatch for ${base}: expected ${expected}, got ${actual}"
  fi
}

case "$REPO" in
  *"<"*)
    if [ "${CTXVIEW_FROM_SOURCE:-0}" != "1" ]; then
      fail "set CTXVIEW_REPO=<github-owner>/<repo> (or update REPO in install.sh after exporting standalone repo)"
    fi
    ;;
esac

if [ "${CTXVIEW_FROM_SOURCE:-0}" = "1" ]; then
  need_cmd go || fail "Go toolchain is required for CTXVIEW_FROM_SOURCE=1"
  target_ref="$VERSION"
  log "Installing via go install github.com/${REPO}@${target_ref}..."
  GO111MODULE=on go install "github.com/${REPO}@${target_ref}"
  exit 0
fi

OS="$(detect_os)"
ARCH="$(detect_arch)"

if [ "$VERSION" = "latest" ]; then
  api_url="https://api.github.com/repos/${REPO}/releases/latest"
  TAG="$(fetch_text "$api_url" | sed -n 's/.*"tag_name":[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)"
  [ -n "$TAG" ] || fail "failed to resolve latest release tag from ${api_url}"
else
  case "$VERSION" in
    v*) TAG="$VERSION" ;;
    *)  TAG="v${VERSION}" ;;
  esac
fi

VER_NO_V="${TAG#v}"
ARCHIVE="${BIN_NAME}_${VER_NO_V}_${OS}_${ARCH}.tar.gz"
BASE_URL="https://github.com/${REPO}/releases/download/${TAG}"

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT INT TERM

log "Downloading ${ARCHIVE} (${TAG})..."
download_file "${BASE_URL}/${ARCHIVE}" "${TMP_DIR}/${ARCHIVE}"
if download_file "${BASE_URL}/checksums.txt" "${TMP_DIR}/checksums.txt" 2>/dev/null; then
  verify_checksum "${TMP_DIR}/${ARCHIVE}" "${TMP_DIR}/checksums.txt"
fi

tar -xzf "${TMP_DIR}/${ARCHIVE}" -C "$TMP_DIR"
[ -f "${TMP_DIR}/${BIN_NAME}" ] || fail "archive did not contain ${BIN_NAME} binary"

INSTALL_DIR="$(resolve_install_dir)"
mkdir -p "$INSTALL_DIR"
install_path="${INSTALL_DIR}/${BIN_NAME}"
cp "${TMP_DIR}/${BIN_NAME}" "$install_path"
chmod 755 "$install_path"

if [ "$OS" = "darwin" ] && need_cmd xattr; then
  xattr -d com.apple.quarantine "$install_path" 2>/dev/null || true
fi

installed_ver="$("$install_path" -version 2>/dev/null || printf '%s' "$TAG")"
log "Installed ${BIN_NAME} (${installed_ver}) to ${install_path}"

case ":${PATH}:" in
  *":${INSTALL_DIR}:"*) ;;
  *)
    log "Note: add ${INSTALL_DIR} to your PATH to run '${BIN_NAME}' from anywhere:"
    log "  export PATH=\"${INSTALL_DIR}:\$PATH\""
    ;;
esac

log "Run: ${BIN_NAME} -open"
