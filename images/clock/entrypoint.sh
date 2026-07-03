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
# annotation KEYS and the kubectl RESOURCE target stay owned by the operator
# (api/v1alpha1) and are passed in, not hardcoded here, so the two never drift.
#
#   APP                       App name to annotate
#   RESOURCE                  group-qualified resource to address (apps.baker....)
#   REQUESTED_AT_ANNOTATION   the rebuild "requested-at" annotation key
#   BY_ANNOTATION             the rebuild "by" annotation key (cleared on trigger)
#   COMMIT_ANNOTATION         the rebuild "commit" annotation key
#   MODE                      "tick" (default) or "watch"
#   REPO                      (watch) git URL; anonymous, or authenticated via an
#                              operator-mounted GIT_CREDENTIAL_DIR credential
#   REF                       (watch) ref to watch; empty means HEAD
#   LAST_SEEN_ANNOTATION      (watch) the last-seen-sha annotation key
#
# HOME points at a writable emptyDir (/tmp) so kubectl's discovery cache has
# somewhere to live under the pod's readOnlyRootFilesystem.
set -euo pipefail

: "${APP:?APP (App name) is required}"
: "${RESOURCE:?RESOURCE (group-qualified resource name) is required}"
: "${REQUESTED_AT_ANNOTATION:?REQUESTED_AT_ANNOTATION is required}"
: "${BY_ANNOTATION:?BY_ANNOTATION is required}"
: "${COMMIT_ANNOTATION:?COMMIT_ANNOTATION is required}"

if [ "${MODE:-tick}" = "tick" ]; then
	exec kubectl annotate "${RESOURCE}" "${APP}" \
		"${REQUESTED_AT_ANNOTATION}=$(date +%s)" \
		"${BY_ANNOTATION}-" \
		"${COMMIT_ANNOTATION}-" \
		--overwrite
fi

# ---- MODE=watch ---------------------------------------------------------------
: "${REPO:?REPO (git URL) is required in watch mode}"
: "${LAST_SEEN_ANNOTATION:?LAST_SEEN_ANNOTATION is required in watch mode}"

# Never prompt on an interactive terminal — fail fast instead of hanging.
export GIT_TERMINAL_PROMPT=0
# Answer any https auth prompt from an OPTIONAL operator-mounted credential at
# GIT_CREDENTIAL_DIR/{username,password} via the askpass helper. With no
# credential mounted the helper prints nothing, so ls-remote of a public repo
# stays anonymous. The credential lifts GitHub's per-IP anonymous rate limit.
# One feature, two mount points (this watcher AND the clone pod).
export GIT_ASKPASS=/usr/local/bin/git-askpass.sh

# Unconditional ssh->https GitHub URL rewrite. This pod has no SSH key, and the
# only credential we support is an https basic-auth token answered via
# GIT_ASKPASS — a token CANNOT authenticate an ssh remote. So an SSH GitHub URL
# (scp-style git@github.com: or ssh://git@github.com/) is ALWAYS rewritten to
# https, whether or not a credential is mounted. Inject via GIT_CONFIG_* env so
# it needs no writable HOME/.gitconfig under readOnlyRootFilesystem.
export GIT_CONFIG_COUNT=2
export GIT_CONFIG_KEY_0='url.https://github.com/.insteadOf'
export GIT_CONFIG_VALUE_0='git@github.com:'
export GIT_CONFIG_KEY_1='url.https://github.com/.insteadOf'
export GIT_CONFIG_VALUE_1='ssh://git@github.com/'

# ls-remote patterns TAIL-match path components: asking for "main" also returns
# refs/heads/feature/main and refs/tags/main — and sorted output can put those
# BEFORE refs/heads/main. Select the exact ref by priority: the ref verbatim
# (HEAD or fully-qualified), then the branch, then the tag. ls-remote failure
# (network, bad repo) aborts via set -e WITHOUT patching anything.
ref="${REF:-HEAD}"
ls_out="$(git ls-remote "${REPO}" "${ref}")"
sha="$(printf '%s\n' "${ls_out}" | awk -v r="${ref}" '
	$2 == r { exact = $1 }
	$2 == "refs/heads/" r { head = $1 }
	$2 == "refs/tags/" r { tag = $1 }
	END {
		if (exact != "") print exact
		else if (head != "") print head
		else if (tag != "") print tag
	}')"
if [ -z "${sha}" ]; then
	echo "watch: ls-remote returned no SHA for ${REPO} ${ref}" >&2
	exit 1
fi

last_seen="$(kubectl get "${RESOURCE}" "${APP}" \
	-o jsonpath="{.metadata.annotations.${LAST_SEEN_ANNOTATION//./\\.}}")"

if [ -z "${last_seen}" ]; then
	# First tick: seed only. The operator's AwaitingFirstBuild bootstrap owns the
	# first build; triggering here would double-build a freshly created app.
	exec kubectl annotate "${RESOURCE}" "${APP}" \
		"${LAST_SEEN_ANNOTATION}=${sha}" \
		--overwrite
fi

if [ "${sha}" = "${last_seen}" ]; then
	exit 0
fi

# New commit: ONE atomic call — trigger + classification + state, clearing any
# stale manual "by" so the operator can't mislabel this build Manual.
exec kubectl annotate "${RESOURCE}" "${APP}" \
	"${REQUESTED_AT_ANNOTATION}=$(date +%s)" \
	"${COMMIT_ANNOTATION}=${sha}" \
	"${LAST_SEEN_ANNOTATION}=${sha}" \
	"${BY_ANNOTATION}-" \
	--overwrite
