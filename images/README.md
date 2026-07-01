# Platform images

Trusted, platform-owned container images the **FrontendApp** operator runs to
build and publish a frontend release. The operator never runs arbitrary user
images for these phases; it runs *these*, pinned by digest, so the behavior is
auditable.

## The pipeline (one pod)

A single Kubernetes pod runs the build:

```
initContainers:  clone  ->  setup  ->  fetch  ->  build
mainContainer:   copier
```

- A **work volume** (emptyDir, or a cache PVC) is shared across every phase at
  `/workspace`.
- The **copier** is the **only** writer to the **output PVC** at `/output`.
- Pods set `automountServiceAccountToken:false` — **no k8s API access**. Every
  image returns data to the operator via the **container termination-message
  file** (`/dev/termination-log`), never the API.

The `du` and `cleanup` images run as their own standalone pods (storage probe /
cache prune), not as part of the build pod.

## Images

| Image | Role | Mounts | Writes |
|---|---|---|---|
| [`clone`](clone/) | Phase 1 initContainer: `git clone --recurse-submodules` | work vol | `/workspace/src` |
| [`copier`](copier/) | Main container: gate + assemble + atomic flip | work vol (ro logic), `/output` | `/output` only |
| [`du`](du/) | Standalone: measure one PVC | target PVC (ro) at `/target` | nothing |
| [`cleanup`](cleanup/) | Standalone: prune cache PVC | cache PVC (rw) at `/cache` | `/cache` |

Each image's own `README.md` has its full contract. Base-image digests are
pinned in every `Dockerfile` and mirrored in the `Makefile` for review.

## Termination-message formats

All four write to `/dev/termination-log`, kept `< 4KB`.

| Image | Success | Failure |
|---|---|---|
| `clone` | (nothing required) | `clone: <reason>` |
| `copier` | status JSON (below) | `{"error":"...","releaseTs":"..."}` |
| `du` | a single integer (bytes) | `du: <reason>` |
| `cleanup` | action JSON (below) | `cleanup: <reason>` |

copier success:

```json
{"releaseTs":"20260625T101500Z-42","outputSize":12345678,"deltas":{"prevFileCount":120,"fileCount":131,"filesAdded":11,"filesRemoved":0}}
```

cleanup success:

```json
{"action":"pnpm store prune","before":900000,"after":300000,"reclaimed":600000,"threshold":500000}
```

## Copier gate ordering (load-bearing)

1. **Retention sweep** (before measuring, race-free) — keep `current` + newest
   `$KEEP_RELEASES`, delete the rest.
2. **Pre-copy size gate** — `du -sb` the **source** on the work volume; reject
   if `> RELEASE_SIZE_CAP` *before writing anything to `/output`*.
3. **Free-space gate** — `df /output`; require `source + FREE_HEADROOM_BYTES <= free`.
4. **Assemble** — reject traversal/odd names, `rsync -a --safe-links` (strip
   outside-pointing symlinks), `chown` to the platform user.
5. **Post-assemble flip gate** — re-`du` the release, re-check the cap.
6. **Atomic flip** — `ln -sfn` a temp link then `mv -T` it over `current`.
7. **Termination JSON.**

## Phase-env convention

`/workspace/phase-env` is a plain `KEY=VALUE` file (one per line) any phase may
write. It is **convention, not contract**.

## Env reference

| Image | Env |
|---|---|
| clone | `REPO` (req), `REF` (req), `DEPTH`, `GIT_CREDENTIAL_DIR` |
| copier | `OUTPUT_DIR` (req), `RELEASE_SIZE_CAP`, `FREE_HEADROOM_BYTES`, `KEEP_RELEASES`, `PLATFORM_OWNER` |
| du | `TARGET` |
| cleanup | `PACKAGE_MANAGER` (req), `CLEANUP_THRESHOLD_BYTES` (req) |

## Build & test

```sh
make build        # docker build all four
make clone        # one image
make shellcheck   # lint all shell scripts (shellcheck 0.11+)
make test         # copier gate unit tests (no container runtime needed)
make digests      # print pinned base digests
```

`make test` sources `copier/lib.sh` directly, so the gate logic (size cap,
free-space, retention sweep, atomic flip, unsafe-name rejection, symlink
stripping) is verified without Docker.
