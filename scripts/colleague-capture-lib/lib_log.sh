#!/usr/bin/env bash
# Append-only execution log. Sanitizes auth/priv passwords + community before
# writing every line.

WRAPPER_LOG=""

log_init() {
  WRAPPER_LOG="${1:-}"
  [ -z "$WRAPPER_LOG" ] && return 1
  : > "$WRAPPER_LOG"
}

log_event() {
  [ -z "${WRAPPER_LOG:-}" ] && return 0  # no-op if log not initialized
  local msg="${1:-}"
  if [ -n "${V3_AUTH_PASS:-}" ]; then
    msg="${msg//${V3_AUTH_PASS}/***}"
  fi
  if [ -n "${V3_PRIV_PASS:-}" ]; then
    msg="${msg//${V3_PRIV_PASS}/***}"
  fi
  if [ -n "${COMMUNITY:-}" ]; then
    msg="${msg//${COMMUNITY}/***}"
  fi
  echo "[$(date -u +%Y-%m-%dT%H:%M:%SZ)] $msg" >> "$WRAPPER_LOG"
}
