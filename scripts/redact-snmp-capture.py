#!/usr/bin/env python3
"""Receipt-side redactor for the colleague-capture toolkit.

Substitutes IPv4, IPv6, and MAC values from documentation ranges. Leaves
netmasks, loopback, multicast, and ASNs alone. Idempotent.
"""
from __future__ import annotations

import argparse
import hashlib
import ipaddress
import json
import re
import shutil
import sys
from pathlib import Path

IPV4_POOLS = [
    ipaddress.IPv4Network("192.0.2.0/24"),
    ipaddress.IPv4Network("198.51.100.0/24"),
    ipaddress.IPv4Network("203.0.113.0/24"),
]
DOC_V4_NETS = IPV4_POOLS  # alias used in strict_check
IPV6_GLOBAL_POOL = ipaddress.IPv6Network("2001:db8::/32")
IPV6_LINKLOCAL_BASE = ipaddress.IPv6Address("fe80::5e:0:53:0")
# IANA VRRP MAC range — 00:00:5E:00:53:xx gives 256 slots (6-octet MACs).
MAC_BASE = "00:00:5e:00:53:"


def is_loopback_v4(s):
    try:
        return ipaddress.IPv4Address(s).is_loopback
    except ValueError:
        return False


def is_multicast_v4(s):
    try:
        return ipaddress.IPv4Address(s).is_multicast
    except ValueError:
        return False


def is_zero_or_bcast(s):
    return s in {"0.0.0.0", "255.255.255.255"}


def is_contiguous_netmask(s):
    try:
        bits = "{:032b}".format(int(ipaddress.IPv4Address(s)))
    except ValueError:
        return False
    return re.fullmatch(r"1*0*", bits) is not None


def is_linklocal_v6(addr):
    return addr.is_link_local


def _in_doc_v4_range(s):
    """Return True if s is already a substitution value (doc range)."""
    try:
        ip = ipaddress.IPv4Address(s)
    except ValueError:
        return False
    return any(ip in n for n in DOC_V4_NETS)


def _is_sub_mac(s):
    """Return True if s is already an anonymised MAC (IANA VRRP prefix)."""
    return s.lower().startswith(MAC_BASE)


class SubstitutionMap:
    def __init__(self, keep_loopback=True, keep_multicast=True):
        self.v4 = {}
        self.v6 = {}
        self.mac = {}
        self._v4_iter = self._iter_v4()
        self._v6_global_iter = self._iter_v6_global()
        self._v6_ll_iter = self._iter_v6_linklocal()
        self._mac_idx = 0
        self.keep_loopback = keep_loopback
        self.keep_multicast = keep_multicast

    def _iter_v4(self):
        for pool in IPV4_POOLS:
            for ip in pool.hosts():
                yield str(ip)

    def _iter_v6_global(self):
        addr = int(IPV6_GLOBAL_POOL.network_address) + 1
        while True:
            yield str(ipaddress.IPv6Address(addr))
            addr += 1

    def _iter_v6_linklocal(self):
        addr = int(IPV6_LINKLOCAL_BASE)
        while True:
            yield str(ipaddress.IPv6Address(addr))
            addr += 1

    def _next_mac(self):
        if self._mac_idx >= 256:
            raise RuntimeError("MAC substitution pool exhausted")
        lo = self._mac_idx & 0xFF
        self._mac_idx += 1
        return f"{MAC_BASE}{lo:02x}"

    def get_v4(self, real):
        if is_zero_or_bcast(real):
            return real
        if is_contiguous_netmask(real):
            return real
        if self.keep_loopback and is_loopback_v4(real):
            return real
        if self.keep_multicast and is_multicast_v4(real):
            return real
        # Already a substitution value — pass through to ensure idempotency.
        if _in_doc_v4_range(real):
            return real
        if real not in self.v4:
            self.v4[real] = next(self._v4_iter)
        return self.v4[real]

    def get_v6(self, real):
        try:
            addr = ipaddress.IPv6Address(real)
        except ValueError:
            return real
        if addr in (ipaddress.IPv6Address("::"), ipaddress.IPv6Address("::1")):
            return real
        if self.keep_multicast and addr.is_multicast:
            return real
        # Already in 2001:db8::/32 — it's a substituted global address, pass through.
        if addr in IPV6_GLOBAL_POOL:
            return real
        if is_linklocal_v6(addr):
            if real not in self.v6:
                self.v6[real] = next(self._v6_ll_iter)
            return self.v6[real]
        if real not in self.v6:
            self.v6[real] = next(self._v6_global_iter)
        return self.v6[real]

    def get_mac(self, real):
        real_lc = real.lower()
        # Already a substitution value — pass through to ensure idempotency.
        if _is_sub_mac(real_lc):
            return real_lc
        if real_lc not in self.mac:
            self.mac[real_lc] = self._next_mac()
        return self.mac[real_lc]


MAC_RE = re.compile(
    r"\b(?:[0-9a-fA-F]{2}[:\-]){5}[0-9a-fA-F]{2}\b"
)
HEXSTRING_MAC_RE = re.compile(
    r"(Hex-STRING:\s+)([0-9a-fA-F]{2}(?:\s+[0-9a-fA-F]{2}){5})\b"
)
IPV4_RE = re.compile(r"\b(?:\d{1,3}\.){3}\d{1,3}\b")
IPV6_RE = re.compile(
    r"\b(?:[0-9a-fA-F]{1,4}:){2,7}[0-9a-fA-F]{0,4}(?::[0-9a-fA-F]{1,4})*\b"
)


def apply_substitutions(text, sub):
    def _mac(m):
        return sub.get_mac(m.group(0))

    def _hex_mac(m):
        prefix = m.group(1)
        bytes_str = m.group(2)
        colon_form = ":".join(bytes_str.split())
        sub_mac = sub.get_mac(colon_form)
        space_form = " ".join(sub_mac.split(":")).lower()
        return f"{prefix}{space_form}"

    def _v6(m):
        token = m.group(0)
        try:
            ipaddress.IPv6Address(token)
        except ValueError:
            return token
        return sub.get_v6(token)

    def _v4(m):
        token = m.group(0)
        try:
            ipaddress.IPv4Address(token)
        except ValueError:
            return token
        return sub.get_v4(token)

    text = HEXSTRING_MAC_RE.sub(_hex_mac, text)
    text = MAC_RE.sub(_mac, text)
    text = IPV6_RE.sub(_v6, text)
    text = IPV4_RE.sub(_v4, text)
    return text


def strict_check(text):
    leaks = []
    for m in IPV4_RE.findall(text):
        try:
            ip = ipaddress.IPv4Address(m)
        except ValueError:
            continue
        if any(ip in n for n in DOC_V4_NETS):
            continue
        if is_zero_or_bcast(m) or is_contiguous_netmask(m):
            continue
        if ip.is_loopback or ip.is_multicast:
            continue
        leaks.append(m)
    for m in MAC_RE.findall(text):
        if not m.lower().startswith(MAC_BASE):
            leaks.append(m)
    return leaks


def redact_dir(in_dir, out_dir, sub):
    if out_dir.exists():
        shutil.rmtree(out_dir)
    out_dir.mkdir(parents=True)

    for path in sorted(in_dir.rglob("*")):
        if path.is_dir():
            continue
        if path.name == "redaction-targets.json":
            continue
        rel = path.relative_to(in_dir)
        out_path = out_dir / rel
        out_path.parent.mkdir(parents=True, exist_ok=True)
        if path.name == "SHA256SUMS":
            continue
        try:
            text = path.read_text()
        except UnicodeDecodeError:
            shutil.copy(path, out_path)
            continue
        new_text = apply_substitutions(text, sub)
        out_path.write_text(new_text)


def recompute_sha256sums(out_dir):
    lines = []
    for path in sorted(out_dir.rglob("*")):
        if path.is_dir() or path.name == "SHA256SUMS":
            continue
        h = hashlib.sha256(path.read_bytes()).hexdigest()
        rel = path.relative_to(out_dir)
        lines.append(f"{h}  ./{rel}")
    (out_dir / "SHA256SUMS").write_text("\n".join(lines) + "\n")


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--in", dest="in_dir", default="./captures")
    p.add_argument("--out", dest="out_dir", default="./captures-redacted")
    p.add_argument("--map", dest="map_file", default=None)
    p.add_argument("--strict", action="store_true", default=True)
    p.add_argument("--no-strict", dest="strict", action="store_false")
    p.add_argument("--keep-loopback", action="store_true", default=True)
    p.add_argument("--keep-multicast", action="store_true", default=True)
    args = p.parse_args()

    in_dir = Path(args.in_dir)
    out_dir = Path(args.out_dir)
    if not in_dir.is_dir():
        print(f"error: input dir not found: {in_dir}", file=sys.stderr)
        sys.exit(2)

    sub = SubstitutionMap(
        keep_loopback=args.keep_loopback,
        keep_multicast=args.keep_multicast,
    )

    map_file = Path(args.map_file) if args.map_file else (in_dir / "redaction-targets.json")
    if map_file.exists():
        targets = json.loads(map_file.read_text())
        for v in sorted(targets.get("ipv4", [])):
            sub.get_v4(v)
        for v in sorted(targets.get("ipv6", [])):
            sub.get_v6(v)
        for v in sorted(targets.get("mac", [])):
            sub.get_mac(v)

    redact_dir(in_dir, out_dir, sub)
    recompute_sha256sums(out_dir)

    leaks = []
    for path in out_dir.rglob("*"):
        if path.is_dir() or path.name == "SHA256SUMS":
            continue
        try:
            text = path.read_text()
        except UnicodeDecodeError:
            continue
        leaks.extend(strict_check(text))

    debug_map = out_dir / ".redaction-map.json"
    debug_map.write_text(json.dumps({
        "_warning": "DO NOT COMMIT — contains real network identifiers",
        "ipv4": sub.v4, "ipv6": sub.v6, "mac": sub.mac,
    }, indent=2))

    print(f"Redacted: IPv4 {len(sub.v4)}, IPv6 {len(sub.v6)}, MAC {len(sub.mac)}")
    if leaks:
        print(f"WARNING: strict-pass found {len(leaks)} unredacted-looking values:", file=sys.stderr)
        for x in leaks[:10]:
            print(f"  {x}", file=sys.stderr)
        if args.strict:
            sys.exit(1)


if __name__ == "__main__":
    main()
