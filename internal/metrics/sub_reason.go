package metrics

// Sub-reason label values for the three "status"-shaped counters partitioned
// in issue #20:
//
//   - network_topology_otlp_push_total{status, reason}
//   - network_topology_discovery_devices_total{status, reason}
//   - network_topology_snmp_walks_total{status, reason}
//
// Each set is a closed enum. Free-form strings must not reach
// WithLabelValues — that would push cardinality into the unbounded series
// regime. Use the typed constants below and gate emission with Valid() so
// a typo at a future call site fails a test instead of producing a new
// silently-registered series.
//
// Wire-format guarantee: the underlying string for each constant is the
// stable label value. Renaming a constant is a Go-only refactor; changing
// its literal is a breaking observability change and must be paired with
// a CHANGELOG entry and an update to docs/metrics.md.
//
// The shared sentinel for ok/dropped/success rows that have no failure
// reason is metrics.ReasonNA ("n/a"). Tests pin this so it does not drift.

// PushReason classifies a sub-reason for an OTLP push attempt. Combined
// with the existing status label (ok | error | dropped) it forms the two
// dimensions of network_topology_otlp_push_total.
//
// Categorisation lives in internal/output/otlp/otlp.go::ClassifyPushError
// so the call site does not need to inspect error text. status="ok" and
// status="dropped" rows carry reason="n/a".
type PushReason string

// Valid reports whether r is one of the declared PushReason constants.
func (r PushReason) Valid() bool {
	switch r {
	case ReasonNA,
		PushReasonTimeout,
		PushReasonTLSError,
		PushReasonHTTP4xx,
		PushReasonHTTP5xx,
		PushReasonPayloadRejected,
		PushReasonNetwork:
		return true
	}
	return false
}

// String implements fmt.Stringer so values format identically in logs.
func (r PushReason) String() string { return string(r) }

// PushReason values. PushReasonNetwork covers the residual class of
// transport errors that are not TLS-handshake failures and not
// context-deadline timeouts — endpoint unreachable, DNS, connection
// reset, EOF mid-response. Use it as the catch-all when nothing more
// specific matches; sustained PushReasonNetwork without other signals
// is the "collector probably down" case from the issue body.
//
// PushReasonPayloadRejected is reserved for 4xx responses that indicate
// the receiver parsed our request but rejected its contents (schema
// violations, mTLS allow-list miss, etc.); today's classifier folds all
// 4xx into PushReasonHTTP4xx, but the constant is declared so a future
// receiver-specific classifier can split it without another breaking
// label change.
const (
	PushReasonTimeout         PushReason = "timeout"
	PushReasonTLSError        PushReason = "tls_error"
	PushReasonHTTP4xx         PushReason = "http_4xx"
	PushReasonHTTP5xx         PushReason = "http_5xx"
	PushReasonPayloadRejected PushReason = "payload_rejected"
	PushReasonNetwork         PushReason = "network"
)

// DiscoveryFailReason classifies a per-device discovery-cycle outcome
// sub-reason. Combined with the existing status label (success | failed)
// it forms the two dimensions of network_topology_discovery_devices_total.
//
// status="success" rows carry reason="n/a".
//
// DiscoveryReasonNoCredentials covers the case where no usable
// credential profile matched the target before any SNMP packet was
// sent; this is operationally distinct from auth_failed (a profile
// was tried and rejected by the device) and from unreachable
// (no network connectivity).
//
// DiscoveryReasonDNSFailed and DiscoveryReasonOutsideAllowList cover the
// two pre-walk gates in cmd/topology-exporter/main.go: DNS resolution and
// the CIDR allow-list. The previous status-only counter conflated these
// with post-walk failures.
//
// DiscoveryReasonBudgetExpired covers the cycle-budget skip path. It is
// distinct from timeout (a per-device deadline) because the discovery
// loop never got the chance to start the probe — the cycle ran out of
// wall time first.
type DiscoveryFailReason string

// Valid reports whether r is one of the declared DiscoveryFailReason
// constants.
func (r DiscoveryFailReason) Valid() bool {
	switch r {
	case ReasonNA,
		DiscoveryReasonUnreachable,
		DiscoveryReasonAuthFailed,
		DiscoveryReasonTimeout,
		DiscoveryReasonSNMPError,
		DiscoveryReasonMIBUnsupported,
		DiscoveryReasonDNSFailed,
		DiscoveryReasonOutsideAllowList,
		DiscoveryReasonNoCredentials,
		DiscoveryReasonBudgetExpired,
		DiscoveryReasonPanic:
		return true
	}
	return false
}

// String implements fmt.Stringer.
func (r DiscoveryFailReason) String() string { return string(r) }

// DiscoveryFailReason values.
const (
	DiscoveryReasonUnreachable      DiscoveryFailReason = "unreachable"
	DiscoveryReasonAuthFailed       DiscoveryFailReason = "auth_failed"
	DiscoveryReasonTimeout          DiscoveryFailReason = "timeout"
	DiscoveryReasonSNMPError        DiscoveryFailReason = "snmp_error"
	DiscoveryReasonMIBUnsupported   DiscoveryFailReason = "mib_unsupported"
	DiscoveryReasonDNSFailed        DiscoveryFailReason = "dns_failed"
	DiscoveryReasonOutsideAllowList DiscoveryFailReason = "outside_allow_list"
	DiscoveryReasonNoCredentials    DiscoveryFailReason = "no_credentials"
	DiscoveryReasonBudgetExpired    DiscoveryFailReason = "budget_expired"
	DiscoveryReasonPanic            DiscoveryFailReason = "panic"
)

// WalkReason classifies an SNMP-walk-attempt sub-reason. Combined with
// the existing status label (ok | timeout | error) it forms the two
// dimensions of network_topology_snmp_walks_total.
//
// status="ok" rows carry reason="n/a". status="timeout" rows also carry
// reason="n/a" because the status itself is already the reason — a
// dedicated reason would just duplicate signal. The reason label
// disambiguates the status="error" rows: did the walk fail with an
// authentication error, an SNMP-level error response (e.g. noSuchName,
// genErr), or a transport-level error?
type WalkReason string

// Valid reports whether r is one of the declared WalkReason constants.
func (r WalkReason) Valid() bool {
	switch r {
	case ReasonNA,
		WalkReasonUnreachable,
		WalkReasonAuthFailed,
		WalkReasonSNMPError,
		WalkReasonMIBUnsupported,
		WalkReasonDecodeError,
		WalkReasonModuleError:
		return true
	}
	return false
}

// String implements fmt.Stringer.
func (r WalkReason) String() string { return string(r) }

// WalkReason values. WalkReasonModuleError is the catch-all for a
// per-module walk that failed for a non-deadline, non-transport reason
// — the device responded but the module's required tables were
// malformed or missing in a way the walker treated as fatal. Sustained
// non-zero WalkReasonModuleError on a specific module is a signal that
// the walker may need vendor-specific tuning; cross-check against
// network_topology_discovery_hard_fail_total{module=...,reason=...}
// for the specific cause.
const (
	WalkReasonUnreachable    WalkReason = "unreachable"
	WalkReasonAuthFailed     WalkReason = "auth_failed"
	WalkReasonSNMPError      WalkReason = "snmp_error"
	WalkReasonMIBUnsupported WalkReason = "mib_unsupported"
	WalkReasonDecodeError    WalkReason = "decode_error"
	WalkReasonModuleError    WalkReason = "module_error"
)

// ReasonNA is the shared sentinel value for the "no failure" rows of
// the three counters above. Using a single shared constant lets tests
// assert one invariant ("ok/success/dropped rows always carry reason=n/a")
// instead of three duplicated checks. The literal "n/a" is the
// wire-format contract; see issue #20.
const ReasonNA = "n/a"
