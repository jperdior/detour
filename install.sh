#!/bin/sh
# Installs the latest detour release into ~/.local/bin (override with
# DETOUR_INSTALL_DIR). macOS and Linux, arm64 and amd64.
#   curl -fsSL https://raw.githubusercontent.com/jperdior/detour/main/install.sh | sh
set -eu
repo=jperdior/detour
dir=${DETOUR_INSTALL_DIR:-$HOME/.local/bin}

case "$(uname -s)" in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  *) echo "detour: unsupported OS $(uname -s)" >&2; exit 1 ;;
esac
case "$(uname -m)" in
  arm64|aarch64) arch=arm64 ;;
  x86_64|amd64) arch=amd64 ;;
  *) echo "detour: unsupported CPU $(uname -m)" >&2; exit 1 ;;
esac

asset=detour_${os}_${arch}
base=https://github.com/$repo/releases/latest/download
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

curl -fsSL "$base/$asset" -o "$tmp/$asset"
curl -fsSL "$base/checksums.txt" -o "$tmp/checksums.txt"
want=$(grep " $asset\$" "$tmp/checksums.txt" | cut -d' ' -f1)
if command -v sha256sum >/dev/null 2>&1; then
  got=$(sha256sum "$tmp/$asset" | cut -d' ' -f1)
else
  got=$(shasum -a 256 "$tmp/$asset" | cut -d' ' -f1)
fi
if [ -z "$want" ] || [ "$want" != "$got" ]; then
  echo "detour: checksum mismatch, not installing" >&2
  exit 1
fi

mkdir -p "$dir"
chmod +x "$tmp/$asset"
mv "$tmp/$asset" "$dir/detour.new" && mv "$dir/detour.new" "$dir/detour"
echo "detour $("$dir/detour" version) installed in $dir/detour"
case ":$PATH:" in
  *":$dir:"*) ;;
  *) echo "Add $dir to your PATH, e.g.: echo 'export PATH=\"$dir:\$PATH\"' >> ~/.zshrc" ;;
esac
echo "Next: detour trial setup ~/path/to/your/repo   (see https://github.com/$repo/blob/main/docs/TRIAL.md)"
