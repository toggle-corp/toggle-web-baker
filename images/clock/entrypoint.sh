#!/usr/bin/env bash
# clock-entrypoint.sh -- tick the rebuild annotation on schedule.
#
# Sets the rebuild "requested-at" annotation to the current epoch seconds AND
# clears any stale manual "by" annotation in a SINGLE kubectl call, so a
# scheduled tick can't be mislabeled Manual by a leftover "by" from an earlier
# manual rebuild (the operator classifies the trigger by the "by" presence).
#
# Parameters come from the environment the operator stamps on the container. The
# annotation KEYS stay owned by the operator (api/v1alpha1) and are passed in,
# not hardcoded here, so the two never drift.
#
#   APP                       FrontendApp name to annotate
#   REQUESTED_AT_ANNOTATION   the rebuild "requested-at" annotation key
#   BY_ANNOTATION             the rebuild "by" annotation key (cleared each tick)
#
# HOME points at a writable emptyDir (/tmp) so kubectl's discovery cache has
# somewhere to live under the pod's readOnlyRootFilesystem.
set -euo pipefail

: "${APP:?APP (FrontendApp name) is required}"
: "${REQUESTED_AT_ANNOTATION:?REQUESTED_AT_ANNOTATION is required}"
: "${BY_ANNOTATION:?BY_ANNOTATION is required}"

exec kubectl annotate frontendapp "${APP}" \
	"${REQUESTED_AT_ANNOTATION}=$(date +%s)" \
	"${BY_ANNOTATION}-" \
	--overwrite
