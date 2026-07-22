#!/usr/bin/env bash
# clone-entrypoint.sh -- anonymous (or optionally credentialed) source checkout.
#
# Phase 1 of the build pipeline (first initContainer). Clones $REPO at $REF into
# /workspace/src, optionally fetching top-level submodules ($SUBMODULES; one
# level only, like actions/checkout submodules:true). Shallow if $DEPTH is set.
#
# Pods have automountServiceAccountToken:false, so we return nothing via the k8s
# API. Failures simply exit non-zero with a short reason written to the
# termination-message file so the operator can surface it.
#
# Env in:
#   REPO        (required)  -- clone URL, expected public.
#   REF         (required)  -- branch, tag, or full commit sha to check out.
#   DEPTH       (optional)  -- positive integer; if set, shallow clone to that depth.
#   SUBMODULES  (optional)  -- when 1/true, fetch top-level submodules (one level,
#                              NOT recursive — nested SSH/private submodules would
#                              abort the clone); default off.
#
# Security notes:
#   * GIT_ASKPASS points at a helper that reads an OPTIONAL credential mount and
#     never writes it to the shared work volume. We also disable the on-disk
#     credential store and prompting so git can never persist a token under
#     /workspace where later phases (setup/fetch/build/copy) could read it.
#   * No .git-credentials, no `git config --global credential.helper store`.
set -euo pipefail

TERM_LOG="${TERMINATION_LOG:-/dev/termination-log}"
SRC_DIR="${SRC_DIR:-/workspace/src}"

# fail "<reason>" -- write a short reason to the termination log and exit 1.
fail() {
	# Keep the message small (<4KB); the operator reads it verbatim.
	printf '%s\n' "clone: $1" | head -c 4000 >"$TERM_LOG" 2>/dev/null || true
	printf '%s\n' "clone: $1" >&2
	exit 1
}

# retry <op-desc> <cmd...> -- run a single network git op, retrying only
# TRANSIENT failures (DNS/network/timeout — the "Could not resolve host" blip
# on pod start that used to fail the whole build). Each op is individually
# idempotent, so we wrap each one separately rather than the whole sequence,
# which avoids re-running work that already succeeded / half-populating /work.
#
# Attempts + backoff are env-overridable so ops can tune without an image
# rebuild (defaults plumbed through the operator config → clone container env):
#   CLONE_RETRIES          total attempts, default 3.
#   CLONE_RETRY_BASE_DELAY seconds for the first backoff, default 2 (then
#                          exponential 2→4→8 + jitter; 0 disables the sleep,
#                          used by the shell tests to stay instant).
#
# Fail-fast on PERMANENT errors: we grep the captured stderr for auth-failed /
# ref-not-found / repo-not-found classes and stop retrying immediately, because
# a real permission error will never succeed on retry and burning ~15s of
# backoff just eats into the Job's activeDeadlineSeconds. On any other
# (assumed-transient) failure we back off and retry until attempts are
# exhausted. retry() RETURNS non-zero on permanent/exhausted failure rather than
# exiting, so callers decide whether the op is fatal (clone/submodule → `|| fail`)
# or best-effort (the ref fetch, whose miss the checkout fallback tolerates).
retry() {
	desc="$1"
	shift
	retries="${CLONE_RETRIES:-3}"
	base="${CLONE_RETRY_BASE_DELAY:-2}"
	attempt=1
	while :; do
		# Capture stderr so we can classify it; still surface it to our stderr.
		# Temporarily relax errexit so a non-zero op doesn't abort the script here.
		err_out=""
		set +e
		err_out="$("$@" 2>&1 1>&2)"
		op_rc=$?
		set -e
		if [ "$op_rc" -eq 0 ]; then
			return 0
		fi
		printf '%s\n' "$err_out" >&2
		# Permanent-error classes: never retry. These are matched on git's
		# SPECIFIC wording for auth/authz/missing-ref failures. We deliberately do
		# NOT match bare "not found" or "403 Forbidden": GitHub returns 403 for
		# SECONDARY RATE LIMITS (very likely during a scheduled-build burst) and
		# proxies/CDNs surface transient "404 Not Found" — both are retryable, and
		# classifying them permanent would fail-fast on exactly the transient class
		# this retry exists to survive. A genuine auth 403 still fails via the
		# specific "Authentication failed"/"could not read Username" strings below.
		case "$err_out" in
		*[Aa]uthentication\ failed* | *[Cc]ould\ not\ read\ [Uu]sername* | \
			*[Rr]epository\ not\ found* | \
			*[Cc]ouldn\'t\ find\ remote\ ref* | *[Rr]emote\ branch*not\ found* | \
			*[Aa]ccess\ denied* | *[Pp]ermission\ denied*)
			printf 'clone: %s failed (permanent, no retry)\n' "$desc" >&2
			return 1
			;;
		esac
		if [ "$attempt" -ge "$retries" ]; then
			printf 'clone: %s failed after %d attempt(s)\n' "$desc" "$attempt" >&2
			return 1
		fi
		# Exponential backoff (base * 2^(attempt-1)) + up to 1s jitter. A base of
		# 0 disables the sleep entirely (tests).
		if [ "$base" != 0 ]; then
			delay=$((base << (attempt - 1)))
			# Zero-pad the jitter to a fixed 3-digit fraction so the magnitude is a
			# true 0-999ms (RANDOM%1000 unpadded would read "5" as ".5"=500ms).
			jitter=$(printf '%03d' $((RANDOM % 1000)))
			printf 'clone: %s attempt %d/%d failed, retrying in ~%ss\n' "$desc" "$attempt" "$retries" "$delay" >&2
			sleep "$delay.$jitter" 2>/dev/null || sleep "$delay"
		fi
		attempt=$((attempt + 1))
	done
}

[ -n "${REPO:-}" ] || fail "REPO is required"
[ -n "${REF:-}" ] || fail "REF is required"

# Refuse to clone over an existing non-empty target so a re-run on a reused work
# volume can't silently merge two checkouts.
if [ -e "$SRC_DIR" ] && [ -n "$(ls -A "$SRC_DIR" 2>/dev/null || true)" ]; then
	fail "target $SRC_DIR already exists and is non-empty"
fi

# Never prompt; never fall back to an interactive terminal; never persist creds.
export GIT_TERMINAL_PROMPT=0
export GIT_ASKPASS=/usr/local/bin/git-askpass.sh
# Defensive: clear any inherited credential helper for this process tree.
git config --global --unset-all credential.helper 2>/dev/null || true
git config --global credential.helper "" 2>/dev/null || true

# Mark the checkout dir safe. $SRC_DIR is a root-owned emptyDir mount, but the
# build pod runs us as a non-root UID, so git's CVE-2022-24765 "dubious
# ownership" guard would abort every command after the initial clone (fetch,
# checkout, submodule) with a fatal we then surface only as a generic checkout
# failure. Inject via GIT_CONFIG_* env so it needs no writable HOME/.gitconfig
# under the pod's readOnlyRootFilesystem.
export GIT_CONFIG_COUNT=1
export GIT_CONFIG_KEY_0=safe.directory
export GIT_CONFIG_VALUE_0="$SRC_DIR"

# Unconditional ssh->https GitHub URL rewrite. This pod has no SSH key, and the
# only credential we support is an https basic-auth token answered via
# GIT_ASKPASS — a token CANNOT authenticate an ssh remote. So an SSH GitHub URL
# (scp-style git@github.com: or ssh://git@github.com/) is ALWAYS rewritten to
# https, whether or not a credential is mounted:
#   * credential mounted: the token then flows over https via askpass.
#   * no credential: a public repo (or public submodule) declared over SSH still
#     clones anonymously over https, exactly as before.
# Same GIT_CONFIG_* env mechanism as above; safe.directory stays KEY_0.
export GIT_CONFIG_COUNT=3
export GIT_CONFIG_KEY_1='url.https://github.com/.insteadOf'
export GIT_CONFIG_VALUE_1='git@github.com:'
export GIT_CONFIG_KEY_2='url.https://github.com/.insteadOf'
export GIT_CONFIG_VALUE_2='ssh://git@github.com/'

# SUBMODULES opt-in: only fetch submodules when explicitly enabled (1/true).
# Default off, so an app without submodules: true never fetches submodules.
#
# This fetches ONLY the top-level submodules (one level deep), mirroring
# actions/checkout's `submodules: true` (NOT `recursive`). We deliberately do
# NOT recurse: a top-level submodule may declare nested submodules over SSH
# (git@github.com:...) or private URLs the build pod can't reach, which would
# abort the whole clone. The init pass below uses `git submodule update --init`
# (no --recursive) for exactly this reason.
submodules=0
case "${SUBMODULES:-}" in
1 | [Tt][Rr][Uu][Ee]) submodules=1 ;;
esac

clone_args=""
if [ -n "${DEPTH:-}" ]; then
	case "$DEPTH" in
	'' | *[!0-9]*) fail "DEPTH must be a positive integer, got '$DEPTH'" ;;
	esac
	clone_args="$clone_args --depth $DEPTH"
fi

# Clone, then checkout the requested ref. We clone the default branch first
# (works for both shallow and full) then fetch+checkout $REF so tags, branches,
# and raw commit shas are all handled uniformly. Each NETWORK op is wrapped in
# retry() so a transient DNS/network blip on pod start is retried rather than
# failing the whole (BackoffLimit=0) build; permanent errors still fail fast.
# shellcheck disable=SC2086  # clone_args is an intentional word-split arg list.
retry "git clone of $REPO" git clone $clone_args -- "$REPO" "$SRC_DIR" ||
	fail "git clone of $REPO failed"

cd "$SRC_DIR" || fail "cannot enter $SRC_DIR"

# Fetch the specific ref (handles the case where $REF is not the default branch).
# A missing ref here is non-fatal (the checkout below falls back to FETCH_HEAD /
# the cloned default branch), so this fetch is best-effort — retry() returning
# non-zero (permanent or exhausted) is swallowed by `|| true`, but a transient
# network blip is still retried first.
if [ -n "${DEPTH:-}" ]; then
	retry "git fetch $REF" git fetch --depth "$DEPTH" origin "$REF" || true
else
	retry "git fetch $REF" git fetch origin "$REF" || true
fi

git checkout --force "$REF" 2>/dev/null ||
	git checkout --force FETCH_HEAD 2>/dev/null ||
	fail "cannot checkout ref '$REF'"

# Top-level submodules only (--init, NOT --recursive): see the SUBMODULES note
# above. Wrapped in retry() for the same transient-network reason as clone.
if [ "$submodules" = 1 ]; then
	# shellcheck disable=SC2086  # optional --depth arg is an intentional split.
	retry "submodule update" git submodule update --init ${DEPTH:+--depth "$DEPTH"} ||
		fail "submodule update failed"
fi

resolved="$(git rev-parse HEAD 2>/dev/null || echo unknown)"
printf 'clone: checked out %s at %s\n' "$REF" "$resolved" >&2
