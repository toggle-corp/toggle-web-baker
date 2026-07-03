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

# Anonymous fallback: with NO mounted credential, an SSH remote (git@github.com:
# or ssh://git@github.com/) can never authenticate — the pod has no SSH key and
# GIT_ASKPASS only answers https prompts. Rewrite SSH GitHub URLs to https so a
# public repo (or a public submodule of one) declared over SSH still clones
# anonymously. When a credential IS mounted the rewrite is skipped: the operator
# owns the URL scheme in that case. Same GIT_CONFIG_* env mechanism as above.
cred_dir="${GIT_CREDENTIAL_DIR:-/run/git-credential}"
if [ ! -r "$cred_dir/username" ] && [ ! -r "$cred_dir/password" ]; then
	export GIT_CONFIG_COUNT=3
	export GIT_CONFIG_KEY_1='url.https://github.com/.insteadOf'
	export GIT_CONFIG_VALUE_1='git@github.com:'
	export GIT_CONFIG_KEY_2='url.https://github.com/.insteadOf'
	export GIT_CONFIG_VALUE_2='ssh://git@github.com/'
fi

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
# and raw commit shas are all handled uniformly.
# shellcheck disable=SC2086  # clone_args is an intentional word-split arg list.
git clone $clone_args -- "$REPO" "$SRC_DIR" || fail "git clone of $REPO failed"

cd "$SRC_DIR" || fail "cannot enter $SRC_DIR"

# Fetch the specific ref (handles the case where $REF is not the default branch).
if [ -n "${DEPTH:-}" ]; then
	git fetch --depth "$DEPTH" origin "$REF" 2>/dev/null || true
else
	git fetch origin "$REF" 2>/dev/null || true
fi

git checkout --force "$REF" 2>/dev/null ||
	git checkout --force FETCH_HEAD 2>/dev/null ||
	fail "cannot checkout ref '$REF'"

# Top-level submodules only (--init, NOT --recursive): see the SUBMODULES note above.
if [ "$submodules" = 1 ]; then
	git submodule update --init ${DEPTH:+--depth "$DEPTH"} ||
		fail "submodule update failed"
fi

resolved="$(git rev-parse HEAD 2>/dev/null || echo unknown)"
printf 'clone: checked out %s at %s\n' "$REF" "$resolved" >&2
