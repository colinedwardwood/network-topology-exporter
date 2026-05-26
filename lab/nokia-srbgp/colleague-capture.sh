#!/usr/bin/env bash
# Thin shim. Exports the vendor.conf path and delegates to the shared wrapper.
SHIM_DIR="$(cd "$(dirname "$0")" && pwd)"
export VENDOR_CONF_PATH="${SHIM_DIR}/vendor.conf"
exec "${SHIM_DIR}/../../scripts/colleague-capture.sh" "$@"
