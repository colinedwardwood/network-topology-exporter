#!/usr/bin/env bash
#
# run-scale-bench.sh — drive the /metrics scale benchmark and capture
# the result + run environment for docs/operator/scale.md.
#
# Usage:
#   ./scripts/run-scale-bench.sh                   # default: count=5, 10s per bench
#   COUNT=10 BENCHTIME=20s ./scripts/run-scale-bench.sh
#   PIN_CORE=2 ./scripts/run-scale-bench.sh        # taskset to a specific CPU
#
# Output: a single file under scripts/bench-results/ named
#   scale-bench-<host>-<YYYYMMDD-HHMMSS>.txt
# containing:
#   - Hardware/kernel/Go version stamp at the top
#   - The full `go test -bench` output below
#
# The script tries (and reports) common variance-reduction steps that
# require root: CPU frequency governor and per-core pinning. Both are
# best-effort; if they fail or you skip them, the output still records
# what state the run was in.

set -euo pipefail

REPO_ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." &> /dev/null && pwd)"
cd "$REPO_ROOT"

OUTDIR="$REPO_ROOT/scripts/bench-results"
mkdir -p "$OUTDIR"

COUNT="${COUNT:-5}"
BENCHTIME="${BENCHTIME:-10s}"
PIN_CORE="${PIN_CORE:-0}"  # taskset -c $PIN_CORE; empty disables pinning

HOST="$(hostname -s 2>/dev/null || hostname)"
STAMP="$(date -u +%Y%m%d-%H%M%S)"
OUT="$OUTDIR/scale-bench-$HOST-$STAMP.txt"

# Build the run command. On Linux, use taskset when available so the bench
# pins to a single CPU; this drops the noise floor by an order of magnitude
# on shared boxes. macOS lacks taskset; the bench runs without pinning.
RUN_PREFIX=()
if [ -n "$PIN_CORE" ] && command -v taskset >/dev/null 2>&1; then
    RUN_PREFIX=(taskset -c "$PIN_CORE")
fi

# Capture the run environment up front. Everything in this block is
# best-effort — failures are noted in the output but do not abort the run.
{
    echo "# /metrics scale benchmark"
    echo "# Captured: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
    echo "# Host: $HOST"
    echo "# Run command: ${RUN_PREFIX[*]:-(no prefix)} go test -tags=bench -bench=BenchmarkMetricsRender -benchtime=$BENCHTIME -count=$COUNT -run=^\$ ./cmd/topology-exporter/"
    echo
    echo "## uname"
    uname -a 2>&1 || echo "(unavailable)"
    echo
    echo "## CPU"
    if [ -r /proc/cpuinfo ]; then
        grep -m1 'model name' /proc/cpuinfo 2>&1 || echo "(no model name)"
        echo -n "cores: "; nproc 2>&1 || echo "(unavailable)"
    elif command -v sysctl >/dev/null 2>&1; then
        sysctl -n machdep.cpu.brand_string 2>&1 || echo "(unavailable)"
        echo -n "cores: "; sysctl -n hw.ncpu 2>&1 || echo "(unavailable)"
    fi
    echo
    echo "## Memory"
    if command -v free >/dev/null 2>&1; then
        free -h 2>&1
    elif command -v sysctl >/dev/null 2>&1; then
        echo -n "hw.memsize: "; sysctl -n hw.memsize 2>&1 || echo "(unavailable)"
    fi
    echo
    echo "## CPU governor (Linux only — variance control)"
    if [ -r /sys/devices/system/cpu/cpu0/cpufreq/scaling_governor ]; then
        for f in /sys/devices/system/cpu/cpu*/cpufreq/scaling_governor; do
            cpu=$(basename "$(dirname "$(dirname "$f")")")
            echo "$cpu: $(cat "$f")"
        done | head -20
    else
        echo "(not applicable on this OS)"
    fi
    echo
    echo "## Go"
    go version 2>&1
    echo
    echo "## Pinning"
    echo "PIN_CORE=${PIN_CORE:-(unset)}"
    if [ ${#RUN_PREFIX[@]} -gt 0 ]; then
        echo "taskset prefix active: ${RUN_PREFIX[*]}"
    else
        echo "no pinning (macOS or taskset unavailable)"
    fi
    echo
    echo "## Benchmark output"
    echo
} > "$OUT"

echo "Running benchmark — writing to $OUT" >&2
echo "  count=$COUNT  benchtime=$BENCHTIME  pin=${PIN_CORE:-(disabled)}" >&2

# Streaming append so the user sees progress and partial results survive
# any interrupt or timeout. The ${arr[@]+"..."} idiom expands to nothing
# when the array is empty, dodging set -u's "unbound variable" error.
${RUN_PREFIX[@]+"${RUN_PREFIX[@]}"} go test \
    -tags=bench \
    -bench=BenchmarkMetricsRender \
    -benchtime="$BENCHTIME" \
    -count="$COUNT" \
    -run='^$' \
    ./cmd/topology-exporter/ 2>&1 | tee -a "$OUT"

echo >&2
echo "Result file: $OUT" >&2
echo "Paste contents back to update docs/operator/scale.md." >&2
