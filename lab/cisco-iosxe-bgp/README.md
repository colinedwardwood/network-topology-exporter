# Lab — cisco-iosxe-bgp

Capture real-device SNMP fixtures for the **`vendor_cisco`** walker
(`internal/discovery/bgp/bgp_vendor.go`) against **Cisco IOS-XE** — the
canonical Cisco production OS named in
[issue #1](https://github.com/colinedwardwood/network-topology-exporter/issues/1)'s
acceptance criteria.

Unlike the sibling labs in this tree, this is **not a containerlab
topology**. Cisco IOS-XE images are not freely redistributable; the only
no-cost path is **Cisco DevNet Sandbox**, which provides hosted routers
reachable via VPN. The lab here is the runbook + capture script for
that flow.

## Why a separate Cisco lab if `cisco-iol-bgp` already produced captures?

The `cisco-iol-bgp` lab uses the IOL (IOS-on-Linux) image — historically
the SDK build of IOS, not the IOS-XE codebase that ships in production
ASR1000 / CSR1000v / Cat8000v / Cat9000 platforms. Issue #31 was closed
against IOL captures, but issue #1's acceptance bar specifically names
"Cisco IOS-XE" because column numbers, index encoding, and
`cbgpPeer2Table` row composition can diverge between IOL and IOS-XE
shipping releases. This lab provides the IOS-XE evidence that
issue #1 requires.

If captures here match the IOL captures byte-for-byte at the column
level, that is itself a strong cross-confirmation worth noting in the
closing PR.

## Status

**Scaffold only.** No captures committed yet. Pending: a DevNet reservation
window and BGP peer establishment.

## Prerequisites

| Tool | How |
|---|---|
| Ubuntu host (or Linux VM) | Sandbox VPN reaches the router from this host |
| Cisco DevNet account | Free signup at https://developer.cisco.com/ |
| `openconnect` | `apt-get install -y openconnect` — open-source AnyConnect-compatible VPN client; Cisco-acknowledged alternative when AnyConnect download is gated |
| `ssh` client | Built-in |
| `snmpwalk` (net-snmp) | `apt-get install -y snmp snmp-mibs-downloader` |
| (Option B only) `bird2` or `frr` | `apt-get install -y bird2` — local BGP speaker for the external-peer path |

## Reservation path

Two viable DevNet sandboxes; pick based on whether you need multiple
routers (BGP peers) or a single router (BGP via external speaker).

### Option A — CML Sandbox (recommended, multi-router)

[Cisco Modeling Labs (CML) Sandbox](https://devnetsandbox.cisco.com/RM/Topology)
gives you a reservation window with full CML access. You build a 2-CSR
topology in CML, configure iBGP between them, then `snmpwalk` both
routers over the VPN. This produces the same shape of capture as
`lab/arista-ceos-bgp/` — two nodes, one iBGP session, deterministic
`cbgpPeer2Table` content.

1. Reserve at https://devnetsandbox.cisco.com/RM/Topology (search "CML").
   Typical reservation: 4–8 hours, free, queue-based.
2. Wait for the `devnetsandbox@cisco.com` email with VPN host, group,
   username, password.
3. Connect:
   ```sh
   sudo openconnect --protocol=anyconnect --user=<sandbox-user> <vpn-host>
   ```
4. SSH to the CML controller (URL in the email). In the CML UI, create a
   new lab with **two CSR1000v** (or **c8000v**) nodes connected by a
   single Ethernet link (Gi2 ↔ Gi2). Leave management on Gi1 (CML's
   default). Start the lab. TODO: capture the CML topology export as
   `configs/cml-topology.yaml` during the first reservation so subsequent
   runs can re-import.
5. SSH into each router's console (CML exposes a terminal), paste the
   contents of `configs/iosxe-r1.cfg` and `configs/iosxe-r2.cfg`.
6. Wait ~60s for BGP to reach `Established`. Verify:
   ```sh
   show ip bgp summary
   show snmp community
   ```
7. Note each router's management IP (CML assigns these per session).
8. Run `./capture.sh <r1-mgmt-ip> <r2-mgmt-ip>` from the Ubuntu host.

### Option B — Single-CSR Sandbox + external BGP speaker

Use the "IOS XE on CSR Recommended Code" or "IOS XE on Cat 8K"
reservation sandbox (single-router). Peer the sandbox CSR to a BGP
speaker running on your Ubuntu host (visible to the sandbox via the
VPN's reverse path).

1. Reserve at https://devnetsandbox.cisco.com/RM/Topology (search "IOS XE").
2. Connect via `openconnect` as above. Note the management IP from the
   email (example: `10.10.20.48`).
3. Verify your Ubuntu host's VPN-side IP: `ip addr show | grep tun0`.
   This is the IP the CSR will see you on.
4. Start a local BGP speaker (bird2 example):
   ```sh
   cat > /etc/bird/bird.conf <<'EOF'
   router id <your-vpn-ip>;
   protocol device { }
   protocol bgp csr_peer {
     local as 65002;
     neighbor 10.10.20.48 as 65001;
     ipv4 { import all; export none; };
   }
   EOF
   sudo systemctl restart bird
   ```
5. SSH to the sandbox CSR (`ssh developer@10.10.20.48`, password
   `C1sco12345`), paste the contents of `configs/iosxe-single.cfg`
   with `<peer-ip>` replaced by your VPN-side IP.
6. Wait for `Established`. Verify on CSR:
   ```
   show ip bgp summary
   ```
7. Run `./capture.sh 10.10.20.48` from the Ubuntu host.

**Trade-off**: Option B yields one `cbgpPeer2Table` row (the BGP session
from CSR to your bird speaker). That's enough to validate column
numbers, index encoding, and state decoding — which is what the walker
tests need. Option A's two-router shape is closer to what production
deployments look like.

## OIDs probed

Same four-OID set as the sibling labs, for direct cross-comparison:

| Root | Walker | Expected on IOS-XE |
|---|---|---|
| `1.3.6.1.2.1.1` | (sys group) | populated — `sysObjectID` should resolve to a Cisco enterprise OID (`1.3.6.1.4.1.9.*`) |
| `1.3.6.1.2.1.15.3` | `rfc4273` (fallback) | populated for IPv4 peers — RFC 4273 baseline |
| `1.3.6.1.4.1.9.9.187.1.2.5` | **`vendor_cisco`** | the canonical capture for this lab — populated whenever a BGP session exists |
| `1.3.6.1.3.5.1.1.2` | (removed) | expected empty — IOS-XE does not implement the IETF draft form at this OID. Captured for negative-result evidence. |

## Capture

```sh
# Option A (two routers, both walked)
./capture.sh <r1-mgmt-ip> <r2-mgmt-ip>

# Option B (single router)
./capture.sh <csr-mgmt-ip>
```

Produces `captures/<host-with-underscores>__<oid-with-underscores>.txt`,
one file per (host, OID-root). Numeric OID form (`snmpwalk -On -Oe`) is
mandatory — the fixture conversion step pastes these into
`[]gosnmp.SnmpPDU` literals where OID *names* would be ambiguous.

## Destroy

CML reservation: stop the lab in the CML UI, end the reservation.

Single-CSR reservation: end the reservation from the DevNet portal.
The sandbox is reset between reservations, so no per-router cleanup
is required.

Disconnect VPN: `sudo killall openconnect`.

## Converting captures to fixtures

Same flow as `lab/cisco-iol-bgp/` — see that lab's README for the
text-output → `[]gosnmp.SnmpPDU` literal conversion pattern. Real-device
fixtures land at `internal/snmptest/testdata/cisco_iosxe_real.*` and
the synthetic `cbgpPeer2Table` cases in `bgp_vendor_test.go` get
real-device siblings.

If captures here disagree with `lab/cisco-iol-bgp/` on column numbers
or index encoding, **IOS-XE wins** — the walker code must match
production IOS-XE behavior, not IOL behavior. File a PR fixing
`bgp_vendor.go` columns and reference both capture sources.
