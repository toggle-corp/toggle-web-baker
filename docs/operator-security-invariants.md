# Operator security invariants

These are **operator-hardcoded** properties of the FrontendApp deploy platform.
They are **not** expressed as CRD spec fields, so a careless or malicious CR
edit cannot weaken them. The test for "hardcoded vs. CR field": if a wrong value
could weaken isolation or let untrusted code do something new → hardcode; if it
only changes the app's own resource/timing/output behavior → CR field.

## Threat model (baseline)

**Trusted CR authors, untrusted build/dependency code.** CR authorship is
platform-team-only (RBAC + review). The controls below do **not** defend against
a malicious *author*; they defend against **untrusted code executing in the
build** — primarily a compromised npm/dependency (supply-chain) running during
`yarn|pnpm install` / `build`. This is why network-egress whitelisting and a
validating webhook are deliberately **out of scope**, while the in-pod controls
(no SA token, read-only fs, credential boundary, copier airlock) are load-bearing.

## Build pod (clone / setup / fetch / build)

- **`securityContext`:** `runAsNonRoot: true`, `readOnlyRootFilesystem: true`
  (except the work + cache volumes), `automountServiceAccountToken: false`,
  `allowPrivilegeEscalation: false`, `capabilities: drop: [ALL]`,
  `seccompProfile: RuntimeDefault`. **Applied to every container in the pod.**
- **One pod, init-container ordering is load-bearing:** `clone → setup → fetch →
  build` are **init containers**; `copy` is the **single main container**, so it
  starts only after every init container (incl. `build`) succeeds. A failed build
  ⇒ the copier never runs ⇒ the last-good release keeps serving.
- **`clone` image** is platform-owned and **digest-pinned**; no user override.
  Anonymous clone (repos are public); any future private-repo credential is held
  only by `clone` and **never forwarded** to later phases.
  > ⚠️ **Current relaxation:** the Helm chart (`deploy/helm/toggle-web-baker`)
  > passes the platform helper images (`clone`/`copier`/`du`/`cleanup`) by **tag**
  > (`repository:appVersion`), not by digest — a deliberate trade for a simpler
  > lockstep release. The "digest-pinned, no user override" guarantee currently
  > holds only for `go run`/non-helm deploys using the `config.go` defaults. Harden
  > by resolving tag→digest in CI before packaging the chart (see chart README).
- **Build container NEVER mounts the output PVC** (only the scratch work volume +
  cache). The copier is the sole writer to output.
- **`backoffLimit: 0`** (a failed build fails once → `Degraded`; last-good keeps
  serving). **`activeDeadlineSeconds`** bounds wedged builds: it is
  `spec.activeDeadlineSeconds` when set, otherwise the operator config default
  (`activeDeadlineSeconds` in the mounted config file). The former hardcoded
  1800s literal moved into operator config.
- **Every phase container (setup / fetch / build) carries resource requirements
  with `memory request == limit`** (memory is incompressible ⇒ Guaranteed QoS,
  so a heavy build can't OOM the node via a low-request/high-limit gap). The
  ceiling is the app's per-phase `spec.<phase>.memoryLimit` when set, else the
  operator per-phase default. **A malformed user `memoryLimit` falls back to the
  operator per-phase default (never unlimited)** — the defaults are parse-validated
  at operator startup, so the fallback always yields a concrete limit. CPU
  request/limit are the global operator defaults (same for every phase).

## Credential boundary

- Two env classes, enforced at **injection** by the operator:
  - **`buildArgs`** — public, **ConfigMap**-sourced, may reach the bundle
    (`VITE_*`, `NEXT_PUBLIC_*`). Allowed in setup/build. **No `secretKeyRef`.**
  - **`secrets`** — **Secret**-sourced, injected into the **fetch container
    ONLY**. The operator rejects `secretKeyRef` in setup/build (CEL + reconcile
    enforcement).
- **Data-flow caveat (documented invariant, not enforceable):** init containers
  share one work volume, so a secret the (trusted) fetch script writes into
  `phase-env` or `dataCache` becomes readable by the build container — where
  untrusted dependency code runs. Therefore: **the fetch script must use the
  secret only to make its API call and must never write it (raw) to `phase-env`
  or `dataCache`.** Only derived, non-secret data flows forward. `phase-env`
  carries scalars by convention (e.g. `DATA_LAST_MODIFIED`), never credentials.
  The operator cannot enforce this; it is a load-bearing contract on the trusted
  author. Residual risk (a fetch author who deliberately relays the secret) is
  accepted under the threat model.

## Copier (airlock)

- **Platform-owned image**, the **sole writer** to the output PVC.
- **Pre-copy size gate (primary capacity guard):** `du -sb` the build output on
  the **work volume** *before* writing anything to the output PVC; reject if it
  exceeds the cap. (A post-assemble flip-gate remains as defense-in-depth, but
  the bytes must be stopped *before* they land on the output PVC.)
- **Free-space gate:** `df` the output filesystem; require
  `source + headroom ≤ free`. Note: local-path PVCs share the node filesystem, so
  this is node-global by design; a cross-app TOCTOU over-commit is **accepted**
  for UAT and backstopped by the node-exporter `PrometheusRule` (alert tier 1).
- **Gate sequence:** retention sweep → measure source → check (`source ≤ cap` and
  `source + headroom ≤ free`) → copy → flip-gate → atomic flip.
- **Hardening:** `rsync --safe-links` (strip symlinks), `chown` output to the
  platform user (so nginx `disable_symlinks if_not_owner` is correct), reject path
  traversal / odd filenames.
- **Atomic deploy:** assemble `releases/<ts>` out of nginx's sight, then atomic
  `rename` of the `current` symlink (`ln -sfn`). No half-written-dir window.
- **Build-derived status leaves the pod via the copier's TERMINATION MESSAGE**
  (`/dev/termination-log`, ≤4KB JSON) — the operator reads it (it has `get` on
  pods) and writes status. The pod has no API access; this is the only channel.

## nginx (serving)

- `disable_symlinks if_not_owner`; fixed document root `/output/current`;
  **read-only** output mount; **single replica**; `Recreate` strategy.
- Co-locates with the output PV by **mounting the already-bound PVC** (no explicit
  node affinity needed). Created **only after the first successful deploy**.
- Separate NetworkPolicy: **ingress only from the Traefik controller**; egress
  DNS only.

## Network

- **Namespace-per-app**, default-deny ingress on the build pod.
- **Build pod egress** (NetworkPolicy is per-pod, so build/fetch cannot be split):
  allow **DNS** + allow `0.0.0.0/0` **EXCEPT** the cluster pod/service CIDRs
  (**mandatory operator config — no default**; operator refuses / `Ready=False`
  if unset) and **`169.254.169.254/32`** (node/cloud metadata — baked default).
  FQDN whitelisting is **not** attempted (not expressible in vanilla
  NetworkPolicy; no Cilium/Calico-FQDN dependency taken).

## Scheduling, storage, deletion

- Build scheduling is driven by a **CronJob-as-clock** that only bumps the rebuild
  annotation; the **operator is the sole build-Job creator** and enforces
  **single-active-build-per-app** (removes the manual-vs-scheduled write race).
- **StorageClass must be `WaitForFirstConsumer`** (operator validates →
  `InvalidStorageClass` otherwise) so the 3 PVCs co-locate; the build pod is the
  first consumer of all three.
- **Registry allowlist** enforced at **reconcile time** (operator config; no
  webhook). Empty allowlist **fails closed**.
- **`du` measurement Job** mounts only the target PVC, **read-only**, pinned to
  the output PV's node; returns a byte count via its termination message.
- **No node-pinned cleanup finalizer.** Deletion: OwnerReferences cascade child
  resources; on-disk data relies on local-path Delete-reclaim (best-effort). A
  node-targeted cleanup finalizer is forbidden — it would wedge `delete` forever
  on a dead node. The only finalizer is a **bounded, best-effort build-Job abort**.

## Status & console

- **`status` is operator-owned** (subresource); nothing else writes it. `phase`
  is derived from conditions, never set independently. `specStale` is
  detect-only — it never triggers a build and is excluded from the ArgoCD health
  verdict.
- **Console** is reachable **only via oauth2-proxy** (Traefik forward-auth);
  GitHub team check **fails closed**. The console is **read-only except** the
  rebuild-annotation patch and has **no Job/Pod-create RBAC**. **kubectl is the
  break-glass** if oauth2-proxy locks the team out (no basic-auth stopgap).
