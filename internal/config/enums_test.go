package config

import "testing"

func TestRoleValid(t *testing.T) {
	cases := []struct {
		in   Role
		want bool
	}{
		{RoleStandalone, true},
		{RoleUncoordinated, true},
		{RoleSpoke, true},
		{RoleHub, true},
		{"", false},
		{"bogus", false},
	}
	for _, tc := range cases {
		if got := tc.in.Valid(); got != tc.want {
			t.Errorf("Role(%q).Valid() = %v, want %v", string(tc.in), got, tc.want)
		}
	}
}

func TestOTLPProtocolValid(t *testing.T) {
	cases := []struct {
		in   OTLPProtocol
		want bool
	}{
		{OTLPProtocolHTTP, true},
		{OTLPProtocolGRPC, true},
		{"", false},
		{"tcp", false},
	}
	for _, tc := range cases {
		if got := tc.in.Valid(); got != tc.want {
			t.Errorf("OTLPProtocol(%q).Valid() = %v, want %v", string(tc.in), got, tc.want)
		}
	}
}

func TestSNMPVersionValid(t *testing.T) {
	cases := []struct {
		in   SNMPVersion
		want bool
	}{
		{SNMPVersionV2c, true},
		{SNMPVersionV3, true},
		{"", false},
		{"v1", false},
	}
	for _, tc := range cases {
		if got := tc.in.Valid(); got != tc.want {
			t.Errorf("SNMPVersion(%q).Valid() = %v, want %v", string(tc.in), got, tc.want)
		}
	}
}

func TestProfileTypeValid(t *testing.T) {
	cases := []struct {
		in   ProfileType
		want bool
	}{
		{ProfileTypeSNMPv2c, true},
		{ProfileTypeSNMPv3, true},
		{"", false},
		{"snmp_v1", false},
	}
	for _, tc := range cases {
		if got := tc.in.Valid(); got != tc.want {
			t.Errorf("ProfileType(%q).Valid() = %v, want %v", string(tc.in), got, tc.want)
		}
	}
}
