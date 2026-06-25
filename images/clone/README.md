# clone

Platform-owned, digest-pinned source-checkout image. **Phase 1** of the build
pipeline (the first initContainer).

## Contract

| | |
|---|---|
| **Runs as** | initContainer (first) |
| **Base** | `alpine/git` pinned by digest |
| **Writes** | `/workspace/src` on the shared work volume |
| **k8s API** | none (`automountServiceAccountToken:false`) |
| **Failure** | exit non-zero + short reason in `/dev/termination-log` |

## Env

| Var | Required | Meaning |
|---|---|---|
| `REPO` | yes | Clone URL (expected public). |
| `REF` | yes | Branch, tag, or full commit sha to check out. |
| `DEPTH` | no | Positive int; shallow clone to that depth (incl. submodules). |
| `SRC_DIR` | no | Override target (default `/workspace/src`). |
| `GIT_CREDENTIAL_DIR` | no | Where the askpass helper reads optional creds (default `/run/git-credential`). |

## Credentials (future private repos)

Anonymous by default. For a future private repo the operator may mount a
read-only credential at `GIT_CREDENTIAL_DIR/{username,password}`. The
`GIT_ASKPASS` helper reads it **only** to answer git's prompt and **never**
writes it to `/workspace`, so no `.git-credentials` is left where later phases
(setup/fetch/build/copy) could read it. `GIT_TERMINAL_PROMPT=0` and a cleared
`credential.helper` prevent any on-disk persistence or interactive prompt.

## Termination message

On success: nothing required (the resolved sha is logged to stderr). On failure:
a single `clone: <reason>` line (<4KB).
