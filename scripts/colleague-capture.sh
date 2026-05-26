#!/usr/bin/env bash
# Colleague-driven SNMP capture wrapper.
# See plans/colleague-capture.md for the design and plans/colleague-capture-pr1.md
# for the implementation plan.
set -euo pipefail
IFS=$'\n\t'

WRAPPER_VERSION="0.1.0"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
LIB_DIR="${SCRIPT_DIR}/colleague-capture-lib"

# Source lib modules. Order matters: log/json have no deps; everything else
# depends on them. Args is sourced last because it consumes the cmdline.
# shellcheck source=colleague-capture-lib/lib_json.sh disable=SC1091
. "${LIB_DIR}/lib_json.sh"

# Subsequent tasks will source more libs here.

main() {
  echo "colleague-capture v${WRAPPER_VERSION}: scaffold only (no behavior yet)" >&2
  exit 0
}

main "$@"
