# cleanup

Platform-owned cache-PVC pruner. The operator runs it when a cache volume
exceeds its cleanup threshold.

## Contract

| | |
|---|---|
| **Runs as** | standalone pod |
| **Base** | `node` (digest-pinned) + corepack (pnpm/yarn shims) |
| **Mounts** | cache PVC **read-write** at `/cache` |
| **k8s API** | none (`automountServiceAccountToken:false`) |

## Env

| Var | Required | Meaning |
|---|---|---|
| `PACKAGE_MANAGER` | yes | `pnpm` or `yarn`. |
| `CLEANUP_THRESHOLD_BYTES` | yes | Only clean if `du -sb /cache` exceeds this. |
| `CACHE` | no | Mount path (default `/cache`). |

## Behavior

1. Measure `du -sb /cache`. If `<= CLEANUP_THRESHOLD_BYTES`, **skip** (no-op).
2. Branch on `PACKAGE_MANAGER`:
   - **pnpm** → `pnpm store prune` against the on-volume store
     (`$CACHE/pnpm-store`). Reference-safe: removes only unreferenced,
     content-addressed entries.
   - **yarn** → `yarn cache clean`, then trim the on-volume cache contents
     (loose and fully regenerable). The `/cache` dir itself is preserved so the
     mount/ownership stays intact.

## Termination message

Skip:

```json
{"action":"skip","reason":"under-threshold","before":123,"threshold":456}
```

Cleaned:

```json
{"action":"pnpm store prune","before":900000,"after":300000,"reclaimed":600000,"threshold":500000}
```
