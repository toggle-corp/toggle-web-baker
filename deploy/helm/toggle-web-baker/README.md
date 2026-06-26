# toggle-web-baker Helm chart

Deploys the **FrontendApp** deploy-platform operator (`baker.toggle-corp.com`)
and, optionally, the read-only admin console.

Published per release as an OCI artifact:

```
oci://ghcr.io/toggle-corp/toggle-web-baker-helm
```

Chart `version`, `appVersion`, and every image tag move in lockstep with the
git release tag.

## What it installs

Always:
- `FrontendApp` CRD (guarded by `crds.install`, default `true`)
- operator `ServiceAccount` + `ClusterRole` + `ClusterRoleBinding`
- operator `Deployment`

When `console.enabled=true`:
- console `Deployment` + `Service` + RBAC (read-only, plus the single annotation
  PATCH the rebuild button needs)
- `oauth2-proxy` `Deployment` + `Service` (+ a `Secret` unless
  `console.oauth2Proxy.existingSecret` is set)
- Traefik forward-auth `Middleware` + two `Ingress` objects

## Required configuration

`operator.clusterCIDRs` is **mandatory** — the pod + Service CIDRs excluded from
build-pod egress. The operator refuses to start (Ready=False) if it is empty;
there is no safe default.

```bash
helm install baker oci://ghcr.io/toggle-corp/toggle-web-baker-helm \
  --namespace toggle-baker-system --create-namespace \
  --set 'operator.clusterCIDRs={10.0.0.0/8,172.20.0.0/16}'
```

Enable the console (provide a GitHub OAuth app, or reference an existing secret):

```bash
helm upgrade baker oci://ghcr.io/toggle-corp/toggle-web-baker-helm \
  --namespace toggle-baker-system \
  --set 'operator.clusterCIDRs={10.0.0.0/8}' \
  --set console.enabled=true \
  --set console.host=baker-console.example.org \
  --set console.oauth2Proxy.existingSecret=baker-oauth2-proxy
```

## Image references — tags, not digests (for now)

The operator stamps platform helper images (`clone`/`copier`/`du`/`cleanup`)
onto the pods it creates. This chart passes them by **tag**
(`repository:appVersion`), not by digest. That deliberately relaxes the
`digest-pinned, no user override` platform-image invariant in
[`docs/operator-security-invariants.md`](../../../docs/operator-security-invariants.md)
in exchange for a simpler lockstep release. Harden later by resolving
tag→digest in CI before packaging the chart.

## CRD lifecycle

The CRD ships in `templates/` (not Helm's `crds/`), so `helm upgrade` applies
schema changes each release. It carries `helm.sh/resource-policy: keep`, so
`helm uninstall` leaves the CRD (and existing `FrontendApp` CRs) in place.

The CRD body in `templates/crd.yaml` is a wrapped copy of
`config/crd/baker.toggle-corp.com_frontendapps.yaml`. After `just manifests`
regenerates that source, re-sync the chart copy (see the repo CONTRIBUTING /
release notes). The `helm-snapshots` test guards against accidental drift in the
rest of the chart.

## Snapshot tests

`helm template` golden snapshots live alongside the chart (`tests.yaml`,
`tests/`, `snapshots/`) and are driven by fugit's `helm-update-snapshots.sh`:

```bash
just helm-snapshots                  # refresh snapshots
just helm-snapshots --check-diff-only # CI mode: fail on drift
```
