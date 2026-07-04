#!/bin/sh
# Orchestra installer — the operating system for AI coding agents.
#
#   curl -fsSL https://raw.githubusercontent.com/crossben/orchestra-code/main/install.sh | sh
#
# Env overrides:
#   ORCHESTRA_VERSION=v0.7.0   pin a version (default: latest release)
#   BINDIR=/usr/local/bin      install location (default: /usr/local/bin if
#                              writable, else ~/.local/bin)
set -eu

REPO="crossben/orchestra-code"
BINARY="orchestra"

info() { printf '  %s\n' "$*"; }
err()  { printf 'error: %s\n' "$*" >&2; exit 1; }

# --- detect OS ---
os=$(uname -s)
case "$os" in
  Linux)  OS=linux ;;
  Darwin) OS=darwin ;;
  *) err "unsupported OS '$os'. Windows users: download the .zip from
     https://github.com/$REPO/releases (or use 'go install github.com/$REPO/cmd/orchestra@latest')." ;;
esac

# --- detect arch ---
arch=$(uname -m)
case "$arch" in
  x86_64|amd64)  ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) err "unsupported architecture '$arch'." ;;
esac

# --- required tools ---
if command -v curl >/dev/null 2>&1; then DL="curl -fsSL"; DLO="curl -fsSL -o";
elif command -v wget >/dev/null 2>&1; then DL="wget -qO-"; DLO="wget -qO";
else err "need curl or wget."; fi

# --- resolve version ---
VERSION="${ORCHESTRA_VERSION:-}"
if [ -z "$VERSION" ]; then
  info "resolving latest release…"
  VERSION=$($DL "https://api.github.com/repos/$REPO/releases/latest" \
    | grep '"tag_name"' | head -1 | cut -d'"' -f4)
  [ -n "$VERSION" ] || err "could not determine the latest release — is one published yet?"
fi
NUM=${VERSION#v} # strip leading v for archive names

EXT=tar.gz
ARCHIVE="${BINARY}_${NUM}_${OS}_${ARCH}.${EXT}"
BASE="https://github.com/$REPO/releases/download/$VERSION"

info "installing $BINARY $VERSION ($OS/$ARCH)"

# --- download to a temp dir ---
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
$DLO "$TMP/$ARCHIVE" "$BASE/$ARCHIVE" || err "download failed: $BASE/$ARCHIVE"

# --- verify checksum (best-effort but on by default) ---
if $DLO "$TMP/checksums.txt" "$BASE/checksums.txt" 2>/dev/null; then
  if command -v sha256sum >/dev/null 2>&1; then SUM="sha256sum";
  elif command -v shasum >/dev/null 2>&1; then SUM="shasum -a 256";
  else SUM=""; fi
  if [ -n "$SUM" ]; then
    want=$(grep " $ARCHIVE\$" "$TMP/checksums.txt" | awk '{print $1}')
    got=$(cd "$TMP" && $SUM "$ARCHIVE" | awk '{print $1}')
    [ -n "$want" ] && [ "$want" = "$got" ] || err "checksum mismatch for $ARCHIVE"
    info "checksum verified"
  fi
else
  info "checksums.txt not found — skipping verification"
fi

# --- extract ---
tar -xzf "$TMP/$ARCHIVE" -C "$TMP"
[ -f "$TMP/$BINARY" ] || err "archive did not contain '$BINARY'"
chmod +x "$TMP/$BINARY"

# --- choose install dir ---
if [ -n "${BINDIR:-}" ]; then
  DEST="$BINDIR"
elif [ -w /usr/local/bin ] 2>/dev/null; then
  DEST=/usr/local/bin
else
  DEST="$HOME/.local/bin"
fi
mkdir -p "$DEST"

if mv "$TMP/$BINARY" "$DEST/$BINARY" 2>/dev/null; then
  :
elif command -v sudo >/dev/null 2>&1 && [ "$DEST" = /usr/local/bin ]; then
  info "writing to $DEST needs sudo…"
  sudo mv "$TMP/$BINARY" "$DEST/$BINARY"
else
  err "cannot write to $DEST — set BINDIR=<dir> and re-run."
fi

info "installed $DEST/$BINARY"
case ":$PATH:" in
  *":$DEST:"*) : ;;
  *) info "note: $DEST is not on your PATH — add it:"
     info "  export PATH=\"\$PATH:$DEST\"" ;;
esac
printf '\n  run: %s --help    (or: %s dashboard)\n' "$BINARY" "$BINARY"
