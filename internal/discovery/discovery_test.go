package discovery

import (
	"errors"
	"testing"
)

// TestPolicyErrorError covers the Error() method with and without a wrapped error.
func TestPolicyErrorError(t *testing.T) {
	t.Run("with_wrapped_error", func(t *testing.T) {
		inner := errors.New("inner cause")
		pe := &PolicyError{Module: "lldp", Reason: "missing_table", Err: inner}
		got := pe.Error()
		if got != "lldp policy failure: missing_table: inner cause" {
			t.Errorf("Error() = %q, want %q", got, "lldp policy failure: missing_table: inner cause")
		}
	})

	t.Run("without_wrapped_error", func(t *testing.T) {
		pe := &PolicyError{Module: "fdb", Reason: "required_table_partial_decode", Err: nil}
		got := pe.Error()
		if got != "fdb policy failure: required_table_partial_decode" {
			t.Errorf("Error() = %q, want %q", got, "fdb policy failure: required_table_partial_decode")
		}
	})
}

// TestPolicyErrorUnwrap covers Unwrap() returning the wrapped error or nil.
func TestPolicyErrorUnwrap(t *testing.T) {
	t.Run("non_nil_err", func(t *testing.T) {
		inner := errors.New("root cause")
		pe := &PolicyError{Module: "bgp", Reason: "some_reason", Err: inner}
		if got := pe.Unwrap(); got != inner {
			t.Errorf("Unwrap() = %v, want %v", got, inner)
		}
	})

	t.Run("nil_err", func(t *testing.T) {
		pe := &PolicyError{Module: "ospf", Reason: "some_reason", Err: nil}
		if got := pe.Unwrap(); got != nil {
			t.Errorf("Unwrap() = %v, want nil", got)
		}
	})
}

// TestJoinReasonCodes covers all branches of JoinReasonCodes.
func TestJoinReasonCodes(t *testing.T) {
	cases := []struct {
		name  string
		input []string
		want  string
	}{
		{
			name:  "empty_input",
			input: []string{},
			want:  "",
		},
		{
			name:  "single_reason",
			input: []string{"missing_srcport_mapping"},
			want:  "missing_srcport_mapping",
		},
		{
			name:  "duplicates_deduplicated",
			input: []string{"a", "b", "a"},
			want:  "a,b",
		},
		{
			name:  "empty_strings_skipped",
			input: []string{"a", "", "b"},
			want:  "a,b",
		},
		{
			name:  "all_blanks",
			input: []string{"", ""},
			want:  "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := JoinReasonCodes(tc.input)
			if got != tc.want {
				t.Errorf("JoinReasonCodes(%v) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// TestEnumValid asserts every declared enum constant reports Valid()==true and
// that an arbitrary unknown value reports false. Guards against accidental
// drift between the const set and the Valid() switch on either side.
func TestEnumValid(t *testing.T) {
	t.Run("DiscoveryProtocol", func(t *testing.T) {
		for _, p := range []DiscoveryProtocol{
			DiscoveryProtocolLLDP, DiscoveryProtocolCDP, DiscoveryProtocolBGP,
			DiscoveryProtocolOSPF, DiscoveryProtocolFDB, DiscoveryProtocolISIS,
			DiscoveryProtocolMPLSTE, DiscoveryProtocolConfigured,
		} {
			if !p.Valid() {
				t.Errorf("%q should be Valid()", p)
			}
		}
		if DiscoveryProtocol("not-a-protocol").Valid() {
			t.Error("unknown DiscoveryProtocol should be !Valid()")
		}
		if DiscoveryProtocol("").Valid() {
			t.Error("empty DiscoveryProtocol should be !Valid()")
		}
	})

	t.Run("LinkKind", func(t *testing.T) {
		for _, k := range []LinkKind{
			LinkKindEthernet, LinkKindMPLSTE, LinkKindIP, LinkKindLogical,
		} {
			if !k.Valid() {
				t.Errorf("%q should be Valid()", k)
			}
		}
		if LinkKind("fiber").Valid() {
			t.Error("unknown LinkKind should be !Valid()")
		}
	})

	t.Run("Direction", func(t *testing.T) {
		for _, d := range []Direction{DirectionBidirectional, DirectionUnidirectional} {
			if !d.Valid() {
				t.Errorf("%q should be Valid()", d)
			}
		}
		if Direction("sideways").Valid() {
			t.Error("unknown Direction should be !Valid()")
		}
	})

	t.Run("Confidence", func(t *testing.T) {
		for _, c := range []Confidence{ConfidenceHigh, ConfidenceMedium, ConfidenceLow} {
			if !c.Valid() {
				t.Errorf("%q should be Valid()", c)
			}
		}
		if Confidence("certain").Valid() {
			t.Error("unknown Confidence should be !Valid()")
		}
	})

	t.Run("Adjacency", func(t *testing.T) {
		for _, a := range []Adjacency{AdjacencyDirect, AdjacencyIndirect, AdjacencyUnknown} {
			if !a.Valid() {
				t.Errorf("%q should be Valid()", a)
			}
		}
		if Adjacency("physical").Valid() {
			t.Error("unknown Adjacency should be !Valid()")
		}
	})
}

// TestEnumWireFormat pins each enum constant to the exact underlying string
// emitted on the wire. These strings are persisted (snapshot.json) and
// emitted (Prometheus labels, OTLP attributes); changing them is a
// downstream-breaking event. If a constant value must change, fail this
// test deliberately and bump the snapshot schema version.
func TestEnumWireFormat(t *testing.T) {
	cases := []struct {
		got, want string
	}{
		{string(DiscoveryProtocolLLDP), "lldp"},
		{string(DiscoveryProtocolCDP), "cdp"},
		{string(DiscoveryProtocolBGP), "bgp"},
		{string(DiscoveryProtocolOSPF), "ospf"},
		{string(DiscoveryProtocolFDB), "fdb"},
		{string(DiscoveryProtocolISIS), "isis"},
		{string(DiscoveryProtocolMPLSTE), "mpls_te"},
		{string(DiscoveryProtocolConfigured), "configured"},
		{string(LinkKindEthernet), "ethernet"},
		{string(LinkKindMPLSTE), "mpls-te"},
		{string(LinkKindIP), "ip"},
		{string(LinkKindLogical), "logical"},
		{string(DirectionBidirectional), "bidirectional"},
		{string(DirectionUnidirectional), "unidirectional"},
		{string(ConfidenceHigh), "high"},
		{string(ConfidenceMedium), "medium"},
		{string(ConfidenceLow), "low"},
		{string(AdjacencyDirect), "direct"},
		{string(AdjacencyIndirect), "indirect"},
		{string(AdjacencyUnknown), "unknown"},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("wire format drift: got %q, want %q", tc.got, tc.want)
		}
	}
}

// TestEnumString confirms String() returns the underlying wire value so
// fmt.Stringer-based formatting (e.g. slog) emits the same bytes that
// serialise through json.Marshal.
func TestEnumString(t *testing.T) {
	if got := DiscoveryProtocolBGP.String(); got != "bgp" {
		t.Errorf("DiscoveryProtocolBGP.String() = %q, want %q", got, "bgp")
	}
	if got := LinkKindEthernet.String(); got != "ethernet" {
		t.Errorf("LinkKindEthernet.String() = %q, want %q", got, "ethernet")
	}
	if got := DirectionBidirectional.String(); got != "bidirectional" {
		t.Errorf("DirectionBidirectional.String() = %q, want %q", got, "bidirectional")
	}
	if got := ConfidenceMedium.String(); got != "medium" {
		t.Errorf("ConfidenceMedium.String() = %q, want %q", got, "medium")
	}
	if got := AdjacencyDirect.String(); got != "direct" {
		t.Errorf("AdjacencyDirect.String() = %q, want %q", got, "direct")
	}
}
