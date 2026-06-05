# Security: credential handling

SNMP credentials — SNMPv2c community strings and SNMPv3 auth/priv keys — live in process memory for at least as long as the exporter is running a discovery cycle against a device. This document covers the in-memory credential threat model, the mitigations the exporter implements, what those mitigations cannot do, and the operator-level controls that fill the gaps.

## Threat model: in-memory credential exposure

The exporter resolves credentials from environment variables at the start of every discovery cycle, holds them in memory while talking to each device, and discards them at the end of the cycle. While they are in memory, a process-memory disclosure event leaks every credential the exporter has touched. The realistic ways that happens:

1. **Core dump.** A panic or `SIGQUIT` that hits a kernel with `kernel.core_pattern` configured to write a dump file leaves the full address space on disk. Anyone with read access to that file recovers every credential the exporter held at the moment of the dump.
2. **Container memory snapshot.** Most container runtimes can snapshot a running container's memory (CRIU, `docker checkpoint`, Kubernetes `kubectl debug --copy-to`). The snapshot contains the same data a core dump would, and is usually written to a location with looser permissions than the container itself.
3. **`/proc/<pid>/mem` read.** A privileged process or a sibling container with `CAP_SYS_PTRACE` can read the exporter's memory directly without leaving a forensic trace.
4. **Memory-image forensics on host compromise.** If the host is compromised, the attacker can dump physical memory from the hypervisor or kernel and grep for credentials in the exporter's pages.

None of these require the attacker to break SNMP, the network, or the exporter's TLS configuration. They require only enough access to read the exporter's memory.

## Mitigation: zeroization

The exporter stores SNMP credential fields as `[]byte` in `internal/discovery/snmp.Params` rather than as Go `string`. Go strings are immutable and the runtime cannot guarantee a string's backing memory will be released or overwritten before the next GC cycle. A `[]byte` is mutable: the code can call `Zeroize()` to overwrite the backing memory with zeros and drop the reference.

`Zeroize()` runs at two points:

1. **End of every per-device walk.** The per-device goroutine in `RunCycle` (`internal/app/cycle.go`) wraps the resolved `Params` in a `defer params.Zeroize()` immediately after the SNMP system-group walk succeeds. When the goroutine returns — successful walk, failed walk, panic recovery, context cancellation — the credentials are overwritten before the goroutine exits.
2. **Discovery-cycle failures.** `WalkSystemWithCredentials` (`internal/app/probe.go`) tries each candidate credential in turn and zeroizes the bytes of every candidate as soon as that candidate is either confirmed not the winner or the calling context is cancelled. The winning candidate's bytes survive only until the per-device defer runs.

On `SIGTERM`/`SIGINT`, `signal.NotifyContext` cancels the discovery loop's context. The in-flight cycle's per-device goroutines see `cycleCtx.Done()`, unwind, and their `defer params.Zeroize()` fires before `RunCycle` returns. The discovery loop logs `snmp credentials zeroized; shutting down discovery loop` so the zeroization is visible in shutdown logs.

The credential resolver (`internal/credentials.Resolver`) does not hold credential bytes between cycles. Its per-device cache stores only the *profile name* that previously authenticated for a given IP. Actual credential bytes are pulled fresh from `os.Getenv` at the start of every cycle, which means there is nothing to zeroize at the resolver level on shutdown.

## What zeroization does not do

Zeroization is best-effort. Several gaps are inherent to running on a managed-memory runtime:

1. **Go GC may have copied the bytes elsewhere.** When a `[]byte` is appended to, passed across a goroutine boundary, or its containing struct escapes to the heap, the runtime may copy the underlying bytes. `Zeroize()` overwrites the slice the code holds a reference to, not any copy the GC has already made. The original copy gets collected eventually, but the timing is not under the exporter's control.
2. **Conversion to `string` makes an immutable copy.** The upstream `github.com/gosnmp/gosnmp` library exposes `Community`, `AuthenticationPassphrase`, and `PrivacyPassphrase` as Go `string` fields. The exporter passes credentials in via `string(p.Community)` at the gosnmp boundary in `buildClient`. That conversion allocates a fresh string whose bytes Zeroize cannot reach. The string survives for the lifetime of the gosnmp session (one device walk). It then becomes unreachable, and is reclaimable, when the gosnmp client is garbage-collected. The exporter does not retain the gosnmp session after a walk.
3. **Environment variables are out of scope.** The credentials originate from `os.Getenv`. The Go standard library backs `os.Getenv` with a process-wide environment map that the exporter cannot reach into and overwrite. Anyone with `/proc/<pid>/environ` access reads credentials directly from there, bypassing the exporter's in-memory copies entirely.
4. **Not constant-time.** `Zeroize()` uses a simple `for i := range b { b[i] = 0 }` loop. Timing side channels on the zeroization operation are not in this threat model. (Constant-time matters when an attacker can observe the timing of credential *comparison*; this code only overwrites.)

## Operator guidance

Software mitigations only go so far. The following operator-level controls cover the gaps above:

- **Restrict who can read process memory.** On the container runtime, drop `CAP_SYS_PTRACE` from the exporter's container and from any sibling container that runs in the same pod. On a bare-metal host, configure `kernel.yama.ptrace_scope=1` (or stricter) so non-privileged users cannot attach to the exporter.
- **Restrict `/proc` visibility.** Mount `/proc` with `hidepid=2` so only the exporter's own UID can see its `/proc/<pid>` entries. In Kubernetes, set `securityContext.procMount: Unmasked` only when absolutely necessary; the default `DefaultProcMount` hides most of `/proc` from sibling containers.
- **Disable or encrypt core dumps.** Set `ulimit -c 0` for the exporter process, or — if you need crash dumps — encrypt them at the file-system layer. Disable `kernel.core_pattern` redirects that pipe cores to network destinations.
- **Restrict environment variable visibility.** `/proc/<pid>/environ` is readable by the exporter's own UID by default. If you use a process-spawning supervisor (systemd, container runtime), arrange for the supervisor to clear the env after passing it to the exporter, or use a credential-file mechanism the supervisor pipes in over a socket.
- **Rotate SNMP credentials on a schedule.** The exporter has no rotation mechanism yet; rotation is a separate workstream beyond this issue. Until that lands, plan periodic credential rotation as part of your normal SNMP credential lifecycle — once per quarter is a reasonable baseline for read-only credentials. Restart the exporter after rotating so it picks up the new values from the environment.
- **Audit who has access to memory snapshots and core-dump locations.** If your platform writes them to S3, NFS, or a sidecar volume, treat that storage as if it contained the credentials in plaintext — because it does.

## Out of scope

This document covers in-memory credential exposure. The following are intentionally out of scope:

- **Credential rotation.** Tracked as a follow-up workstream. The current implementation re-reads `os.Getenv` at every cycle, so an operator can already approximate rotation by updating the environment and restarting the exporter, but the exporter does not detect or coordinate the rotation itself.
- **Cryptographic key wrapping or HSM integration.** Storing credentials in a hardware security module or wrapping them with an in-process key would protect against the runtime-memory exposure vector, but the cost (operational complexity, dependency footprint, deployment surface) is not justified for the threat profile of an internal-network monitoring exporter. Operators who need HSM-backed credentials should use a sidecar to fetch credentials and pass them in via environment or file.
- **Network-side SNMP credential exposure.** SNMPv2c sends the community in plaintext on the wire. Use SNMPv3 (`v3` profile type) with `authPriv` security level for any device that handles credentials you care about over an untrusted network segment. The exporter validates that the requested `auth_protocol` and `priv_protocol` are non-empty for `authPriv` profiles; it does not, by design, support `noAuthNoPriv` v3 sessions for production use.
- **TLS-handshake-time secrets.** This document is about SNMP credentials. Federation mTLS keys, OTLP exporter credentials, and HTTP server TLS keys are handled by their respective standard-library packages and live in their own memory regions; the threat model is the same but the mitigation surface is different.

## Securing the `/metrics` endpoint

The exporter publishes its full topology graph — device IDs, vendor, OS version, port names, edge list — through `/metrics`. That is reconnaissance data: anyone who can scrape `/metrics` learns the shape of your network and can pivot to CVE matching on the vendor/OS-version labels.

By default the listener binds plain HTTP on `:9100`. This is the canonical Prometheus convention ("scrape from a private network"), and the upstream Prometheus security model treats the exporter's HTTP surface as not-internet-exposed. If your deployment matches that assumption — for example, a control-plane subnet with no ingress from user-facing networks, or a Kubernetes namespace with `NetworkPolicy` restricting ingress to the Prometheus pod — no further configuration is required.

If `/metrics` reaches a network where the trusted-network assumption does not hold, configure `listen.web_config_file` to point at a Prometheus [exporter-toolkit web-config](https://github.com/prometheus/exporter-toolkit/blob/master/docs/web-configuration.md) YAML. The toolkit is the same code path snmp_exporter, node_exporter, and blackbox_exporter use, and the YAML schema is shared across the ecosystem — operators already know it.

Three deployment patterns:

**1. Push outbound (recommended for Grafana Cloud).** Run [Grafana Alloy](https://grafana.com/docs/alloy/) or `prometheus-agent` inside the private network alongside the exporter; configure Alloy's `prometheus.scrape` to talk to the exporter on a private address and `prometheus.remote_write` to push outbound to Grafana Cloud Mimir using a Cloud-issued token. No inbound auth required on the exporter — its `/metrics` surface stays on the private network.

**2. Basic auth + server TLS.** For internal Prometheus servers that need to reach a `/metrics` endpoint across a network boundary you do not fully control:

```yaml
# listen.web_config_file → web-config.yml
tls_server_config:
  cert_file: /etc/topology-exporter/cert.pem
  key_file: /etc/topology-exporter/key.pem
basic_auth_users:
  scraper: $2y$10$...bcrypt-hash...
```

Generate the hash with `htpasswd -bnBC 10 "" your-password | tr -d ':\n'`. Configure the Prometheus / Alloy side with `basic_auth.username: scraper` and `basic_auth.password_file: /path/to/secret`.

**3. mTLS (strongest, lowest user-friction once PKI is in place).** If you already operate a federation hub-and-spoke deployment, you have a CA — reuse it:

```yaml
# listen.web_config_file → web-config.yml
tls_server_config:
  cert_file: /etc/topology-exporter/cert.pem
  key_file: /etc/topology-exporter/key.pem
  client_auth_type: RequireAndVerifyClientCert
  client_ca_file: /etc/topology-exporter/ca.pem
  client_allowed_sans: ["prometheus.monitoring.internal"]
```

`client_allowed_sans` pins which scraper identities are accepted — a leaked cert with an unexpected SAN is rejected at the TLS layer before the request body is parsed. The Prometheus exporter-toolkit handles cert reload-on-change automatically, so SAN rotation does not require an exporter restart.

The `listen.tls_cert_file` and `listen.tls_key_file` fields were removed in v1.5.0. Migrate to `listen.web_config_file` (set `tls_server_config.cert_file` / `key_file` to the same paths). Configs using the removed keys will fail to load with a parse error.

## Verifying release artefact provenance

Every release artefact (Go binaries + multi-arch container image) is signed via [cosign](https://github.com/sigstore/cosign) keyless signing and carries a [SLSA build provenance attestation](https://slsa.dev/spec/v1.0/provenance) produced by `actions/attest-build-provenance`. Verifying these before deployment confirms the artefact came from this repository's release workflow and was not tampered with after the fact.

### Container image

```bash
# Verify the keyless cosign signature
cosign verify \
  --certificate-identity-regexp '^https://github.com/colinedwardwood/network-topology-exporter/.github/workflows/' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
  ghcr.io/colinedwardwood/network-topology-exporter:VERSION

# Verify the SLSA provenance attestation
gh attestation verify \
  oci://ghcr.io/colinedwardwood/network-topology-exporter:VERSION \
  --owner colinedwardwood
```

Replace `VERSION` with the tag you are deploying (e.g. `v1.3.0`).

### Binary (downloaded from a GitHub Release)

```bash
# Download the artefacts
curl -LO https://github.com/colinedwardwood/network-topology-exporter/releases/download/VERSION/topology-exporter-linux-amd64
curl -LO https://github.com/colinedwardwood/network-topology-exporter/releases/download/VERSION/topology-exporter-linux-amd64.sig
curl -LO https://github.com/colinedwardwood/network-topology-exporter/releases/download/VERSION/topology-exporter-linux-amd64.cert

# Verify the keyless cosign signature
cosign verify-blob \
  --signature topology-exporter-linux-amd64.sig \
  --certificate topology-exporter-linux-amd64.cert \
  --certificate-identity-regexp '^https://github.com/colinedwardwood/network-topology-exporter/.github/workflows/' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
  topology-exporter-linux-amd64

# Verify the SLSA provenance attestation
gh attestation verify topology-exporter-linux-amd64 --owner colinedwardwood
```

If either verification fails the artefact is not authentic — do not deploy it. Open a security advisory via GitHub Security Advisories on this repository before proceeding.

## References

- [`SECURITY.md`](../../SECURITY.md) — vulnerability reporting policy, response SLA, credit/disclosure terms.
- Issue #5 — the GitHub issue that motivated the SNMP zeroization work.
- Issue #3 — the GitHub issue that motivated the `/metrics` authentication work.
- `internal/discovery/snmp/zeroize.go` — the `Zeroize()` implementation and `zeroBytes` helper.
- `internal/discovery/snmp/snmp.go` — the `Params` struct that holds credentials.
- `internal/app/probe.go` — `WalkSystemWithCredentials` (candidate-trial zeroization).
- `internal/app/cycle.go` — `RunCycle` (per-device `defer params.Zeroize()`).
- `internal/app/app.go` — the HTTP listener / `web.ListenAndServe` integration. (`cmd/topology-exporter/main.go` is now a ~20-line shim that calls into `internal/app`.)
- [Prometheus exporter-toolkit web-configuration.md](https://github.com/prometheus/exporter-toolkit/blob/master/docs/web-configuration.md) — full schema reference for `web_config_file`.
- [SLSA v1.0 provenance spec](https://slsa.dev/spec/v1.0/provenance) and [cosign documentation](https://docs.sigstore.dev/cosign/).
- [`threat-model.md`](threat-model.md) — STRIDE matrix and asset-level analysis.
