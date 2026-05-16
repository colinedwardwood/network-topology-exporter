# Lab — arista-ceos-bgp

2-node Arista cEOS iBGP topology for capturing real-device SNMP
fixtures for the **v2_draft walker** at
`internal/discovery/bgp/bgp_v2.go`.

Arista EOS implements the IETF draft `bgp4V2PeerTable` natively at OID
`1.3.6.1.3.5.1.1.2`. That's the **primary** target of our v2_draft
walker — the one the project's BGP4-V2 work was designed around. Cisco
and Juniper require separate vendor walkers; Arista is the canonical
IETF-form vendor. This lab's snmpwalks become the real-device fixture
the synthetic test cases in `bgp_v2_test.go` should be cross-checked
against.

Related issues: [#1](https://github.com/colinedwardwood/network-topology-exporter/issues/1) (BGP4-V2 vendor fixtures), [#31](https://github.com/colinedwardwood/network-topology-exporter/issues/31) (vendor_cisco walker bugs surfaced from the cisco-iol-bgp captures).

## Prerequisites

| Tool | How |
|---|---|
| Linux host with KVM | cEOS is amd64, needs hardware virt (or runs slowly under software emulation on macOS) |
| `ceos:4.36.0F` image loaded | `docker import ~/cEOS64-lab-4.36.0F.tar.xz ceos:4.36.0F` — cEOS ships as a rootfs tar.xz (NOT a `docker save` archive, so `docker load` won't work) |
| containerlab v0.75+ | `bash -c "$(curl -sL https://get.containerlab.dev)"` |
| `snmpwalk` (net-snmp) | `apt-get install -y snmp` |

Arista cEOS is a free download from arista.com (account required, no
licensing barrier beyond signup). The 64-bit lab variant runs on x86_64
Linux+KVM hosts.

## Deploy

```sh
cd lab/arista-ceos-bgp
sudo containerlab deploy -t ./topology.clab.yml
```

cEOS boots in 30–60 seconds. The deploy command returns when containers
are running; SNMP comes online shortly after.

## Verify BGP

```sh
# Wait for SNMP to respond (typically 30-60s after deploy)
until snmpwalk -v2c -c public -t 2 -r 0 172.20.20.13 1.3.6.1.2.1.1.1 2>&1 | grep -q STRING; do
  sleep 5
done

# BGP state from r1 perspective
sudo containerlab exec --name arista-ceos-bgp --label clab-node-name=r1 --cmd 'Cli -c "show ip bgp summary"'
```

Expect Neighbor 10.0.0.2 in Established state.

## Capture

```sh
./capture.sh
```

Produces files in `captures/`, one per (node, OID-root). The
high-value file is `captures/r1_ietf_bgp4V2PeerTable.txt` —
Arista's response to the IETF draft form `bgp4V2PeerTable`, which is
the primary target of the v2_draft walker.

## OIDs probed

| Root | Walker | Purpose |
|---|---|---|
| `1.3.6.1.2.1.1` | (sys group) | sysObjectID for vendor detection (Arista enterprise prefix 30065) |
| `1.3.6.1.2.1.15.3` | `rfc4273` (fallback) | Confirms Arista responds to RFC 4273 baseline |
| `1.3.6.1.3.5.1.1.2` | **`v2_draft`** | The capture |
| `1.3.6.1.4.1.9.9.187.1.2.5` | `vendor_cisco` | Expected empty — Arista doesn't implement Cisco's MIB |

## Destroy

```sh
sudo containerlab destroy -t ./topology.clab.yml --cleanup
```

## Converting captures to fixtures

Same flow as the cisco-iol-bgp lab — see that lab's README for the
text-output → `[]gsnmp.SnmpPDU` literal conversion pattern. Real-device
fixtures land at `internal/snmptest/testdata/arista_ceos_real.*` (or
similar) and the existing `TestWalkV2IPv6Peer` synthetic case in
`bgp_v2_test.go` gets a real-device sibling.
