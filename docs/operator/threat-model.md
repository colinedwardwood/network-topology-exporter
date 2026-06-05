# Threat Model

This document names the security-relevant surfaces of `network-topology-exporter`, the assets each surface protects, the realistic threats against them, and the mitigations the project ships today. It is operator-facing: the goal is to give a deployment engineer enough to decide where this binary fits in their trust model.

For day-to-day operator hardening guidance, see [`security.md`](security.md). To report a vulnerability, see [`../../SECURITY.md`](../../SECURITY.md).

## Scope

This binary is a polling exporter on the network management plane. It:

1. **Polls network devices via SNMP** (v2c and v3), reading inventory and topology MIBs. Targets are configured via `discovery.scope.cidr_allow_list`. Credentials are resolved from `credentials.profiles` and the per-device assignment ladder.
2. **Serves `/metrics` and `/healthz` / `/readyz`** on a listen port. Defaults to plain HTTP on a private network; can be configured for TLS + basic auth + mTLS via the Prometheus exporter-toolkit `listen.web_config_file`.
3. **Optionally accepts federated pushes from spokes** at `/spoke/push` when `federation.role: hub`. Always requires mTLS — plaintext spoke-to-hub is refused at the TLS handshake.
4. **Optionally pushes the topology graph and change events to an OTLP receiver** when `output.otlp.enabled: true`. Outbound HTTPS; TLS verification depends on the configured endpoint.
5. **Persists a snapshot to disk** at `snapshot.path` after every successful cycle so `/metrics` serves the previous graph immediately on restart.
6. **Reads configuration** from a YAML file at `--config.file=` and **secrets** from environment variables (`*_env` fields) — never the secret values inline.

## Assets to protect

| Asset | Where | What's at stake |
|---|---|---|
| **SNMP credentials** | env vars at process start; in-memory cache during cycles; logical mappings only (not values) in snapshot.json | Direct device access; cascading compromise if the same community / SNMPv3 user is reused across the fleet |
| **Topology graph** | in-memory `TopologyCollector`; `/metrics` body; snapshot.json; outbound OTLP payload | Reconnaissance value to an attacker: device IDs, vendors, OS versions, full edge list reveal exploitable targets and attack paths |
| **The binary process itself** | container or systemd unit on the management network | Process memory contains credentials (until zeroed); access to the management network means access to every device on it |
| **Spoke→hub channel** | TLS connection from `federation.spoke` to `federation.hub` | Fabricated topology pushes poison the hub's graph; an unbounded fabricated payload OOM-kills the hub |

## STRIDE matrix

| Threat | Concrete shape against this binary | Mitigation today |
|---|---|---|
| **Spoofing** — device side | An attacker on the management plane impersonates a target IP and returns crafted SNMP responses | SNMPv3 with auth + priv (operator choice); CIDR allow-list bounds blast radius; PDU parser fuzz coverage across every device-controlled decode path (`internal/discovery/*/fuzz_test.go`, per-PR + nightly CI) |
| **Spoofing** — spoke side | An attacker POSTs to `/spoke/push` claiming to be a legitimate spoke | mTLS with CA-signed client cert required (LD-20); hub verifies the certificate CN matches `federation.spoke.spoke_id` in the payload (LD-21); plaintext refused at the handshake |
| **Spoofing** — scraper side | A scraper-impersonator pulls `/metrics` and exfiltrates the topology graph | Default deployment assumes private network. For untrusted networks, configure `listen.web_config_file` with TLS + basic auth or mTLS (D45, security.md § 1) |
| **Tampering** — disk | An attacker modifies `snapshot.json` to inject false topology before exporter restart | Operator-side file permissions (security.md § 4). On next cycle, the live discovery overwrites the snapshot — tampering window is bounded |
| **Tampering** — binary / image | A compromised release artefact gets deployed | Container image and Go binaries are signed (cosign keyless) and carry SLSA build-provenance attestations on every release. Verification commands in [`security.md`](security.md#verifying-release-artefact-provenance) |
| **Tampering** — device PDUs | A compromised or hostile device returns crafted PDUs designed to confuse the exporter | PDU fuzz coverage (16 harnesses, `internal/discovery/*/fuzz_test.go`); reconciliation surfaces protocol-conflict counters rather than silently merging; per-walker outcome counters surface walker-drift |
| **Tampering** — spoke channel | An attacker on the network MITMs the spoke→hub TLS connection | mTLS chain validation on both sides; cert pinning recommended for environments where the operator can pre-stage CA bundles |
| **Repudiation** | An attacker successfully pushes / pulls without an audit trail | Topology change events emit structured JSON to stderr; spoke push accept/reject emits log lines with `spoke_id`, `cycle_at`, reason, and TLS chain summary. Operator must ship logs to a tamper-resistant store — see security.md § "Logging" |
| **Information disclosure** — `/metrics` | An attacker on the listen-port network reads the topology | Default is private-network trust. `listen.web_config_file` enables TLS + auth (D45) |
| **Information disclosure** — credentials in memory | A privileged process reads `/proc/<pid>/mem` and recovers SNMP credentials | `LD-12` credential zeroization on shutdown and after key rotation; drop `CAP_SYS_PTRACE` from the container (security.md § 2) |
| **Information disclosure** — credentials in env | Any process that can read `/proc/<pid>/environ` recovers the env values | Standard Kubernetes / systemd environment isolation; operator responsibility; future NetBox-Secrets integration would reduce env exposure (tracked outside this milestone) |
| **Information disclosure** — snapshot.json | Snapshot containing device-to-profile mappings is read | Mapping is profile *names*, not credential values. Still treat the file as sensitive: docs/operator/security.md § 4 |
| **Information disclosure** — OTLP push | The configured OTLP receiver is captured or impersonated | TLS is selected by the `output.otlp.endpoint` scheme (`https` enables it, `http` disables it); operator must use an `https` endpoint and verify endpoint trust |
| **Denial of service** — device-induced crash | A crafted PDU panics the exporter; restart loop | PDU fuzz coverage is the primary mitigation (every device-controlled decode path is fuzzed in CI on every PR, plus a 10-minute nightly deep run per harness). Defence in depth: `restartPolicy: Always` on the container minimises operator-visible blast radius if a panic still slips through |
| **Denial of service** — fabricated push | A malicious spoke pushes a multi-million-edge payload | `federation.hub.max_graph_edges` / `max_graph_devices` caps; per-spoke push-rate limit on the hub (security.md § 5); validateSpokePayload (LD-25) rejects oversized label values before they hit the registry |
| **Denial of service** — `/metrics` flood | An attacker scrapes `/metrics` at high frequency to drive CPU | Operator-side reverse proxy with rate-limiting if needed; the rendering path is bounded by `network_topology_metrics_render_duration_seconds` and surfaces if it's the bottleneck |
| **Denial of service** — self-DoS on targets | A misconfigured `discovery.parallelism` or per-target PDU rate overwhelms a device's SNMP daemon | `credentials.trial_rate_per_second` limits the auth-failure rate. Per-device steady-state PDU rate limiting shipped in #72: set `discovery.per_target_pdu_rate_per_second` (0 = unlimited) to cap the SNMP request rate per target; observe `network_topology_snmp_rate_limit_wait_seconds` for time spent blocked on the limiter |
| **Elevation of privilege** — container escape | A bug in the binary lets an attacker break out of the container | Project does not require root or special caps. Helm chart runs non-root; distroless base image minimises attack surface; container runtime isolation is the primary defence |
| **Elevation of privilege** — lateral movement | A compromised exporter becomes a foothold for management-network reconnaissance | Network ACLs limit the exporter's outbound reach; this is operator responsibility. The exporter never initiates connections outside `cidr_allow_list` (for SNMP) and the configured `hub_url` / `otlp.endpoint` |

## Trust boundaries

The exporter trusts:

- **The local filesystem** for config, secrets-pointer env vars, snapshot persistence, and TLS material.
- **The OS for process isolation** — credentials in memory rely on `CAP_SYS_PTRACE` being denied on the host and any sibling container.
- **The mTLS CA** configured for `federation.hub.tls_ca_cert`. Anything signed by it is a legitimate spoke.
- **The configured `output.otlp.endpoint`** to handle topology data appropriately.

The exporter does NOT trust:

- **Network device responses.** Hostile or buggy bytes must not crash the parser; semantic correctness is enforced by per-walker reconciliation.
- **Spoke payloads** beyond what mTLS proves about origin. Payload validation (LD-25) bounds label values, edge counts, and device counts.
- **The unauthenticated network** between scraper and `/metrics` unless `listen.web_config_file` is set.

## Out of scope

Things this binary deliberately does *not* defend against — they belong to the operating environment:

- **Compromised hosts.** Root on the exporter host can read process memory, files, env vars, and stdin/stdout. No process-level defence can survive this.
- **Compromised supply chain.** SLSA + cosign close most of the build-supply-chain gap (issue tracked); operator is still responsible for verifying signatures and provenance at deploy time.
- **Network device-side compromise.** A compromised router can lie about its topology; the exporter will faithfully report what it sees. Cross-source reconciliation (LD-10) and the `network_topology_conflict_total` counter surface divergence between sources for the same physical link, but cannot detect coordinated multi-source lies.
- **Operator-side credential management.** Env-var resolution, NetBox-Secrets, Vault, KMS — the binary supports env-var indirection only today. Other backends are the operator's bridge.
- **Time skew.** mTLS cert validity and `cycle_at` validation both depend on accurate host clocks. NTP is operator responsibility.

## Reporting vulnerabilities

To report a vulnerability privately, see [`SECURITY.md`](../../SECURITY.md) for the full policy, response SLA, and credit/disclosure terms. Use GitHub Security Advisories (`Security` → `Report a vulnerability` on the repo). Do not file public issues for unpatched vulnerabilities.

## Related documents

- [`security.md`](security.md) — operator hardening checklist
- [`federation.md`](federation.md) — mTLS PKI setup and rotation procedures
- [`../architecture.md`](../architecture.md) — system design and trust assumptions
- [`../audits/2026-05-architectural-review.md`](../audits/2026-05-architectural-review.md) — May 2026 architectural audit findings
