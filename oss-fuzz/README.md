# OSS-Fuzz integration (staging copy)

This directory is a **staging copy** of the OSS-Fuzz project integration for
`network-topology-exporter`. The files here are intended to be submitted
*verbatim* to Google's [OSS-Fuzz](https://github.com/google/oss-fuzz)
repository under:

```
projects/network-topology-exporter/
```

Once accepted, OSS-Fuzz continuously builds and runs this project's native Go
fuzz harnesses ([`testing.F`](https://pkg.go.dev/testing#F)) and reports any
crashes back to the maintainers.

Keeping a copy in-repo lets us version the integration alongside the code,
review changes in normal PRs, and re-test locally before pushing updates
upstream.

> **Note:** These files target the OSS-Fuzz build environment. They reference
> `compile_native_go_fuzzer`, `$SRC`, and the `base-builder-go` base image,
> which only exist inside OSS-Fuzz's Docker images. They are **not** executed
> by this repository's CI.

## Files

| File           | Purpose                                                              |
| -------------- | ------------------------------------------------------------------- |
| `project.yaml` | OSS-Fuzz project metadata (language, contacts, engines, sanitizers).|
| `Dockerfile`   | Clones this repo into the `base-builder-go` image.                  |
| `build.sh`     | Auto-discovers and compiles every fuzz target.                      |

## Before submitting — fill the placeholders

`project.yaml` contains `REPLACE_WITH_MAINTAINER_EMAIL` placeholders. Replace
**all** of them before submitting:

- `primary_contact` — the maintainer email that OSS-Fuzz associates with the
  project. It must be tied to a Google account so the contact can access the
  OSS-Fuzz dashboard and ClusterFuzz crash reports.
- `auto_ccs` — additional addresses CC'd on bug reports (optional; remove the
  list if not needed).

Do not commit a real email to this staging copy unless you intend it to be
public — OSS-Fuzz's `projects/` tree is a public repository.

## Submitting to google/oss-fuzz

1. Fork [`google/oss-fuzz`](https://github.com/google/oss-fuzz).
2. Create `projects/network-topology-exporter/` in your fork.
3. Copy these three files into it (with placeholders filled in):
   - `project.yaml`
   - `Dockerfile`
   - `build.sh`
4. Open a PR against `google/oss-fuzz` from your fork. The OSS-Fuzz team
   reviews new project applications; the `primary_contact` must be a real,
   reachable maintainer.

See the OSS-Fuzz
[new project guide](https://google.github.io/oss-fuzz/getting-started/new-project-guide/)
and its [Go-specific notes](https://google.github.io/oss-fuzz/getting-started/new-project-guide/go-lang/)
for the authoritative process.

## Testing the integration locally

Clone OSS-Fuzz and drive the build with `infra/helper.py`. From a checkout of
`google/oss-fuzz` that contains your `projects/network-topology-exporter/`
directory:

```bash
# Build the fuzzers with the address sanitizer.
python infra/helper.py build_fuzzers --sanitizer address network-topology-exporter

# Sanity-check that the built fuzzers are valid.
python infra/helper.py check_build network-topology-exporter

# Run a single fuzzer (output names follow the <pkg>_<FuzzFuncName> convention).
python infra/helper.py run_fuzzer network-topology-exporter bgp_FuzzSplitOIDParts
```

`build_fuzzers` builds from your local OSS-Fuzz checkout but pulls the project
source by `git clone` (see `Dockerfile`), so make sure the code you want to
fuzz is pushed to the `main_repo`.

## Auto-discovery convention (build.sh)

`build.sh` does **not** hardcode a list of fuzz targets. Instead it scans:

```
internal/discovery/<pkg>/*_test.go
```

for any `func Fuzz*(f *testing.F)` declaration and registers each one with
`compile_native_go_fuzzer`. The output binary for each target is named
`<pkg>_<FuzzFuncName>` so targets from different packages never collide.

**To add a new fuzzer to OSS-Fuzz:** add a `func FuzzXxx(f *testing.F)` to any
package under `internal/discovery/`. No edit to `build.sh` is needed — it will
be picked up automatically on the next build.

At the time of writing, the convention discovers the following targets:

| Package | Fuzz function                  | OSS-Fuzz binary                       |
| ------- | ------------------------------ | ------------------------------------- |
| bgp     | FuzzDecodeCiscoCbgpPeer2Index  | bgp_FuzzDecodeCiscoCbgpPeer2Index     |
| bgp     | FuzzDecodeAristaBgp4v2Index    | bgp_FuzzDecodeAristaBgp4v2Index       |
| bgp     | FuzzDecodeBgp4v2InstanceIndex  | bgp_FuzzDecodeBgp4v2InstanceIndex     |
| bgp     | FuzzSplitOIDParts              | bgp_FuzzSplitOIDParts                 |
| bgp     | FuzzReadInetAddrAt             | bgp_FuzzReadInetAddrAt                |
| fdb     | FuzzParseQBridgeIndex          | fdb_FuzzParseQBridgeIndex             |
| lldp    | FuzzDecodePortID               | lldp_FuzzDecodePortID                 |
| lldp    | FuzzDecodeChassisID            | lldp_FuzzDecodeChassisID              |
| mpls    | FuzzParseTunnelSuffix          | mpls_FuzzParseTunnelSuffix            |
| mpls    | FuzzParseIPFromParts           | mpls_FuzzParseIPFromParts             |
| ospf    | FuzzParseNbrOID                | ospf_FuzzParseNbrOID                  |
| snmp    | FuzzPDUString                  | snmp_FuzzPDUString                    |
| snmp    | FuzzPDUBytes                   | snmp_FuzzPDUBytes                     |
| snmp    | FuzzPDUInt                     | snmp_FuzzPDUInt                       |
| snmp    | FuzzPDUIntStrict               | snmp_FuzzPDUIntStrict                 |
| snmp    | FuzzPDUIPv4                    | snmp_FuzzPDUIPv4                      |
