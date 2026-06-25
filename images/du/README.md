# du

Minimal, platform-owned size-probe image.

## Contract

| | |
|---|---|
| **Runs as** | standalone pod |
| **Base** | `alpine` pinned by digest + coreutils (GNU `du -sb`) |
| **Mounts** | one target PVC **read-only** at `/target` |
| **Writes** | nothing on disk |
| **k8s API** | none (`automountServiceAccountToken:false`) |

## Env

| Var | Default | Meaning |
|---|---|---|
| `TARGET` | `/target` | Mount path to measure. |

## Termination message

A single integer: the apparent size of `/target` in bytes (`du -sb`), e.g.

```
12345678
```

(<=4KB.) Exits non-zero with a `du: <reason>` line if `/target` is missing.
