# Security Policy

## Supported versions

The project has not yet reached a stable v1.0 GA (see the pre-release notice in [README.md](README.md)). Until the stability banner is removed, only the current minor release receives security fixes.

| Version series | Supported |
|---|---|
| Current minor (latest `-rc.N` tag) | Yes — patches are applied and a new RC is cut |
| Previous minor | Best-effort: critical-severity issues are backported if the fix is self-contained; lower severity is not |
| Any version two or more minors behind | Not supported |

When v1.0 GA ships, this table will be updated to: current minor = fully supported; previous minor = security fixes only.

## Reporting a vulnerability

**Do not file a public GitHub issue for unpatched vulnerabilities.** Public issues expose the vulnerability to all readers before a fix is available.

Use the GitHub **Private Security Advisory** path:

1. Open [https://github.com/colinedwardwood/network-topology-exporter/security/advisories/new](https://github.com/colinedwardwood/network-topology-exporter/security/advisories/new) — or navigate to the repository on GitHub, click **Security** → **Report a vulnerability**.
2. Fill in the advisory form: affected versions, reproduction steps, and your assessment of severity (CVSSv3 vector is appreciated but not required).
3. Submit. The advisory is private by default; only you and the maintainers can see it.

> **Maintainer note:** Private Vulnerability Reporting must be enabled in the repository settings before the "Report a vulnerability" button appears. Navigate to **Settings → Security → Code security and analysis → Private vulnerability reporting** and enable it.

Alternatively, send a PGP-encrypted report to **colin.wood@grafana.com** with the subject line `[SECURITY] network-topology-exporter — <brief description>`. A public key is available at the email address above; request it if needed.

## Response SLA

| Milestone | Target |
|---|---|
| Acknowledge receipt | ≤72 hours |
| Status update (accepted / needs more info / not applicable) | ≤7 days of acknowledgement |
| Patch + advisory published (for accepted reports) | Coordinated with reporter; target ≤30 days for critical, ≤90 days for moderate |

If a deadline cannot be met, the maintainer will communicate the delay and reason before the window closes.

## What is in scope

The following surfaces are in scope for vulnerability reports:

- **SNMP parsing.** The exporter processes device-controlled bytes in every discovery module. Crashes, panics, or memory corruption triggered by crafted SNMP responses are high priority.
- **Federation hub/spoke channel.** Authentication bypass, payload injection, or denial-of-service on `/spoke/push`.
- **`/metrics` authentication bypass.** When `listen.web_config_file` is configured with TLS or basic auth, bypassing that layer is in scope.
- **Credential leakage.** Any path that exposes SNMP credential bytes outside the mitigations described in `docs/operator/security.md`.
- **Snapshot tampering.** A crafted `snapshot.json` that causes the exporter to crash, execute arbitrary code, or emit false data on restart.
- **Supply-chain integrity.** Tampering with release artefacts, CI workflows, or the cosign signing path.

## What is out of scope

- **General Prometheus exposition-format parsing quirks.** Downstream scrapers have their own security posture.
- **Denial-of-service from the management network itself.** The exporter runs on a trusted management plane; SNMP-level flooding of managed devices is an operator network-design problem, not an exporter vulnerability.
- **Vendor MIB data that is semantically false but syntactically valid.** A compromised router returning false topology is outside the exporter's trust model — see `docs/operator/threat-model.md`.

## Credit and disclosure policy

- **Credit.** Reporters who find valid security issues are credited in the Security Advisory and in the CHANGELOG entry for the fixing release, by name or handle as they prefer. Anonymous reports are honoured if the reporter requests it.
- **Coordinated disclosure.** The default is 90 days from initial acknowledgement, after which the reporter may disclose regardless of patch status. Reporters who need more or less time should say so upfront — the maintainer will accommodate reasonable requests.
- **CVE assignment.** GitHub Security Advisories can request a CVE from GitHub's CNA. The maintainer will request one for all accepted advisories that cross the CVE reporting threshold (broadly: exploitable by an external actor or leading to loss of confidentiality/integrity/availability).

## Related documents

- `docs/operator/security.md` — credential-handling threat model and hardening guidance
- `docs/operator/threat-model.md` — STRIDE matrix for the full binary surface
- `docs/operator/federation.md` — mTLS PKI setup and certificate rotation
