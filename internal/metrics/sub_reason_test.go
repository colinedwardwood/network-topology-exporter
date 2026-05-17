package metrics

import "testing"

// TestSubReasonWireValuesPinned locks the underlying string for every
// PushReason / DiscoveryFailReason / WalkReason constant declared in
// sub_reason.go. These strings appear as the `reason` label on three
// counters: network_topology_otlp_push_total,
// network_topology_discovery_devices_total, and
// network_topology_snmp_walks_total. Changing any value here is a
// breaking observability change: operator dashboards and alert rules
// branch on these strings.
//
// Failures of this test should be paired with a CHANGELOG entry and a
// docs/metrics.md update, not silently fixed by updating `want`.
//
// Issue #20.
func TestSubReasonWireValuesPinned(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
	}{
		// PushReason
		{"PushReasonTimeout", string(PushReasonTimeout), "timeout"},
		{"PushReasonTLSError", string(PushReasonTLSError), "tls_error"},
		{"PushReasonHTTP4xx", string(PushReasonHTTP4xx), "http_4xx"},
		{"PushReasonHTTP5xx", string(PushReasonHTTP5xx), "http_5xx"},
		{"PushReasonPayloadRejected", string(PushReasonPayloadRejected), "payload_rejected"},
		{"PushReasonNetwork", string(PushReasonNetwork), "network"},
		// DiscoveryFailReason
		{"DiscoveryReasonUnreachable", string(DiscoveryReasonUnreachable), "unreachable"},
		{"DiscoveryReasonAuthFailed", string(DiscoveryReasonAuthFailed), "auth_failed"},
		{"DiscoveryReasonTimeout", string(DiscoveryReasonTimeout), "timeout"},
		{"DiscoveryReasonSNMPError", string(DiscoveryReasonSNMPError), "snmp_error"},
		{"DiscoveryReasonMIBUnsupported", string(DiscoveryReasonMIBUnsupported), "mib_unsupported"},
		{"DiscoveryReasonDNSFailed", string(DiscoveryReasonDNSFailed), "dns_failed"},
		{"DiscoveryReasonOutsideAllowList", string(DiscoveryReasonOutsideAllowList), "outside_allow_list"},
		{"DiscoveryReasonNoCredentials", string(DiscoveryReasonNoCredentials), "no_credentials"},
		{"DiscoveryReasonBudgetExpired", string(DiscoveryReasonBudgetExpired), "budget_expired"},
		{"DiscoveryReasonPanic", string(DiscoveryReasonPanic), "panic"},
		// WalkReason
		{"WalkReasonUnreachable", string(WalkReasonUnreachable), "unreachable"},
		{"WalkReasonAuthFailed", string(WalkReasonAuthFailed), "auth_failed"},
		{"WalkReasonSNMPError", string(WalkReasonSNMPError), "snmp_error"},
		{"WalkReasonMIBUnsupported", string(WalkReasonMIBUnsupported), "mib_unsupported"},
		{"WalkReasonDecodeError", string(WalkReasonDecodeError), "decode_error"},
		{"WalkReasonModuleError", string(WalkReasonModuleError), "module_error"},
		// Shared NA sentinel
		{"ReasonNA", ReasonNA, "n/a"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q (wire-format contract — see docs/metrics.md)",
				c.name, c.got, c.want)
		}
	}
}

// TestSubReasonValid confirms Valid() recognises every declared
// constant and rejects unknown strings — the defense-in-depth check
// future emit sites should gate on.
func TestSubReasonValid(t *testing.T) {
	for _, r := range []PushReason{
		ReasonNA,
		PushReasonTimeout, PushReasonTLSError, PushReasonHTTP4xx,
		PushReasonHTTP5xx, PushReasonPayloadRejected, PushReasonNetwork,
	} {
		if !r.Valid() {
			t.Errorf("PushReason(%q).Valid() = false, want true", string(r))
		}
	}
	for _, r := range []DiscoveryFailReason{
		ReasonNA,
		DiscoveryReasonUnreachable, DiscoveryReasonAuthFailed,
		DiscoveryReasonTimeout, DiscoveryReasonSNMPError,
		DiscoveryReasonMIBUnsupported, DiscoveryReasonDNSFailed,
		DiscoveryReasonOutsideAllowList, DiscoveryReasonNoCredentials,
		DiscoveryReasonBudgetExpired, DiscoveryReasonPanic,
	} {
		if !r.Valid() {
			t.Errorf("DiscoveryFailReason(%q).Valid() = false, want true", string(r))
		}
	}
	for _, r := range []WalkReason{
		ReasonNA,
		WalkReasonUnreachable, WalkReasonAuthFailed, WalkReasonSNMPError,
		WalkReasonMIBUnsupported, WalkReasonDecodeError, WalkReasonModuleError,
	} {
		if !r.Valid() {
			t.Errorf("WalkReason(%q).Valid() = false, want true", string(r))
		}
	}

	// Unknown strings must fail Valid().
	for _, s := range []string{"", "n_a", "TIMEOUT", "unknown", "tls"} {
		if PushReason(s).Valid() {
			t.Errorf("PushReason(%q).Valid() = true, want false", s)
		}
		if DiscoveryFailReason(s).Valid() {
			t.Errorf("DiscoveryFailReason(%q).Valid() = true, want false", s)
		}
		if WalkReason(s).Valid() {
			t.Errorf("WalkReason(%q).Valid() = true, want false", s)
		}
	}
}

// TestNAReasonIsShared confirms that the single ReasonNA constant
// satisfies Valid() across all three reason types, so every counter's
// status="ok|success|dropped|timeout" rows use the same literal "n/a"
// label value. Issue #20 invariant.
func TestNAReasonIsShared(t *testing.T) {
	if ReasonNA != "n/a" {
		t.Fatalf("ReasonNA = %q, want %q", ReasonNA, "n/a")
	}
	if !PushReason(ReasonNA).Valid() {
		t.Error("PushReason(ReasonNA).Valid() = false")
	}
	if !DiscoveryFailReason(ReasonNA).Valid() {
		t.Error("DiscoveryFailReason(ReasonNA).Valid() = false")
	}
	if !WalkReason(ReasonNA).Valid() {
		t.Error("WalkReason(ReasonNA).Valid() = false")
	}
}
