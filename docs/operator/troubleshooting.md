# Operator Troubleshooting Guide

## 1. No edges in /metrics

**Check `network_topology_graph_stale`.**

```
curl -s http://localhost:9100/metrics | grep network_topology_graph_stale
```

If it returns `1`, the exporter has not completed its first discovery cycle. Wait for the cycle to finish or see [graph_stale stuck at 1](#7-graph_stale-stuck-at-1).

If it returns `0` and there are still no edges:

1. Confirm at least one target device falls within `discovery.scope.cidr_allow_list`. Any device outside this range is never polled.
2. Check `network_topology_discovery_devices_total{status="failed"}` and `{status="timeout"}`. If all devices are failing, SNMP is not reachable — see [SNMP timeouts and walk failures](#3-snmp-timeouts-and-walk-failures).
3. Check that at least one discovery module is enabled: `modules.lldp.enabled`, `modules.cdp.enabled`, `modules.bgp.enabled`, `modules.ospf.enabled`, `modules.fdb.enabled`, `modules.isis.enabled`, or `modules.mpls_te.enabled`.
4. Edges require both endpoints to be in scope. If only one side of a link is in `cidr_allow_list`, the edge will not appear. Check `network_topology_out_of_scope_neighbours_total`.

---

## 2. Missing specific edges

An edge you expect is absent from `network_topology_edge_info`.

1. Determine whether the missing edge involves a device outside `cidr_allow_list`. Check:
   ```
   curl -s http://localhost:9100/metrics | grep network_topology_out_of_scope_neighbours_total
   ```
   A value > 0 means at least one LLDP/CDP-reported neighbour is outside the allow-list and is being silently dropped.

2. Check whether `cidr_allow_list` covers both endpoints of the expected link. A common mistake is a subnet boundary that excludes one device.

3. For LLDP/CDP edges: confirm LLDP or CDP is enabled on both interfaces, not just one end. If only one side advertises, the edge may appear as one-sided and be suppressed depending on `bidirectional` requirements.

4. For FDB edges: FDB entries age out on the switch (default 300 s). If `discovery.interval` is long, entries may expire before the next poll. Set `discovery.unconfirmed_link_ttl_cycles` so that:
   ```
   unconfirmed_link_ttl_cycles > fdb_aging_seconds / discovery.interval
   ```
   For example, with FDB aging of 300 s and a 60 s interval, set `unconfirmed_link_ttl_cycles` to at least 6.

5. Confirm the relevant protocol module is enabled (`modules.lldp.enabled`, `modules.fdb.enabled`, etc.).

6. **Nokia SR Linux fleets:** SR Linux 24.x does not implement the standard IEEE 802.1AB LLDP MIB at `1.0.8802.1.1.2` via SNMP. The SNMP daemon exposes the system group, IF-MIB interface tables, and selected Nokia enterprise OIDs — but `lldpLocPortTable` and `lldpRemTable` return `No Such Object available on this agent`. LLDP topology on SR Linux is only accessible via gNMI / JSON-RPC at the `/system/lldp` YANG path. The exporter is SNMP-only today; SR Linux LLDP discovery via this binary is not supported. The classic SR-OS family (7705 SAR, 7750 SR, 7250 IXR running SR-OS rather than SR Linux) does implement the standard MIB and works as expected. gNMI as a first-class discovery transport is tracked at v2.0.0.

---

## 3. SNMP timeouts and walk failures

**Symptom:** `network_topology_snmp_walks_total{status="timeout"}` is rising.

1. Verify basic SNMP reachability from the exporter host:
   ```
   snmpwalk -v2c -c public <device-ip> .1.3.6.1.2.1.1
   ```
   If this times out, the problem is network or device configuration, not the exporter.

2. Check firewall rules. SNMP uses UDP/161. Confirm no ACL or stateful firewall is dropping packets from the exporter's source IP.

3. If SNMP is reachable manually but walks still time out under load, reduce `discovery.parallelism` to lower concurrent UDP traffic, or increase `discovery.timeout_per_device`.

4. If a subset of devices consistently time out, consider removing them from targets. Unreachable devices block SNMP workers for the full `timeout_per_device` duration, slowing the entire cycle.

5. Walk the LLDP neighbour table manually to confirm the MIB is supported:
   ```
   snmpwalk -v2c -c public <device-ip> .1.0.8802.1.1.2.1.4
   ```

---

## 4. Credential failures

**Symptom:** `network_topology_credential_trials_total{status="failed"}` is non-zero.

The exporter tries credentials from `credentials.profiles` in order. A failure means none of the configured profiles authenticated successfully against a device.

**Interpreting failure phase:**

- **Community mismatch (SNMPv2c):** The community string is wrong. The device returns an error or no response. Verify the community string in `credentials.profiles`.
- **Auth failure (SNMPv3):** `authNoPriv` or `authPriv` mode — username/auth protocol/auth password mismatch. Check the SNMPv3 user configuration on the device.
- **Priv failure (SNMPv3):** Auth succeeded but privacy decryption failed. Privacy protocol or privacy password is wrong.

**Risk of device lockout:** Some devices enforce lockout after repeated authentication failures. The `credentials.trial_rate_per_second` config limits how fast credential trials are attempted. Keep this conservative (e.g., 1–2 per second) when targeting devices with lockout policies.

See [cold-start-credentials.md](cold-start-credentials.md) for guidance on initial credential configuration.

---

## 5. Discovery cycle too slow

**Symptom:** `network_topology_discovery_cycle_duration_seconds` P99 exceeds `discovery.interval`.

When cycles take longer than the interval, the exporter falls behind and metrics become stale.

**Common causes:**

- **Parallelism too low:** The exporter serialises device polls when `parallelism` is less than the fleet size. As a starting point:
  ```
  parallelism ≈ fleet_size / (interval / timeout_per_device)
  ```
  For example, 200 devices, 60 s interval, 5 s timeout: `parallelism ≈ 200 / (60 / 5) = ~17`.

- **`timeout_per_device` too high:** If most devices respond in under 2 s but `timeout_per_device` is 10 s, unreachable devices consume workers for 10 s each. Lower the timeout to match observed response times.

- **Unreachable devices blocking workers:** A device that never responds occupies a worker for the full timeout. Remove persistently unreachable devices from targets, or track them with a separate health check.

**Tuning steps:**

1. Check current P99 cycle duration:
   ```
   curl -s http://localhost:9100/metrics | grep network_topology_discovery_cycle_duration_seconds
   ```
2. Check the failure breakdown: `network_topology_discovery_devices_total{status="timeout"}` vs `{status="failed"}`.
3. Increase `discovery.parallelism` incrementally, then re-observe cycle duration after a few cycles.
4. Lower `discovery.timeout_per_device` if devices reliably respond faster than the current value.

If timeouts cluster around a stateful firewall or NAT device in path and `nf_conntrack_count` is climbing, the root cause may be SNMP socket churn rather than the exporter or the targets. See [scale.md § SNMP session lifecycle and conntrack pressure](scale.md#snmp-session-lifecycle-and-conntrack-pressure).

---

## 6. Conflict metrics rising

**Metric:** `network_topology_conflict_total{conflict_type=...}`

**`neighbour_disagreement`** — the only `conflict_type` currently emitted.

Two discovery sources name different neighbour devices for the same local port (e.g. LLDP reports `core-sw-02` as the neighbour on `Gi0/1` while CDP reports `core-sw-03`). Usually means one source is stale, or the port has changed neighbours during the cycle. Check that LLDP/CDP are both configured correctly on the local device and that the cached neighbour state on each source matches the current cabling.

Other categories of disagreement (port-name encoding differences, direction asymmetry, static-vs-observed mismatch) are detected upstream of the conflict counter — port-name encoding differences, for example, are normalised to a canonical form before grouping rather than surfaced as a conflict, to avoid false positives on LAG parallel member links.

---

## 7. graph_stale stuck at 1

`network_topology_graph_stale` remains `1` after startup, meaning no discovery cycle has completed.

1. Check `/readyz`:
   ```
   curl -s http://localhost:9100/readyz
   ```
   A `503` response confirms the exporter has not finished its first cycle.

2. Check `/healthz` for `last_cycle_at`:
   ```
   curl -s http://localhost:9100/healthz | jq .
   ```
   If `last_cycle_at` is zero or absent, no cycle has completed.

3. Check exporter logs for discovery errors. A startup log line will indicate if credential loading or SNMP initialisation failed before any device was polled.

4. If all target devices are unreachable, the cycle may complete but yield zero edges — `graph_stale` will move to `0` once a cycle finishes, regardless of result. If `graph_stale` is truly stuck, the cycle itself is not finishing. Look for a deadlock or a very large fleet with low parallelism causing the cycle to exceed the configured timeout.

5. Verify `cidr_allow_list` is non-empty and covers at least one reachable device.

---

## 8. Snapshot issues

**`snapshot_last_written_timestamp_seconds` not advancing**

The snapshot is not being written to `snapshot.path`. Common causes:

- **Permissions:** The exporter process does not have write access to `snapshot.path` or its parent directory.
- **Disk full:** Check available space on the volume hosting the snapshot path.
- **NFS stall:** If `snapshot.path` is on a network filesystem, a stalled mount will block writes silently. Check NFS mount health and consider moving the snapshot to local disk.

**`snapshot_loaded_devices_total` = 0 at startup**

The snapshot was not loaded on startup. This is a cold start (no snapshot file existed) or the snapshot was rejected.

- A log line at startup will indicate whether the snapshot was absent, failed to parse, or was rejected due to a schema version mismatch.
- After a version upgrade, a format change may invalidate an existing snapshot. The exporter will start cold and rebuild from live discovery.

**Verifying snapshot integrity**

The snapshot is a JSON file. To confirm it is well-formed:

```
jq . < /path/to/snapshot.json
```

To count the number of edges stored:

```
jq '.edges | length' < /path/to/snapshot.json
```

A clean parse with a non-zero edge count indicates a healthy snapshot.

---

## 9. out_of_scope_neighbours_total > 0

`network_topology_out_of_scope_neighbours_total` counts LLDP/CDP-reported neighbours whose management IP falls outside `discovery.scope.cidr_allow_list`. These devices are discovered as neighbours but never polled.

**What to do:**

- If the out-of-scope device is a managed device you want to include, add its subnet to `cidr_allow_list` and add SNMP credentials for it.
- If the out-of-scope device is expected to be external (e.g., a carrier handoff, an unmanaged switch, or a device in another team's scope), the metric is informational. You can track it as signal that peering boundaries are working correctly.
- If the count is unexpectedly high, cross-reference which neighbours are being reported using LLDP MIB walks on the relevant devices, then decide whether to expand scope or leave the boundary as-is.

---

## 10. Debugging commands

**Readiness check**

```sh
curl -s http://localhost:9100/readyz
```

Returns `200 OK` after the first discovery cycle completes; `503` during startup.

**Last cycle info**

```sh
curl -s http://localhost:9100/healthz | jq .
```

Returns a JSON object with `last_cycle_at` and per-device error counts.

**Check graph_stale**

```sh
curl -s http://localhost:9100/metrics | grep network_topology_graph_stale
```

**Check conflict counts**

```sh
curl -s http://localhost:9100/metrics | grep network_topology_conflict_total
```

**Check SNMP walk status**

```sh
curl -s http://localhost:9100/metrics | grep network_topology_snmp_walks_total
```

**Manual SNMP system group test**

```sh
snmpwalk -v2c -c public <device-ip> .1.3.6.1.2.1.1
```

Confirms basic SNMP reachability and community string.

**Manual LLDP neighbour table walk**

```sh
snmpwalk -v2c -c public <device-ip> .1.0.8802.1.1.2.1.4
```

Queries the standard LLDP-MIB neighbour table directly.

**Count edges in snapshot**

```sh
jq '.edges | length' < /path/to/snapshot.json
```

**Count devices in snapshot**

```sh
jq '.devices | length' < /path/to/snapshot.json
```

---

## 11. OTLP push goroutine overlapping with the next scrape cycle

**Symptom:** `network_topology_otlp_push_total{status="error"}` is rising; log lines show push timeouts or context cancellation errors; discovery cycle duration is increasing.

When `output.otlp.timeout` is set close to `discovery.interval`, the OTLP push goroutine from one cycle can still be running when the next discovery cycle starts. The two goroutines then compete for the same resources and the push from the previous cycle is cancelled mid-flight, incrementing the `error` counter.

**Operator guidance:** Set `output.otlp.timeout` to less than half the discovery interval:

```
otlp.timeout < discovery.interval / 2
```

For example, with `discovery.interval: 60s`, keep `output.otlp.timeout` at `25s` or lower. The default of `10s` is safe for most intervals of 30 s or longer.

**To diagnose:**

```sh
curl -s http://localhost:9100/metrics | grep network_topology_otlp_push_total
curl -s http://localhost:9100/metrics | grep network_topology_discovery_cycle_duration_seconds
```

If cycle duration P99 is close to the configured interval and `otlp_push_total{status="error"}` is non-zero, lower `output.otlp.timeout` first before investigating the receiver endpoint.

---

## 12. Topology metrics absent from /metrics entirely

**Symptom:** `network_topology_device_info`, `network_topology_edge_info`, and related metric families do not appear anywhere in the `/metrics` output — not even as gauge families with zero series.

This is distinct from [No edges in /metrics](#1-no-edges-in-metrics), where the metric names are present but carry no label tuples. Here the names are missing entirely.

**Cause 1: Startup snapshot window (cold start)**

On startup the exporter loads the previous snapshot and begins serving it immediately, with `network_topology_graph_stale=1`. If no snapshot exists (cold start), the TopologyCollector holds an empty graph. In this state:

- `network_topology_out_of_scope_neighbours_total` and other scalar gauges will appear with value `0`.
- `network_topology_device_info` and `network_topology_edge_info` will have no series at all, because they are label-tuple metrics that require at least one graph element to produce a time series.

This is expected behaviour. Wait for the first discovery cycle to complete; once `network_topology_graph_stale` drops to `0`, the metric families will appear with populated label sets.

```sh
curl -s http://localhost:9100/metrics | grep network_topology_graph_stale
```

**Cause 2: Snapshot version mismatch**

If the on-disk snapshot was written by an older version of the exporter, the snapshot is quarantined on startup: it is renamed to `.bad` and the exporter starts cold as described above.

Check the startup logs for a line such as:

```
snapshot: version mismatch; quarantining  path=/var/lib/network-topology-exporter/snapshot.json
```

Once the first live discovery cycle completes, the exporter writes a fresh snapshot and metrics will appear normally. No operator action is required unless the quarantined `.bad` file needs to be removed for disk space reasons.

---

## OTLP push saturation

**Symptom**: `network_topology_otlp_push_total{status="dropped"}` is non-zero.

**Cause**: More than `maxOTLPPushConcurrency` (4) push goroutines were in-flight simultaneously. This happens when the OTLP receiver is slow and the discovery cycle is short.

**Remediation**:
- Increase `output.otlp.timeout` so pushes complete faster relative to the cycle interval.
- Reduce `discovery.interval` churn (longer interval → fewer push triggers).
- Check OTLP receiver health.
