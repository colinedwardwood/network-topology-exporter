# Upgrades

Guide for upgrading the network topology exporter between releases.

**v1.0.0 is the initial Grafana release.** There are no prior published
versions to migrate from. This document will accumulate per-version upgrade
notes as new releases ship.

## Before every upgrade

1. **Back up `config.yaml`** and any TLS material referenced by
   `listen.web_config_file` or federation mTLS paths.
2. **Back up the snapshot** at `snapshot.path` (default
   `/var/lib/network-topology-exporter/snapshot.json`) if you rely on
   immediate `/metrics` availability across restarts.
3. **Pin versions in production.** Use an explicit image tag or binary
   version — not `:latest` — and read the [CHANGELOG](../../CHANGELOG.md)
   for the target release before rolling out.

## Federation version alignment

There is **no version negotiation** in the federation protocol. Hub and spokes
should run the **same minor version** in steady state. Mixed versions across a
federation may cause push rejections or schema mismatches.

Recommended rollout order for federation fleets:

1. Upgrade the hub (or all hub replicas in HA mode).
2. Upgrade spokes one at a time, confirming `network_topology_federation_spoke_push_outcome_total` on each before proceeding.

## Verifying an upgrade

After deploying the new version:

```sh
curl -sf http://localhost:9100/readyz
curl -sf http://localhost:9100/healthz
./topology-exporter --version
```

Confirm discovery is running:

```sh
curl -s http://localhost:9100/metrics | grep network_topology_discovery_cycle_duration_seconds
```

## Related documents

- [`docs/operator/stability.md`](stability.md) — semver-stable surfaces
- [`docs/release.md`](../release.md) — maintainer release process
- [`CHANGELOG.md`](../../CHANGELOG.md) — per-version change notes
