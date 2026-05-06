// Package snmp walks the SNMP SYSTEM group to anchor the device inventory.
//
// # Specification sources
//
//   - RFC 3418 — Management Information Base (MIB) for the Simple Network
//     Management Protocol (SNMP). Defines the SNMPv2-MIB SYSTEM group:
//     sysDescr (.1.3.6.1.2.1.1.1.0), sysObjectID (.1.3.6.1.2.1.1.2.0),
//     sysUpTime (.1.3.6.1.2.1.1.3.0), sysName (.1.3.6.1.2.1.1.5.0).
//     RFC 3418 obsoletes RFC 1907 and RFC 1213 for these objects.
//   - IANA Enterprise Numbers registry (https://www.iana.org/assignments/
//     enterprise-numbers) — the sysObjectID prefix encodes the vendor's
//     IANA-assigned enterprise number. Matching the prefix identifies the
//     vendor without requiring a full MIB database at runtime.
//
// # Design references
//
//   - prometheus/snmp_exporter (Apache 2.0) — the "separate scalar GET from
//     table BulkWalk" pattern. Scalar OIDs ending in .0 are fetched via a
//     single SNMP GET request; table subtrees are walked via BulkWalkAll.
//     This avoids traversing large tables to retrieve four scalar values.
//     Source: https://github.com/prometheus/snmp_exporter
//   - kentik/ktranslate (Apache 2.0) — the BulkWalkAll → WalkAll → sleep +
//     WalkAll fallback chain for resilience against devices that reject Bulk
//     PDUs or are temporarily overloaded. Pattern reference only, no code
//     copied. Source: https://github.com/kentik/ktranslate
//
// # SNMP client library
//
//   - github.com/gosnmp/gosnmp (BSD-2-Clause) — used by both snmp_exporter
//     and ktranslate; the de-facto standard Go SNMP client. GoSNMP is not
//     goroutine-safe for concurrent use on the same instance: each worker
//     goroutine must own its own *gosnmp.GoSNMP struct and connection.
package snmp

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	g "github.com/gosnmp/gosnmp"

	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
)

// Params holds the resolved connection parameters for one SNMP walk.
// The caller (discovery loop) resolves credentials to concrete values via
// os.Getenv before building a Params; this package never reads env vars.
// Protocol names follow the config schema: "SHA", "SHA-256", "SHA-384",
// "SHA-512", "MD5" for auth; "AES", "AES-192", "AES-256", "DES" for priv.
type Params struct {
	IP      net.IP
	Port    uint16
	Timeout time.Duration

	// SNMPv2c fields.
	Community string

	// SNMPv3 fields.
	V3          bool
	Username    string
	AuthProto   string // "SHA" | "SHA-256" | "SHA-384" | "SHA-512" | "MD5" | ""
	AuthKey     string
	PrivProto   string // "AES" | "AES-192" | "AES-256" | "DES" | ""
	PrivKey     string
	ContextName string
}

// System group OIDs fetched as scalars via SNMP GET (RFC 3418).
const (
	oidSysDescr    = "1.3.6.1.2.1.1.1.0"
	oidSysObjectID = "1.3.6.1.2.1.1.2.0"
	oidSysUpTime   = "1.3.6.1.2.1.1.3.0"
	oidSysName     = "1.3.6.1.2.1.1.5.0"
)

// Pre-built dotted forms for the PDU switch in Walk. Concatenating at the
// call site in a hot loop allocates a new string on every PDU.
const (
	dotOIDSysDescr    = "." + oidSysDescr
	dotOIDSysObjectID = "." + oidSysObjectID
	dotOIDSysUpTime   = "." + oidSysUpTime
	dotOIDSysName     = "." + oidSysName
)

// Open creates and connects a gosnmp session from the given parameters.
// Callers must close the underlying connection (client.Conn.Close()) when done.
// Each call returns a fresh *GoSNMP; callers must not share instances across
// goroutines (GoSNMP is not goroutine-safe).
func Open(p Params) (*g.GoSNMP, error) {
	client := buildClient(p)
	if err := client.Connect(); err != nil {
		return nil, fmt.Errorf("snmp connect %s: %w", p.IP, err)
	}
	return client, nil
}

// BulkWalk walks rootOID using BulkWalkAll, falling back to WalkAll for
// devices that reject GetBulk PDUs (some older IOS/JunOS revisions, some AP
// controllers). ctx is checked before each attempt; the GoSNMP timeout handles
// per-attempt network deadlines.
func BulkWalk(ctx context.Context, client *g.GoSNMP, rootOID string) ([]g.SnmpPDU, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	pdus, err := client.BulkWalkAll(rootOID)
	if err == nil {
		return pdus, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return client.WalkAll(rootOID)
}

// Walk retrieves the SNMP SYSTEM group from the device at p.IP and returns a
// Device record. Creates a short-lived session per snmp_exporter convention.
//
// Returns a non-nil *Device and a non-nil error when the SNMP session
// succeeds but the device reports unexpected values — the partial Device is
// usable for inventory. Returns (nil, err) only on connection / auth failure.
func Walk(ctx context.Context, p Params) (*discovery.Device, error) {
	client, err := Open(p)
	if err != nil {
		return nil, err
	}
	defer func() { _ = client.Conn.Close() }()

	// Respect ctx cancellation for the connect+GET window.
	done := make(chan struct{})
	var result *g.SnmpPacket
	var getErr error
	go func() {
		defer close(done)
		result, getErr = client.Get([]string{
			oidSysDescr,
			oidSysObjectID,
			oidSysUpTime,
			oidSysName,
		})
	}()

	select {
	case <-ctx.Done():
		// Interrupt the blocked Get so the goroutine exits before the deferred
		// Conn.Close runs. Without this, Close races with the goroutine's read.
		_ = client.Conn.SetDeadline(time.Now())
		<-done
		return nil, ctx.Err()
	case <-done:
	}

	if getErr != nil {
		return nil, fmt.Errorf("snmp get system group %s: %w", p.IP, getErr)
	}

	dev := &discovery.Device{
		ID:     p.IP.String(), // fallback until sysName is read
		Vendor: "unknown",
	}

	for _, pdu := range result.Variables {
		switch pdu.Name {
		case dotOIDSysName:
			if s := NormaliseName(PDUString(pdu)); s != "" {
				dev.ID = s
			}
		case dotOIDSysDescr:
			dev.OSVersion = PDUString(pdu)
		case dotOIDSysObjectID:
			dev.Vendor = vendorFromObjectID(pduOID(pdu))
		case dotOIDSysUpTime:
			if ticks, ok := pdu.Value.(uint32); ok {
				dev.Uptime = time.Duration(ticks) * 10 * time.Millisecond
			}
		}
	}

	return dev, nil
}

// buildClient constructs a *gosnmp.GoSNMP from the resolved parameters.
// Each call returns a fresh struct; callers must not share instances across
// goroutines (gosnmp is not goroutine-safe on the same *GoSNMP).
func buildClient(p Params) *g.GoSNMP {
	port := p.Port
	if port == 0 {
		port = 161
	}
	timeout := p.Timeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}

	client := &g.GoSNMP{
		Target:    p.IP.String(),
		Port:      port,
		Timeout:   timeout,
		Retries:   1,
		MaxOids:   g.MaxOids,
		Transport: "udp",
	}

	if p.V3 {
		authProto := parseAuthProto(p.AuthProto)
		privProto := parsePrivProto(p.PrivProto)
		client.Version = g.Version3
		client.SecurityModel = g.UserSecurityModel
		client.MsgFlags = authPrivMsgFlags(authProto, privProto)
		client.SecurityParameters = &g.UsmSecurityParameters{
			UserName:                 p.Username,
			AuthenticationProtocol:   authProto,
			AuthenticationPassphrase: p.AuthKey,
			PrivacyProtocol:          privProto,
			PrivacyPassphrase:        p.PrivKey,
		}
		if p.ContextName != "" {
			client.ContextName = p.ContextName
		}
	} else {
		client.Version = g.Version2c
		client.Community = p.Community
	}

	return client
}

func authPrivMsgFlags(auth g.SnmpV3AuthProtocol, priv g.SnmpV3PrivProtocol) g.SnmpV3MsgFlags {
	switch {
	case auth == g.NoAuth:
		return g.NoAuthNoPriv
	case priv == g.NoPriv:
		return g.AuthNoPriv
	default:
		return g.AuthPriv
	}
}

// parseAuthProto maps a config-level string to a gosnmp auth protocol constant.
// An empty string means no auth configured; unrecognized values default to SHA.
func parseAuthProto(s string) g.SnmpV3AuthProtocol {
	switch s {
	case "":
		return g.NoAuth
	case "MD5":
		return g.MD5
	case "SHA-256":
		return g.SHA256
	case "SHA-384":
		return g.SHA384
	case "SHA-512":
		return g.SHA512
	default: // config validator should have caught unrecognized values
		return g.SHA
	}
}

// parsePrivProto maps a config-level string to a gosnmp priv protocol constant.
// An empty string means no priv configured; unrecognized values default to AES.
func parsePrivProto(s string) g.SnmpV3PrivProtocol {
	switch s {
	case "":
		return g.NoPriv
	case "DES":
		return g.DES
	case "AES-192":
		return g.AES192
	case "AES-256":
		return g.AES256
	default: // config validator should have caught unrecognized values
		return g.AES
	}
}

func pduOID(pdu g.SnmpPDU) string {
	if oid, ok := pdu.Value.(string); ok {
		return oid
	}
	return ""
}

// vendorFromObjectID maps the IANA enterprise number prefix of a sysObjectID
// to a canonical vendor string. Only the enterprise prefix matters; the
// remainder encodes model/platform details that belong in a separate
// lookup if needed.
//
// Source: IANA Enterprise Numbers registry
// (https://www.iana.org/assignments/enterprise-numbers)
func vendorFromObjectID(oid string) string {
	if oid == "" {
		return "unknown"
	}
	// Strip leading dot so both ".1.3.6.1.4.1.9." and "1.3.6.1.4.1.9." match.
	oid = strings.TrimPrefix(oid, ".")
	for prefix, vendor := range enterprisePrefixes {
		if strings.HasPrefix(oid, prefix) {
			return vendor
		}
	}
	return "unknown"
}

// enterprisePrefixes maps the IANA enterprise OID prefix to a canonical
// vendor name. Populated from the IANA Enterprise Numbers registry.
// Entries are checked with strings.HasPrefix, so the most specific prefix
// wins only when a vendor appears multiple times — entries here are at
// enterprise-number granularity (one per vendor) so specificity ties
// don't arise.
var enterprisePrefixes = map[string]string{
	"1.3.6.1.4.1.9.":     "cisco",
	"1.3.6.1.4.1.11.":    "hp",
	"1.3.6.1.4.1.14988.": "mikrotik",
	"1.3.6.1.4.1.2636.":  "juniper",
	"1.3.6.1.4.1.12356.": "fortinet",
	"1.3.6.1.4.1.8072.":  "net-snmp",
	"1.3.6.1.4.1.890.":   "zyxel",
	"1.3.6.1.4.1.6527.":  "nokia",
	"1.3.6.1.4.1.25506.": "huawei",
	"1.3.6.1.4.1.2011.":  "huawei",
	"1.3.6.1.4.1.4526.":  "netgear",
	"1.3.6.1.4.1.1916.":  "extreme",
	"1.3.6.1.4.1.1991.":  "brocade",
	"1.3.6.1.4.1.1872.":  "alteon",
	"1.3.6.1.4.1.3375.":  "f5",
	"1.3.6.1.4.1.25461.": "paloalto",
	"1.3.6.1.4.1.30065.": "arista",
	"1.3.6.1.4.1.40310.": "cumulus",
	"1.3.6.1.4.1.6876.":  "vmware",
	"1.3.6.1.4.1.20301.": "ubiquiti",
	"1.3.6.1.4.1.41112.": "ubiquiti",
	"1.3.6.1.4.1.674.":   "dell",
	"1.3.6.1.4.1.6486.":  "alcatel-lucent",
	"1.3.6.1.4.1.3076.":  "altiga",
	"1.3.6.1.4.1.232.":   "hpe",
	"1.3.6.1.4.1.236.":   "samsung",
	"1.3.6.1.4.1.3417.":  "bluecoat",
	"1.3.6.1.4.1.5624.":  "enterasys",
	"1.3.6.1.4.1.18928.": "aerohive",
	"1.3.6.1.4.1.14179.": "cisco-wlc",
	"1.3.6.1.4.1.45.":    "baynetworks",
}
