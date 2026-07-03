# clock

Minimal, platform-owned trigger image: scheduled tick (`MODE=tick`, default) or
commit watch (`MODE=watch`).

## Contract

| | |
|---|---|
| **Runs as** | CronJob pod (`<app>-clock` / `<app>-watch`), `USER 65532` |
| **Base** | `alpine` pinned by digest + `bash` + `git` + `kubectl` |
| **Mounts** | its ServiceAccount token (RBAC: patch its own `FrontendApp`); writable `/tmp` emptyDir for kubectl's cache |
| **Writes** | nothing on the root filesystem (readOnlyRootFilesystem) |
| **k8s API** | at most one `kubectl get` + one `kubectl annotate` on the app's `FrontendApp` |

## Env

| Var | Mode | Meaning |
|---|---|---|
| `APP` | both | FrontendApp name to annotate (required). |
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

## Credentials (future private repos)

Watch mode is anonymous-only today, mirroring the clone image: nothing supports
private repos yet. `GIT_TERMINAL_PROMPT=0` makes a private repo fail fast
instead of hanging on a prompt. When private-repo support lands, the same
read-only credential mount convention as `images/clone` (`GIT_ASKPASS` +
`GIT_CREDENTIAL_DIR/{username,password}`) must be wired here too — one feature,
two mount points (clone pod AND this watcher).
