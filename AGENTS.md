# AGENTS.md

Guidance for agents (Claude included) working in this repo. Keep changes
focused and validated by the matching test layer below.

Machine-local agent context (paths, local tooling, and other things that don't
belong in the committed repo) lives in `AGENTS.local.md` — gitignored, so it is
absent in fresh clones. Read it if present.

## Layout (where things live)

- `api/v1alpha1/` — the `FrontendApp` CRD types + CEL validation markers.
- `internal/controller/` — the operator reconciler and the build-pod spec
  (`buildpod.go` is the single source of truth for the build Job).
- `internal/domain/` — pure decision logic (build scheduling, registry
  allowlist, storage thresholds).
- `images/` — the platform helper images (clone / copier / du / cleanup).
- `deploy/helm/toggle-web-baker/` — the install chart.
- `config/samples/frontendapp.yaml` — the e2e smoke sample.

## Test layers (run the one that matches your change)

- `just test` — fast unit tests (operator logic) + console module. Run on any
  Go change.
- `make -C images test` — clone/copier entrypoint shell tests. Run on any
  change under `images/`.
- `just test-envtest` — apiserver-backed CRD validation tests (downloads
  envtest assets via setup-envtest; needs network on first run). Run on any
  change to `api/v1alpha1/` CEL rules or the CRD.
- `just helm-snapshots --check-diff-only` — verify the chart still renders to the
  committed snapshots. Run on any change to `api/v1alpha1/`, the CRD, or anything
  under `deploy/helm/`. See the snapshot note below.
- `just e2e-local` — full kind pipeline smoke (MANUAL, Docker required). See
  below.

## ALWAYS run `just lint` before pushing

`just lint` runs `pre-commit run --all-files`: golangci-lint on BOTH Go modules
(operator root AND `console/` — a separate module), shellcheck on every tracked
shell script, and file-hygiene checks (whitespace/EOF fixers, YAML syntax,
merge markers). CI's `pre-commit` job runs the exact same thing, and tool
versions are pinned in ONE place: `.pre-commit-config.yaml`. A lint failure in
EITHER Go module is a CI failure.

pre-commit here is a manually-run check runner, NOT a git hook — do NOT run
`pre-commit install`. One-time setup: `pipx install pre-commit` (or pip). The
first run builds golangci-lint into pre-commit's cached env (needs network);
later runs are fast. The whitespace/EOF hooks FIX files in place — re-stage and
re-run until clean.

## ALWAYS check Helm snapshots before committing

Any change to `api/v1alpha1/` or the CRD flows into the chart (`just manifests`
re-syncs `templates/crd.yaml`), which changes the rendered Helm output. BEFORE
every commit run `just helm-snapshots --check-diff-only`; if it reports outdated
snapshots, run `just helm-snapshots` to regenerate and COMMIT the updated
`deploy/helm/toggle-web-baker/snapshots/*.yaml` alongside your change. A stale
snapshot is a CI failure, so never commit a CRD/chart change without it.

## After operator / API / image changes: ask the user to run `just e2e-local`

After ANY change to the operator (`internal/controller/`), the API types
(`api/v1alpha1/`), or the platform images (`images/`), ASK THE USER to run
`just e2e-local` to validate the full build pipeline on a local kind cluster.

Do NOT run `just e2e-local` autonomously, in CI, or in a sandbox: it needs
Docker, network access, and several minutes, and it builds five images and
spins up (then tears down) a real kind cluster. It is intentionally excluded
from CI and is the user's job to run.

The faster layers above (`just test`, `make -C images test`,
`just test-envtest`) are safe to run yourself and should pass before you hand
off.
