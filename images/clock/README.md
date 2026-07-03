# clock

Minimal, platform-owned trigger image: scheduled tick (`MODE=tick`, default) or
commit watch (`MODE=watch`).

## Contract

| | |
|---|---|
| **Runs as** | CronJob pod (`<app>-clock` / `<app>-watch`), `USER 65532` |
| **Base** | `alpine` pinned by digest + `bash` + `git` + `kubectl` |
| **Mounts** | its ServiceAccount token (RBAC: patch its own `App`); writable `/tmp` emptyDir for kubectl's cache |
| **Writes** | nothing on the root filesystem (readOnlyRootFilesystem) |
| **k8s API** | at most one `kubectl get` + one `kubectl annotate` on the app's `App` |

## Env

| Var | Mode | Meaning |
|---|---|---|
| `APP` | both | App name to annotate (required). |
| `REQUESTED_AT_ANNOTATION` | both | rebuild "requested-at" annotation key (required). |
| `BY_ANNOTATION` | both | rebuild "by" annotation key, cleared on every trigger (required). |
| `COMMIT_ANNOTATION` | both | rebuild "commit" annotation key (required): cleared by tick, set by watch. |
| `MODE` | both | `tick` (default) or `watch`. |
| `REPO` | watch | git URL handed verbatim to `git ls-remote` (required in watch mode). |
| `REF` | watch | ref to watch; empty means `HEAD`. |
| `LAST_SEEN_ANNOTATION` | watch | last-seen-sha annotation key (required in watch mode). |
| `HOME` | both | points at the writable `/tmp` emptyDir so kubectl's discovery cache works under readOnlyRootFilesystem. |

## Behavior

**tick**: sets `REQUESTED_AT_ANNOTATION` to the current epoch seconds and clears
`BY_ANNOTATION` + `COMMIT_ANNOTATION` in a single `kubectl annotate --overwrite`
call, so a scheduled tick can't be mislabeled Manual or Commit by leftovers from
an earlier trigger. Replaces the distroless `registry.k8s.io/kubectl` image,
which has no shell and so could never run the tick command.

**watch**: `git ls-remote REPO REF` resolves the remote SHA, compared against
`LAST_SEEN_ANNOTATION` on the app:

- no last-seen yet (first run): seed last-seen only — NO trigger; the operator's
  AwaitingFirstBuild bootstrap owns the first build.
- unchanged SHA: no-op, exit 0.
- new SHA: ONE atomic `kubectl annotate --overwrite` setting requested-at +
  commit + last-seen and clearing `by`.
- ls-remote failure: log to stderr, exit nonzero, patch NOTHING (the Job's
  backoffLimit retries).

## Credentials

Watch mode is anonymous by default. When the operator mounts a read-only
credential at `GIT_CREDENTIAL_DIR/{username,password}` (default
`/run/git-credential`), the `GIT_ASKPASS` helper (`git-askpass.sh`) answers
GitHub's https auth prompt for the `git ls-remote` poll — lifting GitHub's
per-IP anonymous rate limit. The helper reads the credential **only** to answer
the prompt, never persists it, never echoes it, and the scripts never run
`set -x`. With no credential mounted the helper prints nothing and the poll stays
anonymous. `GIT_TERMINAL_PROMPT=0` makes an unauthenticated private repo fail
fast instead of hanging on a prompt. This is the same mount convention as the
clone image (`images/clone`) — one feature, two mount points (the clone pod AND
this watcher).

The credential is **host-scoped** via `GIT_CREDENTIAL_HOST` (a lowercase
hostname the operator injects at mount time). The helper answers only prompts
whose URL is for exactly that host; for any other host it prints nothing and the
poll proceeds anonymously — the operator-global credential never leaves toward
an unexpected host. Match is on hostname only (port ignored). Unset/empty falls
back to answering any prompt (manual/back-compat).

Because the only credential form is an https basic-auth token (no SSH key
support), the ssh→https GitHub URL rewrite is **unconditional**: an SSH GitHub
`REPO` (`git@github.com:…` or `ssh://git@github.com/…`) is always rewritten to
`https://github.com/…`, credential mounted or not.
