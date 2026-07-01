# clock

Minimal, platform-owned scheduled-tick image.

## Contract

| | |
|---|---|
| **Runs as** | CronJob pod (`<app>-clock`), `USER 65532` |
| **Base** | `alpine` pinned by digest + `bash` + `kubectl` |
| **Mounts** | its ServiceAccount token (RBAC: patch its own `FrontendApp`); writable `/tmp` emptyDir for kubectl's cache |
| **Writes** | nothing on the root filesystem (readOnlyRootFilesystem) |
| **k8s API** | one `kubectl annotate` on the app's `FrontendApp` |

## Env

| Var | Meaning |
|---|---|
| `APP` | FrontendApp name to annotate (required). |
| `REQUESTED_AT_ANNOTATION` | rebuild "requested-at" annotation key (required). |
| `BY_ANNOTATION` | rebuild "by" annotation key, cleared each tick (required). |
| `HOME` | points at the writable `/tmp` emptyDir so kubectl's discovery cache works under readOnlyRootFilesystem. |

## Behavior

Each tick sets `REQUESTED_AT_ANNOTATION` to the current epoch seconds and clears
`BY_ANNOTATION` in a single `kubectl annotate --overwrite` call, so a scheduled
tick can't be mislabeled Manual by a leftover `by`. Replaces the distroless
`registry.k8s.io/kubectl` image, which has no shell and so could never run the
tick command.
