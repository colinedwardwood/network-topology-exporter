// Package snmp walks SNMP SYSTEM-group data and shared table helpers.
//
// Invariants:
// - Scalar SYSTEM OIDs are fetched via GET; table data uses BulkWalk/Walk fallback.
// - Context cancellation must interrupt blocked SNMP reads via connection deadlines.
// - Each goroutine owns its own GoSNMP session (client instances are not shared).
// - sysObjectID vendor mapping uses IANA enterprise prefixes only.
package snmp

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"regexp"
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

	// Retries is the number of SNMP retries per request. 0 means no retries.
	Retries int

	// MaxVlans caps the number of VLANs walked by the FDB module's
	// VLAN community-string path. 0 means use the module default (100).
	MaxVlans int
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
// controllers). ctx is checked before each attempt and wraps the blocking
// BulkWalkAll/WalkAll calls so that context cancellation interrupts a
// mid-walk UDP read via SetDeadline.
func BulkWalk(ctx context.Context, client *g.GoSNMP, rootOID string) ([]g.SnmpPDU, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	type walkResult struct {
		pdus []g.SnmpPDU
		err  error
	}

	ch := make(chan walkResult, 1)
	go func() {
		pdus, err := client.BulkWalkAll(rootOID)
		ch <- walkResult{pdus, err}
	}()
	var bulkErr error
	select {
	case r := <-ch:
		if r.err == nil {
			return r.pdus, nil
		}
		bulkErr = r.err
	case <-ctx.Done():
		_ = client.Conn.SetDeadline(time.Now()) // interrupt pending UDP read
		<-ch                                    // wait for goroutine to exit
		return nil, ctx.Err()
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	_ = bulkErr // checked above; fall through to WalkAll

	ch2 := make(chan walkResult, 1)
	go func() {
		pdus, err := client.WalkAll(rootOID)
		ch2 <- walkResult{pdus, err}
	}()
	select {
	case r := <-ch2:
		return r.pdus, r.err
	case <-ctx.Done():
		_ = client.Conn.SetDeadline(time.Now()) // interrupt pending UDP read
		<-ch2                                   // wait for goroutine to exit
		return nil, ctx.Err()
	}
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
			dev.OSVersion = normalizeSysDescr(PDUString(pdu))
		case dotOIDSysObjectID:
			dev.Vendor = vendorFromObjectID(pduOID(pdu))
		case dotOIDSysUpTime:
			// sysUpTime wraps to zero after ~497 days; callers cannot distinguish wrap from reboot.
			if ticks, ok := PDUIntStrict(pdu); ok {
				dev.Uptime = time.Duration(ticks) * 10 * time.Millisecond
				uptimeSec := dev.Uptime.Seconds()
				if uptimeSec < 86400 {
					slog.Debug("snmp: sysUpTime below 24h; may be recent reboot or 497-day counter wrap",
						"device", dev.ID, "uptime_seconds", uptimeSec)
				}
			}
		}
	}

	return dev, nil
}

// sysDescrVersionRe matches the first version-like token (digits and dots) in a
// sysDescr string, e.g. "15.2.4" in a Cisco IOS sysDescr.
var sysDescrVersionRe = regexp.MustCompile(`\d+\.\d+[\.\d]*`)

// normalizeSysDescr collapses the sysDescr string to the first version token
// (M.N or M.N.P) to prevent one Prometheus series per patch release across
// a device fleet. Using the full sysDescr as a label would multiply series
// count by the number of distinct firmware versions deployed. Falls back to
// the first 64 bytes of raw sysDescr when no version token is found.
func normalizeSysDescr(s string) string {
	// Extract first M.N or M.N.P version token
	if m := sysDescrVersionRe.FindString(s); m != "" {
		return m
	}
	if len(s) > 64 {
		return s[:64]
	}
	return s
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

	retries := p.Retries
	if retries == 0 {
		retries = 1 // default to 1 retry for backwards compatibility
	}
	client := &g.GoSNMP{
		Target:    p.IP.String(),
		Port:      port,
		Timeout:   timeout,
		Retries:   retries,
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

// enterprisePrefix is one entry in the ordered enterprisePrefixes slice.
// Entries are checked in order; longest (most-specific) prefixes should appear
// first so that a more-specific vendor match wins over a shorter prefix.
type enterprisePrefix struct {
	prefix string
	vendor string
}

// enterprisePrefixes is the ordered list of IANA enterprise OID prefixes.
// Enterprise numbers are disjoint — no entry is a prefix of another, so
// iteration order does not affect correctness. Order is fixed for determinism.
//
// Source: IANA Enterprise Numbers registry
// (https://www.iana.org/assignments/enterprise-numbers)
var enterprisePrefixes = []enterprisePrefix{
	{prefix: "1.3.6.1.4.1.9.", vendor: "cisco"},
	{prefix: "1.3.6.1.4.1.11.", vendor: "hp"},
	{prefix: "1.3.6.1.4.1.14988.", vendor: "mikrotik"},
	{prefix: "1.3.6.1.4.1.2636.", vendor: "juniper"},
	{prefix: "1.3.6.1.4.1.12356.", vendor: "fortinet"},
	{prefix: "1.3.6.1.4.1.8072.", vendor: "net-snmp"},
	{prefix: "1.3.6.1.4.1.890.", vendor: "zyxel"},
	{prefix: "1.3.6.1.4.1.6527.", vendor: "nokia"},
	{prefix: "1.3.6.1.4.1.25506.", vendor: "huawei"},
	{prefix: "1.3.6.1.4.1.2011.", vendor: "huawei"},
	{prefix: "1.3.6.1.4.1.4526.", vendor: "netgear"},
	{prefix: "1.3.6.1.4.1.1916.", vendor: "extreme"},
	{prefix: "1.3.6.1.4.1.1991.", vendor: "brocade"},
	{prefix: "1.3.6.1.4.1.1872.", vendor: "alteon"},
	{prefix: "1.3.6.1.4.1.3375.", vendor: "f5"},
	{prefix: "1.3.6.1.4.1.25461.", vendor: "paloalto"},
	{prefix: "1.3.6.1.4.1.30065.", vendor: "arista"},
	{prefix: "1.3.6.1.4.1.40310.", vendor: "cumulus"},
	{prefix: "1.3.6.1.4.1.6876.", vendor: "vmware"},
	{prefix: "1.3.6.1.4.1.20301.", vendor: "ubiquiti"},
	{prefix: "1.3.6.1.4.1.41112.", vendor: "ubiquiti"},
	{prefix: "1.3.6.1.4.1.674.", vendor: "dell"},
	{prefix: "1.3.6.1.4.1.6486.", vendor: "alcatel-lucent"},
	{prefix: "1.3.6.1.4.1.3076.", vendor: "altiga"},
	{prefix: "1.3.6.1.4.1.232.", vendor: "hpe"},
	{prefix: "1.3.6.1.4.1.236.", vendor: "samsung"},
	{prefix: "1.3.6.1.4.1.3417.", vendor: "bluecoat"},
	{prefix: "1.3.6.1.4.1.5624.", vendor: "enterasys"},
	{prefix: "1.3.6.1.4.1.18928.", vendor: "aerohive"},
	{prefix: "1.3.6.1.4.1.14179.", vendor: "cisco-wlc"},
	{prefix: "1.3.6.1.4.1.45.", vendor: "baynetworks"},
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
	for _, e := range enterprisePrefixes {
		if strings.HasPrefix(oid, e.prefix) {
			return e.vendor
		}
	}
	return "unknown"
}
