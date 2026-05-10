# Cold-start credentials

Bringing the topology exporter up against a fleet of devices for the first time is the moment most likely to lock devices out of their own monitoring. This runbook covers getting through that window without tripping anyone's auth-lockout policy.

## What can go wrong

A fresh exporter has nothing in its credential cache. The first cycle for every target enters the trial sequence — explicit assignments first, most-specific CIDR next, then the fallback list. Each candidate that fails counts against the device's authentication-failure counter. Switches and routers from most vendors implement some form of lockout: three consecutive failures and the account is disabled, the SNMP community is rate-limited, or the management interface stops responding for a while. Multiply that by a thousand devices and three candidate profiles each and a naive cold start can take an entire fleet offline for monitoring inside one cycle.

The token-bucket trial limiter (`credentials.trial_rate_per_second`, default 5) caps the global trial rate so the exporter doesn't fire all those auth attempts simultaneously. The limiter slows the cold start, it doesn't prevent the lockout. Slowing the cold start is enough only if the candidate set per device is small.

## The cold-start sequence

The recommended sequence is single-profile-first, expand-once-stable.

Start with one credential profile that you've already tested manually against a representative device. List it as the only entry under `credentials.profiles:` and as the only entry in `credentials.fallback_order:`. Bring the exporter up. Watch `network_topology_credential_trials_total{status="ok"}` climb and `{status="failed"}` stay near zero. The cache fills as cycles complete and `network_topology_snapshot_loaded_devices_total` reflects what the snapshot now knows on the next restart.

Once the cache is stable, add the second profile. The cache means devices that already authenticated under the first profile won't enter the trial path; only devices that failed under the first profile will trial the second. Repeat until every device has a cached profile.

If a target rig has a small number of representative devices, a faster path is to put one device per credential class in `credentials.assignments:` with the right profile pinned, then bring the exporter up with the full profile list at the same time. Pinned assignments skip the trial path entirely.

## Recovery

If you do trip a lockout, the recovery sequence is:

1. Stop the exporter.
2. Wait the lockout window (vendor-dependent; typically 5–15 minutes for SNMP, longer for SSH).
3. Reduce `credentials.trial_rate_per_second` to 1.
4. Bring the exporter up with the single profile that's known to work.
5. Confirm the cache fills and the metric `network_topology_credential_trials_total{status="failed"}` stays flat for one full cycle.
6. Restore the rate and add other profiles per the cold-start sequence above.

The cache is keyed on `Device.ID` (sysName), so it survives IP renumbering. It does not survive a snapshot version bump (the exporter falls through to a cold start on schema mismatch); plan for the cold-start sequence again whenever the snapshot schema changes.

## What to watch

`network_topology_credential_trials_total{status="failed"}` is the leading indicator. A non-trivial rate after the cache has had time to fill means a device is rejecting every profile and re-entering the trial sequence on every cycle — that device needs an explicit assignment or a new profile.

`network_topology_graph_stale` should drop from 1 to 0 within one `discovery.interval` of startup. If it stays at 1 longer than two intervals, the first live cycle isn't completing — usually a credential or scope (CIDR allow-list) problem rather than a network problem.
