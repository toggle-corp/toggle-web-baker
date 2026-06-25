#!/bin/bash
# Thin wrapper around fugit's helm-update-snapshots.sh. Renders `helm template`
# for each scenario in tests.yaml (+ its tests/<key> overlay) and writes/diffs
# the golden snapshot in snapshots/.
#
#   ./update-snapshots.sh                  # refresh snapshots
#   ./update-snapshots.sh --check-diff-only # CI: fail on drift

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
export SCRIPT_DIR

# shellcheck disable=SC2068
"$SCRIPT_DIR/../../../fugit/scripts/helm-update-snapshots.sh" "$@"
