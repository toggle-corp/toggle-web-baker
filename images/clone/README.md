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
| `SUBMODULES` | no | `1`/`true` fetches top-level submodules only (one level, like `actions/checkout submodules:true`; not recursive). Default off. |
| `SRC_DIR` | no | Override target (default `/workspace/src`). |
| `GIT_CREDENTIAL_DIR` | no | Where the askpass helper reads optional creds (default `/run/git-credential`). |
| `GIT_CREDENTIAL_HOST` | no | Lowercase hostname the credential is scoped to; the helper answers only prompts for this host (others fetch anonymously). Unset answers any host. |

## Credentials

Anonymous by default. When the operator mounts a read-only credential at
`GIT_CREDENTIAL_DIR/{username,password}` (default `/run/git-credential`), the
`GIT_ASKPASS` helper (`git-askpass.sh`) reads it **only** to answer git's https
auth prompt and **never** writes it to `/workspace`, so no `.git-credentials` is
left where later phases (setup/fetch/build/copy) could read it. The credential
value is never echoed and the scripts never run `set -x`. `GIT_TERMINAL_PROMPT=0`
and a cleared `credential.helper` prevent any on-disk persistence or interactive
prompt. With no credential mounted the helper prints nothing and the clone stays
anonymous. This is the same mount convention as the clock watcher — one feature,
two mount points (this clone pod AND the commit watcher).

The credential is **host-scoped** via `GIT_CREDENTIAL_HOST` (a lowercase
hostname the operator injects at mount time). The helper answers only prompts
whose URL is for exactly that host; for any other host it prints nothing and the
fetch proceeds anonymously. This closes a `.gitmodules` leak: a submodule
declared on another host (e.g. `https://evil.example/x.git`, whose content the
repo committers — not the platform — control) fetches anonymously and never
receives the operator-global credential. Match is on hostname only (port
ignored). Unset/empty falls back to answering any prompt (manual/back-compat).

Because the only credential form is an https basic-auth token (no SSH key
support), the ssh→https GitHub URL rewrite is **unconditional**: an SSH GitHub
URL (`git@github.com:…` or `ssh://git@github.com/…`) is always rewritten to
`https://github.com/…`, credential mounted or not. Authenticated, the token
flows over https via askpass; anonymous, a public repo/submodule declared over
SSH still clones over https as before.

## Termination message

On success: nothing required (the resolved sha is logged to stderr). On failure:
a single `clone: <reason>` line (<4KB).
