package fdb

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sort"
	"strconv"
	"sync"

	gsnmp "github.com/gosnmp/gosnmp"

	snmputil "github.com/colinedwardwood/network-topology-exporter/internal/discovery/snmp"
)

const oidVlanCurrentTable = "1.3.6.1.2.1.17.7.1.4.2"

// reasonVLANWalkFailed is a degraded reason for
// network_topology_discovery_degraded_total{module="fdb"} (issue #100). It
// signals a per-VLAN community sub-walk failure that left the device's
// bridging topology incomplete. Keep low-cardinality: label by reason only,
// never by VLAN id.
const reasonVLANWalkFailed = "vlan_walk_failed"

// discoverVlanIDs walks dot1qVlanCurrentTable and returns a deduplicated,
// sorted list of active VLAN IDs. Returns nil on any error — the VLAN
// community walk is a best-effort IOS-only path, not a required step.
func discoverVlanIDs(ctx context.Context, client *gsnmp.GoSNMP) []int {
	pdus, err := snmputil.BulkWalk(ctx, client, oidVlanCurrentTable)
	if err != nil {
		return nil
	}
	const prefix = ".1.3.6.1.2.1.17.7.1.4.2.1."
	seen := make(map[int]struct{})
	for _, pdu := range pdus {
		suffix, ok := snmputil.TrimOIDPrefix(pdu.Name, prefix)
		if !ok {
			continue
		}
		// OID instance suffix: {col}.{timeMark}.{vlanId}
		_, rest, ok := snmputil.SplitOIDComponent(suffix) // skip col
		if !ok || rest == "" {
			continue
		}
		_, vlanStr, ok := snmputil.SplitOIDComponent(rest) // skip timeMark
		if !ok || vlanStr == "" {
			continue
		}
		vlanID, err := strconv.Atoi(vlanStr)
		if err != nil || vlanID < 1 || vlanID > 4094 {
			continue
		}
		seen[vlanID] = struct{}{}
	}
	if len(seen) == 0 {
		return nil
	}
	ids := make([]int, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	return ids
}

// maxVlanConcurrency is the maximum number of concurrent per-VLAN SNMP sessions
// opened during a VLAN community FDB walk. Caps resource use on devices with
// 100+ VLANs while still providing meaningful parallelism.
const maxVlanConcurrency = 8

// walkVlanCommunityFdbs uses VLAN community-string indexing (community@vlanId)
// to walk dot1dTpFdbTable for each active VLAN on classic Cisco IOS devices.
// These devices maintain one BRIDGE-MIB instance per VLAN and expose it only
// through community-string indexing; Q-BRIDGE is not available on IOS 12.x/15.x.
// Entries already present in the map (from B-MIB or Q-BRIDGE) are not overwritten.
// maxVlans caps the number of VLANs iterated; if the discovered VLAN list is
// longer, a warning is logged and the remaining VLANs are skipped.
// Per-VLAN walks run in parallel, bounded by maxVlanConcurrency.
func walkVlanCommunityFdbs(ctx context.Context, pp *snmputil.Params, client *gsnmp.GoSNMP, entries map[string]*fdbEntry, maxVlans int) {
	p := *pp
	if p.V3 || len(p.Community) == 0 {
		return
	}
	// p.Community is []byte (see snmp.Params); use bytes-search to avoid an
	// unnecessary string allocation that would create an unreachable copy.
	if bytes.IndexByte(p.Community, '@') >= 0 {
		// Rate-limit per device (issue #16): a misconfigured community
		// string is a static condition that would otherwise emit on every
		// cycle. First occurrence still alerts; cooldown suppresses repeats.
		msg := "fdb: community string contains '@'; skipping per-VLAN community walk to avoid ambiguity"
		attrs := []any{"device", p.IP}
		if p.WarnLimiter != nil {
			p.WarnLimiter.Warn(ctx, "fdb_community_at_sign|"+p.IP.String(), msg, attrs...)
		} else {
			slog.WarnContext(ctx, msg, attrs...)
		}
		return
	}
	vlanIDs := discoverVlanIDs(ctx, client)
	if len(vlanIDs) > maxVlans {
		// Rate-limit per device (issue #16): VLAN-count overflow is a
		// configuration-shaped condition that persists across cycles until
		// max_vlans is raised. The limiter keeps the operator alert on
		// first detection without flooding the log every cycle thereafter.
		msg := "fdb: VLAN community walk truncated at max_vlans limit; increase fdb.max_vlans to see all VLANs"
		attrs := []any{"device", p.IP, "discovered", len(vlanIDs), "max_vlans", maxVlans}
		if p.WarnLimiter != nil {
			p.WarnLimiter.Warn(ctx, "fdb_vlan_truncated|"+p.IP.String(), msg, attrs...)
		} else {
			slog.WarnContext(ctx, msg, attrs...)
		}
		vlanIDs = vlanIDs[:maxVlans]
	}

	type result struct {
		vlanEntries map[string]*fdbEntry
	}

	results := make([]result, len(vlanIDs))
	sem := make(chan struct{}, maxVlanConcurrency)
	var wg sync.WaitGroup

	for i, vlanID := range vlanIDs {
		wg.Add(1)
		go func(idx, vlan int) {
			defer wg.Done()

			// Recover a panic in this per-VLAN walk so one bad VLAN cannot
			// crash the whole discovery process (and, in spoke/standalone
			// mode, take the discovery loop down with it). The recover is
			// local because the panicking unit IS this goroutine; on recovery
			// we log the stack, report it under site "fdb_vlan_walk" through
			// the nil-tolerant PanicReporter seam (keeps this package free of
			// any prometheus/app import), and leave results[idx] zero so the
			// merge below simply skips this VLAN. Mirrors the per-device probe
			// recover in internal/app/cycle.go. Registered first so it runs
			// last in the defer chain, after the semaphore release and session
			// close below.
			defer func() {
				if r := recover(); r != nil {
					slog.Error("fdb: per-VLAN walk panicked; recovered",
						"device", p.IP, "vlan", vlan, "panic", r,
						"stack", string(debug.Stack()))
					if p.PanicReporter != nil {
						p.PanicReporter("fdb_vlan_walk")
					}
				}
			}()

			// Acquire semaphore slot; respect context cancellation while waiting.
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()

			vp := p
			// Build a fresh []byte for the per-VLAN community string so we
			// don't alias p.Community (the caller owns that slice and will
			// zeroize it). The new slice is short-lived and will be GC'd after
			// this goroutine returns.
			vp.Community = []byte(fmt.Sprintf("%s@%d", p.Community, vlan))
			vlanClient, err := snmputil.Open(vp)
			if err != nil {
				slog.Debug("fdb: VLAN community open failed", "device", vp.IP, "vlan", vlan, "err", err)
				// A per-VLAN session that won't open leaves that VLAN's
				// bridging topology undiscovered (issue #100). Label by reason
				// only — never by vlan id — to keep cardinality bounded.
				snmputil.RecordDegraded(pp, walkerFDB, reasonVLANWalkFailed)
				return
			}
			defer func() { _ = vlanClient.Conn.Close() }()
			vlanEntries := make(map[string]*fdbEntry)
			if _, err := walkFdbTableIntoFn(ctx, vlanClient, vlanEntries); err != nil {
				slog.Debug("fdb: VLAN community walk incomplete", "device", vp.IP, "vlan", vlan, "err", err)
				snmputil.RecordDegraded(pp, walkerFDB, reasonVLANWalkFailed)
			}
			results[idx] = result{vlanEntries: vlanEntries}
		}(i, vlanID)
	}

	wg.Wait()

	// Merge per-goroutine maps into entries; don't overwrite existing keys.
	for _, r := range results {
		for key, e := range r.vlanEntries {
			if _, exists := entries[key]; !exists {
				entries[key] = e
			}
		}
	}
}
