// Package metrics declares every Prometheus metric the exporter emits.
//
// All metrics are registered against a single registry exposed on /metrics.
// Full label reference: docs/metrics.md.
//
// Cardinality rules:
//   - No metric uses a raw IP address as a label value.
//   - Interface names appear only on edge series (src_port, dst_port), where
//     they are scoped to a specific device pair — not standalone.
//   - neighbour_hint is free-form text and lives in log lines, not labels.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// Metrics bundles every collector the exporter exposes. One instance per
// process; passed into the discovery loop so it can update series.
type Metrics struct {
	registry *prometheus.Registry

	// Topology is a custom Collector that holds an atomic graph snapshot and
	// generates device, edge, uptime, OOS-count, and boundary-observation
	// metrics on-the-fly at scrape time (no Reset race).
	Topology *TopologyCollector

	// Event-driven counters and histograms — updated by the discovery loop,
	// not derived from the graph snapshot.
	TopologyChangeTotal   *prometheus.CounterVec
	TopologyConflictTotal *prometheus.CounterVec

	// LD-13: graph freshness signals.
	GraphStale                 prometheus.Gauge
	SnapshotLastWrittenUnix    prometheus.Gauge
	SnapshotLoadedDevicesTotal prometheus.Gauge

	// Discovery cycle health. All aggregates — no per-device label values.
	DiscoveryDevicesTotal         *prometheus.GaugeVec
	DiscoveryCycleDuration        prometheus.Histogram
	DiscoveryModuleDuration       *prometheus.HistogramVec
	SNMPWalksTotal                *prometheus.CounterVec
	DiscoveryDecodeIssues         *prometheus.CounterVec
	DiscoveryQuarantinedRowsTotal *prometheus.CounterVec
	DiscoveryDegradedTotal        *prometheus.CounterVec
	DiscoveryHardFailTotal        *prometheus.CounterVec
	ModuleLastStatus              *prometheus.GaugeVec
	CredentialTrialsTotal         *prometheus.CounterVec
	OTLPPushTotal                 *prometheus.CounterVec

	// LD-16/LD-18: hub-mode spoke-liveness signals. Only populated when
	// federation.role is hub; registered always so the metric is present
	// in the schema even on non-hub instances.
	FederationSpokeUp           *prometheus.GaugeVec // {spoke_id} — 1 active, 0 evicted
	FederationSpokeLastPushUnix *prometheus.GaugeVec // {spoke_id}

	// LD-17: spoke-mode push failure counter. Incremented when all retries
	// are exhausted. Alert on rate > 0 to detect silent channel breakage.
	FederationSpokePushFailuresTotal prometheus.Counter

	// GraphUpdatesRejectedTotal counts combined-graph updates rejected at
	// publish time, partitioned by reason. Reason label values are the
	// underlying strings of the RejectReason constants declared in
	// reject_reason.go (size_budget_exceeded, invalid_label_key,
	// invalid_label_value, structural_invalid, stale_generation). Emission
	// sites must call WithLabelValues(string(reject.X)) — the typed
	// constants keep label values in sync with the federation pushRejection
	// JSON wire format. New reasons land alongside the emission site that
	// introduces them; deprecated values are removed in a major version.
	// Operators alert on
	// rate(network_topology_graph_updates_rejected_total[5m]) > 0 to detect
	// any reject pattern, and on the per-reason breakdown for triage.
	GraphUpdatesRejectedTotal *prometheus.CounterVec

	// Hub OOS matching health: non-zero means cross-domain auto-detection is
	// partially failing; operators should add known_inter_domain_links entries.
	HubOOSUnmatchedTotal prometheus.Gauge

	// Scrape-time SLO signals — updated by TopologyCollector on every scrape.
	TopologyLastScrapeDurationSeconds prometheus.Gauge
	TopologyLastScrapeSamplesTotal    prometheus.Gauge

	// Scrape-time scale signals (LD-XX scale-ceiling instrumentation). The
	// gauges above carry only the last value; these histograms surface the
	// distribution so operators can alert on p99 against their configured
	// scrape_timeout. See docs/operator/scale.md for guidance on alerting
	// against these and the three escape hatches when the curves trend high.
	MetricsRenderDuration prometheus.Histogram
	MetricsPayloadBytes   prometheus.Histogram

	// FDBSuppressedMACs counts FDB MAC peers dropped because no LLDP chassis
	// MAC correlation was found; these were not host MACs that passed the
	// single-learned-MAC filter.
	FDBSuppressedMACs prometheus.Counter

	// GoRoutines is the current number of live goroutines in the process,
	// sampled once per discovery cycle.
	GoRoutines prometheus.Gauge

	// SnapshotQueueDepth is the current number of snapshots queued for
	// writing (0 or 1 for the capacity-1 channel).
	SnapshotQueueDepth prometheus.Gauge

	// SnapshotDropsTotal counts snapshot writes that were dropped because
	// the snapshot pipeline could not absorb them. Partitioned by `reason`:
	//   - queue_full:      caller couldn't enqueue (snapshot channel full)
	//   - write_in_flight: writer found previous write still pending
	//
	// Both reasons surface the same underlying condition (storage stall
	// preventing the previous snapshot from completing) at different
	// layers; they are kept distinct so operators can tell whether the
	// upstream cycle has started outpacing the writer, or the writer
	// itself is stalled. Issue #42.
	SnapshotDropsTotal *prometheus.CounterVec

	// CycleBudgetSkipsTotal counts targets that were never polled in a discovery
	// cycle because the cycle budget deadline expired before their goroutine could
	// acquire the parallelism semaphore.
	CycleBudgetSkipsTotal prometheus.Counter

	// AdminRediscoveryTotal counts per-target outcomes of the admin
	// out-of-cycle re-discovery endpoint (POST /admin/rediscover). Labelled by
	// outcome ∈ {success, timeout, auth_failure, out_of_scope, error}. One
	// increment per target per admin request; an audit trail of forced walks.
	// Operators can alert on outcome="auth_failure" rate to spot a misconfigured
	// rediscover client. Issue #73.
	AdminRediscoveryTotal *prometheus.CounterVec

	// BGPWalkerOutcomeTotal counts the outcome of each BGP walker pass.
	// Labels:
	//   walker  ∈ {vendor_cisco, vendor_arista, vendor_juniper, vendor_nokia, rfc4273}
	//   outcome ∈ {edges, no_peers, mib_unimplemented, walker_drift, error, malformed_index}
	// One counter per (walker, outcome) is incremented per device per cycle.
	// Semantics:
	//   - "edges" — walker produced at least one established-peer row
	//   - "mib_unimplemented" — BulkWalk returned zero PDUs; the device does
	//     not implement the table at all (expected on non-BGP devices, must
	//     not page)
	//   - "no_peers" — PDUs arrived AND at least one row decoded cleanly,
	//     but no peer reached bgpStateEstablished — BGP is configured but
	//     every session is down (operationally distinct from
	//     mib_unimplemented; this is the correct signal for "BGP broken"
	//     alerts)
	//   - "walker_drift" — PDUs arrived but EVERY row was rejected by the
	//     vendor decoder. The device DOES implement the MIB, our decoder
	//     just doesn't match. Page-level signal that the walker is broken
	//     on this vendor's MIB; operationally distinct from no_peers (which
	//     means BGP itself is broken) and from mib_unimplemented (which is
	//     expected on non-BGP devices). Added in issue #27.
	//   - "error" — the SNMP walk itself failed
	//   - "malformed_index" — incremented per dropped row inside a walker
	//     that rejected a row via decodeBgp4V2Index
	// Issue #15: the previous "empty" outcome was split into "no_peers" and
	// "mib_unimplemented". Operator alerts on outcome="empty" must migrate
	// to outcome="no_peers".
	// Issue #27 (breaking): the all-rows-malformed sub-case of the prior
	// "no_peers" semantics was hoisted to its own "walker_drift" outcome.
	// Operator alerts on outcome="no_peers" that expected to fire on
	// "every row was decoder-rejected" must migrate to
	// outcome="walker_drift".
	BGPWalkerOutcomeTotal *prometheus.CounterVec

	// WalkerOutcomeTotal counts the outcome of each non-BGP protocol walker
	// pass (LLDP, CDP, OSPF, FDB). It is the generic sibling of
	// BGPWalkerOutcomeTotal — kept as a SEPARATE metric so the BGP series
	// operators already alert on is never renamed (issue #98).
	// Labels:
	//   walker  ∈ {lldp, cdp, ospf, fdb}
	//   outcome ∈ {edges, mib_unimplemented, no_neighbours, walker_drift, error}
	// One (walker, outcome) counter is incremented per device per cycle.
	// Semantics mirror the BGP four-bucket categorisation (see
	// BGPWalkerOutcomeTotal and internal/discovery/bgp/bgp.go):
	//   - "edges" — the walk produced at least one discovery.Edge.
	//   - "mib_unimplemented" — the base table BulkWalk returned zero PDUs;
	//     the device does not implement the MIB at all. Expected on
	//     non-applicable devices (e.g. CDP on a non-Cisco device); MUST NOT
	//     page.
	//   - "no_neighbours" — PDUs arrived AND at least one row decoded
	//     cleanly, but zero usable edges resulted (e.g. neighbours present
	//     but none in an up/usable state, or all filtered out of scope).
	//     Operationally distinct from mib_unimplemented: the MIB is
	//     implemented, the protocol is up, there is simply nothing to report.
	//   - "walker_drift" — PDUs arrived but EVERY row was rejected by the
	//     decoder (none decoded cleanly), zero edges. The MIB IS implemented
	//     but our decoder doesn't match this firmware. Page-level signal that
	//     the walker is broken on this device; distinct from no_neighbours
	//     (which assumes at least one clean decode) and mib_unimplemented
	//     (which is expected on non-applicable devices).
	//   - "error" — the walk itself returned a non-nil error (SNMP failure).
	WalkerOutcomeTotal *prometheus.CounterVec

	// SystemWalkAnomalyTotal counts SNMP system-group walk outcomes that
	// silently degrade downstream behaviour (issue #101). Low cardinality by
	// construction: the ONLY label is reason, a closed two-value set —
	//   - "empty_sysname"   — sysName was empty/garbage so the device ID fell
	//     back to the management IP (unstable across re-addressing).
	//   - "unknown_vendor"  — the sysObjectID resolved to no known vendor, so
	//     Vendor stays "unknown" and the vendor-specific BGP4-V2 walker is
	//     skipped (BGP still falls through to the observable RFC 4273 path).
	// No device, IP, or sysObjectID is ever a label value here.
	SystemWalkAnomalyTotal *prometheus.CounterVec
}

// New builds and registers the exporter's metric set. emitBoundaryObs should
// be true only when federation.role is "uncoordinated".
func New(emitBoundaryObs bool) *Metrics {
	reg := prometheus.NewRegistry()

	reg.MustRegister(collectors.NewGoCollector())
	reg.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	m := &Metrics{
		registry: reg,
		TopologyChangeTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "network_topology_change_total",
			Help: "Topology mutations between discovery cycles. Resets on restart; use increase() not rate().",
		}, []string{"change_kind", "discovery_proto"}),
		TopologyConflictTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "network_topology_conflict_total",
			Help: "Source disagreements detected during reconciliation, by conflict type.",
		}, []string{"conflict_type"}),
		GraphStale: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "network_topology_graph_stale",
			Help: "1 while serving a stale snapshot loaded from disk at startup; 0 once the first live cycle completes.",
		}),
		SnapshotLastWrittenUnix: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "network_topology_snapshot_last_written_timestamp_seconds",
			Help: "Unix timestamp in seconds of the most recent successful snapshot write.",
		}),
		SnapshotLoadedDevicesTotal: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "network_topology_snapshot_loaded_devices_total",
			Help: "Devices loaded from the on-disk snapshot at startup.",
		}),
		DiscoveryDevicesTotal: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "network_topology_discovery_devices_total",
			Help: "Per-cycle device-discovery outcome count, partitioned by status and sub-reason. Reason values are the underlying strings of the DiscoveryFailReason constants in sub_reason.go; status=success rows carry reason=n/a.",
		}, []string{"status", "reason"}), // status ∈ {success, failed}; reason ∈ DiscoveryFailReason
		DiscoveryCycleDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "network_topology_discovery_cycle_duration_seconds",
			Help:    "End-to-end discovery cycle wall time.",
			Buckets: prometheus.ExponentialBuckets(0.5, 2, 10),
		}),
		DiscoveryModuleDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "network_topology_discovery_module_duration_seconds",
			Help:    "Per-module wall time within a discovery cycle.",
			Buckets: prometheus.ExponentialBuckets(0.05, 2, 10),
		}, []string{"module"}),
		SNMPWalksTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "network_topology_snmp_walks_total",
			Help: "SNMP walk attempts, partitioned by terminal status and sub-reason. Reason values are the underlying strings of the WalkReason constants in sub_reason.go; status=ok and status=timeout rows carry reason=n/a.",
		}, []string{"status", "reason"}), // status ∈ {ok, timeout, error}; reason ∈ WalkReason
		DiscoveryDecodeIssues: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "network_topology_discovery_decode_issues_total",
			Help: "SNMP decode anomalies by module, OID, and reason.",
		}, []string{"module", "oid", "reason"}),
		DiscoveryQuarantinedRowsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "network_topology_discovery_quarantined_rows_total",
			Help: "SNMP table rows quarantined due to decode anomalies, by module, OID, and reason.",
		}, []string{"module", "oid", "reason"}),
		DiscoveryDegradedTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "network_topology_discovery_degraded_total",
			Help: "Discovery module runs that completed in degraded mode by reason.",
		}, []string{"module", "reason"}),
		DiscoveryHardFailTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "network_topology_discovery_hard_fail_total",
			Help: "Discovery hard failures by module and policy/runtime reason.",
		}, []string{"module", "reason"}),
		ModuleLastStatus: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "network_topology_module_last_status",
			Help: "Status of each discovery module in the most recent cycle: 0=ok, 1=degraded (partial results), 2=failed (no results).",
		}, []string{"module"}),
		CredentialTrialsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "network_topology_credential_trials_total",
			Help: "Credential trial attempts against polled devices; tracks auth success and failure rates.",
		}, []string{"status"}), // ok | failed
		OTLPPushTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "network_topology_otlp_push_total",
			Help: "OTLP push attempts by status and sub-reason. status ∈ {ok, error, dropped}; reason values are the underlying strings of the PushReason constants in sub_reason.go. status=ok and status=dropped rows carry reason=n/a. Alert on error or dropped rate > 0.",
		}, []string{"status", "reason"}),
		FederationSpokeUp: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "network_topology_federation_spoke_up",
			Help: "Hub mode: 1 while a spoke is active (last push within federation.spoke_timeout); 0 after eviction.",
		}, []string{"spoke_id"}),
		FederationSpokeLastPushUnix: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "network_topology_federation_spoke_last_push_timestamp_seconds",
			Help: "Hub mode: Unix timestamp in seconds of the most recent push from each spoke.",
		}, []string{"spoke_id"}),
		FederationSpokePushFailuresTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "network_topology_federation_spoke_push_failures_total",
			Help: "Total number of spoke push attempts that failed after all retries. Alert on rate > 0.",
		}),
		GraphUpdatesRejectedTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "network_topology_graph_updates_rejected_total",
			Help: "Combined-graph updates rejected at publish time, partitioned by reason (size_budget_exceeded, invalid_label_key, invalid_label_value).",
		}, []string{"reason"}),
		HubOOSUnmatchedTotal: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "network_topology_hub_oos_unmatched_total",
			Help: "Count of OOS neighbour observations with no reverse match in the last hub graph rebuild. Non-zero means cross-domain auto-detection is partially failing; add known_inter_domain_links entries.",
		}),
		TopologyLastScrapeDurationSeconds: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "network_topology_last_scrape_duration_seconds",
			Help: "Time taken to render all topology metrics at the last Prometheus scrape.",
		}),
		TopologyLastScrapeSamplesTotal: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "network_topology_last_scrape_samples_total",
			Help: "Number of metric samples emitted at the last Prometheus scrape. Alert when this approaches scrape_timeout capacity.",
		}),
		FDBSuppressedMACs: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "network_topology_fdb_suppressed_macs_total",
			Help: "FDB MAC peers dropped because no LLDP chassis MAC correlation was found; these were not host MACs that passed the single-learned-MAC filter.",
		}),
		GoRoutines: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "network_topology_goroutines",
			Help: "Current number of live goroutines in the process.",
		}),
		SnapshotQueueDepth: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "network_topology_snapshot_queue_depth",
			Help: "Current number of snapshots queued for writing.",
		}),
		SnapshotDropsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "network_topology_snapshot_drops_total",
			Help: "Snapshot writes dropped because the pipeline could not absorb them. reason ∈ {queue_full, write_in_flight}.",
		}, []string{"reason"}),
		CycleBudgetSkipsTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "network_topology_cycle_budget_skips_total",
			Help: "Targets skipped in a discovery cycle because the cycle budget deadline expired before their goroutine could start.",
		}),
		AdminRediscoveryTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "network_topology_admin_rediscovery_total",
			Help: "Per-target outcomes of forced out-of-cycle re-discovery via POST /admin/rediscover. outcome ∈ {success, timeout, auth_failure, out_of_scope, error}.",
		}, []string{"outcome"}),
		MetricsRenderDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "network_topology_metrics_render_duration_seconds",
			Help: "Wall time to render one /metrics scrape response. Alert at p99 against the scraper's scrape_timeout.",
			// 1ms .. ~32s. Covers a typical small instance (<10ms) through a
			// stressed 50k-edge response (multi-second) up to a clearly-broken
			// scrape that will be killed by the timeout.
			Buckets: prometheus.ExponentialBuckets(0.001, 2, 16),
		}),
		BGPWalkerOutcomeTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "network_topology_bgp_walker_outcome_total",
			Help: "BGP walker pass outcomes. walker ∈ {vendor_cisco, vendor_arista, vendor_juniper, vendor_nokia, rfc4273}; outcome ∈ {edges, mib_unimplemented, no_peers, walker_drift, malformed_index, error}.",
		}, []string{"walker", "outcome"}),
		WalkerOutcomeTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "network_topology_walker_outcome_total",
			Help: "Non-BGP protocol walker pass outcomes. walker ∈ {lldp, cdp, ospf, fdb}; outcome ∈ {edges, mib_unimplemented, no_neighbours, walker_drift, error}.",
		}, []string{"walker", "outcome"}),
		SystemWalkAnomalyTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "network_topology_system_walk_anomaly_total",
			Help: "SNMP system-group walk outcomes that silently degrade downstream behaviour. reason ∈ {empty_sysname, unknown_vendor}.",
		}, []string{"reason"}),
		MetricsPayloadBytes: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "network_topology_metrics_payload_bytes",
			Help: "Response body size of one /metrics scrape in bytes. Tracks growth as the topology scales.",
			// 1KB .. ~64MB. Covers tiny test responses through realistic large
			// production payloads. Buckets are wide because the distribution
			// per instance is typically narrow but absolute scale varies hugely.
			Buckets: prometheus.ExponentialBuckets(1024, 4, 9),
		}),
	}
	m.Topology = newTopologyCollector(emitBoundaryObs, m.TopologyLastScrapeDurationSeconds, m.TopologyLastScrapeSamplesTotal)

	reg.MustRegister(
		m.Topology,
		m.TopologyChangeTotal,
		m.TopologyConflictTotal,
		m.GraphStale,
		m.SnapshotLastWrittenUnix,
		m.SnapshotLoadedDevicesTotal,
		m.DiscoveryDevicesTotal,
		m.DiscoveryCycleDuration,
		m.DiscoveryModuleDuration,
		m.SNMPWalksTotal,
		m.DiscoveryDecodeIssues,
		m.DiscoveryQuarantinedRowsTotal,
		m.DiscoveryDegradedTotal,
		m.DiscoveryHardFailTotal,
		m.ModuleLastStatus,
		m.CredentialTrialsTotal,
		m.OTLPPushTotal,
		m.FederationSpokeUp,
		m.FederationSpokeLastPushUnix,
		m.FederationSpokePushFailuresTotal,
		m.GraphUpdatesRejectedTotal,
		m.HubOOSUnmatchedTotal,
		m.TopologyLastScrapeDurationSeconds,
		m.TopologyLastScrapeSamplesTotal,
		m.FDBSuppressedMACs,
		m.GoRoutines,
		m.SnapshotQueueDepth,
		m.SnapshotDropsTotal,
		m.CycleBudgetSkipsTotal,
		m.AdminRediscoveryTotal,
		m.MetricsRenderDuration,
		m.MetricsPayloadBytes,
		m.BGPWalkerOutcomeTotal,
		m.WalkerOutcomeTotal,
		m.SystemWalkAnomalyTotal,
	)

	return m
}

// Registry returns the underlying Prometheus registry, for use by promhttp.
func (m *Metrics) Registry() *prometheus.Registry {
	return m.registry
}
