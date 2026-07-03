#!/usr/bin/env bash
# clock-entrypoint.sh -- trigger rebuilds: on schedule (tick) or on new commits (watch).
#
# MODE=tick (default): sets the rebuild "requested-at" annotation to the current
# epoch seconds AND clears any stale manual "by" / commit-watch "commit"
# annotations in a SINGLE kubectl call, so a scheduled tick can't be mislabeled
# Manual or Commit by leftovers from an earlier trigger (the operator classifies
# the trigger by which of those keys is present).
#
# MODE=watch: polls the remote with `git ls-remote REPO REF` and compares the
# SHA against the last-seen annotation. First run seeds last-seen WITHOUT
# triggering (the operator bootstraps the first build itself). A changed SHA
# sets requested-at + commit + last-seen and clears "by" in ONE kubectl call.
# An unchanged SHA is a no-op. ls-remote failure exits nonzero without patching
# anything (the Job's backoffLimit retries).
#
# Parameters come from the environment the operator stamps on the container. The
# annotation KEYS stay owned by the operator (api/v1alpha1) and are passed in,
# not hardcoded here, so the two never drift.
#
#   APP                       FrontendApp name to annotate
#   REQUESTED_AT_ANNOTATION   the rebuild "requested-at" annotation key
#   BY_ANNOTATION             the rebuild "by" annotation key (cleared on trigger)
#   COMMIT_ANNOTATION         the rebuild "commit" annotation key
#   MODE                      "tick" (default) or "watch"
#   REPO                      (watch) git URL, anonymous access only
#   REF                       (watch) ref to watch; empty means HEAD
#   LAST_SEEN_ANNOTATION      (watch) the last-seen-sha annotation key
#
# HOME points at a writable emptyDir (/tmp) so kubectl's discovery cache has
# somewhere to live under the pod's readOnlyRootFilesystem.
set -euo pipefail

: "${APP:?APP (FrontendApp name) is required}"
: "${REQUESTED_AT_ANNOTATION:?REQUESTED_AT_ANNOTATION is required}"
: "${BY_ANNOTATION:?BY_ANNOTATION is required}"
: "${COMMIT_ANNOTATION:?COMMIT_ANNOTATION is required}"

if [ "${MODE:-tick}" = "tick" ]; then
	exec kubectl annotate frontendapp "${APP}" \
		"${REQUESTED_AT_ANNOTATION}=$(date +%s)" \
		"${BY_ANNOTATION}-" \
		"${COMMIT_ANNOTATION}-" \
		--overwrite
fi

# ---- MODE=watch ---------------------------------------------------------------
: "${REPO:?REPO (git URL) is required in watch mode}"
: "${LAST_SEEN_ANNOTATION:?LAST_SEEN_ANNOTATION is required in watch mode}"

# Anonymous only: never prompt for credentials — fail fast on private repos.
export GIT_TERMINAL_PROMPT=0

# First matching line's first column is the SHA. ls-remote failure (network,
# bad repo) aborts via set -o pipefail WITHOUT patching anything.
sha="$(git ls-remote "${REPO}" "${REF:-HEAD}" | head -n1 | cut -f1)"
if [ -z "${sha}" ]; then
	echo "watch: ls-remote returned no SHA for ${REPO} ${REF:-HEAD}" >&2
	exit 1
fi

last_seen="$(kubectl get frontendapp "${APP}" \
	-o jsonpath="{.metadata.annotations.${LAST_SEEN_ANNOTATION//./\\.}}")"

if [ -z "${last_seen}" ]; then
	# First tick: seed only. The operator's AwaitingFirstBuild bootstrap owns the
	# first build; triggering here would double-build a freshly created app.
	exec kubectl annotate frontendapp "${APP}" \
		"${LAST_SEEN_ANNOTATION}=${sha}" \
		--overwrite
fi

if [ "${sha}" = "${last_seen}" ]; then
	exit 0
fi

# New commit: ONE atomic call — trigger + classification + state, clearing any
# stale manual "by" so the operator can't mislabel this build Manual.
exec kubectl annotate frontendapp "${APP}" \
	"${REQUESTED_AT_ANNOTATION}=$(date +%s)" \
	"${COMMIT_ANNOTATION}=${sha}" \
	"${LAST_SEEN_ANNOTATION}=${sha}" \
	"${BY_ANNOTATION}-" \
	--overwrite
