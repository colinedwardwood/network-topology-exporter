"""Tests for scripts/redact-snmp-capture.py."""
import json
import re
import subprocess
import shutil
from pathlib import Path

import pytest

FIXTURES = Path(__file__).parent / "fixtures"
SCRIPT = Path(__file__).parent.parent.parent / "scripts" / "redact-snmp-capture.py"


@pytest.fixture
def capture_dir(tmp_path):
    """Stage a captures dir with the fixture content."""
    d = tmp_path / "captures"
    d.mkdir()
    shutil.copy(FIXTURES / "capture-with-ips.txt", d / "r1_sample.txt")
    shutil.copy(FIXTURES / "redaction-targets.json", d / "redaction-targets.json")
    return d


def run_redactor(capture_dir, *extra):
    out = capture_dir.parent / "captures-redacted"
    return subprocess.run(
        ["python3", str(SCRIPT), "--in", str(capture_dir), "--out", str(out), *extra],
        capture_output=True, text=True, check=False,
    ), out


def test_ipv4_substituted(capture_dir):
    _, out = run_redactor(capture_dir)
    redacted = (out / "r1_sample.txt").read_text()
    assert "10.0.0.1" not in redacted
    assert "24.150.96.57" not in redacted
    assert "192.0.2." in redacted


def test_mac_substituted(capture_dir):
    _, out = run_redactor(capture_dir)
    redacted = (out / "r1_sample.txt").read_text()
    assert "62:22:32:96:11:e9" not in redacted
    assert "00:00:5e:00:53:" in redacted.lower()


def test_subnet_mask_preserved(capture_dir):
    _, out = run_redactor(capture_dir)
    redacted = (out / "r1_sample.txt").read_text()
    assert "255.255.255.0" in redacted


def test_idempotent(capture_dir):
    _, out1 = run_redactor(capture_dir)
    content1 = (out1 / "r1_sample.txt").read_text()
    shutil.rmtree(out1)
    _, out2 = run_redactor(capture_dir)
    content2 = (out2 / "r1_sample.txt").read_text()
    assert content1 == content2


def test_strict_pass_finds_no_real_ips(capture_dir):
    result, out = run_redactor(capture_dir, "--strict")
    assert result.returncode == 0, result.stderr
    redacted = (out / "r1_sample.txt").read_text()
    for ip in re.findall(r"\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b", redacted):
        octets = [int(x) for x in ip.split(".")]
        in_doc_range = (
            (octets[0] == 192 and octets[1] == 0 and octets[2] == 2) or
            (octets[0] == 198 and octets[1] == 51 and octets[2] == 100) or
            (octets[0] == 203 and octets[1] == 0 and octets[2] == 113)
        )
        is_netmask = is_contiguous_netmask(octets)
        is_loopback = (octets[0] == 127)
        is_zero = (octets == [0, 0, 0, 0])
        assert in_doc_range or is_netmask or is_loopback or is_zero, (
            f"unredacted IP-like value: {ip}"
        )


def is_contiguous_netmask(octets):
    bits = "".join(f"{o:08b}" for o in octets)
    return re.fullmatch(r"1*0*", bits) is not None


def test_redaction_targets_dropped_from_output(capture_dir):
    _, out = run_redactor(capture_dir)
    assert not (out / "redaction-targets.json").exists()


def test_summary_includes_counts(capture_dir):
    result, _ = run_redactor(capture_dir)
    assert "IPv4" in result.stdout
    assert "MAC" in result.stdout
