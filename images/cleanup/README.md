# cleanup

Platform-owned PVC space reclaimer. The operator runs it to prune a cache
volume over its cleanup threshold (`MODE=cache`) or to prune old served
releases on the output PVC (`MODE=releases`).

## Contract

| | |
|---|---|
| **Runs as** | standalone pod |
| **Base** | `node` (digest-pinned) + corepack (pnpm/yarn shims) |
| **Mounts** | `MODE=cache`: cache PVC **RW** at `/cache`. `MODE=releases`: output PVC **RW** containing `RELEASES_DIR`. |
| **k8s API** | none (`automountServiceAccountToken:false`) |

## Env

| Var | Mode | Required | Meaning |
|---|---|---|---|
| `MODE` | both | no | `cache` (default) or `releases`. |
| `PACKAGE_MANAGER` | cache | yes | `pnpm` or `yarn`. |
| `CLEANUP_THRESHOLD_BYTES` | cache | yes | Only clean if `du -sb /cache` exceeds this. Manual triggers pass `0` to force a prune. |
| `CACHE` | cache | no | Mount path (default `/cache`). |
| `RELEASES_DIR` | releases | yes | Dir holding release subdirs (copier layout: `<output>/releases/<TS>`). |
| `KEEP_RELEASES` | releases | yes | Integer count of newest releases to retain. |
| `PROTECTED_RELEASES` | releases | no | Comma-separated release dir names NEVER deleted (e.g. current + previous). |

## Behavior — `MODE=cache`

1. Measure `du -sb /cache`. If `<= CLEANUP_THRESHOLD_BYTES`, **skip** (no-op).
2. Branch on `PACKAGE_MANAGER`:
   - **pnpm** → `pnpm store prune` against the on-volume store
     (`$CACHE/pnpm-store`). Reference-safe: removes only unreferenced,
     content-addressed entries.
   - **yarn** → `yarn cache clean`, then trim the on-volume cache contents
     (loose and fully regenerable). The `/cache` dir itself is preserved so the
     mount/ownership stays intact.

## Behavior — `MODE=releases`

1. List release dirs under `RELEASES_DIR`, newest-first (by dir name, which is
   the copier's sortable UTC timestamp `%Y%m%dT%H%M%SZ-<pid>`, so lexical sort
   == chronological).
2. Keep-set = newest `KEEP_RELEASES` non-protected dirs ∪ `PROTECTED_RELEASES`.
   Protected dirs are kept even when old.
3. `rm -rf` the rest. Missing/empty `RELEASES_DIR` → no-op (`deleted:0`).

## Termination message

`MODE=cache` skip:

```json
{"action":"skip","reason":"under-threshold","before":123,"threshold":456}
```

`MODE=cache` cleaned:

```json
{"action":"pnpm store prune","before":900000,"after":300000,"reclaimed":600000,"threshold":500000}
```

`MODE=releases`:

```json
{"action":"release-prune","kept":3,"deleted":2,"before":900000,"after":540000,"reclaimed":360000}
```
