# copier

**The critical image.** Platform-owned. Runs as the **MAIN** container after
all initContainers (clone -> setup -> fetch -> build) succeed. It is the **only**
writer to the output PVC mounted at `/output`.

## Contract

| | |
|---|---|
| **Runs as** | main container (root, to chown assembled files) |
| **Base** | `alpine` pinned by digest + rsync, coreutils, findutils, bash |
| **Reads** | build output at `/workspace/$OUTPUT_DIR` (work volume) |
| **Reads** | `/workspace/phase-env` (optional convention file) |
| **Writes** | `/output` only (`releases/<ts>/`, `current` symlink) |
| **k8s API** | none (`automountServiceAccountToken:false`) |

## Env

| Var | Default | Meaning |
|---|---|---|
| `OUTPUT_DIR` | *(required)* | Subdir of `/workspace` holding the build output. |
| `RELEASE_SIZE_CAP` | `0` | Hard byte cap per release (`0` = no cap). |
| `FREE_HEADROOM_BYTES` | `0` | Bytes that must stay free after the copy. |
| `KEEP_RELEASES` | `0` | Non-current releases to retain. |
| `PLATFORM_OWNER` | `65532:65532` | uid:gid the assembled tree is chowned to. |

## Gate ordering (load-bearing)

1. **Retention sweep** — delete release dirs in `/output/releases/` keeping the
   one `current` points at **plus** the newest `$KEEP_RELEASES` others.
   Runs **before** measuring/copying so reclaimed space counts toward the
   free-space gate and the new release dir does not yet exist (race-free).
2. **Pre-copy size gate** — `du -sb` the **source** at `/workspace/$OUTPUT_DIR`
   (on the work volume) *before any write to `/output`*. Reject if
   `source_bytes > RELEASE_SIZE_CAP`.
3. **Free-space gate** — `df` `/output`; require
   `source_bytes + FREE_HEADROOM_BYTES <= free`.
4. **Assemble** — reject path-traversal / odd filenames, then
   `rsync -a --safe-links` the source into `/output/releases/<ts>/` (symlinks
   pointing outside the tree are stripped), and `chown` the tree to
   `PLATFORM_OWNER` so nginx `disable_symlinks if_not_owner` follows only
   platform-owned files.
5. **Post-assemble flip gate** — re-`du` the assembled release and re-check
   `<= RELEASE_SIZE_CAP` (defense in depth; the source could lie via symlinks).
6. **Atomic flip** —
   `ln -sfn releases/<ts> /output/current.tmp && mv -T /output/current.tmp /output/current`.
   The rename is atomic, so there is no half-written `current` window.
7. **Termination message** — emit the status JSON (below).

Any gate failure exits non-zero and writes a JSON `{"error":...}` line to the
termination message; nothing is flipped.

## Termination message format

Success (`< 4KB`):

```json
{
  "releaseTs": "20260625T101500Z-42",
  "dataFreshness": "<value of DATA_LAST_MODIFIED from /workspace/phase-env, or empty>",
  "outputSize": 12345678,
  "deltas": {
    "prevFileCount": 120,
    "fileCount": 131,
    "filesAdded": 11,
    "filesRemoved": 0
  }
}
```

Failure:

```json
{"error": "size cap exceeded: 9000 > 8000", "releaseTs": "20260625T101500Z-42"}
```

## Testing

Gate logic lives in `lib.sh` as sourceable functions and is unit-tested in
`../test/copier_test.sh` (size-cap reject, free-space reject, retention sweep,
atomic flip, unsafe-name rejection) — no container runtime needed.
