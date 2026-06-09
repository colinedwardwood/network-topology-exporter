# Kustomize manifests

Plain-YAML Kubernetes manifests for `network-topology-exporter`, for shops that
deploy with `kubectl apply -k`, Argo CD, or Flux rather than Helm.

These manifests **mirror the rendered output of the Helm chart** in
[`deploy/helm/topology-exporter`](../helm/topology-exporter). The two install
paths must stay in sync — see [Relationship to the Helm chart](#relationship-to-the-helm-chart).

## Layout

```
deploy/kustomize/
  base/                       # all shared resources (do not apply directly)
    serviceaccount.yaml
    configmap.yaml            # sample config (federation.role: standalone)
    secret.yaml               # PLACEHOLDER values only — replace before applying
    service.yaml
    networkpolicy.yaml
    deployment.yaml
    kustomization.yaml
  overlays/
    standalone/               # config.federation.role: standalone
    hub/                      # config.federation.role: hub
    spoke/                    # config.federation.role: spoke
  components/
    egress-networkpolicy/     # opt-in: lock down egress (DNS/SNMP/OTLP/hub)
```

## Hardening: opt-in egress NetworkPolicy

The base NetworkPolicy is **Ingress-only** — a compromised credential-bearing
SNMP scanner would have unrestricted outbound (lateral) reach. The
`components/egress-networkpolicy` component adds `Egress` to `policyTypes` with
allow-rules for DNS, SNMP polling, the OTLP collector, and hub federation.
Enable it from an overlay:

```yaml
# in overlays/<mode>/kustomization.yaml
components:
  - ../../components/egress-networkpolicy
```

With Egress enabled, **only the listed destinations are reachable**. The
component **fails safe (deny-until-edit)**: the SNMP/OTLP/hub example CIDRs ship
as `0.0.0.0/32` placeholders — a non-routable single address that matches no
real traffic — so applying this component **unedited DENIES all SNMP/OTLP/hub
egress** until you replace each `0.0.0.0/32` with your real CIDRs (and adjust
ports). The DNS rule is scoped to the `kube-system` namespace; change it if your
cluster DNS lives elsewhere. (Helm users set `networkPolicy.egress.*`.)

## Federation modes

Each overlay selects one federation mode via `config.federation.role`
(see `internal/config` — valid roles: `standalone`, `uncoordinated`, `spoke`,
`hub`). Apply exactly one overlay; never apply `base/` on its own.

| Overlay      | `federation.role` | What it does                                                                 |
|--------------|-------------------|------------------------------------------------------------------------------|
| `standalone` | `standalone`      | Single-instance discovery, no federation. The default.                       |
| `hub`        | `hub`             | Pure aggregator. Receives spoke pushes on a separate mTLS listener (`:9101`). |
| `spoke`      | `spoke`           | Local discovery + pushes its domain graph to a hub over mTLS.                |

## Apply

```sh
# Standalone (default single-instance mode)
kubectl apply -k deploy/kustomize/overlays/standalone

# Hub (aggregator)
kubectl apply -k deploy/kustomize/overlays/hub

# Spoke (pushes to a hub)
kubectl apply -k deploy/kustomize/overlays/spoke
```

Render without applying to inspect the result:

```sh
kubectl kustomize deploy/kustomize/overlays/standalone
```

## Customising config and secrets

- **Exporter config** lives in the `topology-exporter` ConfigMap
  (`config.yaml`). Edit `base/configmap.yaml` for cross-mode defaults, or the
  per-mode `overlays/<mode>/configmap.yaml` for that mode. The discovery scope,
  modules, credentials, and `targets` are environment-specific and intended to
  be edited.
- **Secrets** — `base/secret.yaml` ships with **placeholder values only**
  (`REPLACE_ME`). Never commit real secrets. Either replace the placeholders
  before applying, or (preferred) manage the Secret out-of-band
  (Sealed Secrets, External Secrets, SOPS, Vault) and drop `secret.yaml` from
  `base/kustomization.yaml`. Consume secret values in the Deployment via
  `env`/`valueFrom.secretKeyRef`.
- **Image / tag** — pin per environment:

  ```sh
  cd deploy/kustomize/overlays/standalone
  kustomize edit set image \
    ghcr.io/colinedwardwood/network-topology-exporter=ghcr.io/colinedwardwood/network-topology-exporter:1.6.0
  ```

  (or edit `images:` in `base/kustomization.yaml`).

### Federation mTLS (hub and spoke)

The hub and spoke overlays reference TLS material at
`/etc/topology-exporter/pki/{ca.crt,tls.crt,tls.key}`. Provide it by creating a
TLS Secret and mounting it into the Deployment (add a `volumes`/`volumeMounts`
patch in the overlay):

```sh
kubectl create secret generic topology-exporter-pki \
  --from-file=ca.crt --from-file=tls.crt --from-file=tls.key
```

For the spoke, also set a unique `federation.spoke.spoke_id` and the correct
`hub_url` (the hub's Service DNS name on port `9101`).

## Relationship to the Helm chart

The Helm chart (`deploy/helm/topology-exporter`) is the source of truth for the
resource shape: names, labels, ports, securityContext, probes, and RBAC. These
Kustomize manifests reproduce the chart's **default** rendered output so the two
paths do not drift.

**When you change the chart, update these manifests to match.** Regenerate the
reference output and diff it:

```sh
helm template te deploy/helm/topology-exporter
```

The `kustomize-smoke` CI workflow only validates that the overlays *render*; it
does not detect drift from the chart, so keeping them in sync is a manual step
in any PR that touches the chart's rendered shape.
