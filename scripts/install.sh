#!/bin/sh
set -eu

repo="ardasevinc/mattermost-cli"
version="${MATTERMOST_CLI_VERSION:-latest}"
install_dir="${MATTERMOST_CLI_INSTALL_DIR:-}"

log() { printf '%s\n' "$*" >&2; }
fail() { log "error: $*"; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || fail "missing required command: $1"; }

need curl
need tar

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$os" in
  darwin|linux) ;;
  *) fail "unsupported OS: $os" ;;
esac

arch="$(uname -m)"
case "$arch" in
  arm64|aarch64) arch="arm64" ;;
  x86_64|amd64) arch="amd64" ;;
  *) fail "unsupported architecture: $arch" ;;
esac

if [ "$version" = "latest" ]; then
  latest_url="https://api.github.com/repos/${repo}/releases/latest"
  version="$(curl -fsSL "$latest_url" | sed -n 's/.*"tag_name":[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)"
  [ -n "$version" ] || fail "could not resolve latest release"
fi
case "$version" in
  v[0-9]*.[0-9]*.[0-9]*|[0-9]*.[0-9]*.[0-9]*) ;;
  *) fail "release version must be a v-prefixed semantic version" ;;
esac
tag="$version"
case "$tag" in v*) ;; *) tag="v$tag" ;; esac
version="${tag#v}"
case "$version" in *[!0-9A-Za-z.-]*|.*|*..*|*.) fail "invalid release version" ;; esac

asset="mattermost-cli_${version}_${os}_${arch}.tar.gz"
base_url="https://github.com/${repo}/releases/download/${tag}"

if [ -z "$install_dir" ]; then
  if [ -n "${GOBIN:-}" ]; then
    install_dir="$GOBIN"
  elif [ -n "${GOPATH:-}" ]; then
    install_dir="$GOPATH/bin"
  else
    install_dir="$HOME/.local/bin"
  fi
fi

tmp=""
install_tmp=""
cleanup() {
  [ -z "$install_tmp" ] || rm -f -- "$install_tmp"
  [ -z "$tmp" ] || rm -rf -- "$tmp"
}
trap cleanup EXIT INT TERM
tmp="$(mktemp -d "${TMPDIR:-/tmp}/mattermost-cli-install.XXXXXX")"
chmod 700 "$tmp"

log "downloading mattermost-cli ${tag} for ${os}/${arch}"
curl -fsSL "${base_url}/${asset}" -o "$tmp/$asset"
curl -fsSL "${base_url}/checksums.txt" -o "$tmp/checksums.txt"

expected="$(awk -v asset="$asset" '$2 == asset { print $1 }' "$tmp/checksums.txt")"
case "$expected" in
  *' '*|*'\n'*|'') fail "checksum for ${asset} must appear exactly once" ;;
  *[!0-9a-f]* ) fail "checksum for ${asset} is invalid" ;;
esac
[ "${#expected}" -eq 64 ] || fail "checksum for ${asset} is invalid"

if command -v sha256sum >/dev/null 2>&1; then
  actual="$(sha256sum "$tmp/$asset" | awk '{print $1}')"
elif command -v shasum >/dev/null 2>&1; then
  actual="$(shasum -a 256 "$tmp/$asset" | awk '{print $1}')"
else
  fail "missing sha256sum or shasum"
fi
[ "$actual" = "$expected" ] || fail "checksum mismatch for ${asset}"

[ "$(tar -tzf "$tmp/$asset")" = "mm" ] || fail "archive member list is invalid"
tar -xzf "$tmp/$asset" -C "$tmp"
[ -f "$tmp/mm" ] && [ ! -L "$tmp/mm" ] || fail "archive did not contain a regular mm binary"
chmod 755 "$tmp/mm"
mkdir -p "$install_dir"
install_tmp="$(mktemp "$install_dir/.mattermost-cli-install.XXXXXX")"
cp "$tmp/mm" "$install_tmp"
chmod 755 "$install_tmp"
mv -f "$install_tmp" "$install_dir/mm"
install_tmp=""

log "installed $install_dir/mm"
"$install_dir/mm" --version
case ":$PATH:" in
  *":$install_dir:"*) ;;
  *) log "note: $install_dir is not on PATH" ;;
esac
