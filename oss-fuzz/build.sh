#!/bin/bash -eu
# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#      http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
#
################################################################################
#
# OSS-Fuzz build script for network-topology-exporter.
#
# AUTO-DISCOVERY CONVENTION
# -------------------------
# Every native Go fuzz harness in this project lives in a `*_test.go` file
# (conventionally `fuzz_test.go`) inside a package under:
#
#     internal/discovery/<pkg>/
#
# and is declared as `func FuzzXxx(f *testing.F)`. This script scans those
# packages, finds every `func Fuzz*` target, and registers it with
# `compile_native_go_fuzzer` automatically. To add a new fuzzer to OSS-Fuzz,
# just add a `func FuzzXxx(f *testing.F)` to a discovery package -- no edit to
# this file is required.
#
# Each fuzzer's output binary is named `<pkg>_<FuzzFuncName>` so binaries from
# different packages never collide (e.g. two packages could both expose a
# `FuzzDecode`).

# Resolve the module path from go.mod so import paths are always correct,
# independent of where the source is checked out.
MODULE_PATH="$(go list -m)"

for fuzz_file in internal/discovery/*/*_test.go; do
  [ -e "$fuzz_file" ] || continue

  # Only files that actually declare a Fuzz target are of interest.
  grep -qE '^func Fuzz[A-Za-z0-9_]+\(' "$fuzz_file" || continue

  pkg_dir="$(dirname "$fuzz_file")"          # e.g. internal/discovery/bgp
  pkg_name="$(basename "$pkg_dir")"          # e.g. bgp
  import_path="${MODULE_PATH}/${pkg_dir}"    # full Go import path

  # Extract each Fuzz function name declared in this file.
  for fuzz_func in $(grep -oE '^func Fuzz[A-Za-z0-9_]+' "$fuzz_file" | awk '{print $2}'); do
    out_name="${pkg_name}_${fuzz_func}"
    echo "Registering fuzzer: ${import_path} ${fuzz_func} -> ${out_name}"
    compile_native_go_fuzzer "${import_path}" "${fuzz_func}" "${out_name}"
  done
done
