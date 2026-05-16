# Lab — cisco-iol-bgp

A 2-node Cisco IOL iBGP topology built to capture real-device SNMP
fixtures for the BGP4-V2 / `cbgpPeer2Table` walker validation needed by
[issue #1](https://github.com/colinedwardwood/network-topology-exporter/issues/1).

## What this lab is for

The `vendor_cisco` walker at `internal/discovery/bgp/bgp_vendor.go`
walks `cbgpPeer2Table` at OID `1.3.6.1.4.1.9.9.187.1.2.5` with column
numbers (`state=3`, `remoteAddr=11`, `remoteAs=13`) transcribed from MIB
documentation. The unit tests use **synthetic** PDU streams hand-crafted
from those same documents. If the column numbers are wrong on real IOS,
the unit tests still pass — and production would silently drop every
Cisco BGP edge.

This lab boots two `vrnetlab/cisco_iol:L2-17.12.1` containers with iBGP
between them and SNMP v2c enabled, then runs `snmpwalk` against the
relevant OID roots. The captured output is the ground truth that
fixture conversion runs against.

## Caveat 1: L2 IOL may not support BGP

The vrnetlab image is `cisco_iol:L2-17.12.1`. "L2" is the switch
variant of IOL — historically lacking L3 routing. The configs in
`configs/` attempt to force `Ethernet0/0` into routed mode via
`no switchport` then enable `router bgp 65001`. Whether this is
accepted on 17.12 L2 is the **first thing the runbook verifies**.

If BGP doesn't start, this lab is a dead end and Cisco fixture capture
stays blocked on a different image (e.g. CSR1000v, c8000v, or the L3
variant of IOL). The FortiOS image stays useful for RFC 4273 fixtures —
see `lab/fortios-bgp/` (not scaffolded yet).

## Caveat 2: this lab CANNOT run on macOS / Apple Silicon

Verified on 2026-05-16: a macOS / Apple Silicon / OrbStack host hits
two blockers that no amount of containerlab configuration can work
around.

1. **Cisco IOL requires an `iourc` license file** keyed to the specific
   `iol.bin`'s hostid. The vrnetlab image is built without one; IOL
   refuses to boot otherwise. The file is per-image-derived,
   Cisco-internal, and not redistributable. If you don't have an
   `iourc` matching this build, this lab is not runnable on any host.
2. **vrnetlab kinds that wrap a qcow2 in QEMU (FortiOS, CSR1000v, vMX,
   vSRX, SR-OS)** need `/dev/kvm` for usable performance. macOS's
   hypervisor does not expose `/dev/kvm` to guest Linux VMs (including
   OrbStack's), so QEMU falls back to TCG software emulation. Boot
   times go from minutes to 30+ minutes, and some images simply fail
   to come up. Cisco IOL itself is a Linux ELF binary running directly
   under Rosetta — it doesn't have the nested-virt problem, only the
   licensing one.

**Run this lab on Linux with KVM enabled** (a CI runner, work desktop,
or remote VM with hardware-virt passthrough). The scaffold in this
directory is portable; only the deploy host changes. Issue
[#1](https://github.com/colinedwardwood/network-topology-exporter/issues/1)
is moved to milestone `v1.3.1 — Lab Fixture Capture` for that reason.

## Prerequisites

| Tool | How |
|---|---|
| Linux host with hardware virt (KVM) | Verify `/dev/kvm` exists and `kvm-ok` reports ok. Do not attempt on macOS/Apple Silicon — see Caveat 2. |
| Docker image `vrnetlab/cisco_iol:L2-17.12.1` loaded | `docker load -i path/to/cisco_iol-L2-17.12.1.bzip`. The .bzip artefacts live in the sibling `network-topology-exporter-resources/` directory on the original Mac; copy to the Linux host first. |
| Cisco `iourc` license file matching this IOL build | Place at `iourc.txt` in this directory. NOT provided in the repo (license-restricted). See "iourc requirement" below. |
| containerlab | `bash -c "$(curl -sL https://get.containerlab.dev)"` on the Linux host |
| `snmpwalk` (net-snmp) | `apt-get install -y snmp` |
| `jq` (optional, helps capture.sh) | `apt-get install -y jq` |

### iourc requirement

Cisco IOL embeds a hostid check; the binary refuses to boot without an
`iourc` file matching its compiled hostid. The file format is:

```
[license]
<hostname> = <16-hex-char-checksum>;
```

The checksum is derived from the iol.bin's hostid + an internal Cisco
algorithm. Generation requires Cisco-internal scripts. If you have an
iourc that matches the iol.bin in `vrnetlab/cisco_iol:L2-17.12.1`,
place it at `./iourc.txt` and the containerlab cisco_iol kind will
bind-mount it into the container. If you don't, this image is not
runnable for you — pivot to a vendor whose images don't require
per-build licensing (CSR1000v with eval license, or different platform
entirely).

## Deploy

From the Mac host:

```sh
orb shell containerlab-vm
cd ~/Code/grafana/network-topology-exporter/lab/cisco-iol-bgp   # path is mounted
containerlab deploy -t ./topology.clab.yml
```

First boot of IOL takes 1–3 minutes. `containerlab deploy` blocks until
the nodes reach a ready state.

## Verify BGP is actually working (the L2-IOL question)

This is the gate. If any of these checks fail, BGP is not supported on
the L2 image — pivot to FortiOS or a different Cisco image.

```sh
# 1. Both nodes booted?
containerlab inspect --name cisco-iol-bgp

# 2. Did the startup configs apply cleanly?
docker logs clab-cisco-iol-bgp-r1 2>&1 | tail -40
docker logs clab-cisco-iol-bgp-r2 2>&1 | tail -40
#    Look for messages rejecting `router bgp`, `no switchport`, or
#    `ip routing`. Any of those means L2 IOL can't host this lab.

# 3. From inside r1, is BGP up?
containerlab exec --name cisco-iol-bgp --label clab-node-name=r1 \
  --cmd "show ip bgp summary"
#    Expect Neighbor 10.0.0.2 in Established state with State/PfxRcd >= 1.

# 4. Is SNMP responding?
snmpwalk -v2c -c public clab-cisco-iol-bgp-r1 1.3.6.1.2.1.1.1
#    Expect a sysDescr string mentioning "IOS" or "Cisco".
```

If all four pass: proceed to capture. If 2 or 3 fail: L2 IOL is the
wrong image; close this branch and revisit with a different vrnetlab
build.

## Capture

```sh
./capture.sh
```

Produces files under `captures/`, one per (node, OID-root) pair:

```
captures/
├── clab-cisco-iol-bgp-r1__1_3_6_1_2_1_1.txt          # sys group
├── clab-cisco-iol-bgp-r1__1_3_6_1_2_1_15_3.txt       # bgpPeerTable (RFC 4273)
├── clab-cisco-iol-bgp-r1__1_3_6_1_4_1_9_9_187_1_2_5.txt   # cbgpPeer2Table (HIGH VALUE)
├── clab-cisco-iol-bgp-r1__1_3_6_1_3_5_1_1_2.txt      # bgp4V2PeerTable (IETF draft)
└── (same four files for r2)
```

Captures are gitignored. They're inputs to fixture conversion, not the
fixtures themselves.

## Convert captures to test fixtures

The captures are raw `snmpwalk -On` text. The unit tests in
`internal/discovery/bgp/bgp_v2_test.go` use `[]gsnmp.SnmpPDU` literals.
Conversion is a manual review step — vendor MIB quirks deserve eyes-on
before committing.

For each promising capture file (i.e. the ones that returned real PDU
rows, not "No Such Object"):

1. Open the file. Each line is `<numeric OID> = <type>: <value>`.
2. Translate each line into a `gsnmp.SnmpPDU` literal:
   ```go
   {Name: ".1.3.6.1.4.1.9.9.187.1.2.5.1.3.<idx>", Type: gsnmp.Integer, Value: int(6)},
   ```
3. Compare the resulting literals against the synthetic ones currently
   in `internal/discovery/bgp/bgp_v2_test.go` (search for
   `buildCiscoCbgpPeer2PDUs`). If a column number differs, the
   production walker constants in `internal/discovery/bgp/bgp_vendor.go`
   need to match what IOL actually emits — that's the real bug surface
   issue #1 exists to surface.
4. Land the real-device PDU stream as an additional fixture builder
   (e.g. `buildCiscoCbgpPeer2RealPDUs`) and update the existing
   `TestWalkVendorDispatchCisco` to also exercise it.
5. Document the IOS-XE version the capture came from in a comment near
   the fixture builder so future readers know the column numbers are
   pinned to a specific image.

## Destroy

```sh
containerlab destroy -t ./topology.clab.yml
```

Cleans up the lab containers and the clab management network.

## Troubleshooting

### `containerlab` not found

You're on the Mac host, not inside the OrbStack VM. `orb shell
containerlab-vm` first.

### Image `vrnetlab/cisco_iol:L2-17.12.1` not found

Image isn't loaded into OrbStack's Docker. Run from the Mac:

```sh
bzip2 -dc ~/Code/grafana/network-topology-exporter-resources/cisco_iol-L2-17.12.1.bzip | docker load
```

### `router bgp 65001` rejected in r1.cfg

L2 IOL doesn't support BGP. Close this branch; use a different Cisco
image (CSR1000v, c8000v, or the L3 variant of IOL).

### iBGP peers stay Active/Idle, never Established

- `show ip route 10.0.0.0/30` on both nodes — is the connected route
  present? If `no switchport` was rejected, the interface is still in
  switching mode and `ip address` has no effect.
- `show ip arp` — does r1 see r2's MAC and vice versa?
- Bring the link up by hand if necessary:
  `containerlab exec --name cisco-iol-bgp --label clab-node-name=r1 \
   --cmd "configure terminal; interface Ethernet0/0; no shutdown; end"`

### `snmpwalk` returns `Timeout` for every OID

The lab management network may not be reachable from inside the OrbStack
VM. Try walking via the container's clab IP instead of the hostname:

```sh
containerlab inspect --name cisco-iol-bgp --format json \
  | jq -r '.containers[] | "\(.name) \(.ipv4_address)"'
# then run snmpwalk against the ipv4_address (strip /N suffix)
```
