# toggle-web-baker

Baker turns a git repository into a live static website on your Kubernetes
cluster. You create one `App` resource that says *where the code lives* and
*how to build it* — Baker takes care of everything else: cloning, installing
dependencies, running the build, publishing the output behind an Ingress, and
rebuilding it again when the code changes.

It is made for teams that run many small frontend sites (dashboards, mapping
tools, campaign pages) and don't want a CI pipeline, a Dockerfile, and a
deployment manifest for each one.

## What it does for you

- **Builds from git** — point at a repo and branch; Baker clones it and runs
  your build command in a managed Node toolchain (`nodeVersion: 18`, `24`, …).
- **Serves the result** — the build output is published under the hostname you
  choose, with optional TLS and HTTP basic auth.
- **Rebuilds automatically** — on a cron schedule (`scheduledBuilds`), when new
  commits land on the branch (`watchCommits`), or on demand with one click in
  the console.
- **Keeps history** — the last `keepReleases` builds are retained; if a build
  fails, the site keeps serving the last good release and the app is marked
  `Degraded` instead of going down.
- **Watches disk usage** — per-app storage is measured and pruned against
  thresholds you set, so old caches and releases don't quietly fill a volume.
- **Shows you what's happening** — a web console lists every app with its
  build status, timings, memory use, storage, and logs; `kubectl get apps`
  gives the same at a glance.

## Installing

The operator ships as a Helm chart:

```bash
helm upgrade --install baker oci://ghcr.io/toggle-corp/toggle-web-baker-helm \
  --namespace toggle-baker-system --create-namespace \
  --set 'operator.clusterCIDRs={10.0.0.0/8,172.20.0.0/16}'  # your cluster's pod + service CIDRs
```

`operator.clusterCIDRs` is the only mandatory setting (it fences build pods
off from your cluster network). The chart can also deploy the admin console
behind GitHub OAuth (`console.enabled=true`). All options are documented in
[deploy/helm/toggle-web-baker/README.md](deploy/helm/toggle-web-baker/README.md).

## Deploying a site

Create an `App` and apply it:

```yaml
apiVersion: baker.toggle-corp.com/v1alpha1
kind: App
metadata:
  name: my-site
spec:
  repo: https://github.com/my-org/my-site
  ref: main
  keepReleases: 3
  scheduledBuilds:
    enabled: true
    schedule: "0 6 * * *"     # rebuild every morning
  watchCommits:
    enabled: true             # ...and whenever main moves
  ingress:
    host: my-site.example.com
    className: nginx
  pipeline:
    packageManager: yarn
    nodeVersion: 24
    phases:
      build:
        command: ["yarn", "build"]
        outputDir: dist
```

```bash
kubectl apply -f my-site.yaml
kubectl get apps            # phase, last build result, storage, last success
```

Baker installs dependencies with the declared package manager automatically;
the `pipeline.phases` block lets you override or extend each step when a site
needs more:

- **setup** — dependency install (defaulted from `packageManager`; set
  `skip: true` to opt out).
- **fetch** — an optional pre-build step for pulling data or assets; this is
  where `secrets:` (API tokens etc., from Kubernetes Secrets) are exposed.
- **build** — your build command and its `outputDir`; supports `env`,
  `memoryLimit`, `timeout`, and a custom `image` if the managed Node images
  don't fit.

Private repositories work via `repoAuth.secretRef` (an SSH key or token in a
Secret). Public GitHub repos need nothing — SSH-style URLs even fall back to
anonymous HTTPS when no credential is provided.

## Day-2: watching and operating your apps

The **console** (if enabled) is the everyday view: every app across the
cluster, grouped and searchable, with a build-flow timeline per app (how long
each phase took, how much memory it used), live logs while a build runs,
storage bars, and a **Rebuild** button. It is read-only by design — the one
thing it can do is request a rebuild, which it records with your GitHub
username.

Without the console, everything is on the resource itself:

```bash
kubectl get app my-site -o yaml      # full status: conditions, build history, storage
kubectl annotate app my-site \
  rebuild.baker.toggle-corp.com/requested-at="$(date -u +%FT%TZ)"   # manual rebuild
```

An app's conditions tell you the state at a glance: `Ready` (serving),
`BuildSucceeded`, `IngressReady`, and `Degraded` (serving an old release
because the latest build failed).

## Good to know

- Builds run in locked-down pods: pinned platform images, no Kubernetes API
  access, egress fenced off from the cluster network, and custom build images
  restricted to an allowlist you configure.
- The operator, console, and all helper images are versioned and released
  together; upgrading is a single `helm upgrade`.
- For every `App` spec/status field — defaults, validation, and behaviour — see
  the [App CRD reference](docs/app-crd-reference.md) (generated from the CRD, so
  it matches `kubectl explain app` exactly).

For repo layout, tests, and contribution workflow, see
[AGENTS.md](AGENTS.md).
