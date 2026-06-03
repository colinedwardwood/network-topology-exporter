# Upgrade Runbook

This runbook covers upgrading `network-topology-exporter` between minor releases. Each section names the breaking changes for that release, what to back up, how to migrate your config, and the recommended rollout order for hub/spoke fleets.

**Pre-release caveat.** Despite the `v1.x` tags, the project follows pre-1.0 stability conventions until the surfaces listed in [`docs/operator/stability.md`](stability.md) are frozen. Every minor release may carry breaking changes. Pin exact versions, and read this document before upgrading.

Cross-references: [troubleshooting.md](troubleshooting.md) § 8 (snapshot issues after upgrade), [federation.md](federation.md) § Hub HA patterns.

---

## General procedure

For any minor-version upgrade:

1. Back up the on-disk snapshot and the credential cache (see [What to back up](#what-to-back-up)).
2. Read the version-specific section below for breaking changes.
3. Migrate your config as described.
4. Roll out in the order described (for hub/spoke fleets).
5. Confirm health with the verification steps at the end of each section.

---

## What to back up

### Snapshot file

The snapshot is a versioned JSON file at the path configured under `snapshot.path` (default: `/var/lib/network-topology-exporter/snapshot.json`). It holds the complete reconciled graph from the last successful discovery cycle and is loaded on startup so `/metrics` can serve stale-but-valid data while the first live cycle runs.

```sh
cp /var/lib/network-topology-exporter/snapshot.json \
   /var/lib/network-topology-exporter/snapshot.json.$(date +%Y%m%d%H%M%S).bak
```

If the snapshot is on a shared path (NFS, PVC), the backup location must be on a different volume than the live file to survive a storage failure.

**After a version upgrade**, a schema-version mismatch causes the exporter to quarantine the old snapshot (renamed to `.bad`) and cold-start. The topology is rebuilt from live discovery within one `discovery.interval`. No operator action is required unless the `.bad` file consumes disk space that must be reclaimed.

### Credential cache

The credential cache is embedded in the snapshot file — there is no separate credential-cache file. The snapshot backup above covers both the graph and the credential cache. If you do not have a snapshot backup and the snapshot is lost or incompatible, the exporter cold-starts and re-proves credentials against every device using the `credentials.trial_rate_per_second` limiter. On a fresh cold start, discovery still works; it just takes one full cycle with credential probing before the graph is live.

### Config file

Back up your `config.yaml` before making migration changes. The exporter validates config at startup using `KnownFields` (since v1.5.0, unknown config keys cause a startup parse error rather than a silent no-op — see below).

---

## Rollout order for hub/spoke fleets

There is **no version negotiation** in the federation protocol. The hub and spokes must run the same major+minor version in steady state. The push payload schema, mTLS certificate format, and reject-reason enum values are not versioned across minor releases — a spoke running v1.3 pushing to a hub running v1.5 may be rejected or silently mishandled if the schema changed.

**Recommended order:**

1. Upgrade the **hub first**. The hub is the passive receiver; an upgraded hub that still accepts the older push format (which is the case for non-schema-breaking upgrades) will process older spoke payloads correctly while spokes are rolling. Check `network_topology_federation_spoke_up{spoke_id="..."}` — all spokes should remain at 1 during the hub upgrade.
2. Upgrade **spokes one at a time**. For each spoke: upgrade, restart, wait for at least one successful push (one `discovery.interval`), confirm `network_topology_federation_spoke_up{spoke_id="<id>"}` returns to 1 before moving to the next spoke.
3. After all spokes are upgraded, verify the hub's unified graph: check edge count stability and `network_topology_graph_stale == 0`.

**Mixed-version operation mid-upgrade** is not supported and not tested. Keep the rolling window short — upgrade all spokes within one `spoke_timeout` window. A spoke that is mid-upgrade and not pushing will be evicted by the hub after `spoke_timeout`; its graph contribution disappears from hub metrics until the upgraded spoke starts pushing again.

---

## v1.5.0

### Summary

Config-schema cleanup release. Three deprecated keys (carried with startup warnings since v1.3.0) are now hard errors. The OTLP output path migrated from a hand-rolled JSON encoder to the official OpenTelemetry Go SDK, changing the wire format from JSON to protobuf. The config loader now rejects unknown keys (`KnownFields`).

### Breaking changes

**Config key removals (startup parse error if present):**

| Removed key | Replacement | Behaviour when present |
|---|---|---|
| `modules.bgp.use_v2_mib` | `modules.bgp.disable_v2_mib` | Config fails to load |
| `federation.hub.strict_device_name_matching` | `federation.hub.loose_device_name_matching` | Config fails to load |
| `listen.tls_cert_file` | `listen.web_config_file` | Config fails to load |
| `listen.tls_key_file` | `listen.web_config_file` | Config fails to load |

**Unknown config keys are now parse errors.** The loader uses `KnownFields`; a key in your YAML that the exporter does not recognise causes startup to fail with a parse error rather than being silently ignored. This catches typos and stale keys left from previous migrations.

**OTLP wire format: JSON → protobuf.** The pre-v1.5.0 hand-rolled output path emitted OTLP/HTTP+JSON. The SDK-based path emits protobuf (the OTLP default). If you have an OTLP receiver that only accepts JSON, it will reject pushes after this upgrade. Virtually all production receivers (Grafana Alloy, OTel Collector, Grafana Cloud, Tempo, Mimir) accept protobuf. There is no JSON option in the SDK exporters.

**OTLP encoding key dropped.** If you were setting `output.otlp.encoding` in your config (e.g., `output.otlp.encoding: json`), that key no longer exists and will cause a startup parse error under the new `KnownFields` loader. Remove it.

**New OTLP key: `output.otlp.protocol`.** Optional; `http` or `grpc`, default `http`. If you were relying on the default HTTP transport, nothing changes. If you want gRPC, set `output.otlp.protocol: grpc` and point `output.otlp.endpoint` at a bare `host:port` authority rather than a full URL.

### Config migration steps

1. **Remove deprecated keys.** Search your config for `use_v2_mib`, `strict_device_name_matching`, `tls_cert_file`, `tls_key_file`, and `encoding`. Remove or replace them.

2. **Migrate TLS config to `listen.web_config_file`.** Create a Prometheus exporter-toolkit web-config YAML at a path the exporter can read:

   ```yaml
   # /etc/topology-exporter/web-config.yaml
   tls_server_config:
     cert_file: /etc/topology-exporter/pki/server.crt
     key_file:  /etc/topology-exporter/pki/server.key
   ```

   Point the exporter at it:

   ```yaml
   listen:
     web_config_file: /etc/topology-exporter/web-config.yaml
   ```

   The exporter-toolkit web-config supports the same cert/key paths you had in `tls_cert_file`/`tls_key_file`, and additionally allows `basic_auth` and full mTLS (`client_auth_type: RequireAndVerifyClientCert`). See [`docs/operator/security.md`](../operator/security.md) for the full options.

3. **Migrate BGP config key.** Replace `modules.bgp.use_v2_mib: true` with nothing (the default `disable_v2_mib: false` enables v2 walkers). Replace `modules.bgp.use_v2_mib: false` with `modules.bgp.disable_v2_mib: true`.

4. **Migrate federation hub matching key.** Replace `federation.hub.strict_device_name_matching: false` with `federation.hub.loose_device_name_matching: true`. Replace `strict_device_name_matching: true` (or omit it) with nothing — `loose_device_name_matching: false` is the new default and the behaviour is unchanged.

5. **Remove any unknown keys.** Run the exporter with `--config.file=<your-config>` in a test environment to surface unknown-key parse errors before production rollout.

### Rollout order

Hub first, then spokes one at a time (see [general rollout order](#rollout-order-for-hubspoke-fleets) above). The config migration must be done on each node before restarting with the new binary. There are no schema changes in the federation push payload for this release; a briefly mixed-version fleet during the rolling upgrade will not corrupt hub state.

### Verification

```sh
# Exporter started without config parse errors
journalctl -u network-topology-exporter --since -5m | grep -i "error\|warn\|config"

# Graph is live (not stale)
curl -s http://localhost:9100/metrics | grep network_topology_graph_stale

# Snapshot is being written
curl -s http://localhost:9100/metrics | grep network_topology_snapshot_last_written_timestamp_seconds

# OTLP push succeeding (if enabled)
curl -s http://localhost:9100/metrics | grep network_topology_otlp_push_total
```

---

## v1.4.0-rc.1 (Lab Fixture Capture)

### Summary

Real-device BGP walker validation for Cisco IOS-XE (cross-confirmation) and lab fixture capture tooling. The Juniper and Nokia BGP4-V2 walkers remain experimental. No breaking config changes. The admin re-discovery endpoint (`POST /admin/rediscover`) was added. The AGPL-3.0 relicensing lands in this release.

### Breaking changes

No breaking config changes.

**`network_topology_bgp_walker_outcome_total` label changes** (carried over from v1.3.0 but worth confirming):
- `walker="v2_draft"` is gone. Alerts on this label produce no data.
- `outcome="empty"` is gone. Alerts on it produce no data.
- Use `walker="vendor_arista"` for Arista BGP coverage.
- Use `outcome="no_peers"` for "BGP configured but sessions down" and `outcome="mib_unimplemented"` for "device does not implement BGP MIB at all".

**`network_topology_graph_updates_rejected_total`** now carries a `reason` label (`size_budget_exceeded`, `invalid_label_key`, `invalid_label_value`). Pre-existing dashboards querying the unlabelled form will see no data after upgrade; migrate to `sum by (reason)(...)` or select a specific reason.

**AGPL-3.0 relicensing.** If you are running a modified version of the exporter and offering it over a network, AGPL §13 requires you to make the modified source available to users of that network service.

### Config migration steps

No config migration required if upgrading from v1.3.0. The deprecated v1.3.0 keys (`use_v2_mib`, `strict_device_name_matching`, `tls_cert_file`, `tls_key_file`) are still accepted with startup warnings in this release; they become parse errors in v1.5.0.

If you have a v1.3.0 deployment with deprecated keys, this release is a safe intermediate to confirm the new key names before the v1.5.0 hard removal.

### Rollout order

Hub first, then spokes. The federation push schema is unchanged; mixed-version operation during the rolling upgrade is safe for the duration of one `spoke_timeout`.

### Verification

```sh
# No unexpected config warnings at startup
journalctl -u network-topology-exporter --since -5m | grep -E "deprecated|warn"

# BGP walker outcomes are using new label values
curl -s http://localhost:9100/metrics | grep network_topology_bgp_walker_outcome_total
```

---

## v1.3.0

### Summary

Post-adversarial-review hardening. 25 changes covering SNMP credential zeroization, `/metrics` authentication via the Prometheus exporter-toolkit, BGP walker structural rewrite informed by real-device captures, partitioned status counters, typed reject reasons and Edge enums, and rate-limited chronic warn logs.

### Breaking changes

**`modules.bgp.use_v2_mib` and `federation.hub.strict_device_name_matching` renamed** (deprecated, not yet removed — removed in v1.5.0). The new keys are `modules.bgp.disable_v2_mib` and `federation.hub.loose_device_name_matching`. The logic is inverted for both: `disable_v2_mib: false` (default) enables v2 walkers; `loose_device_name_matching: false` (default) means strict matching. Default runtime behaviour is unchanged.

**`modules.arp.enabled` now defaults to `false`.** If you relied on ARP enrichment without explicitly setting it, add `modules.arp.enabled: true` to your config. ARP enrichment is only useful when `modules.fdb.enabled: true`; a startup warning fires when FDB is enabled without ARP.

**`federation.hub.strict_device_name_matching` now defaults to `true` (strict).** If you were relying on FQDN-suffix stripping for cross-site device reconciliation, you must set `loose_device_name_matching: true` explicitly. Single-site deployments with mixed short/FQDN forms of the same device are the intended use case for `loose_device_name_matching: true`.

**`listen.web_config_file` added; `listen.tls_cert_file`/`tls_key_file` deprecated** (deprecated, removed in v1.5.0). The `/metrics` endpoint now supports authentication via the Prometheus exporter-toolkit. No immediate action required, but migrate before v1.5.0.

**Metric label changes:**

| Metric | Before | After |
|---|---|---|
| `network_topology_bgp_walker_outcome_total` | `outcome="empty"` | Split into `outcome="no_peers"` and `outcome="mib_unimplemented"` |
| `network_topology_bgp_walker_outcome_total` | `walker="v2_draft"` | Removed; use `walker="vendor_arista"` for Arista, `walker="rfc4273"` for fallback |
| `network_topology_bgp_walker_outcome_total` | `outcome="no_peers"` (covered all-rows-malformed) | `outcome="walker_drift"` for all-rows-malformed; `no_peers` now means BGP is configured but sessions are down |
| `network_topology_otlp_push_total` | `{status}` | Widened to `{status, reason}` |
| `network_topology_discovery_devices_total` | `{status}` | Widened to `{status, reason}` |
| `network_topology_snmp_walks_total` | `{status}` | Widened to `{status, reason}` |
| `network_topology_graph_updates_rejected_total` | No `reason` label | `reason` label added |
| `network_topology_discovery_devices_total` | `network_topology_discovery_devices` | Renamed to `network_topology_discovery_devices_total` |

Prometheus treats `foo{status="error"}` and `foo{status="error", reason="timeout"}` as different series. Historical data does not flow forward after the label widening — the partitioned series starts at the upgrade boundary.

**`network_topology_graph_updates_rejected_total{reason="invalid_label_value"}` used to mislabel structural failures.** They are now `reason="structural_invalid"`. Alerts on `reason="invalid_label_value"` for all structural failures must add `reason="structural_invalid"`.

### Config migration steps

1. If you have `modules.arp.enabled` absent from your config and rely on ARP enrichment, add `modules.arp.enabled: true`.
2. If you have `strict_device_name_matching: false` for FQDN reconciliation, add `loose_device_name_matching: true` (the old key continues to work with a deprecation warning).
3. Update dashboard and alert queries for any metric whose label set was widened (see table above). The common safe migration is `sum by (status)(...)` to aggregate over the new `reason` dimension.

### Rollout order

Hub first, then spokes. The federation push schema itself is not changed in v1.3.0. The label changes only affect Prometheus metrics; they do not affect the push payload between spoke and hub.

### Verification

```sh
# Confirm no deprecated-key warnings
journalctl -u network-topology-exporter --since -5m | grep -i deprecated

# Confirm new metric label shapes are present
curl -s http://localhost:9100/metrics | grep -E "network_topology_otlp_push_total|network_topology_discovery_devices_total"

# Confirm ARP enrichment is as expected (look for arp in module duration)
curl -s http://localhost:9100/metrics | grep network_topology_discovery_module_duration_seconds
```

---

## v1.2.0

### Summary

Reliability and observability hardening. OTLP push goroutines are now bounded and drained on shutdown; SNMP walks respect context cancellation; the readiness probe targets `/readyz` (not `/healthz`). Several metric names corrected or added.

### Breaking changes

**`network_topology_discovery_devices` renamed to `network_topology_discovery_devices_total`.** Dashboards and alerts using the old name will see no data after upgrade.

**`readinessProbe` in the Helm chart now targets `/readyz`.** If you have custom Kubernetes manifests copying the old probe that pointed at `/healthz`, update them. `/readyz` returns 503 until the first discovery cycle completes; `/healthz` always returns 200 (it is the liveness probe target).

**`network_topology_otlp_push_total{status="dropped"}` added.** Not a breaking change but worth instrumenting in alerts if you use OTLP push: `dropped` means the per-process push concurrency limit (4 concurrent pushes) was saturated and a push was silently discarded.

### Config migration steps

No config key changes. If you have alerts or dashboards on `network_topology_discovery_devices`, rename them to `network_topology_discovery_devices_total`.

### Rollout order

Hub first, then spokes. No federation push schema changes in this release.

### Verification

```sh
# Confirm the renamed metric is present
curl -s http://localhost:9100/metrics | grep network_topology_discovery_devices_total

# Confirm readiness probe is healthy (Kubernetes only)
kubectl exec -n <namespace> <pod> -- curl -s http://localhost:9100/readyz
```

---

## v1.1.0

### Summary

IS-IS and MPLS-TE discovery modules added. Several correctness fixes to OTLP push and context lifecycle.

### Breaking changes

No breaking config changes. Two new modules are disabled by default (`modules.isis.enabled: false`, `modules.mpls_te.enabled: false`) — opt in explicitly if you want them.

**`mpls_te.precedenceRank` corrected.** MPLS-TE tunnel edges now have rank 7 (was inadvertently 4, overriding OSPF). If you have conflicts between MPLS-TE and OSPF edges in the graph, the resolution will change after this upgrade: OSPF (rank 6) now beats MPLS-TE (rank 7) as intended.

### Config migration steps

No migration required. Optionally enable the new modules:

```yaml
modules:
  isis:
    enabled: true
  mpls_te:
    enabled: true
```

### Rollout order

Hub first, then spokes. No federation push schema changes in this release.

### Verification

```sh
# IS-IS edges appear if the module is enabled and the device has IS-IS adjacencies
curl -s http://localhost:9100/metrics | grep 'network_topology_edge_info.*isis'

# MPLS-TE tunnel edges appear as src_port="te-tunnel<N>"
curl -s http://localhost:9100/metrics | grep 'te-tunnel'
```
