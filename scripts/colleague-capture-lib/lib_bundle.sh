#!/usr/bin/env bash
# Compute SHA256SUMS for everything in <captures-dir> and tar it up.
# SHA256_CMD is set by main() after detect_sha256.

write_sha256sums() {
  local dir="${1:-}"
  [ -z "$dir" ] || [ ! -d "$dir" ] && return 1
  # shellcheck disable=SC2086
  ( cd "$dir" && find . -type f ! -name 'SHA256SUMS' -print0 \
    | xargs -0 ${SHA256_CMD:-sha256sum} ) > "${dir}/SHA256SUMS"
}

# bundle_tarball CAPTURES_DIR VENDOR HOSTS_JOINED → echoes the tarball path
bundle_tarball() {
  local dir="${1:-}" vendor="${2:-}" hosts_joined="${3:-}"
  [ -z "$dir" ] || [ -z "$vendor" ] && return 1
  local ts safe_hosts tarball
  ts="$(date -u +%Y%m%dT%H%M%SZ)"
  safe_hosts="$(echo "${hosts_joined:-unknown}" | tr ',' '-' | tr '.' '_' | tr ':' '_')"
  tarball="$(dirname "$dir")/topology-capture-${vendor}-${safe_hosts}-${ts}.tar.gz"
  tar -czf "$tarball" -C "$(dirname "$dir")" "$(basename "$dir")"
  echo "$tarball"
}

tarball_sha256() {
  local tarball="${1:-}"
  [ -z "$tarball" ] && return 1
  # shellcheck disable=SC2086
  ${SHA256_CMD:-sha256sum} "$tarball" | awk '{print $1}'
}
