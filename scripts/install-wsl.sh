#!/usr/bin/env bash

set -euo pipefail

readonly target_dir="/home/kkamji/go/bin"
readonly target_path="$target_dir/aiquota"

cleanup_host=""
local_tmp=""
remote_tmp=""

usage() {
  cat <<'USAGE'
Usage: scripts/install-wsl.sh [SSH_HOST]

Cross-build aiquota for linux/amd64 from this Mac checkout and atomically
install it at /home/kkamji/go/bin/aiquota on WSL.

SSH_HOST defaults to AIQUOTA_WSL_HOST, or "wsl" when the variable is unset.
USAGE
}

die() {
  printf 'install-wsl: %s\n' "$*" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"
}

cleanup() {
  local status=$?
  trap - EXIT
  if [[ -n "$local_tmp" ]]; then
    rm -rf -- "${local_tmp:?}"
  fi
  if [[ -n "$remote_tmp" ]]; then
    ssh "$cleanup_host" rm -f -- "$remote_tmp" >/dev/null 2>&1 || true
  fi
  exit "$status"
}

main() {
  case "${1:-}" in
    -h|--help)
      usage
      return 0
      ;;
  esac
  if (( $# > 1 )); then
    usage >&2
    return 2
  fi

  local host="${1:-${AIQUOTA_WSL_HOST:-wsl}}"
  if [[ ! "$host" =~ ^[A-Za-z0-9][A-Za-z0-9._@-]*$ ]]; then
    die "invalid SSH host: $host"
  fi

  require_command go
  require_command ssh
  require_command scp
  require_command shasum

  local script_dir repo_root local_binary local_sha remote_sha
  script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
  repo_root="$(cd -- "$script_dir/.." && pwd)"
  local_tmp="$(mktemp -d "${TMPDIR:-/tmp}/aiquota-wsl-install.XXXXXX")"
  local_binary="$local_tmp/aiquota"
  remote_tmp=""
  cleanup_host="$host"
  trap cleanup EXIT

  (
    cd -- "$repo_root"
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o "$local_binary" ./cmd/aiquota
  )
  read -r local_sha _ < <(shasum -a 256 "$local_binary")

  remote_tmp="$(ssh "$host" bash -s -- "$target_dir" <<'REMOTE_TEMP'
set -euo pipefail
target_dir=$1
mkdir -p -- "$target_dir"
mktemp "$target_dir/.aiquota.install.XXXXXX"
REMOTE_TEMP
)"
  [[ "$remote_tmp" == "$target_dir"/.aiquota.install.* ]] || die "unexpected remote temporary path"

  scp -- "$local_binary" "$host:$remote_tmp"
  ssh "$host" bash -s -- "$remote_tmp" <<'REMOTE_SMOKE'
set -euo pipefail
incoming=$1
chmod 0755 "$incoming"
"$incoming" --help >/dev/null 2>&1
REMOTE_SMOKE

  remote_sha="$(ssh "$host" bash -s -- "$remote_tmp" "$target_path" "$local_sha" <<'REMOTE_INSTALL'
set -euo pipefail

incoming=$1
target=$2
expected_sha=$3
target_dir=${target%/*}
backup=""
had_target=0
installed=0

rollback() {
  status=$?
  trap - EXIT
  if (( status != 0 && installed == 1 )); then
    if (( had_target == 1 )); then
      mv -f -- "$backup" "$target"
      backup=""
    else
      rm -f -- "$target"
    fi
  fi
  rm -f -- "$incoming"
  if [[ -n "$backup" ]]; then
    rm -f -- "$backup"
  fi
  exit "$status"
}
trap rollback EXIT

if [[ -e "$target" ]]; then
  backup="$(mktemp "$target_dir/.aiquota.backup.XXXXXX")"
  cp -p -- "$target" "$backup"
  had_target=1
fi

mv -f -- "$incoming" "$target"
installed=1
"$target" --help >/dev/null 2>&1
read -r installed_sha _ < <(sha256sum "$target")
[[ "$installed_sha" == "$expected_sha" ]]

trap - EXIT
if [[ -n "$backup" ]]; then
  rm -f -- "$backup"
fi
printf '%s\n' "$installed_sha"
REMOTE_INSTALL
)"

  remote_tmp=""
  [[ "$remote_sha" == "$local_sha" ]] || die "installed SHA-256 does not match local build"

  printf 'Installed aiquota on %s at %s\n' "$host" "$target_path"
  printf 'SHA-256: %s\n' "$local_sha"

  trap - EXIT
  rm -rf -- "${local_tmp:?}"
}

main "$@"
