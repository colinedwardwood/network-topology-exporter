# SLO Guidance

This document defines three service-level indicators (SLIs), their SLO targets, and multi-window multi-burn-rate alert rules following the Google SRE Workbook pattern. All PromQL references verified metric names against [`docs/metrics.md`](../metrics.md) and the Go source at `internal/metrics/metrics.go`.

**Lab validation note.** SLI 1 (cycle-duration headroom) and SLI 2 (snapshot-drop rate) are observable on a standalone deployment against `deploy/test-harness/`. SLI 3 (federation spoke down) requires a hub/spoke federation deployment; it cannot be exercised against the standalone test harness.

Cross-references: [docs/operator/troubleshooting.md](troubleshooting.md), [docs/operator/scale.md](scale.md), Helm chart PrometheusRule at `deploy/helm/topology-exporter/templates/prometheusrule.yaml`.

---

## Background: multi-window multi-burn-rate alerting

Each SLI below defines two alert tiers:

- **Page** — high burn rate; fire within minutes; wake someone up.
- **Ticket** — moderate burn rate; file an issue within the business day.

A burn rate of 1.0 means you are consuming the error budget at exactly the rate that exhausts it over the window. A burn rate of 14.4 over one hour consumes in one hour what would take a full day at rate 1.0.

The thresholds below follow the Google SRE Workbook values for a 30-day rolling window. Adjust to match your own on-call latency expectations and fleet size.

---

## SLI 1 — Cycle-duration headroom

### Definition

The exporter must complete its full discovery cycle well within the configured `discovery.interval`. When cycle p99 duration approaches the interval, the exporter falls behind: metrics become stale, the snapshot stops advancing, and `network_topology_graph_stale` rises.

**SLI:** headroom remaining = `1 - (p99_cycle_duration / discovery_interval)`

**SLO:** headroom ≥ 30% over a 30-day window (i.e., p99 cycle duration stays below 70% of `discovery.interval`).

The 30% headroom is a practical lower bound: at 30% remaining, you have meaningful breathing room for a fleet-wide event (e.g., a transient network partition that slows SNMP walks). Below 30%, an adverse event causes a full budget overrun within one cycle.

### Metric

`network_topology_discovery_cycle_duration_seconds` — histogram, no labels. Confirmed present in `internal/metrics/metrics.go` (registered as `DiscoveryCycleDuration`).

The discovery interval is operator-configured (`discovery.interval`, default `60s`). The PromQL below uses `60` as the threshold — replace with your actual interval in seconds. The Helm chart's `prometheusRule.cycleThresholdSeconds` value is the right source of truth for your deployment.

### PromQL

**Current headroom (instantaneous, for dashboards):**

```promql
1 - (
  histogram_quantile(
    0.99,
    sum by (le) (rate(network_topology_discovery_cycle_duration_seconds_bucket[10m]))
  )
  / 60
)
```

Replace `60` with your `discovery.interval` in seconds.

**Error ratio (1-minute window, for burn-rate calculation):**

```promql
# "bad minute" = a 1-minute window where p99 exceeds 70% of the interval
(
  histogram_quantile(
    0.99,
    sum by (le) (rate(network_topology_discovery_cycle_duration_seconds_bucket[1m]))
  )
  > (60 * 0.7)
)
```

### Multi-window multi-burn-rate alerts

```yaml
# SLI 1 — Cycle-duration headroom (SLO: p99 < 70% of discovery.interval over 30 days)
# Replace '60' with your discovery.interval in seconds throughout.

- alert: TopologyCycleHeadroomPageFast
  # Burn rate 14.4× for 1h = ~2% of 30-day budget consumed in 1h. Page immediately.
  expr: |
    (
      histogram_quantile(
        0.99,
        sum by (le) (rate(network_topology_discovery_cycle_duration_seconds_bucket[1h]))
      ) > (60 * 0.7)
    )
    and
    (
      histogram_quantile(
        0.99,
        sum by (le) (rate(network_topology_discovery_cycle_duration_seconds_bucket[5m]))
      ) > (60 * 0.7)
    )
  for: 2m
  labels:
    severity: page
  annotations:
    summary: "Topology discovery p99 exceeds 70% of interval (fast burn)"
    description: >-
      Discovery p99 has been above 70% of the configured interval for
      the past 1h and 5m windows. At this rate the 30-day headroom
      budget will be exhausted in under 2 days. Check
      network_topology_discovery_devices_total for timeout/failed counts,
      and network_topology_discovery_module_duration_seconds per module.
      See docs/operator/troubleshooting.md §5 and docs/operator/scale.md.
    runbook_url: "https://github.com/colinedwardwood/network-topology-exporter/blob/main/docs/operator/troubleshooting.md#5-discovery-cycle-too-slow"

- alert: TopologyCycleHeadroomPageSlow
  # Burn rate 6× for 6h = ~5% of 30-day budget consumed in 6h. Page.
  expr: |
    (
      histogram_quantile(
        0.99,
        sum by (le) (rate(network_topology_discovery_cycle_duration_seconds_bucket[6h]))
      ) > (60 * 0.7)
    )
    and
    (
      histogram_quantile(
        0.99,
        sum by (le) (rate(network_topology_discovery_cycle_duration_seconds_bucket[30m]))
      ) > (60 * 0.7)
    )
  for: 15m
  labels:
    severity: page
  annotations:
    summary: "Topology discovery p99 exceeds 70% of interval (slow burn)"
    description: >-
      Discovery p99 has been above 70% of the configured interval for
      6h and 30m windows. Investigate unreachable devices or parallelism
      settings. See docs/operator/troubleshooting.md §5.

- alert: TopologyCycleHeadroomTicket
  # Burn rate 3× for 24h (≥30d window). File a ticket; not urgent enough to page.
  expr: |
    (
      histogram_quantile(
        0.99,
        sum by (le) (rate(network_topology_discovery_cycle_duration_seconds_bucket[24h]))
      ) > (60 * 0.7)
    )
    and
    (
      histogram_quantile(
        0.99,
        sum by (le) (rate(network_topology_discovery_cycle_duration_seconds_bucket[2h]))
      ) > (60 * 0.7)
    )
  for: 30m
  labels:
    severity: ticket
  annotations:
    summary: "Topology discovery cycle headroom trending low (ticket)"
    description: >-
      Discovery p99 has been above 70% of the configured interval over
      24h and 2h windows. Not yet urgent, but the exporter is trending
      toward the SLO boundary. Review fleet growth, parallelism, and
      timeout tuning. See docs/operator/scale.md.
```

### Helm chart integration

The existing Helm PrometheusRule already ships a `TopologyDiscoverySlow` alert that fires on p99 exceeding `cycleThresholdSeconds`. The multi-burn-rate alerts above replace or supplement it. To use the multi-burn-rate forms, add them under `prometheusRule` in your Helm values or manage them as a separate `PrometheusRule` CR. Remember to replace the hardcoded `60` with `{{ .Values.prometheusRule.cycleThresholdSeconds }}` if templating.

---

## SLI 2 — Snapshot-drop rate

### Definition

The exporter persists the reconciled graph to `snapshot.path` after every discovery cycle. If the snapshot pipeline stalls (disk full, NFS hang, write already in flight), the write is dropped and counted. Dropped snapshots mean a restart after any outage will cold-start from an increasingly stale graph.

**SLI:** snapshot drop rate = rate at which `network_topology_snapshot_drops_total` increases.

**SLO:** zero drops over any 7-day rolling window.

Zero tolerance is appropriate here: each drop is a missed persistence opportunity. A single queue-full or write-in-flight event is a signal the storage tier is struggling, not a transient blip.

### Metric

`network_topology_snapshot_drops_total` — counter, label `reason` ∈ {`queue_full`, `write_in_flight`}. Confirmed present in `internal/metrics/metrics.go` (registered as `SnapshotDropsTotal`).

- `queue_full` — the caller could not enqueue the snapshot (the snapshot channel was full, meaning the previous snapshot write was still in progress at cycle completion).
- `write_in_flight` — the background writer found the previous write still pending when it tried to start the next one.

Both reasons indicate the same underlying condition (storage stall); they are kept distinct to help narrow the root cause.

### PromQL

**Any drops in the last 5 minutes (for dashboards):**

```promql
increase(network_topology_snapshot_drops_total[5m]) > 0
```

**7-day drop total (for SLO burn tracking):**

```promql
increase(network_topology_snapshot_drops_total[7d])
```

### Multi-window multi-burn-rate alerts

The SLO is zero drops over 7 days. Any drop violates the SLO, so the page alert fires on any non-zero rate rather than a burn-rate fraction. The ticket alert provides an early signal on a trend before a page-level event.

```yaml
# SLI 2 — Snapshot-drop rate (SLO: zero drops over 7 days)

- alert: TopologySnapshotDropsPage
  # Any drop in the last 5 minutes is a violation. Page.
  expr: increase(network_topology_snapshot_drops_total[5m]) > 0
  for: 5m
  labels:
    severity: page
  annotations:
    summary: "Topology snapshot drops detected — on-disk state going stale"
    description: >-
      network_topology_snapshot_drops_total is increasing. The snapshot
      pipeline is not keeping up with the discovery cycle. A restart now
      will cold-start from a potentially stale graph.
      reason={{ $labels.reason }}: queue_full means the snapshot channel
      was full (previous write still in progress); write_in_flight means
      the background writer could not start. Check disk space, NFS mount
      health, and I/O latency on the snapshot volume.
      See docs/operator/troubleshooting.md §8.
    runbook_url: "https://github.com/colinedwardwood/network-topology-exporter/blob/main/docs/operator/troubleshooting.md#8-snapshot-issues"

- alert: TopologySnapshotDropsTicket
  # Any drop at all over 1h (same zero tolerance, longer window for ticket).
  expr: increase(network_topology_snapshot_drops_total[1h]) > 0
  for: 0m
  labels:
    severity: ticket
  annotations:
    summary: "Topology snapshot drops in the last hour (ticket)"
    description: >-
      network_topology_snapshot_drops_total has incremented in the last
      hour. Investigate storage health before this escalates.
      See docs/operator/troubleshooting.md §8.
```

**Note on window size.** Because the SLO is zero-drop, multi-window burn-rate is less relevant than for headroom-style SLOs. The two alerts above cover "is it happening right now" (page) and "did it happen in the last hour" (ticket). For a formal 7-day error budget view, use `increase(network_topology_snapshot_drops_total[7d])` in a dashboard panel.

### Helm chart integration

Add these rules to your PrometheusRule CR or as additional entries under the Helm `prometheusRule` values. The existing `TopologySnapshotNotWritten` alert (which fires when `network_topology_snapshot_last_written_timestamp_seconds` stops advancing) is a complementary dead-man's-switch signal that fires when the snapshot is both stale and not being written — pair it with the drop alerts for full coverage.

---

## SLI 3 — Federation spoke-down rate

### Definition

In hub/spoke federation mode, the hub evicts a spoke from its graph after `federation.spoke_timeout` elapses without a push. An evicted spoke's topology is absent from the hub's unified metrics until the spoke reconnects and pushes again.

**SLI:** number of distinct spokes evicted (spoke-down events) per hub per 7-day rolling window.

**SLO:** ≤1 spoke-down event per hub over any 7-day window.

A tolerance of 1 accommodates planned maintenance (rolling a spoke for upgrades, cert rotation). The tolerance is per-hub, not per-fleet — a hub serving 10 spokes and seeing 3 simultaneous evictions is a fleet-level event, not routine maintenance.

**Lab validation note.** This SLI requires a hub/spoke deployment. It cannot be exercised against the standalone `deploy/test-harness/`. Use the `deploy/long-running-test/` lab or a staging hub/spoke stack. See `deploy/long-running-test/README.md` for setup.

**Version negotiation note.** The hub has no version negotiation with spokes — there is no gRPC service-version header or payload-version field in the push schema. A spoke that stops pushing after a failed upgrade is indistinguishable from a spoke that is down. The eviction counter captures both.

### Metrics

`network_topology_federation_spoke_up` — gauge, label `spoke_id`. Hub mode only. 1 while the spoke has pushed within `spoke_timeout`; the series is deleted (not set to 0) on eviction. Confirmed present in `internal/metrics/metrics.go` (registered as `FederationSpokeUp`).

`network_topology_federation_spoke_last_push_timestamp_seconds` — gauge, label `spoke_id`. Hub mode only. Unix timestamp of the most recent push. Confirmed present in `internal/metrics/metrics.go` (registered as `FederationSpokeLastPushUnix`).

The existing Helm PrometheusRule's `TopologyFederationSpokeDown` alert fires on `network_topology_federation_spoke_up == 0` — but because the series is deleted on eviction (not set to 0), the alert fires only when a spoke that previously pushed is evicted and the series momentarily shows 0 before deletion. The absence of the series entirely (for a spoke that never pushed since hub restart) requires an `absent()` query.

### PromQL

**Is any spoke currently evicted (instantaneous)?**

```promql
# A spoke whose last push is older than spoke_timeout.
# Use time() minus last push timestamp, compared to the spoke_timeout.
# Replace 180 with your federation.spoke_timeout in seconds (default 3m = 180s).
time() - network_topology_federation_spoke_last_push_timestamp_seconds > 180
```

**Count of distinct eviction events over 7 days:**

The hub deletes the `spoke_up` series on eviction rather than setting it to 0 — see `internal/federation/hub.go::evictSilentSpokes()`. Counting evictions from a gauge that disappears requires tracking transitions. The practical approach for SLO tracking is to count the number of times the `spoke_last_push` timestamp crosses the `spoke_timeout` threshold:

```promql
# Spokes whose last push is older than spoke_timeout (currently evicted or at risk).
count(
  time() - network_topology_federation_spoke_last_push_timestamp_seconds
  > 180
) or vector(0)
```

**7-day burn: count eviction windows (ticket-level tracking):**

```promql
# Count distinct spoke_ids where last push crossed spoke_timeout at any point in 7d.
# This is a proxy — Prometheus cannot count historical transitions from a gauge.
# Use a recording rule to materialise it:
#   record: network_topology_federation_spoke_eviction_windows_7d
#   expr: count_over_time(
#           (time() - network_topology_federation_spoke_last_push_timestamp_seconds > 180)[7d:1m]
#         )
count_over_time(
  (time() - network_topology_federation_spoke_last_push_timestamp_seconds > 180)[7d:1m]
)
```

### Multi-window multi-burn-rate alerts

```yaml
# SLI 3 — Federation spoke-down rate (SLO: ≤1 eviction per hub per 7 days)
# Requires hub mode. Replace 180 with your federation.spoke_timeout in seconds.

- alert: TopologySpokeDownPage
  # A spoke is currently evicted (or overdue). Page if it has been absent for
  # more than 5 minutes — giving one retry window before alerting.
  expr: |
    time() - network_topology_federation_spoke_last_push_timestamp_seconds
    > 180
  for: 5m
  labels:
    severity: page
  annotations:
    summary: "Federation spoke {{ $labels.spoke_id }} is not pushing (spoke down)"
    description: >-
      Hub {{ $labels.instance }} has not received a push from spoke
      {{ $labels.spoke_id }} in more than spoke_timeout ({{ $value | humanizeDuration }} ago).
      The spoke's topology contribution is absent from the hub's unified
      metrics. Check spoke health, mTLS certificate validity, and network
      connectivity to the hub federation listener on port 9101.
      See docs/operator/federation.md § FederationSpokeDown runbook entry.
    runbook_url: "https://github.com/colinedwardwood/network-topology-exporter/blob/main/docs/operator/federation.md#federationspokedown"

- alert: TopologySpokeDownTicket
  # Budget alert: more than 1 spoke has been evicted in the last 24h.
  # Fires if the SLO (≤1 event per 7d) is trending to be violated.
  expr: |
    count by (instance) (
      time() - network_topology_federation_spoke_last_push_timestamp_seconds
      > 180
    ) > 1
  for: 10m
  labels:
    severity: ticket
  annotations:
    summary: "Multiple spokes down on hub {{ $labels.instance }} (SLO risk)"
    description: >-
      More than one spoke is currently evicted on hub {{ $labels.instance }}.
      The 7-day SLO (≤1 eviction event) is at risk. Investigate fleet-wide
      connectivity or certificate expiry. See docs/operator/federation.md.

- alert: TopologySpokeNeverPushed
  # A spoke_id appears in config but has never pushed since hub restart.
  # absent() fires when the series doesn't exist at all.
  # Define expected spoke_ids and check for absence:
  #   absent(network_topology_federation_spoke_last_push_timestamp_seconds{spoke_id="dc-a"})
  # This template requires one rule per expected spoke_id; manage it via
  # the Helm values or a templated PrometheusRule.
  expr: |
    absent(network_topology_federation_spoke_last_push_timestamp_seconds{spoke_id="dc-a"})
  for: 15m
  labels:
    severity: ticket
  annotations:
    summary: "Expected spoke dc-a has never pushed to hub (possible mis-config)"
    description: >-
      The spoke_id dc-a has no entry in
      network_topology_federation_spoke_last_push_timestamp_seconds,
      meaning it has not pushed since the hub last started.
      Check spoke configuration, mTLS certs, and hub reachability.
```

### Helm chart integration

The existing `TopologyFederationSpokeDown` alert in `deploy/helm/topology-exporter/templates/prometheusrule.yaml` fires on `network_topology_federation_spoke_up == 0`. Because the series is deleted on eviction (not set to 0), this alert fires during the brief window between eviction and series deletion. The `TopologySpokeDownPage` rule above using `time() - last_push > spoke_timeout` is more reliable because it fires on the timestamp metric, which is not deleted on eviction — it persists until the hub restarts. Consider replacing the existing Helm alert with this form, or running both in parallel.

---

## Recording rules

For dashboard performance and the SLO burn-rate calculations above, add these recording rules to your Prometheus/Mimir rule group:

```yaml
groups:
  - name: network-topology-exporter.slo
    interval: 1m
    rules:
      # SLI 1: cycle headroom at p99 over a 10-minute window
      - record: network_topology:cycle_headroom_p99:ratio
        expr: |
          1 - (
            histogram_quantile(
              0.99,
              sum by (le) (rate(network_topology_discovery_cycle_duration_seconds_bucket[10m]))
            )
            / 60
          )
        # Replace 60 with your discovery.interval in seconds.

      # SLI 2: snapshot drop rate over 1h
      - record: network_topology:snapshot_drops:rate1h
        expr: rate(network_topology_snapshot_drops_total[1h])

      # SLI 3: count of currently evicted spokes per hub instance
      - record: network_topology:federation_spokes_overdue:count
        expr: |
          count by (instance) (
            time() - network_topology_federation_spoke_last_push_timestamp_seconds > 180
          ) or vector(0)
        # Replace 180 with your federation.spoke_timeout in seconds.

      # Uncoordinated mode: cross-boundary link presence with staleness tolerance
      - record: network_topology_confirmed_cross_boundary_link
        expr: |
          count by (peer_a, peer_b, proto) (
            last_over_time(network_topology_boundary_observation_info[3m])
          ) == 2
```

---

## Metric name reference

The following table lists the metric names used in this document and confirms them against `docs/metrics.md` and `internal/metrics/metrics.go`.

| Metric | Source | Used for |
|---|---|---|
| `network_topology_discovery_cycle_duration_seconds` | `DiscoveryCycleDuration` histogram | SLI 1 cycle-duration headroom |
| `network_topology_snapshot_drops_total` | `SnapshotDropsTotal` counter | SLI 2 snapshot-drop rate |
| `network_topology_federation_spoke_up` | `FederationSpokeUp` gauge | Hub spoke liveness (supplemental) |
| `network_topology_federation_spoke_last_push_timestamp_seconds` | `FederationSpokeLastPushUnix` gauge | SLI 3 spoke-down rate |
| `network_topology_boundary_observation_info` | `TopologyCollector` gauge | Uncoordinated cross-boundary link presence |

**Metric names not found / clarifications:**

- `network_topology_snapshot_dropped_total` — this name does **not exist**. The real metric is `network_topology_snapshot_drops_total` (with a `reason` label). The brief in issue #65 used the wrong name; the correct name above was verified in source.
- There is no per-spoke eviction counter emitted on eviction events. The eviction is signalled by the deletion of the `network_topology_federation_spoke_up` series and the age of `network_topology_federation_spoke_last_push_timestamp_seconds`. The PromQL above uses the timestamp metric because it survives eviction (the spoke_up series is deleted on eviction per `hub.go::evictSilentSpokes()`).
