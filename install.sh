#!/bin/sh
set -eu

BASE="https://github.com/darqlab/hermes/releases"

os() {
  case "$(uname -s)" in
    Linux)  echo "linux" ;;
    Darwin) echo "darwin" ;;
    *)      echo "unsupported OS: $(uname -s)" >&2; exit 1 ;;
  esac
}

arch() {
  case "$(uname -m)" in
    x86_64|amd64) echo "amd64" ;;
    aarch64|arm64) echo "arm64" ;;
    *) echo "unsupported arch: $(uname -m)" >&2; exit 1 ;;
  esac
}

TAG="${HERMES_VERSION:-latest}"
if [ "$TAG" = "latest" ]; then
  TAG=$(curl -sL "$BASE/latest" | sed -n 's/.*tag\/\(v[0-9.]*\).*/\1/p' | head -1)
  if [ -z "$TAG" ]; then
	TAG=$(curl -sL "https://api.github.com/repos/darqlab/hermes/releases/latest" | grep '"tag_name"' | sed -E 's/.*"([^"]+)".*/\1/')
  fi
fi

if [ -z "$TAG" ]; then
  echo "could not determine latest version" >&2
  exit 1
fi

OS=$(os)
ARCH=$(arch)
URL="$BASE/download/$TAG/hermes-$OS-$ARCH"
echo "==> downloading hermes $TAG for $OS/$ARCH"
curl -sL -o /tmp/hermes "$URL"
chmod +x /tmp/hermes

DEST="${HOME}/.local/bin"
mkdir -p "$DEST"
mv /tmp/hermes "$DEST/hermes"

echo "==> hermes $TAG installed to $DEST/hermes"
"$DEST/hermes" --version
