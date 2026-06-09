package config

// This file defines the named string types for the four config enums. The
// underlying string values are YAML wire-contract values and MUST stay
// byte-identical; the named types add compile-time safety and a Valid() helper
// without changing any unmarshalled wire value.

// Role selects the federation operating mode (LD-15–LD-20).
type Role string

// Federation role wire values.
const (
	RoleStandalone    Role = "standalone"
	RoleUncoordinated Role = "uncoordinated"
	RoleSpoke         Role = "spoke"
	RoleHub           Role = "hub"
)

// Valid reports whether r is a recognized federation role.
func (r Role) Valid() bool {
	switch r {
	case RoleStandalone, RoleUncoordinated, RoleSpoke, RoleHub:
		return true
	}
	return false
}

// OTLPProtocol selects the OTLP transport.
type OTLPProtocol string

// OTLP transport wire values.
const (
	OTLPProtocolHTTP OTLPProtocol = "http"
	OTLPProtocolGRPC OTLPProtocol = "grpc"
)

// Valid reports whether p is a recognized OTLP transport.
func (p OTLPProtocol) Valid() bool {
	return p == OTLPProtocolHTTP || p == OTLPProtocolGRPC
}

// SNMPVersion selects the SNMP protocol version.
type SNMPVersion string

// SNMP version wire values.
const (
	SNMPVersionV2c SNMPVersion = "v2c"
	SNMPVersionV3  SNMPVersion = "v3"
)

// Valid reports whether v is a recognized SNMP version.
func (v SNMPVersion) Valid() bool {
	return v == SNMPVersionV2c || v == SNMPVersionV3
}

// ProfileType selects which credential fields a CredentialProfile consults.
type ProfileType string

// Credential profile type wire values.
const (
	ProfileTypeSNMPv2c ProfileType = "snmp_v2c"
	ProfileTypeSNMPv3  ProfileType = "snmp_v3"
)

// Valid reports whether t is a recognized credential profile type.
func (t ProfileType) Valid() bool {
	return t == ProfileTypeSNMPv2c || t == ProfileTypeSNMPv3
}
