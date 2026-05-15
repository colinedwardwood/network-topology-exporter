# Federation

Federation lets multiple `network-topology-exporter` instances coordinate across administrative boundaries. This runbook covers choosing a mode, PKI setup, configuration, tuning, and troubleshooting.

## Mode selection

**standalone** — one instance covers everything. No cross-instance coordination. Use this when your entire managed fleet is reachable from a single exporter.

**uncoordinated** — each instance runs its own discovery independently and emits a boundary observation metric for every out-of-scope neighbour it sees. A Mimir recording rule fires when both sides have reported the same link. Use this when you have two or more instances with overlapping LLDP/CDP visibility, don't want inter-instance network connectivity, and are comfortable with Mimir doing the join. No mTLS required.

**hub/spoke** — each spoke runs discovery and pushes a pre-reconciled graph to the hub after every cycle. The hub aggregates all spoke graphs, reconciles cross-boundary edges, and serves the unified Prometheus metrics. Use this when you want a single authoritative metrics endpoint and can provide hub-to-spoke network connectivity with mTLS. The hub does no SNMP discovery of its own.

When in doubt, prefer uncoordinated if you already have a multi-tenant Mimir deployment. Use hub/spoke when you need cross-boundary edges to appear in a single scrape target or when your Mimir deployment cannot run recording rules.

## mTLS PKI setup

Hub/spoke requires mutual TLS. The hub presents a server certificate; spokes present client certificates. Both sides validate against the same CA.

The commands below produce a self-signed CA and per-component certificates signed by it. Adapt to your existing PKI if you have one.

```sh
mkdir -p /etc/topo-exporter/pki
cd /etc/topo-exporter/pki

# CA
openssl genrsa -out ca.key 4096
openssl req -new -x509 -days 3650 -key ca.key \
  -subj "/CN=topo-exporter-ca" \
  -out ca.pem

# Hub server cert
openssl genrsa -out hub.key 4096
openssl req -new -key hub.key \
  -subj "/CN=hub.example.com" \
  -out hub.csr
openssl x509 -req -days 825 -in hub.csr \
  -CA ca.pem -CAkey ca.key -CAcreateserial \
  -extfile <(printf "subjectAltName=DNS:hub.example.com") \
  -out hub.crt

# Spoke client cert — repeat for each spoke, changing the CN and output filenames
openssl genrsa -out spoke-dc-a.key 4096
openssl req -new -key spoke-dc-a.key \
  -subj "/CN=spoke-dc-a" \
  -out spoke-dc-a.csr
openssl x509 -req -days 825 -in spoke-dc-a.csr \
  -CA ca.pem -CAkey ca.key -CAcreateserial \
  -out spoke-dc-a.crt
```

Set permissions so only the exporter process can read the private keys:

```sh
chmod 600 /etc/topo-exporter/pki/*.key
chown topo-exporter:topo-exporter /etc/topo-exporter/pki/*
```

The hub SAN must match the URL spokes use. If spokes connect by IP, add `IP:x.x.x.x` to the SAN extension. The hub rejects any connection whose client certificate was not signed by the configured CA — no payload is read before the handshake completes.

## Configuration examples

### standalone

```yaml
federation:
  role: standalone
```

This is the default. The `federation:` block can be omitted entirely.

### uncoordinated

```yaml
federation:
  role: uncoordinated
```

No additional fields required. Each instance emits `network_topology_boundary_observation_info` for every out-of-scope neighbour it observes. Install the following recording rule in Mimir/Prometheus so that confirmed cross-boundary links become a single series:

```yaml
- record: network_topology_confirmed_cross_boundary_link
  expr: >
    count by(peer_a, peer_b, proto)(network_topology_boundary_observation_info) == 2
```

`peer_a` is always the alphabetically smaller hostname, so the count reaches exactly 2 when both sides have reported. If your Mimir scrapes the two instances into different tenants, use a cross-tenant recording rule or federate the metric into a shared tenant before applying the rule.

### spoke

```yaml
federation:
  role: spoke
  spoke:
    spoke_id: dc-a            # unique across all spokes; appears in hub metric labels
    hub_url: https://hub.example.com:9101
    tls_ca_cert: /etc/topo-exporter/pki/ca.pem
    tls_cert: /etc/topo-exporter/pki/spoke-dc-a.crt
    tls_key: /etc/topo-exporter/pki/spoke-dc-a.key
```

`spoke_id` must be unique across all spokes. Choose something stable — changing it later will cause the hub to treat the old and new IDs as separate spokes until the old one is evicted.

### hub

```yaml
federation:
  role: hub
  spoke_timeout: 6m           # must be >= 2× discovery.interval; see tuning section
  hub:
    listen_addr: :9101
    tls_ca_cert: /etc/topo-exporter/pki/ca.pem
    tls_cert: /etc/topo-exporter/pki/hub.crt
    tls_key: /etc/topo-exporter/pki/hub.key
```

The hub's Prometheus metrics are served on the normal listen address (default `:9100`). The federation listener on `:9101` accepts spoke pushes only; it is not scraped by Prometheus.

Firewall: allow spokes to reach the hub on port 9101. The hub does not need outbound connectivity to spokes.

## Spoke push response contract

The hub's `POST /spoke/push` returns one of the following status codes. Tools and dashboards consuming spoke push outcomes should branch on `status` and (for rejected pushes) on the JSON `reason` field, not on free-form message text.

| Status | Meaning | Spoke retry behavior |
|---|---|---|
| `204 No Content` | Payload accepted; the spoke's graph is part of active hub state and was published to Prometheus + snapshot. | None — success. |
| `400 Bad Request` | Malformed payload: JSON parse error, missing required field, invalid `spoke_id` characters/length, `cycle_at` missing or set more than 5 minutes in the future, semantic validation failure (empty device ID, non-UTF-8, duplicate IDs, oversize port name, self-edge). | Fatal — spoke aborts retries; same payload cannot succeed. |
| `403 Forbidden` | `spoke_id` does not match the presenting mTLS client certificate's `CN`. | Fatal — operator must reconcile `spoke_id` with the cert subject. |
| `409 Conflict` | Push processed by the transport but **not applied**: a concurrent newer push from any spoke advanced the publish generation past this one. The newer push's data already supersedes this payload. JSON body present (see below); `reason` is `stale_generation`. | Fatal-for-this-cycle — the next discovery cycle produces a newer payload that will not collide. |
| `413 Payload Too Large` | Either the raw request body exceeded 16 MiB, OR the combined hub graph would exceed `federation.hub.max_graph_edges` / `max_graph_devices`. When rejected for size budget, JSON body present; `reason` is `size_budget_exceeded`. | Fatal-for-this-cycle — retrying the same payload will fail identically. Operator must increase the hub's `max_graph_*` budgets or shrink the spoke's footprint. |
| `429 Too Many Requests` | Push arrived sooner than `federation.hub.min_push_interval` after this spoke's last accepted push. `Retry-After` header set to seconds. | Retried with the spoke's own exponential backoff (3 attempts, base 1s). |
| `503 Service Unavailable` | Reserved for transient internal failures the spoke can resolve by retrying (e.g. snapshot back-pressure). No current code path emits this; documented so spokes implement the retry semantics defensively. | Retried with the spoke's own exponential backoff. |

For `409` and `413`, the response is `Content-Type: application/json` with this schema:

```json
{
  "status": "rejected",
  "reason": "size_budget_exceeded",
  "detail": {
    "combined_devices": 8000,
    "combined_edges": 50001,
    "max_devices": 10000,
    "max_edges": 50000
  }
}
```

`reason` is a stable enum. Current values: `size_budget_exceeded`, `stale_generation`. New values will only be added in a release that ships corresponding emission code and tests; deprecated values are removed in a major version after a deprecation window.

Spoke-side, the `network_topology_federation_spoke_push_failures_total` counter increments once per Push() call that exhausts all retries (including immediate fatal-for-this-cycle abort). Alert on `rate(network_topology_federation_spoke_push_failures_total[5m]) > 0` to catch persistent rejection.

### hub with static inter-domain link overrides

When automatic OOS name-matching fails due to inconsistent device names across boundaries, add explicit link tuples:

```yaml
federation:
  role: hub
  spoke_timeout: 6m
  known_inter_domain_links:
    - local_device: sw-dc-a
      local_port: Gi0/1
      remote_device: sw-dc-b
      remote_port: Gi0/2
      link_kind: fiber          # optional; defaults to "ethernet"
    - local_device: sw-dc-a
      local_port: Gi0/2
      remote_device: dc-b-sw01  # the name dc-b reports for the same device
      remote_port: Gi0/1
  hub:
    listen_addr: :9101
    tls_ca_cert: /etc/topo-exporter/pki/ca.pem
    tls_cert: /etc/topo-exporter/pki/hub.crt
    tls_key: /etc/topo-exporter/pki/hub.key
```

Static entries are injected as rank-0 (highest precedence) confirmed bidirectional edges regardless of what automatic matching produces.

## Certificate rotation

Rotating certificates without topology downtime requires overlapping validity windows. The hub's `spoke_timeout` provides the safety buffer: as long as spokes keep pushing during the rotation window, no topology data is lost.

### Procedure

1. **Issue replacement certificates** from the same CA. Set `NotBefore` to now and `NotAfter` to at least the current validity end plus one rotation window.

2. **Rotate the hub server certificate first.** The hub serves the new cert; existing spokes are already trusted via the CA and continue pushing without interruption.

   ```sh
   # Generate new hub server cert (same CA, new validity window)
   openssl req -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes \
     -keyout hub-new.key -subj "/CN=hub" \
     -addext "subjectAltName=IP:$(hub_ip),DNS:hub.example.com" \
   | openssl x509 -req -CA ca.pem -CAkey ca.key -CAcreateserial \
     -days 365 -out hub-new.crt
   ```

   Replace `hub.tls_cert` and `hub.tls_key` in the hub config and restart the hub.

3. **Rotate spoke client certificates one at a time.** For each spoke: replace its `tls_cert` and `tls_key`, restart the spoke, verify `network_topology_federation_spoke_up{spoke_id="..."}` returns to 1 before moving to the next spoke. Allow at least one full push interval between spokes.

4. **Rotate the CA only if necessary** (compromise or expiry). CA rotation requires rotating all leaf certificates simultaneously, which is a topology-disrupting operation. Mitigate by running dual CA pools: add the new CA to the hub's `tls_ca_cert` (PEM-concatenated) before rotating leaf certs, then remove the old CA once all spokes present new-CA-signed certs.

### Verifying a rotated spoke

```sh
curl -v --cacert /etc/topo-exporter/pki/ca.pem \
  --cert /etc/topo-exporter/pki/spoke-new.crt \
  --key /etc/topo-exporter/pki/spoke-new.key \
  https://hub.example.com:9101/healthz
```

A 200 response confirms the new cert is accepted. A 403 means the spoke_id in the cert CN does not match `federation.spoke.spoke_id` in the spoke config.

## Tuning spoke_timeout

`spoke_timeout` controls how long the hub keeps a spoke's graph in the edge store after the spoke stops pushing. A spoke that misses more than one push window is likely down or partitioned; keeping its stale data in the graph for too long produces phantom edges.

Constraints:
- Must be >= 2× `discovery.interval` (validated at startup; the exporter will not start if violated).
- Default when not set: 3× `discovery.interval`.

A conservative starting point is 3× `discovery.interval`. If spokes push on a 90-second cycle, set `spoke_timeout: 4m30s` or `spoke_timeout: 5m` to give one missed push some headroom before eviction. Tighten it once you have confidence in spoke reliability.

When a spoke is evicted, `network_topology_federation_spoke_up{spoke_id="..."}` drops to 0. If that alert fires frequently for a spoke that is actually healthy, `spoke_timeout` is too short relative to the push latency, or the spoke's discovery cycle is running long.

## Troubleshooting

### Missing cross-boundary edges — uncoordinated mode

**Symptom**: the recording rule `count(...) == 2` never fires for a known cross-boundary link.

1. Check that both instances are running in uncoordinated mode and are being scraped:
   ```sh
   curl -s http://instance-a:9100/metrics | grep boundary_observation_info
   curl -s http://instance-b:9100/metrics | grep boundary_observation_info
   ```
2. If one side is missing entirely, that instance isn't seeing the OOS neighbour. Confirm LLDP/CDP is enabled on the inter-domain interface and that the interface is in scope for that instance.
3. If both sides emit the metric but the count never reaches 2, compare the `peer_a` and `peer_b` label values on both sides. They must be identical — same string, same case. `peer_a` is always the alphabetically smaller of the two hostnames. A discrepancy means the two instances are resolving the device name differently (e.g., one uses a FQDN, the other a short hostname). Fix the naming at the device level (LLDP sysName) or use `known_inter_domain_links` in a hub deployment instead.

### Confirmed-edge series flapping — uncoordinated mode

**Symptom**: a known cross-boundary link's confirmed-edge series (from the `count(...) == 2` recording rule) appears and disappears between rule evaluations, producing flapping alerts or visibly missing intervals on dashboards.

This is a **timing skew** between three independent intervals:
- Per-instance `discovery.interval` (e.g. 60s) — controls when each exporter refreshes its `boundary_observation_info` series.
- Prometheus/Mimir `scrape_interval` (e.g. 15s) — controls when those series are pulled into the TSDB.
- Recording rule evaluation interval (e.g. 60s) — controls when the `count(...) == 2` join runs.

If two exporter instances cycle out of phase, one side's boundary observation may be older than the rule's lookback window while the other side's is fresh — the join returns 1, not 2, and the confirmed-edge series temporarily drops.

**Detection**:

```promql
# Per-instance discovery health. Both sides must report fresh data for the count==2 join to work.
time() - network_topology_snapshot_last_written_timestamp_seconds
```

Compare this against your scrape interval and recording-rule evaluation interval. If either instance's value exceeds the rule's lookback window (defaults to 5 minutes in Mimir), the join can transiently fail.

On the Mimir side, the rule's own freshness is observable via:

```promql
# Last time the recording rule group evaluated successfully (Mimir/Prometheus self-metrics).
time() - prometheus_rule_group_last_evaluation_timestamp_seconds{rule_group="<your-rule-group>"}

# Rule evaluation duration — long evaluations can themselves contribute to skew.
prometheus_rule_evaluation_duration_seconds{rule_group="<your-rule-group>"}
```

**Mitigation**:

1. **Make the rule's lookback explicit and generous.** The default uses the instant vector at evaluation time; if you have any chance of phase drift, switch to a windowed form that tolerates one missed cycle:
   ```promql
   count by(peer_a, peer_b, proto)(
     last_over_time(network_topology_boundary_observation_info[3m])
   ) == 2
   ```
   `3m` should be at least `2 × max(discovery.interval across instances) + scrape_interval`. Three minutes covers the default 60s discovery cycle.

2. **Align discovery intervals across instances.** Configure the same `discovery.interval` on every uncoordinated peer. Different intervals guarantee phase drift over time.

3. **Avoid alerting on the raw `count == 2` directly.** Alert on the windowed form's absence over a multi-cycle window (e.g. `absent_over_time(...)[10m:]`) so that a single missed evaluation cannot page.

The exporter does not expose an "external rule staleness" metric because the recording rule lives outside the exporter — its freshness is owned by Mimir/Prometheus and observable through their self-metrics above. Combining the two sides (exporter cycle health + rule evaluation health) is the operator's join, not the exporter's.

### Missing cross-boundary edges — hub/spoke mode

**Symptom**: a link between two spoke domains doesn't appear in the hub's metrics.

The hub reconciles cross-boundary edges using an exact string match between:
- `NeighbourHint`: the name the observing spoke's device reported for the OOS neighbour (from `lldpRemSysName` or `cdpCacheDeviceId`)
- `ReportingDevice`: the `sysName` of the device that the neighbour's spoke is managing

If these don't match exactly — different case, domain suffix on one side, vendor abbreviation — no cross-boundary edge is created.

1. Enable debug logging on the hub:
   ```sh
   network-topology-exporter --log.level=debug
   ```
   Look for log lines containing `"hub: OOS neighbour has no reverse observation; possible naming mismatch"`. The line will include the unmatched NeighbourHint value.

2. Compare the NeighbourHint from the observing spoke to the ReportingDevice in the other spoke's push. The push payload can be inspected by temporarily enabling debug logging on a spoke — it logs the graph it is about to push.

3. If the naming mismatch is at the device level and cannot be fixed there, add a `known_inter_domain_links` entry for the affected port pair. This injects the edge directly without relying on name matching.

### Spoke eviction firing unexpectedly

**Symptom**: `network_topology_federation_spoke_up` drops to 0 for a spoke that appears healthy.

1. Check the spoke's logs for push errors. A TLS error on the spoke side means the spoke cannot reach the hub (network partition, expired certificate, wrong hub_url).
2. Check the hub's logs for rejected pushes. A rejected push logs the reason — likely a CycleAt validation failure (clock skew > 5 minutes between spoke and hub, or spoke's cycle is completing after `spoke_timeout` has already evicted it).
3. Check NTP sync on all spoke hosts. The hub rejects payloads with CycleAt more than 5 minutes in the future or older than `spoke_timeout`.
4. If the spoke's discovery cycle is running long (visible in `network_topology_graph_stale` staying at 1 for an extended period), the push arrives late. Increase `spoke_timeout` or reduce the discovery scope on the affected spoke.

### Hub rejecting spoke connections

**Symptom**: spoke logs show TLS handshake errors; hub logs show certificate verification failures.

1. Verify the spoke's client certificate was signed by the CA configured in `hub.tls_ca_cert`. The CA cert on the hub and the CA cert used to sign the spoke cert must be the same file (or the same CA chain).
   ```sh
   openssl verify -CAfile /etc/topo-exporter/pki/ca.pem /etc/topo-exporter/pki/spoke-dc-a.crt
   ```
2. Verify the hub's server certificate SAN matches the URL in `spoke.hub_url`:
   ```sh
   openssl x509 -noout -ext subjectAltName -in /etc/topo-exporter/pki/hub.crt
   ```
3. Check certificate expiry on both sides:
   ```sh
   openssl x509 -noout -dates -in /etc/topo-exporter/pki/hub.crt
   openssl x509 -noout -dates -in /etc/topo-exporter/pki/spoke-dc-a.crt
   ```
4. After rotating any certificate, restart the affected component. The exporter reads TLS material at startup only.

### Hub serving stale metrics after restart

The hub loads the same snapshot path as standalone/spoke instances. On startup, `network_topology_graph_stale` is 1 until the first spoke push arrives. If no spoke has pushed within one `discovery.interval` of the hub starting, something is wrong with at least one spoke.

Check `network_topology_federation_spoke_last_push_timestamp_seconds{spoke_id="..."}` to identify which spoke is overdue. A value of 0 means the hub has never received a push from that spoke since starting (no snapshot entry for it either).

## Alert runbook entries

### FederationSpokeDown

```
network_topology_federation_spoke_up{spoke_id="<id>"} == 0
```

The hub has not received a push from this spoke within `spoke_timeout`. The spoke's contribution to the hub's graph has been evicted.

1. Check the spoke host is running: `systemctl status network-topology-exporter` on the spoke host.
2. Check spoke logs for push errors (TLS, connection refused, timeout).
3. Confirm the hub's federation listener is reachable from the spoke:
   ```sh
   curl -v --cacert /etc/topo-exporter/pki/ca.pem \
     --cert /etc/topo-exporter/pki/spoke-dc-a.crt \
     --key /etc/topo-exporter/pki/spoke-dc-a.key \
     https://hub.example.com:9101/healthz
   ```
4. Check for clock skew between the spoke host and hub host — CycleAt validation will reject pushes if clocks differ by more than 5 minutes.
5. If the spoke is healthy but pushes are being rejected, increase `spoke_timeout` as a temporary measure while the root cause is investigated.

### HubGraphStale

```
network_topology_graph_stale{job="topo-hub"} == 1
```

The hub has not received any spoke push since starting (or since the last successful push cycle). The metrics it is serving are from the snapshot or from before the last eviction.

1. Check `network_topology_federation_spoke_up` for all spoke IDs. Any spoke showing 0 is down.
2. If all spokes show 1 but the hub graph is still stale, the hub may have just restarted — wait one `discovery.interval` for pushes to arrive.
3. If stale persists beyond two intervals with all spokes reporting up, check the hub's logs for push processing errors.

### UncoordinatedLinkMissing

This is not a built-in alert; define it against your recording rule:

```
absent(network_topology_confirmed_cross_boundary_link{peer_a="sw-dc-a", peer_b="sw-dc-b"})
```

Fires when a known cross-boundary link stops appearing in the recording rule output. Follow the uncoordinated troubleshooting steps above. The most common causes are: one instance is down, LLDP adjacency was lost on the inter-domain interface, or a device was renamed and the `peer_a`/`peer_b` labels no longer match between the two instances.
